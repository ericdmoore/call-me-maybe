// Package backup is the house as one sealed file, and a new box from it
// (s27, issue #29). A bundle is a tar of files at their absolute paths,
// gzip-compressed, encrypted with age to a recipient whose private half the
// box never holds — so the box cannot read its own backups, and neither can
// any destination. MANIFEST.json is the first entry: what the bundle is,
// from which box and version, and a hash of every file, so a restore can
// say what it is holding before it touches anything.
//
// Nothing here runs on a call path; `doorman backup` and `doorman restore`
// are the only callers (asserted by test in cmd/doorman).
package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"filippo.io/age"
	_ "modernc.org/sqlite"
)

// FormatVersion changes when a restore of an older bundle would need to
// know something new. Bumped deliberately; never silently.
const FormatVersion = 1

// Manifest is the first entry of every bundle.
type Manifest struct {
	Format    int       `json:"format"`
	Doorman   string    `json:"doorman"` // the version that wrote it
	Host      string    `json:"host"`
	CreatedAt time.Time `json:"created_at"`
	Files     []File    `json:"files"`
	// Excluded says what was left out and why ("voicemail: --no-voicemail"),
	// so a restore does not go looking for it.
	Excluded []string `json:"excluded,omitempty"`
}

// File is one entry: where it lives, how big, its hash, and which tier it
// belongs to — "house" (irreplaceable), "state", "voicemail", "spool",
// "asterisk" (a hand-written Asterisk file).
type File struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
	Mode   uint32 `json:"mode"`
	Tier   string `json:"tier"`
	// Note is "snapshot" for the journal (copied via VACUUM INTO) or
	// "customised" for an Asterisk file that differs from what shipped.
	Note string `json:"note,omitempty"`
	// Owner and Group are names, not ids: a restore on a fresh box looks
	// them up, and ids differ between installs. Empty when unknown.
	Owner string `json:"owner,omitempty"`
	Group string `json:"group,omitempty"`
}

// Sources is what to collect. Paths that do not exist are skipped, not
// errors: a box with no contacts.toml has nothing to back up there.
type Sources struct {
	Config    []string // .env and the TOMLs
	StateDir  string   // /var/lib/doorman (journal, provision, inbox, phonebook)
	Journal   string   // the SQLite journal inside StateDir; snapshotted, not copied
	Voicemail string   // /var/spool/asterisk/voicemail
	Spool     string   // /var/spool/call-me-maybe
	Asterisk  []string // hand-written /etc/asterisk files
	// Shipped, when set, answers "is this Asterisk file what the binary
	// shipped": a matching file is left out (render and the installer
	// reproduce it); a differing one is in, marked customised. Nil means
	// every listed file is in.
	Shipped     func(path string, content []byte) (same bool, known bool)
	NoVoicemail bool
	// Exclude are directories never walked, however they fall inside the
	// others: the file destination when it lives under the state directory,
	// or a bundle would carry every bundle before it.
	Exclude []string
}

// Item is one collected file in memory. Bundles are small — a house is a
// few megabytes — so the whole thing is held rather than streamed.
type Item struct {
	File
	Content []byte
}

