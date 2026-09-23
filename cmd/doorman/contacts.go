package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"callmemaybe/internal/contacts"
	"callmemaybe/internal/events"
	"callmemaybe/internal/lobby"
	"callmemaybe/internal/policy"
)

// The contacts side of the daemon and the CLI: which address books this box
// reads, what each contributed, what the merged set adds up to, and the one
// translation between that set and the state machine.
//
// Everything here is silent on an install with no contacts.toml, which is every
// install. That is the compatibility gate for the whole feature: no file, no
// output, no lookup, no behaviour, and nobody has to read the word "contacts"
// to check their config.
//
// Counts only, never a name and never a number — with one exception, the
// contradiction report, which prints a name an operator typed themselves and a
// redacted number, because "resolve this" is not actionable without saying
// which caller. Invariant 1 governs logs rather than an operator's terminal,
// but an address book is the most personal data this project touches and "how
// many" answers every other question `doorman check` exists to answer.

// contactBook adapts the merged set to lobby.Contacts.
//
// internal/lobby deliberately does not import internal/contacts — its Contact
// is a mirror, exactly as its OriginateParams mirrors the ARI client's — so
// this is the one translation point, and it is three fields wide. Everything
// else the set knows (which source named this caller, which signal classified
// them, how many cards were dropped) is an operator's question and stops here.
// contactBook is the merged set the lobby consults, swapped whole by the
// refresher: a call reads one consistent set, never a set mid-update, and
// the swap is the only synchronisation there is.
type contactBook struct{ set atomic.Pointer[contacts.Set] }

var _ lobby.Contacts = (*contactBook)(nil)

func newContactBook(set *contacts.Set) *contactBook {
	b := &contactBook{}
	b.set.Store(set)
	return b
}

func (b *contactBook) current() *contacts.Set { return b.set.Load() }

func (b *contactBook) Lookup(e164 string) (lobby.Contact, bool) {
	e, ok := b.current().Lookup(e164)
	if !ok {
		return lobby.Contact{}, false
	}
	return lobby.Contact{
		Name:      e.Name,
		Published: e.Class == contacts.Published,
		Blocked:   e.Blocked,
		Source:    e.Source,
	}, true
}

// openContacts compiles the address book the daemon answers calls with, or nil
// when there is no contacts.toml — which is every install today, and the state
// in which nothing here logs a single line.
//
// A nil *return*, never a nil *set* in a non-nil interface: a typed nil would
// satisfy lobby.Contacts and turn the state machine's one nil check into a
// lookup that always misses, which is the same behaviour by accident instead of
// by design.
//
// An inventory that will not load is a warning and never fatal, the same
// posture trunks.toml gets and for the stronger reason: this file cannot be
// allowed a say in whether the phone answers. The cost is honest and worth
// stating — a block list that fails to load stops blocking, so it is logged
// where an operator will see it rather than absorbed.
func openContacts(path, defaultCountryCode string, lists []allowList, log *slog.Logger, journal *events.Writer) *contactBook {

	sources, err := policy.LoadContacts(path)
	if err != nil {
		journal.Post(events.System(events.ContactsRefreshFailed, "inventory-unreadable", 0))

		log.Warn("address-book inventory will not load — the phone is unaffected and [[people]] is untouched, "+
			"but nobody in a contacts source is admitted or blocked until it does",
			"path", path, "err", err)
		return nil
	}
	// From disk and from the cache, with no network: the set the daemon
	// starts on is whatever the last run left, which is the last good one.
	// The refresher fetches in the background from the first second.
	set := contacts.Load(sources, defaultCountryCode)
	if !set.Present() {
		return nil
	}
	reportContacts(set, lists, "startup-snapshot", nil, log, journal)
	return newContactBook(set)
}

