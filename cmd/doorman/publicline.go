package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"callmemaybe/internal/books"
	"callmemaybe/internal/lobby"
	"callmemaybe/internal/policy"
)

// What `doorman check` says about a public line: the arithmetic challenge and
// the clips it speaks from.

// soundsDir is Asterisk's sounds tree. A var so a test can point it at a
// temporary directory. check only ever stats beneath it — it never reads
// audio — and only when it exists, which is to say only on the box: a
// workstation or CI validating examples has no sounds tree and is told
// nothing about clips it could not possibly have.
var soundsDir = "/var/lib/asterisk/sounds"

// checkMediaPrefix is PROMPT_MEDIA_PREFIX as check sees it: the prefix a line
// without [line] prompts speaks from, and so where its menu clips live.
var checkMediaPrefix = lobby.BundledMediaPrefix

// linePrefix is the media prefix a line's own words are played from.
func linePrefix(p *policy.Policy) string {
	if prefix := p.Line().Prompts; prefix != "" {
		return prefix
	}
	return checkMediaPrefix
}

// menusAsk reports whether any menu a caller can reach puts a choice behind
// the challenge.
func menusAsk(p *policy.Policy) bool {
	for _, m := range p.Menus() {
		if p.MenuDepth(m.ID) == 0 {
			continue
		}
		for _, c := range m.Choices {
			if c.Challenge {
				return true
			}
		}
	}
	return false
}

// describeMenus prints a line's menus: every key, where it leads, and which
// are behind the challenge — the ones that skip it printed as plainly as the
// ones behind it, because a choice an operator assumed was guarded is
// exactly what this output exists to catch. Then the warnings a file that
// loads can still deserve, and, on the box, the clips nobody has installed.
func describeMenus(p *policy.Policy) {
	line := p.Line()
	menus := p.Menus()
	if line.Menu != "" {
		fmt.Printf("  front menu           : %s   (the PIN lobby never runs on this line)\n", line.Menu)
		fmt.Printf("  admitted callers     : skip the menu and the challenge and ring [house]; a key\n" +
			"                         pressed over their welcome is a choice at the front menu\n")
		fmt.Printf("  menu ceilings        : %d plays or %s a call, then the same as silence\n",
			lobby.MenuMaxPlays, lobby.MenuMaxDuration)
	}
	prefix := linePrefix(p)
	var clips []string
	for _, m := range menus {
		depth := p.MenuDepth(m.ID)
		where := "not reachable — no caller hears it"
		switch depth {
		case 1:
			where = "the front menu"
		case 2:
			where = "one level down"
		}
		fmt.Printf("\n  Menu %q (%s), clip %s/menus/%s\n", m.ID, where, prefix, m.ID)
		if m.Say != "" {
			fmt.Printf("      says         %q\n", m.Say)
		}
		for _, c := range m.Choices {
			fmt.Printf("      %s  %-26s %s\n", c.Digit, string(c.Kind)+" "+c.Target, describeChoice(p, m, c))
		}
		fmt.Printf("      *  repeats the menu; # does nothing; any other key plays it again\n")
		fmt.Printf("      silence      plays it %d more %s, then %s\n", m.Repeat, plural(m.Repeat, "time"),
			describeSilence(line, p.HousePlan().Mailbox))
		if depth > 0 {
			clips = append(clips, "menus/"+m.ID)
			for _, c := range m.Choices {
				if c.Kind == policy.ChoiceRecording {
					clips = append(clips, "recordings/"+c.Target)
				}
			}
		}
	}
	for _, w := range p.Warnings() {
		fmt.Printf("  ! %s\n", w)
	}
	if missing, ok := missingClips(prefix, dedupeStrings(clips)); ok && len(missing) > 0 {
		fmt.Printf("  ! %s has no %s.\n"+
			"    A caller hears silence there, presses nothing, and gets the line's no-input\n"+
			"    disposition. Record or render them (RUNBOOK \"A front menu\").\n",
			filepath.Join(soundsDir, prefix), strings.Join(missing, ", "))
	}
}

