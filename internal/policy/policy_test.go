package policy

import (
	"strings"
	"testing"
	"time"
)

const valid = `
[house]
handsets = ["kitchen", "office"]

[[handsets]]
id = "kitchen"
endpoint = "PJSIP/kitchen"

[[handsets]]
id = "office"
endpoint = "PJSIP/office"

[[people]]
name = "Grandma"
numbers = ["512-555-0100"]

[[extensions]]
pin = "428917"
label = "Kitchen"
handsets = ["kitchen"]

[[extensions]]
pin = "310244"
label = "Office"
handsets = ["office"]
`

func mustPolicy(t *testing.T, src string) *Policy {
	t.Helper()
	p, err := FromTOML([]byte(src))
	if err != nil {
		t.Fatalf("FromTOML: %v", err)
	}
	return p
}

func TestPolicyNormalisesAllowListOnLoad(t *testing.T) {
	p := mustPolicy(t, valid)
	if c, ok := p.LookupCaller("+15125550100"); !ok || c.Name != "Grandma" {
		t.Errorf("LookupCaller = %+v, %v", c, ok)
	}
	if _, ok := p.LookupCaller("+15125559999"); ok {
		t.Error("unexpected match for unlisted number")
	}
	if _, ok := p.LookupCaller(""); ok {
		t.Error("empty caller ID must never match")
	}
}

func TestPolicyResolvesHandsetsToEndpoints(t *testing.T) {
	p := mustPolicy(t, valid)
	house := p.HouseEndpoints()
	if len(house) != 2 || house[0] != "PJSIP/kitchen" || house[1] != "PJSIP/office" {
		t.Errorf("house = %v", house)
	}
	e, ok := p.LookupExtension("428917")
	if !ok || len(e.Plan.Steps) != 1 || len(e.Plan.Steps[0].Endpoints) != 1 ||
		e.Plan.Steps[0].Endpoints[0] != "PJSIP/kitchen" {
		t.Errorf("extension = %+v, %v", e, ok)
	}
}

func TestPolicyDetectsUniformPinLength(t *testing.T) {
	if p := mustPolicy(t, valid); p.PinLength != 6 {
		t.Errorf("PinLength = %d, want 6", p.PinLength)
	}
}

func TestPolicyRejectsUnknownHandsetReference(t *testing.T) {
	broken := strings.Replace(valid, `handsets = ["office"]`, `handsets = ["garage"]`, 1)
	if _, err := FromTOML([]byte(broken)); err == nil || !strings.Contains(err.Error(), "garage") {
		t.Errorf("err = %v, want mention of garage", err)
	}
}

func TestPolicyRejectsDuplicatePins(t *testing.T) {
	broken := strings.Replace(valid, `pin = "310244"`, `pin = "428917"`, 1)
	if _, err := FromTOML([]byte(broken)); err == nil || !strings.Contains(err.Error(), "duplicate pin") {
		t.Errorf("err = %v, want duplicate pin", err)
	}
}

func TestPolicyRejectsInvalidNumber(t *testing.T) {
	broken := strings.Replace(valid, `numbers = ["512-555-0100"]`, `numbers = ["ring me"]`, 1)
	if _, err := FromTOML([]byte(broken)); err == nil || !strings.Contains(err.Error(), "not a valid phone number") {
		t.Errorf("err = %v", err)
	}
}

func TestPolicyDisabledExtensionIsAbsent(t *testing.T) {
	src := valid + "\n[[extensions]]\npin = \"999999\"\nlabel = \"Off\"\nhandsets = [\"kitchen\"]\nenabled = false\n"
	p := mustPolicy(t, src)
	if _, ok := p.LookupExtension("999999"); ok {
		t.Error("disabled extension should not resolve")
	}
	if p.ExtensionCount() != 2 {
		t.Errorf("ExtensionCount = %d, want 2", p.ExtensionCount())
	}
}

func TestPolicyErrorDoesNotLeakFullPin(t *testing.T) {
	broken := strings.Replace(valid, `label = "Office"`, `label = ""`, 1)
	_, err := FromTOML([]byte(broken))
	if err == nil {
		t.Fatal("expected error for missing label")
	}
	if strings.Contains(err.Error(), "310244") {
		t.Errorf("error leaks full pin: %v", err)
	}
}

