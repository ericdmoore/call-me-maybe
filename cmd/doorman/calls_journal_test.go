package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"callmemaybe/internal/calls"
	"callmemaybe/internal/events"
)

func cliJournal(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "journal", "events.db")
	w := events.Start(path, events.Options{Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	for i, r := range []calls.Record{inboundRecord("+15125550100", ""), outboundRecord("+15125550101", "biz")} {
		id := []string{"full-inbound-id", "full-outbound-id"}[i]
		w.Post(events.Call(events.CallObserved, id, r, ""))
		w.Post(events.Call(events.SessionFinished, id, r, ""))
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}
func TestCallsSelectsJournalAndPreservesOutput(t *testing.T) {
	path := cliJournal(t)
	t.Setenv("EVENT_JOURNAL_PATH", path)
	t.Setenv("CALL_LOG_PATH", filepath.Join(t.TempDir(), "unused.jsonl"))
	t.Setenv("ARI_USERNAME", "")
	t.Setenv("ARI_PASSWORD", "")
	var out, diag bytes.Buffer
	if code := dumpCalls([]string{"--json", "--direction", "outbound"}, &out, &diag); code != 0 {
		t.Fatalf("code %d: %s", code, diag.String())
	}
	var got calls.Record
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Direction != "outbound" || got.Outcome != "placed" || got.Dialled == "+15125550101" {
		t.Fatalf("unexpected summary: %+v", got)
	}
	if diag.Len() != 0 {
		t.Fatal(diag.String())
	}
	out.Reset()
	diag.Reset()
	if code := dumpCalls([]string{"--json", "--path", path, "--no-redact", "--line", "biz", "-n", "1"}, &out, &diag); code != 0 {
		t.Fatal(code, diag.String())
	}
	if !strings.Contains(out.String(), "+15125550101") {
		t.Fatal("full identity was not returned")
	}
	out.Reset()
	diag.Reset()
	if code := dumpCalls([]string{"--path", path, "--direction", "inbound"}, &out, &diag); code != 0 {
		t.Fatal(code, diag.String())
	}
	if !strings.Contains(out.String(), "Grandma") || strings.Contains(out.String(), "WAY") {
		t.Fatal("legacy table format changed", out.String())
	}
}
func TestCallsLegacyOverrideAndNoSilentFallback(t *testing.T) {
	path := cliJournal(t)
	legacyPath := filepath.Join(t.TempDir(), "calls.jsonl")
	data, _ := json.Marshal(inboundRecord("+15125550109", ""))
	if err := os.WriteFile(legacyPath, append(data, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EVENT_JOURNAL_PATH", path)
	t.Setenv("CALL_LOG_PATH", legacyPath)
	for _, args := range [][]string{{"--source", "jsonl"}, {"--path", legacyPath}} {
		var out, diag bytes.Buffer
		args = append(args, "--json", "--no-redact")
		if code := dumpCalls(args, &out, &diag); code != 0 {
			t.Fatal(code, diag.String())
		}
		if !strings.Contains(out.String(), "+15125550109") || strings.Contains(out.String(), "+15125550100") {
			t.Fatal("legacy override ignored", out.String())
		}
	}
	t.Setenv("EVENT_JOURNAL_PATH", filepath.Join(t.TempDir(), "missing.db"))
	var out, diag bytes.Buffer
	if code := dumpCalls([]string{"--json"}, &out, &diag); code != 1 || out.Len() != 0 {
		t.Fatal("unavailable journal fell back to stale history", code, out.String())
	}
}
func TestCallsArgumentsDoNotExitTheProcess(t *testing.T) {
	for _, args := range [][]string{{"--source", "typo"}, {"--direction", "up"}, {"-n", "-1"}, {"--unknown"}, {"unexpected"}} {
		var out, diag bytes.Buffer
		if code := dumpCalls(args, &out, &diag); code != 2 || out.Len() != 0 {
			t.Errorf("args %v: code %d stdout %s", args, code, out.String())
		}
	}
}
func TestEventCLICombinedFiltersAndThrough(t *testing.T) {
	path := cliJournal(t)
	var out, diag bytes.Buffer
	args := []string{"--json", "--path", path, "--eventType", "session.finished", "--direction", "outbound", "--line", "biz", "--call", "full-outbound-id", "--through", "4"}
	if code := dumpEvents(args, &out, &diag); code != 0 {
		t.Fatal(code, diag.String())
	}
	var page events.Page
	if err := json.Unmarshal(out.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 1 || page.NextCursor != 4 || page.Through != 4 {
		t.Fatalf("page %+v", page)
	}
	for _, extra := range [][]string{{"--through", "3", "--after", "4"}, {"--through", "-1"}, {"--through", "garbage"}, {"--direction", "up"}} {
		out.Reset()
		diag.Reset()
		if code := dumpEvents(append([]string{"--json", "--path", path}, extra...), &out, &diag); code != 2 {
			t.Fatal("bad query accepted", extra)
		}
	}
}

func TestUnknownCELLineDoesNotCreateTableColumn(t *testing.T) {
	var out bytes.Buffer
	printCalls(&out, []calls.Record{{Direction: "outbound", Line: "unknown", Outcome: "ended"}})
	if strings.Contains(out.String(), "LINE") {
		t.Fatal(out.String())
	}
}
