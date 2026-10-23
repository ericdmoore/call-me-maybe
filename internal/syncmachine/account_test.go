package syncmachine

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"callmemaybe/internal/accountbook"
	"callmemaybe/internal/syncwire"
	"github.com/google/uuid"
)

const (
	familyBook  = "acct-01jaf000000000000000000fam"
	charlieBook = "acct-01jaf000000000000000000chr"
	roseContact = "c-01jaf000000000000000000r5e"
	danaContact = "c-01jaf0000000000000000000da"
)

func accountFixture(t *testing.T) (*Machine, Paths, string) {
	t.Helper()
	m, p, state := fixture(t)
	p.Phonebooks = filepath.Join(filepath.Dir(p.Policy), "phonebook")
	if err := os.MkdirAll(p.Phonebooks, 0o750); err != nil {
		t.Fatal(err)
	}
	m.paths = p
	return m, p, state
}

func wireBook(id, revision string, includes []string, contacts ...string) map[string]any {
	people := map[string]map[string]any{
		roseContact: {"id": roseContact, "name": "Grandma Rose", "numbers": []any{map[string]any{"label": "home", "number": "+15550100143"}}},
		danaContact: {"id": danaContact, "name": "Coach Dana", "numbers": []any{map[string]any{"label": "mobile", "number": "+15550100142"}}},
	}
	list := []any{}
	for _, c := range contacts {
		list = append(list, people[c])
	}
	if includes == nil {
		includes = []string{}
	}
	return map[string]any{"id": id, "label": strings.ToUpper(id[len(id)-3:]), "revision": revision, "includes": includes, "contacts": list}
}

func put(book map[string]any, before string) map[string]any {
	v := map[string]any{"name": "phonebook.put", "book": book}
	if before != "" {
		v["beforeHash"] = before
	}
	return v
}

// proposal builds a queue item with an ordered verbs list (or one verb).
func proposal(t *testing.T, s syncwire.Snapshot, list ...map[string]any) syncwire.QueueItem {
	t.Helper()
	item := map[string]any{"id": uuid.NewString(), "base": map[string]any{"generation": s.Generation, "revision": s.Revision}, "notAfter": time.Now().Add(time.Hour).UnixMilli(), "createdAt": time.Now().UnixMilli(), "author": map[string]any{"kind": "person", "person": uuid.NewString()}}
	if len(list) == 1 {
		item["verb"] = list[0]
	} else {
		item["verbs"] = list
	}
	raw, _ := json.Marshal(item)
	var q syncwire.QueueItem
	if err := syncwire.Decode("QueueItem", raw, &q); err != nil {
		t.Fatalf("decode: %v\n%s", err, raw)
	}
	return q
}

func bookPath(p Paths, id string) string {
	return filepath.Join(p.Phonebooks, accountbook.Dir, accountbook.FileName(id))
}

func echoed(s syncwire.Snapshot, id string) *syncwire.Phonebook {
	for i, b := range s.Configuration.Phonebooks {
		if b.Id == id {
			return &s.Configuration.Phonebooks[i]
		}
	}
	return nil
}

