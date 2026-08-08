package policy

import (
	"os"
	"strings"
	"testing"
)

// These tests exist because the defect they cover is invisible by
// construction: toml.Unmarshal drops a key with no matching field, and
// validation runs afterwards on the struct, so a misspelled optional key had
// already vanished before anything could object. The config validated, and
// did nothing.

// strict is what `doorman check` loads with; loose is what the daemon does.
var (
	strict = Options{StrictUnknownKeys: true}
	loose  = Options{}
)

// loadStrict returns the error text from a strict load, or "" when it passed.
func loadStrict(t *testing.T, policySrc, handsetsSrc string) string {
	t.Helper()
	var hdata []byte
	if handsetsSrc != "" {
		hdata = []byte(handsetsSrc)
	}
	_, err := fromSplitTOML([]byte(policySrc), hdata, strict)
	if err == nil {
		return ""
	}
	return err.Error()
}

func TestUnknownOptionalKeyOnAnExtensionIsRejected(t *testing.T) {
	// The original report: a key that does nothing, on an entry where every
	// key it might have been is optional, so nothing downstream notices.
	src := strings.Replace(valid,
		`label = "Kitchen"`,
		"label = \"Kitchen\"\non_no_input = \"voicemail\"", 1)

	got := loadStrict(t, src, "")
	if !strings.Contains(got, `unknown key "on_no_input"`) {
		t.Errorf("strict load = %q, want it to name on_no_input", got)
	}
	if !strings.Contains(got, "[[extensions]]") {
		t.Errorf("strict load = %q, want the section it appeared in", got)
	}

	// The same file must still load for the daemon: invariant 4 says a policy
	// it accepted yesterday cannot start taking the phone down today.
	if _, err := fromSplitTOML([]byte(src), nil, loose); err != nil {
		t.Errorf("daemon load rejected an unknown key: %v", err)
	}
}

func TestMisspeltOptionalKeyInHouseIsRejectedWithASuggestion(t *testing.T) {
	// `voicmail` is the nastiest shape of this bug: the file reads as though
	// the house has a mailbox, and it silently does not.
	src := strings.Replace(valid,
		`handsets = ["kitchen", "office"]`,
		"handsets = [\"kitchen\", \"office\"]\nvoicmail = \"family\"", 1)

	got := loadStrict(t, src, "")
	if !strings.Contains(got, `unknown key "voicmail" in [house]`) {
		t.Errorf("strict load = %q, want the key and its section", got)
	}
	if !strings.Contains(got, "did you mean voicemail?") {
		t.Errorf("strict load = %q, want a suggestion", got)
	}
}

func TestMisspeltRequiredKeyNamesTheKeyNotJustTheConsequence(t *testing.T) {
	// This one was already caught — an empty handset list violates a semantic
	// rule — but only by its consequence. Both messages should appear now:
	// the cause first, then the rule it broke.
	src := strings.Replace(valid,
		`handsets = ["kitchen", "office"]`, `handets = ["kitchen", "office"]`, 1)

	got := loadStrict(t, src, "")
	if !strings.Contains(got, `unknown key "handets" in [house]`) {
		t.Errorf("strict load = %q, want the misspelling named", got)
	}
	if !strings.Contains(got, "did you mean handsets?") {
		t.Errorf("strict load = %q, want a suggestion", got)
	}
	if !strings.Contains(got, "must list at least one handset") {
		t.Errorf("strict load = %q, want the semantic problem kept too", got)
	}
}

func TestMisspeltSectionIsReportedOnceNotPerKey(t *testing.T) {
	// A mistyped section header leaves every key under it undecoded as well.
	// Reporting all of them buries the one that matters.
	src := valid + "\n[[extenions]]\npin = \"550118\"\nlabel = \"Spare\"\nhandsets = [\"office\"]\n"

	got := loadStrict(t, src, "")
	if !strings.Contains(got, `unknown section "extenions"`) {
		t.Errorf("strict load = %q, want the section named", got)
	}
	if !strings.Contains(got, "did you mean extensions?") {
		t.Errorf("strict load = %q, want a suggestion", got)
	}
	for _, child := range []string{`"pin"`, `"label"`} {
		if strings.Contains(got, "unknown key "+child) {
			t.Errorf("strict load = %q, want no separate complaint about %s", got, child)
		}
	}
}

func TestUnknownKeyInAStepIsAttributedToTheNestedSection(t *testing.T) {
	src := `
[house]
handsets = ["kitchen"]

[[handsets]]
id = "kitchen"
endpoint = "PJSIP/kitchen"

[[extensions]]
pin = "428917"
label = "Kids"

  [[extensions.steps]]
  handsets = ["kitchen"]
  ringz = 3
`
	got := loadStrict(t, src, "")
	if !strings.Contains(got, `unknown key "ringz" in [[extensions.steps]]`) {
		t.Errorf("strict load = %q, want the nested section path", got)
	}
	if !strings.Contains(got, "did you mean rings?") {
		t.Errorf("strict load = %q, want a suggestion", got)
	}
}

