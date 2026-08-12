// Package contacts turns address-book exports into an admission decision.
//
// It reads the sources named in contacts.toml, parses each as vCard, normalises
// every telephone number to E.164, classifies each as personal or published,
// and unions the lot into one set keyed by number. `doorman check` reports what
// that adds up to, and the lobby consults Lookup once per call: a personal
// contact skips the lobby, a published one hears it, a blocked one hears
// nothing at all.
//
// # Not authoritative, ever
//
// Everything here is derived, disposable data. Delete every source and the
// phone still works: callers who would have been admitted simply hear the lobby
// like anyone else, and `[[people]]` in policy.toml — the deliberate list, the
// one an operator wrote by hand — is untouched, because it lives in a different
// file and is compiled by a different package. That is invariant 10's posture
// applied to a second kind of derived input, and it is why a source that cannot
// be read is reported rather than fatal.
//
// # Caller data
//
// A contact's name and number are caller data under invariant 1. This package
// never logs; it returns counts and, for a lookup, one entry. Whatever displays
// an entry is responsible for redacting it — `policy.Redact` for logs, and for
// the operator-facing `doorman check`, counts only. There is no setting that
// prints an address book to a terminal, because "how many" answers every
// question an operator has here and "who" answers none of them.
package contacts

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"callmemaybe/internal/policy"
)

// Entry is one number in the merged set: who it belongs to, how it reads, and
// which source said so.
type Entry struct {
	// E164 is the normalised number, and the key the whole set is built on.
	// Two vCards carrying the same number are the same person for admission
	// purposes, whatever they are called.
	E164 string
	// Name is for the handset display and for a call record, exactly as an
	// allow-list match's name is. It is caller data: redact it in logs.
	Name string
	// Class is personal or published. Published on any disagreement.
	Class Class
	// Reason is which signal decided Class.
	Reason Reason
	// Blocked means some block source named this number. It beats everything,
	// including a personal classification from an admit source and including
	// `[[people]]`: a number in both is dismissed, and `doorman check` fails
	// rather than leaving the contradiction to a ranking.
	Blocked bool
	// Source is the id of the source that supplied Name: the first in
	// declaration order to mention this number, which is the only ordering
	// there is.
	Source string
}

// SourceReport is what one source contributed. Counts only — never a name and
// never a number.
type SourceReport struct {
	ID   string
	Kind string // policy.ContactAdmit | policy.ContactBlock
	// Where is the path or URL this came from. Safe to print: a source's
	// credential travels in a header, never in the URL, precisely so that this
	// string is not a secret.
	Where string

	// Cards is how many vCards were read.
	Cards int
	// Personal and Published are this source's own classification of every
	// number it contributed, before any merge. A block source classifies its
	// numbers too, but nothing turns on it, so `doorman check` shows the total
	// instead.
	Personal  int
	Published int
	// Skipped is TEL values that would not normalise to E.164 — short codes,
	// extensions, junk. Reported rather than swallowed: silence here would be
	// the same defect as dropping an unknown config key.
	Skipped int
	// Dropped is malformed cards, and Malformed is content that was not a
	// property line at all.
	Dropped   int
	Malformed int

	// Unread says why this source contributed nothing, or "" when it was read.
	Unread string
	// Missing narrows Unread to the case an operator can fix: a path that is
	// not there. A url source is unread because this release does not fetch,
	// which is not a mistake anybody made.
	Missing bool
}

// Numbers is how many usable numbers this source contributed, before merging.
func (r SourceReport) Numbers() int { return r.Personal + r.Published }

// Totals is the merged set, counted. Sums here are over distinct numbers, so
// the same mobile in two address books counts once.
type Totals struct {
	Numbers   int
	Personal  int
	Published int
	Blocked   int
	// Skipped is every unparseable number across every source, which is the
	// only figure here that is not distinct-number-based.
	Skipped int

	// The conflicts, resolved conservatively, counted so an operator can see
	// that they happened.

	// Shared is numbers that appeared in more than one source.
	Shared int
	// Renamed is numbers two sources gave different names to. The first source
	// in declaration order won.
	Renamed int
	// Overruled is numbers one source read as personal and another as
	// published. Published won.
	Overruled int
	// Contradicted is numbers present in both an admit source and a block
	// source. Blocked won, and this is the one an operator should resolve
	// rather than leave to a rule.
	Contradicted int
}

