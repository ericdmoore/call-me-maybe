package textlog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLogAppendsReadsAndRotates(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "inbox")
	l, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("state dir: %v %v — it holds full numbers", err, info)
	}
	now := time.Date(2026, 9, 26, 8, 0, 0, 0, time.UTC)
	for i, r := range []Record{
		{At: now.Add(-30 * time.Hour), ID: "old", To: "+15125550101", Result: "replied", Reply: "pong"},
		{At: now.Add(-2 * time.Hour), ID: "m1", To: "+15125550101", Person: "gabi", Word: "garage", Action: "garage", Result: "acted", Reply: "The garage is open"},
		{At: now.Add(-1 * time.Hour), ID: "m2", To: "+15125550199", Result: "unlisted"},
	} {
		if err := l.Append(r); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	if info, err := os.Stat(filepath.Join(dir, "outcomes.jsonl")); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("log file: %v %v", err, info)
	}
	got, err := Read(dir, now.Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "m1" || got[1].ID != "m2" {
		t.Fatalf("since 24h = %+v, want m1 and m2 in order", got)
	}
	if got[0].Reply != "The garage is open" || got[0].Person != "gabi" {
		t.Errorf("record lost fields: %+v", got[0])
	}

	// Rotation keeps one generation, and reads span both.
	l.maxBytes = 200
	for i := 0; i < 5; i++ {
		_ = l.Append(Record{At: now, ID: "r" + string(rune('0'+i)), Result: "replied", Reply: strings.Repeat("x", 60)})
	}
	if _, err := os.Stat(filepath.Join(dir, "outcomes.jsonl.1")); err != nil {
		t.Fatal("no rotated generation")
	}
	// With a cap this small every append rotates, so exactly one previous
	// record survives beside the newest — and a read spans both files.
	all, _ := Read(dir, time.Time{})
	if len(all) != 2 || all[0].ID != "r3" || all[1].ID != "r4" {
		t.Errorf("reads should span both generations, oldest first: %+v", all)
	}

	// No log at all is no records.
	if got, err := Read(filepath.Join(t.TempDir(), "nothing"), time.Time{}); err != nil || len(got) != 0 {
		t.Errorf("missing log: %v %v", got, err)
	}
}
