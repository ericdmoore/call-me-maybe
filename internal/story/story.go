// Package story parses and runs interactive audio stories — the `story` pack
// kind described in docs/STORY-PACKS.md.
//
// A story is one Markdown file. Branching is a document-relative anchor, which
// is what makes the sandbox a property of the format rather than a rule this
// package enforces: there is no syntax for naming a phone number, a handset,
// or another pack, so there is nothing to validate against and nothing to get
// wrong. A hostile story can at worst tell you a bad story.
//
// # A subset, not Markdown
//
// This parses a defined subset line by line rather than implementing
// CommonMark. The subset is small enough to specify exactly (headings,
// paragraphs, ordered lists of links, self-closing tags) and the result still
// renders correctly in any Markdown viewer, which is the point — an author, an
// editor and a parent can all read the story before a character of it reaches
// a TTS vendor. A full parser would accept more syntax than the format defines
// and would have to reject most of it anyway.
package story

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Static limits, checked at build time so a broken story fails on a
// workstation rather than in a child's ear.
const (
	MaxNodes            = 1000
	MaxUtterancesInNode = 20
	MaxChoicesInNode    = 10 // one per digit
)

// RepeatDigit replays the current node from anywhere, without the author
// writing anything. Children need it and it costs no state.
const RepeatDigit = "*"

// A Story is a parsed, validated graph.
type Story struct {
	Meta     Meta
	Voices   map[string]VoiceRef
	Defaults Behaviour
	Nodes    map[string]*Node
	// Order is declaration order, so errors and listings read the way the
	// file does rather than in map order.
	Order []string
}

type Meta struct {
	Title string `yaml:"title"`
	Start string `yaml:"start"`
}

// A VoiceRef names which backend and voice speaks a part.
type VoiceRef struct {
	Backend  string            `yaml:"backend"`
	ID       string            `yaml:"id"`
	Settings map[string]string `yaml:"settings"`
}

// Behaviour is what happens when the caller says nothing or says something
// unrecognised — different events with different answers, which is the one
// idea from VoiceXML worth keeping.
type Behaviour struct {
	Timeout   time.Duration `yaml:"-"`
	TimeoutS  string        `yaml:"timeout"`
	Retries   int           `yaml:"retries"`
	OnTimeout string        `yaml:"on-timeout"`
	OnInvalid string        `yaml:"on-invalid"`
}

// Node is one scene.
type Node struct {
	ID        string
	Lines     []Line
	Choices   []Choice
	Behaviour Behaviour
	// Goto plays this node then continues with no prompt.
	Goto string
	// End finishes the story.
	End bool
	// Line in the source, for error messages.
	Line int
}

// Terminal reports a node the story can stop at.
func (n *Node) Terminal() bool { return n.End || (len(n.Choices) == 0 && n.Goto == "") }

// A Line is either something spoken or a verb.
type Line struct {
	Speaker string // "" for a verb
	Text    string
	Verb    string // pause, sfx
	Attrs   map[string]string
	// Clip is the audio name assigned at parse time: "entrance-01".
	Clip string
}

// Spoken reports a line that becomes audio rendered from text.
func (l Line) Spoken() bool { return l.Verb == "" }

// A Choice is one keypad option.
type Choice struct {
	Digit  string
	Label  string
	Target string
}

// A Problem is one thing wrong, with a line number. Plural by design: an
// author fixing a story wants the whole list, not the first item repeatedly.
type Problem struct {
	Line int
	Msg  string
}

func (p Problem) String() string { return fmt.Sprintf("line %d: %s", p.Line, p.Msg) }

type ProblemList []Problem

func (ps ProblemList) Error() string {
	msgs := make([]string, len(ps))
	for i, p := range ps {
		msgs[i] = p.String()
	}
	return strings.Join(msgs, "\n")
}

// ── parsing ─────────────────────────────────────────────────────────────

