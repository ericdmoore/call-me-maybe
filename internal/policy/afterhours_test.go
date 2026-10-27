package policy

import (
	"strings"
	"testing"
	"time"
)

// businessHours is weekdays 9–5, the rule issue #149 exists for: one window
// cannot close both weeknights and weekends, so the line names two.
const businessHours = `
[house]
handsets = ["office"]

[[handsets]]
id = "office"
endpoint = "PJSIP/office"

[[schedules]]
id = "nights"
start = "17:00"
end = "09:00"
days = ["SU", "MO", "TU", "WE", "TH", "FR", "SA"]

[[schedules]]
id = "weekend-days"
start = "09:00"
end = "17:00"
days = ["SA", "SU"]

[[extensions]]
pin = "428917"
label = "Office"
handsets = ["office"]
voicemail = "office"
afterhours = ["nights", "weekend-days"]
`

// weekday is a local time in the week of Sunday 2026-07-05.
func weekday(day time.Weekday, hh, mm int) time.Time {
	return time.Date(2026, 7, 5, hh, mm, 0, 0, time.Local).AddDate(0, 0, int(day-time.Sunday))
}

func officeExtension(t *testing.T, src string) ResolvedExtension {
	t.Helper()
	e, ok := mustPolicy(t, src).LookupExtension("428917")
	if !ok {
		t.Fatal("extension missing")
	}
	return e
}

func TestAfterhoursListIsTheUnionOfItsSchedules(t *testing.T) {
	e := officeExtension(t, businessHours)
	if got := e.Afterhours.IDs(); strings.Join(got, ",") != "nights,weekend-days" {
		t.Fatalf("ids = %q, want both, in the order written", got)
	}
	cases := []struct {
		t    time.Time
		want bool
		why  string
	}{
		{weekday(time.Friday, 17, 0), true, "Friday 17:00 — the night window opens"},
		{weekday(time.Friday, 16, 59), false, "Friday 16:59 — still business hours"},
		{weekday(time.Saturday, 12, 0), true, "Saturday noon — the weekend-days window"},
		{weekday(time.Saturday, 8, 0), true, "Saturday 08:00 — Friday's night window"},
		{weekday(time.Sunday, 17, 0), true, "Sunday 17:00 — weekend days hand over to the night"},
		{weekday(time.Monday, 8, 59), true, "Monday 08:59 — Sunday's night window"},
		{weekday(time.Monday, 9, 0), false, "Monday 09:00 — open"},
		{weekday(time.Wednesday, 12, 0), false, "Wednesday noon — open"},
	}
	for _, c := range cases {
		if got := e.Afterhours.Active(c.t); got != c.want {
			t.Errorf("Active(%s) = %v, want %v — %s", c.t.Format("Mon 15:04"), got, c.want, c.why)
		}
	}
	if got := e.Afterhours.Describe(); got != "17:00–09:00 every day; 09:00–17:00 SU SA" {
		t.Errorf("Describe = %q", got)
	}
}

// One id and a list holding only that id are the same configuration, and a
// file that has always loaded must keep resolving exactly as it did.
func TestAfterhoursOneIDAndAListOfOneAgree(t *testing.T) {
	one := officeExtension(t, strings.Replace(businessHours,
		`afterhours = ["nights", "weekend-days"]`, `afterhours = "nights"`, 1))
	list := officeExtension(t, strings.Replace(businessHours,
		`afterhours = ["nights", "weekend-days"]`, `afterhours = ["nights"]`, 1))
	for _, e := range []ResolvedExtension{one, list} {
		if len(e.Afterhours) != 1 || e.Afterhours[0].ScheduleID != "nights" || !e.Afterhours.Live() {
			t.Fatalf("afterhours = %+v", e.Afterhours)
		}
		if e.Afterhours.Active(weekday(time.Saturday, 12, 0)) {
			t.Error("Saturday noon is outside the night window alone")
		}
		if !e.Afterhours.Active(weekday(time.Friday, 17, 0)) {
			t.Error("Friday 17:00 is inside the night window")
		}
	}
}