func TestDeliverTwoBooksAndSelectThemInOneProposal(t *testing.T) {
	m, p, _ := accountFixture(t)
	s, err := m.Observe()
	if err != nil {
		t.Fatal(err)
	}
	q := proposal(t, s,
		put(wireBook(familyBook, "1", []string{"house"}, roseContact), ""),
		put(wireBook(charlieBook, "4", []string{familyBook}, danaContact, roseContact), ""),
		map[string]any{"name": "phonebook.select", "handset": "kitchen", "books": []string{charlieBook}})
	m.Applied = func(Paths, map[string][]byte) bool { return true }
	r, err := m.Apply(q)
	if err != nil || r.Status != syncwire.ReceiptStatusApplied {
		t.Fatalf("one receipt for the list: %s %v %v", r.Status, r.Problem, err)
	}
	handsets, _ := os.ReadFile(p.Handsets)
	if !bytes.Contains(handsets, []byte(`phonebook = ["`+charlieBook+`"] # keep this selection comment`)) {
		t.Fatalf("selection not written:\n%s", handsets)
	}
	data, err := os.ReadFile(bookPath(p, charlieBook))
	if err != nil || !bytes.HasPrefix(data, []byte(accountbook.Header)) {
		t.Fatal("managed file missing its header")
	}
	s, err = m.Observe()
	if err != nil {
		t.Fatal(err)
	}
	b := echoed(s, charlieBook)
	if b == nil || b.Origin == nil || *b.Origin != syncwire.Account || *b.Revision != "4" || *b.Hash != accountbook.Hash(data) ||
		len(b.Contacts) != 2 || b.Contacts[0] != danaContact || b.Includes[0] != familyBook {
		t.Fatalf("echo: %+v", b)
	}
	if h := echoed(s, "house"); h == nil || *h.Origin != syncwire.Box {
		t.Fatal("box books lost their origin")
	}
	roses := 0
	for _, c := range s.Configuration.Contacts {
		if c.Id == roseContact {
			roses++
		}
	}
	if roses != 1 {
		t.Fatalf("a contact in two books echoed %d times", roses)
	}
	// Replaying the same item repeats nothing.
	again, err := m.Apply(q)
	if err != nil || again.Status != syncwire.ReceiptStatusApplied {
		t.Fatal("replay changed the answer")
	}
}

func TestARefusedVerbWritesNothing(t *testing.T) {
	m, p, _ := accountFixture(t)
	s, _ := m.Observe()
	for _, tc := range []struct {
		name string
		last map[string]any
		want syncwire.ReceiptProblem
	}{
		{"unknown handset", map[string]any{"name": "phonebook.select", "handset": "attic", "books": []string{familyBook}}, syncwire.Invalid},
		{"unknown box book", map[string]any{"name": "phonebook.select", "handset": "kitchen", "books": []string{"school"}}, syncwire.Invalid},
		{"repeated book", map[string]any{"name": "phonebook.select", "handset": "kitchen", "books": []string{familyBook, familyBook}}, syncwire.Invalid},
		{"book the box does not hold", map[string]any{"name": "phonebook.select", "handset": "kitchen", "books": []string{charlieBook}}, syncwire.Invalid},
		{"verb the box does not run", map[string]any{"name": "backup.run"}, syncwire.Unsupported},
	} {
		r, err := m.Apply(proposal(t, s, put(wireBook(familyBook, "1", nil, roseContact), ""), tc.last))
		if err != nil || r.Status != syncwire.ReceiptStatusRejected || *r.Problem != tc.want {
			t.Fatalf("%s: %s %v %v", tc.name, r.Status, r.Problem, err)
		}
		if _, err := os.Stat(bookPath(p, familyBook)); !os.IsNotExist(err) {
			t.Fatalf("%s: the first verb's book was written", tc.name)
		}
	}
	// A book that fails the shared rules is refused too.
	bad := wireBook(familyBook, "1", nil, roseContact)
	bad["label"] = " "
	if r, _ := m.Apply(proposal(t, s, put(bad, ""))); r.Status != syncwire.ReceiptStatusRejected || *r.Problem != syncwire.Invalid {
		t.Fatalf("invalid book: %s", r.Status)
	}
	huge := wireBook(familyBook, "9999999999999999999", nil, roseContact)
	if r, _ := m.Apply(proposal(t, s, put(huge, ""))); r.Status != syncwire.ReceiptStatusRejected {
		t.Fatalf("revision overflow: %s", r.Status)
	}
	// Both verb and verbs, or neither, has no meaning.
	q := proposal(t, s, put(wireBook(familyBook, "1", nil, roseContact), ""))
	q.Verbs = &[]syncwire.Verb{*q.Verb}
	if r, _ := m.Apply(q); r.Status != syncwire.ReceiptStatusRejected || *r.Problem != syncwire.Invalid {
		t.Fatalf("verb and verbs: %s", r.Status)
	}
	q = proposal(t, s, put(wireBook(familyBook, "1", nil, roseContact), ""))
	q.Verb = nil
	if r, _ := m.Apply(q); r.Status != syncwire.ReceiptStatusRejected {
		t.Fatalf("neither: %s", r.Status)
	}
	var out syncwire.QueueItem
	if syncwire.Decode("QueueItem", []byte(`{"id":"`+uuid.NewString()+`","base":{"generation":"`+s.Generation+`","revision":"1"},"notAfter":1,"createdAt":1,"author":{"kind":"person","person":"`+uuid.NewString()+`"},"verbs":[{"name":"phonebook.shell"}]}`), &out) == nil {
		t.Fatal("accepted an unknown verb in a list")
	}
}