// Collect gathers the sources into items plus a manifest. It reads the
// host and writes nothing but a temporary journal snapshot.
func Collect(src Sources, doormanVersion, host string, now time.Time) ([]Item, *Manifest, error) {
	m := &Manifest{Format: FormatVersion, Doorman: doormanVersion, Host: host, CreatedAt: now.UTC()}
	var items []Item
	excluded := func(path string) bool {
		abs, err := filepath.Abs(path)
		if err != nil {
			abs = path
		}
		for _, ex := range src.Exclude {
			exAbs, err := filepath.Abs(ex)
			if err != nil {
				exAbs = ex
			}
			if abs == exAbs || strings.HasPrefix(abs, exAbs+string(filepath.Separator)) {
				return true
			}
		}
		return false
	}
	add := func(path, tier, note string, content []byte, info fs.FileInfo) {
		// Absolute, always: a bundle is restored from wherever the operator
		// happens to be standing, and "handsets.toml" must mean the one in
		// /opt/call-me-maybe, not the current directory.
		if abs, err := filepath.Abs(path); err == nil {
			path = abs
		}
		sum := sha256.Sum256(content)
		mode := fs.FileMode(0o600)
		if info != nil {
			mode = info.Mode()
		}
		owner, group := ownerOf(info)
		f := File{Path: path, Size: int64(len(content)), SHA256: hex.EncodeToString(sum[:]), Mode: uint32(mode.Perm()), Tier: tier, Note: note, Owner: owner, Group: group}
		items = append(items, Item{File: f, Content: content})
		m.Files = append(m.Files, f)
	}
	readInto := func(path, tier string) error {
		content, info, err := readFile(path)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		add(path, tier, "", content, info)
		return nil
	}

	for _, p := range src.Config {
		if err := readInto(p, "house"); err != nil {
			return nil, nil, err
		}
	}
	for _, p := range src.Asterisk {
		content, info, err := readFile(p)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if errors.Is(err, fs.ErrPermission) {
			// Asterisk's files are root:asterisk; the backup runs as doorman.
			// `init services` grants the read ACL; until it has, say so
			// rather than fail the whole bundle over a reproducible file.
			m.Excluded = append(m.Excluded, p+": unreadable by this user (sudo doorman init services grants it)")
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		note := ""
		if src.Shipped != nil {
			same, known := src.Shipped(p, content)
			if known && same {
				m.Excluded = append(m.Excluded, p+": identical to what this version shipped")
				continue
			}
			if known {
				note = "customised"
			}
		}
		add(p, "asterisk", note, content, info)
	}
	if src.StateDir != "" {
		err := filepath.WalkDir(src.StateDir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return nil
				}
				return err
			}
			if d.IsDir() {
				if excluded(path) {
					return filepath.SkipDir
				}
				return nil
			}
			if excluded(path) {
				return nil
			}
			// The journal and its WAL/SHM are replaced by one snapshot.
			if src.Journal != "" && strings.HasPrefix(path, src.Journal) {
				return nil
			}
			content, info, err := readFile(path)
			if err != nil {
				return err
			}
			add(path, "state", "", content, info)
			return nil
		})
		if err != nil {
			return nil, nil, err
		}
	}
	if src.Journal != "" {
		if _, err := os.Stat(src.Journal); err == nil {
			content, err := SnapshotSQLite(src.Journal)
			if err != nil {
				return nil, nil, fmt.Errorf("journal snapshot: %w", err)
			}
			// The snapshot takes the journal's own owner and mode.
			info, _ := os.Stat(src.Journal)
			add(src.Journal, "state", "snapshot", content, info)
		}
	}
	if src.Voicemail != "" {
		if src.NoVoicemail {
			m.Excluded = append(m.Excluded, src.Voicemail+": --no-voicemail")
		} else if err := walkInto(src.Voicemail, "voicemail", add, excluded); errors.Is(err, fs.ErrPermission) {
			m.Excluded = append(m.Excluded, src.Voicemail+": unreadable by this user (sudo doorman init services grants it)")
		} else if err != nil {
			return nil, nil, err
		}
	}
	if src.Spool != "" {
		if err := walkInto(src.Spool, "spool", add, excluded); err != nil {
			return nil, nil, err
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Path < items[j].Path })
	sort.Slice(m.Files, func(i, j int) bool { return m.Files[i].Path < m.Files[j].Path })
	return items, m, nil
}

func walkInto(root, tier string, add func(path, tier, note string, content []byte, info fs.FileInfo), excluded func(string) bool) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			if excluded(path) {
				return filepath.SkipDir
			}
			return nil
		}
		if excluded(path) {
			return nil
		}
		content, info, err := readFile(path)
		if err != nil {
			return err
		}
		add(path, tier, "", content, info)
		return nil
	})
}

// ownerOf names a file's owner and group, "" where the platform or the
// lookup cannot say. Names travel; ids do not.
func ownerOf(info fs.FileInfo) (owner, group string) {
	if info == nil {
		return "", ""
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", ""
	}
	if u, err := user.LookupId(strconv.FormatUint(uint64(st.Uid), 10)); err == nil {
		owner = u.Username
	}
	if g, err := user.LookupGroupId(strconv.FormatUint(uint64(st.Gid), 10)); err == nil {
		group = g.Name
	}
	return owner, group
}

