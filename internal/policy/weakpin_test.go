package policy

import (
	"fmt"
	"testing"
)

// The whole point is that people may choose. These are ordinary human choices
// that must keep working, or the rule has overreached and everyone ends up with
// a random number on a scrap of paper.
func TestChosenPINsAreAllowed(t *testing.T) {
	fine := []string{
		"428917", "902118", "310244", "775301",
		"090317",                     // a date — weaker than random, undetectable without knowing the family
		"847203", "615492", "308176", // ordinary chosen numbers
		"131313", // wait: this repeats, see the weak list; kept out deliberately
		"800813",
		"4821", "9137",
	}
	for _, pin := range fine {
		if pin == "131313" {
			continue // covered by TestRepeatedBlocksAreRefused
		}
		if why, weak := WeakPIN(pin); weak {
			t.Errorf("%s was refused (%s) — a chosen PIN should be allowed", pin, why)
		}
	}
}

func TestSequencesAreRefused(t *testing.T) {
	for _, pin := range []string{"123456", "654321", "456789", "98765", "3456"} {
		why, weak := WeakPIN(pin)
		if !weak {
			t.Errorf("%s should be refused: it is a run", pin)
		}
		if why == "" {
			t.Errorf("%s was refused with no reason given", pin)
		}
	}
}

func TestRepeatedDigitsAndBlocksAreRefused(t *testing.T) {
	for _, pin := range []string{"111111", "000000", "999999", "4444"} {
		if _, weak := WeakPIN(pin); !weak {
			t.Errorf("%s should be refused: every digit is the same", pin)
		}
	}
}

func TestRepeatedBlocksAreRefused(t *testing.T) {
	for _, pin := range []string{"121212", "123123", "131313", "1212", "252525"} {
		why, weak := WeakPIN(pin)
		if !weak {
			t.Errorf("%s should be refused: it is a repeated block", pin)
		}
		if why == "" {
			t.Errorf("%s was refused with no reason", pin)
		}
	}
}

// The reason has to name what is wrong, because someone has to pick a
// different number and "invalid" tells them nothing.
func TestReasonsAreSpecific(t *testing.T) {
	cases := map[string]string{
		"123456": "common",
		"456789": "count up",
		"98765":  "count down",
		"131313": "repeated",
		"777777": "every digit",
	}
	for pin, want := range cases {
		why, weak := WeakPIN(pin)
		if !weak {
			t.Fatalf("%s should be weak", pin)
		}
		if !contains(why, want) {
			t.Errorf("%s: reason %q should mention %q", pin, why, want)
		}
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}

// A generated PIN must never trip the rule it is offered as the escape from.
func TestRotationNeverGeneratesAWeakPIN(t *testing.T) {
	taken := map[string]bool{}
	for range 2000 {
		pin, err := generatePIN(6, taken)
		if err != nil {
			t.Fatalf("generatePIN: %v", err)
		}
		if why, weak := WeakPIN(pin); weak {
			t.Fatalf("rotation produced a weak PIN (%s) — the escape hatch cannot fail the rule", why)
		}
	}
}

// ── the near-miss rules ──────────────────────────────────────────────────

func TestAllButOneSameIsRefused(t *testing.T) {
	for _, pin := range []string{"111112", "111211", "011111", "999998", "222232"} {
		why, weak := WeakPIN(pin)
		if !weak {
			t.Errorf("%s should be refused: one digit from all-identical", pin)
		} else if why == "" {
			t.Errorf("%s refused with no reason", pin)
		}
	}
}

func TestAllButOneRunIsRefused(t *testing.T) {
	for _, pin := range []string{
		"123457", // one past the end
		"023456", // one at the start
		"123356", // one in the middle
		"654322", // descending, one off
	} {
		if _, weak := WeakPIN(pin); !weak {
			t.Errorf("%s should be refused: one digit from a run", pin)
		}
	}
}

// Counting in twos only reaches five digits — 0,2,4,6,8 has nowhere to go — so
// this rule is about short PINs, and 246810 is not a run at all: as digits it
// is 2,4,6,8,1,0 and eight to one is not a step of two.
func TestEvensAndOddsAreRefused(t *testing.T) {
	for _, pin := range []string{"02468", "13579", "97531", "86420", "2468", "9753"} {
		if _, weak := WeakPIN(pin); !weak {
			t.Errorf("%s should be refused: it counts in twos", pin)
		}
	}
	// And the near-miss rules must not reach down to four digits, where they
	// refuse ordinary choices: 4821 is one substitution from 4321.
	for _, pin := range []string{"4821", "9137", "3856"} {
		if why, weak := WeakPIN(pin); weak {
			t.Errorf("%s refused (%s) — near-miss rules should not apply at four digits", pin, why)
		}
	}
}

func TestPalindromesAreRefused(t *testing.T) {
	for _, pin := range []string{"123321", "456654", "801108", "8228"} {
		if _, weak := WeakPIN(pin); !weak {
			t.Errorf("%s should be refused: it reads the same backwards", pin)
		}
	}
}

// The burden question, answered exhaustively rather than by estimate. Every
// rule added here costs somebody a choice, so the total has to stay small
// enough that "pick a number you'll remember" is still true in practice.
//
// If this number grows, the rules have overreached.
func TestRefusalRateIsSmall(t *testing.T) {
	const total = 1_000_000
	refused := 0
	byReason := map[string]int{}

	for n := range total {
		pin := fmt.Sprintf("%06d", n)
		if why, weak := WeakPIN(pin); weak {
			refused++
			byReason[why]++
		}
	}

	pct := float64(refused) * 100 / total
	t.Logf("refused %d of %d six-digit PINs (%.3f%%)", refused, total, pct)
	for why, n := range byReason {
		t.Logf("    %-52s %6d", why, n)
	}

	// Generous ceiling: the rules as designed land near a third of a percent.
	// This exists to catch a rule that quietly rejects a large slice.
	if pct > 1.5 {
		t.Errorf("refusing %.2f%% of the space is too burdensome — someone picking "+
			"a memorable number will hit this by accident", pct)
	}
	// And a floor, so the rules cannot silently stop doing anything.
	if refused < 2000 {
		t.Errorf("only %d PINs refused — the rules are not catching the obvious ones", refused)
	}
}
