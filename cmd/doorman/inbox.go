package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"callmemaybe/internal/inbox"
	"callmemaybe/internal/policy"
	"callmemaybe/internal/xdg"
)

// `doorman inbox` — texts to the house number, consumed from the house's
// edge inbox and acted on per messages.toml.
//
// One piece of software: the CLI sets the house up, and this is the CLI
// running the house's texts. It long-polls the edge Worker over HTTPS with
// a token scoped to this house — no listener on the box, no carrier
// credential on the box; the edge holds that and sends the replies. Runs in
// a tab for the operator, or under doorman-inbox.service for good.
//
// This is the only file in cmd/doorman that may name internal/inbox,
// asserted by test the way `balance` guards the provider package: the
// daemon on the call path never reads a text.
//
// Exit codes: 0 when there is nothing to do (no INBOX_URL, no
// messages.toml) or on Ctrl-C; 2 when the configuration is wrong.

func runInbox(args []string) int {
	fs := flag.NewFlagSet("inbox", flag.ExitOnError)
	policyFlag := fs.String("policy", "", "policy file, for [[people]] (default $POLICY_PATH or ./policy.toml)")
	handsetsFlag := fs.String("handsets", "", "inventory file (default $HANDSETS_PATH or ./handsets.toml)")
	messagesFlag := fs.String("messages", "", "words file (default $MESSAGES_PATH or ./messages.toml)")
	envFlag := fs.String("env", "./.env", "secrets file, for INBOX_URL and INBOX_TOKEN")
	stateFlag := fs.String("state", "", "where handled message ids are kept (default $XDG_STATE_HOME/doorman/inbox)")
	once := fs.Bool("once", false, "pull once, handle what is there, and exit")
	wait := fs.Duration("wait", 20*time.Second, "how long each pull waits at the edge for a text")
	_ = fs.Parse(args)

	env := secretLookup(*envFlag)
	inboxURL, ok := env("INBOX_URL")
	if !ok || strings.TrimSpace(inboxURL) == "" {
		fmt.Println("INBOX_URL is not set: this house has no edge inbox, so there are no texts to consume.")
		return 0
	}
	token, ok := env("INBOX_TOKEN")
	if !ok || token == "" {
		fmt.Fprintln(os.Stderr, "✗ INBOX_URL is set but INBOX_TOKEN is not — the token the edge issued for this house")
		return 2
	}
	messagesPath := messagesPathArg(*messagesFlag)
	msgs, err := policy.LoadMessages(messagesPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ %s: %v\n", messagesPath, err)
		return 2
	}
	if !msgs.Present() {
		fmt.Printf("No %s: the house answers no texts. Copy examples/messages.example.toml to start.\n", messagesPath)
		return 0
	}
	pol, err := policy.LoadSplit(policyPathArg(*policyFlag), handsetsPathArg(*handsetsFlag))
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ %v\n", err)
		return 2
	}
	if missing := missingPeople(msgs, pol); len(missing) > 0 {
		fmt.Fprintf(os.Stderr, "✗ %s names people policy.toml does not: %s — give each [[people]] entry an id\n", messagesPath, strings.Join(missing, ", "))
		return 2
	}
	stateDir := *stateFlag
	if stateDir == "" {
		stateDir = filepath.Join(xdg.Dir("STATE", os.Getenv, os.UserHomeDir), "doorman", "inbox")
	}
	seen, err := inbox.OpenSeen(stateDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ %v\n", err)
		return 2
	}

	// Not journaled from here: the daemon owns the journal's single writer,
	// and a second writer on the same file is a fight nobody wins. The
	// message.received / message.acted events exist for the day the daemon
	// carries them (s15 M5, with s13).

	reader := inbox.New(inbox.Deps{
		Policy: pol, Messages: msgs, Seen: seen,
		Edge:        &inbox.Edge{URL: inboxURL, Token: token},
		CountryCode: defaultCountryCode(),
	})

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	fmt.Printf("inbox: %s — %d word(s) from %s; waiting for texts (Ctrl-C to stop)\n", inboxURL, len(msgs.Words()), messagesPath)
	onOutcome := func(m inbox.Message, o inbox.Outcome) {
		// Never the body, never the whole number.
		who := o.Person
		if who == "" {
			who = policy.Redact(m.From)
		}
		line := fmt.Sprintf("  %s  %-13s %-9s", time.Now().Format("15:04:05"), o.Result, who)
		if o.Word != "" {
			line += "  " + o.Word
		}
		if o.Detail != "" {
			line += "  (" + o.Detail + ")"
		}
		fmt.Println(line)
	}
	onError := func(err error) { fmt.Printf("  %s  edge: %v — retrying\n", time.Now().Format("15:04:05"), err) }

	if *once {
		msgsNow, err := reader.PullOnce(ctx, *wait, onOutcome)
		if err != nil {
			fmt.Fprintf(os.Stderr, "✗ %v\n", err)
			return 2
		}
		fmt.Printf("handled %d text(s)\n", msgsNow)
		return 0
	}
	reader.Run(ctx, *wait, onOutcome, onError)
	fmt.Println("inbox: stopped")
	return 0
}

// missingPeople is every id messages.toml names that no [[people]] carries.
func missingPeople(msgs *policy.Messages, pol *policy.Policy) []string {
	have := map[string]bool{}
	for _, id := range pol.PersonIDs() {
		have[id] = true
	}
	var missing []string
	for _, id := range msgs.PeopleReferenced() {
		if !have[id] {
			missing = append(missing, id)
		}
	}
	return missing
}

func messagesPathArg(explicit string) string {
	if explicit != "" {
		return explicit
	}
	if p := os.Getenv("MESSAGES_PATH"); p != "" {
		return p
	}
	return "./messages.toml"
}
