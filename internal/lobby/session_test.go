package lobby

import (
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"callmemaybe/internal/policy"
)

const testPolicy = `
[house]
handsets = ["kitchen", "office"]

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

type harness struct {
	fake     *fakeARI
	sess     *Session
	finished chan struct{}
	legs     chan string
	now      time.Time
	rec      *testRecorder
}

// start builds a session against the fake and runs it. Timeouts are real but
// short — tens of milliseconds — which keeps the whole suite under a second
// without a fake clock.
func start(t *testing.T, callerNumber string, limiter *RateLimiter) *harness {
	return startWith(t, testPolicy, callerNumber, limiter)
}

func startWith(t *testing.T, policySrc, callerNumber string, limiter *RateLimiter) *harness {
	t.Helper()
	pol, err := policy.FromTOML([]byte(policySrc))
	if err != nil {
		t.Fatalf("policy: %v", err)
	}
	if limiter == nil {
		limiter = NewRateLimiter(true, 3, time.Hour)
	}
	h := &harness{
		fake:     newFakeARI(),
		finished: make(chan struct{}),
		legs:     make(chan string, 8),
		rec:      &testRecorder{},
		// A school-night Wednesday at 14:00 — safely outside any afterhours
		// window in the fixtures.
		now: time.Date(2026, 7, 8, 14, 0, 0, 0, time.Local),
	}
	h.sess = NewSession("ch-caller-1", callerNumber, Deps{
		ARI:     h.fake,
		Policy:  func() *policy.Policy { return pol },
		Limiter: limiter,
		Prompts: NewPrompts("call-me-maybe"),
		Log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		Calls:   h.rec,
		Cfg: Config{
			DefaultCountryCode: "1",
			ExtensionLength:    6,
			FirstDigitTimeout:  80 * time.Millisecond,
			InterDigitTimeout:  40 * time.Millisecond,
			RingTimeout:        150 * time.Millisecond,
			RingCycle:          40 * time.Millisecond,
			MaxPinAttempts:     2,
		},
		Now:          func() time.Time { return h.now },
		OnLegCreated: func(legID string, _ *Session) { h.legs <- legID },
		OnFinished:   func(*Session) { close(h.finished) },
	})
	go h.sess.Run()
	return h
}

func (h *harness) finishPlayback(t *testing.T) fakeCall {
	t.Helper()
	c := h.fake.expect(t, "Play")
	h.sess.PlaybackFinished(c.Args[2])
	return c
}

func (h *harness) waitFinished(t *testing.T) {
	t.Helper()
	select {
	case <-h.finished:
	case <-time.After(2 * time.Second):
		t.Fatal("session never finished")
	}
}

func promptOf(c fakeCall) string {
	return strings.TrimPrefix(c.Args[1], "sound:call-me-maybe/")
}

// expectCleanup consumes the teardown calls (hangups, bridge destroy) that
// every session issues on exit, in whatever order they arrive.
func (h *harness) expectCleanup(t *testing.T, names ...string) {
	t.Helper()
	h.fake.expectAny(t, names...)
}

func TestKnownCallerRingsTheHouseAndBridgesFirstAnswer(t *testing.T) {
	h := start(t, "5125550100", nil)

	if c := h.finishPlayback(t); promptOf(c) != "welcome-known" {
		t.Fatalf("prompt = %s, want welcome-known", promptOf(c))
	}

	h.fake.expect(t, "CreateBridge")
	h.fake.expect(t, "AddToBridge")
	h.fake.expect(t, "Ring")

	o1 := h.fake.expect(t, "Originate")
	o2 := h.fake.expect(t, "Originate")
	if o1.Args[0] != "PJSIP/kitchen" || o2.Args[0] != "PJSIP/office" {
		t.Fatalf("originated %s, %s", o1.Args[0], o2.Args[0])
	}
	if !strings.HasPrefix(o1.Args[2], "leg,") {
		t.Fatalf("appArgs = %q, want leg,<id> correlation token", o1.Args[2])
	}
	leg1, leg2 := <-h.legs, <-h.legs

	h.sess.LegAnswered(leg1)
	h.fake.expect(t, "RingStop")
	if c := h.fake.expect(t, "AddToBridge"); c.Args[1] != leg1 {
		t.Fatalf("bridged %s, want winner %s", c.Args[1], leg1)
	}
	if c := h.fake.expect(t, "Hangup"); c.Args[0] != leg2 {
		t.Fatalf("hung up %s, want losing sibling %s", c.Args[0], leg2)
	}

	h.sess.LegGone(leg1) // far end hangs up
	h.expectCleanup(t, "DestroyBridge", "Hangup")
	h.waitFinished(t)
}

func TestUnknownCallerWithValidPinReachesThatHandset(t *testing.T) {
	h := start(t, "9995550199", nil)

	if c := h.finishPlayback(t); promptOf(c) != "lobby-greeting" {
		t.Fatalf("prompt = %s, want lobby-greeting", promptOf(c))
	}

	for _, d := range "428917" {
		h.sess.Dtmf(string(d))
	}

	h.fake.expect(t, "CreateBridge")
	h.fake.expect(t, "AddToBridge")
	h.fake.expect(t, "Ring")
	if c := h.fake.expect(t, "Originate"); c.Args[0] != "PJSIP/kitchen" {
		t.Fatalf("originated %s, want only PJSIP/kitchen", c.Args[0])
	}
	leg := <-h.legs

	h.sess.LegAnswered(leg)
	h.fake.expect(t, "RingStop")
	h.fake.expect(t, "AddToBridge")

	h.sess.LegGone(leg)
	h.expectCleanup(t, "DestroyBridge", "Hangup")
	h.waitFinished(t)
}

func TestSilentCallerIsDismissedAfterFirstDigitTimeout(t *testing.T) {
	h := start(t, "9995550199", nil)
	h.finishPlayback(t)

	// Say nothing; the 80ms first-digit window elapses.
	if c := h.finishPlayback(t); promptOf(c) != "good-day" {
		t.Fatalf("prompt = %s, want good-day", promptOf(c))
	}
	h.expectCleanup(t, "Hangup", "Hangup") // dismiss + cleanup, both idempotent
	h.waitFinished(t)
}

func TestWrongPinRetriesThenDismisses(t *testing.T) {
	h := start(t, "9995550199", nil)
	h.finishPlayback(t)

	dial := func(pin string) {
		for _, d := range pin {
			h.sess.Dtmf(string(d))
		}
	}

	dial("000000") // attempt 1: wrong
	if c := h.finishPlayback(t); promptOf(c) != "invalid-extension" {
		t.Fatalf("prompt = %s, want invalid-extension", promptOf(c))
	}
	dial("111111") // attempt 2: wrong
	if c := h.finishPlayback(t); promptOf(c) != "invalid-extension" {
		t.Fatalf("prompt = %s, want invalid-extension", promptOf(c))
	}
	dial("222222") // attempt 3: exceeds MaxPinAttempts=2
	if c := h.finishPlayback(t); promptOf(c) != "good-day" {
		t.Fatalf("prompt = %s, want good-day", promptOf(c))
	}
	h.expectCleanup(t, "Hangup", "Hangup")
	h.waitFinished(t)
}

func TestBargeInDuringGreetingSeedsCollection(t *testing.T) {
	h := start(t, "9995550199", nil)

	// Greeting starts; caller dials before it finishes.
	c := h.fake.expect(t, "Play")
	if promptOf(c) != "lobby-greeting" {
		t.Fatalf("prompt = %s", promptOf(c))
	}
	h.sess.Dtmf("4")
	if stop := h.fake.expect(t, "StopPlayback"); stop.Args[0] != c.Args[2] {
		t.Fatalf("stopped %s, want %s", stop.Args[0], c.Args[2])
	}
	for _, d := range "28917" { // remaining five digits
		h.sess.Dtmf(string(d))
	}

	h.fake.expect(t, "CreateBridge")
	h.fake.expect(t, "AddToBridge")
	h.fake.expect(t, "Ring")
	if o := h.fake.expect(t, "Originate"); o.Args[0] != "PJSIP/kitchen" {
		t.Fatalf("originated %s", o.Args[0])
	}
	leg := <-h.legs
	h.sess.LegAnswered(leg)
	h.fake.expect(t, "RingStop")
	h.fake.expect(t, "AddToBridge")
	h.sess.LegGone(leg)
	h.expectCleanup(t, "DestroyBridge", "Hangup")
	h.waitFinished(t)
}

func TestNobodyAnsweringDismissesWithNoAnswer(t *testing.T) {
	h := start(t, "5125550100", nil)
	h.finishPlayback(t) // welcome-known

	h.fake.expect(t, "CreateBridge")
	h.fake.expect(t, "AddToBridge")
	h.fake.expect(t, "Ring")
	h.fake.expect(t, "Originate")
	h.fake.expect(t, "Originate")
	<-h.legs
	<-h.legs

	// Nobody answers; the 150ms ring timeout fires: both legs hung up, ring
	// indication stopped, then the no-answer prompt.
	h.fake.expectAny(t, "Hangup", "Hangup", "RingStop")
	if c := h.finishPlayback(t); promptOf(c) != "no-answer" {
		t.Fatalf("prompt = %s, want no-answer", promptOf(c))
	}
	h.expectCleanup(t, "Hangup", "DestroyBridge", "Hangup")
	h.waitFinished(t)
}

func TestCallerHangupMidGreetingTearsEverythingDown(t *testing.T) {
	h := start(t, "5125550100", nil)
	h.fake.expect(t, "Play") // welcome-known playing; do not finish it

	h.sess.CallerGone()
	h.expectCleanup(t, "Hangup") // cleanup's idempotent caller hangup
	h.waitFinished(t)
}

func TestRateLimitedCallerSkipsTheGreetingEntirely(t *testing.T) {
	limiter := NewRateLimiter(true, 3, time.Hour)
	now := time.Now()
	for range 3 {
		limiter.Failure("+19995550199", now)
	}

	h := start(t, "9995550199", limiter)

	// Straight to good-day: no lobby greeting, no dial window.
	if c := h.finishPlayback(t); promptOf(c) != "good-day" {
		t.Fatalf("prompt = %s, want good-day with no greeting first", promptOf(c))
	}
	h.expectCleanup(t, "Hangup", "Hangup")
	h.waitFinished(t)
}

func TestLateAnswerAfterWinnerIsHungUp(t *testing.T) {
	h := start(t, "5125550100", nil)
	h.finishPlayback(t)
	h.fake.expect(t, "CreateBridge")
	h.fake.expect(t, "AddToBridge")
	h.fake.expect(t, "Ring")
	h.fake.expect(t, "Originate")
	h.fake.expect(t, "Originate")
	leg1, leg2 := <-h.legs, <-h.legs

	h.sess.LegAnswered(leg1)
	h.fake.expect(t, "RingStop")
	h.fake.expect(t, "AddToBridge")
	h.fake.expect(t, "Hangup") // sibling cancel for leg2

	// leg2 answers anyway — the hangup raced its pickup. It must be hung up
	// again, not bridged into the call.
	h.sess.LegAnswered(leg2)
	if c := h.fake.expect(t, "Hangup"); c.Args[0] != leg2 {
		t.Fatalf("hung up %s, want the late answerer %s", c.Args[0], leg2)
	}

	h.sess.LegGone(leg1)
	h.expectCleanup(t, "DestroyBridge", "Hangup")
	h.waitFinished(t)
}

func TestHandsetTransferDoesNotKillTheTransferredCall(t *testing.T) {
	h := start(t, "5125550100", nil)
	h.finishPlayback(t) // welcome-known

	h.fake.expect(t, "CreateBridge")
	h.fake.expect(t, "AddToBridge")
	h.fake.expect(t, "Ring")
	h.fake.expect(t, "Originate")
	h.fake.expect(t, "Originate")
	leg1, leg2 := <-h.legs, <-h.legs

	h.sess.LegAnswered(leg1)
	h.fake.expect(t, "RingStop")
	h.fake.expect(t, "AddToBridge")
	if c := h.fake.expect(t, "Hangup"); c.Args[0] != leg2 {
		t.Fatalf("hung up %s, want sibling %s", c.Args[0], leg2)
	}

	// The kitchen phone blind-transfers the caller to another extension:
	// Asterisk moves the caller's channel out of our Stasis app while it is
	// very much alive (StasisEnd, not ChannelDestroyed), then the transferrer
	// phone drops.
	h.sess.CallerLeft()
	h.sess.LegGone(leg1)
	h.waitFinished(t)

	// Cleanup ran. It may hang up leg1 (dead anyway, 404 in real life) and
	// must destroy the bridge — but it must NEVER hang up the caller channel,
	// which now carries the transferred call.
	for {
		select {
		case c := <-h.fake.calls:
			if c.Name == "Hangup" && c.Args[0] == h.sess.ChannelID {
				t.Fatalf("cleanup hung up the transferred caller %s — this kills the transfer", h.sess.ChannelID)
			}
		default:
			return
		}
	}
}

func TestNormalHangupStillCleansUpTheCallerChannel(t *testing.T) {
	// The inverse guard: a caller who is genuinely destroyed (not transferred)
	// still gets the idempotent cleanup hangup, so nothing dangles.
	h := start(t, "5125550100", nil)
	h.fake.expect(t, "Play")

	h.sess.CallerGone() // ChannelDestroyed path
	h.waitFinished(t)

	sawCallerHangup := false
	for {
		select {
		case c := <-h.fake.calls:
			if c.Name == "Hangup" && c.Args[0] == h.sess.ChannelID {
				sawCallerHangup = true
			}
		default:
			if !sawCallerHangup {
				t.Fatal("cleanup skipped the caller hangup on a real hangup")
			}
			return
		}
	}
}

const kidsLadderPolicy = `
[house]
handsets = ["adults"]
voicemail = "family"

