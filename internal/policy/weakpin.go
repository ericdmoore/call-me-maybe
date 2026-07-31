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
// an extension nobody uses. Memorable is a feature, not a compromise.
//
// What has to be refused is a much narrower set than "anything a human chose".
// The rate limiter caps a caller at a few failures an hour, which makes
// searching a million combinations hopeless — but it does nothing whatsoever
// against 123456, because that does not require searching. It requires one
// guess. So the rule is not "do not choose", it is "not the ones everybody
// tries first".
//
// Deliberately not included: birthdays and dates. They are weaker than random
// but there is no way to tell 090317 from any other number without knowing the
// family, and refusing every date-shaped PIN would reject a large slice of the
// memorable ones for a guess. That belongs in advice, not in a validator.
func WeakPIN(pin string) (reason string, weak bool) {
	if len(pin) < 2 {
		return "", false // length is a separate rule
	}

	if r, ok := commonPINs[pin]; ok {
		return r, true
	}
	if allSameDigit(pin) {
		return fmt.Sprintf("every digit is %c", pin[0]), true
	}
	if step, ok := consecutiveRun(pin); ok {
		if step > 0 {
			return "the digits just count up", true
		}
		return "the digits just count down", true
	}
	if block, ok := repeatedBlock(pin); ok {
		return fmt.Sprintf("it is %q repeated", block), true
	}
	return "", false
}

// commonPINs are the ones tried first, with the reason phrased for whoever has
// to pick a different one. Short list on purpose: every entry has to earn its
// place, and a long blocklist starts rejecting fine choices.
var commonPINs = map[string]string{
	"123456": "it is the single most common PIN there is",
	"654321": "it is one of the most common PINs there is",
	"111111": "it is one of the most common PINs there is",
	"000000": "it is one of the most common PINs there is",
	"121212": "it is one of the most common PINs there is",
	"112233": "it is one of the most common PINs there is",
	"123123": "it is one of the most common PINs there is",
	"696969": "it is one of the most common PINs there is",
	"666666": "it is one of the most common PINs there is",
	"159753": "it is a keypad pattern, which is as guessable as a sequence",
	"147258": "it is a keypad pattern, which is as guessable as a sequence",
	"258456": "it is a keypad pattern, which is as guessable as a sequence",
	"1234":   "it is the single most common PIN there is",
	"0000":   "it is one of the most common PINs there is",
	"1111":   "it is one of the most common PINs there is",
	"4321":   "it is one of the most common PINs there is",
	"1212":   "it is one of the most common PINs there is",
	"2580":   "it is a straight line down the keypad",
}

func allSameDigit(pin string) bool {
	return strings.Count(pin, string(pin[0])) == len(pin)
}

// consecutiveRun reports a run where every digit steps by the same ±1.
// 123456 and 654321, but also 456789 and 98765.
func consecutiveRun(pin string) (step int, ok bool) {
	d := int(pin[1]) - int(pin[0])
	if d != 1 && d != -1 {
		return 0, false
	}
	for i := 2; i < len(pin); i++ {
		if int(pin[i])-int(pin[i-1]) != d {
			return 0, false
		}
	}
	return d, true
}

// repeatedBlock reports the shortest block that tiles the whole PIN: 121212 is
// "12" three times, 123123 is "123" twice. A PIN of all-identical digits is
// caught earlier and reported more plainly.
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
