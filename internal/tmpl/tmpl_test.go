package tmpl_test

import (
	"os"
	"strings"
	"testing"

	"callmemaybe/internal/policy"
	"callmemaybe/internal/tmpl"
)

const handsets = `
[[handsets]]
id = "kids-room"
label = "Kids Room"
endpoint = "PJSIP/kids-room"

[[handsets]]
id = "kitchen"
label = "Kitchen"
endpoint = "PJSIP/kitchen"

[[handsets]]
id = "primary-bed"
label = "Primary bed"
endpoint = "PJSIP/primary-bed"

[[groups]]
id = "adults"
handsets = ["kitchen", "primary-bed"]
`

const basePolicy = `
[house]
handsets = ["kitchen"]
voicemail = "family"
`

func loadShipped(t *testing.T) *tmpl.Template {
	t.Helper()
	data, err := os.ReadFile("../../templates/kids-line.toml")
	if err != nil {
		t.Fatalf("reading the shipped template: %v", err)
	}
	tp, err := tmpl.Parse(data)
	if err != nil {
		t.Fatalf("the shipped template does not validate: %v", err)
	}
	return tp
}

func answers(redirect bool) tmpl.Answers {
	return tmpl.Answers{
		"room":              "kids-room",
		"escalate_to":       []string{"adults"},
		"mailbox":           "kids",
		"quiet":             tmpl.Window{Start: "20:30", End: "07:00", Days: []string{"SU", "MO", "TU", "WE", "TH"}},
		"redirect_at_night": redirect,
	}
}

func opts() tmpl.Options {
	labels := map[string]string{"kids-room": "Kids Room", "kitchen": "Kitchen", "primary-bed": "Primary bed"}
	return tmpl.Options{HandsetLabel: func(id string) string { return labels[id] }}
}

// The output has to load. Anything less and the template system is a way to
// produce broken configurations quickly.
func TestRenderedTemplateIsValidPolicy(t *testing.T) {
	tp := loadShipped(t)
	out, err := tp.Render(answers(true), opts())
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	if problems := policy.LintSplit([]byte(basePolicy+out), []byte(handsets)); len(problems) != 0 {
		t.Fatalf("rendered policy does not validate: %v\n\n%s", problems, out)
	}
}

// The conditional value is the whole reason `when` exists at field level.
func TestConditionalValueTogglesTheRedirect(t *testing.T) {
	tp := loadShipped(t)

	with, err := tp.Render(answers(true), opts())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(with, "afterhours_ring") {
		t.Error("saying yes to night redirect should emit afterhours_ring")
	}

	without, err := tp.Render(answers(false), opts())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(without, "afterhours_ring") {
		t.Error("saying no should leave afterhours_ring out entirely")
	}
	// And without a redirect it must still have somewhere to send the caller.
	if !strings.Contains(without, "voicemail") {
		t.Error("without a redirect the quiet window needs a mailbox")
	}
	if problems := policy.LintSplit([]byte(basePolicy+without), []byte(handsets)); len(problems) != 0 {
		t.Fatalf("the no-redirect variant does not validate: %v", problems)
	}
}

// This is the case that ruled out mustache/handlebars. A room label containing
// a quote and a newline would, under string interpolation, close the string and
// inject whatever follows. Because values are serialised rather than pasted, it
// comes out as an ordinary escaped string.
func TestAnswersCannotInjectTOML(t *testing.T) {
	tp := loadShipped(t)
	a := answers(false)
	// A hostile "label" arriving through a handset label lookup.
	o := opts()
	o.HandsetLabel = func(id string) string {
		return "Kids\"\npin = \"000000\"\nlabel = \"pwned"
	}

	out, err := tp.Render(a, o)
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	// Exactly one extension, and no injected PIN.
	if n := strings.Count(out, "[[extensions]]"); n != 1 {
		t.Fatalf("injection produced %d extensions, want 1:\n%s", n, out)
	}
	if strings.Contains(out, `pin = "000000"`) {
		t.Fatalf("a literal PIN was injected:\n%s", out)
	}
	// And it still parses and validates.
	if problems := policy.LintSplit([]byte(basePolicy+out), []byte(handsets)); len(problems) != 0 {
		t.Fatalf("output with a hostile label does not validate: %v\n%s", problems, out)
	}
}

// A template may emit extensions and schedules. Anything that could add to the
// allow-list would let its author ring the house.
func TestTemplateCannotEmitPeople(t *testing.T) {
	_, err := tmpl.Parse([]byte(`
[template]
id = "evil"
name = "Evil"
version = "1.0.0"

[[emit.people]]
name = "Author"
numbers = ["+15125550100"]
`))
	if err == nil {
		t.Fatal("a template emitting [[people]] should not load")
	}
	// It fails because emit.people is not a field, so nothing is emitted.
	if !strings.Contains(err.Error(), "emits nothing") {
		t.Logf("note: rejected with %v", err)
	}
}

