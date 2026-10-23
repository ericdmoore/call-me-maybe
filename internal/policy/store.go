package policy

import (
	"os"
	"sync"
	"time"
)

// Store holds the current Policy and optionally swaps it when the file
// changes. A bad edit is reported and discarded — the previous good policy
// stays live, because a syntax error at 2am should not take the house phone
// offline.
//
// Change detection hashes file contents every second. Atomic replacements and
// edits preserving size/mtime still reload, including on network filesystems.
type Store struct {
	mu     sync.RWMutex
	policy *Policy

	policyPath   string
	handsetsPath string
	opts         Options
	initial      string // the stamp the poller starts from
	fallback     error  // why startup is running the last good copy, if it is
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
	return OpenStoreWith(policyPath, handsetsPath, watch, Options{}, onReload, onError)
}

// OpenStoreWith is OpenStore with explicit options, which the daemon uses to
// route unknown-key warnings into its log. The options apply to the initial
// load and to every reload, so a typo introduced at 2am is reported then and
// not only at the next restart.
//
// The daemon passes configcheck.Options, the same rules `doorman check` and
// sync apply. A reload those rules refuse is reported through onError and the
// previous good policy stays in service (invariant 4).
//
// With Options.LastGood set, every accepted load is copied there, and a
// startup whose current files are refused runs that copy instead: Fallback
// says why. Only when there is no usable copy does OpenStoreWith fail.
func OpenStoreWith(policyPath, handsetsPath string, watch bool, o Options, onReload func(*Policy), onError func(error)) (*Store, error) {
	s := &Store{
		policyPath:   policyPath,
		handsetsPath: handsetsPath,
		opts:         o,
		onReload:     onReload,
		onError:      onError,
		stop:         make(chan struct{}),
		done:         make(chan struct{}),
	}
	before := s.stamp()
	trunks := trunksStamp(o.TrunksPath)
	p, err := LoadSplitWith(policyPath, handsetsPath, o)
	switch {
	case err == nil:
		s.policy, s.initial = p, p.SourceDigest()+trunks
		s.keepLastGood(p)
	case o.LastGood != "":
		last, lastErr := loadLastGood(o.LastGood, handsetsPath != "", o.TrunksPath != "", o)
		if lastErr != nil {
			return nil, err // no usable copy: refused, as without one
		}
		// The poller starts from the refused files, so they are not
		// reported a second time; an edit to them is tried at once.
		s.policy, s.initial, s.fallback = last, before, err
	default:
		return nil, err
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

// Fallback is the refusal that put this store on its last good copy at
// startup, or nil when it is running the current files.
func (s *Store) Fallback() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.fallback
}

// stamp identifies the current bytes of every file a load reads. The trunk
// inventory is part of it because [line] trunk is checked against it: an
// edit there can make a policy valid or invalid.
func (s *Store) stamp() string {
	p, err := os.ReadFile(s.policyPath)
	if err != nil {
		return "unread"
	}
	var h []byte
	if s.handsetsPath != "" {
		h, err = os.ReadFile(s.handsetsPath)
		if err != nil && !os.IsNotExist(err) {
			return "unread"
		}
	}
	return SourceDigest(p, h) + trunksStamp(s.opts.TrunksPath)
}

func (s *Store) keepLastGood(p *Policy) {
	if err := s.saveLastGood(p); err != nil && s.opts.OnLastGoodError != nil {
		s.opts.OnLastGoodError(err)
	}
}

func (s *Store) poll() {
	defer close(s.done)
	stamp := s.stamp

	// Start with the loaded bytes, not a second read that could hide an edit
	// made between OpenStore's initial compilation and this goroutine.
	last := s.initial
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

			trunks := trunksStamp(s.opts.TrunksPath)
			next, err := LoadSplitWith(s.policyPath, s.handsetsPath, s.opts)
			if err != nil {
				if s.onError != nil {
					s.onError(err)
				}
				continue // previous good policy stays in service
			}
			if stamp() != next.SourceDigest()+trunks {
				last = "" // related files changed during compilation; retry
				continue
			}
			last = next.SourceDigest() + trunks
			s.mu.Lock()
			s.policy, s.fallback = next, nil
			s.mu.Unlock()
			s.keepLastGood(next)
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

// trunksStamp identifies the trunk inventory's bytes for change detection.
// Empty when there is no path to watch.
func trunksStamp(path string) string {
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "|absent"
		}
		return "|unread"
	}
	return "|" + SourceDigest(data, nil)
}
