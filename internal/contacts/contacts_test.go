package contacts

import (
	"os"
	"path/filepath"
	"testing"

	"callmemaybe/internal/policy"
)

func doc(t *testing.T, id, kind, file string) Document {
	t.Helper()
	return Document{ID: id, Kind: kind, Where: file, Data: fixture(t, file)}
}

func TestOneSourceIsCountedAndClassified(t *testing.T) {
	set := Compile([]Document{doc(t, "eric", policy.ContactAdmit, "icloud.vcf")}, "1")

	r := set.Sources()[0]
	if r.Cards != 4 {
		t.Errorf("cards = %d, want 4", r.Cards)
	}
	if r.Personal != 3 || r.Published != 3 {
		t.Errorf("personal = %d, published = %d; want 3 and 3", r.Personal, r.Published)
	}
	if r.Skipped != 0 {
		t.Errorf("skipped = %d, want 0", r.Skipped)
	}

	// One vCard with several numbers becomes several entries sharing a name.
	for _, n := range []string{"+15125550101", "+15125550102"} {
		e, ok := set.Lookup(n)
		if !ok {
			t.Fatalf("%s not in the set", n)
		}
		if e.Name != "Grandma Mertaugh" || e.Class != Personal {
			t.Errorf("%s = %+v", n, e)
		}
	}
	// The business is published, both its numbers, and the toll-free one says
	// why it is published rather than inheriting the ORG reason.
	if e, _ := set.Lookup("+15125550103"); e.Class != Published || e.Reason != ReasonOrg {
		t.Errorf("plumber's work line = %+v", e)
	}
	if e, _ := set.Lookup("+18005550104"); e.Reason != ReasonTollFree {
		t.Errorf("toll-free number = %+v", e)
	}
	// iCloud's custom-labelled number has no type at all: conservative.
	if e, _ := set.Lookup("+15125550105"); e.Class != Published || e.Reason != ReasonUnclear {
		t.Errorf("untyped number = %+v, want published/unclear", e)
	}
}

func TestNumbersThatWillNotNormaliseAreSkippedAndCounted(t *testing.T) {
	set := Compile([]Document{doc(t, "messy", policy.ContactAdmit, "messy.vcf")}, "1")
	r := set.Sources()[0]
	if r.Skipped != 2 {
		t.Errorf("skipped = %d, want 2 — a short code and a junk value", r.Skipped)
	}
	if set.Totals().Skipped != 2 {
		t.Errorf("totals.Skipped = %d", set.Totals().Skipped)
	}
	if r.Dropped != 2 || r.Malformed != 2 {
		t.Errorf("dropped = %d, malformed = %d; want 2 and 2", r.Dropped, r.Malformed)
	}
	// Silence would be the same defect as swallowing an unknown config key.
	if set.Len() != 1 {
		t.Errorf("set = %d numbers, want just the depot's work line", set.Len())
	}
}

// The key is the number, normalised. Two vCards carrying it are one person for
// admission purposes, whatever they are called.
func TestTheSameNumberInTwoSourcesIsOneEntry(t *testing.T) {
	set := Compile([]Document{
		doc(t, "eric", policy.ContactAdmit, "google.vcf"),
		doc(t, "becky", policy.ContactAdmit, "google.vcf"),
	}, "1")
	if set.Len() != 7 {
		t.Fatalf("distinct numbers = %d, want 7", set.Len())
	}
	if got := set.Totals().Shared; got != 7 {
		t.Errorf("shared = %d, want every number counted as shared", got)
	}
	// Counted once each in the merged totals, twice in the per-source ones.
	if p := set.Totals().Personal; p != 3 {
		t.Errorf("merged personal = %d, want 3", p)
	}
	if p := set.Sources()[0].Personal + set.Sources()[1].Personal; p != 6 {
		t.Errorf("per-source personal = %d, want 6", p)
	}
}

