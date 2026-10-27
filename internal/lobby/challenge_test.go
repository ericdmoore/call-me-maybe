package lobby

import (
	"encoding/json"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"callmemaybe/internal/calls"
)

// The arithmetic challenge on the no-input route (#150 M1): what a silent
// caller on a guarded business line meets before the mailbox records.

// guardedPolicy is the concierge with challenge_no_input on: the business
// line whose silent callers used to fill the mailbox unasked.
var guardedPolicy = strings.Replace(conciergePolicy,
	`on_no_input = "voicemail"`, "on_no_input = \"voicemail\"\nchallenge_no_input = true", 1)

// fixedDraw returns the given draws in order, then repeats the last — so a
// test chooses its questions. Operands are Low + draw, and the default range
// starts at 2: a draw of 1 is "3".
func fixedDraw(values ...int) func(int) int {
	var mu sync.Mutex
	i := 0
	return func(int) int {
		mu.Lock()
		defer mu.Unlock()
		v := values[min(i, len(values)-1)]
		i++
		return v
	}
}

// challengeHarness starts a call with both limiters visible and the question
// fixed.
func challengeHarness(t *testing.T, policySrc, caller string, pins, sums *RateLimiter, draws ...int) *harness {
	t.Helper()
	if pins == nil {
		pins = NewRateLimiter(true, 3, time.Hour)
	}
	if sums == nil {
		sums = NewRateLimiter(true, 3, time.Hour)
	}
	return startTuned(t, policySrc, caller, pins, func(d *Deps) {
		d.ChallengeLimiter = sums
		d.Rand = fixedDraw(draws...)
	})
}

// expectPlays consumes one Play per wanted clip, in order, finishing each.
func (h *harness) expectPlays(t *testing.T, want ...string) {
	t.Helper()
	for _, w := range want {
		if c := h.finishPlayback(t); promptOf(c) != w {
			t.Fatalf("played %s, want %s", promptOf(c), w)
		}
	}
}

// question plays "what is A times B" to the end and returns.
func (h *harness) question(t *testing.T, a, b string) {
	t.Helper()
	h.expectPlays(t, "challenge/what-is", "challenge/"+a, "challenge/times", "challenge/"+b)
}

func (h *harness) dial(keys string) {
	for _, k := range keys {
		h.sess.Dtmf(string(k))
	}
}

func TestChallengeClipsAreInTheManifest(t *testing.T) {
	b, err := os.ReadFile("../../prompts/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Prompts   map[string]string `json:"prompts"`
		Challenge map[string]string `json:"challenge"`
	}
	if err := json.Unmarshal(b, &manifest); err != nil {
		t.Fatal(err)
	}
	var have []string
	for name, text := range manifest.Challenge {
		if strings.TrimSpace(text) == "" {
			t.Errorf("challenge clip %q has no text", name)
		}
		have = append(have, name)
	}
	want := ChallengeClips()
	sort.Strings(have)
	sort.Strings(want)
	if strings.Join(have, ",") != strings.Join(want, ",") {
		t.Fatalf("manifest challenge clips %v, the lobby plays %v", have, want)
	}
	// The six-name contract is untouched: the challenge is a set of its own.
	if len(manifest.Prompts) != 6 {
		t.Fatalf("manifest has %d pack prompts, want the six", len(manifest.Prompts))
	}
}

// The M1 done-when: a silent caller on a guarded line answers, and only then
// reaches the mailbox — the same handoff, detached, never hung up.
func TestChallengedNoInputGuardsTheMailbox(t *testing.T) {
	h := challengeHarness(t, guardedPolicy, "9995550199", nil, nil, 1, 4) // 3 times 6
	h.expectPlays(t, "lobby-greeting")
	// The dial window elapses; the explanation, then the question.
	h.expectPlays(t, "challenge/explain")
	h.question(t, "3", "6")
	h.dial("18")
	h.expectPlays(t, "challenge/right")
	if c := h.fake.expect(t, "SetChannelVar"); c.Args[2] != "family" {
		t.Fatalf("SetChannelVar = %v, want the house mailbox", c.Args)
	}
	h.fake.expect(t, "Continue")
	h.sess.CallerLeft()
	h.waitFinished(t)
	h.fake.expectNone(t, 30*time.Millisecond)

	rec := h.rec.only(t)
	if rec.Challenge != calls.ChallengePassed || rec.Outcome != calls.OutcomeVoicemail || rec.Reason != "no-digits" {
		t.Fatalf("record = %+v, want a passed challenge then the mailbox", rec)
	}
}

