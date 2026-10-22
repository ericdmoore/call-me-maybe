package push

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
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
	// held is the attempt a refusal answered. Retrying the same book against
	// the same phone record cannot succeed sooner, but a change can.
	held [32]byte
}

// holdBackoff spaces out answers that only a person can change: a wrong admin
// password, a device that is not the inventory's phone, or a phone whose
// keep-manual-entries setting has drifted. Each attempt is a full admin login,
// and the phone's owner should not see one every thirty seconds.
const holdBackoff = 5 * time.Minute

func held(err error) bool {
	return errors.Is(err, ErrAuth) || errors.Is(err, ErrIdentity) || errors.Is(err, ErrImport)
}

func backoff(err error, fail int) time.Duration {
	if held(err) {
		return holdBackoff
	}
	return min(30*time.Second, time.Second<<min(fail-1, 5))
}

// attempt identifies what an upload would send and to whom. The admin
// password participates so a corrected inventory retries at once; the value is
// only ever hashed in memory.
func attempt(p provision.Phone, ip netip.Addr, xml []byte) [32]byte {
	h := sha256.New()
	for _, part := range [][]byte{[]byte(p.MAC), []byte(p.Model.ID), []byte(p.AdminPassword), ip.AsSlice(), xml} {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(part)))
		h.Write(n[:])
		h.Write(part)
	}
	var sum [32]byte
	copy(sum[:], h.Sum(nil))
	return sum
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
			if !found || state.busy {
				continue
			}
			key := attempt(doc.Phone, ip, doc.XML)
			if time.Now().Before(state.retry) && (state.held == [32]byte{} || state.held == key) {
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
					state.retry, state.held = time.Time{}, [32]byte{}
				} else {
					state.done = false
					state.fail++
					state.held = [32]byte{}
					if held(err) {
						// Wrong credentials must not lock out the phone's owner.
						state.held = key
					}
					state.retry = time.Now().Add(backoff(err, state.fail))
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
