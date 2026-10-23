package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"callmemaybe/internal/accountbook"
	"callmemaybe/internal/configcheck"
	"callmemaybe/internal/contacts"
	"callmemaybe/internal/lobby"
	"callmemaybe/internal/policy"
)

// The daemon's half of the lobby pass book: the one account book with
// role = "lobby-pass", held in memory as a map the lobby reads once per call,
// reloaded within a second of a change to the account books or the
// inventory, and never replaced by a set the shared rules refuse.
//
// It is read with configcheck.AccountBooks, the rule set sync's observer and
// executor and the directory apply, so the daemon admits from exactly the
// set sync reports as running. Every other account book stays directory
// only: nothing here looks at their contacts.
//
// The posture is policy.toml's (invariant 4), for the same reason: a bad file
// at 2am must not change who gets in. A refused change keeps the pass book in
// service; a restart onto a refused set runs the last good copy of the pass
// book, kept beside the books and written only from bytes that were
// accepted. With no usable copy the box starts with no passes, which sends
// pass holders to the lobby — the phone answers either way.
//
// Logs carry counts, a book id, a revision and a redacted problem. Never a
// name, never a number.

// passActivationKey is the pass book's entry in the activation map, beside
// each line's digest. A colon cannot appear in a line name, so it can never
// be mistaken for one.
const passActivationKey = ":lobby-pass"

// passLastGood is the last good copy of the pass book, inside the account
// book directory. A dotfile without .toml, so nothing that lists the books —
// the observer, the directory, this file — ever reads it as one.
const passLastGood = ".lobby-pass.last-good"

// passSet is one accepted pass book, swapped whole.
type passSet struct {
	index    map[string]accountbook.Holder
	book     string // the pass book's id; "" when the box holds none
	revision int64
	hash     string // the pass book file's sha256; "" when there is none
}

func emptyPasses() *passSet { return &passSet{index: map[string]accountbook.Holder{}} }

// passList adapts the held set to lobby.Passes and owns its reload.
type passList struct {
	dir      string                            // PHONEBOOK_DIR/account
	handsets func() ([]policy.Handset, string) // the inventory, and a digest that changes with it
	log      *slog.Logger

	set atomic.Pointer[passSet]

	mu    sync.Mutex // serialises reloads
	stamp string     // the inputs last considered, accepted or not
}

var _ lobby.Passes = (*passList)(nil)

// openPasses loads the pass book at startup. A box with no account books
// has none, and nobody holds a pass until one is delivered.
func openPasses(dir string, handsets func() ([]policy.Handset, string), log *slog.Logger) *passList {
	p := &passList{dir: dir, handsets: handsets, log: log}
	p.set.Store(emptyPasses())
	p.reload(true)
	return p
}

func (p *passList) current() *passSet { return p.set.Load() }

func (p *passList) Lookup(e164 string) (lobby.PassHolder, bool) {
	h, ok := p.current().index[e164]
	if !ok {
		return lobby.PassHolder{}, false
	}
	return lobby.PassHolder{Name: h.Name}, true
}

// Hash is the sha256 of the pass book file in service, or "" when there is
// none. The activation socket reports it, so a delivery is "applied" only
// once the daemon admits from exactly those bytes.
func (p *passList) Hash() string { return p.current().hash }

// readBounded reads one file within the 1 MiB sync's observer reads.
func readBounded(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return nil, errors.New("not a regular file of at most 1 MiB")
	}
	return io.ReadAll(io.LimitReader(f, 1<<20))
}

// readBooks reads every managed book file, by name. A missing directory
// holds none.
func readBooks(dir string) (map[string][]byte, error) {
	names, err := filepath.Glob(filepath.Join(dir, "*.toml"))
	if err != nil {
		return nil, err
	}
	files := make(map[string][]byte, len(names))
	for _, path := range names {
		data, err := readBounded(path)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", filepath.Base(path), err)
		}
		files[filepath.Base(path)] = data
	}
	return files, nil
}

