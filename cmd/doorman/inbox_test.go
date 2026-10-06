package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"callmemaybe/internal/events"
	"callmemaybe/internal/inbox"
	"callmemaybe/internal/policy"
)

var noSecrets = func(string) (string, bool) { return "", false }

const houseWithAStatefulAction = `
[house]
handsets = ["kitchen"]
[[handsets]]
id = "kitchen"
endpoint = "PJSIP/kitchen"
[[people]]
name = "Gabi"
id = "gabi"
numbers = ["512-555-0101"]
[[extensions]]
pin = "482913"
label = "Family"
handsets = ["kitchen"]
[[actions]]
id = "garage"
webhook = "http://ha.example.invalid/api/webhook/garage"
reply = "Asked the garage to open"
people = ["gabi"]
state = "cover.large_door_door"
done_when = "open"
[[actions]]
id = "ping"
reply = "pong"
people = ["*"]
`

// An action that names a state entity needs both HA keys; half a pair is
// refused, and so is a state with no keys at all — "garage?" would open
// the door, which is the surprise the feature exists to prevent.
func TestTheStateReaderNeedsBothHomeAssistantKeys(t *testing.T) {
	pol, err := policy.FromTOML([]byte(houseWithAStatefulAction))
	if err != nil {
		t.Fatal(err)
	}
	garage, _ := policy.MessagesFromTOML([]byte("[[words]]\nword = \"garage\"\npeople = [\"*\"]\naction = \"garage\"\n"))
	ping, _ := policy.MessagesFromTOML([]byte("[[words]]\nword = \"ping\"\npeople = [\"*\"]\naction = \"ping\"\n"))

	if _, err := newStateReader(noSecrets, garage, pol); err == nil || !strings.Contains(err.Error(), "garage names a state") {
		t.Fatalf("a state with no keys must be refused: %v", err)
	}
	if r, err := newStateReader(noSecrets, ping, pol); err != nil || r != nil {
		t.Fatalf("no stateful word, no keys: reader=%v err=%v", r != nil, err)
	}
	for name, env := range map[string]map[string]string{
		"only the URL":   {"HA_URL": "http://homeassistant:8123"},
		"only the token": {"HA_TOKEN": "not-a-real-token"},
		"a bare host":    {"HA_URL": "homeassistant:8123", "HA_TOKEN": "not-a-real-token"},
	} {
		if _, err := newStateReader(secretsFrom(env), ping, pol); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	r, err := newStateReader(secretsFrom(map[string]string{"HA_URL": "http://homeassistant:8123", "HA_TOKEN": "not-a-real-token"}), garage, pol)
	if err != nil || r == nil {
		t.Fatalf("both keys: reader=%v err=%v", r != nil, err)
	}
}

// What one text leaves in the journal: message.received always, then the
// one row that says what happened — and never the body or the number.
func TestOneTextLeavesItsRowsInTheJournal(t *testing.T) {
	types := func(o inbox.Outcome) []events.Type {
		var out []events.Type
		for _, e := range journalEvents(o) {
			if e.Source != "doorman-inbox" || e.Payload.Action == nil || e.Payload.Action.Message != o.ID {
				t.Fatalf("row %+v is not the inbox's, or lost its message id", e)
			}
			out = append(out, e.Type)
		}
		return out
	}
	join := func(ts []events.Type) string {
		s := make([]string, len(ts))
		for i, t := range ts {
			s[i] = string(t)
		}
		return strings.Join(s, " ")
	}
	for _, c := range []struct {
		o    inbox.Outcome
		want string
	}{
		{inbox.Outcome{ID: "1", Result: "acted", Word: "garage", Action: "garage", Person: "gabi", Via: "sms"}, "message.received action.performed"},
		{inbox.Outcome{ID: "2", Result: "replied", Word: "ping", Action: "ping", Person: "gabi", Via: "sms"}, "message.received action.performed"},
		{inbox.Outcome{ID: "3", Result: "replied", Word: "ping", Person: "gabi", Via: "sms"}, "message.received message.acted"},
		{inbox.Outcome{ID: "4", Result: "unchanged", Word: "garage", Action: "garage", Person: "gabi", State: "open", Via: "sms"}, "message.received action.refused"},
		{inbox.Outcome{ID: "5", Result: "not-allowed", Word: "garage", Action: "garage", Person: "grandma", Via: "sms"}, "message.received action.refused"},
		{inbox.Outcome{ID: "6", Result: "needs-confirmation", Word: "close", Action: "close", Person: "gabi", Via: "sms"}, "message.received action.refused"},
		{inbox.Outcome{ID: "7", Result: "failed", Word: "garage", Action: "garage", Person: "gabi", Via: "sms"}, "message.received action.refused"},
		{inbox.Outcome{ID: "8", Result: "asked", Word: "garage", Action: "garage", Person: "gabi", State: "closed", Via: "sms"}, "message.received"},
		{inbox.Outcome{ID: "9", Result: "unlisted", Via: "sms"}, "message.received"},
		{inbox.Outcome{ID: "10", Result: "unknown-word", Person: "gabi", Via: "sms"}, "message.received"},
		{inbox.Outcome{ID: "11", Result: "duplicate", Via: "sms"}, "message.received"},
		{inbox.Outcome{ID: "13", Result: "pending", Via: "sms", Deferred: true}, ""},
		{inbox.Outcome{ID: "14", Result: "acted", Word: "house", Via: "sms", Deferred: true}, "message.acted"},
	} {
		if got := join(types(c.o)); got != c.want {
			t.Errorf("%s: rows = %q, want %q", c.o.Result, got, c.want)
		}
	}
	rows := journalEvents(inbox.Outcome{ID: "12", Result: "unchanged", Word: "garage", Action: "garage", Person: "gabi", State: "open", Via: "sms", To: "+15125550101", Reply: "It was already open"})
	if rows[1].Payload.Reason != "already open" || rows[1].Payload.Action.State != "open" || rows[1].Payload.Action.Via != "sms" {
		t.Fatalf("refusal = %+v", rows[1])
	}
	for _, e := range rows {
		if strings.Contains(e.Payload.Reason, "5550101") {
			t.Fatal("a number reached the journal")
		}
	}
}

// check names the entity a word can ask about, and refuses a state that
// nothing on the box could read.
func TestCheckRefusesAStateWithoutHomeAssistantKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "messages.toml")
	if err := os.WriteFile(path, []byte("[[words]]\nword = \"garage\"\npeople = [\"*\"]\naction = \"garage\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	lists := allowLists(t, houseWithAStatefulAction)
	var ok bool
	out := capture(t, func() { ok = printMessages(path, lists, noSecrets) })
	if ok || !strings.Contains(out, "garage? asks cover.large_door_door") || !strings.Contains(out, "HA_URL and HA_TOKEN are not set") {
		t.Fatalf("ok=%v\n%s", ok, out)
	}
	both := secretsFrom(map[string]string{"HA_URL": "http://homeassistant:8123", "HA_TOKEN": "not-a-real-token"})
	out = capture(t, func() { ok = printMessages(path, lists, both) })
	if !ok || strings.Contains(out, "not set") || strings.Contains(out, "not-a-real-token") {
		t.Fatalf("ok=%v\n%s", ok, out)
	}
}

func TestPhonebookTargetsMustExistForCheckAndInbox(t *testing.T) {
	path := filepath.Join(t.TempDir(), "messages.toml")
	lists := allowLists(t, houseWithAStatefulAction)
	for _, target := range []string{"house", "handset:kitchen", "handset:missing"} {
		body := []byte("[[words]]\nword='contacts'\npeople=['gabi']\nphonebook='" + target + "'\n")
		if err := os.WriteFile(path, body, 0o600); err != nil {
			t.Fatal(err)
		}
		msgs, err := policy.MessagesFromTOML(body)
		if err != nil {
			t.Fatal(err)
		}
		want := target != "handset:missing"
		if got := len(msgs.MissingPhonebooks(lists[0].pol)) == 0; got != want {
			t.Fatalf("inbox accepted %s = %v", target, got)
		}
		var ok bool
		out := capture(t, func() { ok = printMessages(path, lists, noSecrets) })
		if ok != want || !strings.Contains(out, "vCards to "+target) {
			t.Fatalf("check accepted %s = %v\n%s", target, ok, out)
		}
	}
}