const ladderPolicy = `
[house]
handsets = ["adults"]
voicemail = "family"

[[schedules]]
id = "school-night"
start = "20:30"
end = "07:00"
days = ["SU", "MO", "TU", "WE", "TH"]

[[handsets]]
id = "kids-room"
endpoint = "PJSIP/kids-room"

[[handsets]]
id = "kitchen"
endpoint = "PJSIP/kitchen"

[[handsets]]
id = "primary-bed"
endpoint = "PJSIP/primary-bed"

[[groups]]
id = "adults"
handsets = ["kitchen", "primary-bed"]

[[extensions]]
pin = "555001"
label = "Kids"
voicemail = "kids"
afterhours = "school-night"

  [[extensions.steps]]
  handsets = ["kids-room"]
  rings = 3

  [[extensions.steps]]
  handsets = ["adults"]
  rings = 4

`

func TestGroupsExpandEverywhere(t *testing.T) {
	p := mustPolicy(t, ladderPolicy)
	house := p.HousePlan()
	if len(house.Steps) != 1 || len(house.Steps[0].Endpoints) != 2 {
		t.Fatalf("house = %+v", house)
	}
	if house.Mailbox != "family" {
		t.Errorf("house mailbox = %q", house.Mailbox)
	}
	e, _ := p.LookupExtension("555001")
	if got := e.Plan.Steps[1].Endpoints; len(got) != 2 || got[0] != "PJSIP/kitchen" {
		t.Errorf("step 2 endpoints = %v (group not expanded)", got)
	}
}

func TestLadderStepsCompileToTimeouts(t *testing.T) {
	p := mustPolicy(t, ladderPolicy)
	e, _ := p.LookupExtension("555001")
	if len(e.Plan.Steps) != 2 {
		t.Fatalf("steps = %d", len(e.Plan.Steps))
	}
	if e.Plan.Steps[0].Rings != 3 || e.Plan.Steps[1].Rings != 4 {
		t.Errorf("rings = %d, %d (want 3 and 4; the session applies its cycle length)",
			e.Plan.Steps[0].Rings, e.Plan.Steps[1].Rings)
	}
	if e.Plan.Steps[0].Timeout != 0 {
		t.Errorf("rings-based step should leave Timeout zero, got %v", e.Plan.Steps[0].Timeout)
	}
	if e.Plan.Mailbox != "kids" {
		t.Errorf("mailbox = %q", e.Plan.Mailbox)
	}
}

func TestAfterhoursWindowSemantics(t *testing.T) {
	p := mustPolicy(t, ladderPolicy)
	e, _ := p.LookupExtension("555001")
	ah := e.Afterhours
	if ah == nil {
		t.Fatal("afterhours not compiled")
	}
	at := func(day time.Weekday, hh, mm int) time.Time {
		// 2026-07-05 is a Sunday.
		base := time.Date(2026, 7, 5, hh, mm, 0, 0, time.Local)
		return base.AddDate(0, 0, int(day-time.Sunday))
	}
	cases := []struct {
		t    time.Time
		want bool
		why  string
	}{
		{at(time.Monday, 21, 0), true, "Monday 21:00 is after Monday bedtime"},
		{at(time.Monday, 19, 0), false, "Monday 19:00 is before bedtime"},
		{at(time.Tuesday, 3, 0), true, "Tuesday 03:00 belongs to Monday's window"},
		{at(time.Tuesday, 7, 0), false, "Tuesday 07:00 — window closed"},
		{at(time.Saturday, 3, 0), true, "Saturday 03:00 belongs to Friday? No — TH is last start day; FR not listed... Friday 03:00 belongs to Thursday"},
		{at(time.Friday, 23, 0), false, "Friday 23:00 — FR is not a start day (weekend)"},
		{at(time.Saturday, 21, 0), false, "Saturday night — not a school night"},
	}
	// Fix the mislabelled case: Saturday 03:00 follows Friday, and FR is NOT a
	// start day, so it must be inactive.
	cases[4] = struct {
		t    time.Time
		want bool
		why  string
	}{at(time.Saturday, 3, 0), false, "Saturday 03:00 follows Friday, which is not a start day"}
	// Friday 03:00 follows Thursday (a start day): active.
	cases = append(cases, struct {
		t    time.Time
		want bool
		why  string
	}{at(time.Friday, 3, 0), true, "Friday 03:00 belongs to Thursday's window"})

	for _, c := range cases {
		if got := ah.Active(c.t); got != c.want {
			t.Errorf("Active(%s) = %v, want %v — %s", c.t.Format("Mon 15:04"), got, c.want, c.why)
		}
	}
}