func readFile(path string) ([]byte, fs.FileInfo, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, nil, fs.ErrNotExist
	}
	content, err := os.ReadFile(path)
	return content, info, err
}

// SnapshotSQLite returns a consistent copy of a live SQLite database: a
// read-only connection and VACUUM INTO a temporary file, which is the
// engine's own way of copying under concurrent writers. The daemon keeps
// writing; the copy is as of one transaction.
func SnapshotSQLite(path string) ([]byte, error) {
	tmp, err := os.CreateTemp("", "doorman-journal-*.db")
	if err != nil {
		return nil, err
	}
	tmpPath := tmp.Name()
	_ = tmp.Close()
	_ = os.Remove(tmpPath) // VACUUM INTO refuses to overwrite
	defer os.Remove(tmpPath)
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	if _, err := db.Exec("VACUUM INTO ?", tmpPath); err != nil {
		return nil, err
	}
	return os.ReadFile(tmpPath)
}

// Name is the bundle's file name: the host and the time, sortable.
func Name(host string, at time.Time) string {
	return fmt.Sprintf("callmemaybe-%s-%s.age", host, at.UTC().Format("20060102T150405Z"))
}

// Bundle writes items and their manifest as tar → gzip → age to w.
func Bundle(w io.Writer, m *Manifest, items []Item, recipient string) error {
	rcpt, err := age.ParseX25519Recipient(strings.TrimSpace(recipient))
	if err != nil {
		return fmt.Errorf("BACKUP_RECIPIENT: %w", err)
	}
	enc, err := age.Encrypt(w, rcpt)
	if err != nil {
		return err
	}
	gz := gzip.NewWriter(enc)
	tw := tar.NewWriter(gz)
	manifest, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := writeEntry(tw, "MANIFEST.json", 0o600, manifest, m.CreatedAt); err != nil {
		return err
	}
	for _, it := range items {
		if err := writeEntry(tw, it.Path, fs.FileMode(it.Mode), it.Content, m.CreatedAt); err != nil {
			return err
		}
	}
	for _, c := range []io.Closer{tw, gz, enc} {
		if err := c.Close(); err != nil {
			return err
		}
	}
	return nil
}

func writeEntry(tw *tar.Writer, name string, mode fs.FileMode, content []byte, at time.Time) error {
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: int64(mode.Perm()), Size: int64(len(content)), ModTime: at, Typeflag: tar.TypeReg}); err != nil {
		return err
	}
	_, err := tw.Write(content)
	return err
}

// Open decrypts a bundle and returns its manifest and items, verifying
// every hash. A bundle whose files do not match their manifest is refused
// whole: a restore of half a house is worse than none.
func Open(r io.Reader, identity string) (*Manifest, []Item, error) {
	id, err := age.ParseX25519Identity(strings.TrimSpace(identity))
	if err != nil {
		return nil, nil, fmt.Errorf("identity: %w", err)
	}
	dec, err := age.Decrypt(r, id)
	if err != nil {
		return nil, nil, fmt.Errorf("decrypt: %w", err)
	}
	gz, err := gzip.NewReader(dec)
	if err != nil {
		return nil, nil, err
	}
	tr := tar.NewReader(gz)
	var m *Manifest
	var items []Item
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, nil, err
		}
		content, err := io.ReadAll(tr)
		if err != nil {
			return nil, nil, err
		}
		if h.Name == "MANIFEST.json" {
			m = &Manifest{}
			if err := json.Unmarshal(content, m); err != nil {
				return nil, nil, fmt.Errorf("manifest: %w", err)
			}
			if m.Format > FormatVersion {
				return nil, nil, fmt.Errorf("bundle format %d is newer than this doorman understands (%d)", m.Format, FormatVersion)
			}
			continue
		}
		items = append(items, Item{File: File{Path: h.Name, Size: h.Size, Mode: uint32(h.Mode)}, Content: content})
	}
	if m == nil {
		return nil, nil, errors.New("not a doorman bundle: no manifest")
	}
	want := map[string]File{}
	for _, f := range m.Files {
		want[f.Path] = f
	}
	if len(items) != len(want) {
		return nil, nil, fmt.Errorf("bundle holds %d files, manifest lists %d", len(items), len(want))
	}
	for i := range items {
		f, ok := want[items[i].Path]
		if !ok {
			return nil, nil, fmt.Errorf("%s is in the bundle but not its manifest", items[i].Path)
		}
		sum := sha256.Sum256(items[i].Content)
		if hex.EncodeToString(sum[:]) != f.SHA256 {
			return nil, nil, fmt.Errorf("%s does not match its manifest hash", items[i].Path)
		}
		items[i].File = f
	}
	return m, items, nil
}

