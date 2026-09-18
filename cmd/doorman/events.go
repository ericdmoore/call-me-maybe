package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"callmemaybe/internal/events"
)

func runEvents(args []string) int { return dumpEvents(args, os.Stdout, os.Stderr) }
func dumpEvents(args []string, out, diagnostics io.Writer) int {
	fs := flag.NewFlagSet("events", flag.ContinueOnError)
	fs.SetOutput(diagnostics)
	asJSON := fs.Bool("json", false, "emit one JSON page (required)")
	after := fs.Int64("after", 0, "exclusive sequence cursor; 0 starts at earliest retained event")
	limit := fs.Int("limit", 100, "maximum returned events, 1–10000")
	throughText := fs.String("through", "", "inclusive upper sequence; omitted captures the current high watermark")
	direction := fs.String("direction", "", "inbound|outbound; only call events")
	line := fs.String("line", "", "line name, including default")
	callID := fs.String("call", "", "full Asterisk session channel ID")
	kind := fs.String("eventType", "", "one exact event type; omitted means all")
	path := fs.String("path", "", "override EVENT_JOURNAL_PATH")
	full := fs.Bool("no-redact", false, "export full phone numbers")
	journal := fs.String("journal", "", "expected journal identity")
	generation := fs.String("generation", "", "expected journal generation")
	fs.Usage = func() {
		fmt.Fprintln(diagnostics, "Usage: doorman events --json [--after cursor] [--limit count] [--eventType type]")
		fs.PrintDefaults()
		names := []string{}
		for _, t := range events.Types() {
			names = append(names, string(t))
		}
		fmt.Fprintln(diagnostics, "Event types: "+strings.Join(names, ", "))
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	var through *int64
	if *throughText != "" {
		n, err := strconv.ParseInt(*throughText, 10, 64)
		if err != nil || n < 0 || n < *after {
			fmt.Fprintln(diagnostics, "doorman events: through must be a nonnegative cursor at or after after")
			return 2
		}
		through = &n
	}
	if !events.ValidDirection(*direction) || !*asJSON || fs.NArg() != 0 || *after < 0 || *limit < 1 || *limit > 10000 || (*kind != "" && !events.ValidType(events.Type(*kind))) {
		fs.Usage()
		return 2
	}
	if *path == "" {
		*path = configFileValue("EVENT_JOURNAL_PATH")
	}
	if *path == "" {
		fmt.Fprintln(diagnostics, "doorman events: set EVENT_JOURNAL_PATH or pass --path")
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	page, err := events.Read(ctx, *path, events.Query{After: *after, Limit: *limit, EventType: events.Type(*kind), JournalID: *journal, Generation: *generation, Full: *full, Through: through, Direction: *direction, Line: *line, CallID: *callID})
	if err != nil {
		fmt.Fprintln(diagnostics, "doorman events:", err)
		return 1
	}
	if err = json.NewEncoder(out).Encode(page); err != nil {
		fmt.Fprintln(diagnostics, "doorman events:", err)
		return 1
	}
	return 0
}

func configFileValue(key string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return loadDotEnv(".env")[key]
}
