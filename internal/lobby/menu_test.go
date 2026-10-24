package lobby

import (
	"strings"
	"testing"
	"time"

	"callmemaybe/internal/calls"
	"callmemaybe/internal/policy"
)

// The front menu (#150 M2): "1 → extension A, 2 → recording B, 3 →
// extension C behind the challenge", and everything at its edges.

const bakeryPolicy = `
[line]
label       = "Blue Door Bakery"
menu        = "front"
on_no_input = "voicemail"

[house]
handsets  = ["kitchen", "office"]
voicemail = "bakery"

[[handsets]]
id = "kitchen"
endpoint = "PJSIP/kitchen"

[[handsets]]
id = "office"
endpoint = "PJSIP/office"

[[people]]
name = "Grandma"
numbers = ["512-555-0100"]

[[schedules]]
id    = "closed"
start = "13:00"
end   = "15:00"

[[extensions]]
id        = "orders"
label     = "Orders"
handsets  = ["kitchen"]
voicemail = "orders"

[[extensions]]
id        = "baker"
label     = "The baker"
handsets  = ["office"]
voicemail = "baker"

[[extensions]]
id         = "late"
label      = "Late orders"
handsets   = ["office"]
voicemail  = "late"
afterhours = "closed"

[[menus]]
id  = "front"
say = "Thanks for calling Blue Door Bakery."

  [[menus.choices]]
  digit     = "1"
  extension = "orders"

  [[menus.choices]]
  digit     = "2"
  recording = "hours"

  [[menus.choices]]
  digit     = "3"
  extension = "baker"
  challenge = true

  [[menus.choices]]
  digit = "4"
  menu  = "more"

  [[menus.choices]]
  digit     = "5"
  extension = "late"
  challenge = true

[[menus]]
id     = "more"
repeat = 0

  [[menus.choices]]
  digit     = "1"
  voicemail = "general"

  [[menus.choices]]
  digit = "0"
  menu  = "front"

  [[menus.choices]]
  digit     = "9"
  recording = "directions"
  then      = "goodbye"
`

func bakery(t *testing.T, caller string, tune func(*Deps)) *harness {
	t.Helper()
	return startTuned(t, bakeryPolicy, caller, nil, func(d *Deps) {
		d.ChallengeLimiter = NewRateLimiter(true, 3, time.Hour)
		d.Rand = fixedDraw(1, 4) // 3 times 6
		if tune != nil {
			tune(d)
		}
	})
}

// expectRing consumes the start of a ring plan and returns the endpoint rung.
func (h *harness) expectRing(t *testing.T) string {
	t.Helper()
	h.fake.expect(t, "CreateBridge")
	h.fake.expect(t, "AddToBridge")
	h.fake.expect(t, "Ring")
	return h.fake.expect(t, "Originate").Args[0]
}

// hangUpAndFinish ends a call that is ringing or bridged.
func (h *harness) hangUpAndFinish(t *testing.T) {
	t.Helper()
	h.sess.CallerGone()
	h.waitFinished(t)
}

// The menu clip barges, and its key is the choice.
func (h *harness) chooseOver(t *testing.T, menu, key string) {
	t.Helper()
	if c := h.fake.expect(t, "Play"); promptOf(c) != "menus/"+menu {
		t.Fatalf("played %s, want menus/%s", promptOf(c), menu)
	}
	h.dial(key)
	h.fake.expect(t, "StopPlayback")
}

func TestMenuChoiceRingsItsExtension(t *testing.T) {
	h := bakery(t, "9995550199", nil)
	h.chooseOver(t, "front", "1")
	if ep := h.expectRing(t); ep != "PJSIP/kitchen" {
		t.Fatalf("rang %s, want the orders phone", ep)
	}
	h.hangUpAndFinish(t)
	rec := h.rec.only(t)
	if rec.Menu != "front" || rec.Choice != "extension:orders" || rec.Extension != "Orders" || rec.PIN != "" {
		t.Fatalf("record = %+v, want the menu, the target and never a key", rec)
	}
	if strings.Contains(rec.Choice, "1") {
		t.Fatal("the record carries the key pressed")
	}
}

