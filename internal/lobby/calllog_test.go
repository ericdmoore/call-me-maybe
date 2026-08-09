package lobby

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"callmemaybe/internal/calls"
)

// testRecorder captures what a session recorded. Post runs on the session
// goroutine and the assertions run on the test goroutine, hence the mutex.
type testRecorder struct {
	mu   sync.Mutex
	recs []calls.Record
}

func (r *testRecorder) Post(rec calls.Record) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.recs = append(r.recs, rec)
}

func (r *testRecorder) only(t *testing.T) calls.Record {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.recs) != 1 {
		t.Fatalf("got %d records, want exactly 1 per call", len(r.recs))
	}
	return r.recs[0]
}

// A known caller who gets through.
func TestRecordsAKnownCallerAnswering(t *testing.T) {
	h := start(t, "512-555-0100", nil)
	h.finishPlayback(t) // welcome-known
	h.fake.expect(t, "CreateBridge")
	h.fake.expect(t, "AddToBridge") // the caller
	h.fake.expect(t, "Ring")
	h.fake.expect(t, "Originate")
	h.fake.expect(t, "Originate")
	legID := <-h.legs
	<-h.legs
	h.sess.LegAnswered(legID)
	h.fake.expect(t, "RingStop")
	h.fake.expect(t, "AddToBridge") // the handset that won
	h.sess.CallerGone()
	h.waitFinished(t)

	r := h.rec.only(t)
	if r.Known != "Grandma" {
		t.Errorf("known = %q, want Grandma", r.Known)
	}
	if r.Outcome != calls.OutcomeAnswered {
		t.Errorf("outcome = %q, want answered", r.Outcome)
	}
	if r.AnsweredBy != "PJSIP/kitchen" && r.AnsweredBy != "PJSIP/office" {
		t.Errorf("answeredBy = %q, want a house handset", r.AnsweredBy)
	}
	if len(r.Stages) != 1 || r.Stages[0].Result != "answered" {
		t.Errorf("stages = %+v, want one answered rung", r.Stages)
	}
	if r.Caller != "+15125550100" {
		t.Errorf("caller = %q, want the full E.164 on disk", r.Caller)
	}
}

// A stranger who dials a valid extension.
func TestRecordsTheExtensionLabelAndNeverTheDigits(t *testing.T) {
	h := start(t, "512-555-0199", nil)
	h.finishPlayback(t) // lobby-greeting
	for _, d := range strings.Split("428917", "") {
		h.sess.Dtmf(d)
	}
	h.fake.expect(t, "CreateBridge")
	h.fake.expect(t, "AddToBridge") // the caller
	h.fake.expect(t, "Ring")
	h.fake.expect(t, "Originate")
	legID := <-h.legs
	h.sess.LegAnswered(legID)
	h.fake.expect(t, "RingStop")
	h.fake.expect(t, "AddToBridge") // the handset that won
	h.sess.CallerGone()
	h.waitFinished(t)

	r := h.rec.only(t)
	if r.Extension != "Kitchen" {
		t.Errorf("extension = %q, want the label", r.Extension)
	}
	if r.PIN != "valid" {
		t.Errorf("pin = %q, want the verdict", r.PIN)
	}

	// The entered PIN must appear nowhere in the serialised record. This is
	// the assertion that matters most in this file.
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "428917") {
		t.Fatalf("the entered PIN is in the call record: %s", b)
	}
}

// A near-miss is almost a credential, so a wrong entry must record the
// verdict and the count and nothing else.
func TestRecordsInvalidAttemptsWithoutTheDigits(t *testing.T) {
	h := start(t, "512-555-0199", nil)
	h.finishPlayback(t)
	for _, d := range strings.Split("999111", "") {
		h.sess.Dtmf(d)
	}
	h.finishPlayback(t) // invalid-extension
	h.sess.CallerGone()
	h.waitFinished(t)

	r := h.rec.only(t)
	if r.PIN != "invalid" {
		t.Errorf("pin = %q, want invalid", r.PIN)
	}
	if r.Attempts < 1 {
		t.Errorf("attempts = %d, want at least 1", r.Attempts)
	}
	b, _ := json.Marshal(r)
	if strings.Contains(string(b), "999111") {
		t.Fatalf("a failed attempt's digits are in the record: %s", b)
	}
}

