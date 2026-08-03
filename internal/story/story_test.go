package story_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"callmemaybe/internal/story"
)

const src = `---
story:
  title: The Threshold
  start: entrance

voices:
  NARRATOR: { backend: piper, id: en_GB-alba-medium }
  DOORMAN:  { backend: polly, id: Amy }

defaults:
  timeout: 8s
  retries: 2
  on-timeout: repeat
  on-invalid: reprompt
---

## entrance

The brass door gives way to a lobby that smells of rain
and floor polish.

DOORMAN: Good evening. I don't believe you're expected.

1. [Say you are expected anyway](#bluff)
2. [Ask who *is* expected](#question)
3. [Leave](#politely-out)

## bluff

<choices timeout="12s" retries="1" on-timeout="#politely-out"/>

DOORMAN: Expected. I see. And by whom, precisely?

1. [Invent a name](#question)
0. [Hear that again](#bluff)

## question

<goto anchor="#politely-out"/>

DOORMAN: Nobody, as it happens. It has been a very quiet century.

## politely-out

DOORMAN: Very good. Mind the step.

<end/>
`

func parse(t *testing.T, s string) *story.Story {
	t.Helper()
	st, err := story.Parse([]byte(s))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if problems, _ := st.Validate(); len(problems) > 0 {
		t.Fatalf("validate: %v", problems)
	}
	return st
}

func TestParsesTheShape(t *testing.T) {
	st := parse(t, src)

	if st.Meta.Title != "The Threshold" || st.Meta.Start != "entrance" {
		t.Errorf("meta = %+v", st.Meta)
	}
	if len(st.Nodes) != 4 {
		t.Fatalf("got %d nodes, want 4: %v", len(st.Nodes), st.Order)
	}

	e := st.Nodes["entrance"]
	if len(e.Choices) != 3 {
		t.Fatalf("entrance has %d choices, want 3", len(e.Choices))
	}
	// The list number is the DTMF digit.
	if e.Choices[0].Digit != "1" || e.Choices[0].Target != "bluff" {
		t.Errorf("choice 1 = %+v", e.Choices[0])
	}
	if e.Choices[2].Target != "politely-out" {
		t.Errorf("choice 3 target = %q", e.Choices[2].Target)
	}
}

// An unlabelled paragraph belongs to the first voice declared, and a wrapped
// line is one utterance rather than two.
func TestUnlabelledParagraphsNarrateAndWrappedLinesJoin(t *testing.T) {
	st := parse(t, src)
	e := st.Nodes["entrance"]

	if len(e.Lines) != 2 {
		t.Fatalf("entrance has %d lines, want 2: %+v", len(e.Lines), e.Lines)
	}
	if e.Lines[0].Speaker != "NARRATOR" {
		t.Errorf("unlabelled paragraph went to %q, want NARRATOR", e.Lines[0].Speaker)
	}
	if !strings.Contains(e.Lines[0].Text, "rain and floor polish") {
		t.Errorf("a wrapped line should join into one utterance: %q", e.Lines[0].Text)
	}
	if e.Lines[1].Speaker != "DOORMAN" {
		t.Errorf("speaker = %q", e.Lines[1].Speaker)
	}
}

// Emphasis is left alone: it is prosody, not speaker identity, which is the
// whole reason labels carry the voice.
func TestEmphasisSurvivesIntoTheText(t *testing.T) {
	st := parse(t, src)
	for _, c := range st.Nodes["entrance"].Choices {
		if c.Digit == "2" && !strings.Contains(c.Label, "*is*") {
			t.Errorf("emphasis was stripped from a choice label: %q", c.Label)
		}
	}
}

func TestClipNamesAreStableAndReadable(t *testing.T) {
	st := parse(t, src)
	e := st.Nodes["entrance"]
	if e.Lines[0].Clip != "entrance-01" || e.Lines[1].Clip != "entrance-02" {
		t.Errorf("clips = %q, %q", e.Lines[0].Clip, e.Lines[1].Clip)
	}
	if n := len(st.Utterances()); n != 5 {
		t.Errorf("got %d utterances to render, want 5", n)
	}
}

