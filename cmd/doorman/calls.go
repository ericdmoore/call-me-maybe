package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"callmemaybe/internal/calls"
	"callmemaybe/internal/events"
)

const callsUsage = `doorman calls — read call summaries from the journal or a legacy call log

Usage:
  doorman calls [flags]

Flags:
  --source <kind>    auto (default) | journal | jsonl
  --path <file>      explicit journal or JSONL file (auto detects its format)
  --since <when>     only calls since then: a duration (24h, 7d) or a date
                     (2026-08-01). Default: everything.
  --outcome <what>   answered | voicemail | dismissed | abandoned | placed | ended
  --caller <digits>  match part of the number at the other end — the caller ID
                     on an inbound call, the number dialled on an outbound one
  --line <name>      only calls on one line. "default" is the line plain
                     policy.toml configures, which is every call on a box
                     answering one number
  --direction <way>  inbound | outbound
  -n <count>         show only the most recent N (default 20; 0 for all)
  --json             emit raw JSON Lines instead of a table
  --no-redact        print full numbers, both ends

One log for every line, so a whole day reads in order. The LINE column appears
only once a call has arrived on a line other than the default, and the
direction column only once something has gone out — a box with one number
reads exactly as it always did.

Numbers are redacted unless --no-redact, so the default output is safe to
paste into a bug report or hand to a model. Entered digits and PINs are never
in the log at all — a record says whether a PIN was valid, never what it was.

With EVENT_JOURNAL_PATH set, summaries come from the durable journal. Otherwise
CALL_LOG_PATH selects the legacy JSONL log. --source jsonl explicitly reads old
history; a missing/broken journal never silently falls back to JSONL. The journal
holds Doorman summaries and, with CEL_SPOOL_PATH, Asterisk channel lifecycles.
CEL outcome ended means channel termination with no outbound bridge observed;
it does not assert why the call ended. See docs/events.md.
`

func runCalls(args []string) int { return dumpCalls(args, os.Stdout, os.Stderr) }
func dumpCalls(args []string, out, diagnostics io.Writer) int {
	fs := flag.NewFlagSet("calls", flag.ContinueOnError)
	fs.SetOutput(diagnostics)
	fs.Usage = func() { fmt.Fprint(diagnostics, callsUsage) }

	var (
		since     = fs.String("since", "", "duration or date")
		outcome   = fs.String("outcome", "", "answered|voicemail|dismissed|abandoned|placed|ended")
		caller    = fs.String("caller", "", "substring of a number at either end")
		line      = fs.String("line", "", "only calls on one line")
		direction = fs.String("direction", "", "inbound|outbound")
		limit     = fs.Int("n", 20, "most recent N, 0 for all")
		asJSON    = fs.Bool("json", false, "raw JSON Lines")
		noRedact  = fs.Bool("no-redact", false, "print full numbers")
		path      = fs.String("path", "", "read an explicit journal or legacy JSONL file")
		source    = fs.String("source", "auto", "auto|journal|jsonl")
	)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 || *limit < 0 || !events.ValidDirection(*direction) || (*source != "auto" && *source != "journal" && *source != "jsonl") {
		fmt.Fprintln(diagnostics, "doorman calls: invalid source, direction, limit, or positional argument")
		return 2
	}

	selectedPath, selectedSource, err := callHistorySource(*path, *source)
	if err != nil {
		fmt.Fprintln(diagnostics, "doorman calls:", err)
		return 1
	}

	f := calls.Filter{
		Outcome:   *outcome,
		Caller:    *caller,
		Line:      *line,
		Direction: *direction,
		Limit:     *limit,
	}
	if *since != "" {
		t, err := parseSince(*since, time.Now())
		if err != nil {
			fmt.Fprintf(diagnostics, "doorman calls: %v\n", err)
			return 2
		}
		f.Since = t
	}

	var records []calls.Record
	var skipped int
	if selectedSource == "journal" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		history, err := events.ReadCalls(ctx, selectedPath, f, *noRedact)
		if err != nil {
			fmt.Fprintln(diagnostics, "doorman calls:", err)
			return 1
		}
		records = history.Records
		if history.RetentionFloor > 0 {
			fmt.Fprintf(diagnostics, "Journal history through sequence %d has expired; showing retained call summaries.\n", history.RetentionFloor)
		}
		if history.GapEvents > 0 {
			fmt.Fprintf(diagnostics, "Journal contains %d retained coverage-gap notice(s); call history may be incomplete.\n", history.GapEvents)
		}
	} else {
		records, skipped, err = calls.Read(selectedPath, f)
		if err != nil {
			fmt.Fprintln(diagnostics, "doorman calls:", err)
			return 1
		}
	}

	if !*noRedact && selectedSource == "jsonl" {
		for i := range records {
			records[i] = records[i].Redacted()
		}
	}

	if *asJSON {
		enc := json.NewEncoder(out)
		for _, r := range records {
			if err := enc.Encode(r); err != nil {
				fmt.Fprintf(diagnostics, "doorman calls: %v\n", err)
				return 1
			}
		}
	} else {
		printCalls(out, records)
	}

	// A log with holes in it says so. Silence here would read as "this is
	// everything that happened", which would be a lie.
	if skipped > 0 {
		fmt.Fprintf(diagnostics, "\n%d unreadable line(s) skipped — most likely a write "+
			"interrupted by power loss.\n", skipped)
	}
	return 0
}

