// Package notify posts call events to one HTTP endpoint so the rest of the
// house can react to them — Home Assistant announcing "call from Grandma" on
// the kitchen speaker, a lamp flashing when the lobby is busy, a log in
// somebody else's system.
//
// # One integration point, and doorman knows nothing downstream
//
// There is exactly one endpoint and the payload is an *event*: what happened,
// who, and how it ended. It carries no media_player entities, no speaker
// names, no routing. Home Assistant already models the house's devices
// properly and doorman would only duplicate that badly — so routing lives
// where the speakers live, and every target HA supports (Sonos, Cast, MQTT,
// Alexa through HA) comes free without doorman learning what a speaker is.
// See .plans/s06-speaker-page-targets for the long form of that argument.
//
// # It can never delay a call
//
// Same discipline as internal/calls, for the same reason: Post hands off to a
// buffered channel drained by one goroutine, and a full buffer drops the event
// and counts it. A Home Assistant that is slow, wedged, or switched off is
// completely invisible to a caller. The one goroutine also keeps delivery
// ordered, which matters — "ringing" arriving after "completed" would make an
// announcement automation lie.
//
// # What is never sent
//
// Entered digits and PINs, at any level, no exception. That is structural
// rather than remembered: FromRecord is the only constructor the daemon uses
// and a calls.Record has no field an entered digit could occupy.
//
// Caller IDs are redacted by default, unlike the call log. The call log is a
// 0600 file on the box; this payload is handed to another program over the
// network, so the deliberate choice defaults the other way. Setting
// WEBHOOK_REDACT_CALLER_ID=false sends the full E.164, which is what an
// announcement of an unknown number needs — the zero value of Options stays
// redacted so nothing leaks by omission.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"callmemaybe/internal/calls"
	"callmemaybe/internal/policy"
)

// Event types. Two, because the useful moment and the complete moment are
// different: "call from Grandma" is worth announcing while the phone is still
// ringing, and worth nothing once it has stopped.
const (
	// EventRinging fires when handsets start ringing — for a known caller, or
	// for a stranger who dialled a valid extension. Nothing has happened yet,
	// so it carries identity and no outcome.
	EventRinging = "ringing"
	// EventCompleted fires once per call from the session's single teardown
	// path, however the call ended.
	EventCompleted = "completed"
)

// An Event is one thing that happened on the phone. Field names are short and
// snake_case because the reader is a Home Assistant automation template.
type Event struct {
	Type   string    `json:"event"`
	At     time.Time `json:"at"`
	CallID string    `json:"call_id"`

	// Line is which of this box's numbers was rung, and it is on the ringing
	// event as well as the completed one because that is the whole use for it:
	// announcing the business line on the office speaker and the house line
	// everywhere. Absent means the default line, so a payload from an install
	// with one number is byte-identical to what it was before lines existed.
	Line string `json:"line,omitempty"`
	// Direction is "outbound" on a call this house placed, absent for inbound.
	// Nothing posts an outbound event today — the console has no notifier,
	// because the two events here are about a phone ringing in this house —
	// but the projection below carries every field the record has rather than
	// quietly dropping one.
	Direction string `json:"direction,omitempty"`

	// Caller is redacted unless the operator opted out; empty when withheld.
	Caller string `json:"caller,omitempty"`
	// Known is the allow-list name that matched — the field an announcement
	// actually wants.
	Known string `json:"known,omitempty"`

	// Extension is the label a caller reached, never the PIN. PIN is "valid"
	// or "invalid" and never the digits.
	Extension string `json:"extension,omitempty"`
	PIN       string `json:"pin,omitempty"`

	Outcome    string `json:"outcome,omitempty"`
	Reason     string `json:"reason,omitempty"`
	AnsweredBy string `json:"answered_by,omitempty"`
	Mailbox    string `json:"mailbox,omitempty"`
	MS         int64  `json:"ms,omitempty"`
}