// Both settings of challenge_no_input, as the review's table has them. Off is
// today: the silent caller records a message with no question asked.
func TestSilentCallerGetsTheLineDispositionWithOrWithoutTheChallenge(t *testing.T) {
	t.Run("off", func(t *testing.T) {
		h := challengeHarness(t, conciergePolicy, "9995550199", nil, nil, 1)
		h.expectPlays(t, "lobby-greeting")
		if c := h.fake.expect(t, "SetChannelVar"); c.Args[2] != "family" {
			t.Fatalf("SetChannelVar = %v", c.Args)
		}
		h.fake.expect(t, "Continue")
		h.sess.CallerLeft()
		h.waitFinished(t)
		if rec := h.rec.only(t); rec.Challenge != "" {
			t.Fatalf("challenge = %q with challenge_no_input off", rec.Challenge)
		}
	})
	t.Run("on, and silent at the question too", func(t *testing.T) {
		pins, sums := NewRateLimiter(true, 3, time.Hour), NewRateLimiter(true, 3, time.Hour)
		h := challengeHarness(t, guardedPolicy, "9995550199", pins, sums, 1)
		h.expectPlays(t, "lobby-greeting", "challenge/explain")
		h.question(t, "3", "3")
		// Nothing pressed: never recorded, never rung.
		h.expectPlays(t, "good-day")
		h.fake.expect(t, "Hangup")
		h.expectCleanup(t, "Hangup")
		h.waitFinished(t)
		rec := h.rec.only(t)
		if rec.Outcome != calls.OutcomeDismissed || rec.Reason != "no-digits" || rec.Challenge != calls.ChallengeNoAnswer {
			t.Fatalf("record = %+v, want dismissed, no-digits, no-answer", rec)
		}
		// Silence is not a guess, at the challenge any more than at the keypad.
		if pins.Size() != 0 || sums.Size() != 0 {
			t.Fatalf("silence spent a budget: pins %d, sums %d", pins.Size(), sums.Size())
		}
	})
}

func TestWrongAnswersAreAskedAgainThenDismissed(t *testing.T) {
	pins, sums := NewRateLimiter(true, 9, time.Hour), NewRateLimiter(true, 9, time.Hour)
	// 3×6, then 4×7, then 5×8: a new question on every try.
	h := challengeHarness(t, guardedPolicy, "9995550199", pins, sums, 1, 4, 2, 5, 3, 6)
	h.expectPlays(t, "lobby-greeting", "challenge/explain")

	h.question(t, "3", "6")
	h.dial("17")
	h.expectPlays(t, "challenge/try-again")
	h.question(t, "4", "7")
	h.dial("1#")
	h.expectPlays(t, "challenge/try-again")
	h.question(t, "5", "8")
	h.dial("#") // a bare # is no answer at all, and counts as a wrong one

	// MAX_PIN_ATTEMPTS = 2 retries: the third wrong answer is the end.
	h.expectPlays(t, "good-day")
	h.fake.expect(t, "Hangup")
	h.expectCleanup(t, "Hangup")
	h.waitFinished(t)

	rec := h.rec.only(t)
	if rec.Outcome != calls.OutcomeDismissed || rec.Reason != "challenge-failed" || rec.Challenge != calls.ChallengeFailed {
		t.Fatalf("record = %+v, want dismissed for a failed challenge", rec)
	}
	if rec.PIN != "" || rec.Attempts != 0 {
		t.Fatalf("a sum was recorded as a PIN attempt: %+v", rec)
	}
	// Three wrong sums on the challenge budget, nothing on the PIN budget.
	if got := sums.Failure("+19995550199", time.Now()); got != 4 {
		t.Fatalf("challenge budget holds %d failures after one more, want 3 + 1", got-1)
	}
	if pins.Size() != 0 {
		t.Fatal("a wrong sum spent the PIN budget")
	}
}

