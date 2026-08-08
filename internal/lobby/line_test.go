package lobby

import (
	"strings"
	"testing"
	"time"
)

// Line identity in the state machine: the per-line prompt pack, the
// on_no_input disposition, and the known caller's route through the collector.
//
// The claim these tests exist to falsify is that none of it costs a known
// caller anything. Grandma is the most common call this system handles and she
// dials nothing; if the house rings a dial window later than it used to, the
// feature is not worth having.

// linePolicy is testPolicy with a [line] section in front of it, which is
// exactly how an operator adds one.
func linePolicy(section string) string {
	return "[line]\n" + section + "\n" + testPolicy
}

// concierge is the shape a business line takes: a mailbox to land in, and a
// disposition that uses it.
const conciergePolicy = `
[line]
label       = "Mertaugh Enterprises"
number      = "+15125550142"
on_no_input = "voicemail"

[house]
handsets = ["kitchen", "office"]
voicemail = "family"

[[handsets]]
id = "kitchen"
endpoint = "PJSIP/kitchen"

[[handsets]]
id = "office"
endpoint = "PJSIP/office"

[[people]]
name = "Grandma"
numbers = ["512-555-0100"]

[[extensions]]
pin = "428917"
label = "Kitchen"
handsets = ["kitchen"]
`

// ── the known caller pays nothing ────────────────────────────────────────

// The latency pin. A known caller who dials nothing must reach the house the
// moment the welcome prompt ends — not a dial window later.
//
// The window is set to five seconds here and the assertion is half a second,
// so a regression that put a FirstDigitTimeout in front of the house would
// miss by an order of magnitude rather than by a scheduling hiccup.
func TestKnownCallerReachesTheHouseWithNoDialWindow(t *testing.T) {
	h := startTuned(t, testPolicy, "5125550100", nil, func(c *Config) {
		c.FirstDigitTimeout = 5 * time.Second
		c.InterDigitTimeout = 5 * time.Second
	})

	c := h.fake.expect(t, "Play")
	if promptOf(c) != "welcome-known" {
		t.Fatalf("prompt = %s, want welcome-known", promptOf(c))
	}
	start := time.Now()
	h.sess.PlaybackFinished(c.Args[2])

	h.fake.expect(t, "CreateBridge")
	if waited := time.Since(start); waited > 500*time.Millisecond {
		t.Fatalf("the house rang %s after the greeting ended; a known caller must not wait "+
			"out a dial window (FirstDigitTimeout was 5s)", waited)
	}

	h.sess.CallerGone()
	h.waitFinished(t)
}

// The other half: the greeting IS the window, so a known caller who wants one
// room rather than the whole house dials over it.
func TestKnownCallerCanDialAnExtensionOverTheGreeting(t *testing.T) {
	h := start(t, "5125550100", nil)

	c := h.fake.expect(t, "Play")
	if promptOf(c) != "welcome-known" {
		t.Fatalf("prompt = %s, want welcome-known", promptOf(c))
	}
	h.sess.Dtmf("4")
	if stop := h.fake.expect(t, "StopPlayback"); stop.Args[0] != c.Args[2] {
		t.Fatalf("stopped %s, want the welcome prompt %s", stop.Args[0], c.Args[2])
	}
	for _, d := range "28917" {
		h.sess.Dtmf(string(d))
	}

	h.fake.expect(t, "CreateBridge")
	h.fake.expect(t, "AddToBridge")
	h.fake.expect(t, "Ring")
	// The kitchen alone, not the house: she reached the extension she dialled.
	if o := h.fake.expect(t, "Originate"); o.Args[0] != "PJSIP/kitchen" {
		t.Fatalf("originated %s, want only PJSIP/kitchen", o.Args[0])
	}

	h.sess.CallerGone()
	h.waitFinished(t)
}

// A known caller who presses one key and then thinks better of it must still
// get the house. The keypad is a shortcut past it, never a gate in front.
func TestKnownCallerWhoStopsDiallingStillReachesTheHouse(t *testing.T) {
	h := start(t, "5125550100", nil)

	c := h.fake.expect(t, "Play")
	h.sess.Dtmf("4")
	h.fake.expect(t, "StopPlayback")
	_ = c

	// Silence from here. The inter-digit timer expires and admits her.
	h.fake.expect(t, "CreateBridge")
	h.fake.expect(t, "AddToBridge")
	h.fake.expect(t, "Ring")
	o1 := h.fake.expect(t, "Originate")
	o2 := h.fake.expect(t, "Originate")
	got := map[string]bool{o1.Args[0]: true, o2.Args[0]: true}
	if !got["PJSIP/kitchen"] || !got["PJSIP/office"] {
		t.Fatalf("originated %v, want the whole house", got)
	}
	// And the handset says who it is, not "Lobby".
	if !strings.Contains(o1.Args[3], "Grandma") {
		t.Errorf("caller id = %q, want the allow-list name", o1.Args[3])
	}

	h.sess.CallerGone()
	h.waitFinished(t)
}

