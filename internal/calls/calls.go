// Package calls records one line per completed call.
//
// This is deliberately not a database. The format is JSON Lines appended to
// one file, because a Pi loses power without warning — that is its normal
// shutdown — and an O_APPEND write loses at most a partial final line, which
// the reader skips. There is no schema, so there is no migration, so there is
// no upgrade that fails to start. At twenty calls a day a year of records is
// roughly two megabytes and a full scan is sub-millisecond; indexes would be
// solving a problem that does not exist at this scale.
//
// # The log is never an input
//
// Nothing on the call path may read these records to make a decision. The
// moment something does — "this number has called five times, admit it" — the
// rate limiter's deliberately-in-memory design is undermined and doorman has
// acquired persistent state that can be corrupt, stale, or disagree with
// policy.toml.
//
// That rule is enforced by the compiler rather than by memory: this package
// imports internal/policy, so policy can never import this one. Anything
// wanting to decide from history has to break the cycle first, which is a
// conversation rather than an accident.
//
// # What is never written
//
// Entered digits and PINs, at any level, no exception — a near-miss is almost
// a credential, so a record says whether a PIN was valid and never what was
// typed. Caller IDs *are* written in full, which is the point of a call log on
// a telephone; the file is 0600, stays on the box, and Record.Redacted covers
// them for anything that leaves it.
package calls

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"callmemaybe/internal/policy"
)

// Outcomes. Every call ends as exactly one of these.
const (
	// OutcomeAnswered: a handset picked up and the caller was bridged.
	OutcomeAnswered = "answered"
	// OutcomeVoicemail: released into the dialplan to leave a message.
	OutcomeVoicemail = "voicemail"
	// OutcomeDismissed: doorman ended it deliberately. Reason says why.
	OutcomeDismissed = "dismissed"
	// OutcomeAbandoned: the caller hung up first. The zero value, so a call
	// that ends by cancellation is recorded truthfully without anyone
	// remembering to set it.
	OutcomeAbandoned = "abandoned"
)

// A Record is one call. Field names are short because they are read by people
// and by models more often than by code.
type Record struct {
	ID    string    `json:"id"` // channel id; ties back to the slog lines
	Start time.Time `json:"start"`
	MS    int64     `json:"ms"` // duration

	Caller string `json:"caller"`          // full E.164, or "" when withheld
	Known  string `json:"known,omitempty"` // allow-list name that matched

	Outcome string `json:"outcome"`
	Reason  string `json:"reason,omitempty"` // rate-limited, no-digits, …

	// Extension is the label a caller reached, never the PIN. PIN is
	// "valid" or "invalid" and never the digits.
	Extension string `json:"extension,omitempty"`
	PIN       string `json:"pin,omitempty"`
	Attempts  int    `json:"attempts,omitempty"`

	// Stages is the ladder as actually walked. The most useful field here:
	// it is what tells you a stage times out before anyone can cross a room.
	Stages []Stage `json:"stages,omitempty"`

	AnsweredBy string `json:"answered_by,omitempty"` // handset id
	Mailbox    string `json:"mailbox,omitempty"`
}

// A Stage is one rung of a ring ladder.
type Stage struct {
	Handsets []string `json:"handsets"`
	MS       int64    `json:"ms"`
	Result   string   `json:"result"` // answered, timeout, failed
}

// Redacted returns a copy safe to paste into a bug report or hand to a model.
func (r Record) Redacted() Record {
	r.Caller = policy.Redact(r.Caller)
	return r
}

// Duration is a convenience for readers.
func (r Record) Duration() time.Duration { return time.Duration(r.MS) * time.Millisecond }

// ── writing ─────────────────────────────────────────────────────────────

// DefaultMaxBytes caps the live file. Twenty calls a day is about two
// megabytes a year, so this holds years — the cap exists for the case that is
// not twenty calls a day, which is somebody redialling in a loop.
const DefaultMaxBytes int64 = 32 << 20

// A Writer appends records without ever blocking the caller.
//
// Post hands off to a buffered channel drained by one goroutine. A full buffer
// drops the record and counts it, because a call log must never be able to
// delay a call — the same reason the event router never blocks.
type Writer struct {
	ch   chan Record
	done chan struct{}

	path     string
	maxBytes int64

	dropped atomic.Int64
	failed  atomic.Int64

	closeOnce sync.Once
}

