package policy

import "testing"

// The whole point is that people may choose. These are ordinary human choices
// that must keep working, or the rule has overreached and everyone ends up with
// a random number on a scrap of paper.
func TestChosenPINsAreAllowed(t *testing.T) {
	fine := []string{
		"428917", "902118", "310244", "775301",
		"246810", // an arithmetic run, but not a consecutive one
		"090317", // a date — weaker than random, undetectable without knowing the family
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