func TestUnknownKeyInHandsetsFileIsAttributedToThatFile(t *testing.T) {
	bad := strings.Replace(splitHandsets, `label = "Adult handsets"`, `labl = "Adult handsets"`, 1)
	if !strings.Contains(splitHandsets, "[[groups]]") {
		t.Fatal("fixture changed: expected a [[groups]] section to typo")
	}
	bad = strings.Replace(bad, `id = "kitchen"`, "id = \"kitchen\"\nlabl = \"Kitchen\"", 1)

	_, err := fromSplitTOML([]byte(splitPolicy), []byte(bad), strict)
	if err == nil {
		t.Fatal("strict load accepted an unknown key in handsets.toml")
	}
	got := err.Error()
	if !strings.Contains(got, "handsets: unknown key \"labl\"") {
		t.Errorf("err = %q, want it attributed to the handsets file", got)
	}
	if !strings.Contains(got, "did you mean label?") {
		t.Errorf("err = %q, want a suggestion", got)
	}
}

func TestDaemonWarnsAboutUnknownKeysAndLoadsAnyway(t *testing.T) {
	// The check-vs-daemon split, stated as a test. The daemon hears about
	// every unknown key and still returns a working policy.
	src := strings.Replace(valid,
		`handsets = ["kitchen", "office"]`,
		"handsets = [\"kitchen\", \"office\"]\nvoicmail = \"family\"", 1)

	var seen []UnknownKey
	p, err := fromSplitTOML([]byte(src), nil, Options{
		OnUnknownKey: func(u UnknownKey) { seen = append(seen, u) },
	})
	if err != nil {
		t.Fatalf("daemon load failed on an unknown key: %v", err)
	}
	if p == nil {
		t.Fatal("daemon load returned no policy")
	}
	if len(seen) != 1 {
		t.Fatalf("warnings = %+v, want exactly one", seen)
	}
	if seen[0].File != "policy" || seen[0].Path != "house.voicmail" || seen[0].Suggest != "voicemail" {
		t.Errorf("warning = %+v", seen[0])
	}
	// And the warning is about the key, never its value — a callback that
	// received values would put config contents into the daemon's log.
	if strings.Contains(seen[0].String(), "family") {
		t.Errorf("warning %q leaked the key's value", seen[0].String())
	}
}

func TestRetiredInlineAfterhoursIsNotReportedAsUnknownKeys(t *testing.T) {
	// Extension.Afterhours decodes into an `any` so the retired inline-table
	// form can produce a helpful error rather than a decode failure. The
	// decoder counts everything under an `any` as undecoded, so without the
	// known-leaf check this file would collect two bogus "unknown key"
	// complaints on top of the good message compileChecked already has.
	src := `
[house]
handsets = ["kitchen"]

[[handsets]]
id = "kitchen"
endpoint = "PJSIP/kitchen"

[[extensions]]
pin = "428917"
label = "Kids"
handsets = ["kitchen"]

  [extensions.afterhours]
  start = "20:30"
  end = "07:00"
`
	got := loadStrict(t, src, "")
	if strings.Contains(got, "unknown key") {
		t.Errorf("err = %q, want no unknown-key noise for the retired form", got)
	}
	if !strings.Contains(got, "afterhours is now a named schedule") {
		t.Errorf("err = %q, want the existing helpful message", got)
	}
}

func TestValidConfigProducesNoUnknownKeys(t *testing.T) {
	// The guard against over-eager detection: every key the fixtures use is
	// one the schema knows, so strict and loose loads must agree.
	for name, args := range map[string][2]string{
		"legacy single file": {valid, ""},
		"split layout":       {splitPolicy, splitHandsets},
	} {
		if got := loadStrict(t, args[0], args[1]); got != "" {
			t.Errorf("%s: strict load = %q, want it to pass", name, got)
		}
	}
}

func TestShippedExamplesHaveNoUnknownKeys(t *testing.T) {
	// The examples are the first thing anyone copies. A stray key there ships
	// the defect to every new install.
	pol, err := os.ReadFile("../../examples/policy.example.toml")
	if err != nil {
		t.Skipf("examples not readable from here: %v", err)
	}
	hs, err := os.ReadFile("../../examples/handsets.example.toml")
	if err != nil {
		t.Skipf("examples not readable from here: %v", err)
	}
	_, err = fromSplitTOML(pol, hs, Options{AllowPlaceholders: true, StrictUnknownKeys: true})
	if err != nil {
		t.Errorf("shipped examples fail a strict check: %v", err)
	}
}

