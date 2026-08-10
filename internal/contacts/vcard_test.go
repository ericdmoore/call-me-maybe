package contacts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The fixtures are what the exporters people actually have produce. Parsing
// them is the milestone: a subset that handles iCloud, Google Contacts, a
// CardDAV server and a 2.1 file is worth more than a complete implementation
// of a spec whose long tail we would reject anyway.

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	return data
}

func TestICloudExport(t *testing.T) {
	cards, sk := parseCards(fixture(t, "icloud.vcf"))
	if len(cards) != 4 {
		t.Fatalf("cards = %d, want 4", len(cards))
	}
	if sk.cards != 0 || sk.lines != 0 {
		t.Errorf("skips = %+v, want none", sk)
	}

	if cards[0].name != "Grandma Mertaugh" {
		t.Errorf("name = %q", cards[0].name)
	}
	if len(cards[0].tels) != 2 {
		t.Fatalf("tels = %d, want 2", len(cards[0].tels))
	}
	// TYPE spelled lower case, repeated once per value — iCloud's habit.
	if !has(cards[0].tels[0].types, "cell") {
		t.Errorf("types = %v, want cell among them", cards[0].tels[0].types)
	}

	if cards[1].org != "Kitchen Sink Plumbing & Drain" {
		t.Errorf("org = %q — only the first ORG component names the business", cards[1].org)
	}

	// The one that matters for the classifier: iCloud writes a custom-labelled
	// number as item1.TEL with no type at all, and a property group is not a
	// property name.
	if len(cards[2].tels) != 1 {
		t.Fatalf("grouped property was not read as a TEL: %+v", cards[2])
	}
	if len(cards[2].tels[0].types) != 1 || cards[2].tels[0].types[0] != "pref" {
		t.Errorf("types = %v, want just pref", cards[2].tels[0].types)
	}

	// An empty ORG must not read as a business — iCloud emits "ORG:;" freely.
	if cards[3].org != "" {
		t.Errorf("org = %q, want empty: ORG:; names no organisation", cards[3].org)
	}
}

func TestGoogleContactsExport(t *testing.T) {
	cards, sk := parseCards(fixture(t, "google.vcf"))
	if len(cards) != 5 {
		t.Fatalf("cards = %d, want 5", len(cards))
	}
	if sk.cards != 0 || sk.lines != 0 {
		t.Errorf("skips = %+v, want none", sk)
	}
	if cards[3].name != "" {
		t.Errorf("name = %q — an empty FN and an empty N is a card with no name", cards[3].name)
	}
	// \, is an escaped comma, not two components of anything.
	if cards[4].name != "Marisol Reyes-Ortiz, DVM" {
		t.Errorf("name = %q, escape not resolved", cards[4].name)
	}
}

func TestCardDAVExportInVCard4(t *testing.T) {
	cards, sk := parseCards(fixture(t, "carddav.vcf"))
	if len(cards) != 3 {
		t.Fatalf("cards = %d, want 3", len(cards))
	}
	if sk.cards != 0 || sk.lines != 0 {
		t.Errorf("skips = %+v, want none", sk)
	}
	// 4.0 quotes a list of types, and the colon inside those quotes must not
	// end the property name.
	got := cards[0].tels[0].types
	if !has(got, "voice") || !has(got, "cell") {
		t.Errorf("types = %v, want voice and cell from TYPE=\"voice,cell\"", got)
	}
	if cards[1].org != "Riverside Veterinary" {
		t.Errorf("org = %q", cards[1].org)
	}
	// tel: URI plus a ;ext= parameter. Keeping the extension would produce a
	// thirteen-digit number belonging to nobody.
	if n := telNumber(cards[1].tels[0].value); n != "+15125550122" {
		t.Errorf("telNumber = %q, want the tel: scheme and ;ext= gone", n)
	}
}

func TestVCard21QuotedPrintableAndBareTypes(t *testing.T) {
	cards, sk := parseCards(fixture(t, "outlook21.vcf"))
	if len(cards) != 3 {
		t.Fatalf("cards = %d, want 3", len(cards))
	}
	if sk.cards != 0 || sk.lines != 0 {
		t.Errorf("skips = %+v, want none", sk)
	}
	if cards[0].name != "José Fernández" {
		t.Errorf("name = %q, quoted-printable not decoded", cards[0].name)
	}
	// 2.1 writes types bare: TEL;CELL: means the same as TEL;TYPE=CELL:.
	if !has(cards[0].tels[0].types, "cell") {
		t.Errorf("types = %v, want cell from a bare 2.1 parameter", cards[0].tels[0].types)
	}
	// ENCODING is not a kind of telephone.
	for _, ty := range cards[0].tels[0].types {
		if ty == "quoted-printable" {
			t.Error("an encoding parameter was read as a TYPE")
		}
	}
	// A quoted-printable soft line break: a trailing "=" continues on the next
	// physical line with no leading whitespace, which ordinary unfolding misses.
	if cards[1].name != "Margarethe Schönberg-Whittingham" {
		t.Errorf("name = %q — the quoted-printable soft break was not joined", cards[1].name)
	}
}

