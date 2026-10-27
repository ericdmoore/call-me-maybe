package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"callmemaybe/internal/account"
	"callmemaybe/internal/bell"

	"github.com/gorilla/websocket"
)

// fastCadence runs the loop in milliseconds: the ordinary interval becomes
// 20 ms, and the floor between passes 1 ms.
func fastCadence(t *testing.T) {
	t.Helper()
	interval, gap := syncInterval, syncMinGap
	syncInterval, syncMinGap = 20*time.Millisecond, time.Millisecond
	t.Cleanup(func() { syncInterval, syncMinGap = interval, gap })
}

// passes is a fake exchange that counts, and optionally holds a pass open.
type passes struct {
	n    atomic.Int32
	hold chan struct{} // when non-nil, every pass waits for a value
	in   chan struct{} // receives once per pass started
}

func (p *passes) exchange(ctx context.Context) error {
	p.n.Add(1)
	if p.in != nil {
		p.in <- struct{}{}
	}
	if p.hold != nil {
		select {
		case <-p.hold:
		case <-ctx.Done():
		}
	}
	return nil
}

func loop(t *testing.T, exchange func(context.Context) error, wake *syncWake) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() { done <- syncLoop(ctx, exchange, wake, false) }()
	t.Cleanup(func() {
		cancel()
		if rc := <-done; rc != 0 {
			t.Errorf("loop exited %d on shutdown", rc)
		}
	})
	return cancel
}

