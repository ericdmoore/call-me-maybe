package books

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func menuCatalog(n int) Catalog {
	c := Catalog{Schema: 1}
	for i := 0; i < n; i++ {
		c.Books = append(c.Books, Book{ID: fmt.Sprintf("book-%d", i), Title: fmt.Sprintf("Story %d", i), Language: "en", Origin: "cli", Preview: true, Revision: strings.Repeat("a", 64), Chapters: []Chapter{{Title: "Opening", Audio: "chapter-001.sln16", Seconds: 1}}})
	}
	return c
}

func TestMenuPagesAndPortableKeys(t *testing.T) {
	for _, n := range []int{1, 9, 10, 24} {
		c := menuCatalog(n)
		texts := menuTexts("/local", c)
		var titles []string
		for _, b := range c.Books {
			titles = append(titles, filepath.Join(bookDir("/box", b), "title"))
		}
		for page := 0; page < (n+8)/9; page++ {
			text := texts[pageKey("/box", "books", titles, page)]
			entries := 9
			if n-page*9 < entries {
				entries = n - page*9
			}
			if strings.Count(text, "press ") != entries || !strings.Contains(text, "Star to repeat. Zero to go back.") {
				t.Fatalf("bad menu: %s", text)
			}
			if n <= 9 && strings.Contains(text, "Page ") {
				t.Fatal("single page should not announce a page count")
			}
			if n > 9 && !strings.Contains(text, "Pound ") {
				t.Fatal("missing pagination hint")
			}
		}
		if !strings.Contains(texts[filepath.Join(c.Books[0].ID+"-"+c.Books[0].Revision, "title")], "Preview recording") {
			t.Fatal("preview label missing")
		}
	}
}

func TestMenuBundlePublicationValidationAndCache(t *testing.T) {
	root := t.TempDir()
	c := menuCatalog(2)
	renders := 0
	render := func(text, path string) error { renders++; return os.WriteFile(path, []byte{0, 1, 2, 3}, 0600) }
	if clips, err := loadMenus(root, c); err != nil || clips != nil {
		t.Fatal("legacy library should work")
	}
	if err := buildMenus(root, c, "Samantha", render); err != nil {
		t.Fatal(err)
	}
	clips, err := loadMenus(root, c)
	if err != nil || len(clips) != len(menuTexts(root, c)) {
		t.Fatal("incomplete bundle", err)
	}
	count := renders
	if err := buildMenus(root, c, "Samantha", render); err != nil || renders != count {
		t.Fatal("cache missed", err)
	}
	before, _ := os.ReadFile(filepath.Join(root, "menus.json"))
	failure := errors.New("render failed")
	if err := buildMenus(root, c, "Another voice", func(string, string) error { return failure }); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(root, "menus.json"))
	if string(before) != string(after) {
		t.Fatal("failed render published")
	}
	if err := buildMenus(root, c, "Invalid output", func(_, path string) error { return os.WriteFile(path, []byte{0}, 0600) }); err == nil {
		t.Fatal("odd PCM accepted")
	}
	c.Books[0], c.Books[1] = c.Books[1], c.Books[0]
	if _, err := loadMenus(root, c); err == nil {
		t.Fatal("stale order accepted")
	}
	c.Books[0], c.Books[1] = c.Books[1], c.Books[0]
	var bundle menuBundle
	if err := readJSON(filepath.Join(root, "menus.json"), &bundle); err != nil {
		t.Fatal(err)
	}
	bundle.Clips["tip"] = "../../escape"
	if err := writeJSON(filepath.Join(root, "menus.json"), bundle); err != nil {
		t.Fatal(err)
	}
	if _, err := loadMenus(root, c); err == nil {
		t.Fatal("unsafe path accepted")
	}
	if err := os.WriteFile(filepath.Join(root, "menus.json"), before, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(clips["tip"] + ".sln16"); err != nil {
		t.Fatal(err)
	}
	if _, err := loadMenus(root, c); err == nil {
		t.Fatal("missing audio accepted")
	}
	if err := os.WriteFile(filepath.Join(root, "menus.json"), []byte("broken"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadMenus(root, c); err == nil {
		t.Fatal("bad JSON accepted")
	}
}

func TestWholePageBargeInAndConsistentVoice(t *testing.T) {
	m, tr := testMenu(t, func(string) string { return result('2') })
	c := menuCatalog(2)
	if err := buildMenus(m.root, c, "Samantha", func(_, path string) error { return os.WriteFile(path, []byte{0, 0}, 0644) }); err != nil {
		t.Fatal(err)
	}
	var err error
	m.clips, err = loadMenus(m.root, c)
	if err != nil {
		t.Fatal(err)
	}
	var titles []string
	for _, b := range c.Books {
		titles = append(titles, filepath.Join(bookDir(m.root, b), "title"))
	}
	page := 0
	if index, err := m.browse("books", titles, &page); err != nil || index != 1 {
		t.Fatal("whole-page barge-in", index, err)
	}
	if _, err := m.title(titles[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := m.saySpeed(15); err != nil {
		t.Fatal(err)
	}
	if _, err := m.prompt("tip"); err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(tr.text(), "\n") {
		if !strings.Contains(line, "/menu-audio/") {
			t.Fatalf("mixed voice: %s", line)
		}
	}
}

func TestSayMenuOfflineAndFailures(t *testing.T) {
	root := t.TempDir()
	if err := PrepareMenus(context.Background(), root, ""); err == nil {
		t.Fatal("empty voice")
	}
	if err := PrepareMenus(context.Background(), root, "Samantha"); err == nil {
		t.Fatal("missing catalog")
	}
	bin := t.TempDir()
	t.Setenv("PATH", bin)
	output := filepath.Join(root, "out.sln16")
	if err := sayMenu(context.Background(), "Samantha", "Hello", output); err == nil {
		t.Fatal("missing say")
	}
	if err := os.WriteFile(filepath.Join(bin, "say"), []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := sayMenu(context.Background(), "Samantha", "Hello", output); err == nil {
		t.Fatal("missing ffmpeg")
	}
	// Exercise the real command boundary without requiring a Mac in Linux CI.
	if err := os.WriteFile(filepath.Join(bin, "ffmpeg"), []byte("#!/bin/sh\nfor arg; do output=\"$arg\"; done\nprintf '\\000\\000' > \"$output\"\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := publishJSON(filepath.Join(root, "catalog.json"), menuCatalog(1), 0644); err != nil {
		t.Fatal(err)
	}
	if err := PrepareMenus(context.Background(), root, "Samantha"); err != nil {
		t.Fatal(err)
	}
	if _, err := loadMenus(root, menuCatalog(1)); err != nil {
		t.Fatal(err)
	}
}

func TestShortTipAndFullHelpOnStar(t *testing.T) {
	m, tr := testMenu(t, func(cmd string) string {
		if strings.Contains(cmd, "/short-tip\"") {
			return result('*')
		}
		if strings.Contains(cmd, "/full-help\"") {
			return result('0')
		}
		return result(0)
	})
	m.clips = map[string]string{"tip": "/short-tip", "controls": "/full-help"}
	b := menuCatalog(1).Books[0]
	p := Position{Speed: 10}
	if err := m.listen(b, &p); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tr.text(), "/short-tip") || !strings.Contains(tr.text(), "/full-help") || strings.Contains(tr.text(), "/play-") {
		t.Fatal("help key routing failed")
	}
}
