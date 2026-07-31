package policy

import (
	"fmt"
	"strings"
)

// WeakPIN reports whether a PIN is one an attacker would try first, and why.
//
// The point is to let people *choose* their own extension. A kid who picks
// their own number remembers it and gives it out; a random six digits gets
// written on a scrap of paper and lost, and an extension nobody can recite is
// an extension nobody uses. Memorable is a feature.
//
// What has to be refused is a much narrower set than "anything a human chose".
// The rate limiter caps a caller at a few failures an hour, which makes
// searching a million combinations hopeless — but it does nothing whatsoever
// against 123456, because that does not require searching. It requires one
// guess. So the rule is not "do not choose", it is "not the ones everybody
// tries first, and not the near-misses of those".
//
// The near-miss rules matter because a real guessing list is not just the
// patterns — it is the patterns plus one typo. 111112 and 123457 are the second
// and third things anyone tries.
//
// Together these rules refuse about a third of one percent of the six-digit
// space; TestRefusalRateIsSmall measures it exhaustively so the number cannot
// drift. Anything that would reject a large slice of the memorable PINs to
// catch a guess is deliberately left out — see notWorthIt below.
func WeakPIN(pin string) (reason string, weak bool) {
	if len(pin) < 2 {
		return "", false // length is a separate rule
	}

	if r, ok := commonPINs[pin]; ok {
		return r, true
	}

	// Exact patterns first, so the message names the simplest true thing.
	if allSame(pin) {
		return fmt.Sprintf("every digit is %c", pin[0]), true
	}
	if step, ok := run(pin); ok {
		return runReason(step, false), true
	}
	if block, ok := repeatedBlock(pin); ok {
		return fmt.Sprintf("it is %q repeated", block), true
	}
	if isPalindrome(pin) {
		return "it reads the same backwards", true
	}

	// Near misses: the pattern with one digit changed. This is where most of a
	// real guessing list lives after the patterns themselves.
	//
	// Only for six digits and up. A four-digit PIN has three steps in it, so
	// "one away from a run" leaves nothing recognisable — 4821 is one
	// substitution from 4321 and looks like nothing at all. Applying these
	// rules at that length refuses ordinary choices for no attacker benefit,
	// which is precisely the over-reach worth avoiding.
	if len(pin) < 6 {
		return "", false
	}
	if allButOneSame(pin) {
		return "it is all one digit apart from a single change", true
	}
	if step, ok := allButOneRun(pin); ok {
		return runReason(step, true), true
	}

	return "", false
}

func runReason(step int, nearMiss bool) string {
	var what string
	switch step {
	case 1:
		what = "the digits count up"
	case -1:
		what = "the digits count down"
	case 2:
		what = "the digits count up in twos"
	case -2:
		what = "the digits count down in twos"
	}
	if nearMiss {
		return what + " apart from a single change"
	}
	return what
}

// commonPINs are the ones tried first, phrased for whoever has to pick another.
// Short on purpose: a long blocklist starts rejecting fine choices, and the
// structural rules below already cover most of what would go in one.
var commonPINs = map[string]string{
	"123456": "it is the single most common PIN there is",
	"696969": "it is one of the most common PINs there is",
	"159753": "it is a straight line across the keypad",
	"147258": "it is a straight line down the keypad",
	"357159": "it is a keypad pattern",
	"1234":   "it is the single most common PIN there is",
	"2580":   "it is a straight line down the middle of the keypad",
	"1379":   "it is the four corners of the keypad",
	"0852":   "it is a straight line up the middle of the keypad",
}

// notWorthIt records what was considered and deliberately left out, so the next
// person does not have to rediscover the reasoning:
//
//   - **Dates.** 090317 is indistinguishable from any other number without
//     knowing the family, and date-shaped PINs are a large slice of the
//     memorable ones. Refusing them all to catch a guess is a bad trade.
//   - **Near misses of a repeated block.** 121213 and friends. Correct in
//     principle, but it refuses roughly six percent of the space — twenty times
//     everything else here combined — and starts rejecting numbers that look
//     arbitrary to whoever chose them.
//   - **Runs of four identical digits inside a longer PIN**, like 451111.
//     Weak-ish, but "all but one same" already covers five of six, and the next
//     tier down buys little for a rule nobody can predict.
//   - **Arithmetic runs with a step above two.** 147036 is not a pattern anyone
//     recognises, so it is not on a guessing list.
var notWorthIt = struct{}{}

func allSame(pin string) bool {
	return strings.Count(pin, string(pin[0])) == len(pin)
}

// run reports a constant step of ±1 or ±2 across the whole PIN. Two covers the
// evens and the odds, which people do pick and lists do include; a step of
// three or more is not a pattern anyone recognises.
func run(pin string) (step int, ok bool) {
	d := int(pin[1]) - int(pin[0])
	if d != 1 && d != -1 && d != 2 && d != -2 {
		return 0, false
	}
	for i := 2; i < len(pin); i++ {
		if int(pin[i])-int(pin[i-1]) != d {
			return 0, false
		}
	}
	return d, true
}

// allButOneSame reports a PIN that is one digit away from all-identical:
// 111112, 111211, 011111.
func allButOneSame(pin string) bool {
	counts := map[byte]int{}
	for i := range len(pin) {
		counts[pin[i]]++
	}
	for _, n := range counts {
		if n == len(pin)-1 {
			return true
		}
	}
	return false
}

// allButOneRun reports a PIN one digit away from a consecutive run: 123457,
// 023456, 123356. Checked by trying every single-position substitution rather
// than by pattern-matching, which keeps it honest about what "one away" means.
func allButOneRun(pin string) (step int, ok bool) {
	b := []byte(pin)
	for i := range len(b) {
		orig := b[i]
		for d := byte('0'); d <= '9'; d++ {
			if d == orig {
				continue
			}
			b[i] = d
			if s, isRun := run(string(b)); isRun {
				b[i] = orig
				return s, true
			}
		}
		b[i] = orig
	}
	return 0, false
}

// repeatedBlock reports the shortest block that tiles the whole PIN: 121212 is
// "12" three times, 123123 is "123" twice.
func repeatedBlock(pin string) (block string, ok bool) {
	n := len(pin)
	for size := 1; size <= n/2; size++ {
		if n%size != 0 {
			continue
		}
		candidate := pin[:size]
		if strings.Repeat(candidate, n/size) == pin {
			return candidate, true
		}
	}
	return "", false
}

func isPalindrome(pin string) bool {
	for i, j := 0, len(pin)-1; i < j; i, j = i+1, j-1 {
		if pin[i] != pin[j] {
			return false
		}
	}
	return true
}
