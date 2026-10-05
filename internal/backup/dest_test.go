package backup

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestKeepDailyAndWeekly(t *testing.T) {
	now := time.Date(2026, 10, 30, 6, 0, 0, 0, time.UTC)
	var names []string
	for d := 0; d < 40; d++ {
		names = append(names, Name("jepsen", now.AddDate(0, 0, -d)))
	}
	names = append(names, "notes.txt")
	keep, prune := Keep("jepsen", names, 7, 4, now)
	if len(keep) < 7 || len(keep) > 11 {
		t.Errorf("kept %d: %v", len(keep), keep)
	}
	for _, p := range prune {
		if p == "notes.txt" {
			t.Error("a file that is not a bundle must never be pruned")
		}
	}
	if got, _ := Latest(names); got != Name("jepsen", now) {
		t.Errorf("Latest = %s", got)
	}
	// The newest seven days are all kept.
	for d := 0; d < 7; d++ {
		want := Name("jepsen", now.AddDate(0, 0, -d))
		found := false
		for _, k := range keep {
			if k == want {
				found = true
			}
		}
		if !found {
			t.Errorf("day -%d not kept", d)
		}
	}
}

func TestFileDestinationDeliversAtomicallyAndPrunes(t *testing.T) {
	dir := t.TempDir()
	d := FileDest{Dir: filepath.Join(dir, "backups")}
	now := time.Date(2026, 10, 30, 6, 0, 0, 0, time.UTC)
	for i := 10; i >= 0; i-- {
		at := now.AddDate(0, 0, -i)
		res := Deliver(context.Background(), []Destination{d}, Name("jepsen", at), []byte("bundle"), 3, 1, at)
		if res[0].Err != nil {
			t.Fatal(res[0].Err)
		}
	}
	names, _ := d.List(context.Background())
	if len(names) < 3 || len(names) > 4 {
		t.Errorf("after pruning: %v", names)
	}
	info, _ := os.Stat(filepath.Join(d.Dir, Name("jepsen", now)))
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %o", info.Mode().Perm())
	}
	if leftovers, _ := filepath.Glob(filepath.Join(d.Dir, ".*tmp*")); len(leftovers) != 0 {
		t.Errorf("temp files left: %v", leftovers)
	}
	rc, err := d.Get(context.Background(), Name("jepsen", now))
	if err != nil {
		t.Fatal(err)
	}
	rc.Close()
}

// Two houses in one bucket keep their own history; neither prunes the other.
func TestKeepIsPerHost(t *testing.T) {
	now := time.Date(2026, 10, 30, 6, 0, 0, 0, time.UTC)
	var names []string
	for d := 0; d < 10; d++ {
		names = append(names, Name("jepsen", now.AddDate(0, 0, -d)), Name("grandma", now.AddDate(0, 0, -d)))
	}
	keep, prune := Keep("jepsen", names, 7, 0, now)
	for _, k := range append(keep, prune...) {
		if h, _, _ := parseName(k); h != "jepsen" {
			t.Errorf("another host's bundle was considered at all: %s", k)
		}
	}
	if len(keep) != 7 {
		t.Errorf("kept %d of jepsen's, want 7", len(keep))
	}
}

// The reviewer's reproduction: ten nightly grandma bundles already at the
// destination, then one jepsen delivery with daily=1 — grandma loses none.
func TestDeliveryPrunesOnlyTheDeliveringHost(t *testing.T) {
	dir := t.TempDir()
	d := FileDest{Dir: dir}
	now := time.Date(2026, 10, 30, 6, 0, 0, 0, time.UTC)
	for i := 0; i < 10; i++ {
		os.WriteFile(filepath.Join(dir, Name("grandma", now.AddDate(0, 0, -i))), []byte("g"), 0o600)
	}
	res := Deliver(context.Background(), []Destination{d}, Name("jepsen", now), []byte("j"), 1, 0, now)
	if res[0].Err != nil || res[0].Pruned != 0 {
		t.Fatalf("delivery = %+v", res[0])
	}
	names, _ := d.List(context.Background())
	grandma := 0
	for _, n := range names {
		if h, _, _ := parseName(n); h == "grandma" {
			grandma++
		}
	}
	if grandma != 10 {
		t.Errorf("grandma kept %d of 10 — another house's history was pruned", grandma)
	}
}
