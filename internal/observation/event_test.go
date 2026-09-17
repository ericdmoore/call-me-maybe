package observation

import (
	"callmemaybe/internal/calls"
	"testing"
)

func TestSnapshotsCannotChangeAfterProduction(t *testing.T) {
	r := calls.Record{Outcome: "answered", MS: 10, Stages: []calls.Stage{{Handsets: []string{"kitchen"}}}}
	e := Call(CallAnswered, "channel", r, "")
	r.Stages[0].Handsets[0] = "changed"
	if e.Payload.Record.Stages[0].Handsets[0] != "kitchen" || e.Payload.Record.Outcome != "" || e.Payload.Record.MS != 0 {
		t.Fatal(e)
	}
	e = Call(SessionFinished, "channel", r, "")
	if e.Payload.Record.Outcome != "answered" {
		t.Fatal(e)
	}
	for _, kind := range Types() {
		if !ValidType(kind) {
			t.Fatal(kind)
		}
	}
	if ValidType(Type("invalid")) {
		t.Fatal("invalid type")
	}
	if System(JournalNote, "informational", 0).Payload.Reason != "informational" {
		t.Fatal("note lost")
	}
}
