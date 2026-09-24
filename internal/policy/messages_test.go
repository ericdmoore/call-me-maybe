package policy

import (
	"strings"
	"testing"
)

func TestMessagesLoadAndValidate(t *testing.T) {
	m, err := MessagesFromTOML([]byte(`
[[words]]
word = "garage"
people = ["gabi", "eric"]
webhook = "http://homeassistant.local:8123/api/webhook/cmm-garage"
reply = "The garage is open"

[[words]]
word = "ping"
people = ["*"]
reply = "pong"
`))
	if err != nil {
		t.Fatal(err)
	}
	if !m.Present() || len(m.Words()) != 2 {
		t.Fatalf("words = %+v", m.Words())
	}
	if w, ok := m.Lookup("garage"); !ok || w.Webhook == "" || len(w.People) != 2 {
		t.Fatalf("garage = %+v %v", w, ok)
	}
	if ids := m.PeopleReferenced(); strings.Join(ids, ",") != "eric,gabi" {
		t.Fatalf("referenced = %v", ids)
	}
}

func TestMessagesRefuseWhatWouldMisfire(t *testing.T) {
	for name, body := range map[string]string{
		"a word with a space":  "[[words]]\nword = \"open sesame\"\npeople = [\"*\"]\nreply = \"ok\"\n",
		"nobody may say it":    "[[words]]\nword = \"garage\"\nreply = \"ok\"\n",
		"it does nothing":      "[[words]]\nword = \"garage\"\npeople = [\"*\"]\n",
		"a bad webhook":        "[[words]]\nword = \"garage\"\npeople = [\"*\"]\nwebhook = \"ftp://x\"\n",
		"a reply with a digit": "[[words]]\nword = \"garage\"\npeople = [\"*\"]\nreply = \"Door 1 is open\"\n",
		"a reply with a link":  "[[words]]\nword = \"garage\"\npeople = [\"*\"]\nreply = \"see https://x\"\n",
		"a shouting reply":     "[[words]]\nword = \"garage\"\npeople = [\"*\"]\nreply = \"Open!\"\n",
		"a duplicate word":     "[[words]]\nword = \"garage\"\npeople = [\"*\"]\nreply = \"ok\"\n[[words]]\nword = \"garage\"\npeople = [\"*\"]\nreply = \"ok\"\n",
		"an unknown key":       "[[words]]\nword = \"garage\"\npeople = [\"*\"]\nreply = \"ok\"\nrepply = \"x\"\n",
		"a bad person id":      "[[words]]\nword = \"garage\"\npeople = [\"Gabi Moore\"]\nreply = \"ok\"\n",
	} {
		if _, err := MessagesFromTOML([]byte(body)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestReplyProblemNamesTheRule(t *testing.T) {
	if ReplyProblem("The garage is open") != "" {
		t.Fatal("a boring reply is fine")
	}
	if !strings.Contains(ReplyProblem("It\u2019s open"), "non-ASCII") {
		t.Fatal("a curly quote halves the segment and must be named")
	}
	if !strings.Contains(ReplyProblem(strings.Repeat("a", 161)), "160") {
		t.Fatal("one segment is 160")
	}
}

func TestPeopleIDsAreOptionalUniqueAndShaped(t *testing.T) {
	base := "[house]\nhandsets = [\"kitchen\"]\n[[handsets]]\nid = \"kitchen\"\nendpoint = \"PJSIP/kitchen\"\n[[extensions]]\npin = \"482913\"\nlabel = \"Family\"\nhandsets = [\"kitchen\"]\n"
	pol, err := FromTOML([]byte(base + "[[people]]\nname = \"Gabi\"\nid = \"gabi\"\nnumbers = [\"512-555-0101\"]\n[[people]]\nname = \"Eric\"\nnumbers = [\"512-555-0102\"]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if ids := pol.PersonIDs(); len(ids) != 1 || ids[0] != "gabi" {
		t.Fatalf("ids = %v", ids)
	}
	if c, ok := pol.LookupCaller("+15125550101"); !ok || c.ID != "gabi" {
		t.Fatalf("caller = %+v", c)
	}
	for name, people := range map[string]string{
		"a duplicate id": "[[people]]\nname = \"A\"\nid = \"x\"\nnumbers = [\"512-555-0101\"]\n[[people]]\nname = \"B\"\nid = \"x\"\nnumbers = [\"512-555-0102\"]\n",
		"a bad id":       "[[people]]\nname = \"A\"\nid = \"Gabi M\"\nnumbers = [\"512-555-0101\"]\n",
	} {
		if _, err := FromTOML([]byte(base + people)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestActionsAreARegistryWithOwnersAndRules(t *testing.T) {
	base := "[house]\nhandsets = [\"kitchen\"]\n[[handsets]]\nid = \"kitchen\"\nendpoint = \"PJSIP/kitchen\"\n[[extensions]]\npin = \"482913\"\nlabel = \"Family\"\nhandsets = [\"kitchen\"]\n[[people]]\nname = \"Gabi\"\nid = \"gabi\"\nnumbers = [\"512-555-0101\"]\n"
	pol, err := FromTOML([]byte(base + "[[actions]]\nid = \"garage\"\nlabel = \"Garage door\"\nwebhook = \"http://ha.example.invalid/api/webhook/x\"\nreply = \"The garage is open\"\npeople = [\"gabi\"]\nconfirm = \"passkey\"\n[[actions]]\nid = \"ping\"\nreply = \"pong\"\npeople = [\"*\"]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := pol.Actions(); len(got) != 2 || got[0].ID != "garage" || got[0].Confirm != ConfirmPasskey || got[1].Confirm != ConfirmNone || got[1].Label != "ping" {
		t.Fatalf("actions = %+v", got)
	}
	for name, body := range map[string]string{
		"an unknown person": "[[actions]]\nid = \"garage\"\nreply = \"ok\"\npeople = [\"eric\"]\n",
		"nobody":            "[[actions]]\nid = \"garage\"\nreply = \"ok\"\n",
		"a rude reply":      "[[actions]]\nid = \"garage\"\nreply = \"Open!\"\npeople = [\"*\"]\n",
		"a bad confirm":     "[[actions]]\nid = \"garage\"\nreply = \"ok\"\npeople = [\"*\"]\nconfirm = \"sms\"\n",
		"a duplicate id":    "[[actions]]\nid = \"garage\"\nreply = \"ok\"\npeople = [\"*\"]\n[[actions]]\nid = \"garage\"\nreply = \"ok\"\npeople = [\"*\"]\n",
		"nothing to do":     "[[actions]]\nid = \"garage\"\npeople = [\"*\"]\n",
	} {
		if _, err := FromTOML([]byte(base + body)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestAWordNamesAnActionOrCarriesItsOwnReply(t *testing.T) {
	m, err := MessagesFromTOML([]byte("[[words]]\nword = \"garage\"\npeople = [\"*\"]\naction = \"garage\"\n"))
	if err != nil || m.Words()[0].Action != "garage" || len(m.ActionsReferenced()) != 1 {
		t.Fatalf("err=%v words=%+v", err, m.Words())
	}
	if _, err := MessagesFromTOML([]byte("[[words]]\nword = \"garage\"\npeople = [\"*\"]\naction = \"garage\"\nwebhook = \"http://x/y\"\n")); err == nil {
		t.Fatal("a word may not name an action and carry its own webhook")
	}
}
