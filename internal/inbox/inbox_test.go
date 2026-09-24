package inbox

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"callmemaybe/internal/policy"
)

const testToken = "not-a-real-inbox-token-0123456789"

// fakeEdge is the Worker: a queue, an ack list, and what was sent.
type fakeEdge struct {
	mu     sync.Mutex
	queue  []Message
	acked  []string
	sent   []map[string]string
	auths  []string
	pulls  int
	server *httptest.Server
}

func newFakeEdge(t *testing.T) *fakeEdge {
	t.Helper()
	f := &fakeEdge{}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.auths = append(f.auths, r.Header.Get("Authorization"))
		if r.Header.Get("Authorization") != "Bearer "+testToken {
			http.Error(w, "unauthorized", 401)
			return
		}
		switch {
		case r.URL.Path == "/h/test/inbox/pull":
			f.pulls++
			_ = json.NewEncoder(w).Encode(map[string]any{"messages": f.queue})
			f.queue = nil
		case r.URL.Path == "/h/test/inbox/ack":
			var body struct{ IDs []string }
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.acked = append(f.acked, body.IDs...)
			_ = json.NewEncoder(w).Encode(map[string]int{"acked": len(body.IDs)})
		case r.URL.Path == "/h/test/inbox/send":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.sent = append(f.sent, body)
			_ = json.NewEncoder(w).Encode(map[string]bool{"accepted": true})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeEdge) push(m Message) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queue = append(f.queue, m)
}

const housePolicy = `
[house]
handsets = ["kitchen"]
[[handsets]]
id = "kitchen"
endpoint = "PJSIP/kitchen"
[[people]]
name = "Gabi"
id = "gabi"
numbers = ["512-555-0101"]
[[people]]
name = "Grandma"
numbers = ["512-555-0102"]
[[extensions]]
pin = "482913"
label = "Family"
handsets = ["kitchen"]
`

const houseWords = `
[[words]]
word = "garage"
people = ["gabi"]
webhook = "WEBHOOK"
reply = "The garage is open"
[[words]]
word = "ping"
people = ["*"]
reply = "pong"
`