// The same number written three different ways is still one entry. This is why
// the key is E.164 and not the string the address book happened to hold.
func TestFormattingDoesNotProduceDuplicates(t *testing.T) {
	same := func(id, written string) Document {
		return Document{ID: id, Data: []byte(
			"BEGIN:VCARD\nVERSION:3.0\nFN:Grandma\nTEL;TYPE=CELL:" + written + "\nEND:VCARD\n")}
	}
	set := Compile([]Document{
		same("a", "512-555-0100"),
		same("b", "+1 (512) 555-0100"),
		same("c", "15125550100"),
	}, "1")
	if set.Len() != 1 {
		t.Fatalf("distinct numbers = %d (%v), want 1", set.Len(), set.Numbers())
	}
}

func TestConflictsResolveConservatively(t *testing.T) {
	first := Document{ID: "first", Data: []byte(
		"BEGIN:VCARD\nVERSION:3.0\nFN:Nell Ashby\nTEL;TYPE=CELL:+15125550180\nEND:VCARD\n")}
	second := Document{ID: "second", Data: []byte(
		"BEGIN:VCARD\nVERSION:3.0\nFN:N. Ashby (work)\nORG:Ashby Design\nTEL;TYPE=WORK:+15125550180\nEND:VCARD\n")}

	set := Compile([]Document{first, second}, "1")
	e, ok := set.Lookup("+15125550180")
	if !ok {
		t.Fatal("number missing from the set")
	}
	// Same number, different names: the first source in declaration order wins.
	if e.Name != "Nell Ashby" || e.Source != "first" {
		t.Errorf("name = %q from %q, want the first source's", e.Name, e.Source)
	}
	// One personal, one published: published.
	if e.Class != Published {
		t.Errorf("class = %v, want published — the restrictive answer", e.Class)
	}
	tot := set.Totals()
	if tot.Renamed != 1 || tot.Overruled != 1 || tot.Shared != 1 {
		t.Errorf("totals = %+v, want one renamed, one overruled, one shared", tot)
	}

	// Declaration order is the only ordering: swap them and the other name wins.
	swapped := Compile([]Document{second, first}, "1")
	if e, _ := swapped.Lookup("+15125550180"); e.Name != "N. Ashby (work)" {
		t.Errorf("name = %q, want the newly-first source's", e.Name)
	}
	// And published still wins, whichever order they arrive in.
	if e, _ := swapped.Lookup("+15125550180"); e.Class != Published {
		t.Errorf("class = %v, want published regardless of order", e.Class)
	}
}

// A nameless card cannot claim a number a later source can actually name: the
// first *name* wins, not the first mention. A handset display saying nothing
// helps nobody, and there is no conflict to resolve when only one source
// offered an answer.
func TestANamelessCardDoesNotClaimTheName(t *testing.T) {
	set := Compile([]Document{
		{ID: "first", Data: []byte(
			"BEGIN:VCARD\nVERSION:3.0\nFN:\nN:;;;;\nTEL;TYPE=CELL:+15125550190\nEND:VCARD\n")},
		{ID: "second", Data: []byte(
			"BEGIN:VCARD\nVERSION:3.0\nFN:Nell Ashby\nTEL;TYPE=CELL:+15125550190\nEND:VCARD\n")},
	}, "1")

	e, _ := set.Lookup("+15125550190")
	if e.Name != "Nell Ashby" || e.Source != "second" {
		t.Errorf("entry = %+v, want the only name anybody offered", e)
	}
	if set.Totals().Renamed != 0 {
		t.Error("filling in an absent name was counted as a rename")
	}
}

func TestBlockBeatsAdmit(t *testing.T) {
	set := Compile([]Document{
		doc(t, "eric", policy.ContactAdmit, "google.vcf"),
		doc(t, "nuisance", policy.ContactBlock, "blocked.vcf"),
	}, "1")

	// Wei Chen's mobile is personal in the admit source and present in the
	// block list. Blocked wins, and the contradiction is counted rather than
	// silently ranked.
	e, ok := set.Lookup("+15125550110")
	if !ok {
		t.Fatal("number missing")
	}
	if !e.Blocked {
		t.Error("a number in both an admit and a block source was not blocked")
	}
	if set.Totals().Contradicted != 1 {
		t.Errorf("contradicted = %d, want 1", set.Totals().Contradicted)
	}
	// A blocked number counts as blocked and nothing else.
	tot := set.Totals()
	if tot.Blocked != 2 {
		t.Errorf("blocked = %d, want 2", tot.Blocked)
	}
	if tot.Personal+tot.Published+tot.Blocked != tot.Numbers {
		t.Errorf("tiers do not add up: %+v", tot)
	}
	// Order does not rescue it: a block later in the file still wins.
	reversed := Compile([]Document{
		doc(t, "nuisance", policy.ContactBlock, "blocked.vcf"),
		doc(t, "eric", policy.ContactAdmit, "google.vcf"),
	}, "1")
	if e, _ := reversed.Lookup("+15125550110"); !e.Blocked {
		t.Error("block lost to an admit source declared after it")
	}
}

