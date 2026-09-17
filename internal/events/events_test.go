package events

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"callmemaybe/internal/calls"
)

func testOptions() Options {
	return defaults(Options{Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
}
func testStore(t *testing.T, o Options) *store {
	t.Helper()
	s, err := openStore(filepath.Join(t.TempDir(), "journal", "events.db"), defaults(o))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.db.Close() })
	return s
}
func appendTest(t *testing.T, s *store, typ Type) {
	t.Helper()
	if err := s.append(context.Background(), System(typ, "test", 0)); err != nil {
		t.Fatal(err)
	}
}
func readTest(t *testing.T, path string, q Query) Page {
	t.Helper()
	p, err := Read(context.Background(), path, q)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func waitPage(t *testing.T, path string, match func(Page) bool) Page {
	t.Helper()
	until := time.Now().Add(5 * time.Second)
	for time.Now().Before(until) {
		p, err := Read(context.Background(), path, Query{Limit: 10000})
		if err == nil && match(p) {
			return p
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("journal did not reach expected state")
	return Page{}
}
func TestReplayFilteringAndIdentity(t *testing.T) {
	s := testStore(t, Options{})
	for _, typ := range []Type{DaemonStarted, CallObserved, CallAnswered, SessionFinished, CallAnswered} {
		appendTest(t, s, typ)
	}
	p := readTest(t, s.path, Query{Limit: 1, EventType: CallAnswered})
	if len(p.Events) != 1 || p.NextCursor != 3 || p.HighWatermark != 5 {
		t.Fatalf("first page: %+v", p)
	}
	q := Query{After: p.NextCursor, Limit: 1, EventType: CallAnswered, JournalID: p.JournalID, Generation: p.Generation}
	p2 := readTest(t, s.path, q)
	if p2.NextCursor != 5 || p2.Events[0].ID == p.Events[0].ID {
		t.Fatalf("next page: %+v", p2)
	}
	p3 := readTest(t, s.path, Query{Limit: 1, EventType: ContactsRefreshed})
	if len(p3.Events) != 0 || p3.NextCursor != 5 {
		t.Fatalf("empty filtered page: %+v", p3)
	}
	for _, bad := range []Query{{Limit: 0}, {Limit: 10001}, {Limit: 1, After: -1}, {Limit: 1, After: 6}, {Limit: 1, EventType: "typo"}, {Limit: 1, JournalID: "wrong"}, {Limit: 1, Generation: "wrong"}} {
		if _, err := Read(context.Background(), s.path, bad); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
	if err := s.db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := openStore(s.path, testOptions())
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.db.Close()
	appendTest(t, reopened, DaemonStarted)
	p4 := readTest(t, s.path, Query{After: 5, Limit: 1})
	if p4.JournalID != p.JournalID || p4.Generation != p.Generation || p4.NextCursor != 6 {
		t.Fatalf("restart: %+v", p4)
	}
}
func TestRetentionAndNoSequenceReuse(t *testing.T) {
	s := testStore(t, Options{MaxEvents: 2})
	for i := 0; i < 5; i++ {
		appendTest(t, s, CallObserved)
	}
	p := readTest(t, s.path, Query{Limit: 10})
	if len(p.Events) != 2 || p.RetentionFloor != 3 || p.EarliestAvailable != 4 {
		t.Fatalf("retention: %+v", p)
	}
	if _, err := Read(context.Background(), s.path, Query{After: 2, Limit: 1}); err == nil {
		t.Fatal("expired cursor accepted")
	}
	p = readTest(t, s.path, Query{After: 3, Limit: 1})
	if p.NextCursor != 4 {
		t.Fatal(p)
	}
	if _, err := s.db.Exec("UPDATE events SET recorded_at='2000-01-01T00:00:00.000000000Z'"); err != nil {
		t.Fatal(err)
	}
	appendTest(t, s, DaemonStarted)
	p = readTest(t, s.path, Query{Limit: 10})
	if len(p.Events) != 1 || p.NextCursor != 6 || p.RetentionFloor != 5 {
		t.Fatalf("age retention: %+v", p)
	}
}
func TestReadOnlyDoesNotCreateMissingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.db")
	if _, err := Read(context.Background(), path, Query{Limit: 1}); err == nil {
		t.Fatal("missing file accepted")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("read created a file: %v", err)
	}
}
func TestPayloadSnapshotAndRedaction(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal", "events.db")
	w := Start(path, testOptions())
	r := calls.Record{Caller: "+15125550100", Dialled: "+15125550101", Stages: []calls.Stage{{Handsets: []string{"kitchen"}}}}
	w.Post(Call(SessionFinished, "channel-1", r, ""))
	r.Stages[0].Handsets[0] = "mutated"
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	p := readTest(t, path, Query{Limit: 10})
	record := p.Events[0].Payload.Record
	if record.Stages[0].Handsets[0] != "kitchen" {
		t.Fatal("queued record mutated")
	}
	b, _ := json.Marshal(p)
	if strings.Contains(string(b), "+15125550100") || strings.Contains(string(b), "+15125550101") {
		t.Fatal("redaction failed")
	}
	full := readTest(t, path, Query{Limit: 10, Full: true})
	if full.Events[0].Payload.Record.Caller != r.Caller {
		t.Fatal("full read redacted")
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("database not private", err)
	}
}
func TestDoorbellCommitReplayAndRetry(t *testing.T) {
	s := testStore(t, Options{})
	var requests atomic.Int64
	refuse := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		var b Doorbell
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		p := readTest(t, s.path, Query{Limit: 10})
		if b.Type != "journal.available" || b.Locations[0].Through > p.HighWatermark || b.JournalID != p.JournalID {
			t.Error("doorbell announced unavailable data")
		}
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("missing token")
		}
		if refuse {
			w.WriteHeader(503)
		}
	}))
	defer server.Close()
	o := DoorbellOptions{URL: server.URL, Token: "test-token"}
	client := server.Client()
	appendTest(t, s, DaemonStarted)
	appendTest(t, s, CallObserved)
	if err := ringOnce(context.Background(), s.path, o, client, "target"); err == nil {
		t.Fatal("503 accepted")
	}
	refuse = false
	if err := ringOnce(context.Background(), s.path, o, client, "target"); err != nil {
		t.Fatal(err)
	}
	if err := ringOnce(context.Background(), s.path, o, client, "target"); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 2 {
		t.Fatal("progress not persisted", requests.Load())
	}
	appendTest(t, s, CallAnswered)
	if err := ringOnce(context.Background(), s.path, o, client, "target"); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 3 {
		t.Fatal("new commit not announced")
	}
	// A receiver that lost every notification can independently catch up.
	p := readTest(t, s.path, Query{Limit: 1})
	next := readTest(t, s.path, Query{After: p.NextCursor, Limit: 10})
	if len(next.Events) != 2 {
		t.Fatal("consumer could not catch up")
	}
}
func TestUnavailableStorageNeverBlocksProducers(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	w := Start(filepath.Join(dir, "events.db"), testOptions())
	for i := 0; i < 10000; i++ {
		w.Post(System(CallObserved, "", 0))
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if w.Dropped() == 0 || w.Failed() == 0 || !w.Degraded() {
		t.Fatal("loss/degradation not visible")
	}
	w.Post(System(CallObserved, "", 0)) // Safe after shutdown.
}
func TestConcurrentCloseAndPost(t *testing.T) {
	w := Start(filepath.Join(t.TempDir(), "journal", "events.db"), testOptions())
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 1000; i++ {
			w.Post(System(CallObserved, "", 0))
		}
	}()
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	<-done
}
func TestUnknownSchemaPreserved(t *testing.T) {
	s := testStore(t, Options{})
	appendTest(t, s, CallObserved)
	if _, err := s.db.Exec("PRAGMA user_version=999"); err != nil {
		t.Fatal(err)
	}
	if _, err := openStore(s.path, testOptions()); err == nil {
		t.Fatal("newer schema accepted")
	}
	var count int
	if err := s.db.QueryRow("SELECT count(*) FROM events").Scan(&count); err != nil || count != 1 {
		t.Fatal("data changed")
	}
}
func TestUncleanRestart(t *testing.T) {
	// os.Exit skips Close and leaves committed WAL data. This exercises process
	// loss, not the stronger claim of simulated hardware power failure.
	if path := os.Getenv("CMM_JOURNAL_CRASH_HELPER"); path != "" {
		w := Start(path, testOptions())
		w.Post(System(CallObserved, "", 0))
		for i := 0; i < 1000; i++ {
			p, err := Read(context.Background(), path, Query{Limit: 10})
			if err == nil && len(p.Events) > 0 {
				os.Exit(0)
			}
			time.Sleep(time.Millisecond)
		}
		os.Exit(3)
	}
	path := filepath.Join(t.TempDir(), "journal", "events.db")
	cmd := exec.Command(os.Args[0], "-test.run=^TestUncleanRestart$")
	cmd.Env = append(os.Environ(), "CMM_JOURNAL_CRASH_HELPER="+path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("helper: %v %s", err, out)
	}
	before := readTest(t, path, Query{Limit: 10})
	w := Start(path, testOptions())
	w.Post(System(DaemonStarted, "", 0))
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	after := readTest(t, path, Query{Limit: 10})
	if len(after.Events) != 3 || after.Events[1].Type != CoverageGap || before.Events[0].ID != after.Events[0].ID {
		t.Fatalf("restart history: %+v", after)
	}
}
func TestQueueLossProducesCoverageGap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal", "events.db")
	w := Start(path, testOptions())
	// Simulate a counted producer drop without depending on disk speed.
	w.pending.Add(3)
	w.dropped.Add(3)
	w.Post(System(CallObserved, "", 0))
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	p := readTest(t, path, Query{Limit: 10})
	if len(p.Events) != 2 || p.Events[0].Type != CoverageGap || p.Events[0].Payload.Count != 3 {
		t.Fatalf("missing gap: %+v", p)
	}
}

