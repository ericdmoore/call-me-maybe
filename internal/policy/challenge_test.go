package policy

import (
	"strings"
	"testing"
)

// [line] challenge_no_input and the [challenge] section (#150 M1).

func TestChallengeDefaultsWhenTheSectionIsAbsent(t *testing.T) {
	p, err := FromTOML([]byte(lineHouse))
	if err != nil {
		t.Fatal(err)
	}
	if r := p.Challenge(); r.Low != 2 || r.High != 9 || r.MaxAnswerDigits() != 2 {
		t.Fatalf("challenge = %+v, want factors 2–9 answered in at most 2 digits", r)
	}
	if p.Line().ChallengeNoInput {
		t.Fatal("challenge_no_input must be off unless written")
	}
}

func TestChallengeNoInputCompilesBesideADispositionItCanGuard(t *testing.T) {
	for _, disposition := range []string{"voicemail", "ring-house"} {
		p, err := FromTOML([]byte("[line]\non_no_input = \"" + disposition + "\"\nchallenge_no_input = true\n" + lineHouseWithMailbox))
		if err != nil {
			t.Fatalf("%s: %v", disposition, err)
		}
		if !p.Line().ChallengeNoInput {
			t.Fatalf("%s: challenge_no_input did not compile", disposition)
		}
	}
}

// A dismiss line has nothing to guard: refused rather than kept as a switch
// that reads like a feature and does nothing.
func TestChallengeNoInputWithDismissIsRefused(t *testing.T) {
	for _, section := range []string{
		"[line]\nchallenge_no_input = true\n",
		"[line]\non_no_input = \"dismiss\"\nchallenge_no_input = true\n",
	} {
		msg := lineError(t, section)
		if !strings.Contains(msg, "challenge_no_input") || !strings.Contains(msg, "nothing to guard") {
			t.Errorf("%q: error = %q", section, msg)
		}
	}
}

func TestChallengeFactorsAreValidated(t *testing.T) {
	ok, err := FromTOML([]byte("[challenge]\nfactors = [3, 7]\n" + lineHouse))
	if err != nil {
		t.Fatal(err)
	}
	if r := ok.Challenge(); r.Low != 3 || r.High != 7 || r.Describe() != "factors 3–7" {
		t.Fatalf("challenge = %+v", r)
	}
	for factors, want := range map[string]string{
		"[2, 10]":   "within 0 to 9",
		"[-1, 9]":   "within 0 to 9",
		"[8, 3]":    "low end above",
		"[4]":       "two numbers",
		"[1, 2, 3]": "two numbers",
	} {
		_, err := FromTOML([]byte("[challenge]\nfactors = " + factors + "\n" + lineHouse))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("factors = %s: error = %v, want it to say %q", factors, err, want)
		}
	}
}

// Each file owns its sections: [challenge] is per line, and the loader names
// the file it belongs in rather than merging it.
func TestChallengeSectionInTheWrongFileIsRefused(t *testing.T) {
	_, err := fromSplitTOML([]byte("[house]\nhandsets = [\"kitchen\"]\n"),
		[]byte("[challenge]\nfactors = [2, 9]\n[[handsets]]\nid = \"kitchen\"\nendpoint = \"PJSIP/kitchen\"\n"), Options{})
	if err == nil || !strings.Contains(err.Error(), "challenge belong in policy.toml") {
		t.Fatalf("[challenge] in handsets.toml: err = %v", err)
	}
	if problems := LintSplit([]byte("[house]\nhandsets = [\"kitchen\"]\n"),
		[]byte("[challenge]\nfactors = [2, 9]\n[[handsets]]\nid = \"kitchen\"\nendpoint = \"PJSIP/kitchen\"\n")); len(problems) == 0 {
		t.Fatal("the lint path accepted [challenge] in handsets.toml")
	}
	for _, file := range []string{"trunks", "contacts"} {
		if hint := misplaced[file+".challenge"]; !strings.Contains(hint, "policy.toml") {
			t.Errorf("%s.toml has no hint for a stray [challenge]: %q", file, hint)
		}
	}
}
