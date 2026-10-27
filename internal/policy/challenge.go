package policy

import (
	"fmt"
	"strconv"
)

// ── the [challenge] section ──────────────────────────────────────────────

// Challenge is the raw [challenge] section: how the spoken arithmetic
// question a public line can ask is built. Every key is optional, and a file
// with no [challenge] section asks its sums from the defaults.
//
// The challenge itself is attached to a route, never to an extension: the
// no-input disposition ([line] challenge_no_input) and a menu choice
// ([[menus.choices]] challenge). A caller who dialled a PIN has presented a
// credential and is never asked one, which is why nothing here can name an
// extension.
type Challenge struct {
	// Factors is the inclusive range the two operands of "what is A times B"
	// are drawn from, written [low, high]. Absent is [2, 9].
	Factors []int `toml:"factors"`
}

func (c Challenge) empty() bool { return len(c.Factors) == 0 }

// The defaults and the bound. Zero and one are left out of the default range
// because "what is one times seven" sounds like a trick question. Nine is the
// ceiling because the operands are spoken from the bundled challenge clips,
// which are the digits zero to nine in one voice: a wider range would need
// more clips, or Asterisk's own number reader in a different voice halfway
// through a sentence.
const (
	DefaultChallengeLow  = 2
	DefaultChallengeHigh = 9
	MaxChallengeFactor   = 9
)

// ChallengeRange is the compiled [challenge] section.
type ChallengeRange struct {
	Low, High int
}

// MaxAnswerDigits is the length of the longest answer the range can produce,
// so the collector can finish on the last digit of "81" without waiting for #
// or a pause. It gives away nothing a caller who heard the question does not
// already know.
func (r ChallengeRange) MaxAnswerDigits() int {
	return len(strconv.Itoa(r.High * r.High))
}

// Describe is the range as `doorman check` prints it.
func (r ChallengeRange) Describe() string {
	return fmt.Sprintf("factors %d–%d", r.Low, r.High)
}

func compileChallenge(c Challenge, fail func(string, ...any)) ChallengeRange {
	out := ChallengeRange{Low: DefaultChallengeLow, High: DefaultChallengeHigh}
	if c.empty() {
		return out
	}
	if len(c.Factors) != 2 {
		fail("[challenge] factors must be two numbers, [low, high] — the inclusive range the operands are drawn from")
		return out
	}
	low, high := c.Factors[0], c.Factors[1]
	switch {
	case low < 0 || high > MaxChallengeFactor:
		fail("[challenge] factors [%d, %d] must lie within 0 to %d — the operands are spoken "+
			"from the bundled digit clips, and there is one clip per digit", low, high, MaxChallengeFactor)
	case low > high:
		fail("[challenge] factors [%d, %d] has its low end above its high end", low, high)
	default:
		out = ChallengeRange{Low: low, High: high}
	}
	return out
}
