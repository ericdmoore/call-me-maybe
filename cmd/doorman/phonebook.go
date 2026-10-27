package main

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"callmemaybe/internal/accountbook"
	"callmemaybe/internal/configcheck"
	"callmemaybe/internal/contacts"
	"callmemaybe/internal/ownbook"
	"callmemaybe/internal/policy"
	"callmemaybe/internal/provision"
)

// The phone book, from the CLI's side: which files it is read from and how
// it is kept current. Three inputs — handsets.toml for the house, policy.toml
// for the people, contacts.toml for any source a phone opted into — and one
// output per phone. Read by `render` (a file beside the configuration) and
// by `provision directory` (on demand, re-read when an input changes).

// bookPaths names the inputs.
type bookPaths struct {
	handsets, policy, contacts string
	countryCode                string
	// own is the directory of per-handset additions (*88); empty means none.
	own string
}

// ownBookID is the book key for a handset's own additions.
func ownBookID(handset string) string { return "own:" + handset }

// loadBooks reads every book the house can render.
func loadBooks(paths bookPaths) (provision.Books, error) {
	handsets, _, err := policy.LoadHandsets(paths.handsets)
	if err != nil {
		return nil, err
	}
	pol, err := policy.LoadSplit(paths.policy, paths.handsets)
	if err != nil {
		return nil, err
	}
	books := provision.Books{
		"house":  provision.HouseBook(handsets),
		"people": provision.PeopleBook(pol.Callers()),
	}
	sources, err := policy.LoadContacts(paths.contacts)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", paths.contacts, err)
	}
	set := contacts.Load(sources, paths.countryCode)
	if set.Present() {
		bySource := map[string][]policy.KnownCaller{}
		for _, e164 := range set.Numbers() {
			e, ok := set.Lookup(e164)
			if !ok || e.Blocked || e.Name == "" {
				continue
			}
			bySource[e.Source] = append(bySource[e.Source], policy.KnownCaller{Name: e.Name, E164: e.E164})
		}
		for _, rep := range set.Sources() {
			// A block source is a list of who never gets in; it is not a
			// directory anyone dials from.
			if rep.Kind != policy.ContactAdmit {
				continue
			}
			books[rep.ID] = provision.SourceBook(rep.ID, rep.ID, bySource[rep.ID])
		}
	}
	// A phone's own additions (*88): keyed so Select can never be pointed
	// at another phone's, and served only to the phone that filed them.
	if paths.own != "" {
		ids, err := ownbook.Handsets(paths.own)
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			b, err := ownbook.Load(paths.own, id)
			if err != nil {
				return nil, err
			}
			entries := make([]provision.OwnEntry, 0, len(b.Entries))
			for _, e := range b.Entries {
				entries = append(entries, provision.OwnEntry{Name: e.Name, E164: e.E164})
			}
			books[ownBookID(id)] = provision.OwnBook(entries)
		}
		shared := filepath.Join(filepath.Dir(paths.own), "shared")
		targets := []string{"house"}
		for _, h := range handsets {
			targets = append(targets, "handset:"+h.ID)
		}
		for _, target := range targets {
			dir, id, _ := ownbook.SharedLocation(shared, target)
			b, err := ownbook.Load(dir, id)
			if err != nil {
				return nil, err
			}
			if len(b.Entries) == 0 {
				continue
			}
			key := "house"
			if target != "house" {
				key = ownBookID(id)
			}
			book, ok := books[key]
			if !ok {
				book = provision.OwnBook(nil)
			}
			have := map[string]bool{}
			for _, c := range book.Contacts {
				for _, n := range c.Numbers {
					have[n.Dial] = true
				}
			}
			for _, e := range b.Entries {
				dial := provision.DialString(e.E164)
				if !have[dial] {
					book.Contacts = append(book.Contacts, provision.Contact{Name: e.Name, Numbers: []provision.Number{{Kind: "cell", Dial: dial}}})
					have[dial] = true
				}
			}
			books[key] = book
		}
		// Books the account manages: composed with house when a phone's
		// directory is rendered, never read for admission — all but the
		// lobby pass book, which is the reverse. Refused as a set
		// by the same rules sync applies, so the last good directory stays
		// up through a bad delivery or a hand edit.
		dir := accountDir(paths.own)
		names, err := filepath.Glob(filepath.Join(dir, "*.toml"))
		if err != nil {
			return nil, err
		}
		files := map[string][]byte{}
		for _, path := range names {
			data, err := os.ReadFile(path)
			if err != nil {
				return nil, err
			}
			files[filepath.Base(path)] = data
		}
		held, problems := configcheck.AccountBooks(files, handsets)
		if len(problems) > 0 {
			return nil, fmt.Errorf("%s: %s", filepath.Join(dir, problems[0].File), policy.RedactProblem(problems[0].Message, policy.MaxProblemLength))
		}
		for id, b := range held {
			if b.LobbyPass() {
				// Never a directory, whatever selects it: the rules above
				// already refuse a selection, and leaving it out here means
				// no composition can reach it either.
				continue
			}
			books[id] = provision.AccountBook(b)
		}
	}
	return books, nil
}