// ReadManifest returns only the manifest — what `restore --dry-run` and
// `backup verify` need — still decrypting, since the manifest is inside.
func ReadManifest(r io.Reader, identity string) (*Manifest, error) {
	m, _, err := Open(r, identity)
	return m, err
}

// NewKey makes the keypair: the recipient (public) for .env, the identity
// (private) printed once and kept by the operator.
func NewKey() (recipient, identity string, err error) {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		return "", "", err
	}
	return id.Recipient().String(), id.String(), nil
}

// Place writes items to their paths, creating directories, keeping modes
// and — when running as root, which a real restore is — owners, by name,
// so .env comes back doorman's and /etc/asterisk root's. A name the new
// box does not know is reported, not fatal: the file is placed, the
// operator is told. The journal snapshot lands at the journal's path; the
// daemon must not be running (restore checks). Returns the paths written
// and the owners it could not set.
func Place(items []Item, root string) (written []string, unowned []string, err error) {
	asRoot := os.Geteuid() == 0
	ids := map[string]int{}
	lookup := func(name string, group bool) (int, bool) {
		key := name
		if group {
			key = "group:" + name
		}
		if id, ok := ids[key]; ok {
			return id, id >= 0
		}
		id := -1
		if group {
			if g, err := user.LookupGroup(name); err == nil {
				id, _ = strconv.Atoi(g.Gid)
			}
		} else if u, err := user.Lookup(name); err == nil {
			id, _ = strconv.Atoi(u.Uid)
		}
		ids[key] = id
		return id, id >= 0
	}
	for _, it := range items {
		path := it.Path
		if root != "" {
			path = filepath.Join(root, it.Path)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			return written, unowned, err
		}
		mode := fs.FileMode(it.Mode)
		if mode == 0 {
			mode = 0o600
		}
		if err := os.WriteFile(path, it.Content, mode); err != nil {
			return written, unowned, err
		}
		if err := os.Chmod(path, mode); err != nil { // WriteFile honours umask; the bundle's mode wins
			return written, unowned, err
		}
		written = append(written, path)
		if !asRoot || it.Owner == "" {
			continue
		}
		uid, okU := lookup(it.Owner, false)
		gid, okG := -1, true
		if it.Group != "" {
			gid, okG = lookup(it.Group, true)
		}
		if !okU || !okG {
			unowned = append(unowned, path+" ("+it.Owner+":"+it.Group+")")
			continue
		}
		if err := os.Chown(path, uid, gid); err != nil {
			unowned = append(unowned, path+": "+err.Error())
		}
		// Directories Place created under a doorman-owned tree must be
		// doorman's too, or the daemon cannot write beside the file.
		dir := filepath.Dir(path)
		if info, err := os.Stat(dir); err == nil && info.Sys() != nil {
			if st, ok := info.Sys().(*syscall.Stat_t); ok && int(st.Uid) == 0 && uid != 0 && strings.Contains(path, "/var/lib/doorman") {
				_ = os.Chown(dir, uid, gid)
			}
		}
	}
	return written, unowned, nil
}

// Summary is the manifest in one line, for list and dry runs.
func (m *Manifest) Summary() string {
	var total int64
	tiers := map[string]int{}
	for _, f := range m.Files {
		total += f.Size
		tiers[f.Tier]++
	}
	var parts []string
	for _, t := range []string{"house", "asterisk", "state", "voicemail", "spool"} {
		if n := tiers[t]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, t))
		}
	}
	return fmt.Sprintf("%s · doorman %s · %s · %d files, %s (%s)", m.Host, m.Doorman, m.CreatedAt.Format(time.RFC3339), len(m.Files), human(total), strings.Join(parts, ", "))
}

func human(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

var _ = bytes.Equal
