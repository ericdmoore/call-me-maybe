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
	keep, prune := Keep(names, 7, 4, now)
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
