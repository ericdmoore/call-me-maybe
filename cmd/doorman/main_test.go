package main

import (
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

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

// ── `doorman check`: the defaults it applied, made visible ───────────────
//
// This is the other half of the unknown-key defect. A misspelled key is
// invisible in the file but obvious in the output — provided the output says
// what each setting actually resolved to, rather than staying silent when it
// resolved to nothing.

func TestCheckShowsWhereADefaultWasApplied(t *testing.T) {
	if got := orDefault("", noMailbox); got != noMailbox {
		t.Errorf("orDefault empty = %q, want the explanatory default", got)
	}
	if got := orDefault("family", noMailbox); got != "family" {
		t.Errorf("orDefault set = %q", got)
	}
	if got := withDefault(policy.DefaultCallerIDFormat, true); !strings.Contains(got, "(default)") {
		t.Errorf("withDefault = %q, want it marked", got)
	}
	if got := withDefault("Lobby: {name}", false); strings.Contains(got, "(default)") {
		t.Errorf("withDefault = %q, want a configured value left alone", got)
	}
}

// Invariant 1: `doorman rotate` is the only thing that may print a PIN.
func TestCheckNeverPrintsAPIN(t *testing.T) {
	const pin = "428917"
	got := maskedPIN(pin)
	if strings.Contains(got, pin) {
		t.Fatalf("maskedPIN leaked the PIN: %q", got)
	}
	if !strings.Contains(got, "6 digits") {
		t.Errorf("maskedPIN = %q, want the length shown", got)
	}
	if !strings.Contains(maskedPIN(policy.PlaceholderPIN), "doorman init") {
		t.Errorf("placeholder should say what to do: %q", maskedPIN(policy.PlaceholderPIN))
	}
}

func TestCheckDescribesEveryRingStage(t *testing.T) {
	pol, err := policy.FromTOML([]byte(`
[house]
handsets = ["kitchen"]

[[handsets]]
id = "kitchen"
endpoint = "PJSIP/kitchen"

[[handsets]]
id = "kids-room"
endpoint = "PJSIP/kids-room"

[[schedules]]
id = "school-night"
start = "20:30"
end = "07:00"
days = ["SU", "MO"]

[[extensions]]
pin = "428917"
label = "Flat"
handsets = ["kitchen"]

[[extensions]]
pin = "310244"
label = "Ladder"
voicemail = "kids"
afterhours = "school-night"

  [[extensions.steps]]
  handsets = ["kids-room"]
  rings = 3

  [[extensions.steps]]
  handsets = ["kitchen"]
  seconds = 24
`))
	if err != nil {
		t.Fatalf("policy: %v", err)
	}

	for _, e := range pol.Extensions() {
		switch e.Label {
		case "Flat":
			// A stage with no explicit timing runs on the configured default,
			// and must say so rather than reading as "no timing at all".
			if got := describePlan(e.Plan); !strings.Contains(got, "default ring time") {
				t.Errorf("flat plan = %q, want the default named", got)
			}
			if got := describeAfterhours(e, time.Now()); got != noAfterhours {
				t.Errorf("afterhours = %q, want the explanatory default", got)
			}
		case "Ladder":
			got := describePlan(e.Plan)
			for _, want := range []string{"PJSIP/kids-room", "for 3 rings", "then", "PJSIP/kitchen", "for 24s"} {
				if !strings.Contains(got, want) {
					t.Errorf("ladder plan = %q, want it to mention %q", got, want)
				}
			}
			ah := describeAfterhours(e, time.Now())
			if !strings.Contains(ah, "school-night") || !strings.Contains(ah, "20:30–07:00 SU MO") {
				t.Errorf("afterhours = %q, want the schedule and its window", ah)
			}
			// No afterhours_ring: say where a caller in the window ends up.
			if got := describeAfterhoursPlan(e); !strings.Contains(got, "kids") {
				t.Errorf("afterhours plan = %q, want the mailbox named", got)
			}
		}
	}
}

// ── `doorman check`: the lines it found ──────────────────────────────────

// capture runs f with stdout redirected. `doorman check` is output, so its
// output is what there is to test.
func capture(t *testing.T, f func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stdout
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		var b strings.Builder
		_, _ = io.Copy(&b, r)
		done <- b.String()
	}()
	f()
	os.Stdout = saved
	_ = w.Close()
	out := <-done
	_ = r.Close()
	return out
}

