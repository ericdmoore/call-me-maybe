package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"callmemaybe/internal/policy"
	"callmemaybe/internal/provision"
)

const bookHandsetsTOML = `
[[handsets]]
id = "kitchen"
label = "Kitchen"
endpoint = "PJSIP/kitchen"
number = 101
password_env = "HANDSET_KITCHEN_PASSWORD"
mac = "ec:74:d7:88:a2:54"
model = "grandstream-wp826"

[[handsets]]
id = "theater"
label = "Theater"
endpoint = "PJSIP/theater"
number = 102
password_env = "HANDSET_THEATER_PASSWORD"
`

const bookPolicyTOML = `
[house]
handsets = ["kitchen", "theater"]

[[people]]
name = "Grandma"
numbers = ["+1 555 010 0003"]

[[extensions]]
pin = "482913"
label = "Family"
handsets = ["kitchen"]
`

func bookFixture(t *testing.T) bookPaths {
	t.Helper()
	dir := t.TempDir()
	paths := bookPaths{
		handsets: filepath.Join(dir, "handsets.toml"), policy: filepath.Join(dir, "policy.toml"),
		contacts: filepath.Join(dir, "contacts.toml"), countryCode: "1",
	}
	if err := os.WriteFile(paths.handsets, []byte(bookHandsetsTOML), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.policy, []byte(bookPolicyTOML), 0o600); err != nil {
		t.Fatal(err)
	}
	return paths
}

// No contacts.toml: the house and the people, and nothing else — the
// compatibility gate holds for the phone book too.
func TestBooksWithoutAContactsFileAreHouseAndPeople(t *testing.T) {
	books, err := loadBooks(bookFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(books) != 2 || books["house"].ID != "house" || books["people"].ID != "people" {
		t.Fatalf("books = %v", books)
	}
	if len(books["people"].Contacts) != 1 || books["people"].Contacts[0].Name != "Grandma" {
		t.Fatalf("people = %+v", books["people"].Contacts)
	}
	m, _ := provision.Lookup("grandstream-wp826")
	out, err := renderBooks(policy.Handset{ID: "kitchen"}, m, books)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "<FirstName>Grandma</FirstName>") || !strings.Contains(string(out), "<phonenumber>15550100003</phonenumber>") {
		t.Fatalf("Grandma should be dialable:\n%s", out)
	}
}

// The directory re-reads on change: a name added to [[people]] is in the
// next book with no render run — and a broken edit keeps the last good one.
func TestBookCacheFollowsThePolicyFileAndSurvivesABadEdit(t *testing.T) {
	paths := bookFixture(t)
	cache := &bookCache{paths: paths}
	first, err := cache.current()
	if err != nil {
		t.Fatal(err)
	}
	if len(first["people"].Contacts) != 1 {
		t.Fatalf("people = %+v", first["people"].Contacts)
	}
	time.Sleep(20 * time.Millisecond) // a distinct mtime on coarse filesystems
	if err := os.WriteFile(paths.policy, []byte(bookPolicyTOML+"\n[[people]]\nname = \"Uncle\"\nnumbers = [\"+1 555 010 0004\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := cache.current()
	if err != nil {
		t.Fatal(err)
	}
	if len(second["people"].Contacts) != 2 {
		t.Fatalf("the new name should be in the book: %+v", second["people"].Contacts)
	}
	time.Sleep(20 * time.Millisecond)
	if err := os.WriteFile(paths.policy, []byte("this is not toml = = ="), 0o600); err != nil {
		t.Fatal(err)
	}
	third, err := cache.current()
	if err != nil || len(third["people"].Contacts) != 2 {
		t.Fatalf("a bad edit must keep the last good book: %v %+v", err, third["people"].Contacts)
	}
}
