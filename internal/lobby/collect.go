package lobby

import (
	"context"
	"time"
)

// Variable-length digit collection.
//
// Session.collect gathers a fixed number of digits and compares them against
// the extension map: it knows how long an entry is before it starts, and the
// last digit is the end of it. That is the right shape for a credential and
// the wrong shape for everything else — a phone number, a callback number, an
// amount — where the caller decides when they have finished.
//
// So this is the other collector: digits until the caller says stop. It is
// deliberately a free function over the session event channel rather than a
// method, because the two callers it is meant to serve (the outbound console
// here, the answering service's callback-number capture later) are different
// types that share nothing but this.

// endReason says why a collection stopped. Strings rather than an int enum so
// a log line reads without a lookup table.
type endReason string

const (
	// endHash: the caller pressed #, which is the only affirmative "done".
	endHash endReason = "hash"
	// endTimeout: the inter-digit timer expired with digits in hand. Treated
	// as complete, because a person who stops dialling has finished dialling —
	// # is how you skip the wait, not how you consent.
	endTimeout endReason = "inter-digit-timeout"
	// endNothing: the first-digit timer expired with nothing entered at all.
	// Distinct from endTimeout on purpose: silence is a different intent from
	// a short entry, and callers of this treat them differently.
	endNothing endReason = "no-digits"
	// endFull: the ceiling was reached. The digits are returned; nothing is
	// truncated silently.
	endFull endReason = "full"
	// endGone: the call ended underneath us. Cancellation is the state here as
	// everywhere else — there is no flag, only ctx.Done.
	endGone endReason = "call-ended"
)

// collectSpec is one variable-length collection.
type collectSpec struct {
	// First bounds the wait for the first digit, measured from whenever the
	// caller starts waiting — usually the end of a prompt.
	First time.Duration
	// Inter bounds the gap between digits, and expiring completes the entry.
	Inter time.Duration
	// Max caps the entry. 0 means no ceiling. A ceiling matters because this
	// runs on an open phone line: without one, a stuck keypad accumulates
	// unboundedly.
	Max int
	// Seed is a digit already captured by barge-in over the prompt.
	Seed string
}

// collectDigits gathers digits until the caller finishes, the entry fills, or
// the call ends. It never returns partial digits with endGone — a cancelled
// call has no entry, only an ending.
//
// '*' clears what has been entered so far, matching the lobby collector: on a
// keypad it is the only "I made a mistake" key there is.
//
// Events that are not DTMF are discarded. This is the only consumer of the
// event channel while it runs, so a PlaybackFinished arriving late from a
// prompt that was barged over has nowhere else to go.
func collectDigits(ctx context.Context, events <-chan event, spec collectSpec) (string, endReason) {
	digits := ""

	timer := time.NewTimer(spec.First)
	defer timer.Stop()
	reset := func(d time.Duration) {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(d)
	}

	// handle returns a reason once the entry is over, and "" to keep going.
	handle := func(d string) endReason {
		switch {
		case d == "#":
			return endHash
		case d == "*":
			digits = ""
			// Back to the generous window: someone who cleared an entry is
			// starting again, not continuing.
			reset(spec.First)
		case len(d) == 1 && d[0] >= '0' && d[0] <= '9':
			digits += d
			if spec.Max > 0 && len(digits) >= spec.Max {
				return endFull
			}
			reset(spec.Inter)
		}
		// A, B, C and D exist on the wire and on nobody's keypad. Ignored
		// rather than treated as an error: they cannot be pressed by accident.
		return ""
	}

	if spec.Seed != "" {
		if reason := handle(spec.Seed); reason != "" {
			return digits, reason
		}
	}

	for {
		select {
		case ev := <-events:
			if ev.kind != evDtmf {
				continue
			}
			if reason := handle(ev.value); reason != "" {
				return digits, reason
			}
		case <-timer.C:
			if digits == "" {
				return "", endNothing
			}
			return digits, endTimeout
		case <-ctx.Done():
			return "", endGone
		}
	}
}
