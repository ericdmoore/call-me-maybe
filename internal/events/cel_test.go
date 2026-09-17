package events

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"callmemaybe/internal/calls"
)

func celFixture(t *testing.T) (string, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "master.db")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	db, err := connect(path, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	script, err := os.ReadFile("../../scripts/cel-spool.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(string(script)); err != nil {
		t.Fatal(err)
	}
	if err = CheckCELPath(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	return path, db
}
func celPut(t *testing.T, db *sql.DB, kind, id, linked, ctx, dest, app string, second int) {
	t.Helper()
	_, err := db.Exec("INSERT INTO doorman_cel(eventtype,eventtime,uniqueid,linkedid,context,caller,destination,app) VALUES(?,CAST(? AS TEXT)||'.123456',?,?,?,?,?,?)", kind, 1800000000+second, id, linked, ctx, "+15125550100", dest, app)
	if err != nil {
		t.Fatal(err)
	}
}
func ingest(t *testing.T, s *store, path string) {
	t.Helper()
	if err := s.ingestCEL(context.Background(), path); err != nil {
		t.Fatal(err)
	}
}
func TestCELCaptureReplayProjectionAndRedaction(t *testing.T) {
	path, db := celFixture(t)
	s := testStore(t, Options{})
	celPut(t, db, "CHAN_START", "out", "out", "internal", "5125550101", "", 0)
	celPut(t, db, "APP_START", "out", "out", "cmm-outbound", "5125550101", "Dial", 1)
	celPut(t, db, "CHAN_START", "peer", "out", "internal", "", "", 2)
	celPut(t, db, "ANSWER", "peer", "out", "internal", "", "", 3)
	celPut(t, db, "BRIDGE_ENTER", "out", "out", "cmm-outbound", "5125550101", "Dial", 4)
	ingest(t, s, path)
	// Commit state, close and reopen: no in-memory correlation is required.
	before := readTest(t, s.path, Query{Limit: 100})
	if err := s.db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := openStore(s.path, testOptions())
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	ingest(t, s, path)
	replay := readTest(t, s.path, Query{Limit: 100})
	if replay.HighWatermark != before.HighWatermark {
		t.Fatal("replayed source rows duplicated events")
	}
	celPut(t, db, "HANGUP", "out", "out", "cmm-outbound", "5125550101", "Dial", 10)
	celPut(t, db, "CHAN_END", "out", "out", "cmm-hangup", "", "", 11)
	celPut(t, db, "CHAN_END", "peer", "out", "internal", "", "", 12)
	celPut(t, db, "LINKEDID_END", "peer", "out", "internal", "", "", 12)
	ingest(t, s, path)
	h, err := ReadCalls(context.Background(), s.path, calls.Filter{Direction: "outbound"}, true)
	if err != nil || len(h.Records) != 1 {
		t.Fatal(h, err)
	}
	r := h.Records[0]
	if r.Outcome != "answered" || r.MS != 10000 || r.Dialled != "5125550101" {
		t.Fatalf("wrong call summary: %+v", r)
	}
	// Source IDs, linked IDs, and source order remain available to consumers.
	page := readTest(t, s.path, Query{Limit: 100, Direction: "outbound"})
	for _, e := range page.Events {
		if c := e.Payload.Channel; c != nil && (c.LinkedID != "out" || c.SourceSequence == 0 || c.Dialled == "5125550101") {
			t.Fatal("lost correlation or leaked number")
		}
	}
	data, _ := json.Marshal(page)
	if strings.Contains(string(data), "5125550101") {
		t.Fatal("unredacted destination")
	}
	var remaining int
	if err = s.db.QueryRow("SELECT count(*) FROM cel_channels").Scan(&remaining); err != nil || remaining != 0 {
		t.Fatal("channel state leaked", remaining, err)
	}
}
func TestCELInboundAnswerIsNotHumanAnswerAndNoDuplicateSummary(t *testing.T) {
	path, db := celFixture(t)
	s := testStore(t, Options{})
	celPut(t, db, "CHAN_START", "in", "in", "inbound-trunk", "", "", 0)
	celPut(t, db, "ANSWER", "in", "in", "inbound-trunk", "", "Answer", 1)
	celPut(t, db, "APP_START", "in", "in", "cmm-line-biz", "", "Stasis", 2)
	celPut(t, db, "APP_START", "in", "in", "voicemail-drop", "", "VoiceMail", 3)
	celPut(t, db, "CHAN_END", "in", "in", "voicemail-drop", "", "", 20)
	ingest(t, s, path)
	h, err := ReadCalls(context.Background(), s.path, calls.Filter{}, true)
	if err != nil || len(h.Records) != 1 || h.Records[0].Outcome != "ended" || h.Records[0].Line != "biz" {
		t.Fatal(h, err)
	}
	r := calls.Record{ID: "in", Start: time.Now(), Outcome: "voicemail", Line: "biz"}
	completed(t, s, "in", r)
	h, err = ReadCalls(context.Background(), s.path, calls.Filter{}, true)
	if err != nil || len(h.Records) != 1 || h.Records[0].Outcome != "voicemail" {
		t.Fatal("lost Doorman admission detail", h, err)
	}
	page := readTest(t, s.path, Query{Limit: 100, EventType: ChannelEnded})
	if len(page.Events) != 1 {
		t.Fatal("missing post-handoff channel end")
	}
}
func TestCELAtomicRollbackAndRecovery(t *testing.T) {
	path, db := celFixture(t)
	s := testStore(t, Options{})
	celPut(t, db, "CHAN_START", "out", "out", "internal", "5125550101", "", 0)
	ingest(t, s, path)
	before := readTest(t, s.path, Query{Limit: 100})
	celPut(t, db, "APP_START", "out", "out", "cmm-outbound", "5125550101", "Dial", 1)
	// A failed progress update must roll back both events and correlation state.
	_, err := s.db.Exec("CREATE TRIGGER fail_progress BEFORE UPDATE ON cel_progress BEGIN SELECT RAISE(ABORT,'fixture failure'); END;")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ingestCEL(context.Background(), path); err == nil {
		t.Fatal("fault not exercised")
	}
	after := readTest(t, s.path, Query{Limit: 100})
	if after.HighWatermark != before.HighWatermark {
		t.Fatal("partial source transaction committed")
	}
	if _, err = s.db.Exec("DROP TRIGGER fail_progress"); err != nil {
		t.Fatal(err)
	}
	ingest(t, s, path)
	after = readTest(t, s.path, Query{Limit: 100})
	if after.HighWatermark != before.HighWatermark+1 {
		t.Fatal("recovery skipped or duplicated event")
	}
}
func TestCELSourceRetentionReplacementAndRewind(t *testing.T) {
	path, db := celFixture(t)
	s := testStore(t, Options{})
	celPut(t, db, "CHAN_START", "a", "a", "inbound-trunk", "", "", 0)
	ingest(t, s, path)
	celPut(t, db, "ANSWER", "a", "a", "inbound-trunk", "", "", 1)
	celPut(t, db, "CHAN_END", "a", "a", "inbound-trunk", "", "", 2)
	if _, err := db.Exec("DELETE FROM doorman_cel WHERE AcctId<3"); err != nil {
		t.Fatal(err)
	}
	ingest(t, s, path)
	p := readTest(t, s.path, Query{Limit: 100, EventType: CoverageGap})
	if len(p.Events) != 1 || p.Events[0].Payload.Count != 1 {
		t.Fatal("source retention gap hidden", p)
	}
	if _, err := db.Exec("UPDATE doorman_cel SET eventtime='1900000000.000000' WHERE AcctId=3"); err != nil {
		t.Fatal(err)
	}
	if err := s.ingestCEL(context.Background(), path); err == nil {
		t.Fatal("source anchor change accepted")
	}
	if _, err := db.Exec("UPDATE doorman_cel_meta SET source_id='new-source'; DELETE FROM doorman_cel; DELETE FROM sqlite_sequence WHERE name='doorman_cel'"); err != nil {
		t.Fatal(err)
	}
	celPut(t, db, "CHAN_START", "b", "b", "inbound-trunk", "", "", 5)
	ingest(t, s, path)
	ingest(t, s, path)
	p = readTest(t, s.path, Query{Limit: 100, CallID: "b"})
	if len(p.Events) != 1 {
		t.Fatal("new source not replayed")
	}
	if _, err := db.Exec("DELETE FROM doorman_cel"); err != nil {
		t.Fatal(err)
	}
	if err := s.ingestCEL(context.Background(), path); err == nil {
		t.Fatal("same source rewind accepted")
	}
}
func TestCELNeverStoresCredentialOrApplicationArguments(t *testing.T) {
	path, db := celFixture(t)
	s := testStore(t, Options{})
	for i, ctx := range []string{"internal", "cmm-outbound", "voicemail-drop"} {
		celPut(t, db, "APP_START", "pin", "pin", ctx, "654321", "Dial", i)
	}
	ingest(t, s, path)
	p := readTest(t, s.path, Query{Limit: 100, Full: true})
	encoded, _ := json.Marshal(p)
	if strings.Contains(string(encoded), "654321") {
		t.Fatal("credential-shaped extension reached journal")
	}
	h, err := ReadCalls(context.Background(), s.path, calls.Filter{}, true)
	if err != nil || len(h.Records) != 0 {
		t.Fatal("credential became call summary")
	}
}
func TestCELUnansweredTransferAndConsolePrecedence(t *testing.T) {
	path, db := celFixture(t)
	s := testStore(t, Options{})
	for i, id := range []string{"noanswer", "console"} {
		celPut(t, db, "APP_START", id, id, "outbound-console", "5125550101", "Dial", i*10)
		if i == 1 {
			celPut(t, db, "BRIDGE_ENTER", id, id, "outbound-console", "", "", i*10+1)
			celPut(t, db, "BLINDTRANSFER", id, id, "internal", "", "", i*10+2)
		}
		celPut(t, db, "CHAN_END", id, id, "internal", "", "", i*10+3)
	}
	ingest(t, s, path)
	// A delayed console handoff must not replace an already observed termination.
	completed(t, s, "console", calls.Record{ID: "console", Direction: "outbound", Outcome: "placed"})
	h, err := ReadCalls(context.Background(), s.path, calls.Filter{}, true)
	if err != nil || len(h.Records) != 2 {
		t.Fatal(h, err)
	}
	if h.Records[0].Outcome != "ended" || h.Records[1].Outcome != "placed" {
		t.Fatal("incorrect answer/termination semantics", h)
	}
	p := readTest(t, s.path, Query{Limit: 100, EventType: ChannelTransfer})
	if len(p.Events) != 1 {
		t.Fatal("transfer observation missing")
	}
}

func TestCELEmergencyAndInboundFailover(t *testing.T) {
	path, db := celFixture(t)
	s := testStore(t, Options{})
	celPut(t, db, "APP_START", "emergency", "emergency", "cmm-emergency", "911", "Dial", 0)
	celPut(t, db, "BRIDGE_ENTER", "emergency", "emergency", "cmm-emergency", "911", "Dial", 1)
	celPut(t, db, "CHAN_END", "emergency", "emergency", "internal", "", "", 3)
	celPut(t, db, "APP_START", "fallback", "fallback", "inbound-fallback", "", "Dial", 4)
	celPut(t, db, "BRIDGE_ENTER", "fallback", "fallback", "inbound-fallback", "", "Dial", 5)
	celPut(t, db, "CHAN_END", "fallback", "fallback", "inbound-fallback", "", "", 6)
	ingest(t, s, path)
	h, err := ReadCalls(context.Background(), s.path, calls.Filter{}, true)
	if err != nil || len(h.Records) != 2 {
		t.Fatal(h, err)
	}
	if h.Records[0].Dialled != "911" || h.Records[0].Direction != "outbound" || h.Records[1].Direction != "inbound" || h.Records[1].Outcome != "ended" {
		t.Fatal(h)
	}
}
func TestCELBoundedBatchAndInvalidRows(t *testing.T) {
	path, db := celFixture(t)
	s := testStore(t, Options{})
	for i := 0; i < 130; i++ {
		celPut(t, db, "CHAN_START", "channel", "channel", "internal", "", "", i)
	}
	ingest(t, s, path)
	var after int
	if err := s.db.QueryRow("SELECT through FROM cel_progress").Scan(&after); err != nil || after != 128 {
		t.Fatal(after, err)
	}
	ingest(t, s, path)
	if err := s.db.QueryRow("SELECT through FROM cel_progress").Scan(&after); err != nil || after != 130 {
		t.Fatal(after, err)
	}
	celPut(t, db, "UNRECOGNISED", "channel", "channel", "internal", "", "", 140)
	celPut(t, db, "CHAN_END", "channel", "channel", "internal", "", "", 141)
	if _, err := db.Exec("UPDATE doorman_cel SET eventtime='not-an-epoch' WHERE AcctId=132"); err != nil {
		t.Fatal(err)
	}
	ingest(t, s, path)
	p := readTest(t, s.path, Query{Limit: 100, EventType: CoverageGap})
	if len(p.Events) != 2 {
		t.Fatal("invalid source rows did not report gaps")
	}
}
func TestCELUnavailableSourceDoesNotAdvance(t *testing.T) {
	path, db := celFixture(t)
	s := testStore(t, Options{})
	celPut(t, db, "CHAN_START", "a", "a", "inbound-trunk", "", "", 0)
	ingest(t, s, path)
	if err := s.ingestCEL(context.Background(), path+".missing"); err == nil {
		t.Fatal("missing source accepted")
	}
	if _, err := os.Stat(path + ".missing"); !os.IsNotExist(err) {
		t.Fatal("reader created source")
	}
	celPut(t, db, "CHAN_END", "a", "a", "inbound-trunk", "", "", 1)
	ingest(t, s, path)
	h, err := ReadCalls(context.Background(), s.path, calls.Filter{}, true)
	if err != nil || len(h.Records) != 1 {
		t.Fatal("failed to catch up", h, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err = s.ingestCEL(ctx, path); err == nil {
		t.Fatal("cancelled import succeeded")
	}
}
func TestCELMigrationFromVersionTwo(t *testing.T) {
	s := testStore(t, Options{})
	completed(t, s, "prior", callRecords()[0])
	before := readTest(t, s.path, Query{Limit: 100})
	if _, err := s.db.Exec("DROP TABLE cel_progress; DROP TABLE cel_channels; PRAGMA user_version=2;"); err != nil {
		t.Fatal(err)
	}
	if err := s.db.Close(); err != nil {
		t.Fatal(err)
	}
	newer, err := openStore(s.path, testOptions())
	if err != nil {
		t.Fatal(err)
	}
	defer newer.db.Close()
	after := readTest(t, newer.path, Query{Limit: 100})
	if before.JournalID != after.JournalID || before.HighWatermark != after.HighWatermark {
		t.Fatal("migration changed identity/history")
	}
	var version int
	if err = newer.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != currentSchema {
		t.Fatal(version, err)
	}
}

func TestCELWriterPollsAndResumesAcrossRestarts(t *testing.T) {
	source, db := celFixture(t)
	path := filepath.Join(t.TempDir(), "journal", "events.db")
	o := Options{CELPath: source, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	celPut(t, db, "APP_START", "out", "out", "cmm-outbound", "5125550101", "Dial", 0)
	w := Start(path, o)
	waitFor := func(kind Type) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			p, err := Read(context.Background(), path, Query{Limit: 100, EventType: kind})
			if err == nil && len(p.Events) == 1 {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("writer did not poll CEL source")
	}
	waitFor(ChannelDialStarted)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	celPut(t, db, "CHAN_END", "out", "out", "cmm-outbound", "", "", 5)
	w = Start(path, o)
	defer w.Close()
	waitFor(CallFinished)
	p, err := Read(context.Background(), path, Query{Limit: 100, EventType: ChannelDialStarted})
	if err != nil || len(p.Events) != 1 {
		t.Fatal("restart duplicated earlier source row", err)
	}
}

func TestCELRejectsBackendAutoCreatedTable(t *testing.T) {
	path, db := celFixture(t)
	_, err := db.Exec("DROP TABLE doorman_cel; CREATE TABLE doorman_cel(AcctId INTEGER PRIMARY KEY);")
	if err != nil {
		t.Fatal(err)
	}
	if err = CheckCELPath(context.Background(), path); err == nil {
		t.Fatal("unsafe source sequence accepted")
	}
	s := testStore(t, Options{})
	if err = s.ingestCEL(context.Background(), path); err == nil {
		t.Fatal("ingested unsafe source")
	}
}
