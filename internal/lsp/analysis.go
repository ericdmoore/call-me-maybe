// Package lsp makes doorman a language server for policy.toml and
// handsets.toml. The value is not TOML shape — Taplo and JSON schemas do
// shape — it is the cross-file semantics: "kids-rom" is not a handset,
// "summer" is not a schedule, this extension has afterhours but nowhere to
// send callers. Diagnostics come from the exact same compiler that guards
// the daemon's hot reload, so the editor can never disagree with the phone.
package lsp

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	"callmemaybe/internal/policy"
)

// DocKind classifies which of the two config files a document is.
type DocKind int

const (
	KindPolicy DocKind = iota
	KindHandsets
)

// KindOf classifies by file name; anything not named handsets.toml is
// treated as policy (covering renamed or legacy files).
func KindOf(path string) DocKind {
	if strings.HasSuffix(path, "handsets.toml") {
		return KindHandsets
	}
	return KindPolicy
}

// Position/Range/Diagnostic mirror the LSP structures (0-based lines).
type Position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

type Diagnostic struct {
	Range    Range  `json:"range"`
	Severity int    `json:"severity"` // 1 = error
	Source   string `json:"source"`
	Message  string `json:"message"`
}

// Analyse lints the pair and attributes each problem to one of the two
// documents with a best-effort position. handsetsText nil means legacy
// single-file layout.
func Analyse(policyText string, handsetsText *string) (policyDiags, handsetsDiags []Diagnostic) {
	// Syntax errors first: the decoder knows the exact line.
	if d, bad := syntaxDiagnostic(policyText); bad {
		policyDiags = append(policyDiags, d)
	}
	if handsetsText != nil {
		if d, bad := syntaxDiagnostic(*handsetsText); bad {
			handsetsDiags = append(handsetsDiags, d)
		}
	}
	if len(policyDiags) > 0 || len(handsetsDiags) > 0 {
		return
	}

	var hdata []byte
	if handsetsText != nil {
		hdata = []byte(*handsetsText)
	}
	for _, problem := range policy.LintSplit([]byte(policyText), hdata) {
		d := Diagnostic{Severity: 1, Source: "doorman", Message: problem}
		// Our error messages quote the offending value, conventionally last
		// ('extension "Kids" references unknown handset "kids-rom"'), so
		// quoted tokens are tried in reverse and the first that locates in a
		// document places the squiggle. Problems with no locatable token
		// land at the top of the policy file, where the rules live.
		placed := false
		for _, token := range quotedTokensReversed(problem) {
			// Handsets doc first: an id that exists there makes this an
			// inventory problem (duplicate number, bad endpoint). A policy
			// problem's offending token — an unknown id, a bad schedule —
			// by definition does not exist in the inventory, so it falls
			// through to the policy doc.
			if handsetsText != nil {
				if r, ok := locate(*handsetsText, token); ok {
					d.Range = r
					handsetsDiags = append(handsetsDiags, d)
					placed = true
					break
				}
			}
			if r, ok := locate(policyText, token); ok {
				d.Range = r
				policyDiags = append(policyDiags, d)
				placed = true
				break
			}
		}
		if !placed {
			policyDiags = append(policyDiags, d)
		}
	}
	return
}

func syntaxDiagnostic(text string) (Diagnostic, bool) {
	var f policy.File
	_, err := toml.Decode(text, &f)
	if err == nil {
		return Diagnostic{}, false
	}
	d := Diagnostic{Severity: 1, Source: "doorman", Message: err.Error()}
	var pe toml.ParseError
	if ok := asParseError(err, &pe); ok {
		line := pe.Position.Line - 1 // TOML positions are 1-based
		if line < 0 {
			line = 0
		}
		d.Message = pe.Message
		d.Range = Range{Start: Position{Line: line}, End: Position{Line: line, Character: 200}}
	}
	return d, true
}

func asParseError(err error, target *toml.ParseError) bool {
	if pe, ok := err.(toml.ParseError); ok {
		*target = pe
		return true
	}
	return false
}

var quoted = regexp.MustCompile(`"([^"]+)"`)

func quotedTokensReversed(message string) []string {
	matches := quoted.FindAllStringSubmatch(message, -1)
	out := make([]string, 0, len(matches))
	for i := len(matches) - 1; i >= 0; i-- {
		out = append(out, matches[i][1])
	}
	return out
}