// And one who fumbles the extension until the attempts run out. A stranger is
// dismissed here; somebody on the allow-list cannot be, because they were
// admitted before they touched a key.
func TestKnownCallerRunningOutOfAttemptsStillReachesTheHouse(t *testing.T) {
	h := start(t, "5125550100", nil)

	h.fake.expect(t, "Play") // welcome-known
	h.sess.Dtmf("0")         // barge in
	h.fake.expect(t, "StopPlayback")

	dial := func(rest string) {
		for _, d := range rest {
			h.sess.Dtmf(string(d))
		}
	}
	dial("00000") // attempt 1, seeded by the barge-in digit
	if c := h.finishPlayback(t); promptOf(c) != "invalid-extension" {
		t.Fatalf("prompt = %s, want invalid-extension", promptOf(c))
	}
	dial("111111") // attempt 2
	if c := h.finishPlayback(t); promptOf(c) != "invalid-extension" {
		t.Fatalf("prompt = %s, want invalid-extension", promptOf(c))
	}
	dial("222222") // attempt 3 exceeds MaxPinAttempts=2

	// A stranger would hear good-day here. She rings the house instead.
	h.fake.expect(t, "CreateBridge")
	h.fake.expect(t, "AddToBridge")
	h.fake.expect(t, "Ring")
	h.fake.expect(t, "Originate")
	h.fake.expect(t, "Originate")

	h.sess.CallerGone()
	h.waitFinished(t)
}

// ── on_no_input ──────────────────────────────────────────────────────────

// Absent a [line] section the lobby dismisses a silent caller, which is what
// it has always done. TestSilentCallerIsDismissedAfterFirstDigitTimeout is the
// same assertion from the other direction; this one names the default.
func TestNoInputDefaultsToTodaysDismissal(t *testing.T) {
	h := startWith(t, linePolicy(`label = "Home"`), "9995550199", nil)
	h.finishPlayback(t) // lobby greeting

	if c := h.finishPlayback(t); promptOf(c) != "good-day" {
		t.Fatalf("prompt = %s, want good-day — an unset on_no_input must dismiss", promptOf(c))
	}
	h.expectCleanup(t, "Hangup", "Hangup")
	h.waitFinished(t)
}

func TestNoInputRingsTheHouseWhenTheLineSaysSo(t *testing.T) {
	h := startWith(t, linePolicy("label = \"Home\"\non_no_input = \"ring-house\""),
		"9995550199", nil)
	h.finishPlayback(t) // lobby greeting; then silence

	h.fake.expect(t, "CreateBridge")
	h.fake.expect(t, "AddToBridge")
	h.fake.expect(t, "Ring")
	o := h.fake.expect(t, "Originate")
	h.fake.expect(t, "Originate")
	// The line's label is what rings, so whoever picks up knows which number
	// was dialled — the only thing they have to go on for a caller with no
	// name and no extension.
	if !strings.Contains(o.Args[3], "Home") {
		t.Errorf("caller id = %q, want the line label", o.Args[3])
	}

	h.sess.CallerGone()
	h.waitFinished(t)
}

// Without a label there is still something sensible on the display.
func TestNoInputRingHouseFallsBackToTheLobbyLabel(t *testing.T) {
	h := startWith(t, linePolicy(`on_no_input = "ring-house"`), "9995550199", nil)
	h.finishPlayback(t)

	h.fake.expect(t, "CreateBridge")
	h.fake.expect(t, "AddToBridge")
	h.fake.expect(t, "Ring")
	if o := h.fake.expect(t, "Originate"); !strings.Contains(o.Args[3], lobbyLabel) {
		t.Errorf("caller id = %q, want %q for an unlabelled line", o.Args[3], lobbyLabel)
	}

	h.sess.CallerGone()
	h.waitFinished(t)
}