// A menu line never runs the PIN lobby: no greeting, and a PIN typed at the
// menu is a wrong key, then another, never a credential.
func TestMenuLineNeverOpensThePinLobby(t *testing.T) {
	pol, err := policy.FromTOML([]byte(bakeryPolicy))
	if err != nil {
		t.Fatal(err)
	}
	if pol.ExtensionCount() != 3 || len(pol.PINs()) != 0 {
		t.Fatalf("extensions %d, PINs %d: want three reachable only by the menu", pol.ExtensionCount(), len(pol.PINs()))
	}
	h := bakery(t, "9995550199", nil)
	if c := h.fake.expect(t, "Play"); promptOf(c) != "menus/front" {
		t.Fatalf("played %s, want the menu and not the lobby greeting", promptOf(c))
	}
	h.hangUpAndFinish(t)
}

// After a recording plays, the caller is back at the menu that offered it.
func TestMenuRecordingReturnsToTheMenu(t *testing.T) {
	h := bakery(t, "9995550199", nil)
	h.chooseOver(t, "front", "2")
	h.expectPlays(t, "recordings/hours", "menus/front")
	h.hangUpAndFinish(t)
	if rec := h.rec.only(t); rec.Choice != "recording:hours" || rec.Outcome != calls.OutcomeAbandoned {
		t.Fatalf("record = %+v", rec)
	}
}

// A key over the recording is a choice at the menu that offered it.
func TestMenuKeyOverARecordingChoosesAtThatMenu(t *testing.T) {
	h := bakery(t, "9995550199", nil)
	h.chooseOver(t, "front", "2")
	if c := h.fake.expect(t, "Play"); promptOf(c) != "recordings/hours" {
		t.Fatalf("played %s", promptOf(c))
	}
	h.dial("1")
	h.fake.expect(t, "StopPlayback")
	if ep := h.expectRing(t); ep != "PJSIP/kitchen" {
		t.Fatalf("rang %s", ep)
	}
	h.hangUpAndFinish(t)
}

// The done-when, choice 3: a right answer rings extension C.
func TestMenuChallengedChoiceRingsAfterARightAnswer(t *testing.T) {
	h := bakery(t, "9995550199", nil)
	h.chooseOver(t, "front", "3")
	h.expectPlays(t, "challenge/explain")
	h.question(t, "3", "6")
	h.dial("18")
	h.expectPlays(t, "challenge/right")
	if ep := h.expectRing(t); ep != "PJSIP/office" {
		t.Fatalf("rang %s, want the baker", ep)
	}
	h.hangUpAndFinish(t)
	if rec := h.rec.only(t); rec.Challenge != calls.ChallengePassed || rec.Choice != "extension:baker" {
		t.Fatalf("record = %+v", rec)
	}
}

// And out of hours, its mailbox — asked before it records, never after.
func TestChallengedChoiceInAfterhoursAsksBeforeVoicemail(t *testing.T) {
	t.Run("right", func(t *testing.T) {
		h := bakery(t, "9995550199", nil)
		h.chooseOver(t, "front", "5")
		h.expectPlays(t, "challenge/explain")
		h.question(t, "3", "6")
		h.dial("18")
		h.expectPlays(t, "challenge/right")
		if c := h.fake.expect(t, "SetChannelVar"); c.Args[2] != "late" {
			t.Fatalf("SetChannelVar = %v, want the extension's mailbox", c.Args)
		}
		h.fake.expect(t, "Continue")
		h.sess.CallerLeft()
		h.waitFinished(t)
	})
	t.Run("wrong", func(t *testing.T) {
		h := bakery(t, "9995550199", func(d *Deps) { d.Rand = fixedDraw(1, 4, 1, 4, 1, 4) })
		h.chooseOver(t, "front", "5")
		h.expectPlays(t, "challenge/explain")
		for i := 0; i < 3; i++ {
			h.question(t, "3", "6")
			h.dial("1#")
			if i < 2 {
				h.expectPlays(t, "challenge/try-again")
			}
		}
		h.expectPlays(t, "good-day")
		h.fake.expect(t, "Hangup")
		h.expectCleanup(t, "Hangup")
		h.waitFinished(t)
		h.fake.expectNone(t, 30*time.Millisecond) // and never a Continue
		if rec := h.rec.only(t); rec.Outcome != calls.OutcomeDismissed || rec.Reason != "challenge-failed" {
			t.Fatalf("record = %+v", rec)
		}
	})
}

