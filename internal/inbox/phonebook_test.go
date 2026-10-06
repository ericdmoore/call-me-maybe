package inbox

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"callmemaybe/internal/ownbook"
	"callmemaybe/internal/policy"
)

var sampleContacts = []Contact{{Name: "Jane Smith", Number: "+15125550123"}, {Name: "Jane Smith", Number: "+15125550124"}}

const contactWords = `
[[words]]
word = "house"
people = ["gabi"]
phonebook = "house"
[[words]]
word = "kitchen"
people = ["gabi"]
phonebook = "handset:kitchen"
`

func contactHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	m, err := policy.MessagesFromTOML([]byte(houseWords + contactWords))
	if err != nil {
		t.Fatal(err)
	}
	h.reader.d.Messages = m
	h.reader.d.Phonebooks = t.TempDir()
	return h
}

func TestVCardImportsAreScopedIdempotentAndNeverAdmitCallers(t *testing.T) {
	h := contactHarness(t)
	for i, word := range []string{"house", "kitchen", "house"} {
		id := fmt.Sprint(i)
		out := h.reader.Handle(context.Background(), Message{ID: id, From: "15125550101", Body: word, MediaCount: 1, Contacts: sampleContacts})
		if out.Result != "acted" || policy.ReplyProblem(out.Reply) != "" {
			t.Fatalf("outcome: %+v", out)
		}
		if i == 2 && !strings.Contains(out.Reply, "0 new") {
			t.Fatalf("duplicate import: %+v", out)
		}
		if strings.Contains(out.Detail, "Jane") || strings.Contains(out.Detail, "555") {
			t.Fatal("contact data leaked to outcome")
		}
	}
	for _, target := range []string{"house", "handset:kitchen"} {
		dir, id, _ := ownbook.SharedLocation(h.reader.d.Phonebooks, target)
		b, err := ownbook.Load(dir, id)
		if err != nil || len(b.Entries) != 2 {
			t.Fatalf("%s: %v, %+v", target, err, b)
		}
	}
	if _, ok := h.reader.d.Policy.LookupCaller("+15125550123"); ok {
		t.Fatal("import granted call admission")
	}
	if len(h.hooks) != 0 {
		t.Fatal("contact import called a webhook")
	}
	duplicate := h.reader.Handle(context.Background(), Message{ID: "0", From: "15125550101", Body: "house", MediaCount: 1, Contacts: sampleContacts})
	if duplicate.Result != "duplicate" || h.edge.unexpected != 0 {
		t.Fatalf("duplicate: %+v", duplicate)
	}
}

func TestContactAuthorisationPrecedesSavingAndAttachmentsNeverAct(t *testing.T) {
	h := contactHarness(t)
	for i, tc := range []struct{ from, word, result string }{
		{"15125550199", "house", "unlisted"},
		{"15125550102", "house", "not-allowed"},
		{"15125550101", "missing", "unknown-word"},
		{"15125550101", "garage", "failed"},
	} {
		out := h.reader.Handle(context.Background(), Message{ID: fmt.Sprint(i), From: tc.from, Body: tc.word, MediaCount: 1, Contacts: sampleContacts})
		if out.Result != tc.result {
			t.Fatalf("%s: %+v", tc.result, out)
		}
	}
	if h.edge.unexpected != 0 || len(h.hooks) != 0 {
		t.Fatal("unexpected request or unauthorised action")
	}
	if len(h.edge.sent) != 2 {
		t.Fatal("strangers or denied people were answered")
	}
}

