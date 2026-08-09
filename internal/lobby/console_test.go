package lobby

import (
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// The outbound console, driven through the same fake Asterisk the lobby uses.
// Nothing here needs a trunk, and the one thing that must never happen — an
// emergency call leaving by a line whose registered address is not this house
// — is asserted rather than described.

type consoleHarness struct {
	fake     *fakeARI
	console  *Console
	finished chan struct{}
	rec      *testRecorder
}

var testConsoleLines = []ConsoleLine{
	{Name: "default", Label: "Home", CallerID: "+15125550100", Trunk: "voipms"},
	{Name: "biz", Label: "Mertaugh Enterprises", CallerID: "+15125550142", Trunk: "telnyx"},
}

// startConsole runs a console against the fake. Timeouts are real but short,
// as everywhere else in this package: no fake clock, tens of milliseconds.
func startConsole(t *testing.T, lines []ConsoleLine, tweak ...func(*fakeARI)) *consoleHarness {
	t.Helper()
	return startConsoleWith(t, lines, nil, tweak...)
}

// startConsoleWith is startConsole with the wiring adjustable. Tuning happens
// before Run so a test never writes to deps a running console is reading.
func startConsoleWith(t *testing.T, lines []ConsoleLine, tune func(*ConsoleDeps), tweak ...func(*fakeARI)) *consoleHarness {
	t.Helper()
	h := &consoleHarness{fake: newFakeARI(), finished: make(chan struct{}), rec: &testRecorder{}}
	for _, tw := range tweak {
		tw(h.fake)
	}
	deps := ConsoleDeps{
		ARI: h.fake,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Cfg: Config{
			DefaultCountryCode: "1",
			FirstDigitTimeout:  120 * time.Millisecond,
			InterDigitTimeout:  60 * time.Millisecond,
		},
		Lines:      func() []ConsoleLine { return lines },
		Calls:      h.rec,
		OnFinished: func(*Console) { close(h.finished) },
	}
	if tune != nil {
		tune(&deps)
	}
	h.console = NewConsole("ch-office-1", deps)
	go h.console.Run()
	return h
}

// play consumes exactly one playback, completes it, and returns its media URI.
// Used where the order of what was said is the assertion.
func (h *consoleHarness) play(t *testing.T) string {
	t.Helper()
	c := h.fake.expect(t, "Play")
	h.console.PlaybackFinished(c.Args[2])
	return c.Args[1]
}

// playUntil consumes calls, completing every playback on the way, until one
// plays media. Other calls are ignored: a barge-in produces a StopPlayback in
// the middle of a prompt sequence and these tests care about what was said.
func (h *consoleHarness) playUntil(t *testing.T, media string) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case c := <-h.fake.calls:
			if c.Name != "Play" {
				continue
			}
			h.console.PlaybackFinished(c.Args[2])
			if c.Args[1] == media {
				return
			}
		case <-deadline:
			t.Fatalf("the console never played %q", media)
		}
	}
}

// waitFor consumes calls until one matches name, completing any playback on
// the way so the console keeps moving.
func (h *consoleHarness) waitFor(t *testing.T, name string) fakeCall {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case c := <-h.fake.calls:
			if c.Name == name {
				return c
			}
			if c.Name == "Play" {
				h.console.PlaybackFinished(c.Args[2])
			}
		case <-deadline:
			t.Fatalf("the console never called %s", name)
			return fakeCall{}
		}
	}
}

// waitForVar waits for one named channel variable to be set and returns its
// value. A placed call sets two of them, and which order they go out in is not
// something any test should be asserting.
func (h *consoleHarness) waitForVar(t *testing.T, name string) string {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case c := <-h.fake.calls:
			if c.Name == "SetChannelVar" && c.Args[1] == name {
				return c.Args[2]
			}
			if c.Name == "Play" {
				h.console.PlaybackFinished(c.Args[2])
			}
		case <-deadline:
			t.Fatalf("the console never set %s", name)
			return ""
		}
	}
}

// runOut completes every playback until the console finishes, then returns
// everything it saw. For the tests whose assertion is about where a call ended
// up rather than the order it got there.
func (h *consoleHarness) runOut(t *testing.T) []fakeCall {
	t.Helper()
	var seen []fakeCall
	deadline := time.After(2 * time.Second)
	for {
		select {
		case c := <-h.fake.calls:
			seen = append(seen, c)
			if c.Name == "Play" {
				h.console.PlaybackFinished(c.Args[2])
			}
		case <-h.finished:
			for {
				select {
				case c := <-h.fake.calls:
					seen = append(seen, c)
				default:
					return seen
				}
			}
		case <-deadline:
			t.Fatal("the console never finished")
			return nil
		}
	}
}

func (h *consoleHarness) dial(digits string) {
	for _, d := range digits {
		h.console.Dtmf(string(d))
	}
}

func (h *consoleHarness) waitFinished(t *testing.T) {
	t.Helper()
	select {
	case <-h.finished:
	case <-time.After(2 * time.Second):
		t.Fatal("the console never finished")
	}
}

