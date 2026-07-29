package policy

import (
	"fmt"
	"os"
	"sync"
	"time"
)

// Store holds the current Policy and optionally swaps it when the file
// changes. A bad edit is reported and discarded — the previous good policy
// stays live, because a syntax error at 2am should not take the house phone
// offline.
//
// Change detection is mtime+size polling at 1s rather than inotify: it is
// dependency-free, works on every filesystem including network mounts, and a
// one-second delay on an allow-list edit is imperceptible for a phone.
type Store struct {
	mu     sync.RWMutex
	policy *Policy

	policyPath   string
	handsetsPath string
	onReload     func(*Policy)
	onError      func(error)

	stop chan struct{}
	done chan struct{}
}

// OpenStore loads the policy (split or legacy layout) and, if watch is true,
// begins polling both files for changes — an edit to either triggers a full
// recompile, since extensions cross-reference handsets. onReload and onError
// may be nil.
func OpenStore(policyPath, handsetsPath string, watch bool, onReload func(*Policy), onError func(error)) (*Store, error) {
	p, err := LoadSplit(policyPath, handsetsPath)
	if err != nil {
		return nil, err
	}
	s := &Store{
		policy:       p,
		policyPath:   policyPath,
		handsetsPath: handsetsPath,
		onReload:     onReload,
		onError:      onError,
		stop:         make(chan struct{}),
		done:         make(chan struct{}),
	}
	if watch {
		go s.poll()
	} else {
		close(s.done)
	}
	return s, nil
}

// Current returns the active policy. Sessions capture this once at call start
// so a mid-call reload cannot change the rules under a caller.
func (s *Store) Current() *Policy {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.policy
}

func (s *Store) poll() {
	defer close(s.done)

	stamp := func() string {
		var b []byte
		for _, path := range []string{s.policyPath, s.handsetsPath} {
			if path == "" {
				continue
			}
			info, err := os.Stat(path)
			if err != nil {
				b = append(b, "gone;"...)
				continue
			}
			b = append(b, (info.ModTime().String() + "/" + fmt.Sprint(info.Size()) + ";")...)
		}
		return string(b)
	}

	last := stamp()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-s.stop:
			return
		case <-ticker.C:
			now := stamp()
			if now == last {
				continue
			}
			last = now

			next, err := LoadSplit(s.policyPath, s.handsetsPath)
			if err != nil {
				if s.onError != nil {
					s.onError(err)
				}
				continue // previous good policy stays in service
			}
			s.mu.Lock()
			s.policy = next
			s.mu.Unlock()
			if s.onReload != nil {
				s.onReload(next)
			}
		}
	}
}

// Close stops the watcher. Safe to call once.
func (s *Store) Close() {
	select {
	case <-s.stop:
	default:
		close(s.stop)
	}
	<-s.done
}
