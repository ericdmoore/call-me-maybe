// Package accountbook is the managed copy of a phonebook that belongs to the
// account a box is enrolled with: what the file under PHONEBOOK_DIR/account
// holds, how it is read and written, and the rules a set of them must keep.
//
// The account is authoritative for these books; the box holds a delivered
// copy, composes it with its own `house` directory when it renders a phone's
// directory, and never edits it. A managed book is directory only. Nothing on
// the admission path reads it: putting someone in a phone's book makes them
// dialable from that phone, never admitted past the lobby.
//
// The package is pure (no files, no clock) and builds for GOOS=wasip1, so a
// hosted validator or preview runs exactly these rules.
package accountbook

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/BurntSushi/toml"
)

// Limits. The box refuses anything beyond them rather than truncating, and a
// hosted validator built from this source refuses the same.
const (
	// MaxBooks is how many managed books one box holds.
	MaxBooks = 200
	// MaxContacts is how many contacts one book lists itself.
	MaxContacts = 2000
	// MaxIncludes is how many books one book includes directly.
	MaxIncludes = 16
	// MaxDepth is the longest chain of include edges from any book: a book
	// that includes `house` is depth 1, one that includes it is depth 2.
	MaxDepth = 4
	// MaxNumbers is how many numbers one contact carries.
	MaxNumbers = 8
	// MaxName bounds a contact's name and a book's label, in UTF-16 units as
	// the sync wire counts them.
	MaxName = 128
	// MaxNumberLabel bounds a number's label ("mobile", "home").
	MaxNumberLabel = 64
	// DefaultDirectoryLimit is the most contacts a handset's account books may
	// compose to. No supported model's directory capacity has been measured,
	// so this is deliberately conservative; a model whose capacity is measured
	// lower lowers it (provision.Model.DirectoryCapacity).
	DefaultDirectoryLimit = 1000
)

// House is the reserved include each box resolves to its own rooms and
// feature codes, so a book used in two houses shows each house's rooms.
const House = "house"

// Prefix marks a managed book's id. contacts.toml may not use it for a source.
const Prefix = "acct-"

// Dir is the directory under PHONEBOOK_DIR that holds managed books.
const Dir = "account"

// Header opens every managed file. It is a comment, so the parser ignores it;
// it is there for the person who opens the file.
const Header = "# Managed by doorman sync for the account this box is enrolled with.\n" +
	"# Do not edit: the next delivery refuses to replace a changed copy and\n" +
	"# reports the change instead. Directory only; never call admission.\n"

var (
	// ulid is a lowercase Crockford base-32 ULID body.
	bookID    = regexp.MustCompile(`^acct-[0-9a-hjkmnp-tv-z]{26}$`)
	contactID = regexp.MustCompile(`^c-[0-9a-hjkmnp-tv-z]{26}$`)
	e164      = regexp.MustCompile(`^\+[1-9][0-9]{1,14}$`)
)

// ValidBookID reports whether id is a managed book id.
func ValidBookID(id string) bool { return bookID.MatchString(id) }

// ValidContactID reports whether id is an account contact id.
func ValidContactID(id string) bool { return contactID.MatchString(id) }

// FileName is the file a book is stored in, inside Dir.
func FileName(id string) string { return id + ".toml" }

// Number is one way to reach a contact.
type Number struct {
	Label  string `toml:"label"`
	Number string `toml:"number"`
}

// Contact is one account contact. Its id is the account's, stable across
// edits to the name or numbers, so a book update replaces it in place.
type Contact struct {
	ID      string   `toml:"id"`
	Name    string   `toml:"name"`
	Numbers []Number `toml:"numbers"`
}

// Book is one managed book: its own contacts, then the books it includes.
type Book struct {
	ID       string    `toml:"id"`
	Revision int64     `toml:"revision"`
	Label    string    `toml:"label"`
	Includes []string  `toml:"includes"`
	Contacts []Contact `toml:"contacts"`
}

// Parse reads a managed file strictly: unknown keys are refused, and so is
// any value outside the limits.
func Parse(data []byte) (Book, error) {
	var b Book
	md, err := toml.Decode(string(data), &b)
	if err != nil {
		return Book{}, fmt.Errorf("not valid TOML")
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, 0, len(undecoded))
		for _, k := range undecoded {
			keys = append(keys, k.String())
		}
		return Book{}, fmt.Errorf("unknown key %s", strings.Join(keys, ", "))
	}
	if problems := b.Problems(); len(problems) > 0 {
		return Book{}, fmt.Errorf("%s", strings.Join(problems, "; "))
	}
	return b, nil
}

