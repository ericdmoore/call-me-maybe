package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"callmemaybe/internal/books"
	"callmemaybe/internal/lobby"
	"callmemaybe/internal/policy"
	"callmemaybe/internal/render"
)

// `doorman check` on a Dial-a-Story line (#155): the shelves, the limits, and
// on the box the catalog and the dialplan they need.

const shelfLine = `
[line]
label = "Midbury Library"
menu  = "front"

[house]
handsets = ["desk"]

[[handsets]]
id = "desk"
endpoint = "PJSIP/desk"

[[shelves]]
id     = "stories"
titles = ["little-red-hen", "three-billy-goats", "the-gingerbread-man"]

[[shelves]]
id     = "today"
titles = ["little-red-hen"]

[[shelves]]
id     = "vault"
titles = ["private-recording"]

[listening]
max_minutes = 15

[[menus]]
id = "front"

  [[menus.choices]]
  digit     = "5"
  shelf     = "stories"
  challenge = true

  [[menus.choices]]
  digit            = "6"
  story_of_the_day = "today"
`

func TestCheckDescribesShelvesAndLimits(t *testing.T) {
	pol, err := policy.FromTOML([]byte(shelfLine))
	if err != nil {
		t.Fatalf("policy: %v", err)
	}
	pointSounds(t, filepath.Join(t.TempDir(), "absent"))
	out := capture(t, func() { describeLine("./policy.library.toml", pol, false) })
	for _, want := range []string{
		"5  shelf stories",
		"browses shelf stories (3 titles) — behind the challenge",
		"6  story_of_the_day today",
		"today's story from shelf today (little-red-hen), then goodbye — no challenge",
		"Dial-a-Story",
		"calls end at 15m0s, warned 1m0s before; 1 listener at once",
		"all our listening lines are busy",
		"no bookmarks",
		"shelf stories: 3 titles, offered — little-red-hen, three-billy-goats, the-gingerbread-man",
		"shelf vault: 1 title, offered by no menu a caller can reach",
		`! shelf "vault" is not offered by any menu`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("check should say %q:\n%s", want, out)
		}
	}
}

// pointLibrary aims the catalog and dialplan checks at temporary directories.
func pointLibrary(t *testing.T, library, etc string) {
	t.Helper()
	savedLibrary, savedEtc := booksLibrary, asteriskDir
	t.Cleanup(func() { booksLibrary, asteriskDir = savedLibrary, savedEtc })
	booksLibrary, asteriskDir = library, etc
}

// installCatalog writes a prepared catalog of the named books.
func installCatalog(t *testing.T, dir string, ids ...string) {
	t.Helper()
	c := books.Catalog{Schema: 1}
	for _, id := range ids {
		c.Books = append(c.Books, books.Book{ID: id, Title: id, Language: "en", Origin: "cli", Revision: strings.Repeat("a", 64),
			Chapters: []books.Chapter{{Title: "One", Audio: "chapter-001.sln16", Seconds: 1}}})
	}
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "catalog.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCheckComparesShelvesWithTheCatalog(t *testing.T) {
	pol, err := policy.FromTOML([]byte(shelfLine))
	if err != nil {
		t.Fatal(err)
	}
	results := []checkedLine{{LineFile: policy.LineFile{Name: "library", Path: "./policy.library.toml"}, pol: pol}}
	etc := t.TempDir()

	// No catalog to read — a workstation, CI: a warning, never a failure.
	pointLibrary(t, filepath.Join(t.TempDir(), "absent"), filepath.Join(etc, "absent"))
	var ok bool
	out := capture(t, func() { ok = printShelves(results) })
	if !ok || !strings.Contains(out, "could not read the audiobook catalog") {
		t.Fatalf("ok=%v:\n%s", ok, out)
	}

	// A catalog without one shelf title: refused, naming the line, shelf and
	// title.
	library := t.TempDir()
	installCatalog(t, library, "little-red-hen", "three-billy-goats", "the-gingerbread-man")
	pointLibrary(t, library, filepath.Join(etc, "absent"))
	out = capture(t, func() { ok = printShelves(results) })
	if ok || !strings.Contains(out, `✗ line library shelf "vault" names "private-recording"`) {
		t.Fatalf("ok=%v:\n%s", ok, out)
	}

	// Every title there.
	installCatalog(t, library, "little-red-hen", "three-billy-goats", "the-gingerbread-man", "private-recording")
	out = capture(t, func() { ok = printShelves(results) })
	if !ok || !strings.Contains(out, "✓ every shelf title is in the catalog (4 books installed)") {
		t.Fatalf("ok=%v:\n%s", ok, out)
	}

	// On the box, the context listeners are released into must be installed.
	pointLibrary(t, library, etc)
	out = capture(t, func() { ok = printShelves(results) })
	if !ok || !strings.Contains(out, "has no [cmm-listen]") {
		t.Fatalf("no warning for a missing [cmm-listen] (ok=%v):\n%s", ok, out)
	}
	if err := os.WriteFile(filepath.Join(etc, "extensions_books.conf"), []byte("[handsets-internal](+)\n\n["+render.ListenContext+"]\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	out = capture(t, func() { ok = printShelves(results) })
	if !ok || strings.Contains(out, "cmm-listen") {
		t.Fatalf("warned with the rendered dialplan installed (ok=%v):\n%s", ok, out)
	}

	// A line with no shelves says nothing at all.
	plain, _ := policy.FromTOML([]byte("[house]\nhandsets = [\"desk\"]\n[[handsets]]\nid = \"desk\"\nendpoint = \"PJSIP/desk\"\n"))
	out = capture(t, func() { ok = printShelves([]checkedLine{{pol: plain}}) })
	if !ok || out != "" {
		t.Fatalf("a line without shelves printed:\n%s", out)
	}
}

// The lobby, the renderer and the AGI each keep their own copy of what binds
// them, because none imports another; these hold the copies together.
func TestTheListeningHandoffAgreesAcrossPackages(t *testing.T) {
	if lobby.ListenContext != render.ListenContext || lobby.ListenGroupCategory != render.ListenGroupCategory {
		t.Fatalf("lobby releases to [%s] counting %s; render generates [%s] counting %s",
			lobby.ListenContext, lobby.ListenGroupCategory, render.ListenContext, render.ListenGroupCategory)
	}
	// (render's own tests hold the generated context to render.ListenContext.)
	// A shelf title is valid in policy.toml exactly when the catalog could
	// hold it.
	for _, id := range []string{"little-red-hen", "a", "9lives", "x_y", strings.Repeat("a", 64),
		strings.Repeat("a", 65), "-lead", "Upper", "sp ace", "../up", ""} {
		src := "[house]\nhandsets = [\"desk\"]\n[[handsets]]\nid = \"desk\"\nendpoint = \"PJSIP/desk\"\n" +
			"[[shelves]]\nid = \"s\"\ntitles = [\"" + id + "\"]\n"
		_, err := policy.FromTOML([]byte(src))
		if accepted := err == nil; accepted != books.ValidID(id) {
			t.Errorf("title %q: policy accepts=%v, catalog accepts=%v (%v)", id, accepted, books.ValidID(id), err)
		}
	}
}