// The caller hanging up is the commonest ending and reaches cleanup only by
// cancellation. It must still produce exactly one truthful record.
func TestRecordsAnAbandonedCall(t *testing.T) {
	h := start(t, "512-555-0199", nil)
	h.finishPlayback(t)
	h.sess.CallerGone()
	h.waitFinished(t)

	r := h.rec.only(t)
	if r.Outcome != calls.OutcomeAbandoned {
		t.Errorf("outcome = %q, want abandoned", r.Outcome)
	}
}

// Dismissal records why, which is the field that tells an operator whether
// their lobby window is too short.
func TestRecordsTheDismissalReason(t *testing.T) {
	h := start(t, "512-555-0199", nil)
	h.finishPlayback(t) // greeting, then say nothing
	h.finishPlayback(t) // good-day
	h.waitFinished(t)

	r := h.rec.only(t)
	if r.Outcome != calls.OutcomeDismissed {
		t.Errorf("outcome = %q, want dismissed", r.Outcome)
	}
	if r.Reason != "no-digits" {
		t.Errorf("reason = %q, want no-digits", r.Reason)
	}
}

// The line rides on the record so one log can serve every number and still be
// readable per number. Deps.Line is the router's only input to it.
func TestTheRecordCarriesTheLine(t *testing.T) {
	h := startTuned(t, testPolicy, "512-555-0199", nil, func(d *Deps) { d.Line = "biz" })
	h.finishPlayback(t)
	h.finishPlayback(t) // good-day
	h.waitFinished(t)

	r := h.rec.only(t)
	if r.Line != "biz" {
		t.Errorf("line = %q, want biz", r.Line)
	}
	if r.LineOrDefault() != "biz" {
		t.Errorf("LineOrDefault = %q", r.LineOrDefault())
	}
	if !r.Inbound() {
		t.Error("a session is inbound by construction")
	}
}

// The compatibility gate: an install with one number must write exactly the
// record it wrote before lines existed, which means no line field at all.
func TestTheDefaultLineIsUnnamedOnTheRecord(t *testing.T) {
	h := start(t, "512-555-0199", nil)
	h.finishPlayback(t)
	h.finishPlayback(t) // good-day
	h.waitFinished(t)

	r := h.rec.only(t)
	if r.Line != "" {
		t.Errorf("line = %q, want empty — the router names only the lines that have a name", r.Line)
	}
	if r.LineOrDefault() != "default" {
		t.Errorf("LineOrDefault = %q, want default", r.LineOrDefault())
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"line"`) || strings.Contains(string(b), `"direction"`) {
		t.Errorf("a single-line install's record grew a field: %s", b)
	}
}

// ── the outbound console ─────────────────────────────────────────────────
//
// An outbound record answers a different set of questions: not who answered —
// doorman is out of the call before anybody does — but which of this house's
// numbers it went out as, to what number, and whether it left at all.

func TestAPlacedCallIsRecordedWithItsLineAndNumber(t *testing.T) {
	h := startConsole(t, testConsoleLines)
	h.console.Dtmf("2")
	h.playUntil(t, sayBeep)
	h.dial("5125550199#")
	h.waitFor(t, "Continue")
	h.console.CallerLeft()
	h.waitFinished(t)

	r := h.rec.only(t)
	if r.Inbound() {
		t.Error("a console call is outbound")
	}
	if r.Outcome != calls.OutcomePlaced {
		t.Errorf("outcome = %q, want placed — the vocabulary of who answered does not "+
			"apply to a call doorman hands to the dialplan", r.Outcome)
	}
	if r.Line != "biz" {
		t.Errorf("line = %q, want the line that was chosen", r.Line)
	}
	if r.Dialled != "5125550199" {
		t.Errorf("dialled = %q, want the number as dialled", r.Dialled)
	}

	// The inbound fields have no meaning here and must stay empty rather than
	// being filled in with something plausible.
	if r.Caller != "" || r.Known != "" || r.Extension != "" || r.PIN != "" ||
		r.AnsweredBy != "" || r.Mailbox != "" || len(r.Stages) != 0 {
		t.Errorf("an outbound record carries inbound fields: %+v", r)
	}
}

// The most consequential thing this console does is refuse, so the refusal is
// the one ending that has to be accountable for afterwards.
func TestARefusedEmergencyIsRecorded(t *testing.T) {
	h := startConsole(t, testConsoleLines)
	h.console.Dtmf("2")
	h.playUntil(t, sayBeep)
	h.dial("911#")
	h.runOut(t)

	r := h.rec.only(t)
	if r.Outcome != calls.OutcomeDismissed || r.Reason != "emergency-refused" {
		t.Errorf("outcome = %q reason = %q, want a refusal that says so", r.Outcome, r.Reason)
	}
	if r.Dialled != "911" {
		t.Errorf("dialled = %q — the number is kept even though no call was placed", r.Dialled)
	}
}

// Hanging up mid-menu is cancellation and nothing else, and it still produces
// exactly one truthful record.
func TestAConsoleCallHungUpMidMenuIsAbandoned(t *testing.T) {
	h := startConsole(t, testConsoleLines)
	h.fake.expect(t, "Play")
	h.console.CallerGone()
	h.runOut(t)

	r := h.rec.only(t)
	if r.Outcome != calls.OutcomeAbandoned {
		t.Errorf("outcome = %q, want abandoned", r.Outcome)
	}
	if r.Line != "" || r.Dialled != "" {
		t.Errorf("nothing was chosen or dialled, so nothing should be recorded: %+v", r)
	}
	// No line was chosen, so the record reads as the default line — the same
	// resolution every other unnamed record gets.
	if r.LineOrDefault() != "default" {
		t.Errorf("LineOrDefault = %q", r.LineOrDefault())
	}
}

// Invariant 1's line, drawn where the console draws it: a complete number
// somebody meant to call is a destination, a half-entry they gave up on is
// keypad noise and is forgotten.
func TestAFumbledEntryIsNeverRecorded(t *testing.T) {
	h := startConsole(t, testConsoleLines)
	h.console.Dtmf("1")
	h.playUntil(t, sayBeep)
	h.dial("4#")
	h.playUntil(t, sayInvalid)
	h.console.CallerGone()
	h.runOut(t)

	r := h.rec.only(t)
	if r.Dialled != "" {
		t.Errorf("dialled = %q, want nothing — an abandoned half-number is not a destination", r.Dialled)
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"4"`) {
		t.Errorf("a fumbled entry reached the record: %s", b)
	}
}

