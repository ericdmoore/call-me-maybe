package notify_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"callmemaybe/internal/calls"
	"callmemaybe/internal/notify"
)

// receiver is a stand-in Home Assistant: it records what arrived so a test can
// assert on the wire format rather than on our own structs.
type receiver struct {
	*httptest.Server

	mu      sync.Mutex
	bodies  []map[string]any
	headers []http.Header

	status int // 0 means 200
	hold   chan struct{}
}

func newReceiver(t *testing.T) *receiver {
	t.Helper()
	r := &receiver{}
	r.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if r.hold != nil {
			<-r.hold
		}
		body, _ := io.ReadAll(req.Body)
		var m map[string]any
		_ = json.Unmarshal(body, &m)

		r.mu.Lock()
		r.bodies = append(r.bodies, m)
		r.headers = append(r.headers, req.Header.Clone())
		status := r.status
		r.mu.Unlock()

		if req.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", req.Method)
		}
		if status != 0 {
			w.WriteHeader(status)
		}
	}))
	t.Cleanup(r.Close)
	return r
}

func (r *receiver) got() []map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]map[string]any(nil), r.bodies...)
}

func open(t *testing.T, o notify.Options) *notify.Webhook {
	t.Helper()
	w, err := notify.Open(o)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func record() calls.Record {
	return calls.Record{
		ID:      "ch1",
		Start:   time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC),
		MS:      4200,
		Caller:  "+15125550100",
		Known:   "Grandma",
		Outcome: calls.OutcomeAnswered,
	}
}

func TestBothEventsReachTheEndpointInOrder(t *testing.T) {
	rec := newReceiver(t)
	w := open(t, notify.Options{URL: rec.URL})

	at := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	w.Post(notify.FromRecord(notify.EventRinging, at, record()))
	w.Post(notify.FromRecord(notify.EventCompleted, at, record()))
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	got := rec.got()
	if len(got) != 2 {
		t.Fatalf("endpoint saw %d posts, want 2", len(got))
	}
	// Order is the property one goroutine buys us: an announcement automation
	// that saw "completed" before "ringing" would say the wrong thing.
	if got[0]["event"] != notify.EventRinging || got[1]["event"] != notify.EventCompleted {
		t.Errorf("events arrived as %v then %v", got[0]["event"], got[1]["event"])
	}
	if got[0]["known"] != "Grandma" {
		t.Errorf("known = %v, want the allow-list name — it is the whole point of the ringing event", got[0]["known"])
	}
	if got[1]["outcome"] != calls.OutcomeAnswered {
		t.Errorf("outcome = %v, want answered", got[1]["outcome"])
	}
	if ct := rec.headers[0].Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type = %q", ct)
	}
}

// Nothing has happened yet when the phone starts ringing, and the record's
// Outcome already reads "abandoned" because that is its zero value. Shipping
// that would announce the opposite of what is going on.
func TestARingingEventCarriesNoOutcome(t *testing.T) {
	e := notify.FromRecord(notify.EventRinging, time.Now(), record())
	if e.Outcome != "" || e.MS != 0 || e.AnsweredBy != "" || e.Mailbox != "" {
		t.Errorf("ringing event carries outcome fields: %+v", e)
	}
	if e.Known != "Grandma" || e.CallID != "ch1" {
		t.Errorf("ringing event lost its identity: %+v", e)
	}
}

// Which number was rung is exactly what an announcement wants to route on —
// the business line on the office speaker, the house line everywhere — so it
// rides on the ringing event, not only the completed one.
func TestTheLineReachesTheEndpointOnBothEvents(t *testing.T) {
	rec := newReceiver(t)
	w := open(t, notify.Options{URL: rec.URL})

	r := record()
	r.Line = "biz"
	at := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	w.Post(notify.FromRecord(notify.EventRinging, at, r))
	w.Post(notify.FromRecord(notify.EventCompleted, at, r))
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	for i, got := range rec.got() {
		if got["line"] != "biz" {
			t.Errorf("event %d: line = %v, want biz", i, got["line"])
		}
	}
}

// A box answering one number sends exactly the payload it sent before lines
// existed. An absent line means the default one, the same as everywhere else.
func TestTheDefaultLineAddsNothingToThePayload(t *testing.T) {
	b, err := json.Marshal(notify.FromRecord(notify.EventCompleted, time.Now(), record()))
	if err != nil {
		t.Fatal(err)
	}
	for _, absent := range []string{"line", "direction"} {
		if strings.Contains(string(b), `"`+absent+`"`) {
			t.Errorf("a default-line inbound payload carries %q: %s", absent, b)
		}
	}
}