// stampOf names the inputs a reload reads: every book's bytes and the
// inventory, which decides whether the set is valid at all.
func (p *passList) stampOf() string {
	_, digest := p.handsets()
	files, err := readBooks(p.dir)
	if err != nil {
		return "unread|" + digest
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	h := sha256.New()
	for _, name := range names {
		fmt.Fprintf(h, "%s\x00%d\x00", name, len(files[name]))
		h.Write(files[name])
	}
	return hex.EncodeToString(h.Sum(nil)) + "|" + digest
}

// reload reads the books and swaps the pass book in when the shared rules
// accept the set. startup says whether there is a pass book in service to
// keep: at startup there is not, and the last good copy stands in for one.
func (p *passList) reload(startup bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stamp = p.stampOf()
	files, err := readBooks(p.dir)
	if err != nil {
		p.refused(startup, err.Error())
		return
	}
	handsets, _ := p.handsets()
	books, problems := configcheck.AccountBooks(files, handsets)
	if len(problems) > 0 {
		where := problems[0].File
		if where == "" {
			where = "handsets"
		}
		p.refused(startup, where+": "+problems[0].Message)
		return
	}
	b, ok := accountbook.LobbyPassBook(books)
	if !ok {
		if p.current().book != "" {
			p.log.Info("lobby pass book removed; its holders hear the lobby", "book", p.current().book)
		}
		p.set.Store(emptyPasses())
		return
	}
	data := files[accountbook.FileName(b.ID)]
	p.accept(b, data)
	if err := writeLastGoodPass(filepath.Join(p.dir, passLastGood), data); err != nil {
		p.log.Warn("could not keep a last good copy of the lobby pass book; a restart onto a refused set would start without one",
			"dir", p.dir, "err", err)
	}
}

// accept puts a valid pass book in service. Logged only when it changed, so
// an edit to the inventory does not repeat the line.
func (p *passList) accept(b accountbook.Book, data []byte) {
	hash := accountbook.Hash(data)
	if p.current().hash == hash {
		return
	}
	index := b.Holders()
	p.set.Store(&passSet{index: index, book: b.ID, revision: b.Revision, hash: hash})
	p.log.Info("lobby pass book loaded", "book", b.ID, "revision", b.Revision,
		"holders", len(b.Contacts), "numbers", len(index))
}

// refused reports a set the rules will not take. Running, the pass book in
// service stays; at startup, the last good copy is tried.
func (p *passList) refused(startup bool, problem string) {
	problem = policy.RedactProblem(problem, policy.MaxProblemLength)
	if !startup {
		p.log.Warn("account books refused; the last good lobby pass book stays in service",
			"dir", p.dir, "book", p.current().book, "revision", p.current().revision, "problem", problem)
		return
	}
	data, err := readBounded(filepath.Join(p.dir, passLastGood))
	if err == nil {
		if b, err := accountbook.Parse(data); err == nil && b.LobbyPass() {
			p.log.Warn("account books refused; running the last good lobby pass book until they are valid",
				"dir", p.dir, "problem", problem)
			p.accept(b, data)
			return
		}
	}
	p.log.Warn("account books refused and no last good lobby pass book; nobody holds a pass until they are valid",
		"dir", p.dir, "problem", problem)
}

// writeLastGoodPass replaces the copy atomically: a crash leaves the old copy
// or the new one, never half of either. Unchanged bytes are not rewritten.
func writeLastGoodPass(path string, data []byte) error {
	if old, err := os.ReadFile(path); err == nil && string(old) == string(data) {
		return nil
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".lobby-pass-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// watch reloads on every change to the inputs until ctx ends: the same
// content stamp policy.toml's store uses, so an atomic rename by sync is
// seen as surely as an edit in place.
func (p *passList) watch(ctx context.Context, every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.mu.Lock()
			last := p.stamp
			p.mu.Unlock()
			if p.stampOf() != last {
				p.reload(false)
			}
		}
	}
}

// passBookHash is the hash of the lobby pass book among files, a sync view
// keyed by path, or "" when it holds none. Sync's applied check compares it
// with what the daemon reports.
func passBookHash(accountDir string, files map[string][]byte) string {
	for path, data := range files {
		if filepath.Dir(path) != accountDir {
			continue
		}
		if b, err := accountbook.Parse(data); err == nil && b.LobbyPass() {
			return accountbook.Hash(data)
		}
	}
	return ""
}

// printLobbyPass reports the lobby pass book for `doorman check`: silent
// when the box holds none, which is every box not enrolled with an account.
// Counts only — how many holders and numbers, and per line how many of those
// numbers a block source shuts out or that line's [[people]] already admits
// (and so names). Never a name or a number. An unreadable or refused set of
// account books says so and fails the check, because sync, the directory and
// the daemon refuse it too.
func printLobbyPass(dir string, handsets []policy.Handset, lists []allowList, set *contacts.Set) bool {
	files, err := readBooks(dir)
	if err != nil {
		fmt.Printf("\n✗ account books in %s cannot be read: %s\n", dir, err)
		return false
	}
	books, problems := configcheck.AccountBooks(files, handsets)
	if len(problems) > 0 {
		where := problems[0].File
		if where == "" {
			where = "handsets.toml"
		}
		fmt.Printf("\n✗ account books in %s are refused (%s)\n\n  %s\n\n", dir, where, policy.RedactProblem(problems[0].Message, policy.MaxProblemLength))
		fmt.Println("  The daemon keeps the lobby pass book it last accepted and the directory")
		fmt.Println("  keeps the books it last accepted; sync reports the file.")
		return false
	}
	b, ok := accountbook.LobbyPassBook(books)
	if !ok {
		return true
	}
	index := b.Holders()
	fmt.Printf("\nLobby pass: %d %s, %d %s   (%s, revision %d)\n",
		len(b.Contacts), plural(len(b.Contacts), "holder"), len(index), plural(len(index), "number"), b.ID, b.Revision)
	// Each number lands on the first tier that matches, as a call would:
	// a block, then [[people]], then the pass.
	blocked := 0
	for number := range index {
		if e, ok := set.Lookup(number); ok && e.Blocked {
			blocked++
		}
	}
	for _, list := range lists {
		people, admitted := 0, 0
		for number := range index {
			if e, ok := set.Lookup(number); ok && e.Blocked {
				continue
			}
			if _, ok := list.pol.LookupCaller(number); ok {
				people++
				continue
			}
			admitted++
		}
		fmt.Printf("  %s: %d admitted by pass; %d already in [[people]], welcomed by that name\n", list.where, admitted, people)
	}
	if blocked > 0 {
		fmt.Printf("  ⚠ %d blocked by a contacts source — the block wins over a pass\n", blocked)
	}
	return true
}
