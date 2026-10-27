// Package contentinventory measures the explicitly configured local library.
// It never searches a home directory, reads media bodies, or follows symlinks.
package contentinventory

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"callmemaybe/internal/syncwire"
)

const metadataName = ".content-source.json"
const maxBytes = int64(9_007_199_254_740_991)
const maxItems = 128
const maxNodes = 20000

// errBudget means the walk stopped because the shared node, depth or time
// budget ran out, not because the entry could not be read. The server's item
// state is a closed enum with no "not scanned", so such items stay stored and
// simply carry no sizes.
var errBudget = errors.New("inventory: scan limit")

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

// Scanner keeps the last completed scan for the sync loop. Run walks the disk
// on its own goroutine so a slow or hung mount can only make the inventory
// stale; Sample never waits on the filesystem and is safe from any goroutine.
type Scanner struct {
	Path     string
	Interval time.Duration // zero means 30 seconds
	last     atomic.Pointer[syncwire.ContentInventory]
}

// Sample returns the last completed report, or nil before the first scan has
// finished; a status without contentInventory is valid on the wire.
func (s *Scanner) Sample() *syncwire.ContentInventory { return s.last.Load() }

// Refresh scans now and publishes the result.
func (s *Scanner) Refresh() {
	report := Scan(s.Path)
	s.last.Store(&report)
}

// Run refreshes immediately and then on the interval until ctx ends. A scan
// already blocked in a syscall finishes before Run notices cancellation, which
// is why callers must not wait for Run to return before shutting down.
func (s *Scanner) Run(ctx context.Context) {
	interval := s.Interval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	for ctx.Err() == nil {
		s.Refresh()
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
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
	if w.nodes > maxNodes || depth > 64 || time.Now().After(w.deadline) {
		return errBudget
	}
	info, err := w.root.Lstat(name)
	if err != nil {
		return err
	}
	if !info.IsDir() && !info.Mode().IsRegular() {
		// Inside an item, a symlink, socket or lost+found's special files are
		// not content: leave them out rather than lose the item's sizes. A
		// top-level one is the item itself, which cannot be measured.
		if depth > 0 {
			return nil
		}
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
	names, complete, ok := listNames(root)
	if !ok {
		report.State = "unavailable"
		return report
	}
	if !complete {
		report.State = "partial"
	}
	w := walk{root: root, seen: map[string]bool{}, deadline: time.Now().Add(2 * time.Second)}
	for _, name := range names {
		if name == metadataName {
			continue
		}
		// A name that is not UTF-8 would reach JSON as U+FFFD, so two distinct
		// files could share an id the server refuses. Skipping is simpler and
		// safer than a reversible encoding nobody can read as a title.
		if len(report.Items) == maxItems || len(name) > 128 || !utf8.ValidString(name) {
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
			if !errors.Is(err, errBudget) {
				item.State = "unreadable"
			}
			// A partial count beside missing byte totals reads as a measurement.
			item.FileCount = 0
			item.Bytes, item.AllocatedBytes = nil, nil
			report.State = "partial"
		}
		report.Items = append(report.Items, item)
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
	return conform(report)
}

// listNames reads every top-level name up to the node budget and sorts them,
// so the first 128 items are the same set whatever order readdir returns.
// complete is false when the budget cut the listing short.
func listNames(root *os.Root) (names []string, complete, ok bool) {
	dir, err := root.Open(".")
	if err != nil {
		return nil, false, false
	}
	defer dir.Close()
	for len(names) <= maxNodes {
		batch, err := dir.Readdirnames(256)
		names = append(names, batch...)
		if err == io.EOF {
			sort.Strings(names)
			return names, true, true
		}
		if err != nil {
			return nil, false, false
		}
	}
	sort.Strings(names)
	return names, false, true
}

// conform holds the report to the server's schema before it leaves the box:
// one item the server would refuse makes it refuse the whole status, which
// also stops configuration sync. Offending items are dropped and the report
// becomes partial; if what remains still fails, only the bare report is sent.
func conform(report syncwire.ContentInventory) syncwire.ContentInventory {
	seen := map[string]bool{}
	kept := report.Items[:0:0]
	for _, item := range report.Items {
		data, err := json.Marshal(item)
		if len(kept) == maxItems || seen[item.Id] || err != nil ||
			!utf8.ValidString(item.Id) || !utf8.ValidString(item.Path) || !utf8.ValidString(item.Title) ||
			syncwire.Decode("ContentItem", data, &syncwire.ContentItem{}) != nil {
			report.State = "partial"
			continue
		}
		seen[item.Id] = true
		kept = append(kept, item)
	}
	report.Items = kept
	if report.State != "complete" {
		report.AllocatedBytes = nil
	}
	if data, err := json.Marshal(report); err != nil || !utf8.ValidString(report.Label) ||
		syncwire.Decode("ContentInventory", data, &syncwire.ContentInventory{}) != nil {
		return syncwire.ContentInventory{
			SampledAt: report.SampledAt, State: "partial", Label: "Local library", Items: []syncwire.ContentItem{},
		}
	}
	return report
}
