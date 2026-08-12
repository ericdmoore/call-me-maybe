package policy

// Contacts: the address-book inventory.
//
// `[[people]]` is hand-typed and therefore always out of date, while every
// household already curates a contact list on their phones, continuously,
// without being asked. contacts.toml names the exports to read so the phone
// can see that list. What the entries then *mean* is `internal/contacts`'
// problem; this file is only the inventory.
//
// It is a file of its own rather than a section of policy.toml because policy
// is per line and contact sources are global — one household's address books,
// not the home line's. The tree already has that shape: handsets.toml defines
// the phones and policy.toml picks which ring, trunks.toml defines the
// providers and `[line] trunk` picks one.
//
// Nothing here is authoritative. A contact set is derived, disposable data:
// delete every source and the phone still works, `[[people]]` is untouched,
// and everyone the classifier would have admitted simply hears the lobby. The
// deliberate list stays the one an operator wrote by hand.
//
// Secrets are named here, never written here. `token_env` holds the name of a
// .env variable, and the token it names travels in an Authorization header
// rather than a query string, so the URL stays safe to log, print, and leave
// wrapped inside a *url.Error nobody remembered to unwrap.

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"
)

// ContactFile is the raw shape of contacts.toml.
type ContactFile struct {
	Sources []ContactSource `toml:"sources"`
}

// ContactSource is one address-book export, as written in contacts.toml.
type ContactSource struct {
	// ID names the source in `doorman check` and is what a later per-line
	// selection (`[line] contacts = ["eric"]`) would reference. It is also the
	// tie-break for a number two sources disagree about: declaration order
	// decides, and an id is what makes the winner nameable.
	ID string `toml:"id"`
	// Path is a vCard export on this box. A file dropped by any other tool
	// works, which is what makes the whole feature testable with no network
	// and no token.
	Path string `toml:"path"`
	// URL is a vCard export fetched over HTTP. Reserved: this release reads
	// path sources only, and `doorman check` says so per source rather than
	// letting one contribute nothing in silence. The key exists now so
	// contacts.toml does not churn when fetching lands.
	URL string `toml:"url"`
	// TokenEnv names the .env variable holding this source's bearer token.
	// The secret itself never appears in this file. Reserved with URL.
	TokenEnv string `toml:"token_env"`
	// Kind is "admit" (the default) or "block". A block source is the spam
	// list: those callers do not hear the lobby at all, which is the whole
	// difference between "not admitted" and "blocked".
	Kind string `toml:"kind"`
}

// The two kinds a source may be. Written out rather than inferred from a
// filename because "which of my address books is the block list" is a decision
// an operator should have to make in writing.
const (
	ContactAdmit = "admit"
	ContactBlock = "block"
)

// Contacts is a validated contacts.toml: every address book to read, in
// declaration order, with defaults filled in.
//
// Nil and absent are both usable and mean different things, exactly as they do
// for Trunks. Nil is "the caller did not consult the inventory"; absent
// (Present() == false) is "there is no contacts.toml", which is the state every
// install is in today and the one that must stay completely silent.
type Contacts struct {
	// Path is the file these came from, for messages. Empty when they were
	// compiled from bytes rather than read from disk.
	Path string

	present bool
	list    []ContactSource
}

// Present reports whether there is a contacts.toml at all.
func (c *Contacts) Present() bool { return c != nil && c.present }

// Where names the file for a message, falling back to the conventional name
// when the inventory did not come from disk.
func (c *Contacts) Where() string {
	if c == nil || c.Path == "" {
		return "contacts.toml"
	}
	return c.Path
}

// Len is how many sources were declared.
func (c *Contacts) Len() int {
	if c == nil {
		return 0
	}
	return len(c.list)
}

// All returns every source in file order.
//
// File order is not cosmetic here the way it is for trunks: it is the *only*
// tie-break when two address books disagree about who a number belongs to. No
// scoring and no recency, so "which name wins" is answerable by reading the
// config top to bottom.
func (c *Contacts) All() []ContactSource {
	if c == nil {
		return nil
	}
	return c.list
}

// IDs lists every declared source id, sorted, for messages and completions.
func (c *Contacts) IDs() []string {
	if c == nil {
		return nil
	}
	out := make([]string, 0, len(c.list))
	for _, s := range c.list {
		out = append(out, s.ID)
	}
	sort.Strings(out)
	return out
}