// A handoff that fails is recorded as such rather than as a placed call: the
// difference between "we rang them" and "we could not" is the whole value of
// the record.
func TestAFailedHandoffIsNotRecordedAsPlaced(t *testing.T) {
	h := startConsole(t, testConsoleLines, func(f *fakeARI) { f.failContinue.Store(true) })
	h.console.Dtmf("1")
	h.playUntil(t, sayBeep)
	h.dial("5125550199#")
	h.waitFor(t, "Continue")
	h.runOut(t)

	r := h.rec.only(t)
	if r.Outcome != calls.OutcomeDismissed || r.Reason != "handoff-failed" {
		t.Errorf("outcome = %q reason = %q, want a failure that says so", r.Outcome, r.Reason)
	}
}

// Nil means the call log is off. Every call path has to survive that, because
// it is the default until an operator turns it on.
func TestNoConsoleRecorderIsFine(t *testing.T) {
	h := startConsoleWith(t, testConsoleLines, func(d *ConsoleDeps) { d.Calls = nil })
	h.console.Dtmf("1")
	h.playUntil(t, sayBeep)
	h.dial("5125550199#")
	h.waitFor(t, "Continue")
	h.console.CallerLeft()
	h.waitFinished(t)
}

// Nil means the call log is off. Every call path has to survive that, because
// it is the default until an operator turns it on.
func TestNoRecorderIsFine(t *testing.T) {
	h := start(t, "512-555-0100", nil)
	h.sess.deps.Calls = nil
	h.finishPlayback(t)
	h.fake.expect(t, "CreateBridge")
	h.fake.expect(t, "AddToBridge") // the caller
	h.fake.expect(t, "Ring")
	h.fake.expect(t, "Originate")
	h.fake.expect(t, "Originate")
	legID := <-h.legs
	<-h.legs
	h.sess.LegAnswered(legID)
	h.fake.expect(t, "RingStop")
	h.fake.expect(t, "AddToBridge") // the handset that won
	h.sess.CallerGone()
	h.waitFinished(t)
}
