package policy

import (
	"strings"
	"testing"
)

const splitHandsets = `
[[handsets]]
id = "kitchen"
endpoint = "PJSIP/kitchen"
number = 101
page = true
mailbox = "adults"
password_env = "HANDSET_KITCHEN_PASSWORD"

[[handsets]]
id = "kids-room"
endpoint = "PJSIP/kids-room"
number = 105
page = true
password_env = "HANDSET_KIDS_ROOM_PASSWORD"

[[groups]]
id = "adults"
handsets = ["kitchen"]
`

const splitPolicy = `
[house]
handsets = ["adults"]
voicemail = "family"

[[schedules]]
id = "school-night"
start = "20:30"
end = "07:00"
days = ["SU", "MO", "TU", "WE", "TH"]

[[people]]
name = "Grandma"
numbers = ["512-555-0100"]

[[extensions]]
pin = "428917"
label = "Kids"
handsets = ["kids-room"]
voicemail = "kids"
afterhours = "school-night"
`

func TestSplitLayoutMergesAndCompiles(t *testing.T) {
	p, err := fromSplitTOML([]byte(splitPolicy), []byte(splitHandsets), Options{})
	if err != nil {
		t.Fatalf("fromSplitTOML: %v", err)
	}
	if _, ok := p.LookupCaller("+15125550100"); !ok {
		t.Error("allow-list lost in merge")
	}
	e, ok := p.LookupExtension("428917")
	if !ok || e.Plan.Steps[0].Endpoints[0] != "PJSIP/kids-room" {
		t.Errorf("extension = %+v, %v", e, ok)
	}
	if e.Afterhours == nil {
		t.Error("schedule reference not resolved across files")
	}
	if got := p.HousePlan().Steps[0].Endpoints; len(got) != 1 || got[0] != "PJSIP/kitchen" {
		t.Errorf("house group not resolved from handsets file: %v", got)
	}
}

func TestSplitLayoutRejectsSectionsInTheWrongFile(t *testing.T) {
	wrong := splitPolicy + "\n[[handsets]]\nid = \"rogue\"\nendpoint = \"PJSIP/rogue\"\n"
	if _, err := fromSplitTOML([]byte(wrong), []byte(splitHandsets), Options{}); err == nil ||
		!strings.Contains(err.Error(), "handsets.toml") {
		t.Errorf("err = %v, want move-to-handsets hint", err)
	}

	wrongH := splitHandsets + "\n[[extensions]]\npin = \"111111\"\nlabel = \"Rogue\"\nhandsets = [\"kitchen\"]\n"
	if _, err := fromSplitTOML([]byte(splitPolicy), []byte(wrongH), Options{}); err == nil ||
		!strings.Contains(err.Error(), "policy.toml") {
		t.Errorf("err = %v, want move-to-policy hint", err)
	}
}

func TestLegacySingleFileStillWorks(t *testing.T) {
	if _, err := fromSplitTOML([]byte(ladderPolicy), nil, Options{}); err != nil {
		t.Fatalf("legacy layout broke: %v", err)
	}
}

func TestHandsetNumberValidation(t *testing.T) {
	dup := strings.Replace(splitHandsets, "number = 105", "number = 101", 1)
	if _, err := fromSplitTOML([]byte(splitPolicy), []byte(dup), Options{}); err == nil ||
		!strings.Contains(err.Error(), "share number") {
		t.Errorf("err = %v", err)
	}
	oob := strings.Replace(splitHandsets, "number = 105", "number = 500", 1)
	if _, err := fromSplitTOML([]byte(splitPolicy), []byte(oob), Options{}); err == nil ||
		!strings.Contains(err.Error(), "100-199") {
		t.Errorf("err = %v", err)
	}
}
