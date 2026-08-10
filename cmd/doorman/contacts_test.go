package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"callmemaybe/internal/contacts"
	"callmemaybe/internal/policy"
)

// A small address book, written the way an export would be. 555-01xx is
// reserved for fiction, which is what test fixtures in this repo use.
const someVCards = `BEGIN:VCARD
VERSION:3.0
FN:Grandma Mertaugh
N:Mertaugh;Grandma;;;
TEL;TYPE=CELL:+1 (512) 555-0101
END:VCARD
BEGIN:VCARD
VERSION:3.0
FN:Kitchen Sink Plumbing
ORG:Kitchen Sink Plumbing
TEL;TYPE=WORK:512-555-0103
TEL;TYPE=MAIN:800-555-0104
END:VCARD
BEGIN:VCARD
VERSION:3.0
FN:Shortcode
N:;;;;
TEL;TYPE=CELL:22000
END:VCARD
`

const blockedVCards = `BEGIN:VCARD
VERSION:3.0
FN:Solar Panel Robocall
N:;;;;
TEL;TYPE=VOICE:512-555-0150
END:VCARD
`

// contactSet writes address books plus a contacts.toml naming them, and
// compiles the result the way `doorman check` does.
func contactSet(t *testing.T, sources string, files map[string]string) *contacts.Set {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(dir, "contacts.toml")
	if err := os.WriteFile(path, []byte(strings.ReplaceAll(sources, "$DIR", dir)), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := policy.LoadContacts(path)
	if err != nil {
		t.Fatalf("LoadContacts: %v", err)
	}
	return contacts.Load(c, "1")
}

// The compatibility gate for the whole milestone, stated as a test: with no
// contacts.toml, `doorman check` says nothing about contacts at all.
func TestCheckSaysNothingAboutContactsWithoutAnInventory(t *testing.T) {
	absent := contacts.Load(&policy.Contacts{}, "1")
	var out string
	ok := true
	out = capture(t, func() { ok = printContacts(absent) })
	if out != "" {
		t.Errorf("check spoke about contacts with no contacts.toml:\n%s", out)
	}
	if !ok {
		t.Error("an absent inventory failed the check")
	}
}

func TestCheckReportsWhatEachSourceContributed(t *testing.T) {
	set := contactSet(t, `
[[sources]]
id = "eric"
path = "$DIR/eric.vcf"

[[sources]]
id = "nuisance"
path = "$DIR/blocked.vcf"
kind = "block"
`, map[string]string{"eric.vcf": someVCards, "blocked.vcf": blockedVCards})

	out := capture(t, func() {
		if !printContacts(set) {
			t.Error("a readable inventory failed the check")
		}
	})

	for _, want := range []string{
		"Contacts: 2 sources",
		"eric", "admit", "nuisance", "block",
		// The four counts the milestone is specified in terms of.
		"personal", "published", "blocked", "skipped",
		// And why the published ones are published, so an operator can see the
		// classifier's reasoning rather than take it on faith.
		"a business (ORG is set)", "toll-free",
		// The rule the whole feature turns on, where somebody will read it.
		"look the number up",
		// Honest about what this does today.
		"Nothing on the call path reads this yet",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the contacts block should mention %q:\n%s", want, out)
		}
	}
}

// Invariant 1's spirit on an operator's terminal: counts, never a name and
// never a number. An address book is the most personal data this project
// touches, and "how many" answers every question `doorman check` exists to
// answer.
func TestCheckNeverPrintsAContactsNameOrNumber(t *testing.T) {
	set := contactSet(t, "[[sources]]\nid = \"eric\"\npath = \"$DIR/eric.vcf\"\n",
		map[string]string{"eric.vcf": someVCards})
	out := capture(t, func() { printContacts(set) })

	for _, secret := range []string{
		"Grandma", "Mertaugh", "Kitchen Sink Plumbing",
		"5550101", "555-0101", "5125550103", "8005550104",
	} {
		if strings.Contains(out, secret) {
			t.Errorf("check printed contact data %q:\n%s", secret, out)
		}
	}
}