func TestAfterhoursListWithOneScheduleSwitchedOff(t *testing.T) {
	src := strings.Replace(businessHours, `id = "weekend-days"`, "id = \"weekend-days\"\nenabled = false", 1)
	e := officeExtension(t, src)
	if len(e.Afterhours) != 2 || e.Afterhours[1].Window != nil || !e.Afterhours.Live() {
		t.Fatalf("afterhours = %+v, want weekend-days present and inert", e.Afterhours)
	}
	if e.Afterhours.Active(weekday(time.Saturday, 12, 0)) {
		t.Error("Saturday noon should ring with weekend-days switched off")
	}
	if !e.Afterhours.Active(weekday(time.Friday, 17, 0)) {
		t.Error("the night window should still apply")
	}
	if got := e.Afterhours.Describe(); got != "17:00–09:00 every day" {
		t.Errorf("Describe = %q, want only the live window", got)
	}
}

// The holiday switch, for a line that names several schedules: every one off
// and the line rings at any hour — and, as for a single switched-off
// schedule, the file needs no mailbox to load.
func TestAfterhoursListAllSwitchedOffNeverQuiet(t *testing.T) {
	src := strings.Replace(businessHours, `id = "weekend-days"`, "id = \"weekend-days\"\nenabled = false", 1)
	src = strings.Replace(src, `id = "nights"`, "id = \"nights\"\nenabled = false", 1)
	src = strings.Replace(src, `voicemail = "office"`, "", 1)
	e := officeExtension(t, src)
	if e.Afterhours.Live() || len(e.Afterhours) != 2 {
		t.Fatalf("afterhours = %+v, want both named and both inert", e.Afterhours)
	}
	for day := time.Sunday; day <= time.Saturday; day++ {
		for hh := 0; hh < 24; hh++ {
			if at := weekday(day, hh, 30); e.Afterhours.Active(at) {
				t.Fatalf("Active(%s) with every schedule switched off", at.Format("Mon 15:04"))
			}
		}
	}
}

func TestAfterhoursListIsValidated(t *testing.T) {
	cases := []struct {
		name, replacement, want string
	}{
		{"unknown id", `afterhours = ["nights", "summer"]`, `extension "Office" references unknown schedule "summer"`},
		{"duplicate id", `afterhours = ["nights", "weekend-days", "nights"]`, `extension "Office" names afterhours schedule "nights" twice`},
		{"empty list", `afterhours = []`, `afterhours = [] names no schedule`},
		{"not a string", `afterhours = ["nights", 9]`, `afterhours must list [[schedules]] ids`},
		{"no mailbox", "afterhours = [\"nights\", \"weekend-days\"]\nvoicemail = \"\"", "no voicemail and no afterhours_ring"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src := strings.Replace(businessHours, `afterhours = ["nights", "weekend-days"]`, c.replacement, 1)
			if c.name == "no mailbox" {
				src = strings.Replace(src, `voicemail = "office"`, "", 1)
			}
			_, err := FromTOML([]byte(src))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("err = %v, want %q", err, c.want)
			}
		})
	}
}

func TestAfterhoursRingAppliesToAList(t *testing.T) {
	src := strings.Replace(businessHours, `voicemail = "office"`, `afterhours_ring = ["office"]`, 1)
	e := officeExtension(t, src)
	if len(e.AfterhoursPlan.Steps) != 1 || e.AfterhoursPlan.Steps[0].Endpoints[0] != "PJSIP/office" {
		t.Errorf("afterhours plan = %+v", e.AfterhoursPlan)
	}
}

// The bootstrap fallback runs a refused file under the v0.15 rules, which
// named one schedule: a list must not load there, or the fallback would be
// looser than the release it stands in for.
func TestPreviousRulesRefuseAnAfterhoursList(t *testing.T) {
	_, err := fromSplitTOML([]byte(businessHours), nil, Options{previousRules: true})
	if err == nil || !strings.Contains(err.Error(), "afterhours is now a named schedule") {
		t.Errorf("err = %v", err)
	}
	one := strings.Replace(businessHours, `afterhours = ["nights", "weekend-days"]`, `afterhours = "nights"`, 1)
	if _, err := fromSplitTOML([]byte(one), nil, Options{previousRules: true}); err != nil {
		t.Errorf("one id under the previous rules: %v", err)
	}
}