// reportContacts says what a set adds up to — counts, never names or numbers
// — and what each source that contributed nothing is missing. One line for
// the whole set, because the point of an ambient list is that nobody typed
// it; one line per silent source, because that failure mode is silent by
// nature: an address book that vanished looks exactly like one nobody is in.
func reportContacts(set *contacts.Set, lists []allowList, why string, outcomes []contacts.Outcome, log *slog.Logger, journal *events.Writer) {
	for _, r := range set.Sources() {
		switch {
		case r.Unread != "":
			journal.Post(events.System(events.ContactsRefreshFailed, "source-unreadable", 1))
			log.Warn("address book contributed nothing", "source", r.ID, "kind", r.Kind,
				"from", r.Where, "why", r.Unread)
		case r.Warning != "":
			journal.Post(events.System(events.ContactsRefreshFailed, "source-stale", 1))
			log.Warn("address book served from its cache", "source", r.ID, "kind", r.Kind,
				"from", r.Where, "why", r.Warning)
		}
	}
	for _, o := range outcomes {
		if o.Status == "fresh" {
			log.Info("address book fetched", "source", o.ID)
		}
	}

	t := set.Totals()
	journal.Post(events.System(events.ContactsRefreshed, why, int64(t.Numbers)))
	log.Info("contacts loaded", "path", set.Where(), "sources", len(set.Sources()),
		"numbers", t.Numbers, "personal", t.Personal, "published", t.Published,
		"blocked", t.Blocked, "skipped", t.Skipped)

	for _, c := range contactContradictions(set, lists) {
		// Loud here as well as in `doorman check`, because the effect is
		// invisible from the allow-list: the operator wrote this caller down on
		// purpose and the block list is quietly overruling them.
		log.Warn("a number is both allow-listed and blocked — the block wins, so this caller is dismissed",
			"name", c.Name, "number", policy.Redact(c.Number), "line", c.Where,
			"hint", "remove them from the block source, or delete the [[people]] entry")
	}
}

// contactsRefresher fetches url sources at startup and every refresh, off
// the call path, and swaps the merged set into the book. It re-reads
// contacts.toml each cycle so a source can be added without a restart; an
// inventory that stops loading keeps the last good set, the same rule
// policy.toml gets.
type contactsRefresher struct {
	path, countryCode string
	secret            func(string) (string, bool)
	book              *contactBook
	lists             []allowList
	log               *slog.Logger
	journal           *events.Writer
	// every overrides the inventory's refresh when non-zero (tests).
	every time.Duration
	// client overrides the fetcher's HTTP client (tests).
	client *http.Client
}

