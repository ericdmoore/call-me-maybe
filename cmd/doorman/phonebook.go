package main

import (
	"fmt"
	"os"
	"sync"
	"time"

	"callmemaybe/internal/contacts"
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
}

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
	return books, nil
}

// renderBooks is one phone's directory, in its own format.
func renderBooks(h policy.Handset, m provision.Model, books provision.Books) ([]byte, error) {
	chosen, err := provision.Select(h, books)
	if err != nil {
		return nil, err
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
	for _, p := range []string{c.paths.handsets, c.paths.policy, c.paths.contacts} {
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
