package lsp

import (
	"strings"
	"testing"
)

const contactsDoc = `[[sources]]
id = "eric"
path = "eric.vcf"

[[sources]]
id = "nuisance"
path = "blocked.vcf"
kind = "block"
`

// contacts.toml is a root struct of its own, and classifying it as policy
// produces the worst possible diagnostic: the misplaced-section hint would tell
// somebody editing contacts.toml that [[sources]] belongs in contacts.toml.
func TestKindOfRecognisesTheContactsInventory(t *testing.T) {
	if got := KindOf("/etc/call-me-maybe/contacts.toml"); got != KindContacts {
		t.Errorf("KindOf(contacts.toml) = %v, want KindContacts", got)
	}
}

func TestContactsAreLintedOnTheirOwn(t *testing.T) {
	if d := AnalyseContacts(contactsDoc); len(d) != 0 {
		t.Fatalf("diagnostics on a valid contacts.toml: %v", d)
	}

	broken := strings.Replace(contactsDoc, "kind =", "kinds =", 1)
	d := AnalyseContacts(broken)
	if len(d) == 0 {
		t.Fatal("a misspelled key should be reported")
	}
	if !strings.Contains(d[0].Message, "kinds") {
		t.Errorf("diagnostic = %q", d[0].Message)
	}
	// The squiggle goes on the typo, which is the only place it helps.
	if line := strings.Split(broken, "\n")[d[0].Range.Start.Line]; !strings.Contains(line, "kinds") {
		t.Errorf("diagnostic placed on %q", line)
	}
}

// A policy.toml and a contacts.toml open together must each be linted against
// their own schema, and neither may claim the other's sections.
func TestAContactsBufferDoesNotDisturbThePolicyPair(t *testing.T) {
	if p, h := Analyse(policyDoc, ptrTo(handsetsDoc)); len(p) != 0 || len(h) != 0 {
		t.Fatalf("the fixture pair is not clean: %v %v", p, h)
	}
	if d := AnalyseContacts(contactsDoc); len(d) != 0 {
		t.Errorf("contacts diagnostics: %v", d)
	}
}

func ptrTo(s string) *string { return &s }
