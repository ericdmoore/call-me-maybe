package policy

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// ── [[menus]]: a public line's front menu ────────────────────────────────

// Menu is one raw [[menus]] entry: "press 1 for orders, 2 for our hours".
//
// Menus live in the line's own policy file, as its extensions do, and are
// named from [line] menu. A line whose [line] names no menu answers with the
// PIN lobby exactly as before; a line that names one never runs the PIN lobby
// at all.
//
// The wording is the operator's, not a pack's: doorman plays the clip
// <line prefix>/menus/<id> and never synthesises anything (invariant 7). Say
// is the text that clip should speak, kept beside the reference so it can be
// reviewed, printed by `doorman check`, and recorded or rendered from.
type Menu struct {
	ID  string `toml:"id"`
	Say string `toml:"say"`
	// Repeat is how many times the menu is played again to a caller who
	// presses nothing before the line's on_no_input applies. Absent is 1.
	Repeat  *int         `toml:"repeat"`
	Choices []MenuChoice `toml:"choices"`
}

// MenuChoice is one key on a menu and where it leads. Exactly one target key
// is set. Adding a kind of target — #155's books shelf is the next — is a
// field here, an entry in targets below, a reference check in compileMenus,
// and a case in the lobby's walker; nothing else in the shape changes.
type MenuChoice struct {
	// Digit is the key, "0" to "9". * repeats the menu and # is reserved, so
	// neither can be a choice.
	Digit string `toml:"digit"`

	// The targets: one and only one.
	Extension string `toml:"extension"` // an [[extensions]] id in this file
	Menu      string `toml:"menu"`      // another [[menus]] id
	Recording string `toml:"recording"` // a clip, <line prefix>/recordings/<id>
	Voicemail string `toml:"voicemail"` // a mailbox

	// Then is what follows a recording: "menu" (the default) offers the menu
	// that led to it again; "goodbye" says the parting prompt and hangs up.
	Then string `toml:"then"`

	// Challenge puts the arithmetic challenge in front of this choice. Asked
	// at most once a call: a caller who answered it has answered it.
	Challenge bool `toml:"challenge"`
}

// ChoiceKind is what a menu choice leads to. The values are the TOML keys
// themselves, so `doorman check`, the call record and the logs print what an
// operator wrote.
type ChoiceKind string

const (
	ChoiceExtension ChoiceKind = "extension"
	ChoiceMenu      ChoiceKind = "menu"
	ChoiceRecording ChoiceKind = "recording"
	ChoiceVoicemail ChoiceKind = "voicemail"
)

// What follows a recording.
const (
	ThenMenu    = "menu"
	ThenGoodbye = "goodbye"
)

// The static limits. Two levels follows docs/product-extensions.md — "IVR
// depth one, maybe two — not a tree farm" — and a menu that repeats itself
// more than the per-call play ceiling allows would be cut off mid-repeat.
const (
	MaxMenuDepth  = 2
	MaxMenuRepeat = 5
)

// targetKey is one target a choice names, in the order the kinds are listed.
type targetKey struct {
	kind ChoiceKind
	id   string
}

// targets returns every target the choice wrote. One is valid; none or
// several is a choice that cannot be followed.
func (c MenuChoice) targets() []targetKey {
	var out []targetKey
	for _, t := range []targetKey{
		{ChoiceExtension, c.Extension},
		{ChoiceMenu, c.Menu},
		{ChoiceRecording, c.Recording},
		{ChoiceVoicemail, c.Voicemail},
	} {
		if t.id != "" {
			out = append(out, t)
		}
	}
	return out
}

// ResolvedMenu is a compiled [[menus]] entry.
type ResolvedMenu struct {
	ID     string
	Say    string
	Repeat int
	// Choices, sorted by key, for display; Choice is the lookup.
	Choices []ResolvedChoice
	byDigit map[string]ResolvedChoice
}

// Choice is an exact match on the key pressed.
func (m ResolvedMenu) Choice(digit string) (ResolvedChoice, bool) {
	c, ok := m.byDigit[digit]
	return c, ok
}

