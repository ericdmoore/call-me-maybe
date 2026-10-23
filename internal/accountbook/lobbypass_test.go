package accountbook

import (
	"fmt"
	"strings"
	"testing"
)

const passes = "acct-01jaf000000000000000000pas"

func passBook() Book {
	return Book{ID: passes, Revision: 2, Label: "Lobby pass", Role: RoleLobbyPass,
		Contacts: []Contact{
			{ID: rose, Name: "Grandma Rose", Numbers: []Number{{Label: "home", Number: "+15550100143"}, {Number: "+15550100144"}}},
			{ID: dana, Name: "Coach Dana", Numbers: []Number{{Label: "mobile", Number: "+15550100142"}, {Label: "shared", Number: "+15550100143"}}},
		}}
}

// A directory book written before roles existed has no role key. It must
// read, encode and hash exactly as it did, or every box's books would
// conflict with their next delivery.
func TestADirectoryBookHasNoRoleAndItsBytesAreUnchanged(t *testing.T) {
	data := Encode(sample())
	if strings.Contains(string(data), "role") || !strings.HasPrefix(string(data), Header) {
		t.Fatalf("directory book gained a role or lost its header:\n%s", data)
	}
	const before = "# Managed by doorman sync for the account this box is enrolled with.\n" +
		"# Do not edit: the next delivery refuses to replace a changed copy and\n" +
		"# reports the change instead. Directory only; never call admission.\n" +
		"id = \"acct-01jaf000000000000000000fam\"\nrevision = 1\nlabel = \"Family\"\nincludes = [\"house\"]\ncontacts = []\n"
	b, err := Parse([]byte(before))
	if err != nil || b.Role != "" || b.LobbyPass() {
		t.Fatalf("a file from before roles: %+v %v", b, err)
	}
	if got := string(Encode(b)); got != before {
		t.Fatalf("re-encoding a file from before roles changed it:\n%s", got)
	}
}

func TestThePassBookRoundTripsUnderItsOwnHeader(t *testing.T) {
	data := Encode(passBook())
	if !strings.HasPrefix(string(data), PassHeader) || !strings.Contains(string(data), `role = "lobby-pass"`) {
		t.Fatalf("pass book encoding:\n%s", data)
	}
	b, err := Parse(data)
	if err != nil || !b.LobbyPass() {
		t.Fatalf("pass book did not parse: %v", err)
	}
	if string(Encode(b)) != string(data) {
		t.Fatal("pass book is not byte-stable")
	}
}

func TestPassBookRules(t *testing.T) {
	b := passBook()
	b.Includes = []string{House}
	if p := strings.Join(b.Problems(), "; "); !strings.Contains(p, "the lobby pass book includes nothing") {
		t.Fatalf("pass book with includes: %s", p)
	}
	b = passBook()
	b.Role = "admit-everyone"
	if p := strings.Join(b.Problems(), "; "); !strings.Contains(p, `role must be absent or "lobby-pass"`) {
		t.Fatalf("unknown role: %s", p)
	}

	// The pass book may list more than a directory book, and no more than
	// MaxPassContacts.
	b = passBook()
	b.Contacts = nil
	for i := 0; i < MaxPassContacts; i++ {
		b.Contacts = append(b.Contacts, Contact{ID: fmt.Sprintf("c-01jaf%021d", i), Name: "H", Numbers: []Number{{Number: fmt.Sprintf("+1555%07d", i)}}})
	}
	if p := b.Problems(); len(p) != 0 {
		t.Fatalf("pass book at its limit refused: %v", p)
	}
	b.Contacts = append(b.Contacts, Contact{ID: "c-01jaf0000000000000000zzzzz", Name: "H", Numbers: []Number{{Number: "+15559999999"}}})
	if p := strings.Join(b.Problems(), "; "); !strings.Contains(p, fmt.Sprintf("lists more than %d contacts", MaxPassContacts)) {
		t.Fatalf("pass book over its limit: %s", p)
	}
	b.Role = ""
	if p := strings.Join(b.Problems(), "; "); !strings.Contains(p, fmt.Sprintf("lists more than %d contacts", MaxContacts)) {
		t.Fatalf("a directory book keeps its own limit: %s", p)
	}
}

func TestCheckHoldsOnePassBookAndNobodyIncludesIt(t *testing.T) {
	books := map[string]Book{
		family: {ID: family, Label: "Family", Includes: []string{House}},
		passes: passBook(),
	}
	if p := Check(books); len(p) != 0 {
		t.Fatalf("one pass book refused: %v", p)
	}
	b, ok := LobbyPassBook(books)
	if !ok || b.ID != passes {
		t.Fatalf("pass book not found: %v", ok)
	}
	if _, ok := LobbyPassBook(map[string]Book{family: books[family]}); ok {
		t.Fatal("found a pass book in a set without one")
	}

	second := passBook()
	second.ID = id(7)
	books[second.ID] = second
	if p := Check(books); len(p) != 1 || !strings.Contains(p[0], "are both the lobby pass book") {
		t.Fatalf("two pass books: %v", p)
	}
	delete(books, second.ID)

	books[charlie] = Book{ID: charlie, Label: "Charlie", Includes: []string{family, passes}}
	if p := Check(books); len(p) != 1 || !strings.Contains(p[0], "includes the lobby pass book "+passes) {
		t.Fatalf("including the pass book: %v", p)
	}
}

func TestHoldersIndexByNumberAndTheFirstHolderWins(t *testing.T) {
	index := passBook().Holders()
	if len(index) != 3 {
		t.Fatalf("%d numbers, want 3", len(index))
	}
	if h := index["+15550100143"]; h.Name != "Grandma Rose" || h.ID != rose {
		t.Fatalf("shared number welcomed as %+v, want the first holder", h)
	}
	if h := index["+15550100142"]; h.Name != "Coach Dana" {
		t.Fatalf("%+v", h)
	}
	if len((Book{}).Holders()) != 0 {
		t.Fatal("an empty book holds passes")
	}
}
