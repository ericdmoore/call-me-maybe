package configcheck

import (
	"fmt"
	"strings"
	"testing"

	"callmemaybe/internal/accountbook"
	"callmemaybe/internal/policy"
)

const (
	family  = "acct-01jaf000000000000000000fam"
	charlie = "acct-01jaf000000000000000000chr"
)

func book(id string, includes []string, contacts int) []byte {
	b := accountbook.Book{ID: id, Revision: 1, Label: "Book", Includes: includes}
	for i := 0; i < contacts; i++ {
		b.Contacts = append(b.Contacts, accountbook.Contact{ID: fmt.Sprintf("c-%s%05d", id[5:26], i), Name: "Someone",
			Numbers: []accountbook.Number{{Label: "mobile", Number: "+15550100100"}}})
	}
	return accountbook.Encode(b)
}

func messages(p []Problem) string {
	var out []string
	for _, x := range p {
		out = append(out, x.File+"|"+x.Message)
	}
	return strings.Join(out, "\n")
}

func TestAccountBooksAcceptsAComposableSet(t *testing.T) {
	files := map[string][]byte{family + ".toml": book(family, []string{"house"}, 3), charlie + ".toml": book(charlie, []string{family}, 2)}
	kids := []policy.Handset{{ID: "kids", Endpoint: "PJSIP/kids", Number: 101, Phonebook: []string{charlie, "people"}}, {ID: "hall"}}
	books, p := AccountBooks(files, kids)
	if len(p) != 0 || len(books) != 2 || books[charlie].Includes[0] != family {
		t.Fatalf("valid set refused: %s", messages(p))
	}
	if books, p := AccountBooks(nil, kids[1:]); len(p) != 0 || len(books) != 0 {
		t.Fatalf("no books: %s", messages(p))
	}
}

func TestAccountBooksRefusesBadFilesAndSets(t *testing.T) {
	files := map[string][]byte{
		"notes.txt":                            []byte("x"),
		"acct-nope.toml":                       []byte("x"),
		family + ".toml":                       []byte("id = "),
		charlie + ".toml":                      book(family, nil, 0),
		"acct-01jaf000000000000000000ok0.toml": book("acct-01jaf000000000000000000ok0", nil, 0),
	}
	_, p := AccountBooks(files, nil)
	got := messages(p)
	for _, want := range []string{"notes.txt|not an account book file name", "acct-nope.toml|not an account book file name",
		family + ".toml|not valid TOML", charlie + ".toml|the id inside does not match"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	// A dangling include names the file that has it.
	_, p = AccountBooks(map[string][]byte{charlie + ".toml": book(charlie, []string{family}, 0)}, nil)
	if len(p) != 1 || p[0].File != charlie+".toml" || !strings.Contains(p[0].Message, "does not hold") {
		t.Fatalf("dangling include: %s", messages(p))
	}
	// A problem about the whole set names no file.
	many := map[string][]byte{}
	for i := 0; i <= accountbook.MaxBooks; i++ {
		id := fmt.Sprintf("acct-01jaf%021d", i)
		many[id+".toml"] = book(id, nil, 0)
	}
	_, p = AccountBooks(many, nil)
	if len(p) != 1 || p[0].File != "" {
		t.Fatalf("book count: %s", messages(p))
	}
}

func TestAccountBooksChecksHandsetsAndTheDirectoryLimit(t *testing.T) {
	files := map[string][]byte{family + ".toml": book(family, []string{"house"}, 1)}
	rooms := []policy.Handset{{ID: "kids", Endpoint: "PJSIP/kids", Number: 101, Phonebook: []string{family, charlie}}}
	_, p := AccountBooks(files, rooms)
	if len(p) != 1 || p[0].File != "" || !strings.Contains(p[0].Message, `handset "kids" uses account book `+charlie) {
		t.Fatalf("missing book: %s", messages(p))
	}
	// Family's own contacts fit, but with the house it includes they do not.
	files[family+".toml"] = book(family, []string{"house"}, accountbook.DefaultDirectoryLimit-1)
	rooms[0].Phonebook = []string{family}
	_, p = AccountBooks(files, rooms)
	if len(p) != 1 || !strings.Contains(p[0].Message, "more than the 1000 its directory takes") {
		t.Fatalf("directory limit: %s", messages(p))
	}
	// Box books on the same handset are not counted against it.
	files[family+".toml"] = book(family, nil, accountbook.DefaultDirectoryLimit)
	rooms[0].Phonebook = []string{"house", family}
	if _, p = AccountBooks(files, rooms); len(p) != 0 {
		t.Fatalf("at the limit: %s", messages(p))
	}
}