// describeChoice is where one key leads, said in full.
func describeChoice(p *policy.Policy, m policy.ResolvedMenu, c policy.ResolvedChoice) string {
	var out string
	switch c.Kind {
	case policy.ChoiceExtension:
		ext, ok := p.LookupExtensionID(c.Target)
		if !ok {
			return "(switched off — enabled = false, so the key plays the menu again)"
		}
		out = fmt.Sprintf("%s: rings %s", ext.Label, describePlan(ext.Plan))
		if ext.Plan.Mailbox != "" {
			out += ", then mailbox " + ext.Plan.Mailbox
		}
		if len(ext.Afterhours) > 0 {
			out += "; afterhours " + strings.Join(ext.Afterhours.IDs(), ", ")
		}
	case policy.ChoiceRecording:
		out = "plays recordings/" + c.Target + ", then "
		if c.Then == policy.ThenGoodbye {
			out += "goodbye"
		} else {
			out += "back to menu " + m.ID
		}
	case policy.ChoiceVoicemail:
		out = "takes a message in mailbox " + c.Target
	case policy.ChoiceMenu:
		out = "goes to menu " + c.Target
	case policy.ChoiceShelf:
		shelf, _ := p.Shelf(c.Target)
		out = fmt.Sprintf("browses shelf %s (%d %s)", c.Target, len(shelf.Titles), plural(len(shelf.Titles), "title"))
	case policy.ChoiceStory:
		shelf, _ := p.Shelf(c.Target)
		out = fmt.Sprintf("today's story from shelf %s (%s), then goodbye", c.Target, shelf.StoryOn(time.Now()))
	}
	if c.Challenge {
		return out + " — behind the challenge"
	}
	if c.Kind == policy.ChoiceExtension || c.Kind == policy.ChoiceVoicemail || c.Kind.Shelf() {
		return out + " — no challenge"
	}
	return out
}

// booksLibrary is the prepared audiobook library check compares shelves
// with, and asteriskDir where it looks for the installed books dialplan.
// Vars so a test can point them at temporary directories. Both are only
// read — the catalog is the books AGI's, the dialplan render's.
var (
	booksLibrary = books.DefaultLibrary
	asteriskDir  = "/etc/asterisk"
)

// describeShelves prints a line's shelves and the limits its listeners are
// held to. Silent on a line with none.
func describeShelves(p *policy.Policy) {
	shelves := p.Shelves()
	if len(shelves) == 0 {
		return
	}
	fmt.Printf("\n  Dial-a-Story\n")
	fmt.Printf("      limits       %s\n", p.Listening().Describe())
	fmt.Printf("      over the cap the caller hears \"all our listening lines are busy\" and the menu again;\n" +
		"                   no bookmarks — an outside caller starts each title from the beginning\n")
	for _, s := range shelves {
		where := "offered by no menu a caller can reach"
		if p.ShelfReachable(s.ID) {
			where = "offered"
		}
		fmt.Printf("      shelf %s: %d %s, %s — %s\n", s.ID, len(s.Titles), plural(len(s.Titles), "title"), where, strings.Join(s.Titles, ", "))
	}
}

