package main

import (
	"fmt"
	"os"
	"strings"

	"callmemaybe/internal/contacts"
	"callmemaybe/internal/policy"
)

// The contacts side of the CLI: which address books this box reads, what each
// contributed, and what the merged set adds up to.
//
// Everything here is silent on an install with no contacts.toml, which is every
// install. That is the compatibility gate for the whole milestone: no file, no
// output, no behaviour, and nobody has to read the word "contacts" to check
// their config.
//
// Counts only, never a name and never a number. Invariant 1 governs logs
// rather than an operator's terminal, but an address book is the most personal
// data this project touches and "how many" answers every question `doorman
// check` exists to answer. `doorman rotate` printing a fresh PIN is the one
// place a secret is ever printed, and this is not that.

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
// operator declared could not be read — a mistake they can fix, and one worth a
// non-zero exit from the command whose whole job is finding them.
//
// A source this release cannot fetch is not that: it is reported and shrugged
// at, because nobody made a mistake writing it.
func printContacts(set *contacts.Set) bool {
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
			fmt.Printf("\n  %-*s  %-6s %6s %9s %10s %8s %8s  %s\n",
				width, "id", "kind", "cards", "personal", "published", "blocked", "skipped", "from")
			header = true
		}
		personal, published, blocked := fmt.Sprint(r.Personal), fmt.Sprint(r.Published), "—"
		if r.Kind == policy.ContactBlock {
			// A block source classifies its numbers like any other, but nothing
			// turns on the answer: blocked beats every tier. Showing the split
			// would invite somebody to read meaning into it.
			personal, published, blocked = "—", "—", fmt.Sprint(r.Numbers())
		}
		fmt.Printf("  %-*s  %-6s %6d %9s %10s %8s %8d  %s\n",
			width, r.ID, r.Kind, r.Cards, personal, published, blocked, r.Skipped, r.Where)
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
	return ok
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
	// "would", not "does": the ladder is not wired into the lobby yet, and a
	// count phrased as a live disposition would be a lie an operator acts on.
	fmt.Printf("    personal    %5d   would be admitted — a stranger could not look these up\n", t.Personal)
	fmt.Printf("    published   %5d   would hear the lobby and dial an extension\n", t.Published)
	fmt.Printf("    blocked     %5d   would be dismissed without hearing the lobby at all\n", t.Blocked)
	fmt.Printf("    skipped     %5d   would not normalise to E.164, counted per source above\n", t.Skipped)

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
	fmt.Println("  Nothing on the call path reads this yet. `[[people]]` in policy.toml is")
	fmt.Println("  still the only list that admits anybody, and it stays the deliberate one:")
	fmt.Println("  contacts are ambient, derived, and disposable. Delete every source and")
	fmt.Println("  the phone works exactly as it does now.")
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
