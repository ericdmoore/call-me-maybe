package main

import (
	"bytes"
	"log/slog"
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

// allowGrandma is a policy whose [[people]] holds the mobile someone is about
// to put in a block source.
const allowGrandma = `
[house]
handsets = ["kitchen"]

[[handsets]]
id = "kitchen"
endpoint = "PJSIP/kitchen"

[[people]]
name = "Grandma"
numbers = ["512-555-0101"]

[[extensions]]
pin = "428917"
label = "Kitchen"
handsets = ["kitchen"]
`

// allowLists compiles policy sources into the shape printContacts takes: the
// hand-typed lists the address books have to be weighed against.
func allowLists(t *testing.T, sources ...string) []allowList {
	t.Helper()
	out := make([]allowList, 0, len(sources))
	for i, src := range sources {
		p, err := policy.FromTOML([]byte(src))
		if err != nil {
			t.Fatalf("policy %d: %v", i, err)
		}
		out = append(out, allowList{"policy.toml", p})
	}
	return out
}

// contactsInventory writes address books plus a contacts.toml naming them, and
// returns the inventory's path.
func contactsInventory(t *testing.T, sources string, files map[string]string) string {
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
	return path
}

// contactSet compiles those files the way `doorman check` does.
func contactSet(t *testing.T, sources string, files map[string]string) *contacts.Set {
	t.Helper()
	c, err := policy.LoadContacts(contactsInventory(t, sources, files))
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
	out = capture(t, func() { ok = printContacts(absent, allowLists(t, allowGrandma)) })
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
		if !printContacts(set, nil) {
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
		// And what the lobby now does with all of it. This report described a
		// set nothing consulted for exactly one release; the wording has to
		// keep up, because an operator acts on it.
		"The ladder the lobby walks",
		"stays the deliberate list",
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
	out := capture(t, func() { printContacts(set, nil) })

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
	out := capture(t, func() { printContacts(set, nil) })

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
	out := capture(t, func() { ok = printContacts(set, nil) })
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
	out := capture(t, func() { ok = printContacts(set, nil) })
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
	out := capture(t, func() { printContacts(set, nil) })

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

// The contradiction. Block beats [[people]] when the phone rings, and that is
// exactly why this is an error rather than a silent ranking: somebody typed the
// allow-list entry on purpose and an address book is overruling them.
func TestCheckFailsWhenANumberIsBothAllowListedAndBlocked(t *testing.T) {
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
		// Grandma's mobile, on the block list as well as in [[people]].
		"blocked.vcf": blockedVCards + "BEGIN:VCARD\nVERSION:3.0\nFN:Gran\nTEL;TYPE=CELL:+15125550101\nEND:VCARD\n",
	})

	var ok bool
	out := capture(t, func() { ok = printContacts(set, allowLists(t, allowGrandma)) })
	if ok {
		t.Error("check passed with a number that is both allow-listed and blocked")
	}
	for _, want := range []string{
		"both allow-listed and blocked",
		// The operator's own word for this caller, from their own policy file,
		// because "1 contradiction" is not something anybody can act on.
		"Grandma", "policy.toml",
		// The number, narrowed to the shape every log line uses.
		"+1512•••0101",
		"Remove them from the block source",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the contradiction report should mention %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "+15125550101") {
		t.Errorf("check printed a full caller ID:\n%s", out)
	}
}

// The same allow-list against a block source that does not name it: no
// contradiction, no error, nothing printed about one.
func TestCheckIsQuietWhenTheAllowListAndTheBlockListAgree(t *testing.T) {
	set := contactSet(t, `
[[sources]]
id = "nuisance"
path = "$DIR/blocked.vcf"
kind = "block"
`, map[string]string{"blocked.vcf": blockedVCards})

	var ok bool
	out := capture(t, func() { ok = printContacts(set, allowLists(t, allowGrandma)) })
	if !ok {
		t.Error("check failed with no contradiction to report")
	}
	if strings.Contains(out, "allow-listed and blocked") {
		t.Errorf("check invented a contradiction:\n%s", out)
	}
}

// ── the daemon side ──────────────────────────────────────────────────────

// The compatibility gate, where the daemon meets it: no contacts.toml is a nil
// lookup and not one line of log. A typed nil would satisfy lobby.Contacts and
// turn the state machine's single nil check into a lookup that always misses —
// the same behaviour by accident instead of by design.
func TestOpenContactsIsNilAndSilentWithoutAnInventory(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	book := openContacts(filepath.Join(t.TempDir(), "contacts.toml"), "1", allowLists(t, allowGrandma), log)

	if book != nil {
		t.Errorf("openContacts returned %#v, want an untyped nil", book)
	}
	if buf.Len() > 0 {
		t.Errorf("the daemon said something about contacts with no contacts.toml:\n%s", buf.String())
	}
}

// The whole translation, end to end: files on disk become the three fields the
// ladder turns on.
func TestOpenContactsAnswersTheLaddersQuestion(t *testing.T) {
	path := contactsInventory(t, `
[[sources]]
id = "eric"
path = "$DIR/eric.vcf"

[[sources]]
id = "nuisance"
path = "$DIR/blocked.vcf"
kind = "block"
`, map[string]string{"eric.vcf": someVCards, "blocked.vcf": blockedVCards})

	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	book := openContacts(path, "1", nil, log)
	if book == nil {
		t.Fatal("openContacts returned nil with an inventory on disk")
	}

	for _, c := range []struct {
		number    string
		found     bool
		name      string
		published bool
		blocked   bool
	}{
		{"+15125550101", true, "Grandma Mertaugh", false, false}, // a mobile, no ORG
		{"+15125550103", true, "Kitchen Sink Plumbing", true, false},
		{"+18005550104", true, "Kitchen Sink Plumbing", true, false},
		{"+15125550150", true, "Solar Panel Robocall", true, true},
		{"+15125559999", false, "", false, false},
		{"", false, "", false, false},
	} {
		got, ok := book.Lookup(c.number)
		if ok != c.found {
			t.Errorf("Lookup(%s) found = %v, want %v", c.number, ok, c.found)
			continue
		}
		if got.Name != c.name || got.Published != c.published || got.Blocked != c.blocked {
			t.Errorf("Lookup(%s) = %+v, want name %q published %v blocked %v",
				c.number, got, c.name, c.published, c.blocked)
		}
	}

	// Counts at startup, and nothing else: an address book is the most personal
	// data on this box and the daemon's log is not where it goes.
	out := buf.String()
	if !strings.Contains(out, "contacts loaded") {
		t.Errorf("the daemon said nothing about the address books it read:\n%s", out)
	}
	for _, secret := range []string{"Grandma", "Mertaugh", "Solar Panel", "5125550101", "5125550150"} {
		if strings.Contains(out, secret) {
			t.Errorf("the startup log carried contact data %q:\n%s", secret, out)
		}
	}
}

// An inventory that will not load is a warning and never fatal. The phone must
// keep answering, and this file has no say in whether it can — but a block list
// that silently stopped blocking is exactly the kind of failure that looks like
// working software, so it is said out loud.
func TestOpenContactsWarnsAndCarriesOnWhenTheInventoryIsInvalid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "contacts.toml")
	if err := os.WriteFile(path, []byte("[[sources]]\nid = \"eric\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	if book := openContacts(path, "1", nil, log); book != nil {
		t.Error("an invalid inventory produced a lookup")
	}
	if !strings.Contains(buf.String(), "the phone is unaffected") {
		t.Errorf("the warning did not say the phone keeps answering:\n%s", buf.String())
	}
}

