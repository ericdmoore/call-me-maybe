package main

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"callmemaybe/internal/ari"
	"callmemaybe/internal/calls"
	"callmemaybe/internal/lobby"
	"callmemaybe/internal/policy"
)

// ── a fake Asterisk, just enough to let route hand a call to a session ────
//
// internal/lobby has its own harness and must not gain a line-aware test —
// the whole design claim is that the state machine never learns lines exist.
// So the router gets its own fake here. Play returning an error is what makes
// these tests fast: a prompt that will not start degrades to silence, which is
// the documented behaviour, and the call unwinds in microseconds.
type silentARI struct{}

var _ lobby.ARI = silentARI{}

func (silentARI) AppName() string { return "doorman" }
func (silentARI) Play(context.Context, string, string, string) error {
	return context.Canceled // "no audio here", which play treats as silence
}
func (silentARI) StopPlayback(context.Context, string) error                  { return nil }
func (silentARI) Ring(context.Context, string) error                          { return nil }
func (silentARI) RingStop(context.Context, string) error                      { return nil }
func (silentARI) Hangup(context.Context, string) error                        { return nil }
func (silentARI) SetChannelVar(context.Context, string, string, string) error { return nil }
func (silentARI) ContinueToDialplan(context.Context, string, string, string, int) error {
	return nil
}
func (silentARI) CreateBridge(context.Context) (string, error)      { return "b", nil }
func (silentARI) AddToBridge(context.Context, string, string) error { return nil }
func (silentARI) DestroyBridge(context.Context, string) error       { return nil }
func (silentARI) Originate(context.Context, lobby.OriginateParams) (string, error) {
	return "leg", nil
}

func quiet() *slog.Logger { return slog.New(slog.DiscardHandler) }

// linePolicy compiles a policy whose house ring group names the line, so a
// test can tell which one a session was handed.
func linePolicy(t *testing.T, name string) *policy.Policy {
	t.Helper()
	p, err := policy.FromTOML([]byte(`
[house]
handsets = ["` + name + `"]

[[handsets]]
id = "` + name + `"
endpoint = "PJSIP/` + name + `"
`))
	if err != nil {
		t.Fatalf("policy for %s: %v", name, err)
	}
	return p
}

// testLines builds a set with a default line plus every name given, each with
// its own policy and its own rate limiter.
func testLines(t *testing.T, extra ...string) *lineSet {
	t.Helper()
	set := newLineSet()
	for _, name := range append([]string{policy.DefaultLine}, extra...) {
		pol := linePolicy(t, name)
		set.add(name, lobby.Deps{
			ARI:     silentARI{},
			Policy:  func() *policy.Policy { return pol },
			Limiter: lobby.NewRateLimiter(true, 1, time.Hour),
			Prompts: lobby.NewPrompts("test"),
			Log:     quiet(),
			// Deliberately not reg.remove: these tests assert that a call was
			// admitted, and a session that tidies itself up on another
			// goroutine would race the assertion.
			OnLegCreated: func(string, *lobby.Session) {},
			OnFinished:   func(*lobby.Session) {},
		})
	}
	return set
}

func lineOf(deps lobby.Deps) string {
	ends := deps.Policy().HouseEndpoints()
	if len(ends) == 0 {
		return "(none)"
	}
	return ends[0]
}

// ── falsification test 1: no line argument is today's behaviour ──────────
//
// This is the compatibility gate for every install that exists. If any of
// these reaches anything other than the default line's Deps, multi-line has
// changed the behaviour of a phone whose dialplan never asked for it.
func TestNoLineArgumentGetsTheDefaultLine(t *testing.T) {
	set := testLines(t, "biz", "kids")
	want := lineOf(set.deflt)

	cases := map[string][]string{
		"no arguments at all":                 nil,
		"an empty argument list":              {},
		"a leg correlation token":             {"leg", "abc123"},
		"an argument we do not know":          {"something-else"},
		"a line argument with no name":        {"line"},
		"a line argument with an empty name":  {"line", ""},
		"a line name that has no policy file": {"line", "typo"},
	}
	for what, args := range cases {
		if got := lineOf(set.forCall(args, quiet())); got != want {
			t.Errorf("%s reached line %s, want the default line %s", what, got, want)
		}
	}
}

