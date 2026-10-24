package policy

import (
	"strings"
	"testing"
	"time"
)

// [[shelves]], [listening], and the two menu choices that lead to a shelf
// (#155).

const shelfMenu = `
[[shelves]]
id     = "stories"
titles = ["little-red-hen", "three-billy-goats", "the-gingerbread-man"]

[[shelves]]
id     = "today"
titles = ["little-red-hen"]

[[menus]]
id = "front"

  [[menus.choices]]
  digit = "1"
  extension = "orders"

  [[menus.choices]]
  digit = "5"
  shelf = "stories"
  challenge = true

  [[menus.choices]]
  digit = "6"
  story_of_the_day = "today"
`

const shelfLine = "[line]\nmenu = \"front\"\n" + menuHouse + shelfMenu

func TestShelvesCompileWithTheirChoicesAndDefaultLimits(t *testing.T) {
	p := menuPolicy(t, shelfLine)
	s, ok := p.Shelf("stories")
	if !ok || len(s.Titles) != 3 || s.Titles[0] != "little-red-hen" || s.Titles[2] != "the-gingerbread-man" {
		t.Fatalf("shelf = %+v, want the titles in the order written", s)
	}
	if got := p.Shelves(); len(got) != 2 || got[0].ID != "stories" || got[1].ID != "today" {
		t.Fatalf("shelves = %+v", got)
	}
	front, _ := p.Menu("front")
	c, ok := front.Choice("5")
	if !ok || c.Kind != ChoiceShelf || c.Target != "stories" || !c.Challenge || !c.Kind.Shelf() {
		t.Fatalf("choice 5 = %+v", c)
	}
	c, ok = front.Choice("6")
	if !ok || c.Kind != ChoiceStory || c.Target != "today" || c.Challenge || !c.Kind.Shelf() {
		t.Fatalf("choice 6 = %+v", c)
	}
	if ChoiceExtension.Shelf() || ChoiceRecording.Shelf() {
		t.Fatal("a non-shelf kind says it is a shelf")
	}
	if l := p.Listening(); l.MaxCall != DefaultListenMinutes*time.Minute || l.MaxListeners != DefaultMaxListeners {
		t.Fatalf("default limits = %+v", l)
	}
	if !p.ShelfReachable("stories") || !p.ShelfReachable("today") || p.ShelfReachable("ghost") {
		t.Fatal("ShelfReachable is wrong")
	}
	if w := p.Warnings(); len(w) != 0 {
		t.Fatalf("a well-formed shelf line warns: %q", w)
	}
}

func TestListeningLimitsAreBounded(t *testing.T) {
	p := menuPolicy(t, shelfLine+"\n[listening]\nmax_minutes = 30\nmax_listeners = 3\n")
	if l := p.Listening(); l.MaxCall != 30*time.Minute || l.MaxListeners != 3 {
		t.Fatalf("limits = %+v", l)
	}
	if d := p.Listening().Describe(); !strings.Contains(d, "30m0s") || !strings.Contains(d, "3 listeners") || !strings.Contains(d, "1m0s before") {
		t.Fatalf("describe = %q", d)
	}
	for _, bad := range []string{
		"max_minutes = 4", "max_minutes = 241", "max_minutes = 0",
		"max_listeners = 0", "max_listeners = 11",
	} {
		msg := menuError(t, shelfLine+"\n[listening]\n"+bad+"\n")
		if !strings.Contains(msg, "[listening]") {
			t.Errorf("%s: %s", bad, msg)
		}
	}
}

// Rotation: one title a day, in the order written, starting again at the
// top — the same answer for every call that day, and nothing remembered.
func TestStoryOfTheDayRotatesByCalendarDay(t *testing.T) {
	s := ResolvedShelf{ID: "stories", Titles: []string{"a", "b", "c"}}
	loc := time.FixedZone("box", -5*3600)
	day := time.Date(2026, 10, 24, 0, 0, 1, 0, loc)
	first := s.StoryOn(day)
	if s.StoryOn(day.Add(23*time.Hour+59*time.Minute)) != first {
		t.Fatal("the story changed during the day")
	}
	seen := map[string]bool{first: true}
	for i := 1; i < 3; i++ {
		seen[s.StoryOn(day.AddDate(0, 0, i))] = true
	}
	if len(seen) != 3 {
		t.Fatalf("three days did not visit three titles: %v", seen)
	}
	if s.StoryOn(day.AddDate(0, 0, 3)) != first {
		t.Fatal("the rotation does not start again at the top")
	}
	// The box's midnight, not UTC's: 23:30 at the box is still that day.
	late := time.Date(2026, 10, 24, 23, 30, 0, 0, loc)
	if s.StoryOn(late) != first {
		t.Fatal("the day turned over at UTC midnight rather than the box's")
	}
	if got := (ResolvedShelf{Titles: []string{"only"}}).StoryOn(day); got != "only" {
		t.Fatalf("a one-title shelf rotated to %q", got)
	}
	if got := (ResolvedShelf{}).StoryOn(day); got != "" {
		t.Fatalf("an empty shelf has a story: %q", got)
	}
	// Before 1970 the day number is negative; the index must not be.
	if got := s.StoryOn(time.Date(1960, 1, 1, 12, 0, 0, 0, time.UTC)); got == "" {
		t.Fatal("no story before the epoch")
	}
}

