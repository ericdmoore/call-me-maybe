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
// them for anything that leaves it. A dialled number is a caller ID by the
// same argument and gets the same treatment — full on disk, narrowed by
// Redacted — and the line between the two rules is that a destination somebody
// asked for is not a credential, while a six-digit near-miss at the door is.
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
//
// The first four are the inbound vocabulary and they are about who answered.
// Only two of them mean the same thing in both directions: doorman ended it
// (dismissed) and the human hung up first (abandoned). "Answered" is the one
// an outbound call cannot borrow, because doorman releases the channel into
// the dialplan and is out of the call before anybody picks up — hence
// OutcomePlaced, which is the whole of the outbound vocabulary that inbound
// did not already have.
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
	// OutcomePlaced: an outbound call was handed to the dialplan carrying the
	// chosen line's caller ID. It says the call left the building and nothing
	// more — whether it rang, was answered, or how long it lasted is not
	// knowable here, because the point of the outbound console is that doorman
	// stops being in the call the moment it is placed.
	OutcomePlaced = "placed"
)

// Directions. A record with no direction is inbound: that is the call this
// system was built for, and it means every record written before outbound
// calling existed still says something true.
const (
	DirectionInbound  = "inbound"
	DirectionOutbound = "outbound"
)

// A Record is one call. Field names are short because they are read by people
// and by models more often than by code.
type Record struct {
	ID    string    `json:"id"` // channel id; ties back to the slog lines
	Start time.Time `json:"start"`
	// MS is how long this call was doorman's. Inbound that is the call;
	// outbound it is the time spent at the console, because the call is handed
	// to the dialplan and doorman never learns how long anybody talked.
	MS int64 `json:"ms"`

	// Line is the line the call arrived on, or was placed as. Empty is the
	// default line — every call on an install with one number — so a
	// single-line log is byte-identical to the one written before lines
	// existed, and a record from an older doorman still reads correctly.
	// LineOrDefault resolves it.
	//
	// One log rather than one file per line, deliberately: a whole day still
	// reads in order, which is how anybody actually looks for a call.
	Line string `json:"line,omitempty"`
	// Direction is DirectionOutbound on a call this house placed. Empty is
	// inbound, for the same reason Line's zero value is the default line.
	// Inbound resolves it.
	Direction string `json:"direction,omitempty"`

	Caller string `json:"caller"` // full E.164, or "" when withheld
	// Known is the name that admitted this caller: an allow-list entry, or a
	// personal contact from an address book. One field for both, because
	// "somebody we know rang the house" is one fact however it was decided,
	// and a reader looking for a name should not have to know which list it
	// came from.
	Known string `json:"known,omitempty"`
	// Dialled is the number an outbound call was placed to, exactly as it was
	// dialled — not normalised, because that is what reached the dialplan and
	// what will appear on the bill, and a number doorman cannot normalise is
	// still a number somebody called.
	//
	// It is a caller ID in every sense that matters, so it is held in full
	// here and narrowed by Redacted, exactly like Caller. It is never a
	// fumbled entry: only a complete number the console accepted as a
	// destination lands here, which is what keeps this field on the caller-ID
	// side of invariant 1 rather than the entered-digits side.
	Dialled string `json:"dialled,omitempty"`

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
// Both numbers are narrowed by the same function: an outbound call's dialled
// number identifies a person exactly as precisely as an inbound caller ID.
func (r Record) Redacted() Record {
	r.Caller = policy.Redact(r.Caller)
	r.Dialled = policy.Redact(r.Dialled)
	return r
}

// LineOrDefault is the line this call belongs to, resolving the zero value. A
// reader should use this rather than the field: an empty Line is not "no line",
// it is the default one.
func (r Record) LineOrDefault() string {
	if r.Line == "" {
		return policy.DefaultLine
	}
	return r.Line
}

// Inbound reports whether somebody rang this house. True for the zero value,
// which is what makes every record written before outbound calling existed
// still mean what it says.
func (r Record) Inbound() bool { return r.Direction != DirectionOutbound }

// direction is Inbound as the string a filter compares against.
func (r Record) direction() string {
	if r.Inbound() {
		return DirectionInbound
	}
	return DirectionOutbound
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
	ch chan Record
	// quit signals shutdown. The record channel is deliberately never
	// closed: a send on a closed channel panics *even inside a select with
	// a default*, so closing it would make Post panic for any session still
	// tearing down — which is reachable, because the daemon's deferred
	// Close runs while session goroutines are still live.
	quit chan struct{}
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
		quit:     make(chan struct{}),
		done:     make(chan struct{}),
		path:     path,
		maxBytes: maxBytes,
	}
	go w.run(f)
	return w, nil
}

// Post records a call. Never blocks, and never panics — including after
// Close, which a caller cannot always avoid: the daemon's deferred Close runs
// while sessions are still tearing down, and one of them reaching cleanup in
// that window would otherwise crash the process on its way out.
func (w *Writer) Post(r Record) {
	if w == nil {
		return
	}
	select {
	case <-w.quit:
		// Shutting down. Count it rather than dropping it into a buffer
		// nobody will drain, so the hole is still reported.
		w.dropped.Add(1)
		return
	default:
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
		close(w.quit)
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

	write := func(r Record) {
		line, err := json.Marshal(r)
		if err != nil {
			w.failed.Add(1)
			return
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
			return
		}
		size += int64(n)
	}

	for {
		select {
		case r := <-w.ch:
			write(r)
		case <-w.quit:
			// Drain what is already buffered, then stop. Close promises the
			// records in hand are written.
			for {
				select {
				case r := <-w.ch:
					write(r)
				default:
					return
				}
			}
		}
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
	// Caller is a substring of the number at the other end of the call, so a
	// partial number works. It matches the caller ID inbound and the dialled
	// number outbound: "did we ever speak to this number" is one question, not
	// two.
	Caller string
	// Line is exact and resolved, so "default" finds the records an install
	// with one number writes — which carry no line at all.
	Line string
	// Direction is DirectionInbound or DirectionOutbound, exact, empty for
	// either.
	Direction string
	Limit     int // most recent N, 0 for all
}

func (f Filter) match(r Record) bool {
	if !f.Since.IsZero() && r.Start.Before(f.Since) {
		return false
	}
	if f.Outcome != "" && r.Outcome != f.Outcome {
		return false
	}
	if f.Caller != "" && !strings.Contains(r.Caller, f.Caller) && !strings.Contains(r.Dialled, f.Caller) {
		return false
	}
	if f.Line != "" && r.LineOrDefault() != f.Line {
		return false
	}
	if f.Direction != "" && r.direction() != f.Direction {
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