// R3's firm line: passing a sum is not a credential, so it must never call
// Limiter.Success — which would wipe a redialler's PIN failures every call.
func TestCorrectChallengeDoesNotForgivePinFailures(t *testing.T) {
	pins := NewRateLimiter(true, 3, time.Hour)
	caller := "+19995550199"
	pins.Failure(caller, time.Now())
	pins.Failure(caller, time.Now())

	h := challengeHarness(t, guardedPolicy, "9995550199", pins, nil, 0, 0) // 2 times 2
	h.expectPlays(t, "lobby-greeting", "challenge/explain")
	h.question(t, "2", "2")
	h.dial("4")
	h.expectPlays(t, "challenge/right")
	h.fake.expect(t, "SetChannelVar")
	h.fake.expect(t, "Continue")
	h.sess.CallerLeft()
	h.waitFinished(t)

	pins.Failure(caller, time.Now())
	if !pins.Blocked(caller, time.Now()) {
		t.Fatal("the PIN failures from before the challenge were forgiven by a right answer")
	}
}

// The budget is checked at the challenge, not at the door: a number over it
// still hears the line, and only the guarded route turns it away.
func TestChallengeBudgetExhaustedDismissesAtTheChallengeNotAtTheDoor(t *testing.T) {
	sums := NewRateLimiter(true, 1, time.Hour)
	sums.Failure("+19995550199", time.Now())
	h := challengeHarness(t, guardedPolicy, "9995550199", nil, sums, 1)
	h.expectPlays(t, "lobby-greeting") // the door opened
	h.expectPlays(t, "good-day")       // no explanation, no question
	h.fake.expect(t, "Hangup")
	h.expectCleanup(t, "Hangup")
	h.waitFinished(t)
	rec := h.rec.only(t)
	if rec.Reason != "challenge-rate-limited" || rec.Challenge != calls.ChallengeRateLimited {
		t.Fatalf("record = %+v", rec)
	}
}

// Invariant 3 at the new waits: a hang-up mid-question is the cancelled
// context and nothing else. Cleanup runs once, nothing rings, nothing is
// spent, and the record is the zero outcome.
func TestCallerHangsUpMidChallengeTearsDownAndSpendsNothing(t *testing.T) {
	pins, sums := NewRateLimiter(true, 3, time.Hour), NewRateLimiter(true, 3, time.Hour)
	h := challengeHarness(t, guardedPolicy, "9995550199", pins, sums, 1, 4)
	h.expectPlays(t, "lobby-greeting", "challenge/explain", "challenge/what-is")
	if c := h.fake.expect(t, "Play"); promptOf(c) != "challenge/3" {
		t.Fatalf("played %s, want the first operand", promptOf(c))
	}
	h.sess.CallerGone() // mid-operand
	h.expectCleanup(t, "Hangup")
	h.waitFinished(t)
	h.fake.expectNone(t, 30*time.Millisecond)

	rec := h.rec.only(t)
	if rec.Outcome != calls.OutcomeAbandoned || rec.Challenge != "" {
		t.Fatalf("record = %+v, want abandoned with no verdict", rec)
	}
	if pins.Size() != 0 || sums.Size() != 0 {
		t.Fatal("a hang-up spent a budget")
	}
}