func TestTemplateRejectsHardcodedPINs(t *testing.T) {
	_, err := tmpl.Parse([]byte(`
[template]
id = "x"
name = "X"
version = "1.0.0"

[[emit.extensions]]
label = "Fixed"
pin = "123456"
handsets = ["kitchen"]
`))
	if err == nil || !strings.Contains(err.Error(), "$generate.pin") {
		t.Fatalf("a hardcoded PIN should be refused, got %v", err)
	}
}

func TestTemplateRejectsDanglingReferences(t *testing.T) {
	_, err := tmpl.Parse([]byte(`
[template]
id = "x"
name = "X"
version = "1.0.0"

[[questions]]
id = "room"
prompt = "Which?"
type = "handset"

[[emit.extensions]]
label = "$nonexistent"
pin = "$generate.pin"
handsets = "$room"
`))
	if err == nil || !strings.Contains(err.Error(), "nonexistent") {
		t.Fatalf("a reference to a missing question should be caught before running, got %v", err)
	}
}

func TestTemplateRejectsUnknownQuestionType(t *testing.T) {
	_, err := tmpl.Parse([]byte(`
[template]
id = "x"
name = "X"
version = "1.0.0"

[[questions]]
id = "q"
prompt = "?"
type = "freeform-blob"

[[emit.extensions]]
label = "X"
pin = "$generate.pin"
handsets = ["kitchen"]
`))
	if err == nil || !strings.Contains(err.Error(), "freeform-blob") {
		t.Fatalf("an unknown question type should be refused, got %v", err)
	}
}

// JSON is accepted so a template served without a file extension still works.
func TestJSONTemplatesParse(t *testing.T) {
	tp, err := tmpl.Parse([]byte(`{
  "template": {"id": "json-one", "name": "JSON One", "version": "1.0.0"},
  "questions": [{"id": "room", "prompt": "Which?", "type": "handset"}],
  "emit": {"extensions": [
    {"label": "Line", "pin": "$generate.pin", "handsets": "$room"}
  ]}
}`))
	if err != nil {
		t.Fatalf("JSON template: %v", err)
	}
	out, err := tp.Render(tmpl.Answers{"room": "kitchen"}, opts())
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if problems := policy.LintSplit([]byte(basePolicy+out), []byte(handsets)); len(problems) != 0 {
		t.Fatalf("JSON-authored template produced invalid policy: %v", problems)
	}
}

func TestMissingAnswersAreReported(t *testing.T) {
	tp := loadShipped(t)
	_, err := tp.Render(tmpl.Answers{"room": "kids-room"}, opts())
	if err == nil || !strings.Contains(err.Error(), "unanswered") {
		t.Fatalf("expected an unanswered-questions error, got %v", err)
	}
}

func TestWrongAnswerTypeIsReported(t *testing.T) {
	tp := loadShipped(t)
	a := answers(true)
	a["quiet"] = "20:30-07:00" // a string where a window belongs
	_, err := tp.Render(a, opts())
	if err == nil || !strings.Contains(err.Error(), "wrong answer type") {
		t.Fatalf("expected a type error, got %v", err)
	}
}

// A template's schedule id must not collide with one the operator already has.
func TestScheduleIDsAreMadeUnique(t *testing.T) {
	tp := loadShipped(t)
	o := opts()
	o.TakenScheduleIDs = map[string]bool{"kids-quiet-hours": true}

	out, err := tp.Render(answers(true), o)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `id = "kids-quiet-hours-2"`) {
		t.Errorf("a colliding schedule id should be suffixed:\n%s", out)
	}
	// And the extension must reference the renamed schedule, not the original.
	if !strings.Contains(out, `afterhours = "kids-quiet-hours-2"`) {
		t.Errorf("the reference was not rewritten to the renamed schedule:\n%s", out)
	}
}

func TestGeneratedPINsAreUniqueAndValid(t *testing.T) {
	tp := loadShipped(t)
	taken := map[string]bool{}
	for range 20 {
		o := opts()
		o.TakenPINs = taken
		out, err := tp.Render(answers(true), o)
		if err != nil {
			t.Fatal(err)
		}
		for label, pin := range tp.PINs(out) {
			if len(pin) != 6 {
				t.Errorf("%s: pin %q is not 6 digits", label, pin)
			}
			if pin == policy.PlaceholderPIN {
				t.Errorf("%s: generated the placeholder sentinel", label)
			}
		}
	}
	if len(taken) != 20 {
		t.Errorf("generated %d distinct PINs across 20 renders, want 20", len(taken))
	}
}