// accountDir is where account-managed books live, beside own/ and shared/.
func accountDir(own string) string {
	return filepath.Join(filepath.Dir(own), accountbook.Dir)
}

// renderBooks is one phone's directory, in its own format.
func renderBooks(h policy.Handset, m provision.Model, books provision.Books) ([]byte, error) {
	chosen, err := provision.Select(h, books)
	if err != nil {
		return nil, err
	}
	// What this phone added itself needs no opting into.
	if own, ok := books[ownBookID(h.ID)]; ok && len(own.Contacts) > 0 {
		chosen = append(chosen, own)
	}
	return provision.RenderPhonebook(m, chosen)
}

// bookCache re-reads the inputs when any of them changes on disk, so the
// directory hands out today's allow-list without anyone running render.
type bookCache struct {
	paths    bookPaths
	mu       sync.Mutex
	stamp    string
	books    provision.Books
	handsets map[string]policy.Handset
	at       time.Time
}

func (c *bookCache) current() (provision.Books, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.refresh()
}

// directory renders a selection and its books from the same accepted revision.
// Keeping the inventory captured at server startup would silently ignore new
// assignments; reading it independently would expose half of an invalid edit.
func (c *bookCache) directory(id string, model provision.Model) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	books, err := c.refresh()
	if err != nil {
		return nil, err
	}
	h, ok := c.handsets[id]
	if !ok {
		return nil, fmt.Errorf("phonebook: unknown handset")
	}
	return renderBooks(h, model, books)
}

func (c *bookCache) fingerprint() string {
	watched := []string{c.paths.handsets, c.paths.policy, c.paths.contacts}
	if sources, err := policy.LoadContacts(c.paths.contacts); err == nil {
		for _, source := range sources.All() {
			path := source.Path
			if path == "" {
				path = filepath.Join(sources.CacheDir(), source.ID+".vcf")
			} else if !filepath.IsAbs(path) {
				path = filepath.Join(filepath.Dir(c.paths.contacts), path)
			}
			watched = append(watched, path)
		}
	}
	if c.paths.own != "" {
		// The own books change on their own schedule — a minute after
		// somebody dials *88 — so they are watched like the three files.
		shared := filepath.Join(filepath.Dir(c.paths.own), "shared")
		for _, dir := range []string{c.paths.own, shared, filepath.Join(shared, "handsets")} {
			files, _ := filepath.Glob(filepath.Join(dir, "*.vcf"))
			watched = append(watched, files...)
		}
		// A delivered account book changes what phones show at once.
		books, _ := filepath.Glob(filepath.Join(accountDir(c.paths.own), "*.toml"))
		watched = append(watched, books...)
	}
	digest := sha256.New()
	for _, path := range watched {
		fmt.Fprintf(digest, "%d:%s:", len(path), path)
		file, err := os.Open(path)
		if err != nil {
			fmt.Fprint(digest, "unread;")
			continue
		}
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() {
			file.Close()
			fmt.Fprint(digest, "unread;")
			continue
		}
		content := sha256.New()
		_, err = io.Copy(content, file)
		file.Close()
		fmt.Fprintf(digest, "%x:%t;", content.Sum(nil), err == nil)
	}
	return fmt.Sprintf("%x", digest.Sum(nil))
}

func (c *bookCache) refresh() (provision.Books, error) {
	stamp := c.fingerprint()
	if stamp == c.stamp && c.books != nil {
		return c.books, nil
	}
	books, err := loadBooks(c.paths)
	var handsets []policy.Handset
	if err == nil {
		handsets, _, err = policy.LoadHandsets(c.paths.handsets)
	}
	if err == nil {
		for _, h := range handsets {
			if _, err = provision.Select(h, books); err != nil {
				break
			}
		}
	}
	if err == nil && stamp != c.fingerprint() {
		err = fmt.Errorf("phonebook: inputs changed while loading")
	}
	if err != nil {
		if c.books != nil {
			// An invalid edit does not take the directory down: the last
			// good book stays up, the same rule the daemon keeps for policy.
			return c.books, nil
		}
		return nil, err
	}
	byID := make(map[string]policy.Handset, len(handsets))
	for _, h := range handsets {
		byID[h.ID] = h
	}
	c.handsets = byID
	c.stamp, c.books, c.at = stamp, books, time.Now()
	return books, nil
}
