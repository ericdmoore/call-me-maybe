//go:build linux || darwin

package contentinventory

import (
	"context"
	"encoding/json"
	"fmt"
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
	if scanner.Sample() != nil {
		t.Fatal("a report existed before any scan")
	}
	scanner.Refresh()
	first := scanner.Sample()
	write(t, root, "second.mp3", "two")
	if scanner.Sample() != first || len(first.Items) != 1 {
		t.Fatal("Sample walked the disk instead of returning the cached report")
	}
	scanner.Refresh()
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

func TestRunPublishesInBackgroundAndStopsWithContext(t *testing.T) {
	root := t.TempDir()
	write(t, root, "first.mp3", "one")
	scanner := &Scanner{Path: root, Interval: time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); scanner.Run(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		// Readers on another goroutine are the sync exchange; -race checks them.
		if report := scanner.Sample(); report != nil && len(report.Items) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no background report")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run ignored cancellation")
	}
	idle := &Scanner{Path: root}
	stopped, stop := context.WithCancel(context.Background())
	stop()
	idle.Run(stopped)
	if idle.Sample() != nil {
		t.Fatal("a cancelled Run still scanned")
	}
}

func TestBudgetExhaustionIsNotUnreadable(t *testing.T) {
	root := t.TempDir()
	big := filepath.Join(root, "big")
	if err := os.Mkdir(big, 0700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i <= maxNodes; i++ {
		f, err := os.Create(filepath.Join(big, fmt.Sprintf("%05d.mp3", i)))
		if err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
	write(t, root, "small.mp3", "x")
	report := Scan(root)
	if report.State != "partial" || report.AllocatedBytes != nil || len(report.Items) != 2 {
		t.Fatalf("budget not reported as partial: %+v", report)
	}
	for _, item := range report.Items {
		if item.State != "stored" || item.Bytes != nil || item.AllocatedBytes != nil || item.FileCount != 0 {
			t.Fatalf("healthy item reported as damaged or half-measured: %+v", item)
		}
	}
}

func TestTopLevelSelectionIsSortedBeforeTruncation(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 300; i++ {
		write(t, root, fmt.Sprintf("%03d", i), "x")
	}
	report := Scan(root)
	if report.State != "partial" || len(report.Items) != maxItems {
		t.Fatalf("selection: %+v", report)
	}
	for i, item := range report.Items {
		if item.Id != fmt.Sprintf("%03d", i) {
			t.Fatalf("item %d is %q; the first 128 sorted names are expected", i, item.Id)
		}
	}
}

func TestSymlinksAndSpecialFilesInsideAnItemAreSkipped(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	write(t, outside, "private.txt", strings.Repeat("secret", 500))
	write(t, root, "book/chapter.mp3", "audio")
	if err := os.Symlink(outside, filepath.Join(root, "book", "link")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(root, "book", "pipe"), 0600); err != nil {
		t.Fatal(err)
	}
	report := Scan(root)
	item := report.Items[0]
	if report.State != "complete" || report.AllocatedBytes == nil || item.State != "stored" ||
		item.FileCount != 1 || item.Bytes == nil || *item.Bytes != 5 {
		t.Fatalf("link or FIFO spoiled the item: %+v %+v", report, item)
	}
}

func TestConformDropsWhatTheServerWouldRefuse(t *testing.T) {
	good := func(id string) syncwire.ContentItem {
		return syncwire.ContentItem{Id: id, Path: id, Title: id, Kind: "file", Origin: "unknown", State: "stored"}
	}
	long, badKind, negative, badUTF8 := good(strings.Repeat("l", 129)), good("kind"), good("negative"), good("utf8")
	badKind.Kind = "film"
	negative.FileCount = -1
	badUTF8.Title = "a\xff"
	allocated := int64(1)
	report := syncwire.ContentInventory{
		SampledAt: 1, State: "complete", Label: "Local library", AllocatedBytes: &allocated,
		Items: []syncwire.ContentItem{good("one"), good("one"), long, badKind, negative, badUTF8},
	}
	for i := 0; i < maxItems; i++ {
		report.Items = append(report.Items, good(fmt.Sprintf("extra-%03d", i)))
	}
	got := conform(report)
	if got.State != "partial" || got.AllocatedBytes != nil || len(got.Items) != maxItems || got.Items[0].Id != "one" || got.Items[1].Id != "extra-000" {
		t.Fatalf("conform: state %s, %d items, first %+v", got.State, len(got.Items), got.Items[:2])
	}
	data, err := json.Marshal(got)
	if err != nil || syncwire.Decode("ContentInventory", data, &syncwire.ContentInventory{}) != nil {
		t.Fatalf("conformed report still refused: %v", err)
	}
	report.Label = strings.Repeat("x", 65)
	if bare := conform(report); bare.State != "partial" || len(bare.Items) != 0 || bare.Label != "Local library" {
		t.Fatalf("unsendable report not reduced: %+v", bare)
	}
}
