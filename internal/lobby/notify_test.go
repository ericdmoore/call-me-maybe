package lobby

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"callmemaybe/internal/notify"
)

// testNotifier captures what a session announced. Post runs on the session
// goroutine and the assertions run on the test goroutine, hence the mutex.
type testNotifier struct {
	mu     sync.Mutex
	events []notify.Event
}

func (n *testNotifier) Post(e notify.Event) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.events = append(n.events, e)
}

func (n *testNotifier) all() []notify.Event {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]notify.Event(nil), n.events...)
}

func (n *testNotifier) kinds() []string {
	var out []string
	for _, e := range n.all() {
		out = append(out, e.Type)
	}
	return out
}

// The event a household actually wants: who is calling, while the phone is
// still ringing.
func TestAKnownCallerIsAnnouncedWhileTheHouseIsStillRinging(t *testing.T) {
	h := start(t, "512-555-0100", nil)
	h.finishPlayback(t) // welcome-known
	h.fake.expect(t, "CreateBridge")
	h.fake.expect(t, "AddToBridge")
	h.fake.expect(t, "Ring")

	// The ringing event must be in hand before anybody could have answered —
	// announcing after the fact is the failure mode this exists to avoid.
	h.fake.expect(t, "Originate")
	h.fake.expect(t, "Originate")
	legID := <-h.legs
	<-h.legs

	ringing := h.hook.all()
	if len(ringing) != 1 || ringing[0].Type != notify.EventRinging {
		t.Fatalf("events at ring time = %v, want one ringing event", h.hook.kinds())
	}
	if ringing[0].Known != "Grandma" {
		t.Errorf("known = %q, want Grandma", ringing[0].Known)
	}
	if ringing[0].Caller != "+15125550100" {
		t.Errorf("caller = %q, want the full E.164 — redaction is the sink's decision, not the session's",
			ringing[0].Caller)
	}
	if ringing[0].Outcome != "" {
		t.Errorf("outcome = %q, but nothing has happened yet", ringing[0].Outcome)
	}

	h.sess.LegAnswered(legID)
	h.fake.expect(t, "RingStop")
	h.fake.expect(t, "AddToBridge")
	h.sess.CallerGone()
	h.waitFinished(t)

	got := h.hook.all()
	if len(got) != 2 {
		t.Fatalf("events = %v, want ringing then completed", h.hook.kinds())
	}
	if got[1].Type != notify.EventCompleted {
		t.Fatalf("second event = %q, want completed", got[1].Type)
	}
	if got[1].Outcome != "answered" {
		t.Errorf("outcome = %q, want answered", got[1].Outcome)
	}
	if got[1].AnsweredBy == "" {
		t.Error("a completed event should say which handset won")
	}
}

// A stranger who says nothing rings nothing, so there is nothing to announce
// until it is over. An automation that flashed a light here would be lying.
func TestADismissedStrangerProducesOnlyTheCompletedEvent(t *testing.T) {
	h := start(t, "512-555-0199", nil)
	h.finishPlayback(t) // greeting, then say nothing
	h.finishPlayback(t) // good-day
	h.waitFinished(t)

	got := h.hook.all()
	if len(got) != 1 || got[0].Type != notify.EventCompleted {
		t.Fatalf("events = %v, want exactly one completed event", h.hook.kinds())
	}
	if got[0].Outcome != "dismissed" || got[0].Reason != "no-digits" {
		t.Errorf("outcome/reason = %q/%q, want dismissed/no-digits", got[0].Outcome, got[0].Reason)
	}
}

// A valid extension rings a handset, so it announces — with the label, and
// never with what was typed.
func TestAnExtensionAnnouncesItsLabelAndNeverTheDigits(t *testing.T) {
	h := start(t, "512-555-0199", nil)
	h.finishPlayback(t)
	for _, d := range strings.Split("428917", "") {
		h.sess.Dtmf(d)
	}
	h.fake.expect(t, "CreateBridge")
	h.fake.expect(t, "AddToBridge")
	h.fake.expect(t, "Ring")
	h.fake.expect(t, "Originate")
	legID := <-h.legs
	h.sess.LegAnswered(legID)
	h.fake.expect(t, "RingStop")
	h.fake.expect(t, "AddToBridge")
	h.sess.CallerGone()
	h.waitFinished(t)

	got := h.hook.all()
	if len(got) != 2 || got[0].Type != notify.EventRinging {
		t.Fatalf("events = %v, want ringing then completed", h.hook.kinds())
	}
	if got[0].Extension != "Kitchen" || got[0].PIN != "valid" {
		t.Errorf("ringing event = %+v, want the label and the verdict", got[0])
	}

	// The entered PIN must appear nowhere on the wire. This is the assertion
	// that matters most in this file.
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "428917") {
		t.Fatalf("the entered PIN is in a webhook payload: %s", b)
	}
}

