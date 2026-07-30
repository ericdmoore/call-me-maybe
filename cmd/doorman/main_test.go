package main

import (
	"log/slog"
	"testing"

	"callmemaybe/internal/lobby"
	"callmemaybe/internal/policy"
)

// session builds the minimum Session the registry needs: a channel ID. Nothing
// here runs the state machine.
func session(t *testing.T, channelID string) *lobby.Session {
	t.Helper()
	pol, err := policy.FromTOML([]byte(`
[house]
handsets = ["kitchen"]

[[handsets]]
id = "kitchen"
endpoint = "PJSIP/kitchen"
`))
	if err != nil {
		t.Fatalf("policy: %v", err)
	}
	return lobby.NewSession(channelID, "+15125550100", lobby.Deps{
		Policy:  func() *policy.Policy { return pol },
		Limiter: lobby.NewRateLimiter(false, 3, 0),
		Log:     slog.New(slog.DiscardHandler),
	})
}

// Issue #11. The rate limiter counts PIN failures per caller and has nothing to
// say about a flood from many different numbers, so without a ceiling every
// handset rings until the spammer stops.
func TestAdmitEnforcesConcurrencyLimit(t *testing.T) {
	reg := newRegistry()
	const max = 3

	for i, id := range []string{"a", "b", "c"} {
		if !reg.admit(session(t, id), max) {
			t.Fatalf("call %d was refused below the limit", i+1)
		}
	}
	if reg.admit(session(t, "d"), max) {
		t.Fatal("the fourth call was admitted past a limit of 3")
	}
	if got := reg.callerCount(); got != max {
		t.Fatalf("registry holds %d callers, want %d — a refused call must not be registered", got, max)
	}
}

// A refused call must free its slot for the next one; capacity is a ceiling,
// not a lifetime budget.
func TestAdmitRecoversWhenACallEnds(t *testing.T) {
	reg := newRegistry()
	first := session(t, "a")
	reg.admit(first, 1)

	if reg.admit(session(t, "b"), 1) {
		t.Fatal("admitted a second call at a limit of 1")
	}
	reg.remove(first)
	if !reg.admit(session(t, "c"), 1) {
		t.Fatal("a slot did not free up after the first call ended")
	}
}

func TestAdmitUnlimitedWhenMaxIsZeroOrNegative(t *testing.T) {
	for _, max := range []int{0, -1} {
		reg := newRegistry()
		for i, id := range []string{"a", "b", "c", "d", "e", "f"} {
			if !reg.admit(session(t, id), max) {
				t.Fatalf("max=%d refused call %d; 0 or less means no limit", max, i+1)
			}
		}
	}
}

// The count and the insert must happen under one lock. Checking callerCount()
// and then calling addCaller() lets a burst of simultaneous INVITEs all pass a
// capacity test that none of them should have.
func TestAdmitIsAtomicUnderConcurrency(t *testing.T) {
	reg := newRegistry()
	const max = 5

	ids := make([]string, 64)
	for i := range ids {
		ids[i] = string(rune('a'+i/26)) + string(rune('a'+i%26))
	}
	sessions := make([]*lobby.Session, len(ids))
	for i, id := range ids {
		sessions[i] = session(t, id)
	}

	start := make(chan struct{})
	admitted := make(chan bool, len(ids))
	for _, s := range sessions {
		go func(s *lobby.Session) {
			<-start
			admitted <- reg.admit(s, max)
		}(s)
	}
	close(start)

	count := 0
	for range ids {
		if <-admitted {
			count++
		}
	}
	if count != max {
		t.Fatalf("%d calls admitted concurrently, want exactly %d", count, max)
	}
}