// parseSince accepts a Go duration, a bare number of days, or a date. "7d" is
// the one people actually type and time.ParseDuration rejects it.
func parseSince(s string, now time.Time) (time.Time, error) {
	if strings.HasSuffix(s, "d") {
		var days int
		if _, err := fmt.Sscanf(s, "%dd", &days); err == nil {
			return now.AddDate(0, 0, -days), nil
		}
	}
	if d, err := time.ParseDuration(s); err == nil {
		return now.Add(-d), nil
	}
	for _, layout := range []string{time.DateOnly, time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("cannot read %q as a time: try 24h, 7d, or 2026-08-01", s)
}

// printCalls renders the table. Two columns appear only when they have
// something to say, which is the same rule `doorman check` applies to the word
// "line": a box answering one number and placing no console calls reads
// exactly as it did before either feature existed.
func printCalls(w io.Writer, records []calls.Record) {
	if len(records) == 0 {
		fmt.Fprintln(w, "No calls match.")
		return
	}

	var showLine, showWay bool
	for _, r := range records {
		showLine = showLine || (r.Line != "" && r.Line != "unknown")
		showWay = showWay || !r.Inbound()
	}

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	head := []string{"WHEN"}
	if showLine {
		head = append(head, "LINE")
	}
	if showWay {
		// CALLER is only truthful while every row is inbound. Once one row is a
		// number this house dialled, the column is the far end of the call
		// rather than the person who rang, and the header says so.
		head = append(head, "WAY", "NUMBER")
	} else {
		head = append(head, "CALLER")
	}
	head = append(head, "WHO", "OUTCOME", "DETAIL")
	fmt.Fprintln(tw, strings.Join(head, "\t"))

	for _, r := range records {
		row := []string{r.Start.Local().Format("2006-01-02 15:04")}
		if showLine {
			row = append(row, r.LineOrDefault())
		}
		if showWay {
			row = append(row, way(r))
		}
		row = append(row, farEnd(r), who(r), r.Outcome, detail(r))
		fmt.Fprintln(tw, strings.Join(row, "\t"))
	}
	_ = tw.Flush()
}

func way(r calls.Record) string {
	if r.Inbound() {
		return "in"
	}
	return "out"
}

// farEnd is the number at the other end of the call.
func farEnd(r calls.Record) string {
	if r.Inbound() {
		return orDash(r.Caller)
	}
	// Nothing dialled: the console call ended before a number was accepted, so
	// there is no far end. "withheld" would be a lie — nobody withheld it.
	if r.Dialled == "" {
		return "—"
	}
	return r.Dialled
}

// who is the person or place this call reached. An outbound call reached
// whoever answered at the other end, which doorman never finds out.
func who(r calls.Record) string {
	if w := r.Known; w != "" {
		return w
	}
	if w := r.Extension; w != "" {
		return w
	}
	return "—"
}

// detail is the one-line "and then what happened" — the reason a call was
// dismissed, who picked up, how long it rang.
func detail(r calls.Record) string {
	// An outbound row says only why it did not go out. Its duration is how long
	// the console had the handset, not how long anybody talked, and printed
	// here — beside inbound rows where the same column is exactly that — it
	// would read as a call length doorman does not know.
	if !r.Inbound() {
		if r.Reason != "" {
			return r.Reason
		}
		return "—"
	}

	var parts []string
	switch r.Outcome {
	case calls.OutcomeAnswered:
		if r.AnsweredBy != "" {
			parts = append(parts, r.AnsweredBy)
		}
	case calls.OutcomeDismissed:
		if r.Reason != "" {
			parts = append(parts, r.Reason)
		}
	case calls.OutcomeVoicemail:
		if r.Mailbox != "" {
			parts = append(parts, "mailbox "+r.Mailbox)
		}
	}
	if r.PIN == "invalid" {
		parts = append(parts, fmt.Sprintf("%d bad attempt(s)", r.Attempts))
	}
	if n := len(r.Stages); n > 1 {
		parts = append(parts, fmt.Sprintf("%d rungs", n))
	}
	if d := r.Duration(); d > 0 {
		parts = append(parts, d.Round(time.Second).String())
	}
	if len(parts) == 0 {
		return "—"
	}
	return strings.Join(parts, " · ")
}

func orDash(s string) string {
	if s == "" {
		return "withheld"
	}
	return s
}

// callHistorySource never falls back after selecting a journal: stale legacy
// data must not make unavailable durable history look healthy.
func callHistorySource(path, source string) (string, string, error) {
	if path != "" {
		if source != "auto" {
			return path, source, nil
		}
		file, err := os.Open(path)
		if err != nil {
			return "", "", err
		}
		defer file.Close()
		var header [16]byte
		n, err := io.ReadFull(file, header[:])
		if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
			return "", "", err
		}
		if n == 16 && string(header[:]) == "SQLite format 3\x00" {
			return path, "journal", nil
		}
		return path, "jsonl", nil
	}
	if source == "auto" || source == "journal" {
		if path = configFileValue("EVENT_JOURNAL_PATH"); path != "" {
			return path, "journal", nil
		}
		if source == "journal" {
			return "", "", errors.New("set EVENT_JOURNAL_PATH or pass --path")
		}
	}
	if path = configFileValue("CALL_LOG_PATH"); path != "" {
		return path, "jsonl", nil
	}
	return "", "", errors.New("set EVENT_JOURNAL_PATH or CALL_LOG_PATH, or pass --path")
}
