package syncmachine

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"testing"

	"callmemaybe/internal/accountbook"
	"callmemaybe/internal/syncwire"
)

const passBook = "acct-01jaf000000000000000000pas"

func wirePassBook(revision string, contacts ...string) map[string]any {
	b := wireBook(passBook, revision, nil, contacts...)
	b["role"] = "lobby-pass"
	return b
}

// The pass book is delivered, hashed, echoed and replaced exactly as a
// directory book is — phonebook.put, beforeHash, one receipt — and the echo
// says which book it is.
func TestThePassBookIsDeliveredAndEchoedWithItsRole(t *testing.T) {
	m, p, _ := accountFixture(t)
	s, _ := m.Observe()
	m.Applied = func(Paths, map[string][]byte) bool { return true }
	r, err := m.Apply(proposal(t, s, put(wirePassBook("1", roseContact), "")))
	if err != nil || r.Status != syncwire.ReceiptStatusApplied {
		t.Fatalf("deliver: %s %v %v", r.Status, r.Problem, err)
	}
	data, err := os.ReadFile(bookPath(p, passBook))
	if err != nil || !bytes.HasPrefix(data, []byte(accountbook.PassHeader)) || !bytes.Contains(data, []byte(`role = "lobby-pass"`)) {
		t.Fatalf("pass book not written as one: %v\n%s", err, data)
	}
	s, err = m.Observe()
	if err != nil {
		t.Fatal(err)
	}
	b := echoed(s, passBook)
	if b == nil || b.Role == nil || *b.Role != syncwire.PhonebookRoleLobbyPass || *b.Hash != accountbook.Hash(data) || *b.Revision != "1" {
		t.Fatalf("echo: %+v", b)
	}
	if d := echoed(s, familyBook); d != nil {
		t.Fatal("echoed a book nobody delivered")
	}
	// Replaced only by a delivery that names what the box holds.
	if r, _ := m.Apply(proposal(t, s, put(wirePassBook("2", roseContact, danaContact), ""))); r.Status != syncwire.ReceiptStatusConflict {
		t.Fatalf("replacement without beforeHash: %s", r.Status)
	}
	if r, _ := m.Apply(proposal(t, s, put(wirePassBook("2", danaContact), *b.Hash))); r.Status != syncwire.ReceiptStatusApplied {
		t.Fatalf("replacement: %s %v", r.Status, r.Problem)
	}
	// And removed like any book.
	s, _ = m.Observe()
	if r, _ := m.Apply(proposal(t, s, map[string]any{"name": "phonebook.remove", "book": passBook, "revision": "2"})); r.Status != syncwire.ReceiptStatusApplied {
		t.Fatalf("remove: %s %v", r.Status, r.Problem)
	}
	if _, err := os.Stat(bookPath(p, passBook)); !os.IsNotExist(err) {
		t.Fatal("pass book not removed")
	}
}

// A directory book's echo carries no role.
func TestADirectoryBookEchoesNoRole(t *testing.T) {
	m, _, _ := accountFixture(t)
	s, _ := m.Observe()
	if r, _ := m.Apply(proposal(t, s, put(wireBook(familyBook, "1", []string{"house"}, roseContact), ""))); r.Status != syncwire.ReceiptStatusSaved {
		t.Fatalf("deliver: %s", r.Status)
	}
	s, _ = m.Observe()
	if b := echoed(s, familyBook); b == nil || b.Role != nil {
		t.Fatalf("directory book echo: %+v", b)
	}
}