type harness struct {
	edge    *fakeEdge
	reader  *Reader
	hooks   []map[string]string
	hookErr error
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	pol, err := policy.FromTOML([]byte(housePolicy))
	if err != nil {
		t.Fatal(err)
	}
	msgs, err := policy.MessagesFromTOML([]byte(strings.ReplaceAll(houseWords, "WEBHOOK", "http://ha.example.invalid/api/webhook/garage")))
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{edge: newFakeEdge(t)}
	seen, err := OpenSeen(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h.reader = New(Deps{
		Policy: pol, Messages: msgs, Seen: seen,
		Edge: &Edge{URL: h.edge.server.URL + "/h/test", Token: testToken},
		Webhook: func(_ context.Context, url string, payload map[string]string) error {
			h.hooks = append(h.hooks, payload)
			return h.hookErr
		},
	})
	return h
}

func TestAListedPersonsWordActsAndReplies(t *testing.T) {
	h := newHarness(t)
	out := h.reader.Handle(context.Background(), Message{ID: "m1", From: "15125550101", Body: " Garage "})
	if out.Result != "acted" || out.Word != "garage" || out.Person != "gabi" {
		t.Fatalf("outcome = %+v", out)
	}
	if len(h.hooks) != 1 || h.hooks[0]["word"] != "garage" || h.hooks[0]["person"] != "gabi" || h.hooks[0]["id"] != "m1" {
		t.Fatalf("webhook payload = %v", h.hooks)
	}
	if len(h.edge.sent) != 1 || h.edge.sent[0]["message"] != "The garage is open" || h.edge.sent[0]["to"] != "+15125550101" {
		t.Fatalf("sent = %v", h.edge.sent)
	}
}

func TestAStrangerIsArchivedAndNeverAnswered(t *testing.T) {
	h := newHarness(t)
	out := h.reader.Handle(context.Background(), Message{ID: "m2", From: "15125550199", Body: "garage"})
	if out.Result != "unlisted" || len(h.hooks) != 0 || len(h.edge.sent) != 0 {
		t.Fatalf("a stranger must get nothing: %+v hooks=%d sent=%d", out, len(h.hooks), len(h.edge.sent))
	}
}

func TestAListedPersonMayNotSayEveryWord(t *testing.T) {
	h := newHarness(t)
	// Grandma is on the allow-list but not on garage's list.
	out := h.reader.Handle(context.Background(), Message{ID: "m3", From: "15125550102", Body: "garage"})
	if out.Result != "not-allowed" || len(h.hooks) != 0 || len(h.edge.sent) != 0 {
		t.Fatalf("not-allowed must be silent: %+v", out)
	}
	// But "*" words are hers.
	out = h.reader.Handle(context.Background(), Message{ID: "m4", From: "15125550102", Body: "PING?"})
	if out.Result != "replied" || len(h.edge.sent) != 1 || h.edge.sent[0]["message"] != "pong" {
		t.Fatalf("ping = %+v sent=%v", out, h.edge.sent)
	}
}

func TestAnUnknownWordFromAListedPersonGetsTheList(t *testing.T) {
	h := newHarness(t)
	out := h.reader.Handle(context.Background(), Message{ID: "m5", From: "15125550101", Body: "open the pod bay doors"})
	if out.Result != "unknown-word" || out.Word != "" {
		t.Fatalf("outcome = %+v (the typed text must not be recorded)", out)
	}
	if len(h.edge.sent) != 1 || h.edge.sent[0]["message"] != "I know these words: garage, ping" {
		t.Fatalf("sent = %v", h.edge.sent)
	}
}

func TestTheSameIdActsOnce(t *testing.T) {
	h := newHarness(t)
	first := h.reader.Handle(context.Background(), Message{ID: "m6", From: "15125550101", Body: "garage"})
	second := h.reader.Handle(context.Background(), Message{ID: "m6", From: "15125550101", Body: "garage"})
	if first.Result != "acted" || second.Result != "duplicate" || len(h.hooks) != 1 {
		t.Fatalf("first=%+v second=%+v hooks=%d", first, second, len(h.hooks))
	}
	// And across a restart: the set is on disk.
	seen2, err := OpenSeen(h.reader.d.Seen.path[:len(h.reader.d.Seen.path)-len("/seen")])
	if err != nil || seen2.Mark("m6") {
		t.Fatal("seen ids must survive a restart")
	}
}

func TestAFailedWebhookIsAFailureAndSendsNoReply(t *testing.T) {
	h := newHarness(t)
	h.hookErr = context.DeadlineExceeded
	out := h.reader.Handle(context.Background(), Message{ID: "m7", From: "15125550101", Body: "garage"})
	if out.Result != "failed" || len(h.edge.sent) != 0 {
		t.Fatalf("a door that did not open must not be reported open: %+v sent=%d", out, len(h.edge.sent))
	}
}

func TestTheEdgeIsSpokenToWithTheTokenInAHeaderNeverTheURL(t *testing.T) {
	h := newHarness(t)
	h.edge.push(Message{ID: "m8", From: "15125550101", Body: "ping", ReceivedAt: time.Now()})
	ctx, cancel := context.WithCancel(context.Background())
	var outcomes []Outcome
	go h.reader.Run(ctx, 0, func(_ Message, o Outcome) { outcomes = append(outcomes, o); cancel() }, nil)
	<-ctx.Done()
	time.Sleep(50 * time.Millisecond)
	h.edge.mu.Lock()
	defer h.edge.mu.Unlock()
	if len(outcomes) != 1 || outcomes[0].Result != "replied" {
		t.Fatalf("outcomes = %+v", outcomes)
	}
	if len(h.edge.acked) != 1 || h.edge.acked[0] != "m8" {
		t.Fatalf("handled texts must be acked: %v", h.edge.acked)
	}
	for _, a := range h.edge.auths {
		if a != "Bearer "+testToken {
			t.Fatalf("every request carries the token as a bearer header, got %q", a)
		}
	}
}

func TestARudeReplyIsRefusedBeforeItLeaves(t *testing.T) {
	e := &Edge{URL: "http://edge.example.invalid/h/test", Token: testToken}
	if err := e.Send(context.Background(), "+15125550101", "Door 1 open!"); err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Fatalf("err = %v", err)
	}
}