// The same compatibility gate, in the call log. A named line goes on the
// record so one log can serve every number; the default line goes on unnamed,
// so an install with one number writes exactly what it wrote before lines
// existed and calls.Record.LineOrDefault resolves it on the way back out.
func TestOnlyANamedLineIsNamedOnTheRecord(t *testing.T) {
	if got := recordedLine(policy.DefaultLine); got != "" {
		t.Errorf("recordedLine(default) = %q, want empty", got)
	}
	if got := recordedLine("biz"); got != "biz" {
		t.Errorf("recordedLine(biz) = %q", got)
	}
	if got := (calls.Record{Line: recordedLine(policy.DefaultLine)}).LineOrDefault(); got != policy.DefaultLine {
		t.Errorf("an unnamed record resolves to %q, want the default line", got)
	}
}

// The other half: a line that does exist is the one that answers.
func TestNamedLineGetsItsOwnPolicy(t *testing.T) {
	set := testLines(t, "biz", "kids")
	for _, name := range []string{"biz", "kids"} {
		if got := lineOf(set.forCall([]string{"line", name}, quiet())); got != "PJSIP/"+name {
			t.Errorf("line %s resolved to %s", name, got)
		}
	}
	// The default line is reachable by name as well as by silence.
	if got := lineOf(set.forCall([]string{"line", policy.DefaultLine}, quiet())); got != "PJSIP/default" {
		t.Errorf("line default resolved to %s", got)
	}
}

// Never drop the call. doorman cannot read the dialplan, so a name it does not
// have is a config mistake to shout about, not a reason to stop answering a
// number somebody is paying for.
func TestUnknownLineIsServedAndLogged(t *testing.T) {
	set := testLines(t, "biz")
	var buf logBuffer
	deps := set.forCall([]string{"line", "bizz"}, slog.New(slog.NewTextHandler(&buf, nil)))

	if got := lineOf(deps); got != lineOf(set.deflt) {
		t.Errorf("unknown line reached %s, want the default line", got)
	}
	logged := buf.String()
	for _, want := range []string{"level=WARN", "bizz", "default line", "policy.bizz.toml"} {
		if !strings.Contains(logged, want) {
			t.Errorf("the warning should mention %q:\n%s", want, logged)
		}
	}
}

// Two lines, two budgets. The lines have opposite dispositions by design, so a
// redialler burning through a permissive business line's allowance must not
// lock the house out of its own phone — invariant 6's budget, per line.
func TestRateLimitBudgetsDoNotBleedBetweenLines(t *testing.T) {
	set := testLines(t, "biz")
	const caller = "+15125550111"
	now := time.Now()

	biz := set.forCall([]string{"line", "biz"}, quiet())
	home := set.forCall(nil, quiet())

	if biz.Limiter == home.Limiter {
		t.Fatal("both lines share one rate limiter — a redialler on one would lock out the other")
	}

	// One failure is the whole budget these limiters were built with.
	biz.Limiter.Failure(caller, now)
	if !biz.Limiter.Blocked(caller, now) {
		t.Fatal("the business line did not block a caller who burned its budget")
	}
	if home.Limiter.Blocked(caller, now) {
		t.Error("a caller blocked on the business line is blocked at home too — the budgets bled")
	}
}

// ── one store per line: independent failure ──────────────────────────────

const goodPolicy = `
[house]
handsets = ["kitchen"]

[[extensions]]
pin = "428917"
label = "Kitchen"
handsets = ["kitchen"]
`

const goodHandsets = `
[[handsets]]
id = "kitchen"
endpoint = "PJSIP/kitchen"
`

func policyDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	files["handsets.toml"] = goodHandsets
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}
	return dir
}

func openIn(t *testing.T, dir string) ([]openLine, error) {
	t.Helper()
	files, _ := policy.DiscoverLines(filepath.Join(dir, "policy.toml"))
	opened, err := openLines(files, filepath.Join(dir, "handsets.toml"), false, quiet())
	t.Cleanup(func() {
		for _, o := range opened {
			o.store.Close()
		}
	})
	return opened, err
}

// Invariant 4, generalised: an invalid policy must never take down a line it
// does not belong to. This is the whole argument for one file per line rather
// than [[lines]] sections in one, so it is the test that has to hold.
func TestACorruptLinePolicyLeavesTheOtherLinesAnswering(t *testing.T) {
	dir := policyDir(t, map[string]string{
		"policy.toml":     goodPolicy,
		"policy.biz.toml": "[house\nthis file is not TOML",
	})

	opened, err := openIn(t, dir)
	if err != nil {
		t.Fatalf("the home line should still load: %v", err)
	}
	if len(opened) != 1 || opened[0].name != policy.DefaultLine {
		t.Fatalf("opened %d lines, want just the default", len(opened))
	}
	if got := opened[0].store.Current().ExtensionCount(); got != 1 {
		t.Errorf("the home line resolved %d extensions, want 1 — it is not answering", got)
	}

	// And the calls that would have gone to biz are answered by the home line
	// rather than dropped.
	set := newLineSet()
	set.add(opened[0].name, lobby.Deps{Policy: opened[0].store.Current})
	if set.forCall([]string{"line", "biz"}, quiet()).Policy() == nil {
		t.Error("a call on the broken line reached no policy at all")
	}
}