func TestAssignKeepsAnAccountBooksOwnContacts(t *testing.T) {
	m, p, _ := accountFixture(t)
	s, _ := m.Observe()
	if r, err := m.Apply(proposal(t, s, put(wireBook(familyBook, "1", []string{"house"}, roseContact), ""))); err != nil || r.Status != syncwire.ReceiptStatusSaved {
		t.Fatalf("put: %s %v", r.Status, err)
	}
	s, _ = m.Observe()
	r, err := m.Apply(proposal(t, s, map[string]any{"name": "phonebook.assign", "handset": "kitchen", "phonebook": familyBook}))
	if err != nil || r.Status != syncwire.ReceiptStatusSaved {
		t.Fatalf("assign: %s %v %v", r.Status, r.Problem, err)
	}
	data, _ := os.ReadFile(p.Handsets)
	if !bytes.Contains(data, []byte(`phonebook = ["`+familyBook+`"]`)) {
		t.Fatalf("a book with contacts and includes was flattened to its includes:\n%s", data)
	}
	// Copying a handset's selection still copies its list.
	s, _ = m.Observe()
	var selection string
	for _, b := range s.Configuration.Phonebooks {
		if strings.HasPrefix(b.Id, "selection:") {
			selection = b.Id
		}
	}
	os.WriteFile(p.Handsets, []byte(strings.Replace(handsetsFixture, `["house", "people"]`, `["people", "house"]`, 1)), 0o600)
	s, _ = m.Observe()
	for _, b := range s.Configuration.Phonebooks {
		if strings.HasPrefix(b.Id, "selection:") {
			selection = b.Id
		}
	}
	if r, err := m.Apply(proposal(t, s, map[string]any{"name": "phonebook.assign", "handset": "kitchen", "phonebook": selection})); err != nil || r.Status != syncwire.ReceiptStatusSaved {
		t.Fatalf("selection copy: %s %v", r.Status, err)
	}
	data, _ = os.ReadFile(p.Handsets)
	if !bytes.Contains(data, []byte(`phonebook = ["people", "house"]`)) {
		t.Fatalf("selection not copied:\n%s", data)
	}
	// An account book the box does not hold is no assignment.
	if r, _ := m.Apply(proposal(t, s, map[string]any{"name": "phonebook.assign", "handset": "kitchen", "phonebook": charlieBook})); r.Status != syncwire.ReceiptStatusRejected {
		t.Fatalf("missing book assigned: %s", r.Status)
	}
}