func TestLintReportsUnknownKeysForTheEditor(t *testing.T) {
	// The language server reuses LintSplit, so a typo has to reach it as an
	// ordinary problem string.
	src := strings.Replace(valid,
		`label = "Kitchen"`, "label = \"Kitchen\"\non_no_input = \"voicemail\"", 1)

	problems := LintSplit([]byte(src), nil)
	if len(problems) != 1 || !strings.Contains(problems[0], `unknown key "on_no_input"`) {
		t.Fatalf("problems = %q, want one about on_no_input", problems)
	}
	// Only the offending name may be quoted: the LSP places its squiggle by
	// searching for quoted tokens, and a quoted suggestion would land the
	// marker on a correctly spelled key elsewhere in the file.
	if strings.Count(problems[0], `"`) != 2 {
		t.Errorf("problem %q quotes more than the offending key", problems[0])
	}
}

func TestLoadHandsetsRefusesUnknownKeys(t *testing.T) {
	// render's output overwrites the Asterisk config; a dropped `page` is a
	// phone that never pages, with nothing at runtime to explain why.
	dir := t.TempDir()
	path := dir + "/handsets.toml"
	src := "[[handsets]]\nid = \"kitchen\"\nendpoint = \"PJSIP/kitchen\"\npag = true\n"
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, _, err := LoadHandsets(path); err == nil {
		t.Fatal("LoadHandsets accepted an unknown key")
	} else if !strings.Contains(err.Error(), `unknown key "pag"`) {
		t.Errorf("err = %v, want it to name the key", err)
	}
}

func TestSuggestionsOnlyFireWhenTheyAreProbablyRight(t *testing.T) {
	fields := map[string]bool{
		"pin": true, "label": true, "handsets": true, "steps": true,
		"voicemail": true, "afterhours": true, "afterhours_ring": true, "enabled": true,
	}
	cases := []struct {
		key  string
		want string
	}{
		{"afterhous", "afterhours"},  // one deletion
		{"voicmail", "voicemail"},    // one deletion
		{"handets", "handsets"},      // one deletion
		{"lable", "label"},           // transposition
		{"on_no_input", ""},          // a real key we do not have — no guess
		{"pim", "pin"},               // short key, one substitution
		{"completely_different", ""}, // nothing close
		{"afterhours_rings", "afterhours_ring"},
	}
	for _, c := range cases {
		if got := nearest(c.key, fields); got != c.want {
			t.Errorf("nearest(%q) = %q, want %q", c.key, got, c.want)
		}
	}
}

func TestEditDistanceBasics(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{"", "abc", 3},
		{"abc", "abc", 0},
		{"kitten", "sitting", 3},
		{"voicmail", "voicemail", 1},
	}
	for _, c := range cases {
		if got := editDistance(c.a, c.b); got != c.want {
			t.Errorf("editDistance(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestAfterhoursDescribeShowsTheWindow(t *testing.T) {
	p := mustPolicy(t, `
[house]
handsets = ["kitchen"]

[[handsets]]
id = "kitchen"
endpoint = "PJSIP/kitchen"

[[schedules]]
id = "school-night"
start = "20:30"
end = "07:00"
days = ["SU", "MO"]

[[schedules]]
id = "always"
start = "09:00"
end = "17:00"

[[extensions]]
pin = "428917"
label = "Kids"
handsets = ["kitchen"]
voicemail = "kids"
afterhours = "school-night"

[[extensions]]
pin = "310244"
label = "Day"
handsets = ["kitchen"]
voicemail = "kids"
afterhours = "always"
`)
	for _, e := range p.Extensions() {
		switch e.Label {
		case "Kids":
			if got := e.Afterhours.Describe(); got != "20:30–07:00 SU MO" {
				t.Errorf("Describe = %q", got)
			}
			if e.AfterhoursID != "school-night" {
				t.Errorf("AfterhoursID = %q", e.AfterhoursID)
			}
		case "Day":
			if got := e.Afterhours.Describe(); got != "09:00–17:00 every day" {
				t.Errorf("Describe = %q", got)
			}
		}
	}

	var nilWindow *Afterhours
	if got := nilWindow.Describe(); got != "" {
		t.Errorf("nil Describe = %q, want empty", got)
	}
}

func TestDisabledScheduleKeepsItsIDForDisplay(t *testing.T) {
	// nil Afterhours with a non-empty AfterhoursID is "switched off", which
	// behaves like "no schedule" but means something entirely different to
	// whoever is reading the file.
	p := mustPolicy(t, `
[house]
handsets = ["kitchen"]

[[handsets]]
id = "kitchen"
endpoint = "PJSIP/kitchen"

[[schedules]]
id = "school-night"
enabled = false
start = "20:30"
end = "07:00"

[[extensions]]
pin = "428917"
label = "Kids"
handsets = ["kitchen"]
afterhours = "school-night"
`)
	e, ok := p.LookupExtension("428917")
	if !ok {
		t.Fatal("extension missing")
	}
	if e.Afterhours != nil {
		t.Error("disabled schedule should compile to a nil window")
	}
	if e.AfterhoursID != "school-night" {
		t.Errorf("AfterhoursID = %q, want the id retained for display", e.AfterhoursID)
	}
}