// A near-miss is almost a credential, so a wrong entry travels as a verdict
// and a count, and the call still ends with exactly one event.
func TestAFailedAttemptSendsTheVerdictOnly(t *testing.T) {
	h := start(t, "512-555-0199", nil)
	h.finishPlayback(t)
	for _, d := range strings.Split("999111", "") {
		h.sess.Dtmf(d)
	}
	h.finishPlayback(t) // invalid-extension
	h.sess.CallerGone()
	h.waitFinished(t)

	got := h.hook.all()
	if len(got) != 1 {
		t.Fatalf("events = %v, want one completed event and no ring", h.hook.kinds())
	}
	if got[0].PIN != "invalid" {
		t.Errorf("pin = %q, want invalid", got[0].PIN)
	}
	b, _ := json.Marshal(got[0])
	if strings.Contains(string(b), "999111") {
		t.Fatalf("a failed attempt's digits are on the wire: %s", b)
	}
}

// The caller hanging up reaches cleanup only by cancellation, and must still
// produce exactly one completed event.
func TestAnAbandonedCallStillCompletes(t *testing.T) {
	h := start(t, "512-555-0199", nil)
	h.finishPlayback(t)
	h.sess.CallerGone()
	h.waitFinished(t)

	got := h.hook.all()
	if len(got) != 1 || got[0].Outcome != "abandoned" {
		t.Fatalf("events = %+v, want one abandoned completion", got)
	}
	if got[0].MS < 0 {
		t.Errorf("ms = %d", got[0].MS)
	}
}

// Nil means the webhook is off, which is the default until an operator sets a
// URL. Every call path has to survive it.
func TestNoNotifierIsFine(t *testing.T) {
	h := start(t, "512-555-0100", nil)
	h.sess.deps.Notify = nil
	h.finishPlayback(t)
	h.fake.expect(t, "CreateBridge")
	h.fake.expect(t, "AddToBridge")
	h.fake.expect(t, "Ring")
	h.fake.expect(t, "Originate")
	h.fake.expect(t, "Originate")
	legID := <-h.legs
	<-h.legs
	h.sess.LegAnswered(legID)
	h.fake.expect(t, "RingStop")
	h.fake.expect(t, "AddToBridge")
	h.sess.CallerGone()
	h.waitFinished(t)

	if n := len(h.hook.all()); n != 0 {
		t.Errorf("%d events with the webhook off", n)
	}
}

// Voicemail is a detach, not a hangup (invariant 8), and it must still be
// announced — "nobody answered and they left a message" is exactly the kind of
// thing a household wants a light for.
func TestVoicemailIsAnnouncedAsACompletedCall(t *testing.T) {
	const vmPolicy = `
[house]
handsets = ["kitchen"]
voicemail = "household"

[[handsets]]
id = "kitchen"
endpoint = "PJSIP/kitchen"

[[people]]
name = "Grandma"
numbers = ["512-555-0100"]

[[extensions]]
pin = "428917"
label = "Kitchen"
handsets = ["kitchen"]
`
	h := startWith(t, vmPolicy, "512-555-0100", nil)
	h.finishPlayback(t) // welcome-known
	h.fake.expect(t, "CreateBridge")
	h.fake.expect(t, "AddToBridge")
	h.fake.expect(t, "Ring")
	h.fake.expect(t, "Originate")
	<-h.legs
	// Let the single stage time out into the mailbox.
	h.fake.expectAny(t, "Hangup", "RingStop", "DestroyBridge", "SetChannelVar", "Continue")
	h.waitFinished(t)

	got := h.hook.all()
	if len(got) != 2 {
		t.Fatalf("events = %v, want ringing then completed", h.hook.kinds())
	}
	if got[1].Outcome != "voicemail" || got[1].Mailbox != "household" {
		t.Errorf("completed = %+v, want a voicemail outcome naming the mailbox", got[1])
	}
}