// The default line is the whole phone rather than one number, so it is the one
// failure that is still fatal.
func TestACorruptDefaultPolicyIsAnError(t *testing.T) {
	dir := policyDir(t, map[string]string{
		"policy.toml":     "[house\nnot TOML either",
		"policy.biz.toml": goodPolicy,
	})
	if _, err := openIn(t, dir); err == nil {
		t.Fatal("a broken default policy must be reported, not survived")
	}
}

func TestEachLineGetsItsOwnStore(t *testing.T) {
	dir := policyDir(t, map[string]string{
		"policy.toml":      goodPolicy,
		"policy.biz.toml":  goodPolicy,
		"policy.kids.toml": goodPolicy,
	})
	opened, err := openIn(t, dir)
	if err != nil {
		t.Fatalf("openLines: %v", err)
	}
	if len(opened) != 3 {
		t.Fatalf("opened %d lines, want 3", len(opened))
	}
	seen := map[*policy.Store]bool{}
	for _, o := range opened {
		if seen[o.store] {
			t.Fatalf("line %s shares a store with another line", o.name)
		}
		seen[o.store] = true
	}
}

// ── the router, end to end ───────────────────────────────────────────────

// route is the one place lines are chosen, so the wiring gets tested through
// route rather than only through forCall. NewSession captures Deps.Policy
// synchronously, so by the time route returns the choice has been made.
func TestRouteHandsTheCallToTheLineTheDialplanNamed(t *testing.T) {
	client := ari.New(ari.Options{BaseURL: "http://127.0.0.1:1", App: "doorman", Log: quiet()})

	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"no argument", nil, "PJSIP/default"},
		{"line biz", []string{"line", "biz"}, "PJSIP/biz"},
		{"line typo", []string{"line", "nope"}, "PJSIP/default"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			set := testLines(t, "biz")
			reg := newRegistry()
			// Deps.Policy is called once per session; record what it handed out.
			seen := make(chan string, 1)
			for _, name := range set.names() {
				deps := set.byName[name]
				inner := deps.Policy
				endpoint := "PJSIP/" + name
				deps.Policy = func() *policy.Policy {
					select {
					case seen <- endpoint:
					default:
					}
					return inner()
				}
				set.add(name, deps)
			}

			route(ari.Event{
				Type:    "StasisStart",
				Args:    tc.args,
				Channel: &ari.Channel{ID: "chan-1"},
			}, reg, set, client, quiet())

			select {
			case got := <-seen:
				if got != tc.want {
					t.Errorf("the call was served by %s, want %s", got, tc.want)
				}
			case <-time.After(time.Second):
				t.Fatal("no line ever picked up the call")
			}
			// Whatever the line, the caller is a registered session: a line
			// argument must not change whether the call is answered.
			if reg.callerCount() != 1 {
				t.Errorf("registry holds %d callers, want 1", reg.callerCount())
			}
			reg.caller("chan-1").CallerGone()
		})
	}
}

// A leg entering Stasis is a handset answering, not a new call, and that must
// stay true whatever lines exist.
func TestRouteStillRecognisesALegArgument(t *testing.T) {
	set := testLines(t, "biz")
	reg := newRegistry()
	client := ari.New(ari.Options{BaseURL: "http://127.0.0.1:1", App: "doorman", Log: quiet()})

	s := session(t, "caller-1")
	reg.admit(s, 0)
	reg.addLeg("leg-1", s)

	route(ari.Event{
		Type:    "StasisStart",
		Args:    []string{"leg", s.ID},
		Channel: &ari.Channel{ID: "leg-1"},
	}, reg, set, client, quiet())

	if reg.callerCount() != 1 {
		t.Errorf("registry holds %d callers, want 1 — a leg must not become a caller",
			reg.callerCount())
	}
}

// ── helpers ──────────────────────────────────────────────────────────────

type logBuffer struct{ b []byte }

func (l *logBuffer) Write(p []byte) (int, error) { l.b = append(l.b, p...); return len(p), nil }
func (l *logBuffer) String() string              { return string(l.b) }
