package push

import (
	"context"
	"crypto/sha256"
	"errors"
	"net/netip"
	"sync"
	"time"

	"callmemaybe/internal/provision"
)

type Document struct {
	Phone provision.Phone
	XML   []byte
}

type Result struct {
	Handset string
	Err     error
}

// Worker coalesces edits to the latest rendered directory. An offline phone
// never blocks another, and a newer edit arriving during an upload remains
// pending. Only an acknowledged import advances the delivered fingerprint.
type Worker struct {
	Documents  func() []Document
	Neighbours func() map[string]netip.Addr
	Upload     func(context.Context, provision.Phone, netip.Addr, []byte) error
	OnResult   func(Result)
	Interval   time.Duration
}

type delivery struct {
	digest [32]byte
	ip     netip.Addr
	done   bool
	busy   bool
	retry  time.Time
	fail   int
}

func (w Worker) Run(ctx context.Context) {
	interval := w.Interval
	if interval <= 0 {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var mu sync.Mutex
	var jobs sync.WaitGroup
	defer jobs.Wait()
	states := map[string]*delivery{}
	slots := make(chan struct{}, 8)
	tick := func() {
		docs := w.Documents()
		neighbours := w.Neighbours()
		mu.Lock()
		defer mu.Unlock()
		for _, doc := range docs {
			if !Supported(doc.Phone.Model.ID) {
				continue
			}
			id := doc.Phone.ID
			state := states[id]
			if state == nil {
				state = &delivery{}
				states[id] = state
			}
			ip, found := neighbours[doc.Phone.MAC]
			if !found || state.busy || time.Now().Before(state.retry) {
				continue
			}
			digest := sha256.Sum256(doc.XML)
			if state.done && state.digest == digest && state.ip == ip {
				continue
			}
			select {
			case slots <- struct{}{}:
			default:
				continue
			}
			state.busy = true
			jobs.Add(1)
			go func() {
				defer jobs.Done()
				defer func() { <-slots }()
				attempt, cancel := context.WithTimeout(ctx, 4*time.Second)
				defer cancel()
				err := w.Upload(attempt, doc.Phone, ip, doc.XML)
				mu.Lock()
				state.busy = false
				if err == nil {
					state.digest, state.ip, state.done, state.fail = digest, ip, true, 0
					state.retry = time.Time{}
				} else {
					state.done = false
					state.fail++
					backoff := min(30*time.Second, time.Second<<min(state.fail-1, 5))
					if errors.Is(err, ErrAuth) {
						// Wrong credentials must not lock out the phone's owner.
						backoff = 5 * time.Minute
					}
					state.retry = time.Now().Add(backoff)
				}
				report := err == nil || state.fail == 1
				mu.Unlock()
				if report && w.OnResult != nil {
					w.OnResult(Result{Handset: id, Err: err})
				}
			}()
		}
	}
	tick()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tick()
		}
	}
}