// * repeats; a key with no choice replays too. Neither is a guess at
// anything, so neither touches a limiter.
func TestMenuStarRepeatsAndAWrongKeyReplays(t *testing.T) {
	pins, sums := NewRateLimiter(true, 3, time.Hour), NewRateLimiter(true, 3, time.Hour)
	h := startTuned(t, bakeryPolicy, "9995550199", pins, func(d *Deps) { d.ChallengeLimiter = sums })
	h.chooseOver(t, "front", "*")
	h.chooseOver(t, "front", "7")
	h.chooseOver(t, "front", "1")
	h.expectRing(t)
	h.hangUpAndFinish(t)
	if pins.Size() != 0 || sums.Size() != 0 {
		t.Fatal("a menu key spent a budget")
	}
}

// # is reserved: ignored at the menu rather than read as anything.
func TestMenuIgnoresHash(t *testing.T) {
	h := bakery(t, "9995550199", nil)
	h.finishPlayback(t) // menus/front, heard to the end
	h.dial("#")
	h.dial("1")
	if ep := h.expectRing(t); ep != "PJSIP/kitchen" {
		t.Fatalf("rang %s", ep)
	}
	h.hangUpAndFinish(t)
}

// A submenu, and the choice back to the front — a cycle, and a wanted one.
func TestMenuSubmenuAndBackToTheFront(t *testing.T) {
	h := bakery(t, "9995550199", nil)
	h.chooseOver(t, "front", "4")
	h.chooseOver(t, "more", "0")
	h.chooseOver(t, "front", "4")
	h.chooseOver(t, "more", "1")
	if c := h.fake.expect(t, "SetChannelVar"); c.Args[2] != "general" {
		t.Fatalf("SetChannelVar = %v", c.Args)
	}
	h.fake.expect(t, "Continue")
	h.sess.CallerLeft()
	h.waitFinished(t)
	if rec := h.rec.only(t); rec.Menu != "more" || rec.Choice != "voicemail:general" {
		t.Fatalf("record = %+v", rec)
	}
}

// Invariant 8 with a menu in front: a mailbox leaf releases the caller into
// the dialplan, and nothing hangs that channel up afterwards.
func TestMenuVoicemailLeafDetachesAndNeverHangsUp(t *testing.T) {
	h := bakery(t, "9995550199", nil)
	h.chooseOver(t, "front", "4")
	h.chooseOver(t, "more", "1")
	h.fake.expect(t, "SetChannelVar")
	h.fake.expect(t, "Continue")
	h.sess.CallerLeft()
	h.waitFinished(t)
	for {
		select {
		case c := <-h.fake.calls:
			if c.Name == "Hangup" && c.Args[0] == h.sess.ChannelID {
				t.Fatal("the caller was hung up after the voicemail handoff")
			}
		default:
			return
		}
	}
}

// A recording that ends in a goodbye says it and hangs up.
func TestMenuRecordingThenGoodbye(t *testing.T) {
	h := bakery(t, "9995550199", nil)
	h.chooseOver(t, "front", "4")
	h.chooseOver(t, "more", "9")
	h.expectPlays(t, "recordings/directions", "good-day")
	h.fake.expect(t, "Hangup")
	h.expectCleanup(t, "Hangup")
	h.waitFinished(t)
	if rec := h.rec.only(t); rec.Reason != "recording" || rec.Outcome != calls.OutcomeDismissed {
		t.Fatalf("record = %+v", rec)
	}
}

