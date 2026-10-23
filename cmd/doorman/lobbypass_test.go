package main

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"callmemaybe/internal/accountbook"
	"callmemaybe/internal/calls"
	"callmemaybe/internal/policy"
	"callmemaybe/internal/provision"
)

const acctPasses = "acct-01jaf000000000000000000pas"

var (
	passRose = accountbook.Contact{ID: acctRose, Name: "Grandma Rose", Numbers: []accountbook.Number{{Label: "home", Number: "+15550100143"}}}
	passDana = accountbook.Contact{ID: acctDana, Name: "Coach Dana", Numbers: []accountbook.Number{{Label: "mobile", Number: "+15550100142"}}}
)

// passFixture is an account book directory and an inventory the test can
// change, as the daemon's default line would.
type passFixture struct {
	t   *testing.T
	dir string
	log *syncBuffer

	mu       sync.Mutex
	handsets []policy.Handset
	digest   string
}

// syncBuffer lets the watcher goroutine and the test share a log.
type syncBuffer struct {
	mu sync.Mutex
	b  *bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func (s *syncBuffer) Len() int { return len(s.String()) }

func newPassFixture(t *testing.T) *passFixture {
	t.Helper()
	f := &passFixture{t: t, dir: filepath.Join(t.TempDir(), "phonebook", accountbook.Dir), log: &syncBuffer{b: &bytes.Buffer{}},
		handsets: []policy.Handset{{ID: "kitchen", Endpoint: "PJSIP/kitchen", Number: 101}}, digest: "1"}
	if err := os.MkdirAll(f.dir, 0o750); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *passFixture) inventory() ([]policy.Handset, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.handsets, f.digest
}

func (f *passFixture) select_(books ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handsets = []policy.Handset{{ID: "kitchen", Endpoint: "PJSIP/kitchen", Number: 101, Phonebook: books}}
	f.digest += "+"
}

func (f *passFixture) write(b accountbook.Book) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(f.dir, accountbook.FileName(b.ID)), accountbook.Encode(b), 0o640); err != nil {
		f.t.Fatal(err)
	}
}

func (f *passFixture) writeRaw(id, data string) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(f.dir, accountbook.FileName(id)), []byte(data), 0o640); err != nil {
		f.t.Fatal(err)
	}
}

func (f *passFixture) pass(contacts ...accountbook.Contact) accountbook.Book {
	return accountbook.Book{ID: acctPasses, Revision: 1, Label: "Lobby pass", Role: accountbook.RoleLobbyPass, Contacts: contacts}
}

func (f *passFixture) open() *passList {
	return openPasses(f.dir, f.inventory, slog.New(slog.NewTextHandler(f.log, nil)))
}

func admitted(p *passList, number string) string {
	h, ok := p.Lookup(number)
	if !ok {
		return ""
	}
	return h.Name
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("never: %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestOnlyThePassBookAdmits(t *testing.T) {
	f := newPassFixture(t)
	// Rose is in a directory book the kitchen shows; Dana holds a pass and
	// is on no phone at all.
	f.write(accountbook.Book{ID: acctFamily, Revision: 1, Label: "Family", Includes: []string{"house"}, Contacts: []accountbook.Contact{passRose}})
	f.write(f.pass(passDana))
	f.select_(acctFamily)
	p := f.open()
	if got := admitted(p, "+15550100142"); got != "Coach Dana" {
		t.Fatalf("pass holder = %q, want Coach Dana", got)
	}
	if got := admitted(p, "+15550100143"); got != "" {
		t.Fatalf("a directory book admitted %q", got)
	}
	if p.Hash() != accountbook.Hash(accountbook.Encode(f.pass(passDana))) {
		t.Fatal("hash is not the pass book file's")
	}
	if !strings.Contains(f.log.String(), "lobby pass book loaded") || strings.Contains(f.log.String(), "555") {
		t.Fatalf("load log:\n%s", f.log.String())
	}
}

func TestNoAccountBooksIsNoPasses(t *testing.T) {
	f := newPassFixture(t)
	f.dir = filepath.Join(f.dir, "missing")
	p := f.open()
	if admitted(p, "+15550100142") != "" || p.Hash() != "" || f.log.Len() != 0 {
		t.Fatalf("a box with no books admitted, hashed or logged: %q", f.log.String())
	}
}

func TestPassBookHotReloadsAndKeepsTheLastGood(t *testing.T) {
	f := newPassFixture(t)
	f.write(accountbook.Book{ID: acctFamily, Revision: 1, Label: "Family", Includes: []string{"house"}, Contacts: []accountbook.Contact{passRose}})
	f.write(f.pass(passDana))
	p := f.open()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); p.watch(ctx, 5*time.Millisecond) }()
	defer func() { cancel(); <-done }()

	// An edit to the pass book is picked up without a restart.
	both := f.pass(passDana, passRose)
	both.Revision = 2
	f.write(both)
	eventually(t, "the edit admits Rose", func() bool { return admitted(p, "+15550100143") == "Grandma Rose" })

	// A refused set — here, a broken directory book — keeps the pass book.
	f.writeRaw(acctFamily, "id = ")
	eventually(t, "the refusal is logged", func() bool {
		return strings.Contains(f.log.String(), "the last good lobby pass book stays in service")
	})
	if admitted(p, "+15550100143") != "Grandma Rose" || admitted(p, "+15550100142") != "Coach Dana" {
		t.Fatal("a refused set replaced the pass book in service")
	}

	// So does an inventory that selects the pass book.
	f.write(accountbook.Book{ID: acctFamily, Revision: 2, Label: "Family", Includes: []string{"house"}})
	f.select_(acctPasses)
	time.Sleep(30 * time.Millisecond)
	if admitted(p, "+15550100143") != "Grandma Rose" {
		t.Fatal("selecting the pass book took it out of service")
	}
	f.select_(acctFamily)

	// Removing the pass book takes every pass back.
	if err := os.Remove(filepath.Join(f.dir, accountbook.FileName(acctPasses))); err != nil {
		t.Fatal(err)
	}
	eventually(t, "removal", func() bool { return admitted(p, "+15550100142") == "" })
	if p.Hash() != "" {
		t.Fatal("hash after removal")
	}
	if strings.Contains(f.log.String(), "555") {
		t.Fatalf("a number reached the log:\n%s", f.log.String())
	}
}

