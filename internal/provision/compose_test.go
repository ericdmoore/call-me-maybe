package provision

import (
	"strings"
	"testing"

	"callmemaybe/internal/accountbook"
	"callmemaybe/internal/policy"
)

const (
	familyID  = "acct-01jaf000000000000000000fam"
	charlieID = "acct-01jaf000000000000000000chr"
	grandsID  = "acct-01jaf000000000000000000gr0"
	roseID    = "c-01jaf000000000000000000r5e"
	danaID    = "c-01jaf0000000000000000000da"
	landID    = "c-01jaf00000000000000000000a"
)

func composedBooks() Books {
	rose := accountbook.Contact{ID: roseID, Name: "Grandma Rose", Numbers: []accountbook.Number{{Label: "home", Number: "+15550100143"}}}
	// A second person on the same landline: a different contact, not a duplicate.
	grandpa := accountbook.Contact{ID: landID, Name: "Grandpa Joe", Numbers: []accountbook.Number{{Label: "home", Number: "+15550100143"}}}
	dana := accountbook.Contact{ID: danaID, Name: "Coach Dana", Numbers: []accountbook.Number{{Label: "mobile", Number: "+15550100142"}, {Label: "office", Number: "+447700900123"}}}
	books := Books{
		"house":  HouseBook(bookHandsets),
		"people": PeopleBook(bookCallers),
		familyID: AccountBook(accountbook.Book{ID: familyID, Label: "Family", Includes: []string{"house"},
			Contacts: []accountbook.Contact{rose, grandpa}}),
		// Charlie lists Rose directly as well as through Family.
		charlieID: AccountBook(accountbook.Book{ID: charlieID, Label: "Charlie", Includes: []string{familyID, grandsID, "house"},
			Contacts: []accountbook.Contact{dana, rose}}),
		// Everyone in Grandparents is already reached through Family.
		grandsID: AccountBook(accountbook.Book{ID: grandsID, Label: "Grandparents", Includes: []string{familyID},
			Contacts: []accountbook.Contact{rose}}),
	}
	return books
}

func TestComposedBookShowsOwnContactsThenIncludesOncePerContact(t *testing.T) {
	books := composedBooks()
	chosen, err := Select(policy.Handset{ID: "kids", Phonebook: []string{charlieID}}, books)
	if err != nil {
		t.Fatal(err)
	}
	var groups []string
	for _, g := range chosen {
		var names []string
		for _, c := range g.Contacts {
			names = append(names, c.Name)
		}
		groups = append(groups, g.Name+": "+strings.Join(names, ", "))
	}
	got := strings.Join(groups, "\n")
	// Rose once, under Charlie; Grandpa Joe kept although he shares her
	// number; Grandparents contributes nothing so has no group; House is
	// reached through Family first and shown once.
	want := "Charlie: Coach Dana, Grandma Rose\nFamily: Grandpa Joe\nHouse: Kitchen, Theater, Ring everyone"
	if !strings.HasPrefix(got, want) || strings.Count(got, "House:") != 1 {
		t.Fatalf("composition:\n%s", got)
	}
	ab := parseBook(t, renderGrandstreamPhonebook(chosen))
	if len(ab.Groups) != 3 || ab.Contacts[0].Phones[0].Type != "Cell" || ab.Contacts[0].Phones[1].Type != "Work" ||
		ab.Contacts[0].Phones[0].Number != "15550100142" || ab.Contacts[0].Phones[1].Number != "+447700900123" || ab.Contacts[1].Phones[0].Type != "Home" {
		t.Fatalf("rendered: %+v", ab)
	}
}

func TestComposedAndBoxBooksSideBySide(t *testing.T) {
	books := composedBooks()
	// House named directly first keeps its place; Family's include of it is
	// then already shown. people is a box book and is never merged with an
	// account contact.
	chosen, err := Select(policy.Handset{ID: "kitchen", Phonebook: []string{"house", familyID, "people"}}, books)
	if err != nil {
		t.Fatal(err)
	}
	if len(chosen) != 3 || chosen[0].ID != "house" || chosen[1].ID != familyID || chosen[2].ID != "people" {
		t.Fatalf("order: %+v", chosen)
	}
	// A directly named account book that ends up empty is left out; a box
	// book named directly keeps its group, empty or not.
	books["empty"] = Book{ID: "empty", Name: "Empty"}
	books[grandsID] = AccountBook(accountbook.Book{ID: grandsID, Label: "Grandparents"})
	chosen, err = Select(policy.Handset{ID: "hall", Phonebook: []string{"empty", grandsID}}, books)
	if err != nil || len(chosen) != 1 || chosen[0].ID != "empty" {
		t.Fatalf("empty groups: %+v %v", chosen, err)
	}
}

func TestComposedBookRefusesMissingIncludesAndRunawayNesting(t *testing.T) {
	books := composedBooks()
	delete(books, familyID)
	if _, err := Select(policy.Handset{ID: "kids", Phonebook: []string{charlieID}}, books); err == nil || !strings.Contains(err.Error(), "does not hold") {
		t.Fatalf("missing include: %v", err)
	}
	if _, err := Select(policy.Handset{ID: "kids", Phonebook: []string{"acct-01jaf000000000000000000zzz"}}, books); err == nil || !strings.Contains(err.Error(), "account book this box holds") {
		t.Fatalf("missing book: %v", err)
	}
	loop := Books{}
	ids := []string{familyID, charlieID, grandsID, "acct-01jaf000000000000000000aaa", "acct-01jaf000000000000000000bbb", "acct-01jaf000000000000000000ccc"}
	for i, id := range ids {
		next := ids[(i+1)%len(ids)]
		loop[id] = Book{ID: id, Name: "x", Includes: []string{next}}
	}
	if _, err := Select(policy.Handset{ID: "kids", Phonebook: []string{familyID}}, loop); err == nil || !strings.Contains(err.Error(), "deep") {
		t.Fatalf("runaway nesting: %v", err)
	}
}

func TestDirectoryLimitIsConservativeUntilMeasured(t *testing.T) {
	if DirectoryLimit("grandstream-wp826") != accountbook.DefaultDirectoryLimit || DirectoryLimit("") != accountbook.DefaultDirectoryLimit {
		t.Fatal("unmeasured models take the default")
	}
	models = append(models, Model{ID: "test-small", DirectoryCapacity: 50}, Model{ID: "test-large", DirectoryCapacity: 5000})
	defer func() { models = models[:len(models)-2] }()
	if DirectoryLimit("test-small") != 50 || DirectoryLimit("test-large") != accountbook.DefaultDirectoryLimit {
		t.Fatal("a measured capacity only ever lowers the limit")
	}
}