// The no-input row of the review's table, both ways: a silent caller hears
// the menu 1 + repeat times, then gets the line's disposition — behind the
// challenge only when the line says so. Nobody is ever rung.
func TestSilentCallerOnMenuLineGetsLineDisposition(t *testing.T) {
	t.Run("challenge_no_input off", func(t *testing.T) {
		h := bakery(t, "9995550199", nil)
		h.expectPlays(t, "menus/front", "menus/front")
		if c := h.fake.expect(t, "SetChannelVar"); c.Args[2] != "bakery" {
			t.Fatalf("SetChannelVar = %v, want the house mailbox", c.Args)
		}
		h.fake.expect(t, "Continue")
		h.sess.CallerLeft()
		h.waitFinished(t)
		if rec := h.rec.only(t); rec.Reason != "no-digits" || rec.Outcome != calls.OutcomeVoicemail {
			t.Fatalf("record = %+v", rec)
		}
	})
	t.Run("challenge_no_input on", func(t *testing.T) {
		src := strings.Replace(bakeryPolicy, `on_no_input = "voicemail"`, "on_no_input = \"voicemail\"\nchallenge_no_input = true", 1)
		h := startTuned(t, src, "9995550199", nil, func(d *Deps) { d.Rand = fixedDraw(1, 4) })
		h.expectPlays(t, "menus/front", "menus/front", "challenge/explain")
		h.question(t, "3", "6")
		// Silent at the question too: dismissed, never recorded.
		h.expectPlays(t, "good-day")
		h.fake.expect(t, "Hangup")
		h.expectCleanup(t, "Hangup")
		h.waitFinished(t)
		if rec := h.rec.only(t); rec.Outcome != calls.OutcomeDismissed || rec.Challenge != calls.ChallengeNoAnswer {
			t.Fatalf("record = %+v", rec)
		}
	})
}

// The play ceiling: a held-down * costs the line six plays, then goes the
// way silence goes.
func TestMenuCeilingEndsInTheLineDisposition(t *testing.T) {
	h := bakery(t, "9995550199", nil)
	for i := 0; i < MenuMaxPlays; i++ {
		h.chooseOver(t, "front", "*")
	}
	if c := h.fake.expect(t, "SetChannelVar"); c.Args[2] != "bakery" {
		t.Fatalf("after %d plays: %v, want the line's disposition", MenuMaxPlays, c.Args)
	}
	h.fake.expect(t, "Continue")
	h.sess.CallerLeft()
	h.waitFinished(t)
	if rec := h.rec.only(t); rec.Reason != "menu-ceiling" {
		t.Fatalf("reason = %q, want menu-ceiling", rec.Reason)
	}
}

// The time ceiling: the wall clock from the first menu, whatever the plays.
func TestMenuTimeCeiling(t *testing.T) {
	h := bakery(t, "9995550199", func(d *Deps) {
		d.Cfg.FirstDigitTimeout = 2 * time.Second // longer than the ceiling
		d.Cfg.MenuMaxDuration = 60 * time.Millisecond
	})
	started := time.Now()
	h.finishPlayback(t) // menus/front; then the wait is cut to the ceiling
	h.fake.expect(t, "SetChannelVar")
	if waited := time.Since(started); waited > time.Second {
		t.Fatalf("the menu held the line %s past a 60ms ceiling", waited)
	}
	h.fake.expect(t, "Continue")
	h.sess.CallerLeft()
	h.waitFinished(t)
	if rec := h.rec.only(t); rec.Reason != "menu-ceiling" {
		t.Fatalf("reason = %q", rec.Reason)
	}
}

// Invariant 3 at the menu's waits: a hang-up mid-menu is cancellation, and
// cleanup runs once.
func TestMenuHangUpMidMenuTearsDownOnce(t *testing.T) {
	h := bakery(t, "9995550199", nil)
	h.fake.expect(t, "Play")
	h.sess.CallerGone()
	h.expectCleanup(t, "Hangup")
	h.waitFinished(t)
	h.fake.expectNone(t, 30*time.Millisecond)
	if rec := h.rec.only(t); rec.Outcome != calls.OutcomeAbandoned || rec.Menu != "front" {
		t.Fatalf("record = %+v", rec)
	}
}

