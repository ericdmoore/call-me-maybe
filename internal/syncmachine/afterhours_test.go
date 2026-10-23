package syncmachine

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

const businessHoursPolicy = `[house]
handsets = ["kitchen"]

[[schedules]]
id = "nights"
start = "17:00"
end = "09:00"

[[schedules]]
id = "weekend-days"
start = "09:00"
end = "17:00"
days = ["SA", "SU"]

[[extensions]]
label = "Office"
pin = "846213"
handsets = ["kitchen"]
voicemail = "office"
afterhours = AFTERHOURS
`

// One schedule id goes on the wire as the string it has always been — in the
// extension summary and in the policy values — whichever way the file spells
// it, so a box that never writes a list sends exactly what it sent before,
// and a list of one is not refused by a service that predates lists. Several
// ids go as a list.
func TestAfterhoursOnTheWire(t *testing.T) {
	cases := []struct {
		spelling string
		want     string
	}{
		{`"nights"`, `"afterhours":"nights"`},
		{`["nights"]`, `"afterhours":"nights"`},
		{`["nights", "weekend-days"]`, `"afterhours":["nights","weekend-days"]`},
	}
	revisions := map[string]string{}
	for _, c := range cases {
		t.Run(c.spelling, func(t *testing.T) {
			m, p, _ := fixture(t)
			src := strings.Replace(businessHoursPolicy, "AFTERHOURS", c.spelling, 1)
			if err := os.WriteFile(p.Policy, []byte(src), 0o600); err != nil {
				t.Fatal(err)
			}
			snap, err := m.Observe()
			if err != nil {
				t.Fatalf("observe: %v", err)
			}
			data, _ := json.Marshal(snap.Configuration)
			if n := bytes.Count(data, []byte(c.want)); n != 2 {
				t.Errorf("%s appears %d times, want twice (summary and values):\n%s", c.want, n, data)
			}
			revisions[c.spelling] = snap.Revision
		})
	}
	if revisions[`"nights"`] != revisions[`["nights"]`] {
		t.Error("two spellings of one schedule should be one snapshot")
	}
}
