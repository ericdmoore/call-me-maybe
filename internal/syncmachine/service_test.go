package syncmachine

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"callmemaybe/internal/syncwire"
)

// service is a fake hosted side: it records what the box sends and answers
// with whatever the test has set.
type service struct {
	t          *testing.T
	items      []syncwire.QueueItem
	statusErr  error
	answer     syncwire.ReceiptResultStatus
	snapshots  []syncwire.Snapshot
	receipts   []syncwire.Receipt
	calls      []string
	refuseFrom string
}

func (s *service) transport(_ context.Context, endpoint string, body []byte) ([]byte, error) {
	links := map[string]any{"self": map[string]any{"href": "/v1/sync/"}, "up": map[string]any{"href": "/v1/"}}
	s.calls = append(s.calls, endpoint)
	var result any
	switch endpoint {
	case "status":
		if s.statusErr != nil {
			return nil, s.statusErr
		}
		result = map[string]any{"status": "accepted", "_links": links}
	case "snapshot":
		var current syncwire.Snapshot
		if err := syncwire.Decode("Snapshot", body, &current); err != nil {
			s.t.Fatal(err)
		}
		s.snapshots = append(s.snapshots, current)
		result = map[string]any{"status": "accepted", "generation": current.Generation, "revision": current.Revision, "_links": links}
	case "queue":
		items := s.items
		if items == nil {
			items = []syncwire.QueueItem{}
		}
		result = map[string]any{"items": items, "pollAfterMs": 5000, "_links": links}
	case "receipts":
		var batch syncwire.ReceiptBatch
		if err := syncwire.Decode("ReceiptBatch", body, &batch); err != nil {
			s.t.Fatal(err)
		}
		r := batch.Receipts[0]
		s.receipts = append(s.receipts, r)
		answer := syncwire.ReceiptResultStatusRecorded
		if s.answer != "" {
			answer = s.answer
		}
		result = map[string]any{"receipts": []any{map[string]any{"id": r.Id, "status": answer}}, "_links": links}
	default:
		s.t.Fatal("unexpected transport endpoint")
	}
	return json.Marshal(result)
}

func (s *service) last(id string) syncwire.Receipt {
	s.t.Helper()
	for i := len(s.receipts) - 1; i >= 0; i-- {
		if s.receipts[i].Id == id {
			return s.receipts[i]
		}
	}
	s.t.Fatal("no receipt sent")
	return syncwire.Receipt{}
}

func TestRefusedStatusDoesNotHoldTheQueue(t *testing.T) {
	m, _, _ := fixture(t)
	s, _ := m.Observe()
	q := queue(t, s, "policy.setSchedule")
	var warnings []error
	m.Warn = func(err error) { warnings = append(warnings, err) }
	m.SampleStatus = func() syncwire.Status {
		return syncwire.Status{Schema: 1, Version: "test", SampledAt: time.Now().UnixMilli()}
	}
	svc := &service{t: t, items: []syncwire.QueueItem{q}, statusErr: errors.New("account: service refused request (400, bad_request)")}
	for range 2 {
		if err := m.Exchange(context.Background(), svc.transport); err != nil {
			t.Fatal(err)
		}
	}
	if svc.last(q.Id).Status != syncwire.ReceiptStatusSaved {
		t.Fatal("a refused status report stopped the command")
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0].Error(), "status report not accepted") {
		t.Fatalf("expected one categorical warning, got %v", warnings)
	}
	svc.statusErr = nil
	if err := m.Exchange(context.Background(), svc.transport); err != nil || len(warnings) != 1 {
		t.Fatal("recovered status warned again")
	}
}

