package lobby

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"

	events "callmemaybe/internal/observation"
)

type eventCapture struct {
	mu   sync.Mutex
	rows []events.Event
}

func (c *eventCapture) Post(e events.Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rows = append(c.rows, e)
}
func (c *eventCapture) all() []events.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]events.Event(nil), c.rows...)
}
func TestJournalCapturesAnswerAndTransferWithoutClaimingCallEnded(t *testing.T) {
	c := &eventCapture{}
	h := startTuned(t, testPolicy, "512-555-0100", nil, func(d *Deps) { d.Events = c })
	h.finishPlayback(t)
	h.fake.expect(t, "CreateBridge")
	h.fake.expect(t, "AddToBridge")
	h.fake.expect(t, "Ring")
	h.fake.expect(t, "Originate")
	h.fake.expect(t, "Originate")
	leg := <-h.legs
	<-h.legs
	h.sess.LegAnswered(leg)
	h.fake.expect(t, "RingStop")
	h.fake.expect(t, "AddToBridge")
	h.sess.CallerLeft()
	h.waitFinished(t)
	got := c.all()
	want := []events.Type{events.CallObserved, events.AdmissionDecided, events.RingStarted, events.CallAnswered, events.RingStageFinished, events.CallHandedOff, events.SessionFinished}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i, e := range got {
		if e.Type != want[i] || e.CallID != "ch-caller-1" {
			t.Errorf("event %d = %+v", i, e)
		}
	}
	if got[0].Payload.Record.Outcome != "" {
		t.Fatal("observation falsely reports a final outcome")
	}
	if got[len(got)-1].Payload.Record.AnsweredBy == "" {
		t.Fatal("final summary lost the answering handset")
	}
}
func TestJournalNeverStoresEnteredPINs(t *testing.T) {
	for _, pin := range []string{"428917", "999111"} {
		t.Run(pin, func(t *testing.T) {
			c := &eventCapture{}
			h := startTuned(t, testPolicy, "512-555-0199", nil, func(d *Deps) { d.Events = c })
			h.finishPlayback(t)
			for _, digit := range strings.Split(pin, "") {
				h.sess.Dtmf(digit)
			}
			if pin == "428917" {
				h.fake.expect(t, "CreateBridge")
				h.fake.expect(t, "AddToBridge")
				h.fake.expect(t, "Ring")
				h.fake.expect(t, "Originate")
			} else {
				h.fake.expect(t, "Play")
			}
			h.sess.CallerGone()
			h.waitFinished(t)
			data, err := json.Marshal(c.all())
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(data), pin) {
				t.Fatal("entered credential leaked")
			}
			found := false
			for _, e := range c.all() {
				if e.Type == events.AdmissionDecided && e.Payload.Record.PIN != "" {
					found = true
				}
			}
			if !found {
				t.Fatal("missing credential verdict")
			}
		})
	}
}
func TestConsoleJournalRecordsHandoffNotAnswer(t *testing.T) {
	c := &eventCapture{}
	h := startConsoleWith(t, testConsoleLines, func(d *ConsoleDeps) { d.Events = c })
	// A handset transfer out of the menu is still a live channel leaving control.
	h.fake.expect(t, "Play")
	h.console.CallerLeft()
	h.waitFinished(t)
	rows := c.all()
	if len(rows) != 3 || rows[0].Type != events.CallObserved || rows[1].Type != events.CallHandedOff || rows[2].Type != events.SessionFinished {
		t.Fatalf("console events: %+v", rows)
	}
	for _, e := range rows {
		if e.CallID != "ch-office-1" {
			t.Fatal("missing correlation")
		}
	}
}