func TestPerNodeOverrides(t *testing.T) {
	st := parse(t, src)
	b := st.Nodes["bluff"]
	if b.Behaviour.Timeout != 12*time.Second {
		t.Errorf("timeout = %v, want 12s", b.Behaviour.Timeout)
	}
	if b.Behaviour.Retries != 1 {
		t.Errorf("retries = %d, want 1", b.Behaviour.Retries)
	}
	if b.Behaviour.OnTimeout != "#politely-out" {
		t.Errorf("on-timeout = %q", b.Behaviour.OnTimeout)
	}
	// Defaults still apply where not overridden.
	if b.Behaviour.OnInvalid != "reprompt" {
		t.Errorf("on-invalid = %q, want the inherited default", b.Behaviour.OnInvalid)
	}
	if st.Nodes["entrance"].Behaviour.Timeout != 8*time.Second {
		t.Error("a node without overrides should inherit the frontmatter default")
	}
}

func TestGotoAndEnd(t *testing.T) {
	st := parse(t, src)
	if st.Nodes["question"].Goto != "politely-out" {
		t.Errorf("goto = %q", st.Nodes["question"].Goto)
	}
	if !st.Nodes["politely-out"].End {
		t.Error("<end/> was not recorded")
	}
	if !st.Nodes["politely-out"].Terminal() {
		t.Error("an <end/> node is terminal")
	}
}

// ── validation ──────────────────────────────────────────────────────────

func problemsFor(t *testing.T, s string) string {
	t.Helper()
	st, err := story.Parse([]byte(s))
	all := ""
	if err != nil {
		all += err.Error()
	}
	if st != nil {
		p, _ := st.Validate()
		all += p.Error()
	}
	return all
}

func TestDanglingAnchorIsCaught(t *testing.T) {
	got := problemsFor(t, `---
story: { start: a }
voices: { N: { backend: piper, id: x } }
---
## a
N: Hello.

1. [Go nowhere](#nowhere)
`)
	if !strings.Contains(got, "nowhere") {
		t.Errorf("a link to a missing node must be caught at build time, got: %s", got)
	}
}

func TestUndeclaredSpeakerIsCaught(t *testing.T) {
	got := problemsFor(t, `---
story: { start: a }
voices: { NARRATOR: { backend: piper, id: x } }
---
## a
GHOST: Boo.
`)
	if !strings.Contains(got, "GHOST") {
		t.Errorf("an undeclared speaker must be caught, got: %s", got)
	}
}

func TestDuplicateDigitIsCaught(t *testing.T) {
	got := problemsFor(t, `---
story: { start: a }
voices: { N: { backend: piper, id: x } }
---
## a
N: Hello.

1. [One](#a)
1. [Also one](#a)
`)
	if !strings.Contains(got, "twice") {
		t.Errorf("two choices on the same digit must be caught, got: %s", got)
	}
}

func TestMissingStartIsCaught(t *testing.T) {
	got := problemsFor(t, `---
story: { start: nope }
voices: { N: { backend: piper, id: x } }
---
## a
N: Hello.
`)
	if !strings.Contains(got, "nope") {
		t.Errorf("got: %s", got)
	}
}

func TestChoicesAndGotoTogetherIsCaught(t *testing.T) {
	got := problemsFor(t, `---
story: { start: a }
voices: { N: { backend: piper, id: x } }
---
## a
<goto anchor="#a"/>
N: Hello.

1. [One](#a)
`)
	if !strings.Contains(got, "only do one") {
		t.Errorf("got: %s", got)
	}
}

func TestUnreachableNodeIsAWarningNotAnError(t *testing.T) {
	st, err := story.Parse([]byte(`---
story: { start: a }
voices: { N: { backend: piper, id: x } }
---
## a
N: Hello.
<end/>

## orphan
N: Nobody comes here.
`))
	if err != nil {
		t.Fatal(err)
	}
	problems, warnings := st.Validate()
	if len(problems) != 0 {
		t.Errorf("an offcut should not fail a build: %v", problems)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0].Msg, "orphan") {
		t.Errorf("warnings = %v", warnings)
	}
}

func TestUnknownVerbIsCaught(t *testing.T) {
	got := problemsFor(t, `---
story: { start: a }
voices: { N: { backend: piper, id: x } }
---
## a
<dial number="+15125550100"/>
N: Hello.
`)
	if !strings.Contains(got, "unknown verb") {
		t.Errorf("there is no dial verb and there never will be, got: %s", got)
	}
}

