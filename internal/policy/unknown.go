package policy

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"

	"github.com/BurntSushi/toml"
)

// Unknown keys: why this file exists.
//
// toml.Unmarshal fills a struct and quietly drops any key with no matching
// field. Validation then runs on the struct, so by the time compileChecked
// sees the config the typo is already gone — there is nothing left to
// complain about. That is fine for keys whose absence is illegal (`handets`
// instead of `handsets` leaves an empty list, and a semantic rule catches
// it), and silent for exactly the keys whose absence is legal: the optional
// ones, and the ones that do not exist yet. `on_no_input = "voicemail"` on an
// extension validated cleanly and did nothing at all.
//
// toml.Decode's MetaData records which keys it managed to place, so
// Undecoded() is the list of keys the file offered and the schema ignored.
// Comparing it against the shape of File is what turns "silently dropped"
// into "reported". The schema `doorman schema` publishes has always said
// additionalProperties: false; this is the loader finally agreeing with it.

// UnknownKey is one key that a config file contained and the schema has no
// field for. It carries enough structure to build a message and to place an
// editor diagnostic, rather than being pre-formatted, because `doorman
// check`, the daemon's warning log, and the LSP each want it differently.
type UnknownKey struct {
	// File is "policy", "handsets" or "trunks" — which of the three it was
	// found in, matching the prefix the loader's other errors use.
	File string
	// Path is the full dotted path as TOML reports it: "house.voicmail".
	Path string
	// Key is the offending segment alone: "voicmail".
	Key string
	// Section renders the table the key sits in the way it is written in the
	// file — "[house]", "[[extensions]]", "[[extensions.steps]]" — and is
	// empty for a key at the top level.
	Section string
	// Suggest is the nearest valid key at that level, or "" when nothing is
	// close enough to guess at.
	Suggest string
	// Hint replaces Suggest when the key is not a typo at all but a section
	// that exists in a different file. "unknown section [[trunks]]" is true and
	// unhelpful when trunks.toml is sitting right beside the file it was
	// written in, and no edit distance can produce the useful answer.
	Hint string
	// Table is set when the unknown key is itself a table, i.e. a mistyped
	// section header such as [[extenions]].
	Table bool
}

