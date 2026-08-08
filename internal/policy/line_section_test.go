package policy

import (
	"strings"
	"testing"
)

// The [line] section: identity, and the disposition knob.

const lineHouse = `
[house]
handsets = ["kitchen"]

[[handsets]]
id = "kitchen"
endpoint = "PJSIP/kitchen"
`

const lineHouseWithMailbox = `
[house]
handsets = ["kitchen"]
voicemail = "family"

[[handsets]]
id = "kitchen"
endpoint = "PJSIP/kitchen"
`

func lineFrom(t *testing.T, section string) LineIdentity {
	t.Helper()
	p, err := FromTOML([]byte(section + lineHouse))
	if err != nil {
		t.Fatalf("FromTOML: %v", err)
	}
	return p.Line()
}

func lineError(t *testing.T, section string) string {
	t.Helper()
	if _, err := FromTOML([]byte(section + lineHouse)); err != nil {
		return err.Error()
	}
	t.Fatalf("expected %q to be refused", section)
	return ""
}

// The compatibility gate at the section level: no [line] means the behaviour
// that shipped before there was one. Anything else here is a phone whose
// disposition changed under an operator who never asked for it.
func TestAbsentLineSectionIsTodaysBehaviour(t *testing.T) {
	l := lineFrom(t, "")
	if l.OnNoInput != NoInputDismiss {
		t.Errorf("on_no_input = %q, want %q for a file with no [line] section",
			l.OnNoInput, NoInputDismiss)
	}
	if l.Label != "" || l.Number != "" || l.Prompts != "" {
		t.Errorf("identity = %+v, want it empty", l)
	}
}

func TestLineIdentityCompiles(t *testing.T) {
	l := lineFrom(t, `
[line]
label       = "Mertaugh Enterprises"
number      = "512-555-0142"
prompts     = "concierge"
on_no_input = "ring-house"
`)
	if l.Label != "Mertaugh Enterprises" {
		t.Errorf("label = %q", l.Label)
	}
	// Written however the operator likes, stored E.164 — same as the allow-list.
	if l.Number != "+15125550142" {
		t.Errorf("number = %q, want it normalised to E.164", l.Number)
	}
	if l.Prompts != "concierge" {
		t.Errorf("prompts = %q", l.Prompts)
	}
	if l.OnNoInput != NoInputRingHouse {
		t.Errorf("on_no_input = %q", l.OnNoInput)
	}
}

func TestEveryOnNoInputValueLoads(t *testing.T) {
	for _, want := range []NoInput{NoInputDismiss, NoInputRingHouse, NoInputVoicemail} {
		// voicemail needs somewhere to land; that rule has its own test below.
		house := lineHouse
		if want == NoInputVoicemail {
			house = lineHouseWithMailbox
		}
		p, err := FromTOML([]byte("[line]\non_no_input = \"" + string(want) + "\"\n" + house))
		if err != nil {
			t.Fatalf("%s: %v", want, err)
		}
		if got := p.Line().OnNoInput; got != want {
			t.Errorf("on_no_input = %q, want %q", got, want)
		}
	}
}

// A misspelled disposition is the failure this section exists to avoid:
// "voicemial" that loads and dismisses is a business line quietly dropping
// every lead. It has to be a refusal, with the alternatives named.
func TestUnknownOnNoInputIsRefusedAndNamesTheAlternatives(t *testing.T) {
	msg := lineError(t, "[line]\non_no_input = \"voicemial\"\n")
	for _, want := range []string{"voicemial", "dismiss", "ring-house", "voicemail"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the error should mention %q: %s", want, msg)
		}
	}
}

// The same rule afterhours already applies per extension, for the same
// reason: a caller sent to voicemail needs a mailbox at the other end.
func TestVoicemailDispositionNeedsAHouseMailbox(t *testing.T) {
	msg := lineError(t, "[line]\non_no_input = \"voicemail\"\n")
	if !strings.Contains(msg, "voicemail") || !strings.Contains(msg, "[house]") {
		t.Errorf("the error should say the house has no mailbox: %s", msg)
	}
}

func TestLineNumberMustBeAPhoneNumber(t *testing.T) {
	if msg := lineError(t, "[line]\nnumber = \"not a number\"\n"); !strings.Contains(msg, "not a number") {
		t.Errorf("the error should quote the offending value: %s", msg)
	}
}

