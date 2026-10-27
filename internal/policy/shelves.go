package policy

import (
	"fmt"
	"regexp"
	"time"
)

// ── [[shelves]] and [listening]: a books shelf on a public line (#155) ──

// Shelf is one raw [[shelves]] entry: the titles from the box's audiobook
// library that callers on this line may hear — "Dial-a-Story".
//
// A shelf is an explicit list of title ids from the prepared catalog (`doorman
// books list`), never a tag and never a directory, and never the whole library
// by default: the house's privately imported audio reaches an outside caller
// only by being written here, one id at a time. The order written is the
// order the shelf is announced in.
//
// Shelves live in the line's own policy file, beside the menu that leads to
// them. doorman never reads the catalog on the call path; `doorman check`
// compares the ids with it when it can see one.
type Shelf struct {
	ID     string   `toml:"id"`
	Titles []string `toml:"titles"`
}

// Listening is the raw [listening] section: the limits that suit strangers,
// for every shelf and story on this line. Every key is optional.
type Listening struct {
	// MaxMinutes is the longest a call that reaches a shelf may last, counted
	// from when the call arrived. Absent is DefaultListenMinutes.
	MaxMinutes *int `toml:"max_minutes"`
	// MaxListeners is how many callers may listen on this line at once.
	// Absent is DefaultMaxListeners.
	MaxListeners *int `toml:"max_listeners"`
}

func (l Listening) empty() bool { return l.MaxMinutes == nil && l.MaxListeners == nil }

// The limits. Five minutes is the floor because a caller can spend three in
// the menu (the walker's ceiling) and still deserves a story after the
// one-minute warning. One listener is the default because a provider plan may
// carry two simultaneous calls, and a box whose every channel is a story
// cannot ring the house.
const (
	DefaultListenMinutes = 20
	MinListenMinutes     = 5
	MaxListenMinutes     = 240
	DefaultMaxListeners  = 1
	MaxMaxListeners      = 10
	// MaxShelfTitles is the library's own ceiling (internal/books): a shelf
	// can never name more titles than a catalog can hold.
	MaxShelfTitles = 128
	// ListenWarning is how long before the limit the caller is told the call
	// is ending. Fixed rather than configured, because the bundled phrase
	// says "in one minute" and a configurable lead would make it lie.
	ListenWarning = time.Minute
)

// titlePattern is a catalog book id, exactly as internal/books accepts one
// (cmd/doorman's tests hold the two to each other).
var titlePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// ResolvedShelf is a compiled [[shelves]] entry.
type ResolvedShelf struct {
	ID     string
	Titles []string
}

// StoryOn is the shelf's story of the day: one title, by a rule anybody can
// work out from the list — the titles in order, one a day, starting again at
// the top. The day is the calendar date in t's location, so it turns over at
// the box's midnight, and nothing is remembered between calls.
func (s ResolvedShelf) StoryOn(t time.Time) string {
	if len(s.Titles) == 0 {
		return ""
	}
	y, m, d := t.Date()
	days := time.Date(y, m, d, 0, 0, 0, 0, time.UTC).Unix() / 86400
	i := days % int64(len(s.Titles))
	if i < 0 {
		i += int64(len(s.Titles))
	}
	return s.Titles[i]
}

// ListenLimits is the compiled [listening] section.
type ListenLimits struct {
	MaxCall      time.Duration
	MaxListeners int
}

// Describe is the limits as `doorman check` prints them.
func (l ListenLimits) Describe() string {
	return fmt.Sprintf("calls end at %s, warned %s before; %d %s at once",
		l.MaxCall, ListenWarning, l.MaxListeners, plural(l.MaxListeners, "listener"))
}

func plural(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}

func compileShelves(raw []Shelf, fail func(string, ...any)) (map[string]ResolvedShelf, []string) {
	shelves := map[string]ResolvedShelf{}
	var order []string
	for _, s := range raw {
		if !handsetIDPattern.MatchString(s.ID) {
			fail("shelf id %q must be lowercase alphanumeric/dash/underscore", s.ID)
			continue
		}
		if _, dup := shelves[s.ID]; dup {
			fail("duplicate shelf id %q", s.ID)
			continue
		}
		if len(s.Titles) == 0 {
			fail("shelf %q has no titles — a shelf is the titles a caller may hear, written one by one; "+
				"it is never the whole library", s.ID)
			continue
		}
		if len(s.Titles) > MaxShelfTitles {
			fail("shelf %q names %d titles; a library holds at most %d", s.ID, len(s.Titles), MaxShelfTitles)
			continue
		}
		seen := map[string]bool{}
		ok := true
		for _, title := range s.Titles {
			switch {
			case !titlePattern.MatchString(title):
				fail("shelf %q title %q is not a library title id — `doorman books list` shows them "+
					"(lowercase alphanumeric/dash/underscore, at most 64)", s.ID, truncateForMessage(title))
				ok = false
			case seen[title]:
				fail("shelf %q names title %q twice", s.ID, title)
				ok = false
			}
			seen[title] = true
		}
		if !ok {
			continue
		}
		shelves[s.ID] = ResolvedShelf{ID: s.ID, Titles: append([]string(nil), s.Titles...)}
		order = append(order, s.ID)
	}
	return shelves, order
}

func compileListening(l Listening, fail func(string, ...any)) ListenLimits {
	out := ListenLimits{MaxCall: DefaultListenMinutes * time.Minute, MaxListeners: DefaultMaxListeners}
	if l.MaxMinutes != nil {
		if m := *l.MaxMinutes; m < MinListenMinutes || m > MaxListenMinutes {
			fail("[listening] max_minutes %d must be %d to %d — a caller may spend three minutes in the menu "+
				"and is warned one minute before the end", m, MinListenMinutes, MaxListenMinutes)
		} else {
			out.MaxCall = time.Duration(m) * time.Minute
		}
	}
	if l.MaxListeners != nil {
		if n := *l.MaxListeners; n < 1 || n > MaxMaxListeners {
			fail("[listening] max_listeners %d must be 1 to %d — every listener holds a provider channel", n, MaxMaxListeners)
		} else {
			out.MaxListeners = n
		}
	}
	return out
}

// Shelf looks up a compiled shelf by id.
func (p *Policy) Shelf(id string) (ResolvedShelf, bool) {
	s, ok := p.shelves[id]
	return s, ok
}

// Shelves is every compiled shelf, in declaration order.
func (p *Policy) Shelves() []ResolvedShelf {
	out := make([]ResolvedShelf, 0, len(p.shelfOrder))
	for _, id := range p.shelfOrder {
		out = append(out, p.shelves[id])
	}
	return out
}

// Listening is the line's compiled [listening] limits — the defaults when the
// file has no such section.
func (p *Policy) Listening() ListenLimits { return p.listening }

// ShelfReachable reports whether a menu a caller can reach leads to the shelf,
// to browse it or for its story of the day.
func (p *Policy) ShelfReachable(id string) bool {
	for _, m := range p.Menus() {
		if p.MenuDepth(m.ID) == 0 {
			continue
		}
		for _, c := range m.Choices {
			if c.Kind.Shelf() && c.Target == id {
				return true
			}
		}
	}
	return false
}