// Open starts a writer appending to path, creating it 0600. maxBytes of zero
// means DefaultMaxBytes.
func Open(path string, maxBytes int64) (*Writer, error) {
	if path == "" {
		return nil, errors.New("calls: no path")
	}
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("calls: %w", err)
	}
	// Opened once here so a bad path fails at startup, where an operator will
	// see it, rather than silently at the end of the first call.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("calls: %w", err)
	}

	w := &Writer{
		ch:       make(chan Record, 64),
		done:     make(chan struct{}),
		path:     path,
		maxBytes: maxBytes,
	}
	go w.run(f)
	return w, nil
}

// Post records a call. Never blocks; never panics after Close.
func (w *Writer) Post(r Record) {
	if w == nil {
		return
	}
	select {
	case w.ch <- r:
	default:
		w.dropped.Add(1)
	}
}

// Dropped is the number of records lost to a full buffer, and Failed the
// number lost to a write error. Both are surfaced by `doorman calls` so a log
// with holes in it says so rather than quietly appearing complete.
func (w *Writer) Dropped() int64 { return w.dropped.Load() }
func (w *Writer) Failed() int64  { return w.failed.Load() }

// Close drains what is buffered and stops the writer.
func (w *Writer) Close() error {
	if w == nil {
		return nil
	}
	w.closeOnce.Do(func() {
		close(w.ch)
		<-w.done
	})
	return nil
}

func (w *Writer) run(f *os.File) {
	defer close(w.done)
	defer func() { _ = f.Close() }()

	size := int64(0)
	if st, err := f.Stat(); err == nil {
		size = st.Size()
	}

	for r := range w.ch {
		line, err := json.Marshal(r)
		if err != nil {
			w.failed.Add(1)
			continue
		}
		line = append(line, '\n')

		if size+int64(len(line)) > w.maxBytes {
			if rotated, err := w.rotate(f); err == nil {
				f, size = rotated, 0
			}
		}

		n, err := f.Write(line)
		if err != nil {
			w.failed.Add(1)
			continue
		}
		size += int64(n)
	}
}

// rotate keeps one previous generation. Two files of bounded size is a cap an
// operator can reason about; a numbered series is a retention policy, and
// retention policy on a house phone is somebody else's cron job.
func (w *Writer) rotate(f *os.File) (*os.File, error) {
	if err := f.Close(); err != nil {
		return nil, err
	}
	if err := os.Rename(w.path, w.path+".1"); err != nil {
		return nil, err
	}
	return os.OpenFile(w.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
}

// ── reading ─────────────────────────────────────────────────────────────

// A Filter narrows what Read returns. The zero Filter returns everything.
type Filter struct {
	Since   time.Time
	Outcome string // exact match, empty for any
	Caller  string // substring of the E.164, so a partial number works
	Limit   int    // most recent N, 0 for all
}

func (f Filter) match(r Record) bool {
	if !f.Since.IsZero() && r.Start.Before(f.Since) {
		return false
	}
	if f.Outcome != "" && r.Outcome != f.Outcome {
		return false
	}
	if f.Caller != "" && !strings.Contains(r.Caller, f.Caller) {
		return false
	}
	return true
}

// Read returns matching records oldest-first, plus the number of unparseable
// lines skipped.
//
// A malformed line is skipped rather than fatal: the last line of a file on a
// machine that lost power mid-write is expected to be a partial one, and a
// call log that refuses to open because the power went out is worse than a
// call log missing its final entry. The count is reported so "skipped" never
// silently means "fine".
func Read(path string, f Filter) (records []Record, skipped int, err error) {
	// The previous generation first, so results stay in order across a
	// rotation.
	for _, p := range []string{path + ".1", path} {
		rs, n, err := readOne(p, f)
		if err != nil {
			return nil, 0, err
		}
		records = append(records, rs...)
		skipped += n
	}

	if f.Limit > 0 && len(records) > f.Limit {
		records = records[len(records)-f.Limit:]
	}
	return records, skipped, nil
}

func readOne(path string, f Filter) (records []Record, skipped int, err error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, 0, nil // no calls yet, or no previous generation
	}
	if err != nil {
		return nil, 0, fmt.Errorf("calls: %w", err)
	}
	defer func() { _ = file.Close() }()

	sc := bufio.NewScanner(file)
	// A record with a long ladder can exceed the default 64 KiB.
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var r Record
		if err := json.Unmarshal(line, &r); err != nil {
			skipped++
			continue
		}
		if f.match(r) {
			records = append(records, r)
		}
	}
	if err := sc.Err(); err != nil && !errors.Is(err, io.EOF) {
		return records, skipped, fmt.Errorf("calls: %w", err)
	}
	return records, skipped, nil
}
