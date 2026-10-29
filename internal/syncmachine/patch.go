package syncmachine

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/BurntSushi/toml"
	"github.com/pelletier/go-toml/v2/unstable"
)

type span struct{ start, end int }
type table struct {
	id     string
	end    int
	fields map[string]span
}

// patchTable changes values in one array table, preserving comments, ordering
// and every byte outside those values. TOML's parser finds spans; text matching
// would mistake a header inside a multiline string for an actual table.
func patchTable(data []byte, section, id string, values map[string]any) ([]byte, error) {
	var parser unstable.Parser
	parser.Reset(data)
	var current *table
	var tables []*table
	if section == "" {
		current = &table{end: len(data), fields: map[string]span{}}
		tables = append(tables, current)
	}
	for parser.NextExpression() {
		node := parser.Expression()
		keys := []string{}
		keyEnd, keyStart := 0, 0
		it := node.Key()
		for it.Next() {
			k := it.Node()
			if len(keys) == 0 {
				keyStart = int(k.Raw.Offset)
			}
			keys = append(keys, string(k.Data))
			keyEnd = int(k.Raw.Offset + k.Raw.Length)
		}
		if node.Kind == unstable.Table || node.Kind == unstable.ArrayTable {
			start := bytes.LastIndexByte(data[:keyStart], '\n') + 1
			if current != nil {
				current.end = start
			}
			current = nil
			if node.Kind == unstable.ArrayTable && len(keys) == 1 && keys[0] == section {
				current = &table{end: len(data), fields: map[string]span{}}
				tables = append(tables, current)
			}
			continue
		}
		if node.Kind != unstable.KeyValue || current == nil || len(keys) != 1 {
			continue
		}
		if keys[0] == "id" {
			current.id = string(node.Value().Data)
		}
		end := int(node.Raw.Offset + node.Raw.Length)
		start := keyEnd
		for start < end && data[start] != '=' {
			start++
		}
		start++
		for start < end && (data[start] == ' ' || data[start] == '\t') {
			start++
		}
		if start > end {
			return nil, ErrFiles
		}
		current.fields[keys[0]] = span{start, end}
	}
	if parser.Error() != nil {
		return nil, ErrFiles
	}
	var target *table
	for _, t := range tables {
		if t.id == id {
			target = t
			break
		}
	}
	// Only schedules may be created here. Handset inventory must already exist.
	if target == nil && section != "schedules" {
		return nil, ErrFiles
	}
	type edit struct {
		span
		value []byte
	}
	edits := []edit{}
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var additions bytes.Buffer
	for _, k := range keys {
		var encoded bytes.Buffer
		if toml.NewEncoder(&encoded).Encode(map[string]any{k: values[k]}) != nil {
			return nil, ErrFiles
		}
		line := encoded.Bytes()
		eq := bytes.IndexByte(line, '=')
		if eq < 0 {
			return nil, ErrFiles
		}
		value := bytes.TrimSpace(line[eq+1:])
		if target != nil {
			if field, ok := target.fields[k]; ok {
				edits = append(edits, edit{field, append([]byte{}, value...)})
				continue
			}
		}
		additions.Write(line)
	}
	if target == nil {
		var header bytes.Buffer
		fmt.Fprintf(&header, "\n[[%s]]\n", section)
		if toml.NewEncoder(&header).Encode(map[string]any{"id": id}) != nil {
			return nil, ErrFiles
		}
		header.Write(additions.Bytes())
		edits = append(edits, edit{span{len(data), len(data)}, header.Bytes()})
	} else if additions.Len() > 0 {
		edits = append(edits, edit{span{target.end, target.end}, append([]byte("\n"), additions.Bytes()...)})
	}
	sort.Slice(edits, func(i, j int) bool { return edits[i].start > edits[j].start })
	result := append([]byte{}, data...)
	for _, e := range edits {
		result = append(append(append([]byte{}, result[:e.start]...), e.value...), result[e.end:]...)
	}
	return result, nil
}