var (
	reHeading = regexp.MustCompile(`^##\s+(.+?)\s*$`)
	// "1. [Take the left tunnel](#left-tunnel)" — the list number is the
	// DTMF digit, so the menu renders as a numbered list and is the menu the
	// caller hears.
	reChoice = regexp.MustCompile(`^\s*(\d)\.\s+\[([^\]]*)\]\(#([^)]+)\)\s*$`)
	// "DOORMAN: Good evening." — uppercase-ish label, then a colon.
	reSpeaker = regexp.MustCompile(`^([A-Z][A-Z0-9 _-]*):\s*(.*)$`)
	// The closing slash is required. A bare <end> is an *opening* tag to a
	// Markdown renderer and can swallow everything after it, which would
	// break the "reads correctly in any viewer" property the format rests on.
	reTag  = regexp.MustCompile(`^<([a-z]+)([^>]*?)/>\s*$`)
	reAttr = regexp.MustCompile(`([a-z-]+)\s*=\s*"([^"]*)"`)
)

// Parse reads a story file. It returns as many problems as it can find rather
// than stopping at the first.
func Parse(src []byte) (*Story, error) {
	text := strings.ReplaceAll(string(src), "\r\n", "\n")
	lines := strings.Split(text, "\n")

	front, body, start, err := splitFrontmatter(lines)
	if err != nil {
		return nil, ProblemList{{Line: 1, Msg: err.Error()}}
	}

	s := &Story{Nodes: map[string]*Node{}}
	var problems ProblemList

	var raw struct {
		Story    Meta                `yaml:"story"`
		Voices   map[string]VoiceRef `yaml:"voices"`
		Defaults Behaviour           `yaml:"defaults"`
	}
	if err := yaml.Unmarshal([]byte(strings.Join(front, "\n")), &raw); err != nil {
		return nil, ProblemList{{Line: 1, Msg: "frontmatter is not valid YAML: " + err.Error()}}
	}
	s.Meta, s.Voices = raw.Story, raw.Voices
	s.Defaults = withDefaults(raw.Defaults)
	if d, err := parseDuration(raw.Defaults.TimeoutS); err == nil && d > 0 {
		s.Defaults.Timeout = d
	} else if raw.Defaults.TimeoutS != "" && err != nil {
		problems = append(problems, Problem{1, "defaults.timeout: " + err.Error()})
	}

	// The first declared voice narrates unlabelled paragraphs.
	narrator := firstVoice(front, s.Voices)

	var cur *Node
	for i, line := range body {
		n := start + i + 1
		trimmed := strings.TrimSpace(line)

		switch {
		case trimmed == "":
			continue

		case reHeading.MatchString(trimmed):
			id := Slug(reHeading.FindStringSubmatch(trimmed)[1])
			if _, dup := s.Nodes[id]; dup {
				problems = append(problems, Problem{n, fmt.Sprintf("node %q is defined twice", id)})
			}
			cur = &Node{ID: id, Behaviour: s.Defaults, Line: n}
			s.Nodes[id] = cur
			s.Order = append(s.Order, id)

		case cur == nil:
			problems = append(problems, Problem{n, "content before the first `## node` heading"})

		case reChoice.MatchString(line):
			m := reChoice.FindStringSubmatch(line)
			cur.Choices = append(cur.Choices, Choice{Digit: m[1], Label: m[2], Target: Slug(m[3])})

		case reTag.MatchString(trimmed):
			m := reTag.FindStringSubmatch(trimmed)
			if p := applyTag(cur, m[1], attrs(m[2]), n); p != nil {
				problems = append(problems, *p)
			}

		case strings.HasPrefix(trimmed, "<"):
			problems = append(problems, Problem{n, "tags must be self-closing, e.g. <pause seconds=\"2\"/>"})

		case reSpeaker.MatchString(trimmed):
			m := reSpeaker.FindStringSubmatch(trimmed)
			cur.Lines = append(cur.Lines, Line{Speaker: m[1], Text: strings.TrimSpace(m[2])})

		default:
			// An unlabelled paragraph is the narrator's, and a continuation
			// line joins the utterance above it.
			if k := len(cur.Lines) - 1; k >= 0 && cur.Lines[k].Spoken() && !blankBefore(body, i) {
				cur.Lines[k].Text += " " + trimmed
				continue
			}
			cur.Lines = append(cur.Lines, Line{Speaker: narrator, Text: trimmed})
		}
	}

	assignClips(s)

	if len(problems) > 0 {
		return s, problems
	}
	return s, nil
}

