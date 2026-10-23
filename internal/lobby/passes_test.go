package lobby

import (
	"bytes"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"callmemaybe/internal/calls"
)

// The lobby pass tier, driven through the fake. The list is a lookup, as the
// address book is: the file, its parser and its reload are cmd/doorman's.

type fakePasses map[string]PassHolder

func (p fakePasses) Lookup(e164 string) (PassHolder, bool) {
	h, ok := p[e164]
	return h, ok
}

// lockedLog is a log the session goroutine writes while the test reads it.
type lockedLog struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func startPasses(t *testing.T, callerNumber string, passes Passes, book Contacts, log *lockedLog) *harness {
	t.Helper()
	return startTuned(t, testPolicy, callerNumber, nil, func(d *Deps) {
		d.Passes = passes
		d.Contacts = book
		if log != nil {
			d.Log = slog.New(slog.NewTextHandler(log, nil))
			d.Cfg.RedactCallerID = true
		}
	})
}

func expectWelcomed(t *testing.T, h *harness, name string) {
	t.Helper()
	if c := h.finishPlayback(t); promptOf(c) != "welcome-known" {
		t.Fatalf("prompt = %s, want welcome-known — a pass holder skips the lobby", promptOf(c))
	}
	h.fake.expect(t, "CreateBridge")
	h.fake.expect(t, "AddToBridge")
	h.fake.expect(t, "Ring")
	o := h.fake.expect(t, "Originate")
	h.fake.expect(t, "Originate")
	if got := o.Args[3]; !strings.Contains(got, name) {
		t.Errorf("callerID = %q, want %q on it", got, name)
	}
	<-h.legs
	<-h.legs
	h.sess.CallerGone()
	h.waitFinished(t)
}

func TestAPassHolderIsWelcomedByName(t *testing.T) {
	var log lockedLog
	h := startPasses(t, "9995550123", fakePasses{"+19995550123": {Name: "Grandpa Joe"}}, nil, &log)
	expectWelcomed(t, h, "Grandpa Joe")

	r := h.rec.only(t)
	if r.Known != "Grandpa Joe" || r.Via != calls.ViaLobbyPass {
		t.Errorf("known/via = %q/%q, want Grandpa Joe/%s", r.Known, r.Via, calls.ViaLobbyPass)
	}
	// The welcome line keeps its shape: the name and a masked caller, never
	// the number.
	out := log.String()
	if !strings.Contains(out, `msg="known caller, welcoming"`) || !strings.Contains(out, "Grandpa Joe") || !strings.Contains(out, "via=lobby-pass") {
		t.Errorf("welcome not logged as a known caller:\n%s", out)
	}
	if strings.Contains(out, "9995550123") {
		t.Errorf("full caller ID logged:\n%s", out)
	}
}

// [[people]] is the box's own allow-list and is asked first: a number in both
// is welcomed by the name typed on the box, and recorded as people.
func TestPeopleWinsOverAPass(t *testing.T) {
	h := startPasses(t, "512-555-0100", fakePasses{"+15125550100": {Name: "Rose (account)"}}, nil, nil)
	expectWelcomed(t, h, "Grandma")
	if r := h.rec.only(t); r.Known != "Grandma" || r.Via != calls.ViaPeople {
		t.Errorf("known/via = %q/%q, want Grandma/people", r.Known, r.Via)
	}
}

// The account can never open a door the box has shut.
func TestABlockBeatsAPass(t *testing.T) {
	h := startPasses(t, "9995550150", fakePasses{"+19995550150": {Name: "Somebody"}},
		fakeBook{"+19995550150": {Name: "Robocall", Blocked: true}}, nil)
	if c := h.finishPlayback(t); promptOf(c) != "good-day" {
		t.Fatalf("prompt = %s, want good-day — blocked", promptOf(c))
	}
	h.expectCleanup(t, "Hangup")
	h.waitFinished(t)
	if r := h.rec.only(t); r.Known != "" {
		t.Errorf("known = %q, want empty", r.Known)
	}
}

// A pass outranks a published contact's lobby: the household chose this
// caller by name, which a website listing does not undo.
func TestAPassWinsOverAPublishedContact(t *testing.T) {
	h := startPasses(t, "9995550124", fakePasses{"+19995550124": {Name: "Dana"}},
		fakeBook{"+19995550124": {Name: "Dana's Plumbing", Published: true}}, nil)
	expectWelcomed(t, h, "Dana")
	if r := h.rec.only(t); r.Via != calls.ViaLobbyPass {
		t.Errorf("via = %q, want lobby-pass", r.Via)
	}
}

func TestACallerWithoutAPassHearsTheLobby(t *testing.T) {
	h := startPasses(t, "9995550199", fakePasses{"+19995550123": {Name: "Grandpa Joe"}}, nil, nil)
	if c := h.finishPlayback(t); promptOf(c) != "lobby-greeting" {
		t.Fatalf("prompt = %s, want lobby-greeting", promptOf(c))
	}
	h.sess.CallerGone()
	h.waitFinished(t)
	if r := h.rec.only(t); r.Known != "" {
		t.Errorf("known = %q, want empty", r.Known)
	}
}

// A withheld number never holds a pass, even if a blank slipped into a list.
func TestAWithheldCallerNeverHoldsAPass(t *testing.T) {
	h := startPasses(t, "", strictPasses{t: t, holders: fakePasses{"": {Name: "Nobody"}}}, nil, nil)
	if c := h.finishPlayback(t); promptOf(c) != "lobby-greeting" {
		t.Fatalf("prompt = %s, want lobby-greeting", promptOf(c))
	}
	h.sess.CallerGone()
	h.waitFinished(t)
}

type strictPasses struct {
	t       *testing.T
	holders fakePasses
}

func (p strictPasses) Lookup(e164 string) (PassHolder, bool) {
	if e164 == "" {
		p.t.Errorf("the pass list was consulted for a withheld caller")
	}
	return p.holders.Lookup(e164)
}
