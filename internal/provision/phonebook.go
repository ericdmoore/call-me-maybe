package provision

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"sort"
	"strings"

	"callmemaybe/internal/policy"
)

// The phone book: what a handset shows under "Contacts". Three kinds of
// book, chosen per handset by `phonebook = [...]` in the inventory:
//
//	house   — the other rooms and the feature codes, from handsets.toml
//	people  — the allow-list, from [[people]]
//	<id>    — one contacts.toml source, by its id, for a phone that opted in
//
// Absent means house and people: the rooms and the family, never a whole
// address book. The books are rendered into one vendor file per handset
// beside its configuration, and served by the always-on directory.

// Contact is one entry in a book.
type Contact struct {
	Name    string
	Numbers []Number
}

// Number is one way to reach a contact. Kind is the vendor-neutral label
// (work, home, cell); Dial is what the phone sends when it is picked.
type Number struct {
	Kind string
	Dial string
}

// Book is a named group of contacts, in display order.
type Book struct {
	ID       string
	Name     string
	Contacts []Contact
}

// Books is every book this house can render, by id.
type Books map[string]Book

// HouseBook is the rooms and the feature codes: every registering handset
// with its internal number, then what the dialplan answers.
func HouseBook(handsets []policy.Handset) Book {
	b := Book{ID: "house", Name: "House"}
	rooms := make([]policy.Handset, 0, len(handsets))
	for _, h := range handsets {
		if h.Number > 0 && strings.HasPrefix(h.Endpoint, "PJSIP/") {
			rooms = append(rooms, h)
		}
	}
	sort.Slice(rooms, func(i, j int) bool { return rooms[i].Number < rooms[j].Number })
	for _, h := range rooms {
		b.Contacts = append(b.Contacts, Contact{Name: h.Label, Numbers: []Number{{Kind: "work", Dial: fmt.Sprint(h.Number)}}})
	}
	for _, f := range houseFeatures {
		b.Contacts = append(b.Contacts, Contact{Name: f.name, Numbers: []Number{{Kind: "work", Dial: f.dial}}})
	}
	return b
}

// houseFeatures are the dialplan's own numbers, as RUNBOOK's table lists
// them. The registry (s14) will own this list; until then it is here.
var houseFeatures = []struct{ name, dial string }{
	{"Ring everyone", "100"},
	{"Page everyone", "500"},
	{"Family conference", "600"},
	{"Voicemail", VoicemailCode},
}

// PeopleBook is the allow-list, one contact per name with every number.
func PeopleBook(callers []policy.KnownCaller) Book {
	b := Book{ID: "people", Name: "People"}
	index := map[string]int{}
	for _, c := range callers {
		i, ok := index[c.Name]
		if !ok {
			i = len(b.Contacts)
			index[c.Name] = i
			b.Contacts = append(b.Contacts, Contact{Name: c.Name})
		}
		b.Contacts[i].Numbers = append(b.Contacts[i].Numbers, Number{Kind: "home", Dial: DialString(c.E164)})
	}
	return b
}

// SourceBook is one contacts.toml source as a book: the entries that
// source supplied names for. entries are (name, e164) pairs already
// filtered to that source by the caller, so this package never imports the
// contacts package and the directory stays a rendering concern.
func SourceBook(id, name string, entries []policy.KnownCaller) Book {
	b := PeopleBook(entries)
	b.ID, b.Name = id, name
	return b
}

// DialString is what the phone sends for an E.164 number: North American
// numbers as 1NXXNXXXXXX, which every outbound pattern in the dialplan
// accepts; anything else with its plus, for a dialplan that routes it.
func DialString(e164 string) string {
	if strings.HasPrefix(e164, "+1") && len(e164) == 12 {
		return e164[1:]
	}
	return e164
}

// Select picks a handset's books in the order it named them. An unknown id
// is an error rather than an empty group: a phone quietly showing nothing
// is the failure the inventory exists to prevent.
func Select(h policy.Handset, books Books) ([]Book, error) {
	var out []Book
	for _, id := range h.Books() {
		b, ok := books[id]
		if !ok {
			return nil, fmt.Errorf("handset %q: phonebook %q is not house, people, or a contacts.toml source id", h.ID, id)
		}
		out = append(out, b)
	}
	return out, nil
}

// PhonebookFileName is the rendered file beside the phone's configuration.
func PhonebookFileName(id string) string { return id + "-phonebook.xml" }

// RenderPhonebook writes the books in the phone's own format.
func RenderPhonebook(m Model, books []Book) ([]byte, error) {
	switch m.Family {
	case "grandstream-xml":
		return renderGrandstreamPhonebook(books), nil
	default:
		return nil, fmt.Errorf("%s: no phonebook template", m.ID)
	}
}

// renderGrandstreamPhonebook is the AddressBook XML the GRP/WP/DP families
// download: groups first, then contacts naming their group by number.
func renderGrandstreamPhonebook(books []Book) []byte {
	var buf bytes.Buffer
	buf.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	buf.WriteString("<AddressBook>\n  <version>1</version>\n")
	for i, b := range books {
		fmt.Fprintf(&buf, "  <pbgroup>\n    <id>%d</id>\n    <name>%s</name>\n  </pbgroup>\n", i+1, esc(b.Name))
	}
	n := 0
	for i, b := range books {
		for _, c := range b.Contacts {
			n++
			first, last := splitName(c.Name)
			fmt.Fprintf(&buf, "  <Contact>\n    <id>%d</id>\n    <FirstName>%s</FirstName>\n    <LastName>%s</LastName>\n    <Frequent>0</Frequent>\n", n, esc(first), esc(last))
			for _, num := range c.Numbers {
				fmt.Fprintf(&buf, "    <Phone type=\"%s\">\n      <phonenumber>%s</phonenumber>\n      <accountindex>0</accountindex>\n    </Phone>\n", grandstreamKind(num.Kind), esc(num.Dial))
			}
			fmt.Fprintf(&buf, "    <Primary>0</Primary>\n    <Group>%d</Group>\n  </Contact>\n", i+1)
		}
	}
	buf.WriteString("</AddressBook>\n")
	return buf.Bytes()
}

func grandstreamKind(kind string) string {
	switch kind {
	case "home":
		return "Home"
	case "cell", "mobile":
		return "Cell"
	default:
		return "Work"
	}
}

// splitName gives a phone with first/last fields something sensible: the
// last word is the surname, the rest the given name; one word is given.
func splitName(name string) (first, last string) {
	words := strings.Fields(name)
	switch len(words) {
	case 0:
		return "", ""
	case 1:
		return words[0], ""
	default:
		return strings.Join(words[:len(words)-1], " "), words[len(words)-1]
	}
}

func esc(s string) string {
	var buf bytes.Buffer
	_ = xml.EscapeText(&buf, []byte(s))
	return buf.String()
}