func TestUnchangedSnapshotIsNotResent(t *testing.T) {
	m, p, _ := fixture(t)
	now := time.Now()
	m.now = func() time.Time { return now }
	svc := &service{t: t}
	for range 3 {
		if err := m.Exchange(context.Background(), svc.transport); err != nil {
			t.Fatal(err)
		}
	}
	if len(svc.snapshots) != 1 {
		t.Fatalf("unchanged snapshot sent %d times", len(svc.snapshots))
	}
	if err := os.WriteFile(p.Policy, []byte(strings.Replace(policyFixture, `"21:00"`, `"20:30"`, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := m.Exchange(context.Background(), svc.transport); err != nil || len(svc.snapshots) != 2 {
		t.Fatal("a local edit was not published")
	}
	now = now.Add(republishAfter)
	if err := m.Exchange(context.Background(), svc.transport); err != nil || len(svc.snapshots) != 3 {
		t.Fatal("an unchanged snapshot was never refreshed")
	}
	if svc.snapshots[2].Revision != svc.snapshots[1].Revision {
		t.Fatal("refresh changed the revision")
	}
}

func TestSymlinkedPolicyFailsTheCommandAndTheQueueAdvances(t *testing.T) {
	m, p, _ := fixture(t)
	real := filepath.Join(t.TempDir(), "policy.toml")
	if err := os.Rename(p.Policy, real); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, p.Policy); err != nil {
		t.Fatal(err)
	}
	s, err := m.Observe()
	if err != nil {
		t.Fatal(err)
	}
	blocked, next := queue(t, s, "policy.setSchedule"), queue(t, s, "phonebook.assign")
	svc := &service{t: t, items: []syncwire.QueueItem{blocked, next}}
	for range 2 {
		if err := m.Exchange(context.Background(), svc.transport); err != nil {
			t.Fatal(err)
		}
	}
	r := svc.last(blocked.Id)
	if r.Status != syncwire.ReceiptStatusFailed || r.Problem == nil || *r.Problem != syncwire.ExecutionFailed {
		t.Fatalf("expected failed/execution_failed, got %s", r.Status)
	}
	if got := svc.last(next.Id).Status; got != syncwire.ReceiptStatusSaved {
		t.Fatalf("later command did not run: %s", got)
	}
	data, _ := os.ReadFile(real)
	if string(data) != policyFixture {
		t.Fatal("a failed command changed the policy")
	}
	if info, err := os.Lstat(p.Policy); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("replaced the operator's symlink")
	}
	var target, before, after *string
	if err := m.db.QueryRow("SELECT target_path,before_hash,after_hash FROM commands WHERE id=?", blocked.Id).Scan(&target, &before, &after); err != nil {
		t.Fatal(err)
	}
	if target != nil || before != nil || after != nil {
		t.Fatal("prepared intent left behind")
	}
}

func TestSavedSurvivesUnrelatedEditsAndAConflictDoesNotStopThePass(t *testing.T) {
	m, p, _ := fixture(t)
	s, _ := m.Observe()
	q := queue(t, s, "policy.setSchedule")
	svc := &service{t: t, items: []syncwire.QueueItem{q}}
	if err := m.Exchange(context.Background(), svc.transport); err != nil {
		t.Fatal(err)
	}
	if svc.last(q.Id).Status != syncwire.ReceiptStatusSaved {
		t.Fatal("not saved")
	}
	handsets, _ := os.ReadFile(p.Handsets)
	if err := os.WriteFile(p.Handsets, []byte(strings.Replace(string(handsets), `label = "Kitchen"`, `label = "Kitchen wall"`, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	// Redelivery after an unrelated edit keeps the honest answer.
	if err := m.Exchange(context.Background(), svc.transport); err != nil {
		t.Fatal(err)
	}
	if got := svc.last(q.Id); got.Status != syncwire.ReceiptStatusSaved || got.Problem != nil {
		t.Fatalf("unrelated edit downgraded a saved receipt to %s", got.Status)
	}
	m.Applied = func(Paths, map[string][]byte) bool { return true }
	// The service disagrees about the first command; the second still runs.
	latest, _ := m.Latest()
	assign := queue(t, latest, "phonebook.assign")
	svc.items, svc.answer = []syncwire.QueueItem{q, assign}, syncwire.ReceiptResultStatusConflict
	var warnings []error
	m.Warn = func(err error) { warnings = append(warnings, err) }
	if err := m.Exchange(context.Background(), svc.transport); err != nil {
		t.Fatal(err)
	}
	if got := svc.last(q.Id).Status; got != syncwire.ReceiptStatusApplied {
		t.Fatalf("saved was not upgraded: %s", got)
	}
	if got := svc.last(assign.Id).Status; got != syncwire.ReceiptStatusApplied {
		t.Fatalf("a receipt conflict stopped the pass: %s", got)
	}
	if len(warnings) != 1 || !errors.Is(warnings[0], ErrReceiptConflict) {
		t.Fatalf("expected one receipt warning, got %v", warnings)
	}
	// An edit to the target itself stops the upgrade but never downgrades.
	if _, err := m.db.Exec("UPDATE commands SET receipt=json_set(receipt,'$.status','saved') WHERE id=?", q.Id); err != nil {
		t.Fatal(err)
	}
	policyNow, _ := os.ReadFile(p.Policy)
	if err := os.WriteFile(p.Policy, []byte(strings.Replace(string(policyNow), `"07:00"`, `"06:30"`, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	m.Applied = func(Paths, map[string][]byte) bool { return true }
	r, err := m.Apply(q)
	if err != nil || r.Status != syncwire.ReceiptStatusSaved {
		t.Fatalf("target edit changed a saved receipt: %s %v", r.Status, err)
	}
}

func TestCrashAfterRenameThenUnrelatedEditStillSaves(t *testing.T) {
	m, p, _ := fixture(t)
	s, _ := m.Observe()
	q := queue(t, s, "policy.setSchedule")
	if _, err := m.Apply(q); err != nil {
		t.Fatal(err)
	}
	if _, err := m.db.Exec("UPDATE commands SET receipt=NULL WHERE id=?", q.Id); err != nil {
		t.Fatal(err)
	}
	handsets, _ := os.ReadFile(p.Handsets)
	if err := os.WriteFile(p.Handsets, append(handsets, []byte("# an unrelated note\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := m.Apply(q)
	if err != nil || r.Status != syncwire.ReceiptStatusSaved {
		t.Fatalf("landed write reported as %s %v", r.Status, err)
	}
}

func TestUnreadableFilesStoreNoReceipt(t *testing.T) {
	m, p, _ := fixture(t)
	s, _ := m.Observe()
	q := queue(t, s, "policy.setSchedule")
	if err := os.WriteFile(p.Policy, []byte("broken = ["), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Apply(q); err == nil {
		t.Fatal("answered a command against unreadable files")
	}
	var receipt *string
	if err := m.db.QueryRow("SELECT receipt FROM commands WHERE id=?", q.Id).Scan(&receipt); err != nil || receipt != nil {
		t.Fatal("stored a final answer for a passing problem")
	}
	if err := os.WriteFile(p.Policy, []byte(policyFixture), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := m.Apply(q)
	if err != nil || r.Status != syncwire.ReceiptStatusSaved {
		t.Fatalf("retry did not apply: %s %v", r.Status, err)
	}
}