func TestFoldedLinesAreJoined(t *testing.T) {
	// Both folding characters, and the one-character strip: RFC 2425 removes
	// exactly the leading whitespace, so an intentional space survives.
	doc := "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Anneke van der\r\n  Meulen\r\n" +
		"TEL;TYPE=CELL:+15125550160\r\nEND:VCARD\r\n"
	cards, sk := parseCards([]byte(doc))
	if len(cards) != 1 || sk.lines != 0 {
		t.Fatalf("cards = %d, skips = %+v", len(cards), sk)
	}
	if cards[0].name != "Anneke van der Meulen" {
		t.Errorf("name = %q", cards[0].name)
	}
}

func TestCRLFAndBareCRAreBothAccepted(t *testing.T) {
	for name, sep := range map[string]string{"crlf": "\r\n", "lf": "\n", "cr": "\r"} {
		t.Run(name, func(t *testing.T) {
			doc := strings.Join([]string{
				"BEGIN:VCARD", "VERSION:3.0", "FN:Ida Solheim",
				"TEL;TYPE=CELL:+15125550161", "END:VCARD", "",
			}, sep)
			cards, sk := parseCards([]byte(doc))
			if len(cards) != 1 || sk.lines != 0 || sk.cards != 0 {
				t.Fatalf("cards = %d, skips = %+v", len(cards), sk)
			}
		})
	}
}

// Skip-and-count is the contract: whatever the parser does not understand has
// to show up as a number, because a subset that silently dropped a third of a
// file would look exactly like a subset that understood all of it.
func TestWhatIsNotUnderstoodIsCounted(t *testing.T) {
	cards, sk := parseCards(fixture(t, "messy.vcf"))
	if len(cards) != 2 {
		t.Fatalf("cards = %d, want the two complete ones", len(cards))
	}
	// One embedded AGENT card, one card truncated at end of file.
	if sk.cards != 2 {
		t.Errorf("dropped cards = %d, want 2", sk.cards)
	}
	// Two lines of prose that are not properties.
	if sk.lines != 2 {
		t.Errorf("malformed lines = %d, want 2", sk.lines)
	}
	// The agent's number must not have been absorbed by the card containing it.
	for _, tl := range cards[1].tels {
		if strings.Contains(tl.value, "0141") {
			t.Error("an embedded card's number was attributed to its container")
		}
	}
	// And the truncated card contributed nothing: half a card is not a contact.
	for _, c := range cards {
		if c.name == "Truncated Contact" {
			t.Error("a card unterminated at end of file was admitted anyway")
		}
	}
}

func TestPropertiesOutsideACardAreCounted(t *testing.T) {
	_, sk := parseCards([]byte("FN:Nobody\nTEL;TYPE=CELL:+15125550170\nEND:VCARD\n"))
	if sk.lines != 3 {
		t.Errorf("malformed lines = %d, want 3 — two loose properties and an unmatched END", sk.lines)
	}
}

func TestTelNumber(t *testing.T) {
	for in, want := range map[string]string{
		"tel:+15125550100":           "+15125550100",
		"TEL:+15125550100":           "+15125550100",
		"tel:+1-512-555-0100;ext=42": "+1-512-555-0100",
		"+1 (512) 555-0100":          "+1 (512) 555-0100",
		"  512.555.0100  ":           "512.555.0100",
	} {
		if got := telNumber(in); got != want {
			t.Errorf("telNumber(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestUnescape(t *testing.T) {
	for in, want := range map[string]string{
		`Reyes-Ortiz\, DVM`: "Reyes-Ortiz, DVM",
		`a\;b`:              "a;b",
		`back\\slash`:       `back\slash`,
		`two\nlines`:        "two\nlines",
		`nothing`:           "nothing",
		`trailing\`:         `trailing\`,
	} {
		if got := unescape(in); got != want {
			t.Errorf("unescape(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestStructuredValuesSplitOnUnescapedSeparators(t *testing.T) {
	got := splitEscaped(`O'Neill\; Sons;Plumbing`, ';')
	if len(got) != 2 {
		t.Fatalf("components = %d (%q), want 2 — the escaped semicolon is part of the name", len(got), got)
	}
	if unescape(got[0]) != "O'Neill; Sons" {
		t.Errorf("first component = %q", unescape(got[0]))
	}
}

func TestGarbageIsNotACard(t *testing.T) {
	cards, sk := parseCards([]byte("not a vcard at all\njust some text\n"))
	if len(cards) != 0 {
		t.Errorf("cards = %d, want none", len(cards))
	}
	if sk.lines != 2 {
		t.Errorf("malformed lines = %d, want 2", sk.lines)
	}
}

func TestEmptyDocument(t *testing.T) {
	cards, sk := parseCards(nil)
	if len(cards) != 0 || sk.cards != 0 || sk.lines != 0 {
		t.Errorf("cards = %d, skips = %+v — an empty file is not a problem", len(cards), sk)
	}
}

func has(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