// Never on a phone: selecting it, assigning it or including it is refused,
// and nothing is written.
func TestThePassBookCannotReachADirectory(t *testing.T) {
	m, p, _ := accountFixture(t)
	s, _ := m.Observe()
	if r, _ := m.Apply(proposal(t, s, put(wirePassBook("1", roseContact), ""))); r.Status != syncwire.ReceiptStatusSaved {
		t.Fatalf("deliver: %s", r.Status)
	}
	s, _ = m.Observe()
	handsets, _ := os.ReadFile(p.Handsets)
	for _, tc := range []struct {
		name string
		list []map[string]any
	}{
		{"select", []map[string]any{{"name": "phonebook.select", "handset": "kitchen", "books": []string{"house", passBook}}}},
		{"assign", []map[string]any{{"name": "phonebook.assign", "handset": "kitchen", "phonebook": passBook}}},
		{"include", []map[string]any{put(wireBook(familyBook, "1", []string{passBook}, roseContact), "")}},
		{"pass book with includes", []map[string]any{put(func() map[string]any {
			b := wirePassBook("2", roseContact)
			b["includes"] = []string{"house"}
			return b
		}(), "")}},
	} {
		r, err := m.Apply(proposal(t, s, tc.list...))
		if err != nil || r.Status != syncwire.ReceiptStatusRejected || *r.Problem != syncwire.Invalid {
			t.Fatalf("%s: %s %v %v", tc.name, r.Status, r.Problem, err)
		}
	}
	if after, _ := os.ReadFile(p.Handsets); !bytes.Equal(after, handsets) {
		t.Fatal("a refused selection was written")
	}
	if _, err := os.Stat(bookPath(p, familyBook)); !os.IsNotExist(err) {
		t.Fatal("a book including the pass book was written")
	}
}

// One per box: a second pass book is refused whole.
func TestABoxHoldsOnePassBook(t *testing.T) {
	m, p, _ := accountFixture(t)
	s, _ := m.Observe()
	second := wireBook(charlieBook, "1", nil, danaContact)
	second["role"] = "lobby-pass"
	r, _ := m.Apply(proposal(t, s, put(wirePassBook("1", roseContact), ""), put(second, "")))
	if r.Status != syncwire.ReceiptStatusRejected || *r.Problem != syncwire.Invalid {
		t.Fatalf("two pass books: %s", r.Status)
	}
	if _, err := os.Stat(bookPath(p, passBook)); !os.IsNotExist(err) {
		t.Fatal("the first pass book was written")
	}
}

// The pass book takes more contacts than a directory book; the wire carries
// up to MaxPassContacts, and a directory book is still held to its own limit.
func TestThePassBookTakesMoreContactsThanADirectoryBook(t *testing.T) {
	m, p, _ := accountFixture(t)
	s, _ := m.Observe()
	contacts := []any{}
	for i := 0; i < accountbook.MaxContacts+500; i++ {
		contacts = append(contacts, map[string]any{"id": fmt.Sprintf("c-01jaf%021d", i), "name": "H",
			"numbers": []any{map[string]any{"label": "", "number": fmt.Sprintf("+1555%07d", i)}}})
	}
	b := wirePassBook("1")
	b["contacts"] = contacts
	if r, err := m.Apply(proposal(t, s, put(b, ""))); err != nil || r.Status != syncwire.ReceiptStatusSaved {
		t.Fatalf("large pass book: %s %v %v", r.Status, r.Problem, err)
	}
	if _, err := os.Stat(bookPath(p, passBook)); err != nil {
		t.Fatal("large pass book not written")
	}
	d := wireBook(familyBook, "1", nil)
	d["contacts"] = contacts
	if r, _ := m.Apply(proposal(t, s, put(d, ""))); r.Status != syncwire.ReceiptStatusRejected {
		t.Fatalf("a directory book over its limit: %s", r.Status)
	}
	// Past the pass book's own limit the item does not decode at all.
	over := strings.Repeat(`{"id":"c-01jaf000000000000000000r5e","name":"H","numbers":[{"label":"","number":"+15550100100"}]},`, accountbook.MaxPassContacts)
	raw := `{"name":"phonebook.put","book":{"id":"` + passBook + `","label":"P","revision":"1","role":"lobby-pass","includes":[],"contacts":[` + over + `{"id":"c-01jaf000000000000000000r5e","name":"H","numbers":[{"label":"","number":"+15550100100"}]}]}}`
	var v syncwire.Verb
	if syncwire.Decode("Verb", []byte(raw), &v) == nil {
		t.Fatal("a book over the wire's contact limit decoded")
	}
	// An unknown role does not decode either.
	if syncwire.Decode("Verb", []byte(`{"name":"phonebook.put","book":{"id":"`+passBook+`","label":"P","revision":"1","role":"admit-all","includes":[],"contacts":[]}}`), &v) == nil {
		t.Fatal("an unknown role decoded")
	}
}