func TestAnUnreadableSourceContributesNothingAndStopsNobody(t *testing.T) {
	set := Compile([]Document{
		{ID: "gone", Kind: policy.ContactAdmit, Where: "/nowhere/eric.vcf",
			Err: os.ErrNotExist, Missing: true},
		doc(t, "eric", policy.ContactAdmit, "icloud.vcf"),
	}, "1")

	if r := set.Sources()[0]; r.Unread == "" || !r.Missing {
		t.Errorf("report = %+v, want it marked unread and missing", r)
	}
	if set.Len() != 6 {
		t.Errorf("the readable source contributed %d numbers, want 6", set.Len())
	}
}

func TestEveryFixtureTogether(t *testing.T) {
	set := Compile([]Document{
		doc(t, "icloud", policy.ContactAdmit, "icloud.vcf"),
		doc(t, "google", policy.ContactAdmit, "google.vcf"),
		doc(t, "carddav", policy.ContactAdmit, "carddav.vcf"),
		doc(t, "outlook", policy.ContactAdmit, "outlook21.vcf"),
		doc(t, "messy", policy.ContactAdmit, "messy.vcf"),
		doc(t, "nuisance", policy.ContactBlock, "blocked.vcf"),
	}, "1")

	tot := set.Totals()
	if tot.Numbers != set.Len() {
		t.Errorf("totals.Numbers = %d, Len = %d", tot.Numbers, set.Len())
	}
	if tot.Personal+tot.Published+tot.Blocked != tot.Numbers {
		t.Errorf("tiers do not partition the set: %+v", tot)
	}
	// Every published number has a reason recorded, and the reasons add up.
	var reasoned int
	for _, n := range set.Reasons() {
		reasoned += n
	}
	if reasoned != tot.Published {
		t.Errorf("reasons sum to %d, published = %d", reasoned, tot.Published)
	}
	// The set is deterministic: same inputs, same order out.
	if got := set.Numbers(); len(got) != tot.Numbers {
		t.Errorf("Numbers() = %d entries, want %d", len(got), tot.Numbers)
	}
}

// ── from disk ────────────────────────────────────────────────────────────

func TestLoadReadsPathSources(t *testing.T) {
	dir := t.TempDir()
	copyFixture(t, dir, "icloud.vcf")
	copyFixture(t, dir, "blocked.vcf")

	c := writeContacts(t, dir, `
[[sources]]
id = "eric"
path = "`+filepath.Join(dir, "icloud.vcf")+`"

[[sources]]
id = "nuisance"
path = "`+filepath.Join(dir, "blocked.vcf")+`"
kind = "block"
`)

	set := Load(c, "1")
	if !set.Present() {
		t.Fatal("set is absent with a contacts.toml in play")
	}
	if len(set.Sources()) != 2 {
		t.Fatalf("sources = %d, want 2", len(set.Sources()))
	}
	if e, ok := set.Lookup("+15125550101"); !ok || e.Class != Personal {
		t.Errorf("grandma = %+v, %v", e, ok)
	}
	if e, _ := set.Lookup("+15125550150"); !e.Blocked {
		t.Error("the block list was read as an allow-list")
	}
}

// A relative path is relative to contacts.toml, not to whatever directory the
// command was run from. Otherwise `doorman check` from a home directory reads
// different files than the daemon does, and reports on an address book nobody
// has.
func TestRelativePathsResolveAgainstTheInventory(t *testing.T) {
	dir := t.TempDir()
	copyFixture(t, dir, "icloud.vcf")
	c := writeContacts(t, dir, "[[sources]]\nid = \"eric\"\npath = \"icloud.vcf\"\n")

	set := Load(c, "1")
	if r := set.Sources()[0]; r.Unread != "" {
		t.Fatalf("relative path not resolved against the inventory: %s", r.Unread)
	}
	if set.Len() != 6 {
		t.Errorf("numbers = %d, want 6", set.Len())
	}
}

