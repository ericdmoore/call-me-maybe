// Package contentinventory measures the explicitly configured local library.
// It never searches a home directory, reads media bodies, or follows symlinks.
package contentinventory

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"callmemaybe/internal/syncwire"
)

const metadataName = ".content-source.json"
const maxBytes = int64(9_007_199_254_740_991)

type itemMetadata struct {
	Path   string                     `json:"path"`
	Title  string                     `json:"title"`
	Kind   syncwire.ContentItemKind   `json:"kind,omitempty"`
	Origin syncwire.ContentItemOrigin `json:"origin,omitempty"`
}

type metadata struct {
	Schema int                        `json:"schema"`
	Label  string                     `json:"label"`
	Kind   syncwire.ContentItemKind   `json:"kind"`
	Origin syncwire.ContentItemOrigin `json:"origin"`
	Items  []itemMetadata             `json:"items"`
}

// Scanner is owned by the existing sync loop. Disk walks are cached separately
// from the five-second liveness sample and never run in the call daemon.
type Scanner struct {
	Path string
	next time.Time
	last *syncwire.ContentInventory
}

func (s *Scanner) Sample() *syncwire.ContentInventory {
	now := time.Now()
	if s.last == nil || !now.Before(s.next) {
		report := Scan(s.Path)
		s.last = &report
		s.next = now.Add(30 * time.Second)
	}
	return s.last
}

func readMetadata(root *os.Root) (metadata, error) {
	info, err := root.Lstat(metadataName)
	if err != nil {
		return metadata{}, err
	}
	if !info.Mode().IsRegular() {
		return metadata{}, errors.New("inventory: invalid metadata")
	}
	f, err := root.OpenFile(metadataName, readFlags, 0)
	if err != nil {
		return metadata{}, err
	}
	defer f.Close()
	info, err = f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 65536 {
		return metadata{}, errors.New("inventory: invalid metadata")
	}
	decoder := json.NewDecoder(io.LimitReader(f, 65537))
	decoder.DisallowUnknownFields()
	var value metadata
	if err := decoder.Decode(&value); err != nil {
		return metadata{}, err
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF || value.Schema != 1 || len(value.Label) > 64 ||
		!value.Kind.Valid() || !value.Origin.Valid() || len(value.Items) > 128 {
		return metadata{}, errors.New("inventory: invalid metadata")
	}
	seen := map[string]bool{}
	for _, item := range value.Items {
		if item.Path == "" || strings.HasPrefix(item.Path, ".") || strings.ContainsAny(item.Path, `/\`) ||
			len(item.Path) > 128 || len(item.Title) > 200 || seen[item.Path] ||
			(item.Kind != "" && !item.Kind.Valid()) || (item.Origin != "" && !item.Origin.Valid()) {
			return metadata{}, errors.New("inventory: invalid metadata")
		}
		seen[item.Path] = true
	}
	return value, nil
}

type walk struct {
	root     *os.Root
	seen     map[string]bool
	known    int64
	nodes    int
	deadline time.Time
}

func (w *walk) measure(name string, item *syncwire.ContentItem, local map[string]bool, depth int) error {
	w.nodes++
	if w.nodes > 20000 || depth > 64 || time.Now().After(w.deadline) {
		return errors.New("inventory: scan limit")
	}
	info, err := w.root.Lstat(name)
	if err != nil {
		return err
	}
	if !info.IsDir() && !info.Mode().IsRegular() {
		return errors.New("inventory: unsupported entry")
	}
	key, allocated, ok := allocation(info)
	if !ok || allocated < 0 || allocated > maxBytes-w.known {
		return errors.New("inventory: allocation unavailable")
	}
	if !w.seen[key] {
		w.known += allocated
		w.seen[key] = true
	}
	if !local[key] {
		if allocated > maxBytes-*item.AllocatedBytes || info.Size() < 0 || info.Size() > maxBytes-*item.Bytes {
			return errors.New("inventory: size limit")
		}
		*item.AllocatedBytes += allocated
		if info.Mode().IsRegular() {
			*item.Bytes += info.Size()
		}
		local[key] = true
	}
	if !info.IsDir() {
		item.FileCount++
		return nil
	}
	sub, err := w.root.OpenRoot(name)
	if err != nil {
		return err
	}
	defer sub.Close()
	dir, err := sub.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	for {
		children, readErr := dir.ReadDir(128)
		for _, child := range children {
			if err := w.measure(path.Join(name, child.Name()), item, local, depth+1); err != nil {
				return err
			}
		}
		if readErr == io.EOF {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}

// Scan publishes relative names and bounded metadata only. Origin is importer
// provenance from the local source manifest, never evidence of a store entitlement.
func Scan(directory string) syncwire.ContentInventory {
	report := syncwire.ContentInventory{
		SampledAt: time.Now().UnixMilli(), State: "unavailable", Label: "Local library",
		Items: []syncwire.ContentItem{},
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return report
	}
	defer root.Close()
	report.State = "complete"
	disk(directory, &report)
	meta, err := readMetadata(root)
	if err != nil {
		meta = metadata{Kind: "file", Origin: "unknown"}
		if !errors.Is(err, fs.ErrNotExist) {
			report.State = "partial"
		}
	}
	if meta.Label != "" {
		report.Label = meta.Label
	}
	index := map[string]itemMetadata{}
	for _, item := range meta.Items {
		index[item.Path] = item
	}
	dir, err := root.Open(".")
	if err != nil {
		report.State = "unavailable"
		return report
	}
	children, err := dir.ReadDir(130)
	dir.Close()
	if err != nil && err != io.EOF {
		report.State = "unavailable"
		return report
	}
	sort.Slice(children, func(i, j int) bool { return children[i].Name() < children[j].Name() })
	w := walk{root: root, seen: map[string]bool{}, deadline: time.Now().Add(2 * time.Second)}
	for _, child := range children {
		name := child.Name()
		if name == metadataName {
			continue
		}
		if len(report.Items) == 128 || len(name) > 128 {
			report.State = "partial"
			continue
		}
		item := syncwire.ContentItem{
			Id: name, Path: name, Title: name, Kind: meta.Kind, Origin: meta.Origin, State: "stored",
			Bytes: new(int64), AllocatedBytes: new(int64),
		}
		if value, ok := index[name]; ok {
			if value.Title != "" {
				item.Title = value.Title
			}
			if value.Kind != "" {
				item.Kind = value.Kind
			}
			if value.Origin != "" {
				item.Origin = value.Origin
			}
		}
		if err := w.measure(name, &item, map[string]bool{}, 0); err != nil {
			item.State = "unreadable"
			item.Bytes, item.AllocatedBytes = nil, nil
			report.State = "partial"
		}
		report.Items = append(report.Items, item)
	}
	if len(children) == 130 {
		report.State = "partial"
	}
	// Include the library directory and its provenance file in total disk use,
	// while keeping that bookkeeping out of the content list.
	for _, name := range []string{".", metadataName} {
		if info, err := root.Lstat(name); err == nil {
			if key, allocated, ok := allocation(info); ok && !w.seen[key] && allocated <= maxBytes-w.known {
				w.known += allocated
				w.seen[key] = true
			}
		}
	}
	report.KnownAllocatedBytes = w.known
	if report.State == "complete" {
		report.AllocatedBytes = &report.KnownAllocatedBytes
	}
	return report
}
