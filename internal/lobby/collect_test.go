package lobby

import (
	"context"
	"testing"
	"time"
)

// The variable-length collector, on its own. It is a primitive with more than
// one customer coming, so it is worth testing as one rather than only through
// the console.

func feed(events chan event, digits string) {
	for _, d := range digits {
		events <- event{evDtmf, string(d)}
	}
}

func collectFrom(t *testing.T, spec collectSpec, digits string) (string, endReason) {
	t.Helper()
	events := make(chan event, 32)
	feed(events, digits)
	if spec.First == 0 {
		spec.First = 60 * time.Millisecond
	}
	if spec.Inter == 0 {
		spec.Inter = 30 * time.Millisecond
	}
	return collectDigits(context.Background(), events, spec)
}

func TestCollectEndsOnHash(t *testing.T) {
	got, reason := collectFrom(t, collectSpec{}, "5125550100#")
	if got != "5125550100" || reason != endHash {
		t.Fatalf("got %q/%s, want the number and %s", got, reason, endHash)
	}
}

// A person who stops dialling has finished dialling. # is how you skip the
// wait, not how you consent.
func TestCollectEndsOnAPause(t *testing.T) {
	got, reason := collectFrom(t, collectSpec{}, "5125550100")
	if got != "5125550100" || reason != endTimeout {
		t.Fatalf("got %q/%s, want the number and %s", got, reason, endTimeout)
	}
}

// Silence is a different intent from a short entry, and callers treat them
// differently — so the collector must not conflate them.
func TestCollectDistinguishesSilenceFromAShortEntry(t *testing.T) {
	if got, reason := collectFrom(t, collectSpec{}, ""); got != "" || reason != endNothing {
		t.Fatalf("got %q/%s, want nothing and %s", got, reason, endNothing)
	}
	if got, reason := collectFrom(t, collectSpec{}, "51"); got != "51" || reason != endTimeout {
		t.Fatalf("got %q/%s, want a short entry and %s", got, reason, endTimeout)
	}
}

// A ceiling exists because this runs on an open phone line: without one a
// stuck keypad accumulates for as long as the timer allows.
func TestCollectStopsAtTheCeiling(t *testing.T) {
	got, reason := collectFrom(t, collectSpec{Max: 4}, "12345678")
	if got != "1234" || reason != endFull {
		t.Fatalf("got %q/%s, want 1234 and %s", got, reason, endFull)
	}
}

// Star is the only "I made a mistake" key on a keypad.
func TestCollectStarClearsTheEntry(t *testing.T) {
	got, reason := collectFrom(t, collectSpec{}, "999*5125550100#")
	if got != "5125550100" || reason != endHash {
		t.Fatalf("got %q/%s, want the corrected number", got, reason)
	}
}

func TestCollectTakesASeedFromBargeIn(t *testing.T) {
	got, reason := collectFrom(t, collectSpec{Seed: "5"}, "125550100#")
	if got != "5125550100" || reason != endHash {
		t.Fatalf("got %q/%s, want the seed at the front", got, reason)
	}
	// A seed that is itself the whole entry, which is how a one-digit menu
	// choice arrives.
	if got, reason := collectFrom(t, collectSpec{Max: 1, Seed: "2"}, ""); got != "2" || reason != endFull {
		t.Fatalf("got %q/%s, want the seed alone", got, reason)
	}
}

// A cancelled call has no entry, only an ending: returning half a number here
// would let a caller who hung up place a call.
func TestCollectReturnsNothingWhenTheCallEnds(t *testing.T) {
	events := make(chan event, 8)
	feed(events, "512")
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()
	got, reason := collectDigits(ctx, events, collectSpec{
		First: time.Second, Inter: time.Second,
	})
	if got != "" || reason != endGone {
		t.Fatalf("got %q/%s, want nothing and %s", got, reason, endGone)
	}
}

// Letters exist on the wire and on nobody's keypad, and a playback finishing
// is not a keypress. Neither may end an entry or become part of one.
func TestCollectIgnoresWhatIsNotADigit(t *testing.T) {
	events := make(chan event, 32)
	events <- event{evPlaybackDone, "pb-1"}
	events <- event{evDtmf, "A"}
	feed(events, "5125550100#")
	got, reason := collectDigits(context.Background(), events, collectSpec{
		First: 60 * time.Millisecond, Inter: 30 * time.Millisecond,
	})
	if got != "5125550100" || reason != endHash {
		t.Fatalf("got %q/%s", got, reason)
	}
}

// A bare # is an empty entry rather than a panic waiting to happen: the menu
// reads the result before it reads a byte of it.
func TestCollectHandlesABareHash(t *testing.T) {
	if got, reason := collectFrom(t, collectSpec{Max: 1}, "#"); got != "" || reason != endHash {
		t.Fatalf("got %q/%s, want an empty entry and %s", got, reason, endHash)
	}
}