func (r *contactsRefresher) run(ctx context.Context) {
	for {
		wait := r.once(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// once is one cycle: fetch, merge, swap. It returns how long to wait.
func (r *contactsRefresher) once(ctx context.Context) time.Duration {
	sources, err := policy.LoadContacts(r.path)
	if err != nil {
		r.journal.Post(events.System(events.ContactsRefreshFailed, "inventory-unreadable", 0))
		r.log.Warn("address-book inventory will not load — keeping the last good set", "path", r.path, "err", err)
		return r.interval(nil)
	}
	if !sources.Present() {
		return r.interval(sources)
	}
	if !sources.HasURLSources() {
		// Nothing to fetch; path sources are re-read so a replaced export
		// is picked up on the same schedule.
		set := contacts.Load(sources, r.countryCode)
		r.book.set.Store(set)
		return r.interval(sources)
	}
	set, outcomes := contacts.Fetch(ctx, sources, r.countryCode, &contacts.Fetcher{Secret: r.secret, Client: r.client})
	r.book.set.Store(set)
	reportContacts(set, r.lists, "refresh", outcomes, r.log, r.journal)
	return r.interval(sources)
}

func (r *contactsRefresher) interval(sources *policy.Contacts) time.Duration {
	if r.every > 0 {
		return r.every
	}
	return sources.Refresh()
}

// allowList is one line's hand-typed list, with something to call it. `doorman
// check` names the policy file, the daemon names the line — the two audiences
// want different words for the same thing, and neither wants the other's.
type allowList struct {
	where string
	pol   *policy.Policy
}

// contactContradiction is a caller who is both allow-listed and blocked.
type contactContradiction struct {
	Where  string
	Name   string // the [[people]] name: the operator's own word for this caller
	Number string // full E.164, redacted by everything that prints it
}

// contactContradictions finds numbers a block source names that a policy file
// also allow-lists.
//
// Block beats [[people]] and this is why that ranking is not enough on its own:
// the two files disagree about the same caller, one of them was hand-typed on
// purpose, and no ordering makes that not a mistake. Reported so an operator
// resolves it, rather than discovering it the day somebody they trust cannot
// get through.
func contactContradictions(set *contacts.Set, lists []allowList) []contactContradiction {
	if !set.Present() || len(lists) == 0 {
		return nil
	}
	var out []contactContradiction
	// The set's own order — sorted E.164 — so the report is stable across runs.
	for _, number := range set.Numbers() {
		e, ok := set.Lookup(number)
		if !ok || !e.Blocked {
			continue
		}
		for _, l := range lists {
			if known, listed := l.pol.LookupCaller(number); listed {
				out = append(out, contactContradiction{Where: l.where, Name: known.Name, Number: number})
			}
		}
	}
	return out
}

func contactsPathArg(explicit string) string {
	if explicit != "" {
		return explicit
	}
	if p := os.Getenv("CONTACTS_PATH"); p != "" {
		return p
	}
	return "./contacts.toml"
}

// printContacts reports the contact set. It returns false when a source an
// operator declared could not be read, or when a number is both allow-listed
// and blocked — mistakes they can fix, and worth a non-zero exit from the
// command whose whole job is finding them.
//
// A source this release cannot fetch is not that: it is reported and shrugged
// at, because nobody made a mistake writing it.
func printContacts(set *contacts.Set, lists []allowList) bool {
	if !set.Present() {
		return true
	}

	reports := set.Sources()
	fmt.Printf("\nContacts: %d %s   (%s)\n", len(reports), plural(len(reports), "source"), set.Where())

	width := len("id")
	for _, r := range reports {
		if len(r.ID) > width {
			width = len(r.ID)
		}
	}

	ok, header := true, false
	var unread []contacts.SourceReport
	for _, r := range reports {
		if r.Unread != "" {
			unread = append(unread, r)
			if r.Missing {
				ok = false
			}
			continue
		}
		if !header {
			fmt.Printf("\n  %-*s  %-6s %6s %9s %10s %8s %8s  %-9s %s\n",
				width, "id", "kind", "cards", "personal", "published", "blocked", "skipped", "fetched", "from")
			header = true
		}
		personal, published, blocked := fmt.Sprint(r.Personal), fmt.Sprint(r.Published), "—"
		if r.Kind == policy.ContactBlock {
			// A block source classifies its numbers like any other, but nothing
			// turns on the answer: blocked beats every tier. Showing the split
			// would invite somebody to read meaning into it.
			personal, published, blocked = "—", "—", fmt.Sprint(r.Numbers())
		}
		fmt.Printf("  %-*s  %-6s %6d %9s %10s %8s %8d  %-9s %s\n",
			width, r.ID, r.Kind, r.Cards, personal, published, blocked, r.Skipped, contacts.Age(time.Now(), r.FetchedAt), r.Where)
	}
	for _, r := range reports {
		if r.Warning != "" {
			// Still contributing, and the operator should know how old.
			fmt.Printf("\n  ⚠ %s — %s\n      %s\n", r.ID, r.Where, r.Warning)
		}
	}

	for _, r := range unread {
		mark, note := "·", "Nothing from this source yet."
		if r.Missing {
			mark, note = "✗", "This source contributes nothing."
		}
		fmt.Printf("\n  %s %s — %s\n", mark, r.ID, r.Where)
		fmt.Printf("      %s\n", r.Unread)
		fmt.Printf("      %s The others are unaffected and the phone keeps answering:\n", note)
		fmt.Println("      a contact set is derived data, never the authority.")
	}

	printParseNotes(reports)
	printContactTotals(set)
	return printContactContradictions(contactContradictions(set, lists)) && ok
}

// printContactContradictions reports callers who are both allow-listed and
// blocked, and fails the check.
//
// The one place this command prints a caller: the name is the operator's own,
// typed into their own policy file, and the number is redacted to the shape
// every log line uses. Counts would not do here — "1 contradiction" is not
// something anybody can act on, and the whole reason this is an error rather
// than a ranking is that a human has to decide which file was wrong.
func printContactContradictions(found []contactContradiction) bool {
	if len(found) == 0 {
		return true
	}
	headline := "a number is both allow-listed and blocked"
	if len(found) > 1 {
		headline = fmt.Sprintf("%d numbers are both allow-listed and blocked", len(found))
	}
	fmt.Printf("\n✗ %s\n\n", headline)
	for _, c := range found {
		fmt.Printf("    %s (%s) — [[people]] in %s, and a block source\n",
			c.Name, policy.Redact(c.Number), c.Where)
	}
	fmt.Println("\n  Block beats [[people]], so this caller is dismissed without hearing the")
	fmt.Println("  lobby at all — the allow-list entry does nothing. That is a contradiction")
	fmt.Println("  to resolve rather than a preference to rank: somebody typed the entry on")
	fmt.Println("  purpose, and somebody's address book says otherwise.")
	fmt.Println("\n  Remove them from the block source, or delete the [[people]] entry.")
	return false
}

// printParseNotes reports what the parser did not understand. A vCard subset
// that silently dropped a third of a file would look exactly like a vCard
// subset that understood all of it.
func printParseNotes(reports []contacts.SourceReport) {
	var notes []string
	for _, r := range reports {
		switch {
		case r.Dropped > 0 && r.Malformed > 0:
			notes = append(notes, fmt.Sprintf("  %s: %d %s dropped, %d %s that were not properties",
				r.ID, r.Dropped, plural(r.Dropped, "card"), r.Malformed, plural(r.Malformed, "line")))
		case r.Dropped > 0:
			notes = append(notes, fmt.Sprintf("  %s: %d %s dropped — malformed, or unterminated at end of file",
				r.ID, r.Dropped, plural(r.Dropped, "card")))
		case r.Malformed > 0:
			notes = append(notes, fmt.Sprintf("  %s: %d %s that were not property lines",
				r.ID, r.Malformed, plural(r.Malformed, "line")))
		}
	}
	if len(notes) == 0 {
		return
	}
	fmt.Println("\n  Not understood, and therefore skipped:")
	for _, n := range notes {
		fmt.Println(n)
	}
	fmt.Println("\n    doorman parses the vCard subset real exporters emit — iCloud, Google")
	fmt.Println("    Contacts, CardDAV, and 2.1 files with quoted-printable — and counts")
	fmt.Println("    the rest rather than guessing at it.")
}

func printContactTotals(set *contacts.Set) {
	t := set.Totals()
	fmt.Printf("\n  Merged: %d distinct %s, keyed by E.164\n\n", t.Numbers, plural(t.Numbers, "number"))
	// The present tense, and it has to stay honest: these are dispositions the
	// lobby applies on the next call, not a projection. The conditional wording
	// this replaced was true for exactly one release.
	fmt.Printf("    personal    %5d   admitted — a stranger could not look these up\n", t.Personal)
	fmt.Printf("    published   %5d   hear the lobby and dial an extension\n", t.Published)
	fmt.Printf("    blocked     %5d   dismissed without hearing the lobby at all\n", t.Blocked)
	fmt.Printf("    skipped     %5d   did not normalise to E.164, counted per source above\n", t.Skipped)

	if t.Published > 0 {
		reasons := set.Reasons()
		fmt.Println("\n  Why the published ones are published:")
		for _, r := range []contacts.Reason{
			contacts.ReasonOrg, contacts.ReasonWork, contacts.ReasonTollFree, contacts.ReasonUnclear,
		} {
			if n := reasons[r]; n > 0 {
				fmt.Printf("    %-42s %5d\n", r, n)
			}
		}
	}

	printContactConflicts(t)

	fmt.Println("\n  The rule this turns on: if a stranger can look the number up, it must")
	fmt.Println("  not be automatic admission. Spoofing caller ID requires knowing what to")
	fmt.Println("  spoof, and a plumber's number is on their website while your sister's")
	fmt.Println("  mobile is nowhere. Anything ambiguous is published, because the failures")
	fmt.Println("  are not symmetric: wrong-closed means a ten-second greeting, wrong-open")
	fmt.Println("  means a findable number ringing every phone in the house at 3am.")
	fmt.Println()
	fmt.Println("  The ladder the lobby walks, first match wins: a block source, then")
	fmt.Println("  `[[people]]`, then a personal contact, then everybody else at the lobby.")
	fmt.Println("  `[[people]]` stays the deliberate list and beats the classifier outright —")
	fmt.Println("  write somebody down and they go straight through however their card")
	fmt.Println("  reads. Contacts are the ambient list: derived, disposable, and never the")
	fmt.Println("  authority. Delete every source and the phone works exactly as it did,")
	fmt.Println("  with everyone here hearing the lobby again.")
}

func printContactConflicts(t contacts.Totals) {
	if t.Shared == 0 && t.Renamed == 0 && t.Overruled == 0 && t.Contradicted == 0 {
		return
	}
	fmt.Println("\n  Conflicts, resolved conservatively:")
	if t.Shared > 0 {
		fmt.Printf("    %5d %s in more than one source\n", t.Shared, plural(t.Shared, "number"))
	}
	if t.Renamed > 0 {
		fmt.Printf("    %5d named differently by two sources — the first source in\n", t.Renamed)
		fmt.Println("          declaration order won, which is the only ordering there is")
	}
	if t.Overruled > 0 {
		fmt.Printf("    %5d personal in one source and published in another — published won\n", t.Overruled)
	}
	if t.Contradicted > 0 {
		fmt.Printf("    %5d in both an admit source and a block source — blocked won.\n", t.Contradicted)
		fmt.Println("          Worth resolving rather than leaving to a rule: a number you both")
		fmt.Println("          trust and block is a contradiction, not a preference.")
	}
}

func plural(n int, word string) string {
	if n == 1 {
		return word
	}
	if strings.HasSuffix(word, "s") {
		return word + "es"
	}
	return word + "s"
}
