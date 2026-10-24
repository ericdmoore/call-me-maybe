package books

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Outside listeners (#155): the shelf is the whole of what they can reach,
// nothing they hear is remembered, and the limit warns once and then ends.

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// library writes a prepared catalog of the named books, each one chapter of
// the given length, under <books>/library, and returns <books>.
func library(t *testing.T, seconds float64, ids ...string) string {
	t.Helper()
	books := t.TempDir()
	root := filepath.Join(books, "library")
	var c Catalog
	c.Schema = 1
	for i, id := range ids {
		b := Book{ID: id, Title: "Title " + id, Language: "en", Origin: "cli", Revision: strings.Repeat(fmt.Sprint(i%10), 64),
			Chapters: []Chapter{{Title: "Chapter one", Audio: "chapter-001.sln16", Seconds: seconds}}}
		if err := os.MkdirAll(bookDir(root, b), 0o755); err != nil {
			t.Fatal(err)
		}
		pcm(t, filepath.Join(bookDir(root, b), b.Chapters[0].Audio), seconds)
		c.Books = append(c.Books, b)
	}
	if err := writeJSON(filepath.Join(root, "catalog.json"), c); err != nil {
		t.Fatal(err)
	}
	return books
}

// farLimit is a limit nobody reaches in a test.
func farLimit() *limit { return newLimit(time.Now, time.Hour, time.Minute) }

// The shelf is built from its own titles: announced in the shelf's order,
// every key indexes into it alone, and a title the catalog holds but the
// shelf does not is never so much as named — whichever key is pressed.
func TestAShelfReachesOnlyItsOwnTitles(t *testing.T) {
	books := library(t, 1, "alpha", "bravo", "vault")
	// 3 is past the shelf's end; # opens the page picker, where 9 then # is
	// a page that does not exist and 0 goes back; then 1 chooses.
	keys := []int{'3', '#', '9', '#', '0', '1'}
	bookMenus := 0
	a, tr := fakeAGI(t, func(cmd string) string {
		switch {
		case strings.Contains(cmd, "/last\""), strings.Contains(cmd, "/pick-page\""), strings.HasPrefix(cmd, "WAIT FOR DIGIT"):
			if len(keys) == 0 {
				return result(0)
			}
			k := keys[0]
			keys = keys[1:]
			return result(k)
		case strings.Contains(cmd, "/book\""):
			bookMenus++
			return result('0')
		}
		return result(0)
	})
	if err := listenIn(context.Background(), a, books, "shelf", []string{"bravo", "alpha"}, farLimit()); err != nil {
		t.Fatal(err)
	}
	text := tr.text()
	if strings.Contains(text, "vault-") {
		t.Fatalf("a title off the shelf was reached:\n%s", text)
	}
	b := strings.Index(text, "bravo-")
	al := strings.Index(text, "alpha-")
	if b < 0 || al < 0 || b > al {
		t.Fatalf("the shelf was not announced in its own order:\n%s", text)
	}
	// Key 1 chose the shelf's first title — bravo — not the catalog's: the
	// book menu opens with its title.
	lines := strings.Split(text, "\n")
	opened := ""
	for i, l := range lines {
		if strings.Contains(l, "/book\"") && i > 0 {
			opened = lines[i-1]
			break
		}
	}
	if bookMenus != 1 || !strings.Contains(opened, "bravo-") {
		t.Fatalf("key 1 did not open the shelf's first title (book menus %d, opened %q):\n%s", bookMenus, opened, text)
	}
	if !strings.HasSuffix(text, "goodbye\" \"0123456789*#\"") {
		t.Fatalf("leaving the shelf did not say goodbye:\n%s", text)
	}
	if files, _ := filepath.Glob(filepath.Join(books, "progress", "*.json")); len(files) != 0 {
		t.Fatalf("an outside caller left a bookmark: %v", files)
	}
}

// A shelf title the catalog no longer has is left out, not substituted.
func TestAShelfTitleMissingFromTheCatalogIsLeftOut(t *testing.T) {
	c := Catalog{Schema: 1, Books: []Book{{ID: "alpha"}, {ID: "vault"}}}
	got := shelfOf(c, []string{"ghost", "alpha"})
	if len(got) != 1 || got[0].ID != "alpha" {
		t.Fatalf("shelf = %+v", got)
	}
	if got := shelfOf(c, []string{"ghost"}); len(got) != 0 {
		t.Fatalf("an all-missing shelf became %+v", got)
	}
}

// Every variable doorman sets is required, and a missing or malformed one
// refuses the call — never the whole library, never no limit — before
// anything is read from the spool.
func TestListenRefusesWhatDoormanDidNotSet(t *testing.T) {
	good := map[string]string{
		"CMM_LISTEN_MODE": "shelf", "CMM_LISTEN_TITLES": "alpha,bravo",
		"CMM_LISTEN_SECONDS": "1200", "CMM_LISTEN_WARNING": "60", "ASTSPOOLDIR": "/nonexistent-spool",
	}
	for name, value := range map[string]string{
		"CMM_LISTEN_TITLES":  "",
		"CMM_LISTEN_SECONDS": "",
		"CMM_LISTEN_WARNING": "",
		"CMM_LISTEN_MODE":    "everything",
	} {
		vars := map[string]string{}
		for k, v := range good {
			vars[k] = v
		}
		vars[name] = value
		check(t, name+"="+value, vars)
	}
	for _, titles := range []string{"../vault", "alpha,,bravo", "Alpha"} {
		vars := map[string]string{}
		for k, v := range good {
			vars[k] = v
		}
		vars["CMM_LISTEN_TITLES"] = titles
		check(t, "titles "+titles, vars)
	}
	vars := map[string]string{}
	for k, v := range good {
		vars[k] = v
	}
	vars["CMM_LISTEN_MODE"] = "story" // a story is exactly one title
	check(t, "story of two", vars)
}

func check(t *testing.T, name string, vars map[string]string) {
	t.Helper()
	a, tr := fakeAGI(t, func(cmd string) string {
		for k, v := range vars {
			if strings.Contains(cmd, "${"+k+"}") {
				return "200 result=1 (" + v + ")"
			}
		}
		return result(0)
	})
	if err := RunListen(context.Background(), a); err == nil {
		t.Errorf("%s: accepted", name)
	}
	if strings.Contains(tr.text(), "STREAM FILE") {
		t.Errorf("%s: played something before refusing:\n%s", name, tr.text())
	}
}

// paced answers a STREAM FILE of a playback window as Asterisk would: the
// whole file played, the clock advanced by its length.
func paced(t *testing.T, c *clock, cmd string) (string, bool) {
	t.Helper()
	if !strings.HasPrefix(cmd, "STREAM FILE \"") || !strings.Contains(cmd, "/play-") {
		return "", false
	}
	path := strings.SplitN(strings.TrimPrefix(cmd, "STREAM FILE \""), "\"", 2)[0] + ".sln16"
	st, err := os.Stat(path)
	if err != nil {
		t.Errorf("streamed a window that is not there: %v", err)
		return result(-1), true
	}
	samples := st.Size() / 2
	c.advance(time.Duration(samples) * time.Second / sampleRate)
	return fmt.Sprintf("200 result=0 endpos=%d", samples), true
}

// The story of the day starts at once, plays its title, and ends the call —
// no browsing, no help first, no bookmark.
func TestStoryOfTheDayPlaysItsTitleAndEnds(t *testing.T) {
	requireAudio(t, "ffmpeg")
	books := library(t, 1, "alpha", "vault")
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	a, tr := fakeAGI(t, func(cmd string) string {
		if r, ok := paced(t, c, cmd); ok {
			return r
		}
		return result(0)
	})
	if err := listenIn(context.Background(), a, books, "story", []string{"alpha"}, newLimit(c.now, time.Hour, time.Minute)); err != nil {
		t.Fatal(err)
	}
	text := tr.text()
	for _, unwanted := range []string{"/books\"", "/controls\"", "/tip\"", "/book\"", "vault-"} {
		if strings.Contains(text, unwanted) {
			t.Fatalf("a story of the day played %s:\n%s", unwanted, text)
		}
	}
	if !strings.Contains(text, "/play-") || !strings.Contains(text, "/finished\"") || !strings.HasSuffix(text, "goodbye\" \"0123456789*#\"") {
		t.Fatalf("the story did not play to its end and say goodbye:\n%s", text)
	}
	if files, _ := filepath.Glob(filepath.Join(books, "progress", "*.json")); len(files) != 0 {
		t.Fatalf("a story left a bookmark: %v", files)
	}
}

// The limit: the warning lands on time, mid-window, exactly once; the
// listening carries on where it was; and before Asterisk's own cut-off the
// caller hears a goodbye rather than silence.
func TestTheLimitWarnsOnceThenEnds(t *testing.T) {
	requireAudio(t, "ffmpeg")
	books := library(t, 95, "alpha")
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	var played time.Duration
	a, tr := fakeAGI(t, func(cmd string) string {
		if r, ok := paced(t, c, cmd); ok {
			var samples int64
			fmt.Sscanf(r, "200 result=0 endpos=%d", &samples)
			played += time.Duration(samples) * time.Second / sampleRate
			return r
		}
		if strings.Contains(cmd, listenWarningClip) {
			if !strings.HasSuffix(cmd, `""`) {
				t.Error("the warning can be skipped")
			}
			c.advance(2 * time.Second)
		}
		return result(0)
	})
	// 70 seconds in all: warned at 10, goodbye at 65, Asterisk's cut at 70.
	err := listenIn(context.Background(), a, books, "story", []string{"alpha"}, newLimit(c.now, 70*time.Second, time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	text := tr.text()
	if n := strings.Count(text, listenWarningClip); n != 1 {
		t.Fatalf("warned %d times:\n%s", n, text)
	}
	warned := strings.Index(text, listenWarningClip)
	if first := strings.Index(text, "-cut\""); first < 0 || first > warned {
		t.Fatalf("the warning did not cut into the window it fell in:\n%s", text)
	}
	if !strings.HasSuffix(text, "STREAM FILE \""+listenEndedClip+"\" \"\"") {
		t.Fatalf("the call did not end with the bundled goodbye:\n%s", text)
	}
	// 10 s, the 2 s warning, then 53 s more: 63 s of story by the goodbye.
	if played < 62*time.Second || played > 64*time.Second {
		t.Fatalf("played %s of story before the end, want 63s", played)
	}
	if end := c.now().Sub(time.Unix(1_800_000_000, 0)); end > 65*time.Second+time.Second/10 {
		t.Fatalf("the goodbye came at %s, after the AGI's end", end)
	}
	if strings.Contains(text, "/finished\"") {
		t.Fatal("the limit was reported as the end of the book")
	}
}

// A shelf's pages can be rendered in the bundle's voice (books prompts
// --shelf): used for exactly that list, and never required.
func TestShelfPagesSpeakInTheBundleVoice(t *testing.T) {
	m, tr := testMenu(t, func(string) string { return result('1') })
	c := menuCatalog(3)
	shelf := []string{"book-2", "book-0"}
	silent := func(_, path string) error { return os.WriteFile(path, []byte{0, 0}, 0o644) }
	paths := func(ids []string) []string {
		var out []string
		for _, b := range shelfOf(c, ids) {
			out = append(out, filepath.Join(bookDir(m.root, b), "title"))
		}
		return out
	}

	// Built without the shelf: the bundle still loads, and the shelf is
	// announced with the compatibility prompts.
	if err := buildMenus(m.root, c, "Samantha", silent); err != nil {
		t.Fatal(err)
	}
	clips, err := loadMenus(m.root, c)
	if err != nil {
		t.Fatal(err)
	}
	if clips[pageKey(m.root, "books", paths(shelf), 0)] != "" {
		t.Fatal("a shelf page exists that nobody rendered")
	}

	if err := buildMenus(m.root, c, "Samantha", silent, shelf); err != nil {
		t.Fatal(err)
	}
	if m.clips, err = loadMenus(m.root, c); err != nil {
		t.Fatal(err)
	}
	if m.clips[pageKey(m.root, "books", paths(shelf), 0)] == "" {
		t.Fatal("the shelf's page was not loaded")
	}
	if m.clips[pageKey(m.root, "books", paths([]string{"book-0", "book-2"}), 0)] != "" {
		t.Fatal("a page for the same titles in another order was found: it would number them wrongly")
	}
	page := 0
	if index, err := m.browse("books", paths(shelf), &page); err != nil || index != 0 {
		t.Fatal(index, err)
	}
	if first := strings.Split(tr.text(), "\n")[0]; !strings.Contains(first, "/menu-audio/") {
		t.Fatalf("the shelf page was not the bundle's: %s", first)
	}
}

// The phrases the limits speak are bundled system phrases, rendered by
// prompts/build.sh with the rest of the pack — never synthesised here.
func TestListenPhrasesAreInTheBundledManifest(t *testing.T) {
	data, err := os.ReadFile("../../prompts/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		System map[string]string `json:"system"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	for _, clip := range []string{listenWarningClip, listenEndedClip, "call-me-maybe/system/listen-busy"} {
		name := strings.TrimPrefix(clip, "call-me-maybe/system/")
		if manifest.System[name] == "" {
			t.Errorf("%s is not a system phrase in prompts/manifest.json", name)
		}
	}
	if !strings.Contains(manifest.System["listen-warning"], "one minute") {
		t.Error("the warning no longer says how long is left")
	}
}

// Waiting for a key never waits past the next thing the limit has to say.
func TestWaitsStopAtTheLimit(t *testing.T) {
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	a, tr := fakeAGI(t, func(string) string { return result(0) })
	a.lim = newLimit(c.now, 63*time.Second, time.Minute)
	if _, err := a.wait(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tr.text(), "WAIT FOR DIGIT 3000") {
		t.Fatalf("waited past the warning:\n%s", tr.text())
	}
	c.advance(time.Hour)
	if _, err := a.wait(); !errors.Is(err, errTimeUp) {
		t.Fatalf("a wait past the end = %v", err)
	}
	a.lim = nil
	if _, err := a.wait(); err != nil || !strings.HasSuffix(tr.text(), "WAIT FOR DIGIT 10000") {
		t.Fatalf("a handset's wait changed: %v", err)
	}
}