func TestAfterhoursRequiresVoicemail(t *testing.T) {
	broken := strings.Replace(ladderPolicy, `voicemail = "kids"`, ``, 1)
	if _, err := FromTOML([]byte(broken)); err == nil || !strings.Contains(err.Error(), "no voicemail") {
		t.Errorf("err = %v", err)
	}
}

func TestDisabledScheduleMakesReferencesInert(t *testing.T) {
	off := strings.Replace(ladderPolicy, `id = "school-night"`,
		"id = \"school-night\"\nenabled = false", 1)
	p := mustPolicy(t, off)
	e, _ := p.LookupExtension("555001")
	if e.Afterhours != nil {
		t.Error("a disabled schedule must compile references to nil — that is the holiday switch")
	}
}

func TestUnknownScheduleReferenceFails(t *testing.T) {
	broken := strings.Replace(ladderPolicy, `afterhours = "school-night"`, `afterhours = "summer"`, 1)
	if _, err := FromTOML([]byte(broken)); err == nil || !strings.Contains(err.Error(), "unknown schedule") {
		t.Errorf("err = %v", err)
	}
}

func TestRetiredInlineAfterhoursGetsAHelpfulError(t *testing.T) {
	broken := strings.Replace(ladderPolicy, `afterhours = "school-night"`, "", 1)
	broken += "\n"
	broken = strings.Replace(broken, `label = "Kids"`,
		"label = \"Kids\"\n[extensions.afterhours]\nstart = \"20:00\"\nend = \"07:00\"", 1)
	_, err := FromTOML([]byte(broken))
	if err == nil || !strings.Contains(err.Error(), "named schedule") {
		t.Errorf("err = %v, want migration hint", err)
	}
}

func TestStepsAndHandsetsAreMutuallyExclusive(t *testing.T) {
	broken := strings.Replace(ladderPolicy, `label = "Kids"`, "label = \"Kids\"\nhandsets = [\"kids-room\"]", 1)
	if _, err := FromTOML([]byte(broken)); err == nil || !strings.Contains(err.Error(), "one or the other") {
		t.Errorf("err = %v", err)
	}
}

func TestGroupMayNotContainGroups(t *testing.T) {
	broken := ladderPolicy + "\n[[groups]]\nid = \"everyone\"\nhandsets = [\"adults\"]\n"
	if _, err := FromTOML([]byte(broken)); err == nil || !strings.Contains(err.Error(), "groups may only contain handsets") {
		t.Errorf("err = %v", err)
	}
}

// A hand-edited short PIN used to load fine: rotate refused to generate one,
// but nothing stopped someone typing it. The lobby is a keypad on the PSTN, so
// that is a credential with a hundred combinations.
func TestShortPINIsRejectedOnLoad(t *testing.T) {
	for _, pin := range []string{"1", "12", "123"} {
		problems := LintSplit([]byte(`
[house]
handsets = ["kitchen"]

[[extensions]]
pin = "`+pin+`"
label = "Kitchen"
handsets = ["kitchen"]
`), []byte(`
[[handsets]]
id = "kitchen"
endpoint = "PJSIP/kitchen"
`))
		if len(problems) == 0 {
			t.Errorf("pin %q (%d digits) loaded without complaint", pin, len(pin))
			continue
		}
		joined := strings.Join(problems, " ")
		if !strings.Contains(joined, "minimum") {
			t.Errorf("pin %q: expected a minimum-length error, got %v", pin, problems)
		}
		// The offending PIN must not appear in the message.
		if strings.Contains(joined, `"`+pin+`"`) {
			t.Errorf("pin %q was echoed back in the error: %v", pin, problems)
		}
	}
}