// A source that cannot be read means people quietly lose admission, which is
// the failure mode with no symptom. Warned about, per source, and the rest of
// the books are unaffected.
func TestOpenContactsWarnsAboutASourceItCannotRead(t *testing.T) {
	path := contactsInventory(t, `
[[sources]]
id = "eric"
path = "$DIR/eric.vcf"

[[sources]]
id = "gone"
path = "$DIR/not-there.vcf"
`, map[string]string{"eric.vcf": someVCards})

	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	book := openContacts(path, "1", nil, log)
	if book == nil {
		t.Fatal("one unreadable source took the whole address book down")
	}
	if _, ok := book.Lookup("+15125550101"); !ok {
		t.Error("the readable source stopped contributing")
	}
	if !strings.Contains(buf.String(), "contributed nothing") {
		t.Errorf("the unreadable source was not reported:\n%s", buf.String())
	}
}

// The contradiction is loud at startup as well as in `doorman check`, because
// from the allow-list it is invisible: the operator wrote this caller down on
// purpose and a block source is overruling them.
func TestOpenContactsWarnsWhenABlockOverrulesTheAllowList(t *testing.T) {
	path := contactsInventory(t, `
[[sources]]
id = "nuisance"
path = "$DIR/blocked.vcf"
kind = "block"
`, map[string]string{
		"blocked.vcf": blockedVCards + "BEGIN:VCARD\nVERSION:3.0\nFN:Gran\nTEL;TYPE=CELL:+15125550101\nEND:VCARD\n",
	})

	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	openContacts(path, "1", allowLists(t, allowGrandma), log)

	out := buf.String()
	if !strings.Contains(out, "both allow-listed and blocked") {
		t.Errorf("the contradiction was not warned about:\n%s", out)
	}
	// Invariant 1: the number is redacted even here, where the operator's own
	// allow-list name is not.
	if !strings.Contains(out, "+1512•••0101") || strings.Contains(out, "+15125550101") {
		t.Errorf("the warning did not redact the number:\n%s", out)
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