func TestPhysicalBudgetReclaimsHistory(t *testing.T) {
	s := testStore(t, Options{MaxBytes: 8 << 20})
	e := System(CallObserved, strings.Repeat("x", 30000), 0)
	for i := 0; i < 220; i++ {
		if err := s.appendRecover(context.Background(), e); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	p := readTest(t, s.path, Query{Limit: 1})
	if p.RetentionFloor == 0 {
		t.Fatal("page budget did not reclaim any history")
	}
	var total int64
	for _, suffix := range []string{"", "-wal"} {
		st, err := os.Stat(s.path + suffix)
		if err == nil {
			total += st.Size()
		}
	}
	if total > 8<<20 {
		t.Fatalf("physical journal exceeds budget: %d", total)
	}
}

func TestJournalRecoveryAfterDirectoryBecomesPrivate(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "events.db")
	w := Start(path, testOptions())
	defer w.Close()
	w.Post(System(CallObserved, "", 0))
	until := time.Now().Add(time.Second)
	for w.Failed() == 0 && time.Now().Before(until) {
		time.Sleep(time.Millisecond)
	}
	if w.Failed() == 0 {
		t.Fatal("storage failure was not counted")
	}
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	w.Post(System(DaemonStarted, "", 0))
	p := waitPage(t, path, func(p Page) bool { return len(p.Events) >= 2 })
	if p.Events[0].Type != CoverageGap || p.Events[0].Payload.Count < 1 {
		t.Fatalf("recovery hid loss: %+v", p)
	}
}

