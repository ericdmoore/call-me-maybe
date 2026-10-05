package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"callmemaybe/internal/calls"
	"callmemaybe/internal/textlog"
)

func TestDigestRendersCallsAndTextsRedactedByDefault(t *testing.T) {
	now := time.Date(2026, 9, 26, 7, 30, 0, 0, time.UTC)
	since := now.Add(-24 * time.Hour)
	records := []calls.Record{
		{ID: "a", Start: now.Add(-20 * time.Hour), Caller: "+1512•••0142", Known: "Grandma", Outcome: calls.OutcomeAnswered, AnsweredBy: "kitchen", MS: 61000},
		{ID: "b", Start: now.Add(-3 * time.Hour), Caller: "+1512•••0199", Outcome: calls.OutcomeDismissed, Reason: "no-digits", PIN: "invalid", Attempts: 2},
		{ID: "c", Start: now.Add(-2 * time.Hour), Direction: calls.DirectionOutbound, Dialled: "+1512•••0177", Line: "biz", Outcome: calls.OutcomePlaced},
	}
	texts := []textlog.Record{
		{At: now.Add(-5 * time.Hour), To: "+15125550101", Person: "gabi", Word: "garage", Action: "garage", Result: "acted", Reply: "The garage is open"},
		{At: now.Add(-1 * time.Hour), To: "+15125550199", Result: "unlisted"},
	}
	out := renderDigest(now, since, records, texts, false, true)
	for _, want := range []string{
		"# The house, Saturday 26 September 2026",
		"## Calls (3)",
		"| when | way | number | who | line | outcome | detail |",
		"`+1512•••0142` | Grandma",
		"answered | kitchen · 1m1s",
		"dismissed | no-digits · 2 bad attempt(s)",
		"out | `+1512•••0177` | — | biz | placed",
		"## Texts (2)",
		"`+1512•••0101` | gabi | garage | acted | The garage is open",
		"`+1512•••0199` | — | — | unlisted | —",
		"Numbers are redacted",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "+15125550101") {
		t.Error("a whole number leaked into the redacted digest")
	}

	full := renderDigest(now, since, records, texts, true, true)
	if !strings.Contains(full, "`+15125550101`") || strings.Contains(full, "Numbers are redacted") {
		t.Errorf("--full should show whole numbers and drop the note:\n%s", full)
	}

	empty := renderDigest(now, since, nil, nil, false, true)
	if !strings.Contains(empty, "_No calls._") || !strings.Contains(empty, "_No texts._") {
		t.Errorf("an empty day should say so:\n%s", empty)
	}
	if !strings.Contains(renderDigest(now, since, nil, nil, false, false), "set EVENT_JOURNAL_PATH") {
		t.Error("a box with no journal should be told what to set")
	}
}

func TestMailerRunsTheHookWithSubjectBodyAndAddress(t *testing.T) {
	dir := t.TempDir()
	hook := filepath.Join(dir, "hook")
	capture := filepath.Join(dir, "captured")
	script := "#!/bin/sh\nprintf 'subject=%s\\nto=%s\\n' \"$1\" \"$MAIL_TO\" > \"" + capture + "\"\ncat >> \"" + capture + "\"\n"
	if err := os.WriteFile(hook, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	env := func(k string) (string, bool) {
		switch k {
		case "MAIL_HOOK":
			return hook, true
		case "MAIL_TO":
			return "house@example.invalid", true
		}
		return "", false
	}
	m, err := newMailer(env)
	if err != nil || m == nil {
		t.Fatalf("newMailer: %v %v", m, err)
	}
	if err := m("Voicemail for family", "# body\n\nhello\n"); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(capture)
	if string(got) != "subject=Voicemail for family\nto=house@example.invalid\n# body\n\nhello\n" {
		t.Errorf("hook saw:\n%s", got)
	}

	// A failing hook is an error with its stderr, never a panic or a hang.
	if err := os.WriteFile(hook, []byte("#!/bin/sh\necho 'token revoked' >&2\nexit 3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := m("x", "y"); err == nil || !strings.Contains(err.Error(), "token revoked") {
		t.Errorf("err = %v, want the hook's own words", err)
	}
}

func TestMailerIsNilWithoutAMailboxAndRefusesHalfAConfig(t *testing.T) {
	none := func(string) (string, bool) { return "", false }
	if m, err := newMailer(none); m != nil || err != nil {
		t.Errorf("no mailbox should be nil, nil: %v %v", m, err)
	}
	half := func(k string) (string, bool) {
		if k == "MAIL_TO" {
			return "house@example.invalid", true
		}
		return "", false
	}
	if _, err := newMailer(half); err == nil || !strings.Contains(err.Error(), "MAIL_HOOK") {
		t.Errorf("MAIL_TO alone should name the missing hook: %v", err)
	}
}
