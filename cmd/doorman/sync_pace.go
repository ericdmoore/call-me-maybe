package main

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"time"

	"callmemaybe/internal/account"
)

const syncMaxDelay = 5 * time.Minute

// The healthy cadence and the shortest gap between passes. Variables only so
// the loop's tests can run in milliseconds; nothing else sets them.
var (
	syncInterval = 5 * time.Second
	syncMinGap   = time.Second
)

// syncPace keeps the healthy five-second cadence (a queued command lands within
// a pass) but backs off when the service or the network is failing, so an
// outage is not every box polling every five seconds in step.
type syncPace struct {
	failures int
	jitter   func(time.Duration) time.Duration
}

// next is the wait before the following pass. interval is the healthy
// cadence: syncInterval, or the slower safety net while the sync bell is up.
// Failures back off from syncInterval regardless, exactly as without a bell.
func (p *syncPace) next(err error, elapsed, interval time.Duration) time.Duration {
	if err == nil {
		p.failures = 0
		return max(interval-elapsed, syncMinGap)
	}
	p.failures++
	d := min(syncInterval<<min(p.failures-1, 8), syncMaxDelay)
	jitter := p.jitter
	if jitter == nil {
		jitter = func(d time.Duration) time.Duration { return rand.N(d + 1) }
	}
	// Half fixed, half random: boxes that failed together retry apart.
	d = d/2 + jitter(d/2)
	var refused *account.ServiceError
	if errors.As(err, &refused) && refused.RetryAfter > d {
		d = refused.RetryAfter
	}
	return d
}

// syncBusyRetry polls a local lock, so a fixed short interval costs nothing.
var syncBusyRetry = 2 * time.Second

// syncAccount waits out another account command instead of exiting. A backup
// upload holds the account store for its whole transfer; exiting would read as
// "sign in" and spend systemd's restart budget on a coincidence.
func syncAccount(ctx context.Context, client *account.Client) (account.Status, error) {
	for reported := false; ; reported = true {
		status, err := client.Status()
		if !errors.Is(err, account.ErrBusy) {
			return status, err
		}
		if !reported {
			fmt.Fprintln(os.Stderr, "sync: another account operation is running; retrying")
		}
		timer := time.NewTimer(syncBusyRetry)
		select {
		case <-ctx.Done():
			timer.Stop()
			return status, ctx.Err()
		case <-timer.C:
		}
	}
}