// blankBefore reports whether the previous source line was empty, which is
// what separates one utterance from the next.
func blankBefore(body []string, i int) bool {
	return i == 0 || strings.TrimSpace(body[i-1]) == ""
}

func applyTag(n *Node, name string, a map[string]string, line int) *Problem {
	switch name {
	case "end":
		n.End = true
	case "goto":
		t := a["anchor"]
		if t == "" {
			return &Problem{line, "<goto/> needs an anchor"}
		}
		n.Goto = Slug(strings.TrimPrefix(t, "#"))
	case "choices":
		if v, ok := a["timeout"]; ok {
			d, err := parseDuration(v)
			if err != nil {
				return &Problem{line, "timeout: " + err.Error()}
			}
			n.Behaviour.Timeout = d
		}
		if v, ok := a["retries"]; ok {
			r, err := strconv.Atoi(v)
			if err != nil || r < 0 {
				return &Problem{line, "retries must be a non-negative number"}
			}
			n.Behaviour.Retries = r
		}
		if v, ok := a["on-timeout"]; ok {
			n.Behaviour.OnTimeout = v
		}
		if v, ok := a["on-invalid"]; ok {
			n.Behaviour.OnInvalid = v
		}
	case "pause", "sfx":
		n.Lines = append(n.Lines, Line{Verb: name, Attrs: a})
	default:
		return &Problem{line, fmt.Sprintf("unknown verb <%s/>", name)}
	}
	return nil
}

func attrs(s string) map[string]string {
	out := map[string]string{}
	for _, m := range reAttr.FindAllStringSubmatch(s, -1) {
		out[m[1]] = m[2]
	}
	return out
}

// assignClips gives every spoken line a stable audio name. Readable rather
// than a hash, because an author looking at a directory of WAVs should be able
// to tell which scene they came from.
func assignClips(s *Story) {
	for _, id := range s.Order {
		n := s.Nodes[id]
		k := 0
		for i := range n.Lines {
			if !n.Lines[i].Spoken() {
				continue
			}
			k++
			n.Lines[i].Clip = fmt.Sprintf("%s-%02d", id, k)
		}
	}
}

func splitFrontmatter(lines []string) (front, body []string, start int, err error) {
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return nil, lines, 0, nil // frontmatter is optional; validation catches what is missing
	}
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			return lines[1:i], lines[i+1:], i, nil
		}
	}
	return nil, nil, 0, fmt.Errorf("frontmatter is never closed with ---")
}

// firstVoice returns the voice declared first in the file, which narrates
// unlabelled paragraphs. Map order is random, so the source is re-scanned.
func firstVoice(front []string, voices map[string]VoiceRef) string {
	for _, l := range front {
		t := strings.TrimSpace(l)
		name, _, ok := strings.Cut(t, ":")
		if !ok {
			continue
		}
		if _, isVoice := voices[name]; isVoice {
			return name
		}
	}
	names := make([]string, 0, len(voices))
	for n := range voices {
		names = append(names, n)
	}
	sort.Strings(names)
	if len(names) > 0 {
		return names[0]
	}
	return ""
}

func withDefaults(b Behaviour) Behaviour {
	if b.Timeout == 0 {
		b.Timeout = 8 * time.Second
	}
	if b.Retries == 0 {
		b.Retries = 2
	}
	if b.OnTimeout == "" {
		b.OnTimeout = "repeat"
	}
	if b.OnInvalid == "" {
		b.OnInvalid = "reprompt"
	}
	return b
}