// FromRecord projects a call record onto an event.
//
// Building from the record rather than from loose arguments is the point: the
// record is the one structure in this system that already cannot hold an
// entered digit, so the webhook inherits that property instead of restating
// it. Nothing here reads the call *log* — invariant 10 is about the file, and
// this shares only the type.
func FromRecord(kind string, at time.Time, r calls.Record) Event {
	e := Event{
		Type:      kind,
		At:        at,
		CallID:    r.ID,
		Line:      r.Line,
		Direction: r.Direction,
		Caller:    r.Caller,
		Known:     r.Known,
		Extension: r.Extension,
		PIN:       r.PIN,
	}
	if kind == EventRinging {
		// The record's Outcome already reads "abandoned" — its zero value, so
		// that a call nobody set an outcome on is still recorded truthfully.
		// Shipping it on a ringing event would announce the opposite of what
		// is happening, so the outcome fields stay off until there is one.
		return e
	}
	e.Outcome, e.Reason = r.Outcome, r.Reason
	e.AnsweredBy, e.Mailbox, e.MS = r.AnsweredBy, r.Mailbox, r.MS
	return e
}

// Redacted returns a copy with the caller narrowed to a non-identifying
// fragment, the same shape calls.Record.Redacted produces.
func (e Event) Redacted() Event {
	e.Caller = policy.Redact(e.Caller)
	return e
}

// DefaultTimeout bounds one delivery attempt. Short on purpose: this is a
// notification, and one that arrives late has already missed the ring it was
// describing.
const DefaultTimeout = 3 * time.Second

// Options configures a Webhook. The zero value is safe rather than
// convenient — SendFullCallerID defaults off, so a caller ID cannot escape
// through a struct literal somebody forgot to fill in.
type Options struct {
	// URL is the endpoint. Treat it as a credential: a Home Assistant webhook
	// id is the entire secret, which is why it never reaches a log line.
	URL string
	// Token, when set, is sent as `Authorization: Bearer`. Home Assistant's
	// /api/webhook/<id> endpoints need nothing; its REST API and most other
	// receivers do.
	Token string
	// Timeout bounds one attempt. Zero means DefaultTimeout.
	Timeout time.Duration
	// SendFullCallerID sends the unredacted E.164. Off by default; see the
	// package comment for why this webhook defaults the other way from the
	// call log.
	SendFullCallerID bool
	Log              *slog.Logger
}

// A Webhook posts events without ever blocking the caller.
type Webhook struct {
	// ch is never closed, and that is deliberate: a session goroutine can
	// still be in its teardown when the daemon shuts down, and a send on a
	// closed channel panics even inside a select. Losing a notification at
	// shutdown is nothing; taking the process down with it is not. Close
	// signals through stop instead.
	ch   chan Event
	stop chan struct{}
	done chan struct{}

	ctx    context.Context
	cancel context.CancelFunc

	url     string
	host    string
	token   string
	full    bool
	timeout time.Duration
	client  *http.Client
	log     *slog.Logger

	dropped atomic.Int64
	failed  atomic.Int64

	closeOnce sync.Once
}

