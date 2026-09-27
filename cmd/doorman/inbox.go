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

	"callmemaybe/internal/events"
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
	// Home Assistant, for the actions that name a state entity (s13 M3):
	// "garage?" reads it, done_when checks it. A LAN credential like the
	// ARI password, read here and never by the daemon; an action that
	// needs it and cannot have it is a misconfiguration, not a surprise
	// at the first question.
	ha, err := newStateReader(env, msgs, pol)
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ %v\n", err)
		return 2
	}

	// The journal's second writer (s13 M5): every text and every action
	// lands beside the daemon's own events, so `doorman events` shows the
	// door and who asked for it. Off the decision path — a text is handled
	// before the row is written — and a journal that cannot be written is
	// reported, never fatal.
	var journal *events.Sibling
	if p, ok := env("EVENT_JOURNAL_PATH"); ok && strings.TrimSpace(p) != "" {
		journal = events.OpenSibling(strings.TrimSpace(p), events.Options{})
		defer journal.Close()
	}

	reader := inbox.New(inbox.Deps{
		Policy: pol, Messages: msgs, Seen: seen,
		Edge:        &inbox.Edge{URL: inboxURL, Token: token},
		CountryCode: defaultCountryCode(),
		State:       ha,
	})

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	fmt.Printf("inbox: %s — %d word(s) from %s; waiting for texts (Ctrl-C to stop)\n", inboxURL, len(msgs.Words()), messagesPath)
	if ha != nil {
		fmt.Println("inbox: Home Assistant state reads on (HA_URL and HA_TOKEN)")
	}
	if journal != nil {
		// Try now, so an unwritable journal is known before the first text
		// rather than after it. A journal the daemon has not created yet
		// is fine: the first text after it exists is the first row.
		if err := journal.Append(ctx, events.Event{Type: events.JournalNote, Source: "doorman-inbox", Payload: events.Payload{Reason: "inbox-started"}}); err != nil {
			fmt.Printf("inbox: journal: %v\n", err)
		} else {
			fmt.Println("inbox: journal on — texts and actions are written beside the daemon's events")
		}
	}
	journalWarned := false
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
		for _, e := range journalEvents(o) {
			if err := journal.Append(ctx, e); err != nil {
				if !journalWarned {
					fmt.Printf("  %s  journal: %v (texts are in the outcome log; retrying on the next one)\n", time.Now().Format("15:04:05"), err)
				}
				journalWarned = true
				break
			}
			journalWarned = false
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

// newStateReader is Home Assistant's state endpoint when HA_URL and HA_TOKEN
// are set, nil when neither is. Half a pair is refused, and so is an action
// that names a state with no way to read it: "garage?" would then open the
// door, which is the one surprise this feature exists to prevent.
func newStateReader(env func(string) (string, bool), msgs *policy.Messages, pol *policy.Policy) (func(context.Context, string) (inbox.State, error), error) {
	haURL, _ := env("HA_URL")
	haToken, _ := env("HA_TOKEN")
	haURL, haToken = strings.TrimSpace(haURL), strings.TrimSpace(haToken)
	var asks []string
	for _, id := range msgs.ActionsReferenced() {
		if a, ok := pol.LookupAction(id); ok && a.State != "" {
			asks = append(asks, a.ID)
		}
	}
	switch {
	case haURL == "" && haToken == "":
		if len(asks) > 0 {
			return nil, fmt.Errorf("action %s names a state entity but HA_URL and HA_TOKEN are not set in .env — Home Assistant's address and a long-lived access token (RUNBOOK, \"Actions\")", strings.Join(asks, ", "))
		}
		return nil, nil
	case haURL == "":
		return nil, fmt.Errorf("HA_TOKEN is set but HA_URL is not — Home Assistant's base URL, like http://homeassistant:8123")
	case haToken == "":
		return nil, fmt.Errorf("HA_URL is set but HA_TOKEN is not — a long-lived access token from your Home Assistant profile's Security tab")
	}
	if !strings.HasPrefix(haURL, "http://") && !strings.HasPrefix(haURL, "https://") {
		return nil, fmt.Errorf("HA_URL must start with http:// or https://")
	}
	h := &inbox.HomeAssistant{URL: haURL, Token: haToken}
	return h.State, nil
}

// journalEvents is what one text leaves in the journal: message.received
// for every text, then one of message.acted (a word on its own),
// action.performed or action.refused when a registry action was named.
// Never the body, never the number — the person is an id and the message
// id is the edge's.
func journalEvents(o inbox.Outcome) []events.Event {
	now := time.Now().UTC()
	obs := &events.ActionObservation{Action: o.Action, Person: o.Person, Via: o.Via, Word: o.Word, Message: o.ID, State: o.State}
	mk := func(t events.Type, reason string) events.Event {
		return events.Event{Type: t, At: now, Source: "doorman-inbox", Payload: events.Payload{Action: obs, Reason: reason}}
	}
	out := []events.Event{mk(events.MessageReceived, o.Result)}
	switch {
	case o.Action == "":
		if (o.Result == "acted" || o.Result == "replied") && o.Word != "" {
			out = append(out, mk(events.MessageActed, o.Word))
		}
	case o.Result == "acted" || o.Result == "replied":
		out = append(out, mk(events.ActionPerformed, o.Result))
	case o.Result == "unchanged":
		out = append(out, mk(events.ActionRefused, "already "+o.State))
	case o.Result == "not-allowed" || o.Result == "needs-confirmation" || o.Result == "failed":
		out = append(out, mk(events.ActionRefused, o.Result))
	}
	return out
}