func TestDeliveryNeverOverwritesAHandEditUnlessNamed(t *testing.T) {
	m, p, _ := accountFixture(t)
	s, _ := m.Observe()
	m.Apply(proposal(t, s, put(wireBook(familyBook, "1", nil, roseContact), "")))
	path := bookPath(p, familyBook)
	delivered, _ := os.ReadFile(path)
	// A newer revision must say what it replaces.
	s, _ = m.Observe()
	if r, _ := m.Apply(proposal(t, s, put(wireBook(familyBook, "2", nil, roseContact, danaContact), ""))); r.Status != syncwire.ReceiptStatusConflict {
		t.Fatalf("replaced without beforeHash: %s", r.Status)
	}
	edited := bytes.Replace(delivered, []byte("Grandma Rose"), []byte("Nana Rose"), 1)
	os.WriteFile(path, edited, 0o640)
	s, _ = m.Observe()
	if r, _ := m.Apply(proposal(t, s, put(wireBook(familyBook, "2", nil, roseContact, danaContact), fileHash(delivered)))); r.Status != syncwire.ReceiptStatusConflict {
		t.Fatalf("overwrote a hand edit: %s", r.Status)
	}
	if now, _ := os.ReadFile(path); !bytes.Equal(now, edited) {
		t.Fatal("hand edit lost")
	}
	// "Replace with the account version" names the edited bytes.
	r, err := m.Apply(proposal(t, s, put(wireBook(familyBook, "2", nil, roseContact, danaContact), fileHash(edited))))
	if err != nil || r.Status != syncwire.ReceiptStatusSaved {
		t.Fatalf("named replacement: %s %v", r.Status, err)
	}
	// A beforeHash for a book that is not there is a conflict too.
	s, _ = m.Observe()
	if r, _ := m.Apply(proposal(t, s, put(wireBook(charlieBook, "1", nil, danaContact), fileHash(edited)))); r.Status != syncwire.ReceiptStatusConflict {
		t.Fatalf("beforeHash for an absent book: %s", r.Status)
	}
}

func TestBookDeliveryDoesNotConflictWithAPendingEdit(t *testing.T) {
	m, p, _ := accountFixture(t)
	s, _ := m.Observe()
	pending := queue(t, s, "policy.setSchedule")
	if r, _ := m.Apply(proposal(t, s, put(wireBook(familyBook, "1", nil, roseContact), ""))); r.Status != syncwire.ReceiptStatusSaved {
		t.Fatalf("put: %s", r.Status)
	}
	if after, _ := m.Latest(); after.Revision == s.Revision {
		t.Fatal("delivery did not change the snapshot")
	}
	if r, err := m.Apply(pending); err != nil || r.Status != syncwire.ReceiptStatusSaved {
		t.Fatalf("a delivered book turned a pending edit into %s %v", r.Status, err)
	}
	// An edit to the box's own files still does.
	s, _ = m.Observe()
	stale := queue(t, s, "phonebook.assign")
	os.WriteFile(p.Handsets, []byte(strings.Replace(handsetsFixture, "Kitchen", "Galley", 1)), 0o600)
	if r, _ := m.Apply(stale); r.Status != syncwire.ReceiptStatusConflict {
		t.Fatalf("local edit ignored: %s", r.Status)
	}
	// An unknown base is judged strictly.
	if m.sameBase("123456", s) {
		t.Fatal("unknown base accepted")
	}
}

func TestRemoveDeletesOnlyAnUnusedBookAtItsRevision(t *testing.T) {
	m, p, _ := accountFixture(t)
	s, _ := m.Observe()
	m.Apply(proposal(t, s,
		put(wireBook(familyBook, "3", nil, roseContact), ""),
		map[string]any{"name": "phonebook.select", "handset": "kitchen", "books": []string{familyBook, "house"}}))
	s, _ = m.Observe()
	remove := map[string]any{"name": "phonebook.remove", "book": familyBook, "revision": "3"}
	if r, _ := m.Apply(proposal(t, s, remove)); r.Status != syncwire.ReceiptStatusRejected || *r.Problem != syncwire.Invalid {
		t.Fatalf("removed a book in use: %s", r.Status)
	}
	if r, _ := m.Apply(proposal(t, s, map[string]any{"name": "phonebook.remove", "book": familyBook, "revision": "2"})); r.Status != syncwire.ReceiptStatusConflict {
		t.Fatalf("removed another revision: %s", r.Status)
	}
	r, err := m.Apply(proposal(t, s, map[string]any{"name": "phonebook.select", "handset": "kitchen", "books": []string{"house"}}, remove))
	if err != nil || r.Status != syncwire.ReceiptStatusSaved {
		t.Fatalf("select away then remove: %s %v %v", r.Status, r.Problem, err)
	}
	if _, err := os.Stat(bookPath(p, familyBook)); !os.IsNotExist(err) {
		t.Fatal("book not removed")
	}
	s, _ = m.Observe()
	if echoed(s, familyBook) != nil {
		t.Fatal("removed book still echoed")
	}
	// Removing what is not there is already done.
	if r, _ := m.Apply(proposal(t, s, remove)); r.Status != syncwire.ReceiptStatusSaved {
		t.Fatalf("absent book: %s", r.Status)
	}
}

