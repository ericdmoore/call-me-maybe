package policy

import (
	"strings"
	"testing"
)

func TestNormaliseMACAcceptsEveryWrittenForm(t *testing.T) {
	for _, in := range []string{
		"ec:74:d7:88:a2:54", "EC:74:D7:88:A2:54", "EC-74-D7-88-A2-54",
		"EC74D788A254", "ec74.d788.a254", " ec 74 d7 88 a2 54 ",
	} {
		got, ok := NormaliseMAC(in)
		if !ok || got != "ec:74:d7:88:a2:54" {
			t.Errorf("NormaliseMAC(%q) = %q, %v", in, got, ok)
		}
	}
	for _, bad := range []string{"", "ec:74:d7:88:a2", "ec:74:d7:88:a2:54:00", "zz:74:d7:88:a2:54", "kitchen"} {
		if _, ok := NormaliseMAC(bad); ok {
			t.Errorf("NormaliseMAC(%q) accepted", bad)
		}
	}
	if MACFilename("ec:74:d7:88:a2:54") != "ec74d788a254" {
		t.Error("MACFilename should strip separators")
	}
}

const provisioningPolicy = `
[house]
handsets = ["kitchen"]
`

func lintHandsets(t *testing.T, handsets string, o Options) []string {
	t.Helper()
	return LintSplitWith([]byte(provisioningPolicy), []byte(handsets), o)
}

func TestMACAndModelTravelTogether(t *testing.T) {
	problems := lintHandsets(t, `
[[handsets]]
id = "kitchen"
endpoint = "PJSIP/kitchen"
mac = "EC74D788A254"
`, Options{})
	if len(problems) != 1 || !strings.Contains(problems[0], "travel together") {
		t.Fatalf("mac without model should be exactly one problem, got %v", problems)
	}
	problems = lintHandsets(t, `
[[handsets]]
id = "kitchen"
endpoint = "PJSIP/kitchen"
mac = "EC74D788A254"
model = "grandstream-wp826"
`, Options{})
	if len(problems) != 0 {
		t.Fatalf("a complete pair is valid, got %v", problems)
	}
}

func TestDuplicateAndMalformedMACsAreRefused(t *testing.T) {
	problems := lintHandsets(t, `
[[handsets]]
id = "kitchen"
endpoint = "PJSIP/kitchen"
mac = "ec:74:d7:88:a2:54"
model = "grandstream-wp826"

[[handsets]]
id = "theater"
endpoint = "PJSIP/theater"
mac = "EC-74-D7-88-A2-54"
model = "grandstream-wp826"

[[handsets]]
id = "hall"
endpoint = "PJSIP/hall"
mac = "not-a-mac"
model = "grandstream-wp826"
`, Options{})
	joined := strings.Join(problems, "\n")
	if !strings.Contains(joined, `share mac ec:74:d7:88:a2:54`) {
		t.Errorf("the same MAC in two spellings should be one duplicate, got %v", problems)
	}
	if !strings.Contains(joined, `"hall" mac "not-a-mac" is not a MAC address`) {
		t.Errorf("malformed MAC should be refused, got %v", problems)
	}
}

func TestUnknownModelIsRefusedOnlyWhenTheCallerKnowsModels(t *testing.T) {
	inv := `
[[handsets]]
id = "kitchen"
endpoint = "PJSIP/kitchen"
mac = "EC74D788A254"
model = "grandstrem-wp826"
`
	// The daemon passes no ModelKnown: a well-formed id is accepted, because
	// a phone that registered by hand is still a phone.
	if problems := lintHandsets(t, inv, Options{}); len(problems) != 0 {
		t.Fatalf("without ModelKnown any well-formed model must pass, got %v", problems)
	}
	known := func(id string) (bool, string) {
		if id == "grandstream-wp826" {
			return true, ""
		}
		return false, "grandstream-wp826"
	}
	problems := lintHandsets(t, inv, Options{ModelKnown: known})
	if len(problems) != 1 || !strings.Contains(problems[0], `did you mean "grandstream-wp826"`) {
		t.Fatalf("check should refuse with the suggestion, got %v", problems)
	}
	if problems := lintHandsets(t, strings.Replace(inv, "grandstrem", "grandstream", 1), Options{ModelKnown: known}); len(problems) != 0 {
		t.Fatalf("a known model passes, got %v", problems)
	}
	if problems := lintHandsets(t, strings.Replace(inv, "grandstrem-wp826", "Grandstream WP826", 1), Options{}); len(problems) != 1 || !strings.Contains(problems[0], "lowercase") {
		t.Fatalf("a badly shaped id is refused by the loader itself, got %v", problems)
	}
}

func TestPhonebookSelectionShapeAndDefault(t *testing.T) {
	problems := lintHandsets(t, `
[[handsets]]
id = "kitchen"
endpoint = "PJSIP/kitchen"
phonebook = ["house", "people", "house", "Eric's iCloud"]
`, Options{})
	joined := strings.Join(problems, "\n")
	if !strings.Contains(joined, `lists phonebook "house" twice`) || !strings.Contains(joined, `"Eric's iCloud" must be house, people, or a contacts.toml source id`) {
		t.Fatalf("expected a duplicate and a shape problem, got %v", problems)
	}
	h := Handset{ID: "kitchen"}
	if got := h.Books(); strings.Join(got, ",") != "house,people" {
		t.Errorf("default phonebook should be house,people, got %v", got)
	}
	h.Phonebook = []string{"house"}
	if got := h.Books(); strings.Join(got, ",") != "house" {
		t.Errorf("an explicit selection is returned as given, got %v", got)
	}
}
