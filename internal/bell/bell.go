// Package bell is the box's side of the sync bell: an outbound, normally quiet
// WebSocket on which the account service says only "sync now". It is an
// accelerator for the ordinary HTTPS sync and never a channel of its own.
// Nothing read from the socket is configuration, a command or authority; the
// one message it acts on asks the caller to run the sync it would have run on
// its next poll anyway.
//
// Naming: the event journal's "doorbell" webhook mode runs the other way
// (box to consumer) and is unrelated.
package bell

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"callmemaybe/internal/syncwire"

	"github.com/gorilla/websocket"
)

// Defaults. The heartbeat matches the service's expectation of a ping about
// every 25 seconds (it stops counting the box as present after 60). Backoff
// runs 1 s doubling to 5 min, half fixed and half random, and starts again
// from the bottom after a connection that stayed up for a minute.
const (
	DefaultHeartbeat    = 25 * time.Second
	DefaultPongWait     = 10 * time.Second
	DefaultMinBackoff   = time.Second
	DefaultMaxBackoff   = 5 * time.Minute
	DefaultStable       = time.Minute
	DefaultRefusedCheck = 30 * time.Second
	// DefaultQuiet is how long an identical failure line stays suppressed
	// while the bell keeps failing the same way.
	DefaultQuiet = time.Hour
)

// Server close codes on an established socket.
const (
	CloseReplaced         = 4001
	CloseHeartbeatTimeout = 4002
)

// maxFrame bounds what the box will read. The service's only message is a
// dozen bytes; anything near this is not the service talking.
const maxFrame = 1024

// replacedWarnAfter is how many consecutive "replaced" closes it takes to say
// that two processes are probably fighting over the box's one socket.
const replacedWarnAfter = 3

// ErrGone means the box is no longer enrolled (its owner removed it). Run
// rings once, so the caller's sync pass sees the removal the way it always
// does, and returns an error wrapping this one.
var ErrGone = errors.New("bell: this box is no longer enrolled")

// Refused is a dial or credential error that the same credential will always
// get: the service refused it, or there is no usable one. Run does not retry
// until Credential reports a different credential.
type Refused struct {
	// Reason is categorical, fit for a log line: a status and code.
	Reason string
}

func (e *Refused) Error() string { return "bell: refused for this credential (" + e.Reason + ")" }

// Later is a failure that names its own minimum wait, as a service's
// Retry-After does. Run waits at least After before the next attempt.
type Later struct {
	Err   error
	After time.Duration
}

func (e *Later) Error() string { return e.Err.Error() }
func (e *Later) Unwrap() error { return e.Err }

// Bell keeps one bell connection alive for as long as Run's context lives.
// Dial, Credential and Ring are required; the rest default.
type Bell struct {
	// Dial opens the socket. An error wrapping ErrGone stops Run; a
	// *Refused waits for a new credential; anything else backs off.
	Dial func(context.Context) (*websocket.Conn, error)
	// Credential returns an opaque tag for the credential Dial would use, so
	// a refusal can wait for a new one without asking the service again.
	Credential func(context.Context) (string, error)
	// Ring asks for an ordinary sync now. It must not block: the caller
	// merges a burst of rings into one run.
	Ring func()
	// Healthy reports transitions: true once a connected socket has answered
	// a heartbeat, false when it is lost. Optional.
	Healthy func(bool)
	// Log receives state transitions only, never payloads or credentials.
	// Optional.
	Log func(string)

	Heartbeat, PongWait, MinBackoff, MaxBackoff, Stable, RefusedCheck, Quiet time.Duration
	// Jitter returns a random duration in [0, d]. Tests pin it.
	Jitter func(time.Duration) time.Duration

	quiet    map[string]time.Time
	healthy  bool
	replaced int
}

// Run keeps the bell up until ctx ends, and returns nil then. It returns
// early only with an error wrapping ErrGone, after ringing once so the
// caller's sync pass sees the removal and stops the way it always does.
func (b *Bell) Run(ctx context.Context) error {
	b.defaults()
	failures := 0
	for {
		tag, err := b.Credential(ctx)
		var conn *websocket.Conn
		if err == nil {
			conn, err = b.Dial(ctx)
		}
		if ctx.Err() != nil {
			if conn != nil {
				_ = conn.Close()
			}
			return nil
		}
		var refused *Refused
		switch {
		case err == nil:
		case errors.Is(err, ErrGone):
			b.Ring()
			return err
		case errors.As(err, &refused):
			// Not a fault to back off from: this credential will never be
			// let in. Ordinary polling carries on; try again only once a
			// login or rotation has stored a different one.
			b.say(refused.Error() + "; polling as usual until it changes")
			if err := b.awaitNewCredential(ctx, tag); err != nil || ctx.Err() != nil {
				return err
			}
			failures = 0
			continue
		default:
			b.say(fmt.Sprintf("bell: unavailable (%v); polling as usual", err))
			if !b.sleep(ctx, b.backoff(failures, err)) {
				return nil
			}
			failures++
			continue
		}
		started := time.Now()
		reason := b.session(ctx, conn)
		b.setHealthy(false)
		if ctx.Err() != nil {
			return nil
		}
		if time.Since(started) >= b.Stable {
			failures = 0
			b.quiet = map[string]time.Time{}
		}
		b.say("bell: disconnected (" + reason + "); polling as usual")
		if reason == "replaced" {
			b.replaced++
			if b.replaced == replacedWarnAfter {
				b.say("bell: replaced by a newer connection for this box again and again; is a second `doorman sync run` running?")
			}
		} else {
			b.replaced = 0
		}
		if !b.sleep(ctx, b.backoff(failures, nil)) {
			return nil
		}
		failures++
	}
}