func settle(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestARingRunsTheOrdinarySyncNow(t *testing.T) {
	fastCadence(t)
	wake := newSyncWake()
	wake.slow = time.Hour
	wake.setBell(true)
	var p passes
	loop(t, p.exchange, wake)
	settle(t, "the first pass", func() bool { return p.n.Load() == 1 })
	time.Sleep(60 * time.Millisecond)
	if p.n.Load() != 1 {
		t.Fatal("polled at the ordinary cadence while the bell was up")
	}
	wake.ring()
	settle(t, "the rung pass", func() bool { return p.n.Load() == 2 })
}

func TestABurstOfRingsDuringAPassMergesIntoOneMorePass(t *testing.T) {
	fastCadence(t)
	wake := newSyncWake()
	wake.slow = time.Hour
	wake.setBell(true)
	p := passes{hold: make(chan struct{}), in: make(chan struct{}, 16)}
	loop(t, p.exchange, wake)
	<-p.in
	for range 10 {
		wake.ring()
	}
	p.hold <- struct{}{} // finish pass 1
	<-p.in               // the one merged pass starts
	for range 10 {
		wake.ring() // rings during that pass merge again
	}
	p.hold <- struct{}{}
	<-p.in
	p.hold <- struct{}{}
	select {
	case <-p.in:
		t.Fatalf("ran a fourth pass for one burst: %d", p.n.Load())
	case <-time.After(80 * time.Millisecond):
	}
	if n := p.n.Load(); n != 3 {
		t.Fatalf("%d passes, want the first, one per burst", n)
	}
}

func TestTheBellGoingDownBringsBackTheOrdinaryCadence(t *testing.T) {
	fastCadence(t)
	wake := newSyncWake()
	wake.slow = time.Hour
	var p passes
	wake.setBell(true)
	loop(t, p.exchange, wake)
	settle(t, "the first pass", func() bool { return p.n.Load() == 1 })
	time.Sleep(50 * time.Millisecond)
	if p.n.Load() != 1 {
		t.Fatal("not on the safety net while the bell was up")
	}
	wake.setBell(false)
	settle(t, "ordinary polling again", func() bool { return p.n.Load() >= 4 })
	// And up again: the next wait is the slow one.
	wake.setBell(true)
	time.Sleep(30 * time.Millisecond) // let any pass already due finish
	n := p.n.Load()
	time.Sleep(80 * time.Millisecond)
	if p.n.Load() != n {
		t.Fatal("kept polling quickly after the bell came back")
	}
}

func TestAFailingPassKeepsItsBackoffWhateverTheBellDoes(t *testing.T) {
	fastCadence(t)
	wake := newSyncWake()
	wake.slow = time.Hour
	var n atomic.Int32
	failing := func(context.Context) error { n.Add(1); return errors.New("sync: service unavailable") }
	out := captureStderr(t, func() {
		cancel := loop(t, failing, wake)
		settle(t, "a failed pass", func() bool { return n.Load() == 1 })
		// The first failure backs off by about syncInterval; a bell
		// transition must not cut that short.
		wake.setBell(true)
		wake.setBell(false)
		time.Sleep(5 * time.Millisecond)
		if n.Load() != 1 {
			t.Error("a bell transition skipped the failure backoff")
		}
		settle(t, "a retry", func() bool { return n.Load() >= 3 })
		cancel()
	})
	if out != "sync: service unavailable\n" {
		t.Fatalf("a repeated failure was printed more than once: %q", out)
	}
}

// A blocked or absent bell must leave sync exactly as it was: same cadence
// as a loop that has no bell at all.
func TestABlockedBellLeavesPollingUnchanged(t *testing.T) {
	fastCadence(t)
	count := func(wake *syncWake) int32 {
		var p passes
		cancel := loop(t, p.exchange, wake)
		time.Sleep(220 * time.Millisecond)
		cancel()
		return p.n.Load()
	}
	without := count(nil)

	wake := newSyncWake()
	wake.slow = time.Hour
	var dials atomic.Int32
	b := &bell.Bell{
		Dial: func(context.Context) (*websocket.Conn, error) {
			dials.Add(1)
			return nil, bellError(account.ErrNetwork)
		},
		Credential: func(context.Context) (string, error) { return "tag", nil },
		Ring:       wake.ring, Healthy: wake.setBell,
		MinBackoff: time.Millisecond, MaxBackoff: 5 * time.Millisecond,
	}
	ctx, stop := context.WithCancel(context.Background())
	bellDone := make(chan struct{})
	go func() { defer close(bellDone); _ = b.Run(ctx) }()
	with := count(wake)
	stop()
	<-bellDone
	if dials.Load() < 3 {
		t.Fatalf("the bell was not being retried: %d dials", dials.Load())
	}
	if with < without-2 || with > without+2 {
		t.Fatalf("polling changed with the bell blocked: %d passes, %d without a bell", with, without)
	}
}

func TestOnceModeIsOnePass(t *testing.T) {
	var p passes
	if rc := syncLoop(context.Background(), p.exchange, nil, true); rc != 0 || p.n.Load() != 1 {
		t.Fatalf("once: rc=%d passes=%d", rc, p.n.Load())
	}
	out := captureStderr(t, func() {
		if rc := syncLoop(context.Background(), func(context.Context) error { return errors.New("sync: refused") }, nil, true); rc != 1 {
			t.Errorf("a failed once exited %d", rc)
		}
	})
	if out != "sync: refused\n" {
		t.Fatalf("once output %q", out)
	}
}

func TestBellHealthOnlyMattersWithASafetyNet(t *testing.T) {
	var none *syncWake
	if none.interval() != syncInterval {
		t.Fatal("no bell changed the cadence")
	}
	w := newSyncWake()
	w.setBell(true)
	if w.interval() != syncInterval {
		t.Fatal("a bell with no safety-net interval changed the cadence")
	}
	w.slow = time.Minute
	if w.interval() != time.Minute {
		t.Fatal("healthy bell did not slow polling")
	}
	w.setBell(false)
	w.setBell(false) // a second transition while the first is unread never blocks
	if w.interval() != syncInterval {
		t.Fatal("a lost bell kept the slow cadence")
	}
}

func TestAccountAnswersMeanTheRightThingToTheBell(t *testing.T) {
	if bellError(nil) != nil {
		t.Fatal("success became an error")
	}
	if err := bellError(account.ErrRemoved); !errors.Is(err, bell.ErrGone) || !errors.Is(err, account.ErrRemoved) {
		t.Fatalf("removal: %v", err)
	}
	if err := bellError(&account.ServiceError{Status: 401, Code: "box_removed"}); !errors.Is(err, bell.ErrGone) {
		t.Fatalf("a removal refusal: %v", err)
	}
	var refused *bell.Refused
	if err := bellError(&account.BellRefused{Status: 403, Code: "sync_required"}); !errors.As(err, &refused) || refused.Reason != "403 sync_required" {
		t.Fatalf("refusal: %v", err)
	}
	if err := bellError(account.ErrLogin); !errors.As(err, &refused) {
		t.Fatalf("no credential: %v", err)
	}
	var later *bell.Later
	busy := &account.ServiceError{Status: 429, Code: "rate_limited", RetryAfter: time.Minute}
	if err := bellError(busy); !errors.As(err, &later) || later.After != time.Minute || !errors.Is(err, busy) {
		t.Fatalf("Retry-After: %v", err)
	}
	for _, plain := range []error{account.ErrNetwork, account.ErrBusy, &account.ServiceError{Status: 404, Code: "service_error"}} {
		if err := bellError(plain); err != plain {
			t.Fatalf("%v changed to %v", plain, err)
		}
	}
}
