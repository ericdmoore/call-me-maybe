package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"callmemaybe/internal/calls"
	"callmemaybe/internal/config"
)

const callsUsage = `doorman calls — read the call log

Usage:
  doorman calls [flags]

Flags:
  --since <when>     only calls since then: a duration (24h, 7d) or a date
                     (2026-08-01). Default: everything.
  --outcome <what>   answered | voicemail | dismissed | abandoned | placed
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

The log is written only when CALL_LOG_PATH is set. See doorman schema env.
`

func runCalls(args []string) int {
	fs := flag.NewFlagSet("calls", flag.ExitOnError)
	fs.Usage = func() { fmt.Print(callsUsage) }

	var (
		since     = fs.String("since", "", "duration or date")
		outcome   = fs.String("outcome", "", "answered|voicemail|dismissed|abandoned|placed")
		caller    = fs.String("caller", "", "substring of a number at either end")
		line      = fs.String("line", "", "only calls on one line")
		direction = fs.String("direction", "", "inbound|outbound")
		limit     = fs.Int("n", 20, "most recent N, 0 for all")
		asJSON    = fs.Bool("json", false, "raw JSON Lines")
		noRedact  = fs.Bool("no-redact", false, "print full numbers")
		path      = fs.String("path", "", "override CALL_LOG_PATH")
	)
	if err := fs.Parse(args); err != nil {
		return 2
	}

	logPath := *path
	if logPath == "" {
		_ = loadDotEnv(".env")
		cfg, err := config.Load()
		if err != nil {
			fmt.Fprintf(os.Stderr, "doorman calls: %v\n", err)
			return 1
		}
		logPath = cfg.CallLogPath
	}
	if logPath == "" {
		fmt.Fprint(os.Stderr, "doorman calls: no call log configured.\n\n"+
			"Set CALL_LOG_PATH in .env to start recording, or pass --path to read\n"+
			"a file directly. See `doorman schema env` for what it records.\n")
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
			fmt.Fprintf(os.Stderr, "doorman calls: %v\n", err)
			return 2
		}
		f.Since = t
	}

	records, skipped, err := calls.Read(logPath, f)
	if err != nil {
		fmt.Fprintf(os.Stderr, "doorman calls: %v\n", err)
		return 1
	}

	if !*noRedact {
		for i := range records {
			records[i] = records[i].Redacted()
		}
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		for _, r := range records {
			if err := enc.Encode(r); err != nil {
				fmt.Fprintf(os.Stderr, "doorman calls: %v\n", err)
				return 1
			}
		}
	} else {
		printCalls(os.Stdout, records)
	}

	// A log with holes in it says so. Silence here would read as "this is
	// everything that happened", which would be a lie.
	if skipped > 0 {
		fmt.Fprintf(os.Stderr, "\n%d unreadable line(s) skipped — most likely a write "+
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
func printCalls(w *os.File, records []calls.Record) {
	if len(records) == 0 {
		fmt.Fprintln(w, "No calls match.")
		return
	}

	var showLine, showWay bool
	for _, r := range records {
		showLine = showLine || r.Line != ""
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
