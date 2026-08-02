package calls_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"callmemaybe/internal/calls"
)

func tmp(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "calls.jsonl")
}

func rec(caller, outcome string) calls.Record {
	return calls.Record{
		ID:      "ch-1",
		Start:   time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC),
		MS:      4200,
		Caller:  caller,
		Outcome: outcome,
	}
}

func writeAll(t *testing.T, path string, rs ...calls.Record) *calls.Writer {
	t.Helper()
	w, err := calls.Open(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rs {
		w.Post(r)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return w
}

func TestRoundTrip(t *testing.T) {
	path := tmp(t)
	writeAll(t, path,
		rec("+15125550100", calls.OutcomeAnswered),
		rec("+15125550101", calls.OutcomeDismissed),
	)

	got, skipped, err := calls.Read(path, calls.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if skipped != 0 {
		t.Errorf("skipped %d lines", skipped)
	}
	if len(got) != 2 {
		t.Fatalf("read %d records, want 2", len(got))
	}
	if got[0].Caller != "+15125550100" || got[1].Outcome != calls.OutcomeDismissed {
		t.Errorf("records came back wrong: %+v", got)
	}
	if got[0].Duration() != 4200*time.Millisecond {
		t.Errorf("duration = %v", got[0].Duration())
	}
}

// The file holds numbers; anything leaving the box does not.
func TestRedactedHidesTheNumber(t *testing.T) {
	r := rec("+15125550100", calls.OutcomeAnswered).Redacted()
	if strings.Contains(r.Caller, "5550100") {
		t.Errorf("Redacted still contains the subscriber digits: %q", r.Caller)
	}
	if r.Caller == "" {
		t.Error("redaction should leave something identifiable, not nothing")
	}
}

// A near-miss is almost a credential, so there is nowhere in the record for
// one to go. This asserts the shape of the type itself: if somebody adds a
// field for entered digits, this fails.
func TestNoFieldCanCarryEnteredDigits(t *testing.T) {
	b, err := json.Marshal(calls.Record{
		Caller:    "+15125550100",
		Extension: "Kids",
		PIN:       "valid",
		Attempts:  2,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"digits", "pin_entered", "entered", "code", "secret"} {
		if strings.Contains(string(b), forbidden) {
			t.Errorf("record has a %q field — entered digits must never be recordable: %s", forbidden, b)
		}
	}
	// And the PIN field carries a verdict, not a number.
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if v, _ := m["pin"].(string); v != "valid" && v != "invalid" {
		t.Errorf("pin = %v, want a verdict", m["pin"])
	}
}

// The expected failure on a Pi is losing power mid-write. A log that refuses
// to open because of that is worse than one missing its last entry.
func TestPartialFinalLineIsSkippedNotFatal(t *testing.T) {
	path := tmp(t)
	writeAll(t, path, rec("+15125550100", calls.OutcomeAnswered))

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"id":"ch-2","caller":"+1512555`); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	got, skipped, err := calls.Read(path, calls.Filter{})
	if err != nil {
		t.Fatalf("a torn final line must not be fatal: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("read %d complete records, want 1", len(got))
	}
	if skipped != 1 {
		t.Errorf("skipped = %d, want 1 — a hole must be reported, not silent", skipped)
	}
}

func TestFilters(t *testing.T) {
	path := tmp(t)
	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

	var rs []calls.Record
	for i, c := range []struct{ caller, outcome string }{
		{"+15125550100", calls.OutcomeAnswered},
		{"+15125550101", calls.OutcomeDismissed},
		{"+15125550102", calls.OutcomeAnswered},
	} {
		r := rec(c.caller, c.outcome)
		r.Start = base.Add(time.Duration(i) * time.Hour)
		rs = append(rs, r)
	}
	writeAll(t, path, rs...)

	for _, c := range []struct {
		name string
		f    calls.Filter
		want int
	}{
		{"everything", calls.Filter{}, 3},
		{"by outcome", calls.Filter{Outcome: calls.OutcomeAnswered}, 2},
		{"by since", calls.Filter{Since: base.Add(30 * time.Minute)}, 2},
		{"by partial caller", calls.Filter{Caller: "5550101"}, 1},
		{"limit keeps the most recent", calls.Filter{Limit: 1}, 1},
		{"combined", calls.Filter{Outcome: calls.OutcomeAnswered, Since: base.Add(30 * time.Minute)}, 1},
	} {
		got, _, err := calls.Read(path, c.f)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != c.want {
			t.Errorf("%s: got %d records, want %d", c.name, len(got), c.want)
		}
	}

	// Limit must keep the newest, not the first N off the front.
	got, _, err := calls.Read(path, calls.Filter{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Caller != "+15125550102" {
		t.Errorf("limit kept %s, want the most recent", got[0].Caller)
	}
}

func TestReadingAMissingFileIsNotAnError(t *testing.T) {
	got, _, err := calls.Read(filepath.Join(t.TempDir(), "nope.jsonl"), calls.Filter{})
	if err != nil {
		t.Fatalf("no calls yet is not an error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d records from nothing", len(got))
	}
}

// ── the properties that keep this off the call path ─────────────────────

// Post must never block, whatever the writer is doing. If it can block, a slow
// disk delays a call, which is the one thing a call log may not do.
func TestPostNeverBlocks(t *testing.T) {
	w, err := calls.Open(tmp(t), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()

	done := make(chan struct{})
	go func() {
		defer close(done)
		// Far more than the buffer holds; the excess must be dropped, not
		// waited on.
		for range 10_000 {
			w.Post(rec("+15125550100", calls.OutcomeAnswered))
		}
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Post blocked — a full buffer must drop, not wait")
	}
}

// A dropped record is a hole in the log. Holes are acceptable; silent holes
// are not.
func TestDropsAreCounted(t *testing.T) {
	w, err := calls.Open(tmp(t), 0)
	if err != nil {
		t.Fatal(err)
	}
	for range 10_000 {
		w.Post(rec("+15125550100", calls.OutcomeAnswered))
	}
	dropped := w.Dropped()
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if dropped == 0 {
		t.Error("10,000 records through a 64-deep buffer dropped none — the counter is not wired up")
	}
}

func TestPostOnANilWriterIsSafe(t *testing.T) {
	var w *calls.Writer // logging disabled
	w.Post(rec("+15125550100", calls.OutcomeAnswered))
	if err := w.Close(); err != nil {
		t.Error(err)
	}
}

func TestConcurrentPostersAreSafe(t *testing.T) {
	path := tmp(t)
	w, err := calls.Open(path, 0)
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 4 {
				r := rec("+1512555010"+string(rune('0'+i)), calls.OutcomeAnswered)
				w.Post(r)
				time.Sleep(time.Millisecond)
			}
		}()
	}
	wg.Wait()
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	// Whatever survived must be complete lines — interleaved writes would
	// show up as unparseable ones.
	got, skipped, err := calls.Read(path, calls.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if skipped != 0 {
		t.Errorf("%d torn lines from concurrent posts", skipped)
	}
	if len(got) == 0 {
		t.Error("nothing was written")
	}
}

// ── rotation ────────────────────────────────────────────────────────────

func TestRotationKeepsOneGenerationAndReadsAcrossIt(t *testing.T) {
	path := tmp(t)
	w, err := calls.Open(path, 400) // a handful of records
	if err != nil {
		t.Fatal(err)
	}
	for i := range 20 {
		r := rec("+15125550100", calls.OutcomeAnswered)
		r.Start = r.Start.Add(time.Duration(i) * time.Minute)
		w.Post(r)
		time.Sleep(2 * time.Millisecond) // let the writer drain
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatalf("expected a rotated generation: %v", err)
	}
	if _, err := os.Stat(path + ".2"); err == nil {
		t.Error("only one previous generation should be kept")
	}

	got, skipped, err := calls.Read(path, calls.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if skipped != 0 {
		t.Errorf("%d unparseable lines across the rotation", skipped)
	}
	if len(got) < 2 {
		t.Fatalf("read %d records; the rotated file should still be read", len(got))
	}
	// Order must survive: the previous generation is older.
	for i := 1; i < len(got); i++ {
		if got[i].Start.Before(got[i-1].Start) {
			t.Errorf("records out of order across rotation at %d", i)
			break
		}
	}
}

// The file holds full caller IDs, so it must not be world-readable.
func TestFileIsNotWorldReadable(t *testing.T) {
	path := tmp(t)
	writeAll(t, path, rec("+15125550100", calls.OutcomeAnswered))

	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := st.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("mode %04o — a file of caller IDs must be 0600", perm)
	}
}

func TestOpenRejectsAnUnusablePath(t *testing.T) {
	// A path under a file rather than a directory: this has to fail at
	// startup, where an operator sees it, not after the first call.
	f := filepath.Join(t.TempDir(), "notadir")
	if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := calls.Open(filepath.Join(f, "calls.jsonl"), 0); err == nil {
		t.Error("expected an error opening a log under a regular file")
	}
	if _, err := calls.Open("", 0); err == nil {
		t.Error("expected an error for an empty path")
	}
}
