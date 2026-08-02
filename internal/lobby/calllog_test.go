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