// The concierge: never drop a lead. This is also the invariant 8 test for the
// new path — sendToVoicemail releases the caller into the dialplan, and
// nothing may hang that channel up afterwards.
func TestNoInputTakesAMessageAndNeverHangsUpTheCaller(t *testing.T) {
	h := startWith(t, conciergePolicy, "9995550199", nil)
	h.finishPlayback(t) // lobby greeting; then silence

	// Straight to the handoff: no bridge, no ring, nobody's phone disturbed.
	if c := h.fake.expect(t, "SetChannelVar"); c.Args[1] != "MAILBOX" || c.Args[2] != "family" {
		t.Fatalf("SetChannelVar = %v, want MAILBOX=family", c.Args)
	}
	if c := h.fake.expect(t, "Continue"); c.Args[1] != "voicemail-drop" || c.Args[2] != "s" {
		t.Fatalf("Continue = %v, want voicemail-drop,s", c.Args)
	}

	h.sess.CallerLeft() // the StasisEnd that follows a continue
	h.waitFinished(t)

	for {
		select {
		case c := <-h.fake.calls:
			if c.Name == "Hangup" && c.Args[0] == h.sess.ChannelID {
				t.Fatal("the caller was hung up after the voicemail handoff — " +
					"that cuts them off mid-greeting")
			}
		default:
			return
		}
	}
}

// Invariant 6, from the side that is easy to get wrong: the limiter counts
// wrong PINs, and saying nothing is not a wrong PIN. A caller who is simply
// quiet must never spend the budget — on any line, under any disposition.
func TestNoInputIsNeverAFailedAttempt(t *testing.T) {
	for _, tc := range []struct {
		name, section string
		// reached is the ARI call that proves the dial window actually
		// elapsed; cancelling before it would test nothing at all.
		reached string
	}{
		{"dismiss", `label = "Home"`, "Play"},
		{"ring-house", `on_no_input = "ring-house"`, "CreateBridge"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// A budget of one: a single Failure would block the next call.
			limiter := NewRateLimiter(true, 1, time.Hour)
			h := startWith(t, linePolicy(tc.section), "9995550199", limiter)
			h.finishPlayback(t) // lobby greeting; then silence

			h.fake.expect(t, tc.reached)
			h.sess.CallerGone()
			h.waitFinished(t)

			if limiter.Blocked("+19995550199", time.Now()) {
				t.Error("a caller who said nothing was rate limited for it")
			}
			if limiter.Size() != 0 {
				t.Errorf("limiter holds %d entries, want none — silence recorded a failure",
					limiter.Size())
			}
		})
	}
}

// ring-house must not become a way past the rate limiter. A caller who has
// already burned their budget is dismissed before the lobby opens, so the
// disposition never gets a say — and that ordering is the whole defence.
func TestARateLimitedCallerIsNotAdmittedByARingHouseLine(t *testing.T) {
	limiter := NewRateLimiter(true, 3, time.Hour)
	now := time.Now()
	for range 3 {
		limiter.Failure("+19995550199", now)
	}

	h := startWith(t, linePolicy(`on_no_input = "ring-house"`), "9995550199", limiter)

	if c := h.finishPlayback(t); promptOf(c) != "good-day" {
		t.Fatalf("prompt = %s, want good-day — a blocked caller must not reach the house",
			promptOf(c))
	}
	h.expectCleanup(t, "Hangup", "Hangup")
	h.waitFinished(t)
}

// ── the prompt pack ──────────────────────────────────────────────────────

// One binary, two voices. The pack is read from the policy the session
// captured, so swapping it is a policy edit like any other.
func TestTheLinesPromptPackOverridesTheDefault(t *testing.T) {
	h := startWith(t, linePolicy(`prompts = "concierge"`), "9995550199", nil)

	if c := h.fake.expect(t, "Play"); c.Args[1] != "sound:concierge/lobby-greeting" {
		t.Fatalf("played %q, want sound:concierge/lobby-greeting", c.Args[1])
	}

	h.sess.CallerGone()
	h.waitFinished(t)
}

// And without one, the media prefix from the environment — byte for byte what
// every install had before [line] existed.
func TestWithoutALinePackTheEnvironmentPrefixIsUsed(t *testing.T) {
	h := startWith(t, linePolicy(`label = "Home"`), "9995550199", nil)

	if c := h.fake.expect(t, "Play"); c.Args[1] != "sound:call-me-maybe/lobby-greeting" {
		t.Fatalf("played %q, want the PROMPT_MEDIA_PREFIX pack", c.Args[1])
	}

	h.sess.CallerGone()
	h.waitFinished(t)
}
