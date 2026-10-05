package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

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
	}
	return books, nil
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
	paths bookPaths
	mu    sync.Mutex
	stamp string
	books provision.Books
	at    time.Time
}

func (c *bookCache) current() (provision.Books, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	stamp := ""
	watched := []string{c.paths.handsets, c.paths.policy, c.paths.contacts}
	if c.paths.own != "" {
		// The own books change on their own schedule — a minute after
		// somebody dials *88 — so they are watched like the three files.
		shared := filepath.Join(filepath.Dir(c.paths.own), "shared")
		for _, dir := range []string{c.paths.own, shared, filepath.Join(shared, "handsets")} {
			files, _ := filepath.Glob(filepath.Join(dir, "*.vcf"))
			watched = append(watched, files...)
		}
	}
	for _, p := range watched {
		if info, err := os.Stat(p); err == nil {
			stamp += fmt.Sprintf("%s:%d:%d;", p, info.Size(), info.ModTime().UnixNano())
		}
	}
	if stamp == c.stamp && c.books != nil {
		return c.books, nil
	}
	books, err := loadBooks(c.paths)
	if err != nil {
		if c.books != nil {
			// An invalid edit does not take the directory down: the last
			// good book stays up, the same rule the daemon keeps for policy.
			return c.books, nil
		}
		return nil, err
	}
	c.stamp, c.books, c.at = stamp, books, time.Now()
	return books, nil
}
