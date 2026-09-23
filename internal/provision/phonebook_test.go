package provision

import (
	"encoding/xml"
	"strings"
	"testing"

	"callmemaybe/internal/policy"
)

var bookHandsets = []policy.Handset{
	{ID: "kitchen", Label: "Kitchen", Endpoint: "PJSIP/kitchen", Number: 101},
	{ID: "theater", Label: "Theater", Endpoint: "PJSIP/theater", Number: 102},
	{ID: "conference", Label: "Bridge", Endpoint: "Local/600@internal"},
}

var bookCallers = []policy.KnownCaller{
	{Name: "Dennis Gorman", E164: "+15550100001"},
	{Name: "Dennis Gorman", E164: "+15550100002"},
	{Name: "Grandma", E164: "+15550100003"},
}

type addressBook struct {
	Groups []struct {
		ID   string `xml:"id"`
		Name string `xml:"name"`
	} `xml:"pbgroup"`
	Contacts []struct {
		First  string `xml:"FirstName"`
		Last   string `xml:"LastName"`
		Group  int    `xml:"Group"`
		Phones []struct {
			Type   string `xml:"type,attr"`
			Number string `xml:"phonenumber"`
		} `xml:"Phone"`
	} `xml:"Contact"`
}

func parseBook(t *testing.T, b []byte) addressBook {
	t.Helper()
	var ab addressBook
	if err := xml.Unmarshal(b, &ab); err != nil {
		t.Fatalf("not XML: %v\n%s", err, b)
	}
	return ab
}

func TestDefaultBooksAreTheHouseAndThePeopleAndNothingElse(t *testing.T) {
	books := Books{
		"house":  HouseBook(bookHandsets),
		"people": PeopleBook(bookCallers),
		"school": SourceBook("school", "School", []policy.KnownCaller{{Name: "Front office", E164: "+15550100099"}}),
	}
	kitchen := policy.Handset{ID: "kitchen"} // no phonebook key
	chosen, err := Select(kitchen, books)
	if err != nil {
		t.Fatal(err)
	}
	if len(chosen) != 2 || chosen[0].ID != "house" || chosen[1].ID != "people" {
		t.Fatalf("default books = %v", chosen)
	}
	m, _ := Lookup("grandstream-wp826")
	out, err := RenderPhonebook(m, chosen)
	if err != nil {
		t.Fatal(err)
	}
	ab := parseBook(t, out)
	if len(ab.Groups) != 2 || ab.Groups[0].Name != "House" || ab.Groups[1].Name != "People" {
		t.Fatalf("groups = %+v", ab.Groups)
	}
	var names []string
	for _, c := range ab.Contacts {
		names = append(names, strings.TrimSpace(c.First+" "+c.Last))
	}
	joined := strings.Join(names, "|")
	for _, want := range []string{"Kitchen", "Theater", "Voicemail", "Page everyone", "Dennis Gorman", "Grandma"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in %s", want, joined)
		}
	}
	if strings.Contains(string(out), "<FirstName>Front</FirstName>") || strings.Contains(string(out), "Bridge") {
		t.Errorf("an unselected source or a pseudo-handset leaked in:\n%s", out)
	}
	// Two numbers, one person; dialable as 1NXXNXXXXXX.
	for _, c := range ab.Contacts {
		if c.First == "Dennis" && c.Last == "Gorman" {
			if len(c.Phones) != 2 || c.Phones[0].Number != "15550100001" || c.Phones[0].Type != "Home" {
				t.Errorf("Dennis = %+v", c.Phones)
			}
		}
		if c.First == "Kitchen" && (len(c.Phones) != 1 || c.Phones[0].Number != "101" || c.Group != 1) {
			t.Errorf("Kitchen = %+v group %d", c.Phones, c.Group)
		}
	}
}

func TestAnOptedInSourceShowsOnlyOnThePhoneThatAskedForIt(t *testing.T) {
	books := Books{
		"house":  HouseBook(bookHandsets),
		"people": PeopleBook(bookCallers),
		"school": SourceBook("school", "School", []policy.KnownCaller{{Name: "Front office", E164: "+15550100099"}}),
	}
	m, _ := Lookup("grandstream-wp826")
	optedIn, _ := Select(policy.Handset{ID: "kids", Phonebook: []string{"house", "school"}}, books)
	out, _ := RenderPhonebook(m, optedIn)
	if !strings.Contains(string(out), "<FirstName>Front</FirstName>") || strings.Contains(string(out), "Grandma") {
		t.Fatalf("the opted-in phone should show the source and not the people book:\n%s", out)
	}
	if _, err := Select(policy.Handset{ID: "hall", Phonebook: []string{"house", "nope"}}, books); err == nil || !strings.Contains(err.Error(), `"nope"`) {
		t.Fatalf("an unknown book must be refused by name, got %v", err)
	}
}

func TestNamesAndNumbersAreEscapedAndSplit(t *testing.T) {
	m, _ := Lookup("grandstream-wp826")
	out, _ := RenderPhonebook(m, []Book{{ID: "people", Name: "P & Q", Contacts: []Contact{{Name: "Mary Kate Moore", Numbers: []Number{{Kind: "home", Dial: "+447700900123"}}}}}})
	ab := parseBook(t, out)
	if ab.Groups[0].Name != "P & Q" {
		t.Errorf("group = %q", ab.Groups[0].Name)
	}
	c := ab.Contacts[0]
	if c.First != "Mary Kate" || c.Last != "Moore" || c.Phones[0].Number != "+447700900123" {
		t.Errorf("contact = %+v", c)
	}
	if _, err := RenderPhonebook(Model{ID: "yealink-t31p", Family: "yealink-cfg"}, nil); err == nil {
		t.Error("a family without a phonebook template must say so")
	}
}
