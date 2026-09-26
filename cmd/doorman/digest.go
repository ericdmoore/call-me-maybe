package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"callmemaybe/internal/calls"
	"callmemaybe/internal/events"
	"callmemaybe/internal/policy"
	"callmemaybe/internal/textlog"
	"callmemaybe/internal/xdg"
)

// `doorman digest` — yesterday, every morning, as one mail.
//
// Who rang, who was let in, who was dismissed and why; which texts acted,
// which failed, what the house said back. Rendered as Markdown from the
// journal and the inbox's own outcome log, printed to stdout, or handed to
// the mail hook with --mail. The journal stays the record and this is a
// query over it — never an input to anything (invariant 10), and never on
// a call path: a timer runs it.
//
// Redacted by default, exactly as `doorman calls` is, because stdout goes
// anywhere. --full prints whole numbers, which is what the house mailbox
// wants: it is the audit log, and the carrier's forwarding and the
// voicemail feed already carry them there.

func runDigest(args []string) int {
	fs := flag.NewFlagSet("digest", flag.ExitOnError)
	sinceFlag := fs.String("since", "24h", "how far back: a duration (24h) or a date (2026-09-25)")
	full := fs.Bool("full", false, "whole numbers rather than redacted ones")
	mailFlag := fs.Bool("mail", false, "send it through MAIL_HOOK to MAIL_TO instead of printing")
	subjectFlag := fs.String("subject", "", "the mail's subject (default \"Call Me Maybe — <date>\")")
	envFlag := fs.String("env", "./.env", "secrets file, for EVENT_JOURNAL_PATH, MAIL_HOOK and MAIL_TO")
	pathFlag := fs.String("path", "", "read an explicit journal or legacy JSONL file (default $EVENT_JOURNAL_PATH)")
	stateFlag := fs.String("inbox-state", "", "where `doorman inbox` keeps its outcome log (default $XDG_STATE_HOME/doorman/inbox)")
	_ = fs.Parse(args)

	env := secretLookup(*envFlag)
	now := time.Now()
	since, err := parseSince(*sinceFlag, now)
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ %v\n", err)
		return 2
	}

	// Calls, from the journal (or the legacy log when that is all there is).
	path := *pathFlag
	if path == "" {
		if p, ok := env("EVENT_JOURNAL_PATH"); ok && p != "" {
			path = p
		}
	}
	selectedPath, source, srcErr := callHistorySource(path, "auto")
	haveSource := srcErr == nil
	var records []calls.Record
	switch {
	case !haveSource:
		// No journal and no legacy log is a real state on a box that has
		// never been configured for either; say so in the digest rather
		// than failing the morning mail over it.
		records = nil
	case source == "journal":
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		history, rerr := events.ReadCalls(ctx, selectedPath, calls.Filter{Since: since}, *full)
		cancel()
		if rerr != nil {
			fmt.Fprintf(os.Stderr, "✗ %v\n", rerr)
			return 1
		}
		records = history.Records
	default:
		all, _, rerr := calls.Read(selectedPath, calls.Filter{Since: since})
		if rerr != nil {
			fmt.Fprintf(os.Stderr, "✗ %v\n", rerr)
			return 1
		}
		for _, r := range all {
			if !*full {
				r = r.Redacted()
			}
			records = append(records, r)
		}
	}

	// Texts, from the inbox's outcome log.
	stateDir := *stateFlag
	if stateDir == "" {
		stateDir = filepath.Join(xdg.Dir("STATE", os.Getenv, os.UserHomeDir), "doorman", "inbox")
	}
	texts, err := textlog.Read(stateDir, since)
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ %v\n", err)
		return 1
	}

	body := renderDigest(now, since, records, texts, *full, haveSource)
	if !*mailFlag {
		fmt.Print(body)
		return 0
	}
	mail, err := newMailer(env)
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ %v\n", err)
		return 2
	}
	if mail == nil {
		fmt.Fprintln(os.Stderr, "✗ --mail needs MAIL_HOOK and MAIL_TO in the environment or the secrets file")
		return 2
	}
	subject := *subjectFlag
	if subject == "" {
		subject = "Call Me Maybe — " + now.Format("Mon 2 Jan 2006")
	}
	if err := mail(subject, body); err != nil {
		fmt.Fprintf(os.Stderr, "✗ %v\n", err)
		return 1
	}
	fmt.Printf("sent: %s (%d call(s), %d text(s))\n", subject, len(records), len(texts))
	return 0
}

// renderDigest is the whole of the mail, as Markdown. Pure, so the shape
// is tested without a journal.
func renderDigest(now, since time.Time, records []calls.Record, texts []textlog.Record, full, haveSource bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# The house, %s\n\n", now.Format("Monday 2 January 2006"))
	fmt.Fprintf(&b, "Since %s.\n\n", since.Local().Format("Mon 2 Jan 15:04"))

	fmt.Fprintf(&b, "## Calls (%d)\n\n", len(records))
	switch {
	case !haveSource:
		b.WriteString("_No journal and no call log to read; set EVENT_JOURNAL_PATH._\n\n")
	case len(records) == 0:
		b.WriteString("_No calls._\n\n")
	default:
		var showLine bool
		for _, r := range records {
			showLine = showLine || (r.Line != "" && r.Line != "unknown")
		}
		head := "| when | way | number | who |"
		sep := "|---|---|---|---|"
		if showLine {
			head += " line |"
			sep += "---|"
		}
		head += " outcome | detail |"
		sep += "---|---|"
		b.WriteString(head + "\n" + sep + "\n")
		for _, r := range records {
			row := []string{r.Start.Local().Format("Mon 15:04"), way(r), mdCode(farEnd(r)), mdCell(who(r))}
			if showLine {
				row = append(row, mdCell(r.LineOrDefault()))
			}
			row = append(row, mdCell(r.Outcome), mdCell(detail(r)))
			b.WriteString("| " + strings.Join(row, " | ") + " |\n")
		}
		b.WriteString("\n")
	}

	fmt.Fprintf(&b, "## Texts (%d)\n\n", len(texts))
	if len(texts) == 0 {
		b.WriteString("_No texts._\n\n")
	} else {
		b.WriteString("| when | from | who | word | result | the house said |\n|---|---|---|---|---|---|\n")
		for _, t := range texts {
			from := t.To
			if !full {
				from = policy.Redact(from)
			}
			b.WriteString("| " + strings.Join([]string{
				t.At.Local().Format("Mon 15:04"), mdCode(orDash(from)), mdCell(orDashText(t.Person)),
				mdCell(orDashText(t.Word)), mdCell(t.Result), mdCell(orDashText(t.Reply)),
			}, " | ") + " |\n")
		}
		b.WriteString("\n")
	}
	if !full {
		b.WriteString("_Numbers are redacted; `doorman digest --full` shows them whole._\n")
	}
	return b.String()
}

// mdCode keeps a number out of Markdown's way: a code span renders
// literally, and `+1512…` is not a list item or an autolink.
func mdCode(s string) string {
	if s == "" || s == "—" {
		return "—"
	}
	return "`" + strings.ReplaceAll(s, "`", "'") + "`"
}

// mdCell escapes the one character a table cell cannot hold.
func mdCell(s string) string {
	if s == "" {
		return "—"
	}
	return strings.ReplaceAll(s, "|", "\\|")
}

func orDashText(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