// locate finds the first occurrence of token as a quoted value or bare word
// in the text and returns its range.
func locate(text, token string) (Range, bool) {
	for i, line := range strings.Split(text, "\n") {
		if col := strings.Index(line, `"`+token+`"`); col >= 0 {
			return Range{
				Start: Position{Line: i, Character: col + 1},
				End:   Position{Line: i, Character: col + 1 + len(token)},
			}, true
		}
		if col := strings.Index(line, token); col >= 0 {
			return Range{
				Start: Position{Line: i, Character: col},
				End:   Position{Line: i, Character: col + len(token)},
			}, true
		}
	}
	return Range{}, false
}

// ── Completions ──────────────────────────────────────────────────────────

// Model is the reference vocabulary extracted from the current documents:
// what ids exist to be completed. It parses leniently — a half-typed file
// still yields whatever decodes.
type Model struct {
	HandsetIDs []string
	GroupIDs   []string
	Schedules  []string
	Mailboxes  []string
}

func BuildModel(policyText string, handsetsText *string) Model {
	var m Model
	seenMailbox := map[string]bool{}
	addMailbox := func(mb string) {
		if mb != "" && !seenMailbox[mb] {
			seenMailbox[mb] = true
			m.Mailboxes = append(m.Mailboxes, mb)
		}
	}

	var pf policy.File
	_, _ = toml.Decode(policyText, &pf)
	inventory := pf // legacy layout carries handsets inline
	if handsetsText != nil {
		var hf policy.File
		_, _ = toml.Decode(*handsetsText, &hf)
		inventory = hf
	}
	for _, h := range inventory.Handsets {
		m.HandsetIDs = append(m.HandsetIDs, h.ID)
		addMailbox(h.Mailbox)
	}
	for _, g := range inventory.Groups {
		m.GroupIDs = append(m.GroupIDs, g.ID)
	}
	for _, sc := range pf.Schedules {
		m.Schedules = append(m.Schedules, sc.ID)
	}
	addMailbox(pf.House.Voicemail)
	for _, e := range pf.Extensions {
		addMailbox(e.Voicemail)
	}
	sort.Strings(m.HandsetIDs)
	sort.Strings(m.GroupIDs)
	sort.Strings(m.Schedules)
	sort.Strings(m.Mailboxes)
	return m
}

// CompletionItem mirrors the LSP structure.
type CompletionItem struct {
	Label  string `json:"label"`
	Kind   int    `json:"kind"` // 12 = Value, 6 = Variable
	Detail string `json:"detail,omitempty"`
}

var (
	handsetsCtx   = regexp.MustCompile(`handsets\s*=\s*\[[^\]]*$`)
	afterhoursCtx = regexp.MustCompile(`afterhours\s*=\s*"[^"]*$`)
	daysCtx       = regexp.MustCompile(`days\s*=\s*\[[^\]]*$`)
	mailboxCtx    = regexp.MustCompile(`(voicemail|mailbox)\s*=\s*"[^"]*$`)
)

var allDays = []string{"SU", "MO", "TU", "WE", "TH", "FR", "SA"}

// Complete returns items for the cursor position. line is the text of the
// current line; col is the cursor column within it.
func Complete(m Model, line string, col int) []CompletionItem {
	if col > len(line) {
		col = len(line)
	}
	prefix := line[:col]

	switch {
	case afterhoursCtx.MatchString(prefix):
		return items(6, "schedule", m.Schedules)
	case daysCtx.MatchString(prefix):
		return items(12, "day (window start)", allDays)
	case mailboxCtx.MatchString(prefix):
		return items(6, "mailbox (voicemail.conf [household])", m.Mailboxes)
	case handsetsCtx.MatchString(prefix):
		out := items(6, "handset", m.HandsetIDs)
		return append(out, items(6, "group", m.GroupIDs)...)
	}
	return nil
}

func items(kind int, detail string, labels []string) []CompletionItem {
	out := make([]CompletionItem, 0, len(labels))
	for _, l := range labels {
		out = append(out, CompletionItem{Label: l, Kind: kind, Detail: detail})
	}
	return out
}

// Describe is used by tests and debug logging.
func (m Model) Describe() string {
	return fmt.Sprintf("handsets=%d groups=%d schedules=%d mailboxes=%d",
		len(m.HandsetIDs), len(m.GroupIDs), len(m.Schedules), len(m.Mailboxes))
}
