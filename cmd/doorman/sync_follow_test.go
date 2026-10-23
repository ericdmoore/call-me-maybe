package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"callmemaybe/internal/activation"
	"callmemaybe/internal/policy"
	"callmemaybe/internal/syncmachine"
	"callmemaybe/internal/syncwire"
)

// quickFollow runs the follower's checks in milliseconds.
func quickFollow(t *testing.T, probe, fast, most time.Duration) {
	t.Helper()
	p, f, m := syncApplyProbe, syncApplyFast, syncApplyProbeMax
	syncApplyProbe, syncApplyFast, syncApplyProbeMax = probe, fast, most
	t.Cleanup(func() { syncApplyProbe, syncApplyFast, syncApplyProbeMax = p, f, m })
}

// probes is a fake machine answer that counts how often it was asked.
type probes struct {
	mu             sync.Mutex
	n              int
	waiting, ready []string
	err            error
}

func (p *probes) probe() ([]string, []string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.n++
	return slices.Clone(p.waiting), slices.Clone(p.ready), p.err
}

func (p *probes) set(waiting, ready []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.waiting, p.ready = waiting, ready
}

func (p *probes) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.n
}

func follow(t *testing.T, p *probes) (*applyFollow, *atomic.Int32) {
	t.Helper()
	var rings atomic.Int32
	f := newApplyFollow(p.probe, func() { rings.Add(1) })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); f.run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	return f, &rings
}

func TestNothingWaitingMeansNoChecksAndNoRings(t *testing.T) {
	quickFollow(t, time.Millisecond, 10*time.Millisecond, 5*time.Millisecond)
	var p probes
	f, rings := follow(t, &p)
	settle(t, "the first check", func() bool { return p.count() == 1 })
	time.Sleep(50 * time.Millisecond)
	if n := p.count(); n != 1 {
		t.Fatalf("checked %d times with nothing waiting", n)
	}
	f.kick()
	settle(t, "the check after a pass", func() bool { return p.count() == 2 })
	time.Sleep(30 * time.Millisecond)
	if n, r := p.count(), rings.Load(); n != 2 || r != 0 {
		t.Fatalf("idle follower: %d checks, %d rings", n, r)
	}
}

func TestAProvenReceiptRingsOnceAndNeverSpins(t *testing.T) {
	quickFollow(t, time.Millisecond, 20*time.Millisecond, 10*time.Millisecond)
	var p probes
	p.set([]string{"a"}, nil)
	f, rings := follow(t, &p)
	settle(t, "a few checks", func() bool { return p.count() >= 3 })
	if rings.Load() != 0 {
		t.Fatal("rang before the daemon loaded anything")
	}
	// Loaded, but the pass it rings for never upgrades it (the service
	// stopped offering it): one ring, not one per check.
	p.set([]string{"a"}, []string{"a"})
	settle(t, "the ring", func() bool { return rings.Load() == 1 })
	at := p.count()
	settle(t, "more checks", func() bool { return p.count() >= at+5 })
	if r := rings.Load(); r != 1 {
		t.Fatalf("rang %d times for one receipt", r)
	}
	// A second receipt proven later rings for itself.
	p.set([]string{"a", "b"}, []string{"a", "b"})
	f.kick()
	settle(t, "the second ring", func() bool { return rings.Load() == 2 })
	p.set(nil, nil)
	f.kick()
	time.Sleep(30 * time.Millisecond)
	at = p.count()
	time.Sleep(60 * time.Millisecond)
	if n := p.count(); n != at {
		t.Fatalf("kept checking after nothing was waiting: %d more", n-at)
	}
}

func TestANeverLoadedRevisionBacksOff(t *testing.T) {
	quickFollow(t, time.Millisecond, 10*time.Millisecond, 40*time.Millisecond)
	var p probes
	p.set([]string{"a"}, nil)
	follow(t, &p)
	time.Sleep(30 * time.Millisecond)
	early := p.count()
	time.Sleep(200 * time.Millisecond)
	late := p.count() - early
	// At the cap of 40 ms, 200 ms is about five checks; at the first gap of
	// 1 ms it would be about two hundred.
	if late > 12 {
		t.Fatalf("%d checks in 200 ms after the fast window: no backoff", late)
	}
	if late == 0 {
		t.Fatal("stopped checking altogether")
	}
}

func TestALedgerErrorIsCheckedAgainLaterNotInALoop(t *testing.T) {
	quickFollow(t, time.Millisecond, 0, 30*time.Millisecond)
	p := probes{err: errors.New("sync: state")}
	_, rings := follow(t, &p)
	time.Sleep(150 * time.Millisecond)
	if n := p.count(); n > 15 || rings.Load() != 0 {
		t.Fatalf("%d checks, %d rings on a broken ledger", n, rings.Load())
	}
}

// fakeSync is the hosted side as the box sees it: it offers a command until
// the box answers it with anything but saved, as the service does.
type fakeSync struct {
	mu       sync.Mutex
	item     *syncwire.QueueItem
	receipts []syncwire.Receipt
}

func (s *fakeSync) transport(_ context.Context, endpoint string, body []byte) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	links := map[string]any{"self": map[string]any{"href": "/v1/sync/"}, "up": map[string]any{"href": "/v1/"}}
	var result any
	switch endpoint {
	case "snapshot":
		var snap syncwire.Snapshot
		if err := syncwire.Decode("Snapshot", body, &snap); err != nil {
			return nil, err
		}
		result = map[string]any{"status": "accepted", "generation": snap.Generation, "revision": snap.Revision, "_links": links}
	case "queue":
		items := []syncwire.QueueItem{}
		if s.item != nil {
			items = append(items, *s.item)
		}
		result = map[string]any{"items": items, "pollAfterMs": 5000, "_links": links}
	case "receipts":
		var batch syncwire.ReceiptBatch
		if err := syncwire.Decode("ReceiptBatch", body, &batch); err != nil {
			return nil, err
		}
		r := batch.Receipts[0]
		s.receipts = append(s.receipts, r)
		if r.Status != syncwire.ReceiptStatusSaved {
			s.item = nil
		}
		result = map[string]any{"receipts": []any{map[string]any{"id": r.Id, "status": "recorded"}}, "_links": links}
	default:
		return nil, errors.New("unexpected endpoint")
	}
	return json.Marshal(result)
}