// FromRecord is a projection, so a field the record grows must not be silently
// dropped on the way out. Nothing posts an outbound event today — the console
// has no notifier — but the projection is what would carry it.
func TestFromRecordDropsNoField(t *testing.T) {
	r := record()
	r.Line, r.Direction = "biz", calls.DirectionOutbound
	e := notify.FromRecord(notify.EventCompleted, time.Now(), r)
	if e.Line != "biz" || e.Direction != calls.DirectionOutbound {
		t.Errorf("line = %q direction = %q, want the record's own", e.Line, e.Direction)
	}
}

// ── redaction ───────────────────────────────────────────────────────────

// The destination is another program on the network, so the default is the
// opposite of the call log's.
func TestTheCallerIsRedactedByDefault(t *testing.T) {
	rec := newReceiver(t)
	w := open(t, notify.Options{URL: rec.URL})
	w.Post(notify.FromRecord(notify.EventCompleted, time.Now(), record()))
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	caller, _ := rec.got()[0]["caller"].(string)
	if strings.Contains(caller, "5550100") {
		t.Errorf("caller = %q — the subscriber digits went out by default", caller)
	}
	if caller == "" {
		t.Error("redaction should leave something identifiable, not nothing")
	}
}

func TestTheFullNumberGoesOutOnlyWhenAskedFor(t *testing.T) {
	rec := newReceiver(t)
	w := open(t, notify.Options{URL: rec.URL, SendFullCallerID: true})
	w.Post(notify.FromRecord(notify.EventCompleted, time.Now(), record()))
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	if caller := rec.got()[0]["caller"]; caller != "+15125550100" {
		t.Errorf("caller = %v, want the full E.164 once the operator opted in", caller)
	}
}

// The safe behaviour has to be what you get from a struct literal somebody
// forgot to finish.
func TestTheZeroOptionsRedact(t *testing.T) {
	e := notify.Event{Caller: "+15125550100"}.Redacted()
	if strings.Contains(e.Caller, "5550100") {
		t.Errorf("Redacted left the subscriber digits: %q", e.Caller)
	}
	var zero notify.Options
	if zero.SendFullCallerID {
		t.Error("the zero Options must not send full caller IDs")
	}
}

