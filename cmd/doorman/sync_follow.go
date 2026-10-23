package main

import (
	"context"
	"time"
)

// How often a saved receipt is checked against the running daemon. Variables
// only so the tests can run in milliseconds; nothing else sets them.
var (
	// syncApplyProbe is the first gap between checks. The daemon reloads
	// within about a second of a write, so this keeps saved → applied within
	// a couple of seconds of the reload.
	syncApplyProbe = 500 * time.Millisecond
	// syncApplyFast is how long checks stay at syncApplyProbe after a new
	// receipt starts waiting. After that they double, up to syncApplyProbeMax:
	// a revision the daemon never loads (it is stopped, or refused the file)
	// costs a few local reads a minute until the command expires.
	syncApplyFast     = 30 * time.Second
	syncApplyProbeMax = 30 * time.Second
)

// applyFollow closes the gap between "saved" and "applied". Apply answers
// saved as soon as the file is written, and only a later pass can prove the
// daemon loaded it. Passes used to be five seconds apart; with the bell up
// they are SYNC_BELL_POLL_S apart and nothing rings for the upgrade, so the
// account showed "waiting for its policy reload" for minutes after the
// change was live.
//
// It asks the machine — locally, through the daemon's activation socket —
// whether a waiting receipt is now provably applied, and rings for one pass
// when one is. It never talks to the service itself, rings at most once per
// receipt, and with nothing waiting it sleeps until the next pass.
type applyFollow struct {
	kicks chan struct{}
	probe func() (waiting, ready []string, err error)
	ring  func()
}

func newApplyFollow(probe func() ([]string, []string, error), ring func()) *applyFollow {
	return &applyFollow{kicks: make(chan struct{}, 1), probe: probe, ring: ring}
}

// kick never blocks: the loop calls it after every pass, which is when a new
// saved receipt can appear.
func (f *applyFollow) kick() {
	select {
	case f.kicks <- struct{}{}:
	default:
	}
}

func (f *applyFollow) run(ctx context.Context) {
	seen, rung := map[string]bool{}, map[string]bool{}
	var since time.Time
	delay := syncApplyProbe
	for {
		waiting, ready, err := f.probe()
		if err == nil && len(waiting) == 0 {
			clear(seen)
			clear(rung)
			select {
			case <-ctx.Done():
				return
			case <-f.kicks:
			}
			continue
		}
		// A receipt not waiting before starts the quick checks again.
		fresh := false
		next := make(map[string]bool, len(waiting))
		for _, id := range waiting {
			fresh = fresh || !seen[id]
			next[id] = true
		}
		seen = next
		if fresh {
			since, delay = time.Now(), syncApplyProbe
		} else if time.Since(since) >= syncApplyFast {
			delay = min(delay*2, syncApplyProbeMax)
		}
		// Ring once per receipt. If its pass does not upgrade it (the pass
		// failed, or the service stopped offering the command), the ordinary
		// cadence and its backoff take over; ringing again would be a spin.
		ringNow := false
		proven := make(map[string]bool, len(ready))
		for _, id := range ready {
			ringNow = ringNow || !rung[id]
			proven[id] = true
		}
		rung = proven
		if ringNow {
			f.ring()
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		case <-f.kicks:
			timer.Stop()
		}
	}
}