func TestContactImportRefusesBadBatchesWithoutPartialWrites(t *testing.T) {
	for _, tc := range []struct {
		name      string
		contacts  []Contact
		count     int
		edgeError string
	}{
		{"missing-card", sampleContacts, 0, ""},
		{"missing-metadata", nil, 1, ""},
		{"edge-error", sampleContacts, 1, "invalid-card"},
		{"untrusted-error", sampleContacts, 1, "secret-name-and-number"},
		{"too-many-files", sampleContacts, 4, ""},
		{"invalid-number", []Contact{{Name: "Jane", Number: "https://example.invalid/15125550123"}}, 1, ""},
		{"partial", []Contact{sampleContacts[0], {Name: "bad", Number: "123"}}, 1, ""},
		{"long-name", []Contact{{Name: strings.Repeat("x", 201), Number: "+15125550123"}}, 1, ""},
		{"control-name", []Contact{{Name: "Jane\x00Smith", Number: "+15125550123"}}, 1, ""},
		{"xml-name", []Contact{{Name: "Jane\uffff", Number: "+15125550123"}}, 1, ""},
		{"invalid-utf8", []Contact{{Name: string([]byte{0xff}), Number: "+15125550123"}}, 1, ""},
		{"unqualified-number", []Contact{{Number: "1234567"}}, 1, ""},
		{"bad-international", []Contact{{Number: "+012345678"}}, 1, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := contactHarness(t)
			out := h.reader.Handle(context.Background(), Message{ID: "m", From: "15125550101", Body: "house", MediaCount: tc.count, Contacts: tc.contacts, ContactError: tc.edgeError})
			if out.Result != "failed" || out.Reply == "" || strings.Contains(out.Detail, "secret") {
				t.Fatalf("outcome: %+v", out)
			}
			if _, err := os.Stat(filepath.Join(h.reader.d.Phonebooks, "house.vcf")); !os.IsNotExist(err) {
				t.Fatal("failed batch wrote contacts")
			}
		})
	}
}

func TestPullNeverFollowsRedirectWithInboxToken(t *testing.T) {
	hits := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++ }))
	defer target.Close()
	edge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 302) }))
	defer edge.Close()
	e := &Edge{URL: edge.URL, Token: testToken}
	if _, err := e.Pull(context.Background(), 0); err == nil || hits != 0 {
		t.Fatal("redirect followed or not refused")
	}
}

func TestContactMetadataLimitsAndNumberNormalisation(t *testing.T) {
	h := contactHarness(t)
	many := make([]Contact, 101)
	for i := range many {
		many[i] = Contact{Name: "Example", Number: fmt.Sprintf("+151255501%02d", i%100)}
	}
	out := h.reader.Handle(context.Background(), Message{ID: "too-many", From: "15125550101", Body: "house", MediaCount: 2, Contacts: many})
	if out.Result != "failed" || !strings.Contains(out.Reply, "at most 100") {
		t.Fatalf("limit: %+v", out)
	}
	out = h.reader.Handle(context.Background(), Message{ID: "duplicates", From: "15125550101", Body: "house", MediaCount: 2, Contacts: []Contact{{Name: "", Number: "5125550123"}, {Name: "Later name", Number: "+15125550123"}}})
	if out.Result != "acted" || !strings.Contains(out.Reply, "1 new") {
		t.Fatalf("duplicates: %+v", out)
	}
	b, err := ownbook.Load(h.reader.d.Phonebooks, "house")
	if err != nil || len(b.Entries) != 1 || b.Entries[0].Name != "+15125550123" {
		t.Fatalf("normalisation: %+v %v", b, err)
	}
}

func TestStructuredMetadataCannotOperateActionWithoutMediaCount(t *testing.T) {
	h := contactHarness(t)
	out := h.reader.Handle(context.Background(), Message{ID: "metadata", From: "15125550101", Body: "garage", Contacts: sampleContacts})
	if out.Result != "failed" || len(h.hooks) != 0 {
		t.Fatalf("metadata operated garage: %+v", out)
	}
}

func TestPulledMetadataImportsWithoutDownloadingAttachments(t *testing.T) {
	h := contactHarness(t)
	h.edge.push(Message{ID: "queued", From: "15125550101", Body: "Add to: house", MediaCount: 1, Contacts: sampleContacts})
	messages, err := h.reader.d.Edge.Pull(context.Background(), 0)
	if err != nil || len(messages) != 1 {
		t.Fatalf("pull: %v", err)
	}
	out := h.reader.Handle(context.Background(), messages[0])
	if out.Result != "acted" || h.edge.unexpected != 0 {
		t.Fatalf("import: %+v", out)
	}
}

