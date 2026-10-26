package books

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
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
	if err := sayMenu(context.Background(), "Samantha", "Hello", output); err == nil || !strings.Contains(err.Error(), "say not found (macOS only)") || !errors.Is(err, exec.ErrNotFound) {
		t.Fatal("missing say", err)
	}
	if err := checkVoice(context.Background(), "Samantha"); err == nil || !strings.Contains(err.Error(), "say not found (macOS only)") {
		t.Fatal("missing say", err)
	}
	// The fake answers `say -v '?'` as macOS does, including a name with spaces.
	fakeSay := "#!/bin/sh\nif [ \"$1\" = -v ] && [ \"$2\" = '?' ]; then\n" +
		"printf 'Samantha            en_US    # Hello, my name is Samantha.\\n'\n" +
		"printf 'Eddy (English (UK)) en_GB    # Hello! My name is Eddy.\\n'\nfi\nexit 0\n"
	if err := os.WriteFile(filepath.Join(bin, "say"), []byte(fakeSay), 0755); err != nil {
		t.Fatal(err)
	}
	if err := sayMenu(context.Background(), "Samantha", "Hello", output); err == nil || !strings.Contains(err.Error(), "ffmpeg not found") {
		t.Fatal("missing ffmpeg", err)
	}
	for _, voice := range []string{"Samantha", "samantha", "Eddy (English (UK))"} {
		if err := checkVoice(context.Background(), voice); err != nil {
			t.Fatal(voice, err)
		}
	}
	for _, voice := range []string{"Samanta", "Eddy", "en_US", "English (UK))"} {
		if err := checkVoice(context.Background(), voice); err == nil || !strings.Contains(err.Error(), "not installed") {
			t.Fatal("uninstalled voice accepted:", voice)
		}
	}
	// Exercise the real command boundary without requiring a Mac in Linux CI.
	if err := os.WriteFile(filepath.Join(bin, "ffmpeg"), []byte("#!/bin/sh\nfor arg; do output=\"$arg\"; done\nprintf '\\000\\000' > \"$output\"\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := publishJSON(filepath.Join(root, "catalog.json"), menuCatalog(1), 0644); err != nil {
		t.Fatal(err)
	}
	if err := PrepareMenus(context.Background(), root, "Samanta"); err == nil {
		t.Fatal("misspelt voice rendered")
	}
	if _, err := os.Stat(filepath.Join(root, "menus.json")); !os.IsNotExist(err) {
		t.Fatal("misspelt voice published a bundle")
	}
	if err := PrepareMenus(context.Background(), root, "Samantha"); err != nil {
		t.Fatal(err)
	}
	if _, err := loadMenus(root, menuCatalog(1)); err != nil {
		t.Fatal(err)
	}
}

func TestVoiceNamesFromSayList(t *testing.T) {
	list := "Albert              en_US    # Hello! My name is Albert.\n" +
		"Eddy (English (UK)) en_GB    # Hello! My name is Eddy.\n" +
		"Grandma (Français (Canada)) fr_CA    # Bonjour, je m’appelle Grandma.\n" +
		"\n   \nmalformed\n"
	got := strings.Join(voiceNames(list), "|")
	if got != "Albert|Eddy (English (UK))|Grandma (Français (Canada))" {
		t.Fatal(got)
	}
}

func TestSpeechCommandsInTitlesAreSpokenLiterally(t *testing.T) {
	c := menuCatalog(1)
	c.Books[0].Title = "Quiet [[slnc 60000]] [[[volm 0]]]"
	c.Books[0].Chapters[0].Title = "Chapter [[rate 1]]"
	for key, text := range menuTexts("/box", c) {
		if strings.Contains(text, "[[") || strings.Contains(text, "]]") {
			t.Fatalf("%s carries a speech command: %s", key, text)
		}
	}
	title := menuTexts("/box", c)[filepath.Join(c.Books[0].ID+"-"+c.Books[0].Revision, "title")]
	if !strings.Contains(title, "slnc 60000") {
		t.Fatal("title text lost:", title)
	}
}

func TestPrepareRetiresOnlyStaleMenus(t *testing.T) {
	root := t.TempDir()
	c := menuCatalog(1)
	if err := publishJSON(filepath.Join(root, "catalog.json"), c, 0644); err != nil {
		t.Fatal(err)
	}
	if err := retireStaleMenus(root); err != nil {
		t.Fatal("no bundle", err)
	}
	if err := buildMenus(root, c, "Samantha", func(_, path string) error { return os.WriteFile(path, []byte{0, 0}, 0644) }); err != nil {
		t.Fatal(err)
	}
	if err := retireStaleMenus(root); err != nil {
		t.Fatal(err)
	}
	if _, err := loadMenus(root, c); err != nil {
		t.Fatal("current bundle retired", err)
	}
	if err := publishJSON(filepath.Join(root, "catalog.json"), menuCatalog(2), 0644); err != nil {
		t.Fatal(err)
	}
	if err := retireStaleMenus(root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "menus.json")); !os.IsNotExist(err) {
		t.Fatal("stale bundle left in place")
	}
	if _, err := os.Stat(filepath.Join(root, "menus.json.stale")); err != nil {
		t.Fatal("stale bundle not kept aside", err)
	}
	if err := os.WriteFile(filepath.Join(root, "menus.json"), []byte("broken"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := retireStaleMenus(root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "menus.json")); !os.IsNotExist(err) {
		t.Fatal("unreadable bundle left in place")
	}
}

