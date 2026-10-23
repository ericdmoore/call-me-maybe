package syncmachine

import (
	"os"
	"slices"
	"testing"
	"time"

	"callmemaybe/internal/syncwire"
)

func TestAwaitingApplyTracksSavedReceiptsUntilAppliedOrExpired(t *testing.T) {
	m, p, _ := fixture(t)
	if waiting, ready, err := m.AwaitingApply(); err != nil || waiting != nil || ready != nil {
		t.Fatalf("an empty ledger is waiting: %v %v %v", waiting, ready, err)
	}
	s, err := m.Observe()
	if err != nil {
		t.Fatal(err)
	}
	q := queue(t, s, "policy.setSchedule")
	if r, err := m.Apply(q); err != nil || r.Status != syncwire.ReceiptStatusSaved {
		t.Fatalf("apply: %s %v", r.Status, err)
	}
	waiting, ready, err := m.AwaitingApply()
	if err != nil || !slices.Equal(waiting, []string{q.Id}) || ready != nil {
		t.Fatalf("saved, daemon unknown: %v %v %v", waiting, ready, err)
	}
	loaded := false
	m.Applied = func(Paths, map[string][]byte) bool { return loaded }
	if _, ready, _ = m.AwaitingApply(); ready != nil {
		t.Fatalf("ready before the daemon loaded it: %v", ready)
	}
	loaded = true
	if waiting, ready, err = m.AwaitingApply(); err != nil || !slices.Equal(waiting, []string{q.Id}) || !slices.Equal(ready, []string{q.Id}) {
		t.Fatalf("loaded but not ready: %v %v %v", waiting, ready, err)
	}

	// The command's expiry ends the wait even if nothing ever upgrades it.
	m.now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	if waiting, _, _ = m.AwaitingApply(); waiting != nil {
		t.Fatalf("an expired command is still waiting: %v", waiting)
	}
	m.now = time.Now

	// Unreadable files: still waiting, nothing proven.
	good, _ := os.ReadFile(p.Policy)
	if err := os.WriteFile(p.Policy, []byte("[house\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if waiting, ready, err = m.AwaitingApply(); err != nil || len(waiting) != 1 || ready != nil {
		t.Fatalf("broken file: %v %v %v", waiting, ready, err)
	}
	// A later local edit replaced the written bytes: Apply can never upgrade
	// it, so it is not waited for.
	edited := append(append([]byte{}, good...), []byte("\n# a local note\n")...)
	if err := os.WriteFile(p.Policy, edited, 0o600); err != nil {
		t.Fatal(err)
	}
	if waiting, ready, err = m.AwaitingApply(); err != nil || waiting != nil || ready != nil {
		t.Fatalf("overwritten write is still waiting: %v %v %v", waiting, ready, err)
	}
	if err := os.WriteFile(p.Policy, good, 0o600); err != nil {
		t.Fatal(err)
	}

	if r, err := m.Apply(q); err != nil || r.Status != syncwire.ReceiptStatusApplied {
		t.Fatalf("upgrade: %s %v", r.Status, err)
	}
	if waiting, ready, err = m.AwaitingApply(); err != nil || waiting != nil || ready != nil {
		t.Fatalf("applied is still waiting: %v %v %v", waiting, ready, err)
	}
}

func TestAwaitingApplyReportsALedgerItCannotRead(t *testing.T) {
	m, _, _ := fixture(t)
	if _, err := m.db.Exec("DROP TABLE commands"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.AwaitingApply(); err != ErrState {
		t.Fatalf("got %v", err)
	}
}
