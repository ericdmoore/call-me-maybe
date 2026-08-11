package lobby

import (
	"strings"
	"testing"
	"time"

	"callmemaybe/internal/calls"
)

// The ladder, driven through the same fake as everything else. No vCards, no
// files, no contacts.toml: the state machine takes a lookup, so a test that
// wants a blocked caller says so in one line.

// fakeBook is an address book keyed the way a real one is — by normalised
// number, exact match only.
type fakeBook map[string]Contact

func (b fakeBook) Lookup(e164 string) (Contact, bool) {
	c, ok := b[e164]
	return c, ok
}

// strictBook fails the test if anything ever looks up the numbers a lookup
// must never be asked about.
type strictBook struct {
	t      *testing.T
	forbid string
	book   fakeBook
}

func (b strictBook) Lookup(e164 string) (Contact, bool) {
	b.t.Helper()
	if e164 == b.forbid {
		b.t.Errorf("the address book was consulted for %q", e164)
	}
	return b.book.Lookup(e164)
}

func startBook(t *testing.T, callerNumber string, limiter *RateLimiter, book Contacts) *harness {
	t.Helper()
	return startTuned(t, testPolicy, callerNumber, limiter, func(d *Deps) { d.Contacts = book })
}

// The sister case, and the whole reason the feature exists: a number nobody
// published, in an address book somebody actually curates, admitted exactly as
// an allow-list match is — same prompt, same house, same record.
func TestAPersonalContactRingsTheHouseLikeAnAllowListMatch(t *testing.T) {
	h := startBook(t, "9995550123", nil, fakeBook{
		"+19995550123": {Name: "Sister"},
	})

	if c := h.finishPlayback(t); promptOf(c) != "welcome-known" {
		t.Fatalf("prompt = %s, want welcome-known — a personal contact skips the lobby", promptOf(c))
	}
	h.fake.expect(t, "CreateBridge")
	h.fake.expect(t, "AddToBridge")
	h.fake.expect(t, "Ring")
	o := h.fake.expect(t, "Originate")
	h.fake.expect(t, "Originate")
	// The name reaches the handset display the way an allow-list name does.
	if got := o.Args[3]; !strings.Contains(got, "Sister") {
		t.Errorf("callerID = %q, want the contact's name on it", got)
	}
	<-h.legs
	<-h.legs

	h.sess.CallerGone()
	h.waitFinished(t)

	if r := h.rec.only(t); r.Known != "Sister" {
		t.Errorf("known = %q, want Sister — the record carries a contact name exactly as an allow-list name", r.Known)
	}
}

// The plumber. Findable, therefore impersonable, therefore not automatic
// admission — and knowing their name buys them nothing at the keypad.
func TestAPublishedContactHearsTheLobby(t *testing.T) {
	h := startBook(t, "9995550124", nil, fakeBook{
		"+19995550124": {Name: "Kitchen Sink Plumbing", Published: true},
	})

	if c := h.finishPlayback(t); promptOf(c) != "lobby-greeting" {
		t.Fatalf("prompt = %s, want lobby-greeting", promptOf(c))
	}
	// Says nothing, and is dismissed like any other stranger: the exemptions an
	// admitted caller has at this keypad are not extended by being in a book.
	if c := h.finishPlayback(t); promptOf(c) != "good-day" {
		t.Fatalf("prompt = %s, want good-day", promptOf(c))
	}
	h.waitFinished(t)

	r := h.rec.only(t)
	if r.Outcome != calls.OutcomeDismissed || r.Reason != "no-digits" {
		t.Errorf("outcome/reason = %q/%q, want dismissed/no-digits", r.Outcome, r.Reason)
	}
	if r.Known != "" {
		t.Errorf("known = %q, want empty — a published contact was not admitted", r.Known)
	}
}

// A published contact still reaches the house by dialling, like anybody else.
// The lobby is a door, not a wall.
func TestAPublishedContactCanStillDialAnExtension(t *testing.T) {
	h := startBook(t, "9995550124", nil, fakeBook{
		"+19995550124": {Name: "Kitchen Sink Plumbing", Published: true},
	})
	h.finishPlayback(t) // lobby-greeting
	for _, d := range "428917" {
		h.sess.Dtmf(string(d))
	}

	h.fake.expect(t, "CreateBridge")
	h.fake.expect(t, "AddToBridge")
	h.fake.expect(t, "Ring")
	h.fake.expect(t, "Originate")
	<-h.legs

	h.sess.CallerGone()
	h.waitFinished(t)

	if r := h.rec.only(t); r.Extension != "Kitchen" || r.PIN != "valid" {
		t.Errorf("extension/pin = %q/%q, want Kitchen/valid", r.Extension, r.PIN)
	}
}

// Blocked is not "not admitted". The door does not open at all: no greeting, no
// dial window, nothing to work against.
func TestABlockedContactIsDismissedWithoutTheLobby(t *testing.T) {
	h := startBook(t, "9995550150", nil, fakeBook{
		"+19995550150": {Name: "Solar Panel Robocall", Published: true, Blocked: true},
	})

	if c := h.finishPlayback(t); promptOf(c) != "good-day" {
		t.Fatalf("prompt = %s, want good-day — a blocked caller never hears the lobby", promptOf(c))
	}
	h.expectCleanup(t, "Hangup")
	h.waitFinished(t)

	r := h.rec.only(t)
	if r.Outcome != calls.OutcomeDismissed || r.Reason != "blocked" {
		t.Errorf("outcome/reason = %q/%q, want dismissed/blocked", r.Outcome, r.Reason)
	}
}

