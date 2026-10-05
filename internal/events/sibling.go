package events

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
)

// Sibling is the journal's second writer: another doorman process on the
// same box — `doorman inbox` today — appending its own observations to
// the daemon's journal.
//
// The owner lock the daemon takes was a choice, not a law (s13, question
// 5, settled 2026-09-26: "a second writer is fine"). SQLite serialises
// writers itself; what the lock protects is the *bookkeeping* only one
// process may do — the clean flag, coverage-gap notes on an unclean
// restart, CEL ingestion, pruning on a timer. A sibling does none of it:
// it never creates or migrates the journal, never touches metadata beyond
// the high-water mark every append moves, and its rows ring no doorbell —
// they are visible to the next read, and announced by the daemon's next
// commit. Every append is committed before it returns, so a sibling has
// no queue to lose and no coverage gap to declare.
type Sibling struct {
	path string
	opts Options
	mu   sync.Mutex
	s    *store
}

// OpenSibling prepares a sibling writer; the journal is opened on the
// first Append, so a process may start before the daemon has created it.
func OpenSibling(path string, o Options) *Sibling {
	return &Sibling{path: path, opts: defaults(o)}
}

// ErrNoJournal is Append's answer while the daemon has not created the
// journal yet: not this process's job, and not an error worth stopping for.
var ErrNoJournal = errors.New("journal not created yet; the daemon creates it on its first start")

func (w *Sibling) open() (*store, error) {
	if w.s != nil {
		return w.s, nil
	}
	if _, err := os.Lstat(w.path); os.IsNotExist(err) {
		return nil, ErrNoJournal
	} else if err != nil {
		return nil, err
	}
	// The same open as the owner's: the same permission checks, the same
	// budget and the same pragmas; one connection, closed with the sibling.
	s, err := openStore(w.path, w.opts)
	if err != nil {
		return nil, err
	}
	var version int
	if err = s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		_ = s.db.Close()
		return nil, err
	}
	if version != currentSchema {
		_ = s.db.Close()
		return nil, fmt.Errorf("journal schema is version %d, this binary expects %d; restart the daemon to upgrade it", version, currentSchema)
	}
	w.s = s
	return s, nil
}

// Append commits one event and returns; the daemon's Post is a queue, this
// is a write. A busy database is retried briefly, as the owner's is.
func (w *Sibling) Append(ctx context.Context, e Event) error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	s, err := w.open()
	if err != nil {
		return err
	}
	if err = s.appendRecover(ctx, e); err != nil {
		// A connection that failed is dropped so the next append reopens
		// — the daemon may have rotated or repaired the file under it.
		_ = s.db.Close()
		w.s = nil
		return err
	}
	return nil
}

func (w *Sibling) Close() error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.s == nil {
		return nil
	}
	err := w.s.db.Close()
	w.s = nil
	return err
}
