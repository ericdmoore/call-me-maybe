package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"callmemaybe/internal/reminder"
)

// Invoked by Asterisk as its own user, never with the daemon's credentials.
func runReminders(args []string) int {
	if len(args) > 0 && args[0] == "prune" {
		fs := flag.NewFlagSet("reminders prune", flag.ContinueOnError)
		spool := fs.String("spool", "/var/spool/asterisk", "Asterisk's astspooldir")
		if fs.Parse(args[1:]) != nil || fs.NArg() != 0 {
			return 2
		}
		s, err := reminder.Open(*spool)
		if err == nil {
			err = s.Prune(time.Now())
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "reminders: housekeeping failed; check spool access")
			return 1
		}
		return 0
	}
	if len(args) != 2 || (args[0] != "menu" && args[0] != "deliver") {
		fmt.Fprintln(os.Stderr, "usage: doorman reminders prune [--spool directory] (menu/deliver are Asterisk AGI modes)")
		return 2
	}
	a, err := reminder.Connect(os.Stdin, os.Stdout)
	if err == nil {
		err = reminder.Run(a, args[0], args[1], time.Now)
	}
	if err != nil && !errors.Is(err, reminder.ErrHangup) {
		// AGI errors and response bodies can contain entered digits. No raw
		// error, arguments or protocol transcript may reach diagnostics.
		fmt.Fprintln(os.Stderr, "reminders: request failed; check Asterisk media and private spool access")
		return 1
	}
	return 0
}