func TestDirectoryLimitRefusesAnOversizedComposition(t *testing.T) {
	m, _, _ := accountFixture(t)
	s, _ := m.Observe()
	book := wireBook(familyBook, "1", []string{"house"})
	contacts := []any{}
	for i := 0; i < accountbook.DefaultDirectoryLimit; i++ {
		id := "c-01jaf" + strings.Repeat("0", 21-len(itoa(i))) + itoa(i)
		contacts = append(contacts, map[string]any{"id": id, "name": "Someone", "numbers": []any{map[string]any{"label": "", "number": "+15550100100"}}})
	}
	book["contacts"] = contacts
	// Delivering is fine; pointing a phone at it would overflow its directory.
	r, _ := m.Apply(proposal(t, s, put(book, ""), map[string]any{"name": "phonebook.select", "handset": "kitchen", "books": []string{familyBook}}))
	if r.Status != syncwire.ReceiptStatusRejected || *r.Problem != syncwire.Invalid {
		t.Fatalf("oversized directory: %s", r.Status)
	}
}

func itoa(i int) string { b, _ := json.Marshal(i); return string(b) }

func TestPutWithoutAPhonebookDirectoryIsUnsupported(t *testing.T) {
	m, _, _ := fixture(t)
	s, _ := m.Observe()
	for _, v := range []map[string]any{put(wireBook(familyBook, "1", nil, roseContact), ""), {"name": "phonebook.remove", "book": familyBook, "revision": "1"}} {
		if r, _ := m.Apply(proposal(t, s, v)); r.Status != syncwire.ReceiptStatusRejected || *r.Problem != syncwire.Unsupported {
			t.Fatalf("no phonebook directory: %s", r.Status)
		}
	}
}

func TestObserverRejectsABrokenManagedBookByName(t *testing.T) {
	m, p, _ := accountFixture(t)
	s, _ := m.Observe()
	m.Apply(proposal(t, s, put(wireBook(familyBook, "1", nil, roseContact), "")))
	good, err := m.Observe()
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(bookPath(p, familyBook), []byte("id = "), 0o640)
	// A writer's temporary file and other files are not books.
	os.WriteFile(filepath.Join(p.Phonebooks, accountbook.Dir, ".sync-123"), []byte("x"), 0o640)
	os.WriteFile(filepath.Join(p.Phonebooks, accountbook.Dir, "README"), []byte("x"), 0o640)
	_, err = m.Observe()
	if err == nil || m.Rejection() == nil || m.Rejection().File != accountbook.FileName(familyBook) {
		t.Fatalf("broken book: %v %+v", err, m.Rejection())
	}
	if last, _ := m.Latest(); last.Revision != good.Revision {
		t.Fatal("lost last good snapshot")
	}
	// A handset naming a book the box does not hold is the inventory's problem.
	os.Remove(bookPath(p, familyBook))
	os.WriteFile(p.Handsets, []byte(strings.Replace(handsetsFixture, `["house", "people"]`, `["`+charlieBook+`"]`, 1)), 0o600)
	if _, err = m.Observe(); err == nil || m.Rejection().File != "handsets.toml" {
		t.Fatalf("dangling handset book: %v %+v", err, m.Rejection())
	}
}