// String is the human message, without the file prefix: callers that handle
// both files add `u.File + ": "`, and the LSP — which already knows which
// document it is annotating — does not.
//
// Only the offending name is quoted, and that is deliberate rather than
// stylistic. The language server places a squiggle by searching the document
// for the quoted tokens in a problem string, so quoting the suggestion too
// would let the marker land on the correctly-spelled key somewhere else in
// the file.
func (u UnknownKey) String() string {
	kind := "unknown key"
	if u.Table {
		kind = "unknown section"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s %q", kind, u.Key)
	if u.Section != "" {
		fmt.Fprintf(&b, " in %s", u.Section)
	}
	switch {
	case u.Hint != "":
		fmt.Fprintf(&b, " — %s", u.Hint)
	case u.Suggest != "":
		fmt.Fprintf(&b, " — did you mean %s?", u.Suggest)
	}
	return b.String()
}

// ── the shape of File, as a lookup ───────────────────────────────────────

// keyTable is what File permits at one table path: every valid key name, and
// the subset of those that are themselves tables. Derived from the struct by
// reflection rather than written out, so it cannot drift from the thing it
// describes — the same reason internal/schema's tests walk the tags.
type keyTable struct {
	array  bool // written [[x]] rather than [x]
	fields map[string]bool
	tables map[string]*keyTable
}

// fileShape is computed once: File is immutable and acyclic, so there is
// nothing to invalidate.
var fileShape = sync.OnceValue(func() *keyTable {
	return describeType(reflect.TypeOf(File{}), false)
})

// trunkFileShape is the same trick for trunks.toml. It gets its own root
// struct rather than more fields on File because each file owns its sections
// exclusively — folding [[trunks]] into File would mean the detector could no
// longer tell a section in the wrong file from a section in the right one.
var trunkFileShape = sync.OnceValue(func() *keyTable {
	return describeType(reflect.TypeOf(TrunkFile{}), false)
})

// misplaced names sections that exist, but not in the file they turned up in.
// Keyed by "<file>.<dotted path>". Each file owning its sections exclusively is
// the design; this is the design saying so out loud, because "unknown section"
// sends someone hunting for a typo that is not there.
var misplaced = map[string]string{
	"policy.trunks":     "[[trunks]] belongs in trunks.toml",
	"handsets.trunks":   "[[trunks]] belongs in trunks.toml",
	"trunks.line":       "[line] belongs in policy.toml — one shared inventory cannot hold something true of only one line",
	"trunks.house":      "[house] belongs in policy.toml",
	"trunks.people":     "[[people]] belongs in policy.toml",
	"trunks.extensions": "[[extensions]] belongs in policy.toml",
	"trunks.schedules":  "[[schedules]] belongs in policy.toml",
	"trunks.handsets":   "[[handsets]] belongs in handsets.toml",
	"trunks.groups":     "[[groups]] belongs in handsets.toml",
}

func describeType(t reflect.Type, array bool) *keyTable {
	kt := &keyTable{
		array:  array,
		fields: make(map[string]bool, t.NumField()),
		tables: map[string]*keyTable{},
	}
	for i := range t.NumField() {
		f := t.Field(i)
		name := strings.Split(f.Tag.Get("toml"), ",")[0]
		if name == "" || name == "-" {
			continue
		}
		kt.fields[name] = true

		ft, isArray := f.Type, false
		for ft.Kind() == reflect.Pointer || ft.Kind() == reflect.Slice {
			if ft.Kind() == reflect.Slice {
				isArray = true
			}
			ft = ft.Elem()
		}
		// Only struct-valued fields are tables. []string, *bool and `any` are
		// leaves — and `any` mattering is the point of the check below.
		if ft.Kind() == reflect.Struct {
			kt.tables[name] = describeType(ft, isArray)
		}
	}
	return kt
}

// ── detection ────────────────────────────────────────────────────────────

// unknownKeys turns a decoder's leftovers into reportable problems. file is
// "policy", "handsets" or "trunks"; root is the shape that file is expected to
// have, since the three are different structs.
func unknownKeys(md toml.MetaData, file string, root *keyTable) []UnknownKey {
	var out []UnknownKey
	reported := map[string]bool{}

	for _, k := range md.Undecoded() {
		parts := []string(k)
		if len(parts) == 0 {
			continue
		}
		// A mistyped section header arrives as the section plus every key
		// beneath it; the message about the section is the only useful one.
		// This also collapses the same typo repeated across several
		// [[extensions]] entries, which share one dotted path.
		if reportedPrefix(reported, parts) {
			continue
		}

		node, walked, at := root, []string(nil), -1
		for i, p := range parts {
			if child, ok := node.tables[p]; ok {
				node = child
				walked = append(walked, p)
				continue
			}
			if node.fields[p] {
				// A known leaf. Whatever sits below it is that field's own
				// opaque content, not an unknown key: Extension.Afterhours
				// decodes into an `any`, so the retired inline-table form
				// leaves afterhours.start/.end undecoded even though
				// `afterhours` itself is perfectly valid — and compileChecked
				// already has a far better message for that mistake than
				// "unknown key" would be.
				at = -1
				break
			}
			at = i
			break
		}
		if at < 0 {
			continue
		}

		path := strings.Join(parts[:at+1], ".")
		reported[path] = true
		u := UnknownKey{
			File:    file,
			Path:    path,
			Key:     parts[at],
			Suggest: nearest(parts[at], node.fields),
		}
		if hint, ok := misplaced[file+"."+path]; ok {
			u.Hint, u.Suggest = hint, ""
		}
		if len(walked) > 0 {
			u.Section = renderSection(walked, node.array)
		}
		switch md.Type(parts[:at+1]...) {
		case "Hash", "ArrayHash":
			u.Table = true
		}
		out = append(out, u)
	}
	return out
}

func reportedPrefix(reported map[string]bool, parts []string) bool {
	for i := 1; i <= len(parts); i++ {
		if reported[strings.Join(parts[:i], ".")] {
			return true
		}
	}
	return false
}

func renderSection(path []string, array bool) string {
	joined := strings.Join(path, ".")
	if array {
		return "[[" + joined + "]]"
	}
	return "[" + joined + "]"
}

// ── "did you mean" ───────────────────────────────────────────────────────

// nearest is the closest valid key by edit distance, or "" when nothing is
// near enough to be worth guessing at. One or two slips is a typo; three is a
// different word, and a confidently wrong suggestion sends someone looking in
// the wrong place — worse than no suggestion at all.
func nearest(key string, valid map[string]bool) string {
	limit := 2
	if len(key) < 5 {
		limit = 1
	}

	names := make([]string, 0, len(valid))
	for n := range valid {
		names = append(names, n)
	}
	sort.Strings(names) // deterministic when two candidates tie

	best, bestDist := "", 0
	for _, n := range names {
		d := editDistance(key, n)
		if d > limit {
			continue
		}
		if best == "" || d < bestDist {
			best, bestDist = n, d
		}
	}
	return best
}

// editDistance is Levenshtein over two rolling rows. The inputs are TOML key
// names, so a simple implementation is comfortably fast enough.
func editDistance(a, b string) int {
	ar, br := []rune(a), []rune(b)
	prev := make([]int, len(br)+1)
	curr := make([]int, len(br)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ar); i++ {
		curr[0] = i
		for j := 1; j <= len(br); j++ {
			cost := 1
			if ar[i-1] == br[j-1] {
				cost = 0
			}
			curr[j] = min(prev[j]+1, curr[j-1]+1, prev[j-1]+cost)
		}
		prev, curr = curr, prev
	}
	return prev[len(br)]
}
