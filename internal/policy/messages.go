package policy

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// messages.toml — who may text the house which word, and what the word does.
//
// A text to the house number is control plane, never conversation (s15,
// "one number, one purpose"): a known word from a listed person does one
// thing and gets one boring reply; everything else is archived by the
// carrier's email forwarding and never answered. This file is the list of
// words. People are named by their [[people]] id in policy.toml, never by a
// number retyped here, and never by an index.

// MessageFile is messages.toml as written.
type MessageFile struct {
	Words []Word `toml:"words"`
}

// Word is one thing the house understands by text.
type Word struct {
	// Word is what the sender types: lowercase, letters and digits, no
	// spaces. Matched exactly against the trimmed, lowercased text.
	Word string `toml:"word"`
	// People are the [[people]] ids allowed to say it, or ["*"] for
	// everyone on the allow-list. A stricter list than the ring allow-list
	// by design: ringing the house and opening its garage are different
	// trusts.
	People []string `toml:"people"`
	// Action is the [[actions]] id in policy.toml this word performs — what
	// it does, who may, what it says back and whether it confirms all live
	// there, once, for every transport (s13).
	Action string `toml:"action"`
	// Webhook is POSTed to when the word arrives from a listed sender —
	// Home Assistant's webhook, typically. The stopgap that shipped before
	// [[actions]]: still accepted, reported by `doorman check`, and going.
	Webhook string `toml:"webhook"`
	// Reply is texted back. Optional, and boring on purpose: plain ASCII,
	// under 160 characters, no digits, no links, no exclamation marks —
	// one segment on the bill and past the carrier's spam filter.
	Reply string `toml:"reply"`
}

// Messages is a validated messages.toml.
type Messages struct {
	Path string

	present bool
	words   []Word
}

func (m *Messages) Present() bool { return m != nil && m.present }

func (m *Messages) Where() string {
	if m == nil || m.Path == "" {
		return "messages.toml"
	}
	return m.Path
}

// Words are the words in declaration order.
func (m *Messages) Words() []Word {
	if m == nil {
		return nil
	}
	return m.words
}

// Lookup finds a word.
func (m *Messages) Lookup(word string) (Word, bool) {
	for _, w := range m.Words() {
		if w.Word == word {
			return w, true
		}
	}
	return Word{}, false
}

// ActionsReferenced is every [[actions]] id any word names, sorted.
func (m *Messages) ActionsReferenced() []string {
	seen := map[string]bool{}
	for _, w := range m.Words() {
		if w.Action != "" {
			seen[w.Action] = true
		}
	}
	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// PeopleReferenced is every [[people]] id any word names, sorted, for
// `doorman check` to cross-reference against policy.toml.
func (m *Messages) PeopleReferenced() []string {
	seen := map[string]bool{}
	for _, w := range m.Words() {
		for _, id := range w.People {
			if id != "*" {
				seen[id] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

var wordPattern = regexp.MustCompile(`^[a-z0-9]+$`)

// LoadMessages reads messages.toml; absent is a usable, empty inventory.
func LoadMessages(path string) (*Messages, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &Messages{}, nil
	}
	if err != nil {
		return nil, err
	}
	m, merr := MessagesFromTOML(data)
	if merr != nil {
		return nil, merr
	}
	m.Path = path
	return m, nil
}

// MessagesFromTOML parses and validates.
func MessagesFromTOML(data []byte) (*Messages, error) {
	var f MessageFile
	md, err := toml.Decode(string(data), &f)
	if err != nil {
		return nil, err
	}
	var problems []string
	for _, u := range md.Undecoded() {
		problems = append(problems, fmt.Sprintf("unknown key %q", u.String()))
	}
	fail := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }

	m := &Messages{present: true}
	if len(f.Words) == 0 {
		fail("at least one [[words]] entry is required — delete messages.toml entirely to answer no texts")
	}
	seen := map[string]bool{}
	for _, w := range f.Words {
		where := fmt.Sprintf("word %q", w.Word)
		if !wordPattern.MatchString(w.Word) {
			fail("%s must be lowercase letters and digits, no spaces", where)
			continue
		}
		if seen[w.Word] {
			fail("duplicate %s", where)
			continue
		}
		seen[w.Word] = true
		if len(w.People) == 0 {
			fail("%s names nobody — list [[people]] ids, or [\"*\"] for everyone on the allow-list", where)
		}
		for _, id := range w.People {
			if id != "*" && !handsetIDPattern.MatchString(id) {
				fail("%s: %q is not a [[people]] id (lowercase alphanumeric/dash/underscore) or \"*\"", where, id)
			}
		}
		if w.Action != "" && !handsetIDPattern.MatchString(w.Action) {
			fail("%s: action %q is not an [[actions]] id", where, w.Action)
		}
		if w.Action != "" && w.Webhook != "" {
			fail("%s names an action and carries its own webhook — the action owns the webhook", where)
		}
		if w.Action == "" && w.Webhook == "" && w.Reply == "" {
			fail("%s does nothing — name an action, or give it a reply", where)
		}
		if w.Webhook != "" {
			if u, err := url.Parse(w.Webhook); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				fail("%s: webhook must be an http or https URL", where)
			}
		}
		if w.Reply != "" {
			if why := ReplyProblem(w.Reply); why != "" {
				fail("%s: reply %s", where, why)
			}
		}
		m.words = append(m.words, w)
	}
	if len(problems) > 0 {
		return nil, errors.New(strings.Join(problems, "\n"))
	}
	return m, nil
}

// ReplyProblem says why a reply may not be sent, or "" when it may. Plain
// ASCII, one segment, no digits, no links, no exclamation marks: one rule
// with two reasons — one segment on the bill, and past the carrier's spam
// filter, which ate the first text this house ever sent.
func ReplyProblem(reply string) string {
	switch {
	case strings.TrimSpace(reply) == "":
		return "is empty"
	case len(reply) > 160:
		return fmt.Sprintf("is %d characters; one segment is 160", len(reply))
	case strings.ContainsAny(reply, "!"):
		return "has an exclamation mark — carriers read that as spam"
	case strings.Contains(strings.ToLower(reply), "http"):
		return "has a link — carriers drop those from a VoIP number"
	case strings.ContainsAny(reply, "0123456789"):
		return "has digits — a number in a text is what the carrier filter drops first"
	}
	for _, r := range reply {
		if r > 126 || (r < 32 && r != '\n') {
			return fmt.Sprintf("has a non-ASCII character %q — that alone halves the segment to seventy characters", r)
		}
	}
	return ""
}