// printShelves checks every line's shelves against the installed catalog,
// when check can read one, and looks for the context listeners are released
// into, when it can see the box's dialplan. A title the catalog lacks fails
// the check: at call time it is simply not offered, and nobody should find
// that out from a caller. A catalog check cannot read is a warning — on a
// workstation, or in CI, there is none to read.
func printShelves(results []checkedLine) bool {
	var lines []checkedLine
	for _, r := range results {
		if r.err == nil && len(r.pol.Shelves()) > 0 {
			lines = append(lines, r)
		}
	}
	if len(lines) == 0 {
		return true
	}
	fmt.Println()
	fmt.Println("Dial-a-Story shelves")
	ok := true
	c, err := books.Load(booksLibrary)
	if err != nil {
		fmt.Printf("  ! could not read the audiobook catalog in %s, so shelf titles were not checked\n"+
			"    against it. On the box: `doorman books list` shows what is installed.\n", booksLibrary)
	} else {
		have := map[string]bool{}
		for _, b := range c.Books {
			have[b.ID] = true
		}
		missing := 0
		for _, r := range lines {
			for _, s := range r.pol.Shelves() {
				for _, title := range s.Titles {
					if !have[title] {
						fmt.Printf("  ✗ line %s shelf %q names %q, which the catalog in %s does not have\n",
							r.Name, s.ID, title, booksLibrary)
						missing++
					}
				}
			}
		}
		if missing > 0 {
			fmt.Println("    A caller is never offered a title the catalog lacks. Prepare it (`doorman books")
			fmt.Println("    prepare`), or take it off the shelf. `doorman books list` shows what is installed.")
			ok = false
		} else {
			fmt.Printf("  ✓ every shelf title is in the catalog (%d %s installed)\n", len(c.Books), plural(len(c.Books), "book"))
		}
	}
	if st, err := os.Stat(asteriskDir); err == nil && st.IsDir() {
		conf := filepath.Join(asteriskDir, "extensions_books.conf")
		data, err := os.ReadFile(conf)
		if err != nil || !strings.Contains(string(data), "["+lobby.ListenContext+"]") {
			fmt.Printf("  ! %s has no [%s], so a caller who chooses a shelf is released\n"+
				"    into nothing and the call ends. Run `doorman render` and install\n"+
				"    extensions_books.conf (docs/BOOKS.md).\n", conf, lobby.ListenContext)
		}
	}
	return ok
}

// describeSilence is where a menu's silent caller ends up.
func describeSilence(line policy.LineIdentity, houseMailbox string) string {
	var out string
	switch line.OnNoInput {
	case policy.NoInputVoicemail:
		out = fmt.Sprintf("on_no_input: mailbox %q", houseMailbox)
	case policy.NoInputRingHouse:
		out = "on_no_input: the house rings"
	default:
		out = "on_no_input: goodbye"
	}
	if line.ChallengeNoInput {
		out += ", behind the challenge"
	}
	return out
}

func dedupeStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// describeChallenge is the one line check prints for a challenge a line asks.
func describeChallenge(p *policy.Policy) string {
	r := p.Challenge()
	return fmt.Sprintf("\"what is A times B\", %s; MAX_PIN_ATTEMPTS retries, each a new question.\n"+
		"                         A right answer forgives no PIN failure; wrong ones count against\n"+
		"                         the line's challenge budget (RATELIMIT_*), never its PIN budget.",
		r.Describe())
}

// missingClips reports which of the named clips have no file under the media
// prefix in the sounds tree, in the order given. Asterisk picks the format,
// so any extension counts. ok is false when there is no sounds tree to look
// in, and the caller then says nothing at all.
func missingClips(prefix string, clips []string) (missing []string, ok bool) {
	if st, err := os.Stat(soundsDir); err != nil || !st.IsDir() {
		return nil, false
	}
	for _, clip := range clips {
		matches, _ := filepath.Glob(filepath.Join(soundsDir, prefix, clip) + ".*")
		if len(matches) == 0 {
			missing = append(missing, clip)
		}
	}
	return missing, true
}

// warnChallengeClips prints a warning when the bundled challenge set is not
// installed. A warning, never a failure: the call still works — a missing
// clip is silence, not a stuck call — but a caller asked a question they
// cannot hear presses nothing and is dismissed.
func warnChallengeClips() {
	missing, ok := missingClips(lobby.ChallengeMediaPrefix(), lobby.ChallengeClips())
	if !ok || len(missing) == 0 {
		return
	}
	fmt.Printf("  ! the challenge clips are not all installed: %s has no %s.\n"+
		"    Callers will hear silence where the question should be. Build them with\n"+
		"    prompts/build.sh and install them as RUNBOOK \"The arithmetic challenge\" says.\n",
		filepath.Join(soundsDir, lobby.ChallengeMediaPrefix()), strings.Join(missing, ", "))
}