// A restart onto a refused set runs the last good pass book; with no copy it
// starts with no passes, and the phone answers either way.
func TestARestartOntoARefusedSetRunsTheLastGoodPassBook(t *testing.T) {
	f := newPassFixture(t)
	f.write(f.pass(passDana))
	f.open()
	if _, err := os.Stat(filepath.Join(f.dir, passLastGood)); err != nil {
		t.Fatal("no last good copy kept")
	}
	f.writeRaw(acctPasses, "id = ")
	p := f.open()
	if admitted(p, "+15550100142") != "Coach Dana" || !strings.Contains(f.log.String(), "running the last good lobby pass book") {
		t.Fatalf("restart did not run the last good copy:\n%s", f.log.String())
	}
	// The copy is not a book: nothing that lists the books reads it.
	if files, err := readBooks(f.dir); err != nil || len(files) != 1 {
		t.Fatalf("the copy was listed as a book: %v %d", err, len(files))
	}
	if err := os.Remove(filepath.Join(f.dir, passLastGood)); err != nil {
		t.Fatal(err)
	}
	p = f.open()
	if admitted(p, "+15550100142") != "" || !strings.Contains(f.log.String(), "no last good lobby pass book") {
		t.Fatal("restart with no copy admitted somebody")
	}
	// An unreadable book (a directory where a file should be) is a refusal
	// too, not a crash.
	if err := os.Remove(filepath.Join(f.dir, accountbook.FileName(acctPasses))); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(f.dir, accountbook.FileName(acctPasses)), 0o750); err != nil {
		t.Fatal(err)
	}
	if p := f.open(); admitted(p, "+15550100142") != "" || p.stampOf() == "" {
		t.Fatal("unreadable book")
	}
}

func TestWriteLastGoodPassSkipsUnchangedBytesAndReportsFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, passLastGood)
	if err := writeLastGoodPass(path, []byte("a")); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	if err := writeLastGoodPass(path, []byte("a")); err != nil {
		t.Fatal(err)
	}
	if again, _ := os.Stat(path); !again.ModTime().Equal(info.ModTime()) || info.Mode().Perm() != 0o600 {
		t.Fatal("unchanged copy was rewritten, or is not private")
	}
	if err := writeLastGoodPass(filepath.Join(dir, "missing", "x"), []byte("a")); err == nil {
		t.Fatal("write into a missing directory succeeded")
	}
}