// A near-miss is almost a credential, so there is nowhere in the payload for
// one to go. This asserts the shape of the type: if somebody adds a field for
// entered digits, it fails.
func TestNoFieldCanCarryEnteredDigits(t *testing.T) {
	b, err := json.Marshal(notify.Event{
		Type: notify.EventCompleted, Caller: "+15125550100",
		Extension: "Kids", PIN: "valid",
		// Every field added since, filled in, so a new one that could hold a
		// PIN fails here rather than in review.
		Line: "biz", Direction: calls.DirectionOutbound,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"digits", "pin_entered", "entered", "code", "secret"} {
		if strings.Contains(string(b), forbidden) {
			t.Errorf("payload has a %q field — entered digits must never be sendable: %s", forbidden, b)
		}
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if v, _ := m["pin"].(string); v != "valid" && v != "invalid" {
		t.Errorf("pin = %v, want a verdict", m["pin"])
	}
}

// ── the properties that keep this off the call path ─────────────────────

// Post must never block, whatever the endpoint is doing. If it can block, a
// wedged Home Assistant delays a call, which is the one thing this may not do.
func TestPostNeverBlocks(t *testing.T) {
	rec := newReceiver(t)
	rec.hold = make(chan struct{})
	w := open(t, notify.Options{URL: rec.URL, Timeout: 50 * time.Millisecond})
	t.Cleanup(func() { close(rec.hold); _ = w.Close() })

	done := make(chan struct{})
	go func() {
		defer close(done)
		// Far more than the buffer holds; the excess must be dropped, not
		// waited on.
		for range 10_000 {
			w.Post(notify.FromRecord(notify.EventCompleted, time.Now(), record()))
		}
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Post blocked — a full buffer must drop, not wait")
	}
	if w.Dropped() == 0 {
		t.Error("10,000 events through a 64-deep buffer dropped none — the counter is not wired up")
	}
}

// A dead endpoint is the expected state of a home lab. It must cost the caller
// nothing and must not be silent either.
func TestAnUnreachableEndpointIsCountedNotFatal(t *testing.T) {
	// Port 1 refuses immediately, so this stays fast.
	w := open(t, notify.Options{
		URL:     "http://127.0.0.1:1/api/webhook/abc",
		Timeout: 200 * time.Millisecond,
	})
	w.Post(notify.FromRecord(notify.EventCompleted, time.Now(), record()))
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if w.Failed() != 1 {
		t.Errorf("failed = %d, want 1 — a delivery that never happened must be counted", w.Failed())
	}
}

func TestARejectedEventIsCounted(t *testing.T) {
	rec := newReceiver(t)
	rec.status = http.StatusInternalServerError
	w := open(t, notify.Options{URL: rec.URL})
	w.Post(notify.FromRecord(notify.EventCompleted, time.Now(), record()))
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if w.Failed() != 1 {
		t.Errorf("failed = %d, want 1 for a 500", w.Failed())
	}
}

// The URL is a credential — a Home Assistant webhook id is the entire secret —
// and net/url helpfully embeds it in every transport error. It must not come
// back out through a warn line that journald then ships off the box.
func TestTheURLNeverReachesTheLog(t *testing.T) {
	var buf bytes.Buffer
	w := open(t, notify.Options{
		URL:     "http://127.0.0.1:1/api/webhook/supersecretwebhookid",
		Timeout: 200 * time.Millisecond,
		Log:     slog.New(slog.NewTextHandler(&buf, nil)),
	})
	w.Post(notify.FromRecord(notify.EventCompleted, time.Now(), record()))
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	out := buf.String()
	if strings.Contains(out, "supersecretwebhookid") {
		t.Errorf("the webhook id is in the log: %s", out)
	}
	if !strings.Contains(out, "webhook post failed") {
		t.Errorf("a failed delivery must be logged at warn; got: %s", out)
	}
	if !strings.Contains(out, "127.0.0.1:1") {
		t.Errorf("the host is what makes the warning actionable; got: %s", out)
	}
}

func TestPostOnANilWebhookIsSafe(t *testing.T) {
	var w *notify.Webhook // the webhook is off
	w.Post(notify.Event{Type: notify.EventRinging})
	if w.Host() != "" {
		t.Error("a nil webhook has no host")
	}
	if err := w.Close(); err != nil {
		t.Error(err)
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	rec := newReceiver(t)
	w := open(t, notify.Options{URL: rec.URL})
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	// And posting afterwards must not panic on a closed channel.
	w.Post(notify.Event{Type: notify.EventRinging})
}

func TestConcurrentPostersAreSafe(t *testing.T) {
	rec := newReceiver(t)
	w := open(t, notify.Options{URL: rec.URL})

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 4 {
				w.Post(notify.FromRecord(notify.EventCompleted, time.Now(), record()))
			}
		}()
	}
	wg.Wait()
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if len(rec.got()) == 0 {
		t.Error("nothing arrived")
	}
}

// ── configuration ───────────────────────────────────────────────────────

func TestTokenBecomesABearerHeader(t *testing.T) {
	rec := newReceiver(t)
	w := open(t, notify.Options{URL: rec.URL, Token: "t0k3n"})
	w.Post(notify.Event{Type: notify.EventRinging})
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if got := rec.headers[0].Get("Authorization"); got != "Bearer t0k3n" {
		t.Errorf("authorization = %q", got)
	}

	// And no header at all when there is no token — Home Assistant's webhook
	// endpoints reject some requests that carry one.
	rec2 := newReceiver(t)
	w2 := open(t, notify.Options{URL: rec2.URL})
	w2.Post(notify.Event{Type: notify.EventRinging})
	if err := w2.Close(); err != nil {
		t.Fatal(err)
	}
	if got := rec2.headers[0].Get("Authorization"); got != "" {
		t.Errorf("authorization = %q, want none", got)
	}
}

func TestOpenRejectsAUnusableURL(t *testing.T) {
	for _, bad := range []string{
		"",
		"not a url",
		"ftp://ha.local/hook",
		"http://",
		"/api/webhook/abc",
	} {
		if w, err := notify.Open(notify.Options{URL: bad}); err == nil {
			_ = w.Close()
			t.Errorf("Open(%q) succeeded; a URL that cannot work must fail at startup", bad)
		}
	}
}

func TestTimeoutDefaultsRatherThanBlockingForever(t *testing.T) {
	rec := newReceiver(t)
	w := open(t, notify.Options{URL: rec.URL, Timeout: 0})
	defer func() { _ = w.Close() }()
	if notify.DefaultTimeout <= 0 || notify.DefaultTimeout > 10*time.Second {
		t.Errorf("DefaultTimeout = %v; this bounds shutdown as well as a post", notify.DefaultTimeout)
	}
}