// ResolvedChoice is one compiled choice.
type ResolvedChoice struct {
	Digit     string
	Kind      ChoiceKind
	Target    string
	Then      string // recordings only: ThenMenu or ThenGoodbye
	Challenge bool
}

var menuDigit = regexp.MustCompile(`^[0-9]$`)

// compiledMenus is what compileMenus hands back to compileChecked.
type compiledMenus struct {
	menus map[string]ResolvedMenu
	order []string
	// reachable is every extension id a menu reachable from [line] menu
	// names: the only extensions that may go without a PIN.
	reachable map[string]bool
	warnings  []string
}

// compileMenus validates and compiles [[menus]]. root is [line] menu;
// extensionIDs is every [[extensions]] id in this file, enabled or not, so
// switching an extension off never invalidates a menu that names it.
func compileMenus(raw []Menu, root string, extensionIDs map[string]bool, fail func(string, ...any)) compiledMenus {
	out := compiledMenus{menus: map[string]ResolvedMenu{}, reachable: map[string]bool{}}

	for _, m := range raw {
		if !handsetIDPattern.MatchString(m.ID) {
			fail("menu id %q must be lowercase alphanumeric/dash/underscore", m.ID)
			continue
		}
		if _, dup := out.menus[m.ID]; dup {
			fail("duplicate menu id %q", m.ID)
			continue
		}
		rm := ResolvedMenu{ID: m.ID, Say: m.Say, Repeat: 1, byDigit: map[string]ResolvedChoice{}}
		if m.Repeat != nil {
			if *m.Repeat < 0 || *m.Repeat > MaxMenuRepeat {
				fail("menu %q repeat %d must be 0 to %d — how many more times a silent caller hears it", m.ID, *m.Repeat, MaxMenuRepeat)
			} else {
				rm.Repeat = *m.Repeat
			}
		}
		if len(m.Choices) == 0 {
			fail("menu %q has no choices — a menu is the keys a caller may press", m.ID)
		}
		for _, c := range m.Choices {
			where := fmt.Sprintf("menu %q", m.ID)
			switch {
			case c.Digit == "*" || c.Digit == "#":
				fail("%s: digit %q is reserved — * repeats the menu and # is kept for later; use 0 to 9", where, c.Digit)
				continue
			case !menuDigit.MatchString(c.Digit):
				fail("%s: digit %q must be one key, 0 to 9", where, c.Digit)
				continue
			}
			where = fmt.Sprintf("menu %q digit %q", m.ID, c.Digit)
			if _, dup := rm.byDigit[c.Digit]; dup {
				fail("menu %q offers digit %q twice", m.ID, c.Digit)
				continue
			}
			targets := c.targets()
			if len(targets) != 1 {
				fail("%s must lead to exactly one of extension, menu, recording or voicemail (it names %d)", where, len(targets))
				continue
			}
			t := targets[0]
			rc := ResolvedChoice{Digit: c.Digit, Kind: t.kind, Target: t.id, Challenge: c.Challenge}
			switch t.kind {
			case ChoiceExtension:
				if !extensionIDs[t.id] {
					fail("%s names extension %q, which no [[extensions]] entry in this file carries as its id", where, t.id)
				}
			case ChoiceMenu:
				if t.id == m.ID {
					fail("%s leads back to menu %q itself — * already repeats a menu", where, t.id)
				}
				// Existence is checked once every menu is known, below.
			case ChoiceRecording:
				if !handsetIDPattern.MatchString(t.id) {
					fail("%s recording %q must be lowercase alphanumeric/dash/underscore — it names the clip recordings/<id>", where, t.id)
				}
			case ChoiceVoicemail:
				if !mailboxPattern.MatchString(t.id) {
					fail("%s voicemail %q must be lowercase alphanumeric/dash/underscore", where, t.id)
				}
			}
			switch {
			case t.kind == ChoiceRecording && c.Then == "":
				rc.Then = ThenMenu
			case t.kind == ChoiceRecording && (c.Then == ThenMenu || c.Then == ThenGoodbye):
				rc.Then = c.Then
			case t.kind == ChoiceRecording:
				fail("%s then %q must be %q or %q", where, c.Then, ThenMenu, ThenGoodbye)
			case c.Then != "":
				fail("%s has then, which only follows a recording", where)
			}
			rm.byDigit[c.Digit] = rc
			rm.Choices = append(rm.Choices, rc)
		}
		sort.Slice(rm.Choices, func(i, j int) bool { return rm.Choices[i].Digit < rm.Choices[j].Digit })
		out.menus[m.ID] = rm
		out.order = append(out.order, m.ID)
	}

	for _, id := range out.order {
		for _, c := range out.menus[id].Choices {
			if c.Kind == ChoiceMenu && c.Target != id {
				if _, ok := out.menus[c.Target]; !ok {
					fail("menu %q digit %q names menu %q, which is not a [[menus]] id", id, c.Digit, c.Target)
				}
			}
		}
	}

	if root == "" {
		if len(out.order) > 0 {
			out.warnings = append(out.warnings, "[[menus]] are defined but [line] menu names none, so no caller hears them")
		}
		return out
	}
	if _, ok := out.menus[root]; !ok {
		known := "there are none"
		if len(out.order) > 0 {
			known = "it has " + strings.Join(out.order, ", ")
		}
		fail("[line] menu %q is not a [[menus]] id (%s)", root, known)
		return out
	}

	// Breadth first, so a menu's depth is its shortest distance from the
	// front. A choice back to a menu already seen is a cycle, and wanted: "0
	// for the main menu". A choice to a menu not seen yet is one level
	// deeper, and the third level is the one refused.
	depth := map[string]int{root: 1}
	queue := []string{root}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		for _, c := range out.menus[id].Choices {
			switch c.Kind {
			case ChoiceExtension:
				out.reachable[c.Target] = true
			case ChoiceMenu:
				if _, seen := depth[c.Target]; seen {
					continue
				}
				if _, ok := out.menus[c.Target]; !ok {
					continue // reported above
				}
				d := depth[id] + 1
				if d > MaxMenuDepth {
					fail("menu %q is %d menus deep from [line] menu %q — at most %d: a front menu and one level below it. "+
						"Deeper menus are a different product (docs/product-extensions.md)", c.Target, d, root, MaxMenuDepth)
					continue
				}
				depth[c.Target] = d
				queue = append(queue, c.Target)
			}
		}
	}
	for _, id := range out.order {
		if _, ok := depth[id]; !ok {
			out.warnings = append(out.warnings, fmt.Sprintf("menu %q is not reachable from [line] menu %q, so no caller hears it", id, root))
		}
	}
	return out
}