[[schedules]]
id = "school-night"
start = "20:30"
end = "07:00"
days = ["SU", "MO", "TU", "WE", "TH"]

[[handsets]]
id = "kids-room"
endpoint = "PJSIP/kids-room"

[[handsets]]
id = "kitchen"
endpoint = "PJSIP/kitchen"

[[handsets]]
id = "primary-bed"
endpoint = "PJSIP/primary-bed"

[[groups]]
id = "adults"
handsets = ["kitchen", "primary-bed"]

[[extensions]]
pin = "555001"
label = "Kids"
voicemail = "kids"
afterhours = "school-night"

  [[extensions.steps]]
  handsets = ["kids-room"]
  rings = 2

  [[extensions.steps]]
  handsets = ["adults"]
  rings = 3

`

func dialPin(h *harness, pin string) {
	for _, d := range pin {
		h.sess.Dtmf(string(d))
	}
}

func TestLadderEscalatesKidsToAdultsToVoicemail(t *testing.T) {
	h := startWith(t, kidsLadderPolicy, "9995550199", nil)
	h.finishPlayback(t) // lobby greeting
	dialPin(h, "555001")

	h.fake.expect(t, "CreateBridge")
	h.fake.expect(t, "AddToBridge")
	h.fake.expect(t, "Ring")

	// Stage 1: the kids' room, alone, for 2 ring cycles.
	if c := h.fake.expect(t, "Originate"); c.Args[0] != "PJSIP/kids-room" {
		t.Fatalf("stage 1 originated %s, want PJSIP/kids-room", c.Args[0])
	}
	kidsLeg := <-h.legs

	// Nobody answers; the stage times out and its leg is torn down.
	if c := h.fake.expect(t, "Hangup"); c.Args[0] != kidsLeg {
		t.Fatalf("hung up %s, want the kids leg %s", c.Args[0], kidsLeg)
	}

	// Stage 2: the adults group.
	o1 := h.fake.expect(t, "Originate")
	o2 := h.fake.expect(t, "Originate")
	got := map[string]bool{o1.Args[0]: true, o2.Args[0]: true}
	if !got["PJSIP/kitchen"] || !got["PJSIP/primary-bed"] {
		t.Fatalf("stage 2 originated %v, want the adults group", got)
	}
	adult1, adult2 := <-h.legs, <-h.legs

	// Still nobody. Ladder exhausted → voicemail handoff: legs and bridge
	// torn down, MAILBOX set, caller released to the dialplan — and never
	// hung up by us.
	h.fake.expectAny(t, "Hangup", "Hangup", "RingStop", "DestroyBridge")
	if c := h.fake.expect(t, "SetChannelVar"); c.Args[1] != "MAILBOX" || c.Args[2] != "kids" {
		t.Fatalf("SetChannelVar = %v, want MAILBOX=kids", c.Args)
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
				t.Fatal("caller hung up after voicemail handoff — cuts them off mid-greeting")
			}
			_ = adult1
			_ = adult2
		default:
			return
		}
	}
}

func TestLadderAnswerAtSecondStageBridges(t *testing.T) {
	h := startWith(t, kidsLadderPolicy, "9995550199", nil)
	h.finishPlayback(t)
	dialPin(h, "555001")

	h.fake.expect(t, "CreateBridge")
	h.fake.expect(t, "AddToBridge")
	h.fake.expect(t, "Ring")
	h.fake.expect(t, "Originate") // kids-room
	<-h.legs
	h.fake.expect(t, "Hangup") // stage 1 times out

	h.fake.expect(t, "Originate")
	h.fake.expect(t, "Originate")
	adult1, adult2 := <-h.legs, <-h.legs

	h.sess.LegAnswered(adult2)
	h.fake.expect(t, "RingStop")
	if c := h.fake.expect(t, "AddToBridge"); c.Args[1] != adult2 {
		t.Fatalf("bridged %s, want %s", c.Args[1], adult2)
	}
	if c := h.fake.expect(t, "Hangup"); c.Args[0] != adult1 {
		t.Fatalf("hung up %s, want sibling %s", c.Args[0], adult1)
	}

	h.sess.LegGone(adult2)
	h.expectCleanup(t, "DestroyBridge", "Hangup")
	h.waitFinished(t)
}

func TestAfterhoursGoesStraightToVoicemailWithoutRinging(t *testing.T) {
	h := startWith(t, kidsLadderPolicy, "9995550199", nil)
	// 21:30 on a Tuesday: inside the school-night window.
	h.now = time.Date(2026, 7, 7, 21, 30, 0, 0, time.Local)

	h.finishPlayback(t) // greeting still plays; the PIN is still required
	dialPin(h, "555001")

	// No bridge, no ring, no originate — the very next thing is the
	// voicemail handoff.
	if c := h.fake.expect(t, "SetChannelVar"); c.Args[2] != "kids" {
		t.Fatalf("mailbox = %s, want kids", c.Args[2])
	}
	h.fake.expect(t, "Continue")

	h.sess.CallerLeft()
	h.waitFinished(t)
}

func TestHouseNoAnswerFallsToFamilyVoicemail(t *testing.T) {
	src := strings.Replace(kidsLadderPolicy, `[[people]]`, ``, 1) // no-op; fixture has none
	src += "\n[[people]]\nname = \"Grandma\"\nnumbers = [\"512-555-0100\"]\n"
	h := startWith(t, src, "5125550100", nil)
	h.finishPlayback(t) // welcome-known

	h.fake.expect(t, "CreateBridge")
	h.fake.expect(t, "AddToBridge")
	h.fake.expect(t, "Ring")
	h.fake.expect(t, "Originate")
	h.fake.expect(t, "Originate")
	<-h.legs
	<-h.legs

	// House uses the default RingTimeout (150ms in tests); nobody answers.
	h.fake.expectAny(t, "Hangup", "Hangup", "RingStop", "DestroyBridge")
	if c := h.fake.expect(t, "SetChannelVar"); c.Args[2] != "family" {
		t.Fatalf("mailbox = %s, want family", c.Args[2])
	}
	h.fake.expect(t, "Continue")
	h.sess.CallerLeft()
	h.waitFinished(t)
}

// With afterhours_ring the window rings somewhere awake instead of going
// straight to voicemail — homework hours sending the kids' line to the adults.
// Proving it at the policy layer is not enough; the state machine has to
// actually originate.
func TestAfterhoursRingRedirectsInsteadOfVoicemail(t *testing.T) {
	src := strings.Replace(kidsLadderPolicy,
		`afterhours = "school-night"`,
		"afterhours = \"school-night\"\nafterhours_ring = [\"adults\"]", 1)

	h := startWith(t, src, "9995550199", nil)
	// 21:30 on a Tuesday: inside the school-night window.
	h.now = time.Date(2026, 7, 7, 21, 30, 0, 0, time.Local)

	h.finishPlayback(t)
	dialPin(h, "555001")

	// Instead of the voicemail handoff, the adults' handsets ring.
	h.fake.expect(t, "CreateBridge")
	h.fake.expect(t, "AddToBridge")
	h.fake.expect(t, "Ring")
	h.fake.expect(t, "Originate")
	h.fake.expect(t, "Originate")
	adult := <-h.legs
	<-h.legs

	h.sess.LegAnswered(adult)
	h.fake.expectAny(t, "RingStop", "AddToBridge", "Hangup")

	h.sess.CallerGone()
	h.waitFinished(t)
}

// And the fallback still works: if the redirect goes unanswered the caller
// lands in voicemail rather than being dropped.
func TestAfterhoursRingFallsBackToVoicemail(t *testing.T) {
	src := strings.Replace(kidsLadderPolicy,
		`afterhours = "school-night"`,
		"afterhours = \"school-night\"\nafterhours_ring = [\"adults\"]", 1)

	h := startWith(t, src, "9995550199", nil)
	h.now = time.Date(2026, 7, 7, 21, 30, 0, 0, time.Local)

	h.finishPlayback(t)
	dialPin(h, "555001")

	h.fake.expect(t, "CreateBridge")
	h.fake.expect(t, "AddToBridge")
	h.fake.expect(t, "Ring")
	h.fake.expect(t, "Originate")
	h.fake.expect(t, "Originate")
	<-h.legs
	<-h.legs

	// Nobody answers before the stage times out.
	h.fake.expectAny(t, "Hangup", "Hangup", "RingStop", "DestroyBridge")
	if c := h.fake.expect(t, "SetChannelVar"); c.Args[2] != "kids" {
		t.Fatalf("mailbox = %s, want kids", c.Args[2])
	}
	h.fake.expect(t, "Continue")

	h.sess.CallerLeft()
	h.waitFinished(t)
}