// The prompt prefix names a directory Asterisk resolves under its sounds path.
// doorman never opens it, which is exactly why an escape has to be refused
// here rather than discovered later.
func TestPromptPrefixRejectsAnythingThatCouldEscapeTheSoundsDirectory(t *testing.T) {
	for _, bad := range []string{"../../etc", "/absolute", "with space", "trailing/", "has.dot"} {
		if msg := lineError(t, "[line]\nprompts = \""+bad+"\"\n"); !strings.Contains(msg, "media prefix") {
			t.Errorf("prompts = %q: the error should explain the shape: %s", bad, msg)
		}
	}
	for _, good := range []string{"concierge", "call-me-maybe", "packs/concierge", "a1_b-2"} {
		if got := lineFrom(t, "[line]\nprompts = \""+good+"\"\n").Prompts; got != good {
			t.Errorf("prompts = %q was refused", good)
		}
	}
}

// handsets.toml is shared by every line, so a [line] section in it is a
// statement that cannot be true. Same treatment as the other misplaced
// sections: refused, never merged.
func TestLineSectionInHandsetsFileIsRefused(t *testing.T) {
	_, err := fromSplitTOML(
		[]byte("[house]\nhandsets = [\"kitchen\"]\n"),
		[]byte("[line]\nlabel = \"Nope\"\n\n[[handsets]]\nid = \"kitchen\"\nendpoint = \"PJSIP/kitchen\"\n"),
		Options{})
	if err == nil {
		t.Fatal("a [line] section in handsets.toml must be refused")
	}
	if !strings.Contains(err.Error(), "policy.toml") {
		t.Errorf("the error should say where it belongs: %v", err)
	}
}

// And the linter agrees with the loader, so the editor squiggles it too.
func TestLintReportsAMisplacedLineSection(t *testing.T) {
	problems := LintSplit(
		[]byte("[house]\nhandsets = [\"kitchen\"]\n"),
		[]byte("[line]\nlabel = \"Nope\"\n\n[[handsets]]\nid = \"kitchen\"\nendpoint = \"PJSIP/kitchen\"\n"))
	if len(problems) == 0 || !strings.Contains(strings.Join(problems, " "), "policy.toml") {
		t.Errorf("problems = %v, want the misplaced [line] section reported", problems)
	}
}

// ── unknown keys inside [line] ───────────────────────────────────────────

// The whole point of publishing this section is that a model or a person
// writing `on_no_inpt` finds out. Unknown-key detection reflects over
// policy.File, so [line] joined it for free — this test is what says so
// out loud, and what fails if the section is ever decoded some other way.
func TestATypoInsideTheLineSectionIsCaughtWithASuggestion(t *testing.T) {
	problems := LintSplit([]byte("[line]\non_no_inpt = \"voicemail\"\n"+lineHouse), nil)
	joined := strings.Join(problems, "\n")
	for _, want := range []string{`"on_no_inpt"`, "[line]", "did you mean on_no_input?"} {
		if !strings.Contains(joined, want) {
			t.Errorf("problems should mention %q:\n%s", want, joined)
		}
	}
}

// And the loader refuses it under `doorman check`'s strictness while the
// daemon merely warns — the split that keeps a bad edit from taking the phone
// down. Both halves have to see the same key.
func TestTheDaemonWarnsAboutALineTypoAndKeepsAnswering(t *testing.T) {
	var seen []UnknownKey
	p, err := fromSplitTOML([]byte("[line]\nlabl = \"Home\"\n"+lineHouse), nil,
		Options{OnUnknownKey: func(u UnknownKey) { seen = append(seen, u) }})
	if err != nil {
		t.Fatalf("the daemon must still load this file: %v", err)
	}
	if p.Line().OnNoInput != NoInputDismiss {
		t.Errorf("on_no_input = %q, want the default", p.Line().OnNoInput)
	}
	if len(seen) != 1 || seen[0].Key != "labl" || seen[0].Suggest != "label" {
		t.Fatalf("unknown keys = %+v, want labl → label", seen)
	}

	if _, err := fromSplitTOML([]byte("[line]\nlabl = \"Home\"\n"+lineHouse), nil,
		Options{StrictUnknownKeys: true}); err == nil {
		t.Error("`doorman check` must refuse the same key the daemon warns about")
	}
}