// Problems lists what is wrong with one book on its own. Messages name ids
// and fields, never a name or a number.
func (b Book) Problems() []string {
	var problems []string
	fail := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }
	if !ValidBookID(b.ID) {
		fail("book id must be acct- and a 26-character lowercase ULID")
	}
	if b.Revision < 0 {
		fail("revision must not be negative")
	}
	if problem := text(b.Label, MaxName); problem != "" {
		fail("label %s", problem)
	}
	if len(b.Includes) > MaxIncludes {
		fail("includes more than %d books", MaxIncludes)
	}
	included := map[string]bool{}
	for _, inc := range b.Includes {
		switch {
		case inc == b.ID:
			fail("includes itself")
		case inc != House && !ValidBookID(inc):
			// people is the allow-list and a contact source is one box's: an
			// account book may name neither.
			fail("includes %q, which is neither house nor an account book", inc)
		case included[inc]:
			fail("includes %q twice", inc)
		}
		included[inc] = true
	}
	if len(b.Contacts) > MaxContacts {
		fail("lists more than %d contacts", MaxContacts)
	}
	ids := map[string]bool{}
	for i, c := range b.Contacts {
		where := fmt.Sprintf("contact %d", i+1)
		if !ValidContactID(c.ID) {
			fail("%s: id must be c- and a 26-character lowercase ULID", where)
		} else if ids[c.ID] {
			fail("%s: id %s is listed twice", where, c.ID)
		}
		ids[c.ID] = true
		if problem := text(c.Name, MaxName); problem != "" {
			fail("%s: name %s", where, problem)
		}
		if len(c.Numbers) == 0 || len(c.Numbers) > MaxNumbers {
			fail("%s: needs 1 to %d numbers", where, MaxNumbers)
		}
		for j, n := range c.Numbers {
			if !e164.MatchString(n.Number) {
				fail("%s: number %d is not E.164", where, j+1)
			}
			if n.Label != "" {
				if problem := text(n.Label, MaxNumberLabel); problem != "" {
					fail("%s: number %d label %s", where, j+1, problem)
				}
			}
		}
	}
	if len(problems) > maxProblems {
		problems = append(problems[:maxProblems], fmt.Sprintf("and %d more problems", len(problems)-maxProblems))
	}
	return problems
}

// maxProblems bounds one book's list: a file of 2,000 broken contacts is
// reported by its first few problems, not 6,000 lines.
const maxProblems = 25

// text checks a display string: present, printable, and short enough for
// the wire and a handset's screen.
func text(s string, max int) string {
	switch {
	case strings.TrimSpace(s) == "":
		return "is empty"
	case !utf8.ValidString(s):
		return "is not UTF-8"
	case len(utf16.Encode([]rune(s))) > max:
		return fmt.Sprintf("is longer than %d characters", max)
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return "contains a control character"
		}
	}
	return ""
}

// Encode is the one way a managed file is written: the header, then the
// book. Equal books encode to equal bytes, so a repeated delivery is
// recognised as already in place.
func Encode(b Book) []byte {
	var buf bytes.Buffer
	buf.WriteString(Header)
	if b.Includes == nil {
		b.Includes = []string{}
	}
	// The encoder cannot fail on these field types.
	_ = toml.NewEncoder(&buf).Encode(b)
	return buf.Bytes()
}

// Hash is how a managed file's bytes are named on the wire: the snapshot
// echoes it, and phonebook.put's beforeHash quotes it back.
func Hash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Check validates a set of books as one box holds them: every include
// resolves, nothing includes itself through another book, and no chain is
// deeper than MaxDepth. Each book is assumed to have passed Problems.
func Check(books map[string]Book) []string {
	var problems []string
	if len(books) > MaxBooks {
		problems = append(problems, fmt.Sprintf("more than %d account books", MaxBooks))
	}
	ids := make([]string, 0, len(books))
	for id := range books {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	missing := false
	for _, id := range ids {
		for _, inc := range books[id].Includes {
			if _, ok := books[inc]; inc != House && !ok {
				problems = append(problems, fmt.Sprintf("account book %s includes %s, which this box does not hold", id, inc))
				missing = true
			}
		}
	}
	if missing {
		return problems
	}
	const (
		unvisited = iota
		active
		done
	)
	state := map[string]int{}
	depth := map[string]int{}
	var cycle string
	var visit func(id string) int
	visit = func(id string) int {
		switch state[id] {
		case active:
			if cycle == "" {
				cycle = id
			}
			return 0
		case done:
			return depth[id]
		}
		state[id] = active
		deepest := 0
		for _, inc := range books[id].Includes {
			d := 1
			if inc != House {
				d += visit(inc)
			}
			if d > deepest {
				deepest = d
			}
		}
		state[id], depth[id] = done, deepest
		return deepest
	}
	for _, id := range ids {
		visit(id)
		if cycle != "" {
			return append(problems, fmt.Sprintf("account book includes form a cycle through %s", cycle))
		}
	}
	for _, id := range ids {
		if depth[id] > MaxDepth {
			problems = append(problems, fmt.Sprintf("account book %s nests includes %d deep, more than %d", id, depth[id], MaxDepth))
		}
	}
	return problems
}
