package provision

import "testing"

func TestLookupAndKnown(t *testing.T) {
	if _, ok := Lookup("grandstream-wp826"); !ok {
		t.Fatal("the first customer's phone is not a known model")
	}
	if known, _ := Known("grandstream-wp826"); !known {
		t.Fatal("Known disagrees with Lookup")
	}
}

func TestSuggestionsAreOfferedOnlyWhenProbablyRight(t *testing.T) {
	cases := map[string]string{
		"wp826":             "grandstream-wp826", // bare model number
		"grandstream-wp862": "grandstream-wp826", // transposition
		"grandstrem-wp826":  "grandstream-wp826", // typo
		"toaster":           "",                  // nothing like it
		"GRANDSTREAM-WP826": "grandstream-wp826", // case
	}
	for in, want := range cases {
		if got := Suggest(in); got != want {
			t.Errorf("Suggest(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEveryModelHasAVendorAndName(t *testing.T) {
	for _, m := range Models() {
		if m.Vendor == "" || m.Name == "" || m.ID == "" {
			t.Errorf("model %+v is missing a field", m)
		}
		if m.Templated && m.Family == "" {
			t.Errorf("model %s claims a template but names no family", m.ID)
		}
	}
}
