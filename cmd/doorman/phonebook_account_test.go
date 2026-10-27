package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"callmemaybe/internal/accountbook"
	"callmemaybe/internal/provision"
)

const (
	acctFamily  = "acct-01jaf000000000000000000fam"
	acctCharlie = "acct-01jaf000000000000000000chr"
	acctRose    = "c-01jaf000000000000000000r5e"
	acctDana    = "c-01jaf0000000000000000000da"
)

func TestDirectoryComposesAccountBooksAndKeepsLastGood(t *testing.T) {
	paths := bookFixture(t)
	paths.own = filepath.Join(filepath.Dir(paths.handsets), "phonebook", "own")
	dir := accountDir(paths.own)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	rose := accountbook.Contact{ID: acctRose, Name: "Grandma Rose", Numbers: []accountbook.Number{{Label: "home", Number: "+15550100143"}}}
	dana := accountbook.Contact{ID: acctDana, Name: "Coach Dana", Numbers: []accountbook.Number{{Label: "mobile", Number: "+15550100142"}}}
	write := func(b accountbook.Book) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, accountbook.FileName(b.ID)), accountbook.Encode(b), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	write(accountbook.Book{ID: acctFamily, Revision: 1, Label: "Family", Includes: []string{"house"}, Contacts: []accountbook.Contact{rose}})
	write(accountbook.Book{ID: acctCharlie, Revision: 1, Label: "Charlie", Includes: []string{acctFamily}, Contacts: []accountbook.Contact{dana, rose}})
	changed := strings.Replace(bookHandsetsTOML, `id = "kitchen"`, "id = \"kitchen\"\nphonebook = [\""+acctCharlie+"\"]", 1)
	if err := os.WriteFile(paths.handsets, []byte(changed), 0o600); err != nil {
		t.Fatal(err)
	}
	cache := &bookCache{paths: paths}
	model, _ := provision.Lookup("grandstream-wp826")
	body, err := cache.directory("kitchen", model)
	if err != nil {
		t.Fatal(err)
	}
	xml := string(body)
	if strings.Count(xml, "<FirstName>Grandma</FirstName>") != 1 || !strings.Contains(xml, "Coach") || !strings.Contains(xml, "<name>House</name>") ||
		strings.Contains(xml, "<name>Family</name>") || strings.Contains(xml, "<name>People</name>") {
		t.Fatalf("composed directory:\n%s", xml)
	}
	// A delivered edit to Family reaches the phone through Charlie.
	write(accountbook.Book{ID: acctFamily, Revision: 2, Label: "Family", Includes: []string{"house"},
		Contacts: []accountbook.Contact{rose, {ID: "c-01jaf0000000000000000000ab", Name: "Uncle Ben", Numbers: []accountbook.Number{{Number: "+15550100144"}}}}})
	body, err = cache.directory("kitchen", model)
	if err != nil || !strings.Contains(string(body), "Uncle") || !strings.Contains(string(body), "<name>Family</name>") {
		t.Fatalf("included edit did not reach the directory: %v", err)
	}
	// A broken copy keeps the last good directory up.
	if err := os.WriteFile(filepath.Join(dir, accountbook.FileName(acctFamily)), []byte("id = "), 0o640); err != nil {
		t.Fatal(err)
	}
	body, err = cache.directory("kitchen", model)
	if err != nil || !strings.Contains(string(body), "Uncle") {
		t.Fatal("invalid account book replaced the last good directory")
	}
	fresh := &bookCache{paths: paths}
	if _, err := fresh.current(); err == nil || !strings.Contains(err.Error(), accountbook.FileName(acctFamily)) {
		t.Fatalf("invalid account book not reported: %v", err)
	}
}