func TestShelfRefusals(t *testing.T) {
	cases := []struct {
		name, src, want string
	}{
		{"unknown shelf", strings.Replace(shelfLine, `shelf = "stories"`, `shelf = "ghost"`, 1), `names shelf "ghost"`},
		{"unknown story shelf", strings.Replace(shelfLine, `story_of_the_day = "today"`, `story_of_the_day = "ghost"`, 1), `names shelf "ghost"`},
		{"empty shelf", strings.Replace(shelfLine, `titles = ["little-red-hen"]`, `titles = []`, 1), `shelf "today" has no titles`},
		{"absent titles", strings.Replace(shelfLine, "titles = [\"little-red-hen\"]\n", "", 1), `shelf "today" has no titles`},
		{"title not an id", strings.Replace(shelfLine, `"three-billy-goats"`, `"../private"`, 1), `is not a library title id`},
		{"title twice", strings.Replace(shelfLine, `"three-billy-goats"`, `"little-red-hen"`, 1), `names title "little-red-hen" twice`},
		{"duplicate shelf", strings.Replace(shelfLine, `id     = "today"`, `id     = "stories"`, 1), `duplicate shelf id "stories"`},
		{"bad shelf id", strings.Replace(shelfLine, `id     = "today"`, `id     = "To Day"`, 1), `shelf id "To Day"`},
		{"two targets", strings.Replace(shelfLine, "  digit = \"6\"\n", "  digit = \"6\"\n  recording = \"hours\"\n", 1), `exactly one of extension, menu, recording, voicemail, shelf or story_of_the_day`},
		{"then after a shelf", strings.Replace(shelfLine, "  shelf = \"stories\"\n", "  shelf = \"stories\"\n  then = \"goodbye\"\n", 1), `has then, which only follows a recording`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if msg := menuError(t, c.src); !strings.Contains(msg, c.want) {
				t.Fatalf("err = %s\nwant %q", msg, c.want)
			}
		})
	}
	// A shelf refused for its titles is reported there, once — not again at
	// each choice that names it.
	msg := menuError(t, strings.Replace(shelfLine, `titles = ["little-red-hen"]`, `titles = []`, 1))
	if strings.Contains(msg, `names shelf "today"`) {
		t.Fatalf("a refused shelf is reported twice: %s", msg)
	}
	tooMany := make([]string, MaxShelfTitles+1)
	for i := range tooMany {
		tooMany[i] = `"t` + strings.Repeat("x", i%5) + string(rune('a'+i%26)) + string(rune('a'+i/26)) + `"`
	}
	msg = menuError(t, strings.Replace(shelfLine, `titles = ["little-red-hen"]`, "titles = ["+strings.Join(tooMany, ", ")+"]", 1))
	if !strings.Contains(msg, "at most 128") {
		t.Fatalf("err = %s", msg)
	}
}

func TestShelvesNoMenuOffersAreWarnedNotRefused(t *testing.T) {
	orphan := shelfLine + "\n[[shelves]]\nid = \"vault\"\ntitles = [\"private-recording\"]\n"
	p := menuPolicy(t, orphan)
	if w := strings.Join(p.Warnings(), "\n"); !strings.Contains(w, `shelf "vault" is not offered`) {
		t.Fatalf("warnings = %q", w)
	}
	if p.ShelfReachable("vault") {
		t.Fatal("an unoffered shelf is reachable")
	}

	// No [line] menu at all: shelves and [listening] reach nobody.
	withPIN := strings.Replace(menuHouse, `label = "Orders"`, "label = \"Orders\"\npin = \"428917\"", 1)
	p = menuPolicy(t, withPIN+shelfMenu+"\n[listening]\nmax_minutes = 10\n")
	w := strings.Join(p.Warnings(), "\n")
	if !strings.Contains(w, "[[shelves]] are defined but [line] menu names no menu") || !strings.Contains(w, "[listening] is set but") {
		t.Fatalf("warnings = %q", w)
	}
}

func TestShelvesInTheWrongFileAreRefused(t *testing.T) {
	for _, stray := range []string{
		"[[shelves]]\nid = \"stories\"\ntitles = [\"little-red-hen\"]\n",
		"[listening]\nmax_minutes = 10\n",
	} {
		handsets := "[[handsets]]\nid = \"kitchen\"\nendpoint = \"PJSIP/kitchen\"\n" + stray
		_, err := fromSplitTOML([]byte("[house]\nhandsets = [\"kitchen\"]\n"), []byte(handsets), Options{})
		if err == nil || !strings.Contains(err.Error(), "shelves/listening belong in policy.toml") {
			t.Fatalf("%q in handsets.toml: err = %v", stray, err)
		}
		if problems := LintSplit([]byte("[house]\nhandsets = [\"kitchen\"]\n"), []byte(handsets)); len(problems) == 0 {
			t.Fatalf("%q in handsets.toml passes the lint path", stray)
		}
	}
	for _, file := range []string{"trunks", "contacts"} {
		for _, section := range []string{"shelves", "listening"} {
			if hint := misplaced[file+"."+section]; !strings.Contains(hint, "policy.toml") {
				t.Errorf("%s.toml has no hint for a stray %s: %q", file, section, hint)
			}
		}
	}
}

// A file with no shelves compiles to exactly what it did before #155.
func TestNoShelvesIsNoChange(t *testing.T) {
	p := menuPolicy(t, "[line]\nmenu = \"front\"\n"+menuHouse+frontMenu)
	if len(p.Shelves()) != 0 || len(p.Warnings()) != 0 {
		t.Fatalf("shelves %v, warnings %q", p.Shelves(), p.Warnings())
	}
	if l := p.Listening(); l.MaxListeners != DefaultMaxListeners {
		t.Fatalf("limits = %+v", l)
	}
}
