// Package configcheck is the one answer to "is this configuration valid?".
//
// `doorman check`, the daemon's policy reload, the sync observer and the
// hosted validator built from this source all take their rules from here, so
// they accept and refuse the same files (call-me-maybe#131). Before, check and
// the daemon accepted a disabled schedule starting at 25:99 that sync refused
// without saying why.
//
// The rules themselves live in internal/policy; this package exists because
// the model registry they need is internal/provision, which imports policy.
package configcheck

import (
	"fmt"
	"sort"
	"strings"

	"callmemaybe/internal/accountbook"
	"callmemaybe/internal/policy"
	"callmemaybe/internal/provision"
)

// Options is the shared rule set: unknown keys are errors, a handset model
// must be one doorman knows, and [line] trunk is checked against the trunk
// inventory. trunks nil means there is no trunks.toml, which is itself
// something a line naming a trunk is told about.
func Options(trunks *policy.Trunks) policy.Options {
	if trunks == nil {
		trunks = &policy.Trunks{}
	}
	return policy.Options{StrictUnknownKeys: true, ModelKnown: provision.Known, Trunks: trunks}
}

// Problems validates one line's policy file with the shared inventory and
// returns every problem, or none when the files are valid. handsetsData nil
// is the legacy single-file layout; trunksData nil means no trunks.toml. The
// messages may quote values from the files; pass them through policy.Redact-
// Problem before they reach a log or leave the box.
func Problems(policyData, handsetsData, trunksData []byte) []string {
	var trunks *policy.Trunks
	if trunksData != nil {
		t, err := policy.TrunksFromTOML(trunksData)
		if err != nil {
			return []string{"trunks: " + err.Error()}
		}
		trunks = t
	}
	return policy.LintSplitWith(policyData, handsetsData, Options(trunks))
}

// Problem is one reason a set of account books is refused. File is the
// managed file it is about, by its name inside PHONEBOOK_DIR/account, or ""
// when the problem is in handsets.toml.
type Problem struct {
	File    string
	Message string
}

// AccountBooks validates the account-managed phonebooks a box holds — files
// by name inside PHONEBOOK_DIR/account — together with the inventory whose
// handsets use them. It returns the books when there is no problem.
//
// The rules: each file parses strictly and is named for its book; the set
// keeps accountbook's limits, includes resolve without a cycle or a chain
// deeper than accountbook.MaxDepth; a handset names only books the box
// holds; and what a handset's account books compose to, with the house they
// include, fits that handset's directory (provision.DirectoryLimit). The
// sync observer, the sync executor before it writes, and the directory
// before it serves all ask this, and so can a hosted validator built from
// this source.
func AccountBooks(files map[string][]byte, handsets []policy.Handset) (map[string]accountbook.Book, []Problem) {
	var problems []Problem
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	books := map[string]accountbook.Book{}
	for _, name := range names {
		id, ok := strings.CutSuffix(name, ".toml")
		if !ok || !accountbook.ValidBookID(id) {
			problems = append(problems, Problem{name, "not an account book file name: acct-<id>.toml"})
			continue
		}
		b, err := accountbook.Parse(files[name])
		if err != nil {
			problems = append(problems, Problem{name, err.Error()})
			continue
		}
		if b.ID != id {
			problems = append(problems, Problem{name, "the id inside does not match the file name"})
			continue
		}
		books[id] = b
	}
	if len(problems) > 0 {
		return nil, problems
	}
	for _, p := range accountbook.Check(books) {
		problems = append(problems, Problem{"", p})
	}
	if len(problems) > 0 {
		// A set problem spans files; name the first file it mentions.
		for i := range problems {
			for _, name := range names {
				if strings.Contains(problems[i].Message, strings.TrimSuffix(name, ".toml")) {
					problems[i].File = name
					break
				}
			}
		}
		return nil, problems
	}
	directory := provision.Books{"house": provision.HouseBook(handsets)}
	for id, b := range books {
		directory[id] = provision.AccountBook(b)
	}
	for _, h := range handsets {
		var mine []string
		for _, id := range h.Phonebook {
			if !strings.HasPrefix(id, accountbook.Prefix) {
				continue
			}
			if _, ok := books[id]; !ok {
				problems = append(problems, Problem{"", fmt.Sprintf("handset %q uses account book %s, which this box does not hold", h.ID, id)})
				continue
			}
			mine = append(mine, id)
		}
		if len(mine) == 0 {
			continue
		}
		// Every include resolves (checked above), so composing cannot fail.
		groups, _ := provision.Select(policy.Handset{ID: h.ID, Phonebook: mine}, directory)
		count := 0
		for _, g := range groups {
			count += len(g.Contacts)
		}
		if limit := provision.DirectoryLimit(h.Model); count > limit {
			problems = append(problems, Problem{"", fmt.Sprintf("handset %q: its account books compose to %d contacts, more than the %d its directory takes", h.ID, count, limit)})
		}
	}
	if len(problems) > 0 {
		return nil, problems
	}
	return books, nil
}