// R9: an admitted caller skips the menu and the challenge and rings the
// house, exactly as they skip the PIN lobby today.
func TestAllowListedCallerSkipsTheMenu(t *testing.T) {
	h := bakery(t, "5125550100", nil)
	h.expectPlays(t, "welcome-known")
	h.expectRing(t)
	h.fake.expect(t, "Originate")
	h.hangUpAndFinish(t)
	if rec := h.rec.only(t); rec.Menu != "" {
		t.Fatalf("an admitted caller heard menu %q", rec.Menu)
	}
}

// An admitted caller who presses 3 over the welcome is choosing at the menu,
// with the challenge already satisfied.
func TestKnownCallerBargeOnMenuLineChoosesWithoutChallenge(t *testing.T) {
	h := bakery(t, "5125550100", nil)
	if c := h.fake.expect(t, "Play"); promptOf(c) != "welcome-known" {
		t.Fatalf("played %s", promptOf(c))
	}
	h.dial("3")
	h.fake.expect(t, "StopPlayback")
	if ep := h.expectRing(t); ep != "PJSIP/office" {
		t.Fatalf("rang %s, want the baker with no question asked", ep)
	}
	h.hangUpAndFinish(t)
	if rec := h.rec.only(t); rec.Challenge != "" || rec.Known != "Grandma" {
		t.Fatalf("record = %+v", rec)
	}
}

// The review's credential rule: collect submits "" on a bare #, and a
// PIN-less extension in the PIN map would sit under exactly that key.
func TestHashAloneNeverReachesAPinlessExtension(t *testing.T) {
	pol, err := policy.FromTOML([]byte(bakeryPolicy))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := pol.LookupExtension(""); ok {
		t.Fatal("the empty PIN finds an extension")
	}
	if _, ok := pol.LookupExtensionID("orders"); !ok {
		t.Fatal("the menu cannot find its extension by id")
	}

	// And through the collector itself, in case the lobby is ever reachable
	// on such a line: # alone is an invalid extension, never a ring.
	h := startTuned(t, bakeryPolicy, "9995550199", nil, nil)
	h.sess.CallerGone()
	h.waitFinished(t)
	drain(h.fake)

	s := NewSession("ch-hash", "9995550199", h.sess.deps)
	finished := make(chan struct{})
	s.deps.OnFinished = func(*Session) { close(finished) }
	go func() {
		defer s.cleanup()
		s.collect("", "")
	}()
	s.Dtmf("#")
	if c := h.fake.expect(t, "Play"); !strings.HasSuffix(c.Args[1], "/invalid-extension") {
		t.Fatalf("played %s, want invalid-extension", c.Args[1])
	}
	s.CallerGone()
	<-finished
	for {
		select {
		case c := <-h.fake.calls:
			if c.Name == "Originate" || c.Name == "Continue" {
				t.Fatalf("a bare # reached something: %s(%v)", c.Name, c.Args)
			}
		default:
			return
		}
	}
}

func drain(f *fakeARI) {
	for {
		select {
		case <-f.calls:
		default:
			return
		}
	}
}

// The menu's words come from the line's own prefix; the challenge's from the
// bundled set whatever the line's pack is.
func TestMenuPlaysItsOwnClipFromTheLinePrefix(t *testing.T) {
	src := strings.Replace(bakeryPolicy, `menu        = "front"`, "menu        = \"front\"\nprompts     = \"bluedoor\"", 1)
	h := startTuned(t, src, "9995550199", nil, func(d *Deps) { d.Rand = fixedDraw(1, 4) })
	c := h.fake.expect(t, "Play")
	if c.Args[1] != "sound:bluedoor/menus/front" {
		t.Fatalf("played %s, want the line's own menu clip", c.Args[1])
	}
	h.dial("3")
	h.fake.expect(t, "StopPlayback")
	if c := h.fake.expect(t, "Play"); c.Args[1] != "sound:call-me-maybe/challenge/explain" {
		t.Fatalf("played %s, want the bundled challenge set", c.Args[1])
	}
	h.hangUpAndFinish(t)
}
