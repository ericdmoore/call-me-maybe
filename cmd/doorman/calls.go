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
  --outcome <what>   answered | voicemail | dismissed | abandoned
  --caller <digits>  match part of a caller ID, e.g. the last four
  -n <count>         show only the most recent N (default 20; 0 for all)
  --json             emit raw JSON Lines instead of a table
  --no-redact        print full caller IDs

Caller IDs are redacted unless --no-redact, so the default output is safe to
paste into a bug report or hand to a model. Entered digits and PINs are never
in the log at all — a record says whether a PIN was valid, never what it was.

The log is written only when CALL_LOG_PATH is set. See doorman schema env.
`

func runCalls(args []string) int {
	fs := flag.NewFlagSet("calls", flag.ExitOnError)
	fs.Usage = func() { fmt.Print(callsUsage) }

	var (
		since    = fs.String("since", "", "duration or date")
		outcome  = fs.String("outcome", "", "answered|voicemail|dismissed|abandoned")
		caller   = fs.String("caller", "", "substring of a caller ID")
		limit    = fs.Int("n", 20, "most recent N, 0 for all")
		asJSON   = fs.Bool("json", false, "raw JSON Lines")
		noRedact = fs.Bool("no-redact", false, "print full caller IDs")
		path     = fs.String("path", "", "override CALL_LOG_PATH")
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

	f := calls.Filter{Outcome: *outcome, Caller: *caller, Limit: *limit}
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

func printCalls(w *os.File, records []calls.Record) {
	if len(records) == 0 {
		fmt.Fprintln(w, "No calls match.")
		return
	}

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "WHEN\tCALLER\tWHO\tOUTCOME\tDETAIL")

	for _, r := range records {
		who := r.Known
		if who == "" {
			who = r.Extension
		}
		if who == "" {
			who = "—"
		}

		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			r.Start.Local().Format("2006-01-02 15:04"),
			orDash(r.Caller), who, r.Outcome, detail(r))
	}
	_ = tw.Flush()
}

// detail is the one-line "and then what happened" — the reason a call was
// dismissed, who picked up, how long it rang.
func detail(r calls.Record) string {
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