// Open validates the endpoint and starts the delivery goroutine. A bad URL is
// an error here, at startup, where an operator will see it — not silently at
// the end of the first call.
func Open(o Options) (*Webhook, error) {
	if o.URL == "" {
		return nil, errors.New("notify: no url")
	}
	u, err := url.Parse(o.URL)
	if err != nil {
		return nil, fmt.Errorf("notify: not a valid URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("notify: scheme must be http or https, got %q", u.Scheme)
	}
	if u.Host == "" {
		return nil, errors.New("notify: url has no host")
	}
	if o.Timeout <= 0 {
		o.Timeout = DefaultTimeout
	}
	if o.Log == nil {
		o.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}

	ctx, cancel := context.WithCancel(context.Background())
	w := &Webhook{
		// 64, matching the call log. Far beyond what a household generates,
		// and the point of the cap is the case that is not a household.
		ch:      make(chan Event, 64),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
		ctx:     ctx,
		cancel:  cancel,
		url:     o.URL,
		host:    u.Host,
		token:   o.Token,
		full:    o.SendFullCallerID,
		timeout: o.Timeout,
		client:  &http.Client{Timeout: o.Timeout},
		log:     o.Log,
	}
	go w.run()
	return w, nil
}

// Post queues an event. Never blocks, and never panics — including after
// Close, when it is accepted and quietly discarded.
func (w *Webhook) Post(e Event) {
	if w == nil {
		return
	}
	select {
	case w.ch <- e:
	default:
		w.dropped.Add(1)
	}
}

// Dropped is the number of events lost to a full buffer, Failed the number the
// endpoint refused or never received. Both are counters rather than errors:
// there is nobody on the call path to hand an error to.
func (w *Webhook) Dropped() int64 { return w.dropped.Load() }
func (w *Webhook) Failed() int64  { return w.failed.Load() }

// Host is the endpoint's host, which is the only part of the URL safe to
// print — see Options.URL.
func (w *Webhook) Host() string {
	if w == nil {
		return ""
	}
	return w.host
}

// Close drains what is buffered and stops the writer.
//
// The drain is bounded: a dead endpoint must not hold a shutdown open for the
// buffer depth times the timeout. Past the grace period the context is
// cancelled, which aborts the request in flight, and the backlog is discarded
// and counted.
func (w *Webhook) Close() error {
	if w == nil {
		return nil
	}
	w.closeOnce.Do(func() {
		close(w.stop)
		select {
		case <-w.done:
		case <-time.After(w.timeout):
			w.cancel()
			<-w.done
		}
	})
	w.cancel()
	return nil
}

func (w *Webhook) run() {
	defer close(w.done)
	for {
		select {
		case e := <-w.ch:
			w.deliver(e)
		case <-w.stop:
			// Drain what is already queued and then go. Close is a shutdown,
			// not an abort: the last event of a call is usually the one worth
			// having, and the deadline in Close caps how long this can take.
			for {
				select {
				case e := <-w.ch:
					w.deliver(e)
				default:
					return
				}
			}
		}
	}
}

func (w *Webhook) deliver(e Event) {
	if w.ctx.Err() != nil {
		// Shutting down against an endpoint that is not answering. Abandon
		// the backlog rather than hold the process open.
		w.dropped.Add(1)
		return
	}
	w.send(e)
}

func (w *Webhook) send(e Event) {
	if !w.full {
		e = e.Redacted()
	}
	body, err := json.Marshal(e)
	if err != nil {
		w.failed.Add(1)
		return
	}

	req, err := http.NewRequestWithContext(w.ctx, http.MethodPost, w.url, bytes.NewReader(body))
	if err != nil {
		w.failed.Add(1)
		w.warn("webhook request could not be built", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "doorman")
	if w.token != "" {
		req.Header.Set("Authorization", "Bearer "+w.token)
	}

	resp, err := w.client.Do(req)
	if err != nil {
		w.failed.Add(1)
		w.warn("webhook post failed", err)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	// Read and discard so the connection can be reused; a bounded read
	// because an endpoint that answers with a megabyte is not our problem.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))

	if resp.StatusCode >= 300 {
		w.failed.Add(1)
		w.log.Warn("webhook rejected the event", "host", w.host, "status", resp.StatusCode)
	}
}

// warn logs a delivery failure without the URL in it. net/url wraps transport
// errors together with the full URL, and that URL is a credential — a Home
// Assistant webhook id is the whole secret. Unwrap to the transport error and
// print the host on its own.
func (w *Webhook) warn(msg string, err error) {
	var uerr *url.Error
	if errors.As(err, &uerr) {
		err = uerr.Err
	}
	w.log.Warn(msg, "host", w.host, "err", err)
}
