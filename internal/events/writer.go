package events

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const DefaultMaxBytes int64 = 64 << 20
const DefaultMaxEvents = 100000
const DefaultMaxAge = 90 * 24 * time.Hour

type Options struct {
	CELPath            string
	DefaultCountryCode string
	MaxBytes           int64
	MaxEvents          int
	MaxAge             time.Duration
	Log                *slog.Logger
}

func defaults(o Options) Options {
	if o.DefaultCountryCode == "" {
		o.DefaultCountryCode = "1"
	}
	if o.MaxBytes == 0 {
		o.MaxBytes = DefaultMaxBytes
	}
	if o.MaxEvents == 0 {
		o.MaxEvents = DefaultMaxEvents
	}
	if o.MaxAge == 0 {
		o.MaxAge = DefaultMaxAge
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	return o
}

type Writer struct {
	path     string
	opts     Options
	ready    chan error
	queue    chan Event
	stop     chan struct{}
	done     chan struct{}
	mu       sync.Mutex
	closed   bool
	cancel   context.CancelFunc
	dropped  atomic.Int64
	failed   atomic.Int64
	degraded atomic.Bool
	pending  atomic.Int64
	late     atomic.Bool
}

// Start retries unavailable storage in the background. It never takes down the
// telephone because its observational state could not be opened.
func Start(path string, o Options) *Writer {
	o = defaults(o)
	ctx, cancel := context.WithCancel(context.Background())
	w := &Writer{ready: make(chan error, 1), path: path, opts: o, queue: make(chan Event, 256), stop: make(chan struct{}), done: make(chan struct{}), cancel: cancel}
	w.degraded.Store(true)
	go w.run(ctx)
	return w
}
func (w *Writer) Post(e Event) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		if !w.late.Swap(true) {
			w.opts.Log.Error("observation rejected after journal close; stop producers before closing the writer")
		}
		w.dropped.Add(1)
		w.pending.Add(1)
		w.degraded.Store(true)
		return
	}
	select {
	case w.queue <- e:
	default:
		w.dropped.Add(1)
		w.pending.Add(1)
	}
}
func (w *Writer) Dropped() int64 { return w.dropped.Load() }
func (w *Writer) Failed() int64  { return w.failed.Load() }
func (w *Writer) Degraded() bool { return w.degraded.Load() }
func (w *Writer) Close() error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	if !w.closed {
		w.closed = true
		close(w.stop)
	}
	w.mu.Unlock()
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	select {
	case <-w.done:
		return nil
	case <-timer.C:
		w.cancel()
		<-w.done
		return errors.New("journal shutdown deadline exceeded")
	}
}
func (w *Writer) run(ctx context.Context) {
	defer close(w.done)
	defer w.cancel()
	defer func() {
		if w.dropped.Load()+w.failed.Load() > 0 {
			w.opts.Log.Warn("event journal observations lost", "dropped", w.dropped.Load(), "failed", w.failed.Load())
		}
	}()
	var s *store
	var owner *os.File
	defer func() {
		if s != nil {
			_ = s.db.Close()
		}
		if owner != nil {
			_ = syscall.Flock(int(owner.Fd()), syscall.LOCK_UN)
			_ = owner.Close()
		}
	}()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	warned := false
	celWarned := false
	var openErr error
	fail := func(err error) {
		openErr = err
		w.degraded.Store(true)
		if !warned {
			w.opts.Log.Error("event journal degraded; calls continue", "err", err, "dropped", w.dropped.Load(), "failed", w.failed.Load())
			warned = true
		}
	}
	open := func() bool {
		if s != nil {
			return true
		}
		if w.opts.MaxBytes < 8<<20 || w.opts.MaxEvents < 1 || w.opts.MaxAge <= 0 {
			fail(errors.New("invalid journal storage budget"))
			return false
		}
		// openStore checks directory/file permissions before SQLite touches sidecars.
		var err error
		if owner == nil {
			// The directory is created privately before taking the process owner lock.
			if err = os.MkdirAll(filepath.Dir(w.path), 0700); err != nil {
				fail(err)
				return false
			}
			owner, err = os.OpenFile(w.path+".lock", os.O_CREATE|os.O_RDWR, 0600)
			if err != nil {
				fail(err)
				return false
			}
			if err = syscall.Flock(int(owner.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
				_ = owner.Close()
				owner = nil
				fail(fmt.Errorf("journal writer lock unavailable (another writer may be running): %w", err))
				return false
			}
		}
		s, err = openStore(w.path, w.opts)
		if err != nil {
			fail(err)
			return false
		}
		var clean int
		if err = s.db.QueryRowContext(ctx, "SELECT clean FROM metadata").Scan(&clean); err == nil {
			_, err = s.db.ExecContext(ctx, "UPDATE metadata SET clean=0")
		}
		if err == nil && clean == 0 {
			err = s.appendRecover(ctx, System(CoverageGap, "unclean-restart; queued events and active sessions may be missing", 0))
		}
		if err != nil {
			_ = s.db.Close()
			s = nil
			fail(err)
			return false
		}
		w.degraded.Store(false)
		return true
	}
	write := func(e Event) {
		if !open() {
			w.failed.Add(1)
			w.pending.Add(1)
			return
		}
		if n := w.pending.Load(); n > 0 {
			if err := s.appendRecover(ctx, System(CoverageGap, "observations-lost", n)); err != nil {
				w.failed.Add(1)
				w.pending.Add(1)
				fail(err)
				return
			}
			w.pending.Add(-n)
		}
		if e.Type == "" {
			w.degraded.Store(false)
			warned = false
			return
		}
		err := s.appendRecover(ctx, e)
		if err != nil {
			w.failed.Add(1)
			w.pending.Add(1)
			fail(err)
			return
		}
		if w.degraded.Swap(false) {
			w.opts.Log.Info("event journal available")
		}
		warned = false
	}
	if open() {
		w.ready <- nil
	} else {
		w.ready <- openErr
	}
	for {
		select {
		case <-ctx.Done():
			return
		case e := <-w.queue:
			write(e)
		case <-ticker.C:
			if open() {
				if w.opts.CELPath != "" {
					batchCtx, cancel := context.WithTimeout(ctx, 750*time.Millisecond)
					err := s.ingestCELRecover(batchCtx, w.opts.CELPath)
					cancel()
					if err != nil {
						if !celWarned {
							_ = s.appendRecover(ctx, System(JournalNote, "cel-ingestion-unavailable; replay pending", 0))
							w.opts.Log.Warn("CEL ingestion paused; source cursor retained", "err", err)
						}
						celWarned = true
					} else if celWarned {
						w.opts.Log.Info("CEL ingestion resumed")
						celWarned = false
					}
				}
				if w.pending.Load() > 0 {
					write(Event{})
				}
				if err := s.walBudget(ctx); err != nil {
					fail(err)
					continue
				}
				tx, err := s.db.BeginTx(ctx, nil)
				if err == nil {
					err = s.prune(ctx, tx, 0)
					if err == nil {
						err = tx.Commit()
					} else {
						_ = tx.Rollback()
					}
				}
				if err != nil {
					fail(err)
				}
				_, _ = s.db.ExecContext(ctx, "PRAGMA wal_checkpoint(PASSIVE)")
			}
		case <-w.stop:
			for {
				select {
				case e := <-w.queue:
					write(e)
				default:
					if w.pending.Load() > 0 {
						write(Event{})
					}
					if s != nil && !w.degraded.Load() && w.pending.Load() == 0 {
						_, _ = s.db.ExecContext(ctx, "UPDATE metadata SET clean=1")
						_, _ = s.db.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)")
					}
					return
				}
			}
		}
	}
}

// StartChecked refuses an unusable configured journal at startup. Runtime
// failures still degrade observation without taking down calls.
func StartChecked(path string, o Options) (*Writer, error) {
	w := Start(path, o)
	if err := <-w.ready; err != nil {
		_ = w.Close()
		return nil, err
	}
	return w, nil
}

// WriterActive probes the existing owner lock without creating a file. A running
// owner is useful status, not a reason for an operator's read-only check to fail.
func WriterActive(path string) (bool, error) {
	f, err := os.Open(path + ".lock")
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer f.Close()
	err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return false, syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