// numReasons sizes Reasons. Adding a Reason means widening this, and
// TestReasonsArrayCoversEveryReason fails until it is — an index off the end of
// a fixed array is a panic, and a panic in `doorman check` is not the way to
// discover that a classifier grew a signal.
const numReasons = int(ReasonUnclear) + 1

// Reasons counts why the published side of the set is published, indexed by
// Reason. ReasonPersonal's slot stays zero: a personal number is not published,
// so it has nothing to explain here.
type Reasons [numReasons]int

// Set is the merged contact set.
//
// A nil *Set and an absent one are both usable. Absent — Present() == false —
// is "there is no contacts.toml", which is the state every install is in and
// the one that must produce no output anywhere.
type Set struct {
	present bool
	where   string
	byE164  map[string]Entry
	order   []string
	reports []SourceReport
	totals  Totals
	reasons Reasons
}

// Present reports whether there was a contacts.toml at all.
func (s *Set) Present() bool { return s != nil && s.present }

// Where names the inventory file, for messages.
func (s *Set) Where() string {
	if s == nil {
		return "contacts.toml"
	}
	return s.where
}

// Len is how many distinct numbers the set holds.
func (s *Set) Len() int {
	if s == nil {
		return 0
	}
	return len(s.byE164)
}

// Lookup resolves a normalised E.164 number. This is the whole call-path API,
// and it is a map read: no scan, no prefix match, no fuzzy fallback.
func (s *Set) Lookup(e164 string) (Entry, bool) {
	if s == nil || e164 == "" {
		return Entry{}, false
	}
	e, ok := s.byE164[e164]
	return e, ok
}

// Sources returns one report per declared source, in declaration order.
func (s *Set) Sources() []SourceReport {
	if s == nil {
		return nil
	}
	return s.reports
}

// Totals is the merged set, counted.
func (s *Set) Totals() Totals {
	if s == nil {
		return Totals{}
	}
	return s.totals
}

// Reasons is why the published side is published.
func (s *Set) Reasons() Reasons {
	if s == nil {
		return Reasons{}
	}
	return s.reasons
}

// Numbers lists every number in the set, sorted. For tests and for anything
// that needs a deterministic walk; nothing on the call path uses it.
func (s *Set) Numbers() []string {
	if s == nil {
		return nil
	}
	return s.order
}

// Document is one source's bytes, already fetched or read.
//
// The seam is here rather than inside Compile so that merging is a pure
// function of what the sources said: tests drive it with literals, `Load`
// drives it from disk, and a fetcher can drive it from a cache without any of
// them being able to disagree about what a set means.
type Document struct {
	ID    string
	Kind  string
	Where string
	Data  []byte
	// Err marks a source that could not be obtained. It contributes nothing
	// and does not stop the others.
	Err error
	// Missing narrows Err to an operator mistake — a declared path that is not
	// there — as opposed to a source this release cannot fetch yet.
	Missing bool
}

// errNotFetched is what a url source gets in this release. Reserved rather
// than refused: the key is in the schema so contacts.toml does not churn when
// fetching lands, and saying so per source beats letting one quietly
// contribute nothing.
var errNotFetched = errors.New("url sources are not fetched yet — this release reads path sources only")

// Load reads every source in the inventory and compiles the merged set. An
// absent inventory produces an absent set.
//
// No source can fail the load. A path that is not there is reported against
// that source and the others are unaffected, which is invariant 4's posture
// applied to a second kind of input: the phone must not stop answering because
// somebody moved a file.
func Load(c *policy.Contacts, defaultCountryCode string) *Set {
	if !c.Present() {
		return &Set{}
	}
	// A relative path is relative to contacts.toml, not to whatever directory
	// the command happened to be run from. The exports live beside the
	// inventory that names them, and `doorman check` from a home directory has
	// to read the same files the daemon would.
	base := filepath.Dir(c.Where())

	docs := make([]Document, 0, c.Len())
	for _, src := range c.All() {
		d := Document{ID: src.ID, Kind: src.Kind, Where: src.Path}
		switch {
		case src.Path != "":
			path := src.Path
			if !filepath.IsAbs(path) {
				path = filepath.Join(base, path)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				d.Err, d.Missing = err, true
			}
			d.Data = data
		default:
			d.Where, d.Err = src.URL, errNotFetched
		}
		docs = append(docs, d)
	}
	s := Compile(docs, defaultCountryCode)
	s.where = c.Where()
	return s
}

