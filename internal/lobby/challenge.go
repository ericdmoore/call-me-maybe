package lobby

import (
	"math/rand/v2"
	"strconv"
	"time"

	"callmemaybe/internal/calls"
	events "callmemaybe/internal/observation"
)

// The arithmetic challenge: "what is three times six", asked of a caller who
// reached a guarded route without a credential, before anything rings or
// records.
//
// What it is for, honestly: it stops dialers that press nothing or press keys
// at random, and very young children. It is not age verification — an
// eight-year-old can answer it — and it is no defence against an automated
// caller that understands speech. It also shuts out anybody whose phone
// cannot send tones. That is why it guards routes a line chooses, and why the
// ladder's admitted callers never meet it.
//
// It is attached to a route and never to an extension: the no-input
// disposition ([line] challenge_no_input) and a menu choice (challenge =
// true). A caller who typed a valid PIN has presented a stronger credential
// than a sum and is never asked one, which keeps evaluate — where invariants
// 5 and 6 live — exactly as it was.
//
// Its budget is its own. A wrong answer costs ChallengeLimiter, never the PIN
// Limiter, and a right one forgives nothing in either: passing a sum is not a
// credential, and if it called Limiter.Success, solving "two times three"
// would wipe a redialler's PIN failures on every call.

// challengeMediaPrefix is where the challenge clips live: the bundled pack's
// challenge/ directory, beside system/ and outside the six-name pack
// contract (docs/PACKS.md). Played from here whatever pack the line speaks
// with, because doorman never opens a sound file and so cannot fall back at
// call time from a pack that lacks them — a fixed source is the only way the
// challenge can never half-work.
const challengeMediaPrefix = BundledMediaPrefix + "/challenge"

// The challenge's own clips. Each must be a key under "challenge" in
// prompts/manifest.json, beside the ten digit clips "0" to "9"; a test holds
// the two together.
const (
	ClipExplain  = "explain"
	ClipWhatIs   = "what-is"
	ClipTimes    = "times"
	ClipRight    = "right"
	ClipTryAgain = "try-again"
)

// ChallengeClips is every clip the challenge can play, for the manifest test
// and for `doorman check` to look for on the box.
func ChallengeClips() []string {
	out := []string{ClipExplain, ClipWhatIs, ClipTimes, ClipRight, ClipTryAgain}
	for d := 0; d <= 9; d++ {
		out = append(out, strconv.Itoa(d))
	}
	return out
}

// ChallengeMediaPrefix is the Asterisk media prefix the clips are played
// from, for `doorman check`.
func ChallengeMediaPrefix() string { return challengeMediaPrefix }

func challengeURI(clip string) string { return "sound:" + challengeMediaPrefix + "/" + clip }

// maxChallengeRepeats bounds how many times * may replay one question. Enough
// for a caller who misheard; not enough for a held-down key to keep the line.
const maxChallengeRepeats = 2

// challenge asks the sum. It returns true when the caller answered correctly
// and the route may go on; false means the call has already moved on —
// dismissed, or gone — and the caller of this must simply return.
//
// No state outlives it: the attempt count and the question are locals, a
// hang-up anywhere is ctx.Done unwinding every wait, and the deferred cleanup
// in Run is still the only teardown (invariant 3).
func (s *Session) challenge() bool {
	key := s.limitKey()
	limiter := s.deps.ChallengeLimiter

	// Checked here and not at the door: a number over its challenge budget
	// still hears the lobby or the menu — "two for our hours" harms nobody —
	// and only the guarded route turns it away.
	if limiter != nil && limiter.Blocked(key, time.Now()) {
		s.rec.Challenge = calls.ChallengeRateLimited
		s.observe(events.AdmissionDecided, "challenge-rate-limited")
		s.log.Warn("caller is over the challenge budget, dismissing at the challenge")
		s.dismiss("challenge-rate-limited")
		return false
	}

	r := s.pol.Challenge()
	draw := s.deps.Rand
	if draw == nil {
		draw = rand.IntN
	}
	operand := func() int { return r.Low + draw(r.High-r.Low+1) }

	// The explanation barges: a caller who knows the drill skips straight to
	// the question. The key that skipped it is not an answer — nobody has
	// heard a question yet.
	s.playMedia(challengeURI(ClipExplain), true)
	if s.ctx.Err() != nil {
		return false
	}

	attempts := 0
	for {
		// A new question on every try, not only every call: asking the same
		// one again would let a masher walk through the answers.
		a, b := operand(), operand()
		answer := a * b

		var entered string
		var end endReason
		for repeats := 0; ; repeats++ {
			// Only the last operand barges, and its key is the first digit of
			// the answer: nothing pressed before the question has been asked
			// can count as answering it.
			s.say(false, challengeURI(ClipWhatIs), challengeURI(strconv.Itoa(a)), challengeURI(ClipTimes))
			if s.ctx.Err() != nil {
				return false
			}
			seed := s.playMedia(challengeURI(strconv.Itoa(b)), true)
			if s.ctx.Err() != nil {
				return false
			}
			// The first-digit window, not the inter-digit one, so a relay
			// operator or a slow keypad has time to start.
			entered, end = collectDigits(s.ctx, s.events, collectSpec{
				First:       s.deps.Cfg.FirstDigitTimeout,
				Inter:       s.deps.Cfg.InterDigitTimeout,
				Max:         r.MaxAnswerDigits(),
				Seed:        seed,
				StarRepeats: repeats < maxChallengeRepeats,
			})
			if end != endRepeat {
				break
			}
		}

		switch end {
		case endGone:
			return false
		case endNothing:
			// Silence is not a wrong answer: nothing was guessed, so neither
			// budget is spent, exactly as noInput leaves the PIN limiter alone.
			s.rec.Challenge = calls.ChallengeNoAnswer
			s.observe(events.AdmissionDecided, "challenge-no-answer")
			s.log.Info("nobody answered the challenge, dismissing")
			s.dismiss("no-digits")
			return false
		}

		if n, err := strconv.Atoi(entered); err == nil && entered != "" && n == answer {
			s.rec.Challenge = calls.ChallengePassed
			s.observe(events.AdmissionDecided, "challenge-passed")
			s.log.Info("challenge passed")
			s.playMedia(challengeURI(ClipRight), false)
			return s.ctx.Err() == nil
		}

		attempts++
		failures := 0
		if limiter != nil {
			failures = limiter.Failure(key, time.Now())
		}
		// The count, never what was typed.
		s.log.Info("wrong answer to the challenge", "attempt", attempts, "windowFailures", failures)

		// MAX_PIN_ATTEMPTS means the same thing here as at the keypad: retries
		// after the first try.
		if attempts > s.deps.Cfg.MaxPinAttempts {
			s.rec.Challenge = calls.ChallengeFailed
			s.observe(events.AdmissionDecided, "challenge-failed")
			s.dismiss("challenge-failed")
			return false
		}
		s.playMedia(challengeURI(ClipTryAgain), false)
		if s.ctx.Err() != nil {
			return false
		}
	}
}