func (s *fakeSync) statuses() []syncwire.ReceiptStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []syncwire.ReceiptStatus
	for _, r := range s.receipts {
		out = append(out, r.Status)
	}
	return out
}

// The live report: a web edit reached the box over the bell and the daemon
// reloaded it in about a second, but the receipt said saved until the next
// five-minute poll. Here the real daemon line store reloads the file, the
// real activation socket reports it, and the bell is up with an hour between
// polls: the receipt must still turn applied within about two seconds.
func TestASavedReceiptTurnsAppliedPromptlyWhileTheBellIsUp(t *testing.T) {
	fastCadence(t)
	// A short directory: a unix socket path has to fit in about 100 bytes.
	dir, err := os.MkdirTemp("", "cmm")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	policyPath, handsetsPath := filepath.Join(dir, "policy.toml"), filepath.Join(dir, "handsets.toml")
	body := goodPolicy + `
[[schedules]]
id = "bedtime"
start = "21:00"
end = "07:00"
days = ["MO", "TU"]
`
	if os.WriteFile(policyPath, []byte(body), 0o600) != nil || os.WriteFile(handsetsPath, []byte(goodHandsets), 0o600) != nil {
		t.Fatal("write fixture")
	}

	// The daemon: its line store reloads the file, and serves activation.
	files, _ := policy.DiscoverLines(policyPath)
	opened, err := openLines(files, handsetsPath, filepath.Join(dir, "trunks.toml"), true, quiet(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, o := range opened {
			o.store.Close()
		}
	})
	state := filepath.Join(dir, "sync")
	m, err := syncmachine.Open(state, "ac409cd3-8105-4200-9a32-42a064a98bb0", syncmachine.Paths{Policy: policyPath, Handsets: handsetsPath})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	m.Warn = func(error) {}
	m.Applied = activationApplied(state)
	served, stopServing := context.WithCancel(context.Background())
	serving := make(chan struct{})
	go func() {
		defer close(serving)
		activation.Serve(served, state, func() map[string]string {
			return map[string]string{opened[0].name: opened[0].store.Current().SourceDigest()}
		})
	}()
	t.Cleanup(func() { stopServing(); <-serving })
	before := opened[0].store.Current().SourceDigest()
	settle(t, "the activation socket", func() bool {
		current, err := activation.Read(state)
		return err == nil && current[opened[0].name] == before
	})

	// The web edit, waiting in the queue.
	snap, err := m.Observe()
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{
		"id": "6f1c1d4e-3c39-4a55-9a51-7c1b1f0c2a10", "base": map[string]any{"generation": snap.Generation, "revision": snap.Revision},
		"notAfter": time.Now().Add(time.Hour).UnixMilli(), "createdAt": time.Now().UnixMilli(),
		"author": map[string]any{"kind": "person", "person": "0b6b1d2e-5f4a-4c3b-8a29-1e0d9c8b7a65"},
		"verb":   map[string]any{"name": "policy.setSchedule", "policy": "default", "schedule": map[string]any{"id": "bedtime", "start": "22:00", "end": "07:00", "days": []string{"MO", "TU"}, "enabled": true}}})
	var item syncwire.QueueItem
	if err := syncwire.Decode("QueueItem", raw, &item); err != nil {
		t.Fatal(err)
	}
	svc := &fakeSync{item: &item}

	// The sync process, bell up, an hour between polls.
	wake := newSyncWake()
	wake.slow = time.Hour
	wake.setBell(true)
	f := newApplyFollow(m.AwaitingApply, wake.ring)
	var passes atomic.Int32
	exchange := func(ctx context.Context) error {
		passes.Add(1)
		err := m.Exchange(ctx, svc.transport)
		f.kick()
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	followed := make(chan struct{})
	go func() { defer close(followed); f.run(ctx) }()
	t.Cleanup(func() { cancel(); <-followed })
	loop(t, exchange, wake)

	settle(t, "the saved receipt", func() bool { return len(svc.statuses()) == 1 })
	if got := svc.statuses(); got[0] != syncwire.ReceiptStatusSaved {
		t.Fatalf("first answer %v, want saved before the daemon reloads", got)
	}
	deadline := time.Now().Add(5 * time.Second)
	for opened[0].store.Current().SourceDigest() == before {
		if time.Now().After(deadline) {
			t.Fatal("the daemon never reloaded the written file")
		}
		time.Sleep(5 * time.Millisecond)
	}
	reloaded := time.Now()
	settle(t, "the applied receipt", func() bool {
		got := svc.statuses()
		return len(got) > 0 && got[len(got)-1] == syncwire.ReceiptStatusApplied
	})
	lag := time.Since(reloaded)
	if lag > 2*time.Second {
		t.Fatalf("applied %v after the daemon reloaded", lag)
	}
	t.Logf("applied %v after the daemon reloaded", lag.Round(time.Millisecond))
	if n := passes.Load(); n != 2 {
		t.Fatalf("%d passes, want the first and the one rung for the upgrade", n)
	}
	// Nothing waiting now: no more passes until the hour is up.
	time.Sleep(1200 * time.Millisecond)
	if n := passes.Load(); n != 2 {
		t.Fatalf("%d passes once nothing was waiting", n)
	}
}