func TestPartWayCrashResumesTheRemainingWrites(t *testing.T) {
	m, p, state := accountFixture(t)
	s, _ := m.Observe()
	original, _ := os.ReadFile(p.Handsets)
	q := proposal(t, s,
		put(wireBook(familyBook, "1", nil, roseContact), ""),
		map[string]any{"name": "phonebook.select", "handset": "kitchen", "books": []string{familyBook}})
	r, err := m.Apply(q)
	if err != nil || r.Status != syncwire.ReceiptStatusSaved {
		t.Fatal(err)
	}
	waiting, ready, err := m.AwaitingApply()
	if err != nil || len(waiting) != 1 || len(ready) != 0 {
		t.Fatalf("awaiting: %v %v %v", waiting, ready, err)
	}
	m.Applied = func(Paths, map[string][]byte) bool { return true }
	if _, ready, _ := m.AwaitingApply(); len(ready) != 1 {
		t.Fatal("multi-file command not ready once applied")
	}
	m.Applied = nil
	// The process died after the book landed and before the inventory did.
	m.db.Exec("UPDATE commands SET receipt=NULL WHERE id=?", q.Id)
	os.WriteFile(p.Handsets, original, 0o600)
	m.Close()
	m, err = Open(state, "ac409cd3-8105-4200-9a32-42a064a98bb0", p)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	r, err = m.Apply(q)
	if err != nil || r.Status != syncwire.ReceiptStatusSaved {
		t.Fatalf("resume: %s %v %v", r.Status, r.Problem, err)
	}
	if data, _ := os.ReadFile(p.Handsets); !bytes.Contains(data, []byte(familyBook)) {
		t.Fatal("remaining write not finished")
	}
	// If a target moved to something else meanwhile, the answer is a conflict.
	s, _ = m.Observe()
	q = proposal(t, s, map[string]any{"name": "phonebook.select", "handset": "kitchen", "books": []string{"house"}})
	m.Apply(q)
	m.db.Exec("UPDATE commands SET receipt=NULL WHERE id=?", q.Id)
	os.WriteFile(p.Handsets, []byte(strings.Replace(handsetsFixture, "Kitchen", "Galley", 1)), 0o600)
	if r, _ := m.Apply(q); r.Status != syncwire.ReceiptStatusConflict {
		t.Fatalf("moved target: %s", r.Status)
	}
}

func TestALedgerFromAnEarlierReleaseStillReplays(t *testing.T) {
	m, p, state := fixture(t)
	s, _ := m.Observe()
	q := queue(t, s, "policy.setSchedule")
	r, err := m.Apply(q)
	if err != nil || r.Status != syncwire.ReceiptStatusSaved {
		t.Fatal(err)
	}
	// An earlier release kept only the single-target columns, and had no
	// writes column at all.
	m.db.Exec("UPDATE commands SET receipt=NULL WHERE id=?", q.Id)
	m.db.Exec("ALTER TABLE commands DROP COLUMN writes")
	m.Close()
	m, err = Open(state, "ac409cd3-8105-4200-9a32-42a064a98bb0", p)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	waiting, _, err := m.AwaitingApply()
	if err != nil || len(waiting) != 0 {
		t.Fatal("an unanswered command counted as awaiting")
	}
	replayed, err := m.Apply(q)
	if err != nil || replayed.Status != syncwire.ReceiptStatusSaved || *replayed.Revision != *r.Revision {
		t.Fatalf("legacy intent not recognised: %s %v", replayed.Status, err)
	}
	if waiting, _, _ := m.AwaitingApply(); len(waiting) != 1 {
		t.Fatal("legacy saved receipt not awaited")
	}
}
