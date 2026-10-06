package inbox

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestShortcutImportsImmediatelyAndLeavesSMSShelfUntouched(t *testing.T) {
	h := newShelfHarness(t)
	pending := h.card("sms-card")
	pending.Contacts = []Contact{{Name: "SMS only", Number: "+15125550129"}}
	h.handle(pending)
	before := map[string]contactBatch{}
	for key, batch := range h.reader.d.Shelf.batches {
		before[key] = batch
	}
	m := h.card("upload:example")
	m.Source, m.Phonebooks = "contact-upload", []string{"kitchen"}
	out := h.handle(m)
	if out.Result != "acted" || out.Via != "shortcut" || out.Reply == "" || out.Retry || h.entries(t, "house") != 0 || h.entries(t, "handset:kitchen") != 2 {
		t.Fatalf("upload: %+v", out)
	}
	if len(h.edge.sent) != 0 || !reflect.DeepEqual(before, h.reader.d.Shelf.batches) || len(h.hooks) != 0 {
		t.Fatal("upload changed the SMS exchange or sent a text/action")
	}
	if again := h.handle(m); again.Result != "acted" {
		t.Fatalf("retry must recover a result until the edge acknowledges it: %+v", again)
	}
	h.tick(contactQuiet)
	h.tick(contactChoice)
	if h.entries(t, "house") != 1 || h.entries(t, "handset:kitchen") != 2 {
		t.Fatal("SMS default included a Shortcut contact")
	}
}

func TestShortcutRequiresValidPermittedBooksWithoutTouchingPendingSMS(t *testing.T) {
	for _, books := range [][]string{nil, {}, {""}, {"house", "missing"}, {"garage"}, {"house,garage"}} {
		h := newShelfHarness(t)
		h.handle(h.card("pending"))
		m := h.card("upload:invalid")
		m.Source, m.Phonebooks = "contact-upload", books
		out := h.handle(m)
		if out.Result == "acted" || out.Retry || h.entries(t, "house") != 0 || h.entries(t, "handset:kitchen") != 0 || len(h.edge.sent) != 0 || len(h.hooks) != 0 {
			t.Fatalf("bad selection acted: %+v", out)
		}
		h.tick(contactQuiet)
		h.tick(contactChoice)
		if h.entries(t, "house") != 2 {
			t.Fatal("bad Shortcut selection cancelled the SMS default")
		}
	}
	h := newShelfHarness(t)
	m := h.card("upload:denied")
	m.Source, m.Phonebooks, m.From = "contact-upload", []string{"house"}, "15125550102"
	if out := h.handle(m); out.Result != "not-allowed" || len(h.edge.sent) != 0 {
		t.Fatalf("permissions: %+v", out)
	}
}

func TestShortcutStorageFailureRetriesOnlyItsExplicitBooks(t *testing.T) {
	h := newShelfHarness(t)
	m := h.card("upload:storage")
	m.Source, m.Phonebooks = "contact-upload", []string{"kitchen"}
	block := filepath.Join(h.reader.d.Phonebooks, "handsets")
	if err := os.WriteFile(block, []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	if out := h.handle(m); !out.Retry || len(h.reader.d.Shelf.batches) != 0 {
		t.Fatalf("storage failure: %+v", out)
	}
	if err := os.Remove(block); err != nil {
		t.Fatal(err)
	}
	if out := h.handle(m); out.Result != "acted" || h.entries(t, "house") != 0 || h.entries(t, "handset:kitchen") != 2 || len(h.edge.sent) != 0 {
		t.Fatalf("storage retry: %+v", out)
	}
}

func TestShortcutResultAcknowledgementRecoversAfterFailure(t *testing.T) {
	h := newShelfHarness(t)
	m := h.card("upload:receipt")
	m.Source, m.Phonebooks = "contact-upload", []string{"house", "kitchen"}
	acks := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/inbox/pull":
			if r.URL.Query().Get("contact_uploads") != "1" {
				t.Error("missing upload capability")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"messages": []Message{m}})
		case "/inbox/ack":
			acks++
			var body struct {
				IDs     []string       `json:"ids"`
				Results []uploadResult `json:"results"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if len(body.Results) != 1 || body.Results[0].Status != "saved" || body.Results[0].ID != m.ID || body.Results[0].Message == "" {
				t.Errorf("wrong receipt: %+v", body)
			}
			if acks == 1 {
				http.Error(w, "retry", http.StatusServiceUnavailable)
			}
		default:
			t.Errorf("unexpected request: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	h.reader.d.Edge = &Edge{URL: server.URL, Client: server.Client()}
	if _, err := h.reader.PullOnce(context.Background(), time.Second, nil); err == nil {
		t.Fatal("lost ack was hidden")
	}
	if _, err := h.reader.PullOnce(context.Background(), time.Second, nil); err != nil {
		t.Fatal(err)
	}
	if acks != 2 || h.entries(t, "house") != 2 || h.entries(t, "handset:kitchen") != 2 {
		t.Fatal("ack retry duplicated or lost entries")
	}
}