func TestCheckListsEveryLineAndWhatItResolvesTo(t *testing.T) {
	results := []checkedLine{
		{LineFile: policy.LineFile{Name: policy.DefaultLine, Path: "./policy.toml"}},
		{LineFile: policy.LineFile{Name: "biz", Path: "./policy.biz.toml"}},
		{LineFile: policy.LineFile{Name: "kids", Path: "./policy.kids.toml"},
			err: errors.New("policy: toml: line 2")},
	}
	out := capture(t, func() { printLineSummary(results) })

	for _, want := range []string{
		"default", "./policy.toml", "the default line",
		"biz", "./policy.biz.toml",
		"kids", "./policy.kids.toml", "will not load",
		// Whoever reads this has to learn where an unrecognised name goes,
		// because it is the one behaviour they cannot discover from the files.
		"Stasis(${DOORMAN_APP},line,<name>)", "default line",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the line summary should mention %q:\n%s", want, out)
		}
	}
}

// Separate namespaces are the design, so this is a note and not an error —
// but invariant 1 still holds: the labels are named, the digits never are.
func TestCheckFlagsAPINSharedBetweenLinesWithoutPrintingIt(t *testing.T) {
	const shared = "428917"
	pol := func(t *testing.T, label string) *policy.Policy {
		t.Helper()
		p, err := policy.FromTOML([]byte(`
[house]
handsets = ["kitchen"]

[[handsets]]
id = "kitchen"
endpoint = "PJSIP/kitchen"

[[extensions]]
pin = "` + shared + `"
label = "` + label + `"
handsets = ["kitchen"]
`))
		if err != nil {
			t.Fatalf("policy: %v", err)
		}
		return p
	}
	results := []checkedLine{
		{LineFile: policy.LineFile{Name: policy.DefaultLine}, pol: pol(t, "Kitchen")},
		{LineFile: policy.LineFile{Name: "biz"}, pol: pol(t, "Front Desk")},
	}
	out := capture(t, func() { printSharedPINs(results) })

	if strings.Contains(out, shared) {
		t.Fatalf("the shared-PIN note printed the PIN:\n%s", out)
	}
	for _, want := range []string{"Kitchen", "Front Desk", "share a PIN"} {
		if !strings.Contains(out, want) {
			t.Errorf("the note should mention %q:\n%s", want, out)
		}
	}
}

func TestCheckSaysNothingAboutPINsThatDoNotClash(t *testing.T) {
	one, err := policy.FromTOML([]byte(`
[house]
handsets = ["kitchen"]

[[handsets]]
id = "kitchen"
endpoint = "PJSIP/kitchen"

[[extensions]]
pin = "428917"
label = "Kitchen"
handsets = ["kitchen"]
`))
	if err != nil {
		t.Fatalf("policy: %v", err)
	}
	two, err := policy.FromTOML([]byte(`
[house]
handsets = ["kitchen"]

[[handsets]]
id = "kitchen"
endpoint = "PJSIP/kitchen"

[[extensions]]
pin = "310244"
label = "Front Desk"
handsets = ["kitchen"]
`))
	if err != nil {
		t.Fatalf("policy: %v", err)
	}
	out := capture(t, func() {
		printSharedPINs([]checkedLine{
			{LineFile: policy.LineFile{Name: policy.DefaultLine}, pol: one},
			{LineFile: policy.LineFile{Name: "biz"}, pol: two},
		})
	})
	if out != "" {
		t.Errorf("distinct PINs should produce no note, got:\n%s", out)
	}
}

func TestCheckDistinguishesNoScheduleFromASwitchedOffOne(t *testing.T) {
	// Identical at call time, entirely different to whoever wrote the file.
	pol, err := policy.FromTOML([]byte(`
[house]
handsets = ["kitchen"]

[[handsets]]
id = "kitchen"
endpoint = "PJSIP/kitchen"

[[schedules]]
id = "school-night"
enabled = false
start = "20:30"
end = "07:00"

[[extensions]]
pin = "428917"
label = "Kids"
handsets = ["kitchen"]
afterhours = "school-night"
`))
	if err != nil {
		t.Fatalf("policy: %v", err)
	}
	e, ok := pol.LookupExtension("428917")
	if !ok {
		t.Fatal("extension missing")
	}
	got := describeAfterhours(e, time.Now())
	if !strings.Contains(got, "school-night") || !strings.Contains(got, "switched off") {
		t.Errorf("afterhours = %q, want the disabled schedule still named", got)
	}
}