func TestActivationReportsThePassBookHash(t *testing.T) {
	f := newPassFixture(t)
	f.write(f.pass(passDana))
	p := f.open()
	got := activationDigests(nil, p)
	if got[passActivationKey] != p.Hash() || p.Hash() == "" {
		t.Fatalf("activation: %v", got)
	}
	if _, ok := activationDigests(nil, nil)[passActivationKey]; ok {
		t.Fatal("reported a pass book with no reader")
	}
	files := map[string][]byte{
		filepath.Join(f.dir, accountbook.FileName(acctPasses)): accountbook.Encode(f.pass(passDana)),
		filepath.Join(f.dir, accountbook.FileName(acctFamily)): accountbook.Encode(accountbook.Book{ID: acctFamily, Label: "Family"}),
		"/elsewhere/policy.toml":                               []byte("x"),
	}
	if passBookHash(f.dir, files) != p.Hash() {
		t.Fatal("sync's view and the daemon disagree on the pass book")
	}
	delete(files, filepath.Join(f.dir, accountbook.FileName(acctPasses)))
	if passBookHash(f.dir, files) != "" {
		t.Fatal("found a pass book that is not there")
	}
}

// Never on a phone: even a set that somehow reached the directory with the
// pass book in it would not render it, and a valid set cannot select it.
func TestTheDirectoryNeverHoldsThePassBook(t *testing.T) {
	paths := bookFixture(t)
	paths.own = filepath.Join(filepath.Dir(paths.handsets), "phonebook", "own")
	dir := accountDir(paths.own)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, accountbook.FileName(acctPasses)), accountbook.Encode(accountbook.Book{ID: acctPasses, Label: "Lobby pass", Role: accountbook.RoleLobbyPass, Contacts: []accountbook.Contact{passDana}}), 0o640); err != nil {
		t.Fatal(err)
	}
	cache := &bookCache{paths: paths}
	books, err := cache.current()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := books[acctPasses]; ok {
		t.Fatal("the pass book is a directory book")
	}
	model, _ := provision.Lookup("grandstream-wp826")
	body, err := cache.directory("kitchen", model)
	if err != nil || strings.Contains(string(body), "Coach") {
		t.Fatalf("a pass holder reached a directory: %v", err)
	}
}

func TestCheckReportsThePassBookInCounts(t *testing.T) {
	f := newPassFixture(t)
	if out := capture(t, func() {
		if !printLobbyPass(f.dir, f.handsets, nil, nil) {
			t.Fatal("no pass book failed the check")
		}
	}); out != "" {
		t.Fatalf("no pass book printed: %q", out)
	}
	f.write(f.pass(passDana, passRose, accountbook.Contact{ID: "c-01jaf0000000000000000000ab", Name: "Spammy", Numbers: []accountbook.Number{{Number: "+15550100150"}}}))
	pol, err := policy.FromTOML([]byte("[house]\nhandsets = [\"kitchen\"]\n\n[[handsets]]\nid = \"kitchen\"\nendpoint = \"PJSIP/kitchen\"\n\n[[people]]\nname = \"Grandma\"\nnumbers = [\"+15550100143\"]\n"))
	if err != nil {
		t.Fatal(err)
	}
	set := contactSet(t, `
[[sources]]
id = "spam"
kind = "block"
path = "$DIR/spam.vcf"
`, map[string]string{"spam.vcf": "BEGIN:VCARD\nVERSION:3.0\nFN:Spam\nTEL:+15550100150\nEND:VCARD\n"})
	out := capture(t, func() {
		if !printLobbyPass(f.dir, f.handsets, []allowList{{"policy.toml", pol}}, set) {
			t.Fatal("valid pass book failed the check")
		}
	})
	for _, want := range []string{"Lobby pass: 3 holders, 3 numbers", acctPasses, "policy.toml: 1 admitted by pass; 1 already in [[people]]", "1 blocked by a contacts source"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "555") || strings.Contains(out, "Dana") {
		t.Fatalf("check printed caller data:\n%s", out)
	}

	f.writeRaw(acctFamily, "id = ")
	out = capture(t, func() {
		if printLobbyPass(f.dir, f.handsets, nil, nil) {
			t.Fatal("refused books passed the check")
		}
	})
	if !strings.Contains(out, "are refused") {
		t.Fatalf("refusal not reported:\n%s", out)
	}
	if err := os.Remove(filepath.Join(f.dir, accountbook.FileName(acctFamily))); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(f.dir, accountbook.FileName(acctFamily)), 0o750); err != nil {
		t.Fatal(err)
	}
	if out := capture(t, func() { printLobbyPass(f.dir, f.handsets, nil, nil) }); !strings.Contains(out, "cannot be read") {
		t.Fatalf("unreadable books:\n%s", out)
	}
}

func TestCallLogNamesTheLobbyPass(t *testing.T) {
	if got := who(calls.Record{Known: "Coach Dana", Via: calls.ViaLobbyPass}); got != "Coach Dana (lobby-pass)" {
		t.Fatalf("who = %q", got)
	}
	if got := who(calls.Record{Known: "Grandma", Via: calls.ViaPeople}); got != "Grandma" {
		t.Fatalf("who = %q", got)
	}
}