// atomicReplace reports whether the rename happened. Every failure before it
// leaves the target exactly as it was; a failure after it (the directory fsync)
// leaves the new bytes in place without a durability guarantee.
func atomicReplace(path string, before, after []byte) (renamed bool, err error) {
	return atomicReplaceMode(path, before, after, false)
}

func atomicReplaceMode(path string, before, after []byte, private bool, readable ...bool) (bool, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false, ErrFiles
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".sync-*")
	if err != nil {
		return false, ErrFiles
	}
	defer os.Remove(f.Name())
	defer f.Close()
	mode := info.Mode().Perm()
	if len(readable) > 0 && readable[0] {
		mode = 0o644
	}
	if private {
		mode = 0o600
	}
	if err = f.Chmod(mode); err != nil {
		return false, ErrFiles
	}
	// An operator running as root must not leave a doorman-owned file root-only.
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && os.Geteuid() == 0 {
		if f.Chown(int(stat.Uid), int(stat.Gid)) != nil {
			return false, ErrFiles
		}
	}
	if _, err = f.Write(after); err != nil || f.Sync() != nil || f.Close() != nil {
		return false, ErrFiles
	}
	current, err := readFile(path)
	if err != nil || !bytes.Equal(before, current) {
		return false, ErrChanged
	}
	if os.Rename(f.Name(), path) != nil {
		return false, ErrFiles
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return true, ErrFiles
	}
	defer dir.Close()
	if dir.Sync() != nil {
		return true, ErrFiles
	}
	return true, nil
}

// atomicCreate writes a file that must not exist yet: a hard link from a
// synced temporary file, which fails rather than replacing anything that
// appeared meanwhile. created reports whether the file now exists.
func atomicCreate(path string, after []byte) (created bool, err error) {
	return atomicCreateMode(path, after, false)
}

func atomicCreateMode(path string, after []byte, private bool, readable ...bool) (bool, error) {
	dir := filepath.Dir(path)
	parent, err := os.Stat(filepath.Dir(dir))
	if err != nil || !parent.IsDir() {
		return false, ErrFiles
	}
	if _, err := os.Lstat(dir); errors.Is(err, os.ErrNotExist) {
		if os.Mkdir(dir, 0o750) != nil || chownLike(dir, parent) != nil {
			return false, ErrFiles
		}
	}
	f, err := os.CreateTemp(dir, ".sync-*")
	if err != nil {
		return false, ErrFiles
	}
	defer os.Remove(f.Name())
	defer f.Close()
	mode := os.FileMode(0o640)
	if len(readable) > 0 && readable[0] {
		mode = 0o644
	}
	if private {
		mode = 0o600
		parent, err = os.Stat(dir)
		if err != nil {
			return false, ErrFiles
		}
	}
	if f.Chmod(mode) != nil || chownLike(f.Name(), parent) != nil {
		return false, ErrFiles
	}
	if _, err = f.Write(after); err != nil || f.Sync() != nil || f.Close() != nil {
		return false, ErrFiles
	}
	if err := os.Link(f.Name(), path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return false, ErrChanged
		}
		return false, ErrFiles
	}
	return true, syncDir(dir)
}

// atomicRemove deletes a file only while it still holds exactly before.
// removed reports whether it is gone.
func atomicRemove(path string, before []byte) (removed bool, err error) {
	current, err := readFile(path)
	if err != nil || !bytes.Equal(before, current) {
		return false, ErrChanged
	}
	if os.Remove(path) != nil {
		return false, ErrFiles
	}
	return true, syncDir(filepath.Dir(path))
}

// chownLike gives a file the owner of the directory it serves: an operator
// running sync as root must not leave the directory's reader locked out.
func chownLike(path string, like os.FileInfo) error {
	if stat, ok := like.Sys().(*syscall.Stat_t); ok && os.Geteuid() == 0 {
		return os.Chown(path, int(stat.Uid), int(stat.Gid))
	}
	return nil
}

func syncDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return ErrFiles
	}
	defer dir.Close()
	if dir.Sync() != nil {
		return ErrFiles
	}
	return nil
}

func verbName(data []byte) string {
	// A decoded Verb has already passed the closed OpenAPI boundary.
	var v struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(data, &v)
	return strings.TrimSpace(v.Name)
}
