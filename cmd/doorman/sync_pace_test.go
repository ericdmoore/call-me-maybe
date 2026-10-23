package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"callmemaybe/internal/account"
)

func TestSyncPaceBacksOffHonoursRetryAfterAndRecovers(t *testing.T) {
	p := syncPace{jitter: func(d time.Duration) time.Duration { return d }}
	if d := p.next(nil, time.Second, syncInterval); d != 4*time.Second {
		t.Fatalf("healthy cadence changed: %v", d)
	}
	if d := p.next(nil, 10*time.Second, syncInterval); d != time.Second {
		t.Fatalf("slow pass lost the floor: %v", d)
	}
	failed := errors.New("account: service connection failed")
	var got []time.Duration
	for range 10 {
		got = append(got, p.next(failed, 0, syncInterval))
	}
	if got[0] != 5*time.Second || got[1] != 10*time.Second || got[2] != 20*time.Second || got[9] != syncMaxDelay {
		t.Fatalf("unexpected backoff: %v", got)
	}
	p.jitter = func(time.Duration) time.Duration { return 0 }
	if d := p.next(failed, 0, syncInterval); d != syncMaxDelay/2 {
		t.Fatalf("jitter is not half the delay: %v", d)
	}
	if d := p.next(&account.ServiceError{Status: 429, Code: "rate_limited", RetryAfter: 20 * time.Minute}, 0, syncInterval); d != 20*time.Minute {
		t.Fatalf("ignored Retry-After: %v", d)
	}
	if d := p.next(nil, 0, syncInterval); d != syncInterval {
		t.Fatalf("success did not reset the backoff: %v", d)
	}
	if d := p.next(failed, 0, syncInterval); d != syncInterval/2 {
		t.Fatalf("backoff did not restart from the base: %v", d)
	}
	p.jitter = nil
	for range 50 {
		if d := p.next(failed, 0, syncInterval); d < 0 || d > syncMaxDelay {
			t.Fatalf("random jitter out of bounds: %v", d)
		}
	}
}

func TestSyncStartupWaitsOutABusyAccountStore(t *testing.T) {
	defer func(d time.Duration) { syncBusyRetry = d }(syncBusyRetry)
	syncBusyRetry = 10 * time.Millisecond
	state := t.TempDir()
	if err := os.MkdirAll(filepath.Join(state, "account"), 0o700); err != nil {
		t.Fatal(err)
	}
	// Another process (a backup upload) holds the account lock.
	lock, err := os.OpenFile(filepath.Join(state, "account", ".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	fd := int(lock.Fd())
	if err := syscall.Flock(fd, syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	client := account.New(state)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := syncAccount(ctx, client); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("busy store was not retried: %v", err)
	}
	time.AfterFunc(30*time.Millisecond, func() { _ = syscall.Flock(fd, syscall.LOCK_UN) })
	status, err := syncAccount(context.Background(), client)
	if err != nil || !status.NeedsLogin {
		t.Fatalf("did not read the store once free: %v", err)
	}
}
