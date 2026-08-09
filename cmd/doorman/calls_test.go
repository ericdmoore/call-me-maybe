package main

import (
	"os"
	"strings"
	"testing"
	"time"

	"callmemaybe/internal/calls"
)

// `doorman calls` follows the rule `doorman check` set: a box answering one
// number never has to read the word "line", and a box that has never placed a
// call through `*4` never has to read about directions. Both columns are
// earned, not assumed.

func inboundRecord(caller, line string) calls.Record {
	return calls.Record{
		ID:      "ch1",
		Start:   time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC),
		MS:      18420,
		Line:    line,
		Caller:  caller,
		Known:   "Grandma",
		Outcome: calls.OutcomeAnswered,
	}
}

func outboundRecord(dialled, line string) calls.Record {
	return calls.Record{
		ID:        "ch2",
		Start:     time.Date(2026, 8, 1, 12, 30, 0, 0, time.UTC),
		MS:        9000,
		Line:      line,
		Direction: calls.DirectionOutbound,
		Dialled:   dialled,
		Outcome:   calls.OutcomePlaced,
	}
}

func TestOneNumberReadsExactlyAsItAlwaysDid(t *testing.T) {
	out := capture(t, func() {
		printCalls(os.Stdout, []calls.Record{inboundRecord("+1512•••0100", "")})
	})

	if !strings.Contains(out, "CALLER") {
		t.Errorf("the caller column lost its header:\n%s", out)
	}
	for _, unwanted := range []string{"LINE", "WAY", "NUMBER"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("a single-line inbound log grew a %s column:\n%s", unwanted, out)
		}
	}
}

func TestTheLineColumnAppearsOnceThereIsASecondLine(t *testing.T) {
	out := capture(t, func() {
		printCalls(os.Stdout, []calls.Record{
			inboundRecord("+1512•••0100", ""),
			inboundRecord("+1512•••0101", "biz"),
		})
	})

	if !strings.Contains(out, "LINE") {
		t.Fatalf("no line column:\n%s", out)
	}
	// The unnamed record is the default line, and the table says so rather
	// than leaving a blank an operator has to interpret.
	if !strings.Contains(out, "default") || !strings.Contains(out, "biz") {
		t.Errorf("both lines should be named:\n%s", out)
	}
	// Still inbound only, so nothing about direction.
	if strings.Contains(out, "WAY") {
		t.Errorf("an inbound-only log grew a direction column:\n%s", out)
	}
}

func TestAnOutboundCallGetsADirectionAndAFarEndNumber(t *testing.T) {
	out := capture(t, func() {
		printCalls(os.Stdout, []calls.Record{
			inboundRecord("+1512•••0100", "biz"),
			outboundRecord("+1512•••0199", "biz"),
		})
	})

	if !strings.Contains(out, "WAY") {
		t.Fatalf("no direction column once something has gone out:\n%s", out)
	}
	// CALLER is only truthful while every row is inbound.
	if strings.Contains(out, "CALLER") || !strings.Contains(out, "NUMBER") {
		t.Errorf("the far-end column should stop calling itself CALLER:\n%s", out)
	}
	if !strings.Contains(out, " in ") || !strings.Contains(out, " out ") {
		t.Errorf("both directions should be marked:\n%s", out)
	}
	if !strings.Contains(out, "+1512•••0199") {
		t.Errorf("the dialled number is the point of an outbound row:\n%s", out)
	}
	if !strings.Contains(out, calls.OutcomePlaced) {
		t.Errorf("outcome missing:\n%s", out)
	}
}

// The duration doorman knows about an outbound call is how long the console
// took, not how long anybody talked. Printed in the column where inbound rows
// show exactly that, it would be a lie.
func TestAnOutboundRowNeverShowsACallLength(t *testing.T) {
	r := outboundRecord("+1512•••0199", "biz")
	if got := detail(r); got != "—" {
		t.Errorf("detail = %q, want nothing to say about a placed call", got)
	}

	r.Outcome, r.Reason = calls.OutcomeDismissed, "emergency-refused"
	if got := detail(r); got != "emergency-refused" {
		t.Errorf("detail = %q, want the reason it did not go out", got)
	}

	// The inbound row is unchanged: its duration is the call.
	if got := detail(inboundRecord("+1512•••0100", "")); !strings.Contains(got, "18s") {
		t.Errorf("inbound detail = %q, want the call length", got)
	}
}

// A console call that ended before a number was accepted has no far end.
// "withheld" would be a lie — nobody withheld it.
func TestAnOutboundRowWithNoNumberSaysSo(t *testing.T) {
	r := outboundRecord("", "")
	r.Outcome, r.Reason = calls.OutcomeAbandoned, ""
	if got := farEnd(r); got != "—" {
		t.Errorf("farEnd = %q, want a dash", got)
	}
	if got := farEnd(inboundRecord("", "")); got != "withheld" {
		t.Errorf("farEnd = %q, want withheld — an inbound call with no number is one", got)
	}
}