func TestRenderSweepsInterruptedTempFolders(t *testing.T) {
	root := t.TempDir()
	leftover := filepath.Join(root, "menu-audio", ".render-123")
	if err := os.MkdirAll(leftover, 0755); err != nil {
		t.Fatal(err)
	}
	if err := buildMenus(root, menuCatalog(1), "Samantha", func(_, path string) error { return os.WriteFile(path, []byte{0, 0}, 0644) }); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(leftover); !os.IsNotExist(err) {
		t.Fatal("interrupted render left behind")
	}
}

// A stale or incomplete bundle must degrade to the compatibility prompts
// rather than failing the call or reading titles against other numbers.
func TestRunFallsBackFromUnusableMenus(t *testing.T) {
	corruptions := map[string]func(root string, c Catalog){
		"stale": func(root string, c Catalog) {
			if err := buildMenus(root, menuCatalog(2), "Samantha", func(_, path string) error { return os.WriteFile(path, []byte{0, 0}, 0644) }); err != nil {
				t.Fatal(err)
			}
		},
		"missing clip": func(root string, c Catalog) {
			if err := buildMenus(root, c, "Samantha", func(_, path string) error { return os.WriteFile(path, []byte{0, 0}, 0644) }); err != nil {
				t.Fatal(err)
			}
			clips, err := loadMenus(root, c)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(clips["books"] + ".sln16"); err != nil {
				t.Fatal(err)
			}
		},
		"unreadable": func(root string, c Catalog) {
			if err := os.WriteFile(filepath.Join(root, "menus.json"), []byte("broken"), 0644); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, corrupt := range corruptions {
		t.Run(name, func(t *testing.T) {
			spool := t.TempDir()
			root := filepath.Join(spool, "cmm-books", "library")
			if err := os.MkdirAll(root, 0o755); err != nil {
				t.Fatal(err)
			}
			b := fixtureBook(t, root, 0.1)
			corrupt(root, Catalog{Schema: 1, Books: []Book{b}})
			var log strings.Builder
			diagnostics = &log
			t.Cleanup(func() { diagnostics = os.Stderr })
			a, tr := fakeAGI(t, func(cmd string) string {
				switch {
				case strings.Contains(cmd, "CHANNEL(endpoint)"), strings.Contains(cmd, "CMM_BOOKS_HANDSET"):
					return "200 result=1 (test-phone)"
				case strings.Contains(cmd, "ASTSPOOLDIR"):
					return "200 result=1 (" + spool + ")"
				case strings.Contains(cmd, "/books\""):
					return result('0')
				}
				return result(0)
			})
			if err := Run(context.Background(), a); err != nil {
				t.Fatal(err)
			}
			text := tr.text()
			if !strings.Contains(text, "/prompts-v1/books\"") || !strings.Contains(text, "/prompts-v1/goodbye\"") || strings.Contains(text, "/menu-audio/") {
				t.Fatalf("compatibility prompts not used:\n%s", text)
			}
			if !strings.Contains(log.String(), "using compatibility prompts") || strings.Contains(log.String(), b.Title) {
				t.Fatalf("diagnostic: %q", log.String())
			}
		})
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

func TestStarPoundDuringWholePageAudio(t *testing.T) {
	keys := []int{'*', '#', '4', '#', '2'}
	m, tr := testMenu(t, func(cmd string) string {
		if len(keys) == 0 {
			t.Error("unexpected command")
			return result('0')
		}
		k := keys[0]
		keys = keys[1:]
		return result(k)
	})
	c := menuCatalog(120)
	if err := buildMenus(m.root, c, "Zoe (Premium)", func(_, p string) error { return os.WriteFile(p, []byte{0, 0}, 0644) }); err != nil {
		t.Fatal(err)
	}
	var err error
	m.clips, err = loadMenus(m.root, c)
	if err != nil {
		t.Fatal(err)
	}
	titles := make([]string, len(c.Books))
	for i, b := range c.Books {
		titles[i] = filepath.Join(bookDir(m.root, b), "title")
	}
	page := 0
	got, err := m.browse("books", titles, &page)
	if err != nil || got != 28 || page != 3 || len(keys) != 0 {
		t.Fatalf("got %d page %d remaining %v err %v", got, page, keys, err)
	}
	for _, line := range strings.Split(tr.text(), "\n") {
		if !strings.Contains(line, "/menu-audio/") && !strings.HasPrefix(line, "WAIT FOR DIGIT") {
			t.Fatalf("unexpected fallback %s", line)
		}
	}
	text := menuTexts(m.root, c)[pageKey(m.root, "books", titles, 0)]
	if !strings.Contains(text, "Star then pound to choose a page.") {
		t.Fatal("missing shortcut instruction")
	}
}
