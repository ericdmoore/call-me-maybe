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
	"callmemaybe/internal/textlog"
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
	// The inbox's own record of what it did, for the morning digest, and
	// the house mailbox's copy of every reply — both optional, neither on
	// the decision path: a text is handled before either is told.
	outcomes, err := textlog.Open(stateDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ %v\n", err)
		return 2
	}
	mail, err := newMailer(env)
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
		if err := outcomes.Append(textlog.Record{
			At: time.Now(), ID: o.ID, To: o.To, Person: o.Person, Word: o.Word,
			Action: o.Action, Result: o.Result, Reply: o.Reply, Detail: o.Detail,
		}); err != nil {
			fmt.Printf("  %s  outcome log: %v\n", time.Now().Format("15:04:05"), err)
		}
		if mail != nil && o.Reply != "" {
			// Best-effort and off the loop: the house has already spoken.
			go func(o inbox.Outcome) {
				if err := mail(replySubject(o), replyBody(o, time.Now())); err != nil {
					fmt.Printf("  %s  house mailbox: %v\n", time.Now().Format("15:04:05"), err)
				}
			}(o)
		}
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

// replySubject and replyBody are the house mailbox's copy of a reply. Full
// number, because that mailbox is the audit log and the carrier's own
// forwarding of the inbound text already carries it; never the text of an
// unknown word, which the outcome does not hold either.
func replySubject(o inbox.Outcome) string {
	who := o.Person
	if who == "" {
		who = o.To
	}
	if o.Word != "" {
		return fmt.Sprintf("The house replied to %s (%s): %s", who, o.Word, o.Reply)
	}
	return fmt.Sprintf("The house replied to %s: %s", who, o.Reply)
}

func replyBody(o inbox.Outcome, at time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "The house replied **%s**\n\n", o.Reply)
	fmt.Fprintf(&b, "- to: `%s`", o.To)
	if o.Person != "" {
		fmt.Fprintf(&b, " (%s)", o.Person)
	}
	b.WriteString("\n")
	if o.Word != "" {
		fmt.Fprintf(&b, "- word: %s\n", o.Word)
	}
	if o.Action != "" {
		fmt.Fprintf(&b, "- action: %s\n", o.Action)
	}
	fmt.Fprintf(&b, "- result: %s\n", o.Result)
	if o.Detail != "" {
		fmt.Fprintf(&b, "- detail: %s\n", o.Detail)
	}
	fmt.Fprintf(&b, "- when: %s\n", at.Format("2006-01-02 15:04 MST"))
	return b.String()
}