func TestProblemsCarryLineNumbers(t *testing.T) {
	st, _ := story.Parse([]byte(`---
story: { start: a }
voices: { N: { backend: piper, id: x } }
---
## a
N: Hello.

1. [Nowhere](#missing)
`))
	problems, _ := st.Validate()
	if len(problems) == 0 {
		t.Fatal("expected a problem")
	}
	if problems[0].Line == 0 {
		t.Error("a problem without a line number makes an author hunt")
	}
}

// ── the remaining corners ───────────────────────────────────────────────

func TestFrontmatterProblemsAreReported(t *testing.T) {
	for _, c := range []struct{ name, src, want string }{
		{"unclosed", "---\nstory: { start: a }\n## a\n", "never closed"},
		{"bad yaml", "---\nstory: {{{\n---\n## a\n", "valid YAML"},
		{"bad duration", "---\nstory: { start: a }\nvoices: { N: {backend: piper, id: x} }\ndefaults: { timeout: soon }\n---\n## a\nN: Hi.\n", "duration"},
	} {
		got := problemsFor(t, c.src)
		if !strings.Contains(got, c.want) {
			t.Errorf("%s: want %q in %q", c.name, c.want, got)
		}
	}
}

func TestContentBeforeAnyNodeIsCaught(t *testing.T) {
	got := problemsFor(t, `---
story: { start: a }
voices: { N: { backend: piper, id: x } }
---
This line has no scene to belong to.

## a
N: Hi.
`)
	if !strings.Contains(got, "before the first") {
		t.Errorf("got: %s", got)
	}
}

func TestDuplicateNodeIsCaught(t *testing.T) {
	got := problemsFor(t, `---
story: { start: a }
voices: { N: { backend: piper, id: x } }
---
## a
N: One.

## a
N: Two.
`)
	if !strings.Contains(got, "twice") {
		t.Errorf("got: %s", got)
	}
}

func TestBadBehaviourRuleIsCaught(t *testing.T) {
	got := problemsFor(t, `---
story: { start: a }
voices: { N: { backend: piper, id: x } }
---
## a
<choices on-invalid="explode"/>
N: Hi.

1. [Round](#a)
`)
	if !strings.Contains(got, "on-invalid") {
		t.Errorf("got: %s", got)
	}
}

func TestNonSelfClosingTagIsCaught(t *testing.T) {
	got := problemsFor(t, `---
story: { start: a }
voices: { N: { backend: piper, id: x } }
---
## a
<end>
N: Hi.
`)
	if !strings.Contains(got, "self-closing") {
		t.Errorf("got: %s", got)
	}
}

func TestSfxWithoutANameIsCaught(t *testing.T) {
	got := problemsFor(t, `---
story: { start: a }
voices: { N: { backend: piper, id: x } }
---
## a
<sfx/>
N: Hi.
`)
	if !strings.Contains(got, "needs a name") {
		t.Errorf("got: %s", got)
	}
}

func TestGotoWithoutAnAnchorIsCaught(t *testing.T) {
	if !strings.Contains(problemsFor(t, `---
story: { start: a }
voices: { N: { backend: piper, id: x } }
---
## a
<goto/>
N: Hi.
`), "needs an anchor") {
		t.Error("a goto with nowhere to go must be caught")
	}
}

func TestTooManyChoicesIsCaught(t *testing.T) {
	var b strings.Builder
	b.WriteString("---\nstory: { start: a }\nvoices: { N: {backend: piper, id: x} }\n---\n## a\nN: Hi.\n\n")
	for i := range 11 {
		fmt.Fprintf(&b, "%d. [Go](#a)\n", i%10)
	}
	if !strings.Contains(problemsFor(t, b.String()), "limit is") {
		t.Error("more choices than digits must be caught")
	}
}

func TestSlugMatchesMarkdownAnchorRules(t *testing.T) {
	for in, want := range map[string]string{
		"The Cellar":    "the-cellar",
		"left_tunnel":   "left-tunnel",
		"  Spaced  ":    "spaced",
		"Punctuation!?": "punctuation",
	} {
		if got := story.Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestClipsIncludeChoiceLabelsWithMarkupStripped(t *testing.T) {
	st := parse(t, src)
	var label string
	for _, c := range st.Clips() {
		if c.Name == "choice/entrance-2" {
			label = c.Text
		}
	}
	if label == "" {
		t.Fatal("choice labels must be rendered — otherwise the menu is silent")
	}
	if strings.Contains(label, "*") {
		t.Errorf("emphasis marks must not be read aloud: %q", label)
	}
}
