package accountbook

import (
	"fmt"
	"strings"
	"testing"
)

const (
	family  = "acct-01jaf000000000000000000fam"
	charlie = "acct-01jaf000000000000000000chr"
	rose    = "c-01jaf000000000000000000r5e"
	dana    = "c-01jaf0000000000000000000da"
)

func sample() Book {
	return Book{ID: charlie, Revision: 3, Label: "Charlie", Includes: []string{family, House},
		Contacts: []Contact{
			{ID: dana, Name: "Coach Dana", Numbers: []Number{{Label: "mobile", Number: "+15550100142"}}},
			{ID: rose, Name: "Grandma Rose \"Ro\"", Numbers: []Number{{Label: "home", Number: "+15550100143"}, {Number: "+15550100144"}}},
		}}
}

func TestEncodeParseRoundTripIsByteStable(t *testing.T) {
	data := Encode(sample())
	if !strings.HasPrefix(string(data), Header) {
		t.Fatal("managed file lost its header")
	}
	b, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if again := Encode(b); string(again) != string(data) {
		t.Fatalf("re-encoding changed the bytes:\n%s\n---\n%s", data, again)
	}
	if Hash(data) != Hash(Encode(sample())) || len(Hash(data)) != 64 {
		t.Fatal("hash is not stable")
	}
	empty := Encode(Book{ID: family, Label: "Family"})
	if b, err := Parse(empty); err != nil || len(b.Includes) != 0 || len(b.Contacts) != 0 {
		t.Fatalf("empty book did not round trip: %v\n%s", err, empty)
	}
}

func TestParseRefusesUnknownKeysAndBadTOML(t *testing.T) {
	if _, err := Parse([]byte("id = ")); err == nil || err.Error() != "not valid TOML" {
		t.Fatalf("bad TOML: %v", err)
	}
	data := append(Encode(sample()), []byte("admit = true\n")...)
	if _, err := Parse(data); err == nil || !strings.Contains(err.Error(), "unknown key") {
		t.Fatalf("unknown key accepted: %v", err)
	}
	bad := sample()
	bad.Label = ""
	if _, err := Parse(Encode(bad)); err == nil || !strings.Contains(err.Error(), "label is empty") {
		t.Fatalf("invalid book parsed: %v", err)
	}
}

func TestProblemsCoverEveryRule(t *testing.T) {
	long := strings.Repeat("x", MaxName+1)
	b := Book{ID: "Bad", Revision: -1, Label: long, Includes: []string{"people", "acct-short", House, House, "Bad"}}
	b.Contacts = []Contact{
		{ID: "x", Name: "", Numbers: nil},
		{ID: rose, Name: "a\x07b", Numbers: []Number{{Number: "5550100"}, {Label: strings.Repeat("l", MaxNumberLabel+1), Number: "+15550100100"}}},
		{ID: rose, Name: "\xff", Numbers: make([]Number, MaxNumbers+1)},
	}
	got := strings.Join(b.Problems(), "\n")
	for _, want := range []string{
		"book id must be acct-",
		"revision must not be negative",
		"label is longer than 128",
		"includes itself",
		`includes "people", which is neither house nor an account book`,
		`includes "acct-short"`,
		`includes "house" twice`,
		"contact 1: id must be c-",
		"contact 1: name is empty",
		"contact 1: needs 1 to 8 numbers",
		"contact 2: name contains a control character",
		"contact 2: number 1 is not E.164",
		"contact 2: number 2 label is longer than 64",
		"contact 3: id " + rose + " is listed twice",
		"contact 3: name is not UTF-8",
		"contact 3: needs 1 to 8 numbers",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing problem %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "5550100") {
		t.Fatal("a problem quoted a number")
	}
	if p := sample().Problems(); len(p) != 0 {
		t.Fatalf("valid book refused: %v", p)
	}
}

func TestProblemsBoundCountsAndTheirOwnLength(t *testing.T) {
	b := sample()
	for len(b.Includes) <= MaxIncludes {
		b.Includes = append(b.Includes, id(len(b.Includes)))
	}
	for len(b.Contacts) <= MaxContacts {
		b.Contacts = append(b.Contacts, Contact{ID: fmt.Sprintf("c-01jaf%021d", len(b.Contacts)), Name: "D", Numbers: []Number{{Number: "+15550100101"}}})
	}
	got := strings.Join(b.Problems(), "\n")
	if !strings.Contains(got, "includes more than 16") || !strings.Contains(got, "lists more than 2000 contacts") {
		t.Fatalf("count limits: %s", got)
	}
	for i := range b.Contacts {
		b.Contacts[i].Name = ""
	}
	p := b.Problems()
	if len(p) != maxProblems+1 || !strings.Contains(p[maxProblems], "more problems") {
		t.Fatalf("problems were not bounded: %d", len(p))
	}
}

func TestIDs(t *testing.T) {
	if !ValidBookID(family) || ValidBookID("acct-01JAF000000000000000000FAM") || ValidBookID("acct-01jaf0000000000000000000il") {
		t.Fatal("book id pattern")
	}
	if !ValidContactID(rose) || ValidContactID("person:abc") {
		t.Fatal("contact id pattern")
	}
	if FileName(family) != family+".toml" {
		t.Fatal("file name")
	}
}

func id(n int) string { return fmt.Sprintf("acct-01jaf%021d", n) }

func TestCheckResolvesIncludesCyclesAndDepth(t *testing.T) {
	books := map[string]Book{
		family:  {ID: family, Label: "Family", Includes: []string{House}},
		charlie: {ID: charlie, Label: "Charlie", Includes: []string{family, House}},
	}
	if p := Check(books); len(p) != 0 {
		t.Fatalf("valid set refused: %v", p)
	}
	books[charlie] = Book{ID: charlie, Label: "Charlie", Includes: []string{id(9)}}
	if p := Check(books); len(p) != 1 || !strings.Contains(p[0], "does not hold") {
		t.Fatalf("dangling include: %v", p)
	}
	books[charlie] = Book{ID: charlie, Label: "Charlie", Includes: []string{family}}
	books[family] = Book{ID: family, Label: "Family", Includes: []string{charlie}}
	if p := Check(books); len(p) != 1 || !strings.Contains(p[0], "cycle") {
		t.Fatalf("cycle: %v", p)
	}
	chain := map[string]Book{}
	for i := 0; i <= MaxDepth; i++ {
		inc := []string{House}
		if i > 0 {
			inc = []string{id(i - 1)}
		}
		chain[id(i)] = Book{ID: id(i), Label: "x", Includes: inc}
	}
	p := Check(chain)
	if len(p) != 1 || !strings.Contains(p[0], fmt.Sprintf("%d deep", MaxDepth+1)) {
		t.Fatalf("depth: %v", p)
	}
	delete(chain, id(MaxDepth))
	if p := Check(chain); len(p) != 0 {
		t.Fatalf("depth at the limit refused: %v", p)
	}
	many := map[string]Book{}
	for i := 0; i <= MaxBooks; i++ {
		many[id(i)] = Book{ID: id(i), Label: "x"}
	}
	if p := Check(many); len(p) != 1 || !strings.Contains(p[0], "more than 200") {
		t.Fatalf("book count: %v", p)
	}
}