func TestLoadReportsAMissingFileWithoutFailing(t *testing.T) {
	dir := t.TempDir()
	c := writeContacts(t, dir, `
[[sources]]
id = "eric"
path = "`+filepath.Join(dir, "not-there.vcf")+`"
`)
	set := Load(c, "1")
	r := set.Sources()[0]
	if !r.Missing || r.Unread == "" {
		t.Errorf("report = %+v, want missing", r)
	}
	if set.Len() != 0 {
		t.Errorf("set = %d numbers", set.Len())
	}
}

// url sources are reserved, and reserved has to be visible. A source that
// quietly contributed nothing would be the silence this milestone exists to
// avoid.
func TestURLSourcesAreReportedAsNotFetchedYet(t *testing.T) {
	dir := t.TempDir()
	c := writeContacts(t, dir, `
[[sources]]
id  = "shared"
url = "https://example.com/contacts/shared.vcf"
`)
	set := Load(c, "1")
	r := set.Sources()[0]
	if r.Unread == "" {
		t.Fatal("a url source was silently empty")
	}
	if r.Missing {
		t.Error("a url source is not an operator's mistake, and must not read as one")
	}
	if r.Where != "https://example.com/contacts/shared.vcf" {
		t.Errorf("where = %q — the URL is safe to print, and should be", r.Where)
	}
}

// The compatibility gate: no contacts.toml, no set, nothing anywhere.
func TestNoContactsFileMeansAnAbsentSet(t *testing.T) {
	c, err := policy.LoadContacts(filepath.Join(t.TempDir(), "contacts.toml"))
	if err != nil {
		t.Fatalf("LoadContacts on a missing file: %v", err)
	}
	set := Load(c, "1")
	if set.Present() {
		t.Error("set claims to be present with no contacts.toml")
	}
	if set.Len() != 0 || len(set.Sources()) != 0 {
		t.Errorf("absent set is not empty: %d numbers, %d sources", set.Len(), len(set.Sources()))
	}
	if _, ok := set.Lookup("+15125550101"); ok {
		t.Error("an absent set matched a number")
	}
}

// A nil set is as usable as an absent one. The daemon never builds one — with
// no contacts.toml it hands the lobby a nil lookup instead — but the whole type
// is written to be inert when empty, and a Lookup that panicked would do it on
// a call.
func TestNilSetIsUsable(t *testing.T) {
	var set *Set
	if set.Present() || set.Len() != 0 || set.Sources() != nil || set.Numbers() != nil {
		t.Error("nil *Set is not inert")
	}
	if _, ok := set.Lookup("+15125550101"); ok {
		t.Error("nil *Set matched a number")
	}
	if set.Totals() != (Totals{}) || set.Reasons() != (Reasons{}) || set.Where() != "contacts.toml" {
		t.Error("nil *Set does not report empty")
	}
}

func TestSourceReportString(t *testing.T) {
	set := Compile([]Document{
		doc(t, "eric", policy.ContactAdmit, "icloud.vcf"),
		{ID: "gone", Kind: policy.ContactAdmit, Err: os.ErrNotExist, Missing: true},
	}, "1")
	if s := set.Sources()[0].String(); s == "" {
		t.Error("no report line")
	}
	if s := set.Sources()[1].String(); s == "" {
		t.Error("no report line for an unread source")
	}
	if n := set.Sources()[0].Numbers(); n != 6 {
		t.Errorf("Numbers() = %d, want 6", n)
	}
}

func copyFixture(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), fixture(t, name), 0o600); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
}

func writeContacts(t *testing.T, dir, body string) *policy.Contacts {
	t.Helper()
	path := filepath.Join(dir, "contacts.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing contacts.toml: %v", err)
	}
	c, err := policy.LoadContacts(path)
	if err != nil {
		t.Fatalf("LoadContacts: %v", err)
	}
	return c
}