func TestPINAtTheFloorIsAccepted(t *testing.T) {
	problems := LintSplit([]byte(`
[house]
handsets = ["kitchen"]

[[extensions]]
pin = "4821"
label = "Kitchen"
handsets = ["kitchen"]
`), []byte(`
[[handsets]]
id = "kitchen"
endpoint = "PJSIP/kitchen"
`))
	if len(problems) != 0 {
		t.Fatalf("a %d-digit pin should load, got %v", MinPINLength, problems)
	}
}

// afterhours could only ever mean "take a message". That cannot express
// homework hours, a rotating night shift, or forwarding to a babysitter — all
// of which want the window to ring somewhere else instead.
func TestAfterhoursRingRedirects(t *testing.T) {
	handsets := []byte(`
[[handsets]]
id = "kids-room"
endpoint = "PJSIP/kids-room"

[[handsets]]
id = "office"
endpoint = "PJSIP/office"

[[groups]]
id = "adults"
handsets = ["office"]
`)
	policy := []byte(`
[house]
handsets = ["office"]

[[schedules]]
id = "homework"
start = "16:00"
end = "18:00"
days = ["MO", "TU", "WE", "TH"]

[[extensions]]
pin = "902118"
label = "Kids"
handsets = ["kids-room"]
afterhours = "homework"
afterhours_ring = ["adults"]
`)
	if problems := LintSplit(policy, handsets); len(problems) != 0 {
		t.Fatalf("afterhours_ring should be accepted: %v", problems)
	}
}

// The redirect is a destination, so its handsets must resolve like any other.
func TestAfterhoursRingValidatesTargets(t *testing.T) {
	handsets := []byte(`
[[handsets]]
id = "kids-room"
endpoint = "PJSIP/kids-room"
`)
	problems := LintSplit([]byte(`
[house]
handsets = ["kids-room"]

[[schedules]]
id = "night"
start = "20:00"
end = "07:00"

[[extensions]]
pin = "902118"
label = "Kids"
handsets = ["kids-room"]
afterhours = "night"
afterhours_ring = ["nobody-by-that-name"]
`), handsets)
	if len(problems) == 0 {
		t.Fatal("an unknown afterhours_ring target should be a load error")
	}
	if !strings.Contains(strings.Join(problems, " "), "nobody-by-that-name") {
		t.Errorf("the error should name the unresolved target: %v", problems)
	}
}

// A redirect with no window to apply to is a configuration mistake, not a
// silent no-op.
func TestAfterhoursRingRequiresASchedule(t *testing.T) {
	problems := LintSplit([]byte(`
[house]
handsets = ["kids-room"]

[[extensions]]
pin = "902118"
label = "Kids"
handsets = ["kids-room"]
afterhours_ring = ["kids-room"]
`), []byte(`
[[handsets]]
id = "kids-room"
endpoint = "PJSIP/kids-room"
`))
	if len(problems) == 0 {
		t.Fatal("afterhours_ring without afterhours should be an error")
	}
}

// The old rule was "afterhours requires voicemail". With a redirect there is
// somewhere for the caller to go, so a mailbox becomes optional.
func TestAfterhoursRingSatisfiesTheSomewhereToGoRule(t *testing.T) {
	handsets := []byte(`
[[handsets]]
id = "kids-room"
endpoint = "PJSIP/kids-room"

[[handsets]]
id = "office"
endpoint = "PJSIP/office"
`)
	if problems := LintSplit([]byte(`
[house]
handsets = ["office"]

[[schedules]]
id = "night"
start = "20:00"
end = "07:00"

[[extensions]]
pin = "902118"
label = "Kids"
handsets = ["kids-room"]
afterhours = "night"
afterhours_ring = ["office"]
`), handsets); len(problems) != 0 {
		t.Fatalf("a redirect should satisfy the somewhere-to-go rule: %v", problems)
	}

	// Neither a mailbox nor a redirect is still an error.
	if problems := LintSplit([]byte(`
[house]
handsets = ["office"]

[[schedules]]
id = "night"
start = "20:00"
end = "07:00"

[[extensions]]
pin = "902118"
label = "Kids"
handsets = ["kids-room"]
afterhours = "night"
`), handsets); len(problems) == 0 {
		t.Error("afterhours with neither voicemail nor redirect should still fail")
	}
}