func parseDuration(s string) (time.Duration, error) {
	if s == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("%q is not a duration (try 8s)", s)
	}
	if d <= 0 {
		return 0, fmt.Errorf("%q must be positive", s)
	}
	return d, nil
}

// Slug is the heading-to-anchor rule every Markdown renderer already uses, so
// the links work in a browser as well as on the phone.
func Slug(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '_':
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

// ── validation ──────────────────────────────────────────────────────────

// Validate reports everything wrong with a parsed story. Warnings are things
// worth knowing that do not stop a build — an unreachable node is usually a
// draft's offcut, not a bug.
func (s *Story) Validate() (problems ProblemList, warnings ProblemList) {
	if s.Meta.Start == "" {
		problems = append(problems, Problem{1, "frontmatter needs story.start naming the first node"})
	} else if _, ok := s.Nodes[Slug(s.Meta.Start)]; !ok {
		problems = append(problems, Problem{1, fmt.Sprintf("story.start names %q, which is not a node", s.Meta.Start)})
	}
	if len(s.Voices) == 0 {
		problems = append(problems, Problem{1, "frontmatter needs at least one entry under voices"})
	}
	if len(s.Nodes) > MaxNodes {
		problems = append(problems, Problem{1, fmt.Sprintf("%d nodes exceeds the limit of %d", len(s.Nodes), MaxNodes)})
	}

	for _, id := range s.Order {
		n := s.Nodes[id]
		problems = append(problems, s.checkNode(n)...)
	}

	// Reachability from the start node.
	if _, ok := s.Nodes[Slug(s.Meta.Start)]; ok {
		seen := map[string]bool{}
		s.walk(Slug(s.Meta.Start), seen)
		for _, id := range s.Order {
			if !seen[id] {
				warnings = append(warnings, Problem{s.Nodes[id].Line,
					fmt.Sprintf("node %q cannot be reached from the start", id)})
			}
		}
	}
	return problems, warnings
}

func (s *Story) checkNode(n *Node) (problems ProblemList) {
	spoken := 0
	for _, l := range n.Lines {
		if l.Spoken() {
			spoken++
			if l.Speaker == "" {
				problems = append(problems, Problem{n.Line,
					fmt.Sprintf("node %q has an unlabelled line and no voice is declared to narrate it", n.ID)})
			} else if _, ok := s.Voices[l.Speaker]; !ok {
				problems = append(problems, Problem{n.Line,
					fmt.Sprintf("node %q: %s is not declared under voices", n.ID, l.Speaker)})
			}
		}
		if l.Verb == "sfx" && l.Attrs["name"] == "" {
			problems = append(problems, Problem{n.Line, fmt.Sprintf("node %q: <sfx/> needs a name", n.ID)})
		}
	}
	if spoken > MaxUtterancesInNode {
		problems = append(problems, Problem{n.Line,
			fmt.Sprintf("node %q has %d utterances, limit is %d", n.ID, spoken, MaxUtterancesInNode)})
	}
	if len(n.Choices) > MaxChoicesInNode {
		problems = append(problems, Problem{n.Line,
			fmt.Sprintf("node %q has %d choices, limit is %d (one per digit)", n.ID, len(n.Choices), MaxChoicesInNode)})
	}

	seen := map[string]bool{}
	for _, c := range n.Choices {
		if seen[c.Digit] {
			problems = append(problems, Problem{n.Line,
				fmt.Sprintf("node %q uses digit %s twice", n.ID, c.Digit)})
		}
		seen[c.Digit] = true
		if _, ok := s.Nodes[c.Target]; !ok {
			problems = append(problems, Problem{n.Line,
				fmt.Sprintf("node %q: choice %s points at %q, which is not a node", n.ID, c.Digit, c.Target)})
		}
	}
	if n.Goto != "" {
		if _, ok := s.Nodes[n.Goto]; !ok {
			problems = append(problems, Problem{n.Line,
				fmt.Sprintf("node %q: <goto/> points at %q, which is not a node", n.ID, n.Goto)})
		}
		if len(n.Choices) > 0 {
			problems = append(problems, Problem{n.Line,
				fmt.Sprintf("node %q has both choices and a <goto/>; it can only do one", n.ID)})
		}
	}
	// on-timeout / on-invalid may name an anchor.
	for _, b := range []struct{ what, v string }{{"on-timeout", n.Behaviour.OnTimeout}, {"on-invalid", n.Behaviour.OnInvalid}} {
		if strings.HasPrefix(b.v, "#") {
			if _, ok := s.Nodes[Slug(strings.TrimPrefix(b.v, "#"))]; !ok {
				problems = append(problems, Problem{n.Line,
					fmt.Sprintf("node %q: %s points at %q, which is not a node", n.ID, b.what, b.v)})
			}
		} else if b.v != "repeat" && b.v != "reprompt" && b.v != "end" {
			problems = append(problems, Problem{n.Line,
				fmt.Sprintf("node %q: %s must be repeat, reprompt, end, or #anchor", n.ID, b.what)})
		}
	}
	return problems
}

func (s *Story) walk(id string, seen map[string]bool) {
	if seen[id] {
		return
	}
	n, ok := s.Nodes[id]
	if !ok {
		return
	}
	seen[id] = true
	for _, c := range n.Choices {
		s.walk(c.Target, seen)
	}
	if n.Goto != "" {
		s.walk(n.Goto, seen)
	}
	for _, v := range []string{n.Behaviour.OnTimeout, n.Behaviour.OnInvalid} {
		if strings.HasPrefix(v, "#") {
			s.walk(Slug(strings.TrimPrefix(v, "#")), seen)
		}
	}
}

// Utterances lists every line that needs rendering, in declaration order, so
// the pack builder can walk them and estimate before spending.
func (s *Story) Utterances() []Line {
	var out []Line
	for _, id := range s.Order {
		for _, l := range s.Nodes[id].Lines {
			if l.Spoken() {
				out = append(out, l)
			}
		}
	}
	return out
}

// A Clip is one thing that needs rendering to audio.
type Clip struct {
	Name    string
	Text    string
	Speaker string
}

// Clips lists everything a pack builder must render: every utterance, and
// every choice label.
//
// Choice labels are rendered exactly as written, with no "Press one to…"
// wrapper. The builder has no business injecting English into a format that
// should work in any language — an author who wants the digit spoken writes it
// into the label.
func (s *Story) Clips() []Clip {
	narrator := ""
	for _, id := range s.Order {
		for _, l := range s.Nodes[id].Lines {
			if l.Spoken() && l.Speaker != "" {
				narrator = l.Speaker
				break
			}
		}
		if narrator != "" {
			break
		}
	}

	var out []Clip
	for _, id := range s.Order {
		n := s.Nodes[id]
		for _, l := range n.Lines {
			if l.Spoken() {
				out = append(out, Clip{Name: l.Clip, Text: l.Text, Speaker: l.Speaker})
			}
		}
		for _, c := range n.Choices {
			out = append(out, Clip{
				Name:    "choice/" + n.ID + "-" + c.Digit,
				Text:    stripMarkup(c.Label),
				Speaker: narrator,
			})
		}
	}
	return out
}

// stripMarkup removes emphasis marks and link syntax from a label so they are
// not read aloud. The marks stay in the source because they are prosody, and a
// backend that understands direction may still want them; this is only for the
// plain-text path.
func stripMarkup(s string) string {
	r := strings.NewReplacer("**", "", "__", "", "*", "", "_", "", "`", "")
	return strings.TrimSpace(r.Replace(s))
}