// LoadContacts reads and compiles contacts.toml.
//
// A missing file is not an error: it is the state every install is in, and it
// compiles to an absent inventory that reads nothing and prints nothing. Any
// other read error is returned — a contacts.toml that exists and cannot be read
// is a different problem, and treating it as absent would silently drop every
// address book the operator listed.
func LoadContacts(path string) (*Contacts, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &Contacts{}, nil
	}
	if err != nil {
		return nil, err
	}
	c, cerr := ContactsFromTOML(data)
	if cerr != nil {
		return nil, cerr
	}
	c.Path = path
	return c, nil
}

// ContactsFromTOML compiles contacts.toml content. The returned Contacts has an
// empty Path; LoadContacts fills it in, which is what makes Present() mean
// "read from a file" rather than "non-empty".
func ContactsFromTOML(data []byte) (*Contacts, error) {
	f, unknown, err := decodeContacts(data)
	if err != nil {
		return nil, err
	}
	// Unknown keys are refused outright, as they are for trunks.toml and
	// handsets.toml. The reason is sharper here than there: the call path never
	// reads this file, but it reads the set compiled from it, so a dropped
	// `kind = "block"` turns a block list into an address book that admits —
	// with nothing at runtime to suggest why the spam is ringing the house.
	msgs := make([]string, 0, len(unknown))
	for _, u := range unknown {
		msgs = append(msgs, u.File+": "+u.String())
	}
	c, problems := compileContacts(f)
	msgs = append(msgs, problems...)
	if len(msgs) > 0 {
		return nil, errors.New(strings.Join(msgs, "\n"))
	}
	return c, nil
}

// LintContacts returns every problem in contacts.toml, one string per issue;
// empty means valid. Same shape as LintTrunks, for the same consumer.
func LintContacts(data []byte) []string {
	f, unknown, err := decodeContacts(data)
	if err != nil {
		return []string{err.Error()}
	}
	var problems []string
	for _, u := range unknown {
		problems = append(problems, u.String())
	}
	_, semantic := compileContacts(f)
	return append(problems, semantic...)
}

func compileContacts(f ContactFile) (*Contacts, []string) {
	var problems []string
	fail := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}

	c := &Contacts{present: true}
	if len(f.Sources) == 0 {
		fail("at least one [[sources]] entry is required — " +
			"delete contacts.toml entirely to go back to [[people]] as the only list")
	}

	seen := map[string]bool{}
	for _, s := range f.Sources {
		if !handsetIDPattern.MatchString(s.ID) {
			fail("source id %q must be lowercase alphanumeric/dash/underscore", s.ID)
			continue
		}
		if seen[s.ID] {
			fail("duplicate source id %q", s.ID)
			continue
		}
		seen[s.ID] = true

		where := fmt.Sprintf("source %q", s.ID)
		switch {
		case s.Path == "" && s.URL == "":
			fail("%s needs a path (a vCard export on this box) or a url", where)
		case s.Path != "" && s.URL != "":
			fail("%s sets both path and url — a source is one address book, from one place", where)
		case s.URL != "":
			if u, err := url.Parse(s.URL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				fail("%s url must be an http or https URL", where)
			}
		}

		switch {
		case s.TokenEnv == "":
		case s.URL == "":
			fail("%s sets token_env but has no url — a local file needs no credential", where)
		case !envVarPattern.MatchString(s.TokenEnv):
			// The one rule worth being strict about in a file that must never
			// hold a secret: a token pasted here fails this pattern, so the
			// mistake is loud instead of committed.
			fail("%s token_env %q must be the NAME of a .env variable such as CONTACTS_%s_TOKEN "+
				"— never the token itself",
				where, s.TokenEnv, strings.ToUpper(strings.NewReplacer("-", "_").Replace(s.ID)))
		}

		switch s.Kind {
		case "":
			s.Kind = ContactAdmit
		case ContactAdmit, ContactBlock:
		default:
			fail("%s kind %q must be %q or %q", where, s.Kind, ContactAdmit, ContactBlock)
		}

		c.list = append(c.list, s)
	}

	if len(problems) > 0 {
		return nil, problems
	}
	return c, nil
}