func called(seen []fakeCall, name string) bool {
	for _, c := range seen {
		if c.Name == name {
			return true
		}
	}
	return false
}

// The happy path, and the whole feature in one test: choose a line, hear the
// number that will be presented, dial, and leave by the dialplan carrying that
// caller ID.
func TestConsolePlacesACallAsTheChosenLine(t *testing.T) {
	h := startConsole(t, testConsoleLines)

	// The menu reads each line's outbound number out. That is the one fact
	// there is to choose between, and the only one that can be spoken without
	// a recording of somebody saying "Mertaugh Enterprises".
	for i, want := range []string{
		"digits:1", "digits:15125550100",
		"digits:2", "digits:15125550142",
	} {
		if got := h.play(t); got != want {
			t.Fatalf("menu clip %d = %q, want %q", i+1, got, want)
		}
	}

	h.console.Dtmf("2")

	// Confirmation before dialling, not after: a wrong choice costs a keypress
	// rather than a customer seeing the wrong number.
	h.playUntil(t, "digits:15125550142")
	h.playUntil(t, sayBeep)
	h.dial("5125550199#")

	// Both variables, from the same line. The caller ID and the trunk that
	// carries it are one decision: a provider will not present a number its
	// account does not own.
	if got := h.waitForVar(t, "OUTBOUND_TRUNK"); got != "telnyx" {
		t.Fatalf("OUTBOUND_TRUNK=%q, want telnyx — the chosen line's provider", got)
	}
	if got := h.waitForVar(t, "OUTBOUND_CID"); got != "+15125550142" {
		t.Fatalf("OUTBOUND_CID=%q, want +15125550142", got)
	}
	c := h.waitFor(t, "Continue")
	if c.Args[1] != "outbound-console" || c.Args[2] != "5125550199" {
		t.Fatalf("continued to %s at %s, want outbound-console at the dialled number",
			c.Args[1], c.Args[2])
	}

	// StasisEnd: the handset left our app alive, dialling a trunk. Invariant 8
	// — the console must not hang it up, or every outbound call dies at the
	// moment it is placed.
	h.console.CallerLeft()
	h.waitFinished(t)
	h.fake.expectNone(t, 50*time.Millisecond)
}

// The most consequential test in the package. E911 is registered per DID
// against a street address, so an emergency call placed as another line would
// reach a dispatcher with the wrong address on screen.
func TestConsoleRefusesEmergencyNumbers(t *testing.T) {
	for _, number := range []string{"911", "112", "999", "000"} {
		t.Run(number, func(t *testing.T) {
			h := startConsole(t, testConsoleLines)
			h.console.Dtmf("2")
			h.playUntil(t, sayBeep)
			h.dial(number + "#")

			seen := h.runOut(t)
			for _, name := range []string{"SetChannelVar", "Continue", "Originate"} {
				if called(seen, name) {
					t.Fatalf("an emergency number reached the dialplan (%s)", name)
				}
			}
			// Not detached, so the channel is hung up here rather than left
			// in a context that could dial anything.
			if !called(seen, "Hangup") {
				t.Error("the console did not release the handset after refusing")
			}
		})
	}
}

// A number that merely starts like an emergency number still goes out: the
// refusal is exact-match, because 512-555-0911 is somebody's phone.
func TestConsoleDialsNumbersThatMerelyLookLikeEmergencies(t *testing.T) {
	h := startConsole(t, testConsoleLines)
	h.console.Dtmf("1")
	h.playUntil(t, sayBeep)
	h.dial("9115550100#")

	h.waitFor(t, "SetChannelVar")
	if c := h.waitFor(t, "Continue"); c.Args[2] != "9115550100" {
		t.Fatalf("dialled %s", c.Args[2])
	}
	h.console.CallerLeft()
	h.waitFinished(t)
}

// A line with no outbound_cid and no trunk presents the dialplan's defaults —
// and both variables are set to empty anyway, because leaving the handset's own
// defaults in place would place the call as an identity the operator went out
// of their way to choose against. Clearing one and not the other would be the
// worst of the three: the business caller ID down the home trunk.
func TestConsoleClearsTheHandsetDefaultForALineWithNoCallerID(t *testing.T) {
	h := startConsole(t, []ConsoleLine{{Name: "default"}})
	h.console.Dtmf("1")
	h.playUntil(t, sayBeep)
	h.dial("5125550199#")

	if got := h.waitForVar(t, "OUTBOUND_TRUNK"); got != "" {
		t.Fatalf("OUTBOUND_TRUNK=%q, want it cleared", got)
	}
	if got := h.waitForVar(t, "OUTBOUND_CID"); got != "" {
		t.Fatalf("OUTBOUND_CID=%q, want it cleared", got)
	}
	h.waitFor(t, "Continue")
	h.console.CallerLeft()
	h.waitFinished(t)
}

// Hanging up mid-menu is cancellation and nothing else, and it still reaches
// the single teardown path (invariant 3).
func TestConsoleHangupMidMenuTearsDown(t *testing.T) {
	h := startConsole(t, testConsoleLines)
	h.fake.expect(t, "Play")
	h.console.CallerGone()

	seen := h.runOut(t)
	if called(seen, "Continue") {
		t.Fatal("a call was placed after the handset hung up")
	}
	if !called(seen, "Hangup") {
		t.Error("cleanup did not hang up a channel that never left the app")
	}
}