func TestReaderSeesOnlyCommittedRows(t *testing.T) {
	s := testStore(t, Options{})
	appendTest(t, s, DaemonStarted)
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec("UPDATE events SET type='call.answered'"); err != nil {
		t.Fatal(err)
	}
	p := readTest(t, s.path, Query{Limit: 1})
	if p.Events[0].Type != DaemonStarted {
		t.Fatal("reader saw uncommitted update")
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
}

func TestDoorbellWorkerRetriesAndCancels(t *testing.T) {
	s := testStore(t, Options{})
	appendTest(t, s, DaemonStarted)
	var requests atomic.Int64
	delivered := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			w.WriteHeader(503)
			return
		}
		select {
		case delivered <- struct{}{}:
		default:
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); Ring(ctx, s.path, DoorbellOptions{URL: server.URL, Log: testOptions().Log}) }()
	select {
	case <-delivered:
	case <-time.After(4 * time.Second):
		t.Fatal("doorbell was not retried")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("doorbell did not cancel")
	}
}

func TestCheckPathDoesNotMutateAndRejectsSharedStorage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new-journal", "events.db")
	if err := CheckPath(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatal("check created storage")
	}
	s := testStore(t, Options{})
	appendTest(t, s, DaemonStarted)
	if err := CheckPath(s.path); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Dir(s.path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := CheckPath(s.path); err == nil {
		t.Fatal("shared storage accepted")
	}
}

func TestPinnedReaderCannotGrowWALWithoutBound(t *testing.T) {
	s := testStore(t, Options{MaxBytes: 8 << 20})
	appendTest(t, s, DaemonStarted)
	reader, err := connect(s.path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	tx, err := reader.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var high int64
	if err = tx.QueryRow("SELECT high FROM metadata").Scan(&high); err != nil {
		t.Fatal(err)
	}
	e := System(CallObserved, strings.Repeat("x", 30000), 0)
	stopped := false
	for i := 0; i < 400; i++ {
		if err = s.appendRecover(context.Background(), e); err != nil {
			if !strings.Contains(err.Error(), "WAL budget") {
				t.Fatal(err)
			}
			stopped = true
			break
		}
	}
	if !stopped {
		t.Fatal("pinned WAL never hit its bound")
	}
	var total int64
	for _, suffix := range []string{"", "-wal"} {
		st, err := os.Stat(s.path + suffix)
		if err == nil {
			total += st.Size()
		}
	}
	if total > 8<<20 {
		t.Fatalf("pinned reader exceeded disk budget: %d", total)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err = s.appendRecover(context.Background(), System(DaemonStarted, "reader-released", 0)); err != nil {
		t.Fatal("writer did not recover:", err)
	}
}