func TestContactCaptionRoutesStructuredContactsToSeveralBooks(t *testing.T) {
	h := contactHarness(t)
	m, err := policy.MessagesFromTOML([]byte(houseWords + contactWords + "\n[[words]]\nword='family'\npeople=['gabi']\nphonebook='house'\n"))
	if err != nil {
		t.Fatal(err)
	}
	h.reader.d.Messages = m
	for i, caption := range []string{" Add to: House, kitchen,HOUSE,family ", "ADD TO : KITCHEN,house"} {
		id := fmt.Sprint(i)
		out := h.reader.Handle(context.Background(), Message{ID: id, From: "15125550101", Body: caption, MediaCount: 1, Contacts: sampleContacts})
		if out.Result != "acted" || policy.ReplyProblem(out.Reply) != "" || !strings.Contains(out.Reply, "2 phone books") {
			t.Fatalf("caption outcome: %+v", out)
		}
		want := "4 new entries"
		if i == 1 {
			want = "0 new entries"
		}
		if !strings.Contains(out.Reply, want) {
			t.Fatalf("wrong deduplication count: %+v", out)
		}
	}
	if h.edge.unexpected != 0 {
		t.Fatal("import attempted to download an attachment")
	}
	for _, target := range []string{"house", "handset:kitchen"} {
		dir, id, _ := ownbook.SharedLocation(h.reader.d.Phonebooks, target)
		b, err := ownbook.Load(dir, id)
		if err != nil || len(b.Entries) != 2 {
			t.Fatalf("target %s: %+v, %v", target, b, err)
		}
	}
}

func TestContactCaptionValidatesEveryDestinationBeforeAnyImport(t *testing.T) {
	h := contactHarness(t)
	m, err := policy.MessagesFromTOML([]byte(houseWords + contactWords + "\n[[words]]\nword='private'\npeople=['eric']\nphonebook='house'\n"))
	if err != nil {
		t.Fatal(err)
	}
	h.reader.d.Messages = m
	for i, tc := range []struct{ caption, result string }{
		{"Add to:", "unknown-word"},
		{"Add to: house,", "unknown-word"},
		{"Add to: house,,kitchen", "unknown-word"},
		{"Add to: house,missing", "unknown-word"},
		{"Add to: house,garage", "unknown-word"},
		{"Add to: house,private", "not-allowed"}, // denied alias names the same book
		{"Add to: private,house", "not-allowed"},
	} {
		out := h.reader.Handle(context.Background(), Message{ID: fmt.Sprint(i), From: "15125550101", Body: tc.caption, MediaCount: 1, Contacts: sampleContacts})
		if out.Result != tc.result || (tc.result == "not-allowed" && out.Reply != "") {
			t.Fatalf("%s: %+v", tc.caption, out)
		}
		if out.Reply != "" && policy.ReplyProblem(out.Reply) != "" {
			t.Fatal("reply does not fit SMS")
		}
	}
	if h.edge.unexpected != 0 || len(h.hooks) != 0 {
		t.Fatal("invalid destination made unexpected request or operated an action")
	}
	if _, err := os.Stat(filepath.Join(h.reader.d.Phonebooks, "house.vcf")); !os.IsNotExist(err) {
		t.Fatal("invalid destination partially imported")
	}
}

func TestContactCaptionReportsPartialSaveAndResendCompletesIt(t *testing.T) {
	h := contactHarness(t)
	blocked := filepath.Join(h.reader.d.Phonebooks, "handsets")
	if err := os.WriteFile(blocked, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := h.reader.Handle(context.Background(), Message{ID: "first", From: "15125550101", Body: "Add to: house,kitchen", MediaCount: 1, Contacts: sampleContacts})
	if out.Result != "failed" || !strings.Contains(out.Reply, "Updated 1 of 2") || policy.ReplyProblem(out.Reply) != "" {
		t.Fatalf("partial save: %+v", out)
	}
	if err := os.Remove(blocked); err != nil {
		t.Fatal(err)
	}
	out = h.reader.Handle(context.Background(), Message{ID: "resend", From: "15125550101", Body: "Add to: house,kitchen", MediaCount: 1, Contacts: sampleContacts})
	if out.Result != "acted" || !strings.Contains(out.Reply, "2 new entries") || !strings.Contains(out.Reply, "2 already present") {
		t.Fatalf("resend: %+v", out)
	}
}

func TestMissingCardReplyDescribesDeliveryFailure(t *testing.T) {
	h := contactHarness(t)
	out := h.reader.Handle(context.Background(), Message{ID: "missing-card", From: "15125550101", Body: "Add to: house,kitchen"})
	if out.Result != "failed" || out.Detail != "no contact attachment received" || !strings.Contains(out.Reply, "No contact attachment reached the house") || len(out.Reply) > 160 {
		t.Fatalf("missing attachment outcome: %+v", out)
	}
	for _, target := range []string{"house", "handset:kitchen"} {
		dir, id, _ := ownbook.SharedLocation(h.reader.d.Phonebooks, target)
		book, err := ownbook.Load(dir, id)
		if err != nil || len(book.Entries) != 0 {
			t.Fatal("missing attachment wrote contacts")
		}
	}
}