// Barge-in, clip by clip (R7): the explanation barges and its key is not an
// answer; the frame does not; the last operand does, and its key is the
// answer's first digit.
func TestChallengeBargeInIsPerClip(t *testing.T) {
	h := challengeHarness(t, guardedPolicy, "9995550199", nil, nil, 1, 4) // 3 times 6
	h.expectPlays(t, "lobby-greeting")

	// A caller who knows the drill skips the explanation with any key.
	c := h.fake.expect(t, "Play")
	if promptOf(c) != "challenge/explain" {
		t.Fatalf("played %s", promptOf(c))
	}
	h.dial("9")
	h.fake.expect(t, "StopPlayback")

	// The 9 was not an answer; a key over "what is" is dropped.
	c = h.fake.expect(t, "Play")
	if promptOf(c) != "challenge/what-is" {
		t.Fatalf("played %s, want the question straight after a skipped explanation", promptOf(c))
	}
	h.dial("5")
	h.sess.PlaybackFinished(c.Args[2])
	h.expectPlays(t, "challenge/3", "challenge/times")

	// The first digit of the answer, pressed over the last operand.
	c = h.fake.expect(t, "Play")
	if promptOf(c) != "challenge/6" {
		t.Fatalf("played %s", promptOf(c))
	}
	h.dial("1")
	h.fake.expect(t, "StopPlayback")
	h.dial("8")
	h.expectPlays(t, "challenge/right")
	h.fake.expect(t, "SetChannelVar")
	h.fake.expect(t, "Continue")
	h.sess.CallerLeft()
	h.waitFinished(t)
	if rec := h.rec.only(t); rec.Challenge != calls.ChallengePassed {
		t.Fatalf("challenge = %q, want passed: the dropped 5 and the skip key must not count", rec.Challenge)
	}
}

// * replays the same question — hearing it again is not a try — and only a
// bounded number of times.
func TestStarRepeatsTheQuestion(t *testing.T) {
	h := challengeHarness(t, guardedPolicy, "9995550199", nil, nil, 1, 4)
	h.expectPlays(t, "lobby-greeting", "challenge/explain")
	h.question(t, "3", "6")
	h.dial("*")
	h.question(t, "3", "6")
	h.dial("18")
	h.expectPlays(t, "challenge/right")
	h.fake.expect(t, "SetChannelVar")
	h.fake.expect(t, "Continue")
	h.sess.CallerLeft()
	h.waitFinished(t)
}

// R1: the challenge is on the route, never the PIN path. A caller who dialled
// a valid PIN on a guarded line rings with no question asked.
func TestPinPathIsNeverChallenged(t *testing.T) {
	h := challengeHarness(t, guardedPolicy, "9995550199", nil, nil, 1)
	h.fake.expect(t, "Play") // lobby greeting, barged
	h.dial("428917")
	h.fake.expect(t, "StopPlayback")
	h.fake.expect(t, "CreateBridge")
	h.fake.expect(t, "AddToBridge")
	h.fake.expect(t, "Ring")
	if c := h.fake.expect(t, "Originate"); c.Args[0] != "PJSIP/kitchen" {
		t.Fatalf("originated %s", c.Args[0])
	}
	leg := <-h.legs
	h.sess.LegAnswered(leg)
	h.fake.expect(t, "RingStop")
	h.fake.expect(t, "AddToBridge")
	h.sess.LegGone(leg)
	h.expectCleanup(t, "DestroyBridge", "Hangup")
	h.waitFinished(t)
	if rec := h.rec.only(t); rec.Challenge != "" {
		t.Fatalf("a PIN holder was asked a sum: %+v", rec)
	}
}

// An allow-listed caller never meets the challenge, even one who dials
// nothing on a guarded line: admission happened at the door.
func TestAllowListedCallerSkipsTheChallenge(t *testing.T) {
	h := challengeHarness(t, guardedPolicy, "5125550100", nil, nil, 1)
	h.expectPlays(t, "welcome-known")
	h.fake.expect(t, "CreateBridge")
	h.fake.expect(t, "AddToBridge")
	h.fake.expect(t, "Ring")
	h.fake.expect(t, "Originate")
	h.fake.expect(t, "Originate")
	h.sess.CallerGone()
	h.waitFinished(t)
}