// Compile merges documents into one set, in declaration order.
//
// The conflict rules, all three of them restrictive:
//
//   - Same number, different names: the first source in declaration order
//     wins. Not the longest name, not the most recent — an ordering an
//     operator can predict by reading the file top to bottom.
//   - One source personal, another published: published.
//   - Present in an admit source and a block source: blocked.
func Compile(docs []Document, defaultCountryCode string) *Set {
	s := &Set{present: true, where: "contacts.toml", byE164: map[string]Entry{}}

	// Merge bookkeeping, kept beside the entries rather than on them: none of
	// it is call-path data, and Entry is what the call path will hold.
	var (
		lastSource = map[string]string{}
		fromAdmit  = map[string]bool{}
		fromBlock  = map[string]bool{}
		shared     = map[string]bool{}
		renamed    = map[string]bool{}
		overruled  = map[string]bool{}
	)

	for _, d := range docs {
		kind := d.Kind
		if kind == "" {
			kind = policy.ContactAdmit
		}
		r := SourceReport{ID: d.ID, Kind: kind, Where: d.Where}
		if d.Err != nil {
			r.Unread, r.Missing = d.Err.Error(), d.Missing
			s.reports = append(s.reports, r)
			continue
		}

		cards, sk := parseCards(d.Data)
		r.Cards, r.Dropped, r.Malformed = len(cards), sk.cards, sk.lines

		for _, c := range cards {
			for _, t := range c.tels {
				e164 := policy.E164OrEmpty(telNumber(t.value), defaultCountryCode)
				if e164 == "" {
					r.Skipped++
					continue
				}
				class, reason := classify(c, t, e164)
				if class == Personal {
					r.Personal++
				} else {
					r.Published++
				}

				if kind == policy.ContactBlock {
					fromBlock[e164] = true
				} else {
					fromAdmit[e164] = true
				}
				if prev, seen := lastSource[e164]; seen && prev != d.ID {
					shared[e164] = true
				}
				lastSource[e164] = d.ID

				existing, seen := s.byE164[e164]
				if !seen {
					s.byE164[e164] = Entry{
						E164:    e164,
						Name:    c.name,
						Class:   class,
						Reason:  reason,
						Blocked: kind == policy.ContactBlock,
						Source:  d.ID,
					}
					continue
				}
				// First name wins; the restrictive class wins; any block wins.
				if c.name != "" && existing.Name != "" && c.name != existing.Name {
					renamed[e164] = true
				}
				// A nameless card cannot claim a number that a later source can
				// actually name, so the first *name* wins rather than the first
				// mention. Source travels with it: it answers "which address
				// book called them that".
				if existing.Name == "" && c.name != "" {
					existing.Name, existing.Source = c.name, d.ID
				}
				if class > existing.Class {
					if existing.Class == Personal {
						overruled[e164] = true
					}
					existing.Class, existing.Reason = class, reason
				}
				if kind == policy.ContactBlock {
					existing.Blocked = true
				}
				s.byE164[e164] = existing
			}
		}
		s.reports = append(s.reports, r)
		s.totals.Skipped += r.Skipped
	}

	s.order = make([]string, 0, len(s.byE164))
	for e164, e := range s.byE164 {
		s.order = append(s.order, e164)
		switch {
		case e.Blocked:
			s.totals.Blocked++
		case e.Class == Personal:
			s.totals.Personal++
		default:
			s.totals.Published++
			s.reasons[e.Reason]++
		}
		if fromAdmit[e164] && fromBlock[e164] {
			s.totals.Contradicted++
		}
	}
	sort.Strings(s.order)
	s.totals.Numbers = len(s.order)
	s.totals.Shared = len(shared)
	s.totals.Renamed = len(renamed)
	s.totals.Overruled = len(overruled)
	return s
}

// String renders a report line for tests and messages. Counts only.
func (r SourceReport) String() string {
	if r.Unread != "" {
		return fmt.Sprintf("%s (%s): unread — %s", r.ID, r.Kind, r.Unread)
	}
	return fmt.Sprintf("%s (%s): %d cards, %d personal, %d published, %d skipped",
		r.ID, r.Kind, r.Cards, r.Personal, r.Published, r.Skipped)
}