func (b *Bell) defaults() {
	set := func(d *time.Duration, v time.Duration) {
		if *d <= 0 {
			*d = v
		}
	}
	set(&b.Heartbeat, DefaultHeartbeat)
	set(&b.PongWait, DefaultPongWait)
	set(&b.MinBackoff, DefaultMinBackoff)
	set(&b.MaxBackoff, DefaultMaxBackoff)
	set(&b.Stable, DefaultStable)
	set(&b.RefusedCheck, DefaultRefusedCheck)
	set(&b.Quiet, DefaultQuiet)
	if b.Jitter == nil {
		b.Jitter = func(d time.Duration) time.Duration { return rand.N(d + 1) }
	}
	if b.quiet == nil {
		b.quiet = map[string]time.Time{}
	}
}

// backoff is the wait before attempt failures+1: MinBackoff doubling to
// MaxBackoff, half fixed and half random so boxes that dropped together come
// back apart. A service's Retry-After is honoured when longer.
func (b *Bell) backoff(failures int, err error) time.Duration {
	d := b.MaxBackoff
	if failures < 30 {
		d = min(b.MinBackoff<<failures, b.MaxBackoff)
	}
	d = d/2 + b.Jitter(d/2)
	var later *Later
	if errors.As(err, &later) && later.After > d {
		d = later.After
	}
	return d
}

// session runs one connected socket until it fails, and says why in a few
// words fit for a log line.
func (b *Bell) session(ctx context.Context, conn *websocket.Conn) string {
	defer conn.Close()
	conn.SetReadLimit(maxFrame)
	done := make(chan struct{})
	defer close(done)
	frames := make(chan []byte)
	failed := make(chan error, 1)
	go func() {
		for {
			kind, data, err := conn.ReadMessage()
			if err != nil {
				failed <- err
				return
			}
			if kind != websocket.TextMessage {
				continue
			}
			select {
			case frames <- data:
			case <-done:
				return
			}
		}
	}()
	// Ping at once: the first pong is what makes the socket count as
	// healthy, and it tells the service the box is present.
	ping := time.NewTimer(0)
	defer ping.Stop()
	var pongDue <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
			return "shutting down"
		case <-ping.C:
			_ = conn.SetWriteDeadline(time.Now().Add(b.PongWait))
			if conn.WriteMessage(websocket.TextMessage, []byte("ping")) != nil {
				return "network"
			}
			if pongDue == nil {
				pongDue = time.After(b.PongWait)
			}
			ping.Reset(b.Heartbeat)
		case <-pongDue:
			return "no heartbeat answer"
		case data := <-frames:
			if string(data) == "pong" {
				pongDue = nil
				if !b.healthy {
					b.setHealthy(true)
					b.say("bell: connected; polling slowly while it stays up")
					// Anything queued while the bell was down rang nobody.
					b.Ring()
				}
				continue
			}
			var signal syncwire.BellSignal
			if json.Unmarshal(data, &signal) == nil && signal.T == syncwire.Sync {
				b.Ring()
			}
			// Anything else is ignored: never configuration, never a command.
		case err := <-failed:
			return closeClass(err)
		}
	}
}

// awaitNewCredential checks the stored credential, which is a local read,
// until it differs from the one the service refused.
func (b *Bell) awaitNewCredential(ctx context.Context, refused string) error {
	for {
		if !b.sleep(ctx, b.RefusedCheck) {
			return nil
		}
		tag, err := b.Credential(ctx)
		if errors.Is(err, ErrGone) {
			b.Ring()
			return err
		}
		if err == nil && tag != refused {
			b.quiet = map[string]time.Time{}
			return nil
		}
	}
}

func (b *Bell) setHealthy(up bool) {
	if b.healthy == up {
		return
	}
	b.healthy = up
	if b.Healthy != nil {
		b.Healthy(up)
	}
}

// say logs a line unless the identical line was logged within Quiet. A bell
// that keeps failing the same way says so once an hour, not once a minute;
// a connection that stayed up clears the memory, so a later outage is told.
func (b *Bell) say(line string) {
	if b.Log == nil {
		return
	}
	if at, ok := b.quiet[line]; ok && time.Since(at) < b.Quiet {
		return
	}
	b.quiet[line] = time.Now()
	b.Log(line)
}

func (b *Bell) sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// closeClass turns a read failure into a reason class. Close reasons from
// the service are not repeated; the code says enough.
func closeClass(err error) string {
	if errors.Is(err, websocket.ErrReadLimit) {
		return "protocol"
	}
	var closed *websocket.CloseError
	if !errors.As(err, &closed) {
		return "network"
	}
	switch closed.Code {
	case CloseReplaced:
		return "replaced"
	case CloseHeartbeatTimeout:
		return "service heartbeat timeout"
	case websocket.ClosePolicyViolation, websocket.CloseMessageTooBig:
		return "protocol"
	}
	return "closed by the service"
}
