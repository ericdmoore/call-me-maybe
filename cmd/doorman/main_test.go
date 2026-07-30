package main

import (
	"log/slog"
	"strings"
	"testing"

	"callmemaybe/internal/lobby"
	"callmemaybe/internal/policy"
	"callmemaybe/internal/tmpl"
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

// ── template answer parsing ──────────────────────────────────────────────

func testPolicy(t *testing.T) *policy.Policy {
	t.Helper()
	p, err := policy.FromTOML([]byte(`
[house]
handsets = ["kitchen"]

[[handsets]]
id = "kitchen"
label = "Kitchen"
endpoint = "PJSIP/kitchen"

[[handsets]]
id = "kids-room"
label = "Kids Room"
endpoint = "PJSIP/kids-room"

[[groups]]
id = "adults"
handsets = ["kitchen"]
`))
	if err != nil {
		t.Fatalf("policy: %v", err)
	}
	return p
}

func templateFor(t *testing.T, questions string) *tmpl.Template {
	t.Helper()
	tp, err := tmpl.Parse([]byte(`
[template]
id = "t"
name = "T"
version = "1.0.0"
` + questions + `
[[emit.extensions]]
label = "X"
pin = "$generate.pin"
handsets = ["kitchen"]
`))
	if err != nil {
		t.Fatalf("template: %v", err)
	}
	return tp
}

// --answers is what makes a template usable from a script, so each question
// type has to survive the round trip from a flag string.
func TestGatherAnswersFromFlag(t *testing.T) {
	tp := templateFor(t, `
[[questions]]
id = "room"
prompt = "?"
type = "handset"

[[questions]]
id = "who"
prompt = "?"
type = "handset-group"

[[questions]]
id = "box"
prompt = "?"
type = "mailbox"

[[questions]]
id = "redirect"
prompt = "?"
type = "yes-no"
`)
	got, err := gatherAnswers(tp, "room=kids-room,who=adults kitchen,box=kids,redirect=y", testPolicy(t))
	if err != nil {
		t.Fatalf("gatherAnswers: %v", err)
	}
	if got["room"] != "kids-room" {
		t.Errorf("room = %v", got["room"])
	}
	if list, ok := got["who"].([]string); !ok || len(list) != 2 {
		t.Errorf("handset-group did not split on spaces: %#v", got["who"])
	}
	if got["box"] != "kids" {
		t.Errorf("box = %v", got["box"])
	}
	if got["redirect"] != true {
		t.Errorf("yes-no did not parse y as true: %v", got["redirect"])
	}
}

func TestYesNoAcceptsTheUsualSpellings(t *testing.T) {
	tp := templateFor(t, `
[[questions]]
id = "q"
prompt = "?"
type = "yes-no"
`)
	for _, yes := range []string{"y", "Y", "yes", "YES", "true"} {
		got, err := gatherAnswers(tp, "q="+yes, testPolicy(t))
		if err != nil || got["q"] != true {
			t.Errorf("%q should be true, got %v (%v)", yes, got["q"], err)
		}
	}
	for _, no := range []string{"n", "no", "false", "nope"} {
		got, err := gatherAnswers(tp, "q="+no, testPolicy(t))
		if err != nil || got["q"] != false {
			t.Errorf("%q should be false, got %v (%v)", no, got["q"], err)
		}
	}
}

// The window format is one I invented, so it needs pinning down: start-end,
// with an optional :D-prefixed pipe-separated day list.
func TestTimeWindowFlagFormat(t *testing.T) {
	tp := templateFor(t, `
[[questions]]
id = "quiet"
prompt = "?"
type = "time-window"
default = { start = "20:30", end = "07:00", days = ["SU", "MO"] }
`)

	got, err := gatherAnswers(tp, "quiet=21:00-06:30", testPolicy(t))
	if err != nil {
		t.Fatalf("gatherAnswers: %v", err)
	}
	w, ok := got["quiet"].(tmpl.Window)
	if !ok {
		t.Fatalf("quiet is %T, want tmpl.Window", got["quiet"])
	}
	if w.Start != "21:00" || w.End != "06:30" {
		t.Errorf("window = %s-%s, want 21:00-06:30", w.Start, w.End)
	}
	// Days fall back to the template's default when not overridden.
	if len(w.Days) != 2 {
		t.Errorf("days = %v, want the default of two", w.Days)
	}

	got, err = gatherAnswers(tp, "quiet=22:00-05:00:DFR|SA", testPolicy(t))
	if err != nil {
		t.Fatalf("gatherAnswers: %v", err)
	}
	w = got["quiet"].(tmpl.Window)
	if len(w.Days) != 2 || w.Days[0] != "FR" || w.Days[1] != "SA" {
		t.Errorf("days = %v, want [FR SA]", w.Days)
	}
}

func TestUnknownTemplateIsReported(t *testing.T) {
	if _, err := byID("no-such-template"); err == nil {
		t.Fatal("expected an error for an unknown template id")
	} else if !strings.Contains(err.Error(), "template list") {
		t.Errorf("the error should point at `doorman template list`: %v", err)
	}
}