// MenuDepth reports how far a menu is from the front one: 1 for [line] menu
// itself, 2 for a menu it names, 0 for a menu no caller can reach.
func (p *Policy) MenuDepth(id string) int {
	root := p.line.Menu
	if root == "" {
		return 0
	}
	depth := map[string]int{root: 1}
	queue := []string{root}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, c := range p.menus[cur].Choices {
			if _, seen := depth[c.Target]; c.Kind == ChoiceMenu && !seen {
				depth[c.Target] = depth[cur] + 1
				queue = append(queue, c.Target)
			}
		}
	}
	return depth[id]
}

// Menu looks up a compiled menu by id.
func (p *Policy) Menu(id string) (ResolvedMenu, bool) {
	m, ok := p.menus[id]
	return m, ok
}

// Menus is every compiled menu, in declaration order.
func (p *Policy) Menus() []ResolvedMenu {
	out := make([]ResolvedMenu, 0, len(p.menuOrder))
	for _, id := range p.menuOrder {
		out = append(out, p.menus[id])
	}
	return out
}

// LookupExtensionID finds an enabled extension by its id — the menu walker's
// lookup, and the only way to an extension with no PIN. Deliberately a
// different map from LookupExtension's: a PIN-less extension in the PIN map
// would sit under "", which is what a bare # submits.
func (p *Policy) LookupExtensionID(id string) (ResolvedExtension, bool) {
	if id == "" {
		return ResolvedExtension{}, false
	}
	e, ok := p.byID[id]
	return e, ok
}

// Warnings are things `doorman check` should say about a file that loads:
// valid, and probably not what its author meant.
func (p *Policy) Warnings() []string { return append([]string(nil), p.warnings...) }
