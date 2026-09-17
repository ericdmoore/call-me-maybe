package events

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"callmemaybe/internal/calls"
)

func completed(t *testing.T, s *store, id string, r calls.Record) {
	t.Helper()
	if err := s.append(context.Background(), Call(SessionFinished, id, r, "")); err != nil {
		t.Fatal(err)
	}
}
func callRecords() []calls.Record {
	start := time.Date(2026, 9, 17, 12, 0, 0, 123456789, time.UTC)
	return []calls.Record{
		{ID: "same-short-id", Start: start, Caller: "+15125550100", Known: "Grandma", Outcome: calls.OutcomeAnswered, MS: 1000},
		{ID: "same-short-id", Start: start.Add(time.Hour), Caller: "+15125550101", Line: "biz", Outcome: calls.OutcomeDismissed, Reason: "no-digits"},
		{ID: "out", Start: start.Add(2 * time.Hour), Direction: calls.DirectionOutbound, Line: "biz", Dialled: "+15125550102", Outcome: calls.OutcomePlaced},
	}
}
func TestCallProjectionMatchesLegacyFiltersAndRedaction(t *testing.T) {
	s := testStore(t, Options{})
	records := callRecords()
	for i, r := range records {
		completed(t, s, []string{"full-1", "full-2", "full-3"}[i], r)
	}
	legacyPath := filepath.Join(t.TempDir(), "calls.jsonl")
	legacy, err := calls.Open(legacyPath, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range records {
		legacy.Post(r)
	}
	if err = legacy.Close(); err != nil {
		t.Fatal(err)
	}
	filters := []calls.Filter{
		{}, {Limit: 1}, {Limit: 2}, {Line: "default"}, {Line: "biz"}, {Direction: "inbound"}, {Direction: "outbound"},
		{Outcome: "dismissed"}, {Caller: "0102"}, {Caller: "%"}, {Caller: "' OR 1=1 --"},
		{Since: records[1].Start}, {Since: records[1].Start.Add(time.Nanosecond)},
		{Since: records[1].Start.In(time.FixedZone("offset", -6*3600)), Limit: 1, Line: "biz"},
	}
	for _, f := range filters {
		want, _, err := calls.Read(legacyPath, f)
		if err != nil {
			t.Fatal(err)
		}
		got, err := ReadCalls(context.Background(), s.path, f, true)
		if err != nil {
			t.Fatal(err)
		}
		if len(want) == 0 {
			want = []calls.Record{}
		}
		if !reflect.DeepEqual(got.Records, want) {
			t.Errorf("filter %+v: got %+v want %+v", f, got.Records, want)
		}
	}
	got, err := ReadCalls(context.Background(), s.path, calls.Filter{}, false)
	if err != nil {
		t.Fatal(err)
	}
	for i, r := range got.Records {
		if !reflect.DeepEqual(r, records[i].Redacted()) {
			t.Error("redaction differs", i)
		}
	}
}
func TestCallProjectionUsesLatestSummaryAndFullIdentity(t *testing.T) {
	s := testStore(t, Options{})
	r := callRecords()[0]
	completed(t, s, "channel-a", r)
	r.Outcome = calls.OutcomeDismissed
	completed(t, s, "channel-b", r)
	r.Outcome = calls.OutcomeVoicemail
	completed(t, s, "channel-a", r)
	if err := s.append(context.Background(), Call(CallObserved, "active-channel", r, "")); err != nil {
		t.Fatal(err)
	}
	got, err := ReadCalls(context.Background(), s.path, calls.Filter{}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Records) != 2 || got.Records[0].Outcome != calls.OutcomeDismissed || got.Records[1].Outcome != calls.OutcomeVoicemail {
		t.Fatalf("summaries: %+v", got)
	}
	got, err = ReadCalls(context.Background(), s.path, calls.Filter{Outcome: calls.OutcomeAnswered}, true)
	if err != nil || len(got.Records) != 0 {
		t.Fatal("filter resurrected an obsolete summary", err)
	}
}
func TestEventQueryFiltersAndFiniteBatch(t *testing.T) {
	s := testStore(t, Options{})
	records := callRecords()
	appendTest(t, s, DaemonStarted)
	for i, r := range records {
		completed(t, s, []string{"full-1", "full-2", "full-3"}[i], r)
	}
	config := System(ConfigReloaded, "", 0)
	config.Line = "biz"
	if err := s.append(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		q   Query
		seq []int64
	}{
		{Query{Limit: 10, Direction: "inbound"}, []int64{2, 3}},
		{Query{Limit: 10, Direction: "outbound"}, []int64{4}},
		{Query{Limit: 10, Line: "default"}, []int64{2}},
		{Query{Limit: 10, Line: "biz"}, []int64{3, 4, 5}},
		{Query{Limit: 10, Line: "biz", Direction: "inbound"}, []int64{3}},
		{Query{Limit: 10, CallID: "full-3"}, []int64{4}},
		{Query{Limit: 10, CallID: "' OR 1=1 --"}, nil},
	} {
		p := readTest(t, s.path, tc.q)
		var got []int64
		for _, e := range p.Events {
			got = append(got, e.Sequence)
		}
		if !reflect.DeepEqual(got, tc.seq) {
			t.Errorf("query %+v: %v != %v", tc.q, got, tc.seq)
		}
		if p.NextCursor != 5 {
			t.Error("filtered page did not advance to scanned end", p.NextCursor)
		}
	}
	first := readTest(t, s.path, Query{Limit: 1, Direction: "inbound"})
	appendTest(t, s, DaemonStarted)
	second := readTest(t, s.path, Query{After: first.NextCursor, Limit: 10, Direction: "inbound", Through: &first.Through})
	if len(second.Events) != 1 || second.NextCursor != 5 || second.Through != 5 || second.HighWatermark != 6 {
		t.Fatalf("moving batch boundary: %+v", second)
	}
	zero := int64(0)
	empty := readTest(t, s.path, Query{Limit: 10, Through: &zero})
	if empty.NextCursor != 0 || len(empty.Events) != 0 {
		t.Fatal("explicit zero boundary ignored")
	}
	for _, n := range []int64{-1, 7} {
		if _, err := Read(context.Background(), s.path, Query{Limit: 1, Through: &n}); err == nil {
			t.Fatal("invalid bound accepted", n)
		}
	}
}
func TestSparseFilterStopsAtScanBudget(t *testing.T) {
	s := testStore(t, Options{})
	_, err := s.db.Exec(`WITH RECURSIVE numbers(n) AS (VALUES(1) UNION ALL SELECT n+1 FROM numbers WHERE n<10002)
 INSERT INTO events(event_id,type,version,occurred_at,recorded_at,source,call_id,line,payload)
 SELECT CAST(n AS TEXT),CASE WHEN n=10001 THEN 'call.answered' ELSE 'daemon.started' END,1,
 '2026-09-17T00:00:00Z','2026-09-17T00:00:00Z','fixture','','','{}' FROM numbers;
 UPDATE metadata SET high=10002;`)
	if err != nil {
		t.Fatal(err)
	}
	first := readTest(t, s.path, Query{Limit: 1, EventType: CallAnswered})
	if len(first.Events) != 0 || first.NextCursor != 10000 {
		t.Fatalf("unbounded sparse scan: %+v", first)
	}
	second := readTest(t, s.path, Query{After: first.NextCursor, Limit: 10, EventType: CallAnswered, Through: &first.Through})
	if len(second.Events) != 1 || second.Events[0].Sequence != 10001 || second.NextCursor != 10002 {
		t.Fatalf("lost matching event: %+v", second)
	}
}
func downgradeFixture(t *testing.T, s *store) {
	t.Helper()
	_, err := s.db.Exec(`DROP TABLE cel_progress; DROP TABLE cel_channels; DROP VIEW public_calls_v1; DROP INDEX events_type_sequence; DROP INDEX events_call_sequence; DROP INDEX events_line_sequence; PRAGMA user_version=1;`)
	if err != nil {
		t.Fatal(err)
	}
}
func TestQueryMigrationPreservesHistoryIdentityAndDelivery(t *testing.T) {
	s := testStore(t, Options{})
	completed(t, s, "channel", callRecords()[0])
	before := readTest(t, s.path, Query{Limit: 10})
	if _, err := s.db.Exec("INSERT INTO delivery_state VALUES('consumer',1)"); err != nil {
		t.Fatal(err)
	}
	downgradeFixture(t, s)
	old, err := ReadCalls(context.Background(), s.path, calls.Filter{}, true)
	if err != nil || len(old.Records) != 1 {
		t.Fatal("read-only v1 support failed", err)
	}
	var version int
	if err = s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 1 {
		t.Fatal("reader migrated database")
	}
	if err = s.db.Close(); err != nil {
		t.Fatal(err)
	}
	migrated, err := openStore(s.path, testOptions())
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.db.Close()
	after := readTest(t, s.path, Query{Limit: 10})
	if !reflect.DeepEqual(before, after) {
		t.Fatal("migration changed event history or identity")
	}
	var progress int
	if err = migrated.db.QueryRow("SELECT through FROM delivery_state WHERE target='consumer'").Scan(&progress); err != nil || progress != 1 {
		t.Fatal("delivery progress lost", err)
	}
	if err = migrated.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != currentSchema {
		t.Fatal("migration version missing")
	}
}
func TestFailedQueryMigrationRollsBack(t *testing.T) {
	s := testStore(t, Options{})
	completed(t, s, "channel", callRecords()[0])
	downgradeFixture(t, s)
	// Fail after the first index is created, proving the whole migration rolls back.
	if _, err := s.db.Exec("CREATE TABLE events_call_sequence (value INTEGER)"); err != nil {
		t.Fatal(err)
	}
	if _, err := openStore(s.path, testOptions()); err == nil {
		t.Fatal("conflicting migration accepted")
	}
	var version, count int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 1 {
		t.Fatal("schema version advanced on failure")
	}
	if err := s.db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name='events_type_sequence'").Scan(&count); err != nil || count != 0 {
		t.Fatal("partial index survived rollback")
	}
	got, err := ReadCalls(context.Background(), s.path, calls.Filter{}, true)
	if err != nil || len(got.Records) != 1 {
		t.Fatal("migration damaged existing history", err)
	}
}
