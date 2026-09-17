package events

import (
	"callmemaybe/internal/calls"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAgeRetentionOnlyDeletesExpiredPrefix(t *testing.T) {
	s := testStore(t, Options{MaxAge: time.Hour})
	for i := 0; i < 5; i++ {
		if err := s.append(context.Background(), System(DaemonStarted, "", 0)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.Exec("UPDATE events SET recorded_at='1970-01-01T00:00:00.000000000Z' WHERE sequence=5"); err != nil {
		t.Fatal(err)
	}
	if err := s.append(context.Background(), System(DaemonStarted, "", 0)); err != nil {
		t.Fatal(err)
	}
	p := readTest(t, s.path, Query{After: 2, Limit: 10})
	if len(p.Events) != 4 || p.RetentionFloor != 0 {
		t.Fatal("clock regression pruned fresh history", p)
	}
	if _, err := s.db.Exec("UPDATE events SET recorded_at='1970-01-01T00:00:00.000000000Z' WHERE sequence<=2"); err != nil {
		t.Fatal(err)
	}
	if err := s.append(context.Background(), System(DaemonStarted, "", 0)); err != nil {
		t.Fatal(err)
	}
	p = readTest(t, s.path, Query{Limit: 10})
	if p.RetentionFloor != 2 {
		t.Fatal(p)
	}
	zero := int64(0)
	p = readTest(t, s.path, Query{Limit: 10, Through: &zero})
	if len(p.Events) != 0 || p.NextCursor != 0 {
		t.Fatal("explicit empty batch failed", p)
	}
}
func TestImmediateWriterAndReconnectionPragmas(t *testing.T) {
	s := testStore(t, Options{})
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	other, err := connect(s.path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	_, err = other.Exec("INSERT INTO delivery_state VALUES('target',0)")
	if err == nil {
		t.Fatal("writer transaction did not reserve the write lock")
	}
	if err = s.appendTx(context.Background(), tx, System(DaemonStarted, "", 0)); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	// Explicitly recycle the physical connection, as interruption also does.
	s.db.SetMaxIdleConns(0)
	for key, want := range map[string]int64{"max_page_count": s.opts.MaxBytes / 4 / 4096, "wal_autocheckpoint": 64, "journal_size_limit": 0, "synchronous": 2} {
		var got int64
		if err = s.db.QueryRow("PRAGMA " + key).Scan(&got); err != nil || got != want {
			t.Fatal(key, got, want, err)
		}
	}
}
func TestConcurrentDoorbellsDoNotLoseObservations(t *testing.T) {
	s := testStore(t, Options{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	defer server.Close()
	var wg sync.WaitGroup
	wg.Add(1)
	errs := make(chan error, 1)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			if err := ringOnce(context.Background(), s.path, DoorbellOptions{URL: server.URL}, server.Client(), "consumer"); err != nil {
				errs <- err
				return
			}
		}
	}()
	for i := 0; i < 100; i++ {
		if err := s.appendRecover(context.Background(), System(DaemonStarted, "", 0)); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
	select {
	case err := <-errs:
		t.Fatal(err)
	default:
	}
	p := readTest(t, s.path, Query{Limit: 100})
	if len(p.Events) != 100 {
		t.Fatal("event loss", len(p.Events))
	}
}
func TestCELRootClassificationNormalisationAndBridgeReason(t *testing.T) {
	path, db := celFixture(t)
	s := testStore(t, Options{})
	celPut(t, db, "APP_START", "out", "out", "internal", "5125550101", "Dial", 0)
	celPut(t, db, "CHAN_START", "trunk-leg", "out", "inbound-trunk", "", "", 1)
	celPut(t, db, "BRIDGE_ENTER", "trunk-leg", "out", "inbound-trunk", "", "", 2)
	celPut(t, db, "CHAN_END", "trunk-leg", "out", "inbound-trunk", "", "", 3)
	celPut(t, db, "CHAN_END", "out", "out", "internal", "", "", 4)
	celPut(t, db, "CHAN_START", "in", "in", "from-voipms", "", "", 5)
	celPut(t, db, "BRIDGE_ENTER", "in", "in", "inbound-trunk", "", "", 6)
	celPut(t, db, "CHAN_END", "in", "in", "inbound-trunk", "", "", 7)
	if _, err := db.Exec("UPDATE doorman_cel SET caller='5125550100' WHERE uniqueid='in'"); err != nil {
		t.Fatal(err)
	}
	ingest(t, s, path)
	h, err := ReadCalls(context.Background(), s.path, calls.Filter{}, true)
	if err != nil || len(h.Records) != 2 {
		t.Fatal("phantom peer summary", h, err)
	}
	h, err = ReadCalls(context.Background(), s.path, calls.Filter{Direction: "inbound", Caller: "+1512"}, true)
	if err != nil || len(h.Records) != 1 || h.Records[0].Caller != "+15125550100" || !strings.Contains(h.Records[0].Reason, "bridge observed, inbound") {
		t.Fatal(h, err)
	}
}
func TestConsoleProjectionRetainsCompatibilityAndCELDuration(t *testing.T) {
	s := testStore(t, Options{})
	r := calls.Record{ID: "out", Direction: "outbound", Line: "biz", Known: "name", Outcome: "placed", MS: 100}
	completed(t, s, "out", r)
	cel := r
	cel.Line = ""
	cel.Outcome = "answered"
	cel.MS = 9000
	if err := s.append(context.Background(), Event{Type: CallFinished, CallID: "out", Payload: Payload{Record: &cel}}); err != nil {
		t.Fatal(err)
	}
	for _, f := range []calls.Filter{{}, {Line: "biz"}, {Line: "biz", Direction: "outbound"}, {Outcome: "placed"}} {
		h, err := ReadCalls(context.Background(), s.path, f, true)
		if err != nil || len(h.Records) != 1 || h.Records[0].MS != 9000 || h.Records[0].Known != "name" || h.Records[0].Outcome != "placed" {
			t.Fatal(h, err)
		}
	}
}
func TestInformationalCELStartDoesNotWarnAboutLoss(t *testing.T) {
	path, _ := celFixture(t)
	s := testStore(t, Options{})
	ingest(t, s, path)
	h, err := ReadCalls(context.Background(), s.path, calls.Filter{}, true)
	if err != nil || h.GapEvents != 0 {
		t.Fatal(h, err)
	}
}
func TestStartupFailureAndCheckDiagnostics(t *testing.T) {
	_, err := StartChecked(filepath.Join(t.TempDir(), "db"), Options{})
	if err == nil {
		t.Fatal("insecure parent accepted")
	}
	s := testStore(t, Options{})
	if err = CheckPath(s.path, Options{MaxBytes: 4096}); err == nil || !strings.Contains(err.Error(), "budget") {
		t.Fatal(err)
	}
	missing := filepath.Join(t.TempDir(), "missing.db")
	if err = CheckCELPath(context.Background(), missing); err == nil || strings.Contains(err.Error(), "table missing") {
		t.Fatal("open error disguised", err)
	}
	if _, err = os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal(err)
	}
}
func TestStartCheckedEmptyCloseIsClean(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "db")
	w, err := StartChecked(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := connect(path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var clean int
	if err = db.QueryRow("SELECT clean FROM metadata").Scan(&clean); err != nil || clean != 1 {
		t.Fatal(clean, err)
	}
}

func TestCELMappingFiltersIFBranchWithoutSplittingBackendColumns(t *testing.T) {
	data, err := os.ReadFile("../../asterisk/cel_sqlite3_custom.conf")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "values =>") {
			continue
		}
		fields := strings.Split(strings.TrimPrefix(line, "values =>"), ",")
		if len(fields) != 8 {
			t.Fatal("legacy CEL backend splits every literal comma", len(fields))
		}
		if !strings.Contains(fields[6], "?${FILTER(0-9${URIDECODE(%2C)}${CHANNEL(exten)})}:") {
			t.Fatal("IF true branch can expose colon-bearing extension")
		}
	}
}
func TestSourceReplacementAndRetentionHaveSeparateNotices(t *testing.T) {
	path, db := celFixture(t)
	s := testStore(t, Options{})
	ingest(t, s, path)
	if _, err := db.Exec("UPDATE doorman_cel_meta SET source_id='replacement'"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		celPut(t, db, "CHAN_START", "c", "c", "inbound-trunk", "", "", i)
	}
	if _, err := db.Exec("DELETE FROM doorman_cel WHERE AcctId<3"); err != nil {
		t.Fatal(err)
	}
	ingest(t, s, path)
	page := readTest(t, s.path, Query{Limit: 100, EventType: CoverageGap})
	if len(page.Events) != 2 || page.Events[0].Payload.Reason != "cel-source-replaced" || page.Events[1].Payload.Reason != "cel-source-retention-gap" {
		t.Fatal(page)
	}
}
func TestWriterOwnershipIsReportedAndEnforced(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "db")
	w, err := StartChecked(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	active, err := WriterActive(path)
	if err != nil || !active {
		t.Fatal(active, err)
	}
	_, err = StartChecked(path, Options{})
	if err == nil || !strings.Contains(err.Error(), "writer lock") {
		t.Fatal(err)
	}
}