// Block beats [[people]]. The contradiction is reported by `doorman check`
// rather than resolved quietly here, but when the phone rings something has to
// happen, and the restrictive answer is the one that happens.
func TestABlockSourceBeatsTheAllowList(t *testing.T) {
	// 512-555-0100 is Grandma in testPolicy.
	h := startBook(t, "5125550100", nil, fakeBook{
		"+15125550100": {Name: "Grandma", Blocked: true},
	})

	if c := h.finishPlayback(t); promptOf(c) != "good-day" {
		t.Fatalf("prompt = %s, want good-day — a block beats an allow-list entry", promptOf(c))
	}
	h.expectCleanup(t, "Hangup")
	h.waitFinished(t)

	if r := h.rec.only(t); r.Reason != "blocked" {
		t.Errorf("reason = %q, want blocked", r.Reason)
	}
}

// [[people]] beats the classifier. Hand-typed entries are explicit intent: if
// you want the dentist straight through, write them down and their card does
// not get a vote.
func TestTheAllowListBeatsAPublishedContact(t *testing.T) {
	h := startBook(t, "5125550100", nil, fakeBook{
		"+15125550100": {Name: "Gran's Nursing Home", Published: true},
	})

	if c := h.finishPlayback(t); promptOf(c) != "welcome-known" {
		t.Fatalf("prompt = %s, want welcome-known — [[people]] outranks the classifier", promptOf(c))
	}
	h.fake.expect(t, "CreateBridge")
	h.fake.expect(t, "AddToBridge")
	h.fake.expect(t, "Ring")
	h.fake.expect(t, "Originate")
	h.fake.expect(t, "Originate")
	<-h.legs
	<-h.legs

	h.sess.CallerGone()
	h.waitFinished(t)

	if r := h.rec.only(t); r.Known != "Grandma" {
		t.Errorf("known = %q, want Grandma — the name on the record is the one the operator wrote", r.Known)
	}
}

// Invariant 6, from both sides. The rate limiter is the bouncer's only defence
// against a redialler, and being on a block list is not a guess at a keypad: a
// blocked caller must not spend that budget, and must not be dismissed *as*
// rate-limited when the reason is on file.
func TestABlockedCallerIsNotAFailedPinAttempt(t *testing.T) {
	limiter := NewRateLimiter(true, 3, time.Hour)
	h := startBook(t, "9995550150", limiter, fakeBook{
		"+19995550150": {Name: "Solar Panel Robocall", Blocked: true},
	})
	h.finishPlayback(t) // good-day
	h.expectCleanup(t, "Hangup")
	h.waitFinished(t)

	if n := limiter.Size(); n != 0 {
		t.Errorf("the limiter holds %d entries, want 0 — a block is not a failed attempt", n)
	}
	if r := h.rec.only(t); r.Attempts != 0 || r.PIN != "" {
		t.Errorf("attempts/pin = %d/%q, want 0/empty", r.Attempts, r.PIN)
	}
}

// The other direction: a blocked caller who has also burned their budget is
// still dismissed as blocked. The block is checked first, so the record says
// the true reason rather than whichever check happened to fire.
func TestABlockedCallerIsNotAffectedByTheRateLimitBudget(t *testing.T) {
	limiter := NewRateLimiter(true, 3, time.Hour)
	now := time.Now()
	for range 3 {
		limiter.Failure("+19995550150", now)
	}

	h := startBook(t, "9995550150", limiter, fakeBook{
		"+19995550150": {Name: "Solar Panel Robocall", Blocked: true},
	})
	h.finishPlayback(t) // good-day
	h.expectCleanup(t, "Hangup")
	h.waitFinished(t)

	if r := h.rec.only(t); r.Reason != "blocked" {
		t.Errorf("reason = %q, want blocked — the budget must not relabel a blocked caller", r.Reason)
	}
	if n := limiter.Size(); n != 1 {
		t.Errorf("the limiter holds %d entries, want the 1 it started with", n)
	}
}

// An admitted contact clears the caller's slate exactly as an allow-list match
// does — same tier, same treatment.
func TestAnAdmittedContactClearsTheRateLimitBudget(t *testing.T) {
	limiter := NewRateLimiter(true, 3, time.Hour)
	limiter.Failure("+19995550123", time.Now())

	h := startBook(t, "9995550123", limiter, fakeBook{"+19995550123": {Name: "Sister"}})
	h.finishPlayback(t) // welcome-known
	h.fake.expect(t, "CreateBridge")

	h.sess.CallerGone()
	h.waitFinished(t)

	if n := limiter.Size(); n != 0 {
		t.Errorf("the limiter holds %d entries, want 0 — an admitted caller's slate is wiped", n)
	}
}

// A withheld number is never in a book, exactly as it is never on the
// allow-list. One blank TEL in one export would otherwise admit — or block —
// every anonymous caller there is.
func TestAnAnonymousCallerIsNeverLookedUp(t *testing.T) {
	h := startBook(t, "", nil, strictBook{
		t:      t,
		forbid: "",
		book:   fakeBook{"": {Name: "Nobody", Blocked: true}},
	})

	if c := h.finishPlayback(t); promptOf(c) != "lobby-greeting" {
		t.Fatalf("prompt = %s, want lobby-greeting — anonymous meets the lobby", promptOf(c))
	}
	h.sess.CallerGone()
	h.waitFinished(t)
}

// The compatibility gate, stated where it is enforced: no contacts.toml is a
// nil lookup, and a nil lookup is the ladder this phone has always walked.
func TestWithoutAnAddressBookTheLadderIsUnchanged(t *testing.T) {
	h := startBook(t, "9995550199", nil, nil)

	if c := h.finishPlayback(t); promptOf(c) != "lobby-greeting" {
		t.Fatalf("prompt = %s, want lobby-greeting", promptOf(c))
	}
	h.sess.CallerGone()
	h.waitFinished(t)
}
