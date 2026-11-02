package inbox

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestReplyIDsDistinguishIdenticalBatchReplies(t *testing.T) {
	h := newHarness(t)
	h.edge.push(Message{ID: "first", From: "15125550101", Body: "ping"})
	h.edge.push(Message{ID: "second", From: "15125550101", Body: "ping"})
	if n, err := h.reader.PullOnce(context.Background(), 0, nil); err != nil || n != 2 {
		t.Fatalf("pull: %d %v", n, err)
	}
	if len(h.edge.sent) != 2 {
		t.Fatalf("sent %d replies", len(h.edge.sent))
	}
	for i, id := range []string{"first", "second"} {
		if h.edge.sent[i]["message"] != "pong" || h.edge.sent[i]["in_reply_to"] != id {
			t.Fatalf("wrong reply attribution: %v", h.edge.sent[i])
		}
	}
}

func TestReplyIDsCoverUnknownAndConfirmationReplies(t *testing.T) {
	for _, body := range []string{"unknown", "close"} {
		h := newHarness(t)
		h.reader.Handle(context.Background(), Message{ID: "origin", From: "15125550101", Body: body})
		if len(h.edge.sent) != 1 || h.edge.sent[0]["in_reply_to"] != "origin" {
			t.Fatalf("missing origin on %s", body)
		}
	}
}

func TestDeferredReplyIDSurvivesShelfRestart(t *testing.T) {
	h := newShelfHarness(t)
	h.handle(h.card("card-origin"))
	h.reopen(t)
	h.tick(contactQuiet)
	h.tick(contactChoice)
	if len(h.edge.sent) != 2 {
		t.Fatalf("sent %d deferred replies", len(h.edge.sent))
	}
	for _, reply := range h.edge.sent {
		if reply["in_reply_to"] != "card-origin" {
			t.Fatal("deferred reply lost persisted origin")
		}
	}
}

func TestSendReplyRetainsIDAcrossAttemptsAndLegacySendOmitsIt(t *testing.T) {
	var bodies []map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]string
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		bodies = append(bodies, payload)
		if len(bodies) == 1 {
			http.Error(w, "uncertain", 502)
			return
		}
		w.WriteHeader(200)
	}))
	defer server.Close()
	e := Edge{URL: server.URL, Client: server.Client()}
	if e.SendReply(context.Background(), "+15125550101", "pong", "original") == nil {
		t.Fatal("hidden uncertain response")
	}
	if err := e.SendReply(context.Background(), "+15125550101", "pong", "original"); err != nil {
		t.Fatal(err)
	}
	if err := e.Send(context.Background(), "+15125550101", "legacy"); err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 3 || bodies[0]["in_reply_to"] != "original" || bodies[1]["in_reply_to"] != "original" {
		t.Fatal("retry origin changed")
	}
	if _, ok := bodies[2]["in_reply_to"]; ok {
		t.Fatal("legacy send gained an invented origin")
	}
	// The transport never retries automatically after the first HTTP failure.
}
