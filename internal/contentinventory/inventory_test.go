//go:build linux || darwin

package contentinventory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"callmemaybe/internal/syncwire"
)

func write(t *testing.T, root, name, data string) {
	t.Helper()
	target := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestRealFilesAndImporterProvenance(t *testing.T) {
	root := t.TempDir()
	write(t, root, metadataName, `{"schema":1,"label":"Test box","kind":"audiobook","origin":"cli","items":[{"path":"first","title":"First book"},{"path":"second","title":"Second book","origin":"store","kind":"interactive"}]}`)
	write(t, root, "first/chapter-01.mp3", strings.Repeat("a", 5000))
	write(t, root, "first/chapter-02.mp3", strings.Repeat("b", 6000))
	write(t, root, "second/opening.mp3", "audio")
	report := Scan(root)
	if report.State != "complete" || report.Label != "Test box" || len(report.Items) != 2 {
		t.Fatalf("unexpected report: %+v", report)
	}
	first, second := report.Items[0], report.Items[1]
	if first.Title != "First book" || first.Origin != "cli" || first.State != "stored" ||
		first.FileCount != 2 || *first.Bytes != 11000 || first.AllocatedBytes == nil {
		t.Fatalf("first: %+v", first)
	}
	if second.Origin != "store" || second.Kind != "interactive" {
		t.Fatalf("second: %+v", second)
	}
	if report.AllocatedBytes == nil || *report.AllocatedBytes != report.KnownAllocatedBytes ||
		report.CapacityBytes == nil || report.UsedBytes == nil || report.AvailableBytes == nil ||
		report.ReservedBytes == nil || *report.CapacityBytes != *report.UsedBytes+*report.AvailableBytes+*report.ReservedBytes {
		t.Fatalf("disk accounting: %+v", report)
	}
	data, err := json.Marshal(report)
	if err != nil || strings.Contains(string(data), root) {
		t.Fatalf("absolute paths must not enter reports: %v", err)
	}
	var decoded syncwire.ContentInventory
	if err := syncwire.Decode("ContentInventory", data, &decoded); err != nil {
		t.Fatal(err)
	}
}

func TestHardLinksCountOnceAcrossLibrary(t *testing.T) {
	root := t.TempDir()
	write(t, root, "one.mp3", strings.Repeat("a", 9000))
	before := Scan(root)
	if err := os.Link(filepath.Join(root, "one.mp3"), filepath.Join(root, "two.mp3")); err != nil {
		t.Fatal(err)
	}
	after := Scan(root)
	if len(after.Items) != 2 || after.Items[1].Origin != "unknown" {
		t.Fatalf("hardlink: %+v", after)
	}
	// Directory allocation may change, but the second reference must not add
	// another file's physical allocation to the total.
	info, _ := os.Stat(root)
	_, rootBlocks, _ := allocation(info)
	if after.KnownAllocatedBytes != rootBlocks+*before.Items[0].AllocatedBytes {
		t.Fatalf("shared file counted twice: %+v", after)
	}
}

func TestUnknownMissingAndUnsafeEntries(t *testing.T) {
	missing := Scan(filepath.Join(t.TempDir(), "missing"))
	if missing.State != "unavailable" || missing.AllocatedBytes != nil || len(missing.Items) != 0 {
		t.Fatalf("missing became empty: %+v", missing)
	}
	root := t.TempDir()
	outside := t.TempDir()
	write(t, outside, "private.txt", strings.Repeat("secret", 500))
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	report := Scan(root)
	if report.State != "partial" || report.AllocatedBytes != nil ||
		report.Items[0].State != "unreadable" || report.Items[0].Bytes != nil {
		t.Fatalf("link followed: %+v", report)
	}
	if report.Items[0].Origin != "unknown" || report.Items[0].FileCount != 0 {
		t.Fatalf("guessed provenance or read outside root: %+v", report)
	}
}

func TestBadMetadataNeverAssignsStoreOrigin(t *testing.T) {
	for _, data := range []string{
		`{"schema":1,"kind":"file","origin":"store","items":[{"path":"../outside","title":"bad"}]}`,
		`{"schema":1,"kind":"file","origin":"store","items":[{"path":"one","title":"a"},{"path":"one","title":"b"}]}`,
		`{"schema":1,"kind":"file","origin":"store","token":"do-not-read"}`,
		`{"schema":2,"kind":"file","origin":"store"}`,
		`{"schema":1,"kind":"bad","origin":"store"}`,
		`{"schema":1,"kind":"file","origin":"bad"}`,
		`{"schema":1,"kind":"file","origin":"cli"} {}`,
		strings.Repeat("x", 65537),
	} {
		t.Run(data[:min(len(data), 60)], func(t *testing.T) {
			root := t.TempDir()
			write(t, root, "one.mp3", "test")
			write(t, root, metadataName, data)
			report := Scan(root)
			if report.State != "partial" || report.Items[0].Origin != "unknown" || report.AllocatedBytes != nil {
				t.Fatalf("malformed metadata trusted: %+v", report)
			}
		})
	}
	root := t.TempDir()
	write(t, root, "one.mp3", "test")
	if err := syscall.Mkfifo(filepath.Join(root, metadataName), 0600); err != nil {
		t.Fatal(err)
	}
	if report := Scan(root); report.State != "partial" {
		t.Fatalf("FIFO metadata accepted: %+v", report)
	}
}

func TestBoundedReportsAndCachedRefresh(t *testing.T) {
	root := t.TempDir()
	write(t, root, "first.mp3", "one")
	scanner := Scanner{Path: root}
	first := scanner.Sample()
	write(t, root, "second.mp3", "two")
	if scanner.Sample() != first || len(first.Items) != 1 {
		t.Fatal("cache rescanned before its deadline")
	}
	scanner.next = time.Time{}
	if len(scanner.Sample().Items) != 2 {
		t.Fatal("new content did not enter the next report")
	}
	for i := 0; i < 130; i++ {
		write(t, root, string(rune('a'+i))+".mp3", "data")
	}
	report := Scan(root)
	if report.State != "partial" || len(report.Items) > 128 || report.AllocatedBytes != nil {
		t.Fatalf("unbounded inventory: %+v", report)
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	for _, limited := range []walk{
		{root: r, seen: map[string]bool{}, deadline: time.Now().Add(-time.Second)},
		{root: r, seen: map[string]bool{}, deadline: time.Now().Add(time.Second), nodes: 20000},
	} {
		item := syncwire.ContentItem{Bytes: new(int64), AllocatedBytes: new(int64)}
		if limited.measure("first.mp3", &item, map[string]bool{}, 0) == nil {
			t.Fatal("scan budget ignored")
		}
	}
}