// Nothing dialled at the menu ends the call rather than leaving a handset
// listening to silence.
func TestConsoleGivesUpWhenNobodyChoosesALine(t *testing.T) {
	h := startConsole(t, testConsoleLines)

	seen := h.runOut(t)
	if called(seen, "Continue") {
		t.Fatal("a call was placed with no line chosen")
	}
	saidGoodbye := false
	for _, c := range seen {
		if c.Name == "Play" && c.Args[1] == sayGoodbye {
			saidGoodbye = true
		}
	}
	if !saidGoodbye {
		t.Error("the console ended without saying goodbye")
	}
}

// A digit no line answers to is a mistake, not an ending: say so and read the
// menu again.
func TestConsoleRepeatsTheMenuAfterABadChoice(t *testing.T) {
	h := startConsole(t, testConsoleLines)
	h.fake.expect(t, "Play")
	h.console.Dtmf("7")

	h.playUntil(t, sayInvalid)
	if got := h.play(t); got != "digits:1" {
		t.Fatalf("after a bad choice the console played %q, want the menu again", got)
	}
	h.console.CallerGone()
	h.waitFinished(t)
}

// Barge-in: a household member who knows the menu should not have to sit
// through it.
func TestConsoleTakesADigitOverTheMenu(t *testing.T) {
	h := startConsole(t, testConsoleLines)
	h.fake.expect(t, "Play") // digits:1, still playing
	h.console.Dtmf("2")
	h.playUntil(t, "digits:15125550142")

	h.console.CallerGone()
	h.waitFinished(t)
}

// The number ends on a pause as well as on #, because a person who stops
// dialling has finished dialling.
func TestConsoleAcceptsANumberEndedByAPause(t *testing.T) {
	h := startConsole(t, testConsoleLines)
	h.console.Dtmf("1")
	h.playUntil(t, sayBeep)
	h.dial("5125550199")

	h.waitFor(t, "SetChannelVar")
	if c := h.waitFor(t, "Continue"); c.Args[2] != "5125550199" {
		t.Fatalf("dialled %q", c.Args[2])
	}
	h.console.CallerLeft()
	h.waitFinished(t)
}

// Too few digits to be a number is a fumble, not a call.
func TestConsoleRefusesAnEntryTooShortToBeANumber(t *testing.T) {
	h := startConsole(t, testConsoleLines)
	h.console.Dtmf("1")
	h.playUntil(t, sayBeep)
	h.dial("4#")

	h.playUntil(t, sayInvalid)
	h.console.CallerGone()
	seen := h.runOut(t)
	if called(seen, "Continue") {
		t.Fatal("two digits were placed as a call")
	}
}

// A handoff that fails leaves the channel ours again — otherwise a failed
// release strands a handset nobody hangs up.
func TestConsoleReattachesWhenTheHandoffFails(t *testing.T) {
	h := startConsole(t, testConsoleLines, func(f *fakeARI) { f.failContinue.Store(true) })

	h.console.Dtmf("1")
	h.playUntil(t, sayBeep)
	h.dial("5125550199#")

	h.waitFor(t, "Continue")
	seen := h.runOut(t)
	if !called(seen, "Hangup") {
		t.Error("a failed handoff left the channel neither placed nor hung up")
	}
}

// A clip Asterisk cannot find degrades to silence and the console carries on.
// These are Asterisk's own sounds rather than the prompt pack, and the digits
// — which carry the meaning — are synthesised from files every install has.
func TestConsoleSurvivesMissingPrompts(t *testing.T) {
	h := startConsole(t, testConsoleLines, func(f *fakeARI) { f.failPlay.Store(true) })

	// Nothing to complete: every playback fails to start, so the console runs
	// on its timers alone.
	h.console.Dtmf("2")
	h.dial("5125550199#")

	if got := h.waitForVar(t, "OUTBOUND_CID"); got != "+15125550142" {
		t.Fatalf("presented %q with no audio available", got)
	}
	h.waitFor(t, "Continue")
	h.console.CallerLeft()
	h.waitFinished(t)
}

// Nine is the ceiling: a tenth line cannot be reached from a keypad, so it is
// dropped rather than shifting somebody else's digit.
func TestConsoleOffersAtMostNineLines(t *testing.T) {
	var many []ConsoleLine
	for i := range 12 {
		many = append(many, ConsoleLine{
			Name:     string(rune('a' + i)),
			CallerID: "+1512555010" + string(rune('0'+i%10)),
		})
	}
	h := startConsole(t, many)

	clips := 0
	for range 18 {
		if strings.HasPrefix(h.play(t), "digits:") {
			clips++
		}
	}
	// Nine lines, each a digit and a number, is exactly eighteen clips — and
	// then the menu is over rather than continuing to a tenth.
	if clips != 18 {
		t.Fatalf("menu played %d recognisable clips, want 18", clips)
	}
	h.console.CallerGone()
	h.waitFinished(t)
}