func TestCheckCountsWhatTheParserSkipped(t *testing.T) {
	set := contactSet(t, "[[sources]]\nid = \"eric\"\npath = \"$DIR/eric.vcf\"\n",
		map[string]string{"eric.vcf": someVCards + "this line is not a property\n"})
	out := capture(t, func() { printContacts(set) })

	// One short code that will not normalise, and one line of prose.
	if !strings.Contains(out, "Not understood") {
		t.Errorf("the parser's leftovers were not reported:\n%s", out)
	}
	if !strings.Contains(out, "not property lines") && !strings.Contains(out, "not properties") {
		t.Errorf("malformed lines were not reported:\n%s", out)
	}
}

// A path an operator declared and doorman cannot read is a mistake they can
// fix, and `doorman check` is the command whose whole job is finding those.
func TestCheckFailsOnASourceItCannotRead(t *testing.T) {
	set := contactSet(t, "[[sources]]\nid = \"eric\"\npath = \"$DIR/not-there.vcf\"\n", nil)
	var ok bool
	out := capture(t, func() { ok = printContacts(set) })
	if ok {
		t.Error("check passed with a source it could not read")
	}
	if !strings.Contains(out, "contributes nothing") {
		t.Errorf("the unreadable source was not explained:\n%s", out)
	}
	// It is reported, never fatal to the phone: the wording has to say so.
	if !strings.Contains(out, "keeps") || !strings.Contains(out, "never the authority") {
		t.Errorf("check did not say the phone is unaffected:\n%s", out)
	}
}

// A url source is not somebody's mistake — it is a key this release reserves.
// Reported, and not a failure.
func TestCheckReportsAURLSourceWithoutFailing(t *testing.T) {
	set := contactSet(t, "[[sources]]\nid = \"shared\"\nurl = \"https://example.com/shared.vcf\"\n", nil)
	var ok bool
	out := capture(t, func() { ok = printContacts(set) })
	if !ok {
		t.Error("a reserved url source failed the check")
	}
	if !strings.Contains(out, "not fetched yet") {
		t.Errorf("the url source was not explained:\n%s", out)
	}
	// The URL is safe to print precisely because the credential is not in it.
	if !strings.Contains(out, "https://example.com/shared.vcf") {
		t.Errorf("the source's URL was not shown:\n%s", out)
	}
}

func TestCheckReportsConflictsBetweenSources(t *testing.T) {
	set := contactSet(t, `
[[sources]]
id = "eric"
path = "$DIR/eric.vcf"

[[sources]]
id = "nuisance"
path = "$DIR/blocked.vcf"
kind = "block"
`, map[string]string{
		"eric.vcf": someVCards,
		// The same mobile that is personal in the admit source.
		"blocked.vcf": blockedVCards + "BEGIN:VCARD\nVERSION:3.0\nFN:Grandma\nTEL;TYPE=CELL:+15125550101\nEND:VCARD\n",
	})
	out := capture(t, func() { printContacts(set) })

	for _, want := range []string{
		"Conflicts, resolved conservatively",
		"in both an admit source and a block source",
		"blocked won",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("check should report %q:\n%s", want, out)
		}
	}
}

func TestContactsPathFallsBackToTheEnvironmentThenTheConvention(t *testing.T) {
	t.Setenv("CONTACTS_PATH", "")
	if got := contactsPathArg(""); got != "./contacts.toml" {
		t.Errorf("default = %q", got)
	}
	t.Setenv("CONTACTS_PATH", "/etc/call-me-maybe/contacts.toml")
	if got := contactsPathArg(""); got != "/etc/call-me-maybe/contacts.toml" {
		t.Errorf("from the environment = %q", got)
	}
	if got := contactsPathArg("/tmp/other.toml"); got != "/tmp/other.toml" {
		t.Errorf("the flag should win: %q", got)
	}
}

func TestPlural(t *testing.T) {
	for _, c := range []struct {
		n    int
		word string
		want string
	}{
		{1, "source", "source"},
		{2, "source", "sources"},
		{0, "card", "cards"},
		{2, "address", "addresses"},
	} {
		if got := plural(c.n, c.word); got != c.want {
			t.Errorf("plural(%d, %q) = %q, want %q", c.n, c.word, got, c.want)
		}
	}
}
