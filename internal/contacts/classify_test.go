package contacts

import (
	"strings"
	"testing"
)

// The classifier is the security boundary of this whole feature, so these tests
// are written from the rule rather than from the code: if a stranger can look
// the number up, it must not be automatic admission.

func TestPersonalNeedsEverySignal(t *testing.T) {
	person := card{name: "Grandma Mertaugh"}
	mobile := tel{types: []string{"cell", "voice", "pref"}}

	if class, reason := classify(person, mobile, "+15125550101"); class != Personal || reason != ReasonPersonal {
		t.Fatalf("a named card with a mobile and no ORG = %v (%v), want personal", class, reason)
	}

	// Remove any one of the three signals and it must fall back to published.
	t.Run("an organisation makes it findable", func(t *testing.T) {
		withOrg := card{name: person.name, org: "Kitchen Sink Plumbing"}
		if class, reason := classify(withOrg, mobile, "+15125550101"); class != Published || reason != ReasonOrg {
			t.Errorf("= %v (%v), want published because ORG is set", class, reason)
		}
	})
	t.Run("no name is no evidence", func(t *testing.T) {
		if class, reason := classify(card{}, mobile, "+15125550101"); class != Published || reason != ReasonUnclear {
			t.Errorf("= %v (%v), want published: an unnamed card cannot be vouched for", class, reason)
		}
	})
	t.Run("no telephone type is no evidence", func(t *testing.T) {
		if class, reason := classify(person, tel{}, "+15125550101"); class != Published || reason != ReasonUnclear {
			t.Errorf("= %v (%v), want published: an untyped number is ambiguous", class, reason)
		}
	})
}

func TestWorkNumbersArePublishedEvenWithoutAnOrg(t *testing.T) {
	// A colleague's desk line: a real person, a number on a directory page.
	person := card{name: "Dana Whitlock"}
	if class, reason := classify(person, tel{types: []string{"work", "voice"}}, "+15125550111"); class != Published || reason != ReasonWork {
		t.Errorf("= %v (%v), want published", class, reason)
	}
	for _, ty := range []string{"fax", "pager", "main"} {
		if class, _ := classify(person, tel{types: []string{ty}}, "+15125550111"); class != Published {
			t.Errorf("TYPE=%s classified as personal", ty)
		}
	}
}

// The restrictive reading wins a card that says both, which is what someone
// working from home looks like.
func TestWorkBeatsHomeOnTheSameNumber(t *testing.T) {
	c := card{name: "Dana Whitlock"}
	if class, reason := classify(c, tel{types: []string{"home", "work"}}, "+15125550112"); class != Published || reason != ReasonWork {
		t.Errorf("= %v (%v), want published", class, reason)
	}
}

func TestTollFreeIsNeverPersonal(t *testing.T) {
	// A named card, a mobile type, no ORG — every signal a person has, and it
	// still must not be admitted: an 800 number is bought to be published.
	c := card{name: "Reception"}
	mobile := tel{types: []string{"cell"}}
	for _, npa := range []string{"800", "888", "877", "866", "855", "844", "833"} {
		n := "+1" + npa + "5550199"
		if class, reason := classify(c, mobile, n); class != Published || reason != ReasonTollFree {
			t.Errorf("%s = %v (%v), want published", n, class, reason)
		}
	}
}

func TestTollFreeDetectionIsNarrow(t *testing.T) {
	// Deliberately only +1 numbers of the right length: "the digits look like
	// 800" is true of plenty of numbers elsewhere that are nothing of the sort.
	for _, n := range []string{"+15125550100", "+448005550199", "+1800555019", ""} {
		if tollFree(n) {
			t.Errorf("tollFree(%q) = true", n)
		}
	}
	if !tollFree("+18005550199") {
		t.Error("tollFree(+18005550199) = false")
	}
}

// The three spellings a mobile arrives as. Missing one would send a whole
// platform's contacts to the lobby.
func TestEveryMobileSpellingIsPersonal(t *testing.T) {
	c := card{name: "Britt Halvorsen"}
	for _, ty := range []string{"cell", "mobile", "iphone", "home"} {
		if class, _ := classify(c, tel{types: []string{ty}}, "+15125550106"); class != Personal {
			t.Errorf("TYPE=%s classified as published", ty)
		}
	}
}

// Published sorts after Personal, and merging takes the higher class. That
// ordering is what makes "the restrictive answer wins" true by construction
// rather than by a rule somebody has to remember to apply.
func TestPublishedOutranksPersonal(t *testing.T) {
	if !(Published > Personal) {
		t.Fatal("Class ordering reversed — merging two sources would now take the permissive answer")
	}
	if Personal.String() != "personal" || Published.String() != "published" {
		t.Errorf("Class strings changed: %q, %q", Personal, Published)
	}
}

func TestReasonsAreAllDescribed(t *testing.T) {
	for _, r := range []Reason{ReasonPersonal, ReasonOrg, ReasonWork, ReasonTollFree, ReasonUnclear} {
		if r.String() == "" {
			t.Errorf("reason %d has no description", r)
		}
	}
}

// Reasons is a fixed array indexed by Reason, so a new signal that nobody
// widened it for is an index off the end — a panic inside `doorman check`,
// which is not how anyone should learn that the classifier grew.
func TestReasonsArrayCoversEveryReason(t *testing.T) {
	for _, r := range []Reason{ReasonPersonal, ReasonOrg, ReasonWork, ReasonTollFree, ReasonUnclear} {
		if int(r) >= numReasons {
			t.Fatalf("Reason %d (%s) does not fit in Reasons — widen numReasons", r, r)
		}
	}
	if got := ReasonUnclear.String(); !strings.Contains(got, "unclear") {
		t.Errorf("the conservative default should read as unclear: %q", got)
	}
}
