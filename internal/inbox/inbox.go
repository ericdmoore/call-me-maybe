// Package inbox consumes the house's edge inbox: texts to the house number,
// pulled from the edge worker over HTTPS, matched against messages.toml,
// acted on, and answered through the same edge.
//
// It is reachable from `doorman inbox` and from nothing else: nothing on the
// call path reads a text, and the daemon never holds the inbox token.
//
// The edge holds the carrier's API key; this package never sees it. What
// this box holds is one token scoped to pulling this house's texts and
// sending its replies — revocable in one click, and unable to buy a number.
package inbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"callmemaybe/internal/policy"
)

// Message is one text as the edge hands it over.
type Message struct {
	ID         string    `json:"id"`
	From       string    `json:"from"`
	To         string    `json:"to"`
	Body       string    `json:"body"`
	ReceivedAt time.Time `json:"received_at"`
}

// Edge is the house's inbox at the edge.
type Edge struct {
	// URL is the house's base, e.g. https://edge.callmemaybe.cc/h/midbury.
	URL string
	// Token is the house's inbox token, sent as a bearer header, never in
	// the URL — so the URL is safe to print and an error is safe to log.
	Token  string
	Client *http.Client
}

func (e *Edge) client() *http.Client {
	if e.Client != nil {
		return e.Client
	}
	return &http.Client{Timeout: 45 * time.Second}
}

func (e *Edge) do(ctx context.Context, method, path string, body any, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(e.URL, "/")+path, rdr)
	if err != nil {
		return safeErr(err)
	}
	req.Header.Set("Authorization", "Bearer "+e.Token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := e.client().Do(req)
	if err != nil {
		return safeErr(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("edge answered HTTP %s", resp.Status)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out)
}

// Pull waits up to wait seconds at the edge for texts, returning what
// arrived, oldest first. An empty answer is normal.
func (e *Edge) Pull(ctx context.Context, wait time.Duration) ([]Message, error) {
	var out struct {
		Messages []Message `json:"messages"`
	}
	if err := e.do(ctx, http.MethodGet, fmt.Sprintf("/inbox/pull?wait=%d", int(wait.Seconds())), nil, &out); err != nil {
		return nil, err
	}
	sort.Slice(out.Messages, func(i, j int) bool { return out.Messages[i].ReceivedAt.Before(out.Messages[j].ReceivedAt) })
	return out.Messages, nil
}

// Ack tells the edge these are handled; it will not hand them over again.
func (e *Edge) Ack(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	return e.do(ctx, http.MethodPost, "/inbox/ack", map[string]any{"ids": ids}, nil)
}

// Send texts a reply from the house number, through the edge, which holds
// the carrier credential. The edge applies the same reply rule.
func (e *Edge) Send(ctx context.Context, to, body string) error {
	if why := policy.ReplyProblem(body); why != "" {
		return fmt.Errorf("refusing to send a reply that %s", why)
	}
	return e.do(ctx, http.MethodPost, "/inbox/send", map[string]string{"to": to, "message": body}, nil)
}

// safeErr strips the URL a *url.Error carries. The token is in a header
// and the URL is not a secret, but the operation and the cause are all a
// log line needs.
func safeErr(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return fmt.Errorf("%s: %w", ue.Op, ue.Err)
	}
	return err
}

// Seen is the set of message ids already handled — the reader's only
// state. VoIP.ms retries callbacks and the edge is at-least-once, so an id
// can arrive twice; a door opens once.
type Seen struct {
	path string
	mu   sync.Mutex
	ids  map[string]bool
	list []string
}

const seenKeep = 10000

// OpenSeen loads the set from dir/seen, creating the directory 0700.
func OpenSeen(dir string) (*Seen, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	s := &Seen{path: filepath.Join(dir, "seen"), ids: map[string]bool{}}
	b, err := os.ReadFile(s.path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	for _, id := range strings.Split(string(b), "\n") {
		if id = strings.TrimSpace(id); id != "" && !s.ids[id] {
			s.ids[id] = true
			s.list = append(s.list, id)
		}
	}
	return s, nil
}

// Mark records an id; it reports whether it was new.
func (s *Seen) Mark(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ids[id] {
		return false
	}
	s.ids[id] = true
	s.list = append(s.list, id)
	if len(s.list) > seenKeep {
		for _, old := range s.list[:len(s.list)-seenKeep] {
			delete(s.ids, old)
		}
		s.list = append([]string(nil), s.list[len(s.list)-seenKeep:]...)
	}
	_ = os.WriteFile(s.path+".tmp", []byte(strings.Join(s.list, "\n")+"\n"), 0o600)
	_ = os.Rename(s.path+".tmp", s.path)
	return true
}

// Outcome is what the reader did with one text — for the log and the
// journal. Never the body, never the full number.
type Outcome struct {
	ID string
	// Result: acted, replied, unlisted, unknown-word, not-allowed,
	// duplicate, refused, failed.
	Result string
	Word   string
	Person string // [[people]] id or name, when the sender is on the list
	Detail string
}

// Deps is what the reader needs from the house.
type Deps struct {
	Policy   *policy.Policy
	Messages *policy.Messages
	Edge     *Edge
	Seen     *Seen
	// Webhook posts a word's payload; nil means net/http with a timeout.
	Webhook func(ctx context.Context, url string, payload map[string]string) error
	// CountryCode normalises the sender's number.
	CountryCode string
	Now         func() time.Time
}

// Reader is the loop.
type Reader struct {
	d Deps
}

func New(d Deps) *Reader {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.CountryCode == "" {
		d.CountryCode = "1"
	}
	if d.Webhook == nil {
		d.Webhook = postJSON
	}
	return &Reader{d: d}
}

func postJSON(ctx context.Context, target string, payload map[string]string) error {
	b, _ := json.Marshal(payload)
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(b))
	if err != nil {
		return safeErr(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return safeErr(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("webhook answered HTTP %s", resp.Status)
	}
	return nil
}

// Handle decides and acts on one text. Every path returns an Outcome; only
// "acted"/"replied" touch anything.
func (r *Reader) Handle(ctx context.Context, m Message) Outcome {
	out := Outcome{ID: m.ID}
	if r.d.Seen != nil && !r.d.Seen.Mark(m.ID) {
		out.Result = "duplicate"
		return out
	}
	n := policy.NormaliseCallerID(m.From, r.d.CountryCode)
	if n.Kind != policy.KindE164 {
		out.Result = "unlisted"
		out.Detail = "sender is not a phone number"
		return out
	}
	caller, ok := r.d.Policy.LookupCaller(n.Value)
	if !ok {
		// Archived by the carrier's email path; never answered, because a
		// reply tells a stranger the number is live.
		out.Result = "unlisted"
		return out
	}
	out.Person = caller.ID
	if out.Person == "" {
		out.Person = caller.Name
	}
	text := strings.ToLower(strings.TrimSpace(m.Body))
	text = strings.TrimRight(text, ".?! ")
	word, found := r.d.Messages.Lookup(text)
	if !found {
		out.Result = "unknown-word"
		out.Word = "" // never log what they typed
		// A listed person gets the list, once per text.
		reply := "I know these words: " + strings.Join(r.allowedWords(caller), ", ")
		if err := r.d.Edge.Send(ctx, n.Value, reply); err != nil {
			out.Detail = "reply failed: " + err.Error()
		}
		return out
	}
	out.Word = word.Word
	if !allowed(word, caller) {
		out.Result = "not-allowed"
		return out
	}
	if word.Webhook != "" {
		payload := map[string]string{"word": word.Word, "person": out.Person, "id": m.ID}
		if err := r.d.Webhook(ctx, word.Webhook, payload); err != nil {
			out.Result = "failed"
			out.Detail = "webhook: " + err.Error()
			return out
		}
		out.Result = "acted"
	} else {
		out.Result = "replied"
	}
	if word.Reply != "" {
		if err := r.d.Edge.Send(ctx, n.Value, word.Reply); err != nil {
			out.Detail = "reply failed: " + err.Error()
		}
	}
	return out
}

func allowed(w policy.Word, c policy.KnownCaller) bool {
	for _, id := range w.People {
		if id == "*" || (c.ID != "" && id == c.ID) {
			return true
		}
	}
	return false
}

func (r *Reader) allowedWords(c policy.KnownCaller) []string {
	var out []string
	for _, w := range r.d.Messages.Words() {
		if allowed(w, c) {
			out = append(out, w.Word)
		}
	}
	if len(out) == 0 {
		out = []string{"nothing yet"}
	}
	return out
}

// PullOnce pulls once, handles and acks what arrived, and reports how many.
func (r *Reader) PullOnce(ctx context.Context, wait time.Duration, onOutcome func(Message, Outcome)) (int, error) {
	msgs, err := r.d.Edge.Pull(ctx, wait)
	if err != nil {
		return 0, err
	}
	var done []string
	for _, m := range msgs {
		o := r.Handle(ctx, m)
		if onOutcome != nil {
			onOutcome(m, o)
		}
		done = append(done, m.ID)
	}
	if err := r.d.Edge.Ack(ctx, done); err != nil {
		return len(msgs), fmt.Errorf("ack: %w", err)
	}
	return len(msgs), nil
}

// Run pulls until ctx is done, calling onOutcome for every text and
// acking what it handled. A pull that fails backs off and tries again;
// nothing here ever stops the phone.
func (r *Reader) Run(ctx context.Context, wait time.Duration, onOutcome func(Message, Outcome), onError func(error)) {
	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return
		}
		msgs, err := r.d.Edge.Pull(ctx, wait)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if onError != nil {
				onError(err)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			if backoff < time.Minute {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second
		var done []string
		for _, m := range msgs {
			o := r.Handle(ctx, m)
			if onOutcome != nil {
				onOutcome(m, o)
			}
			done = append(done, m.ID)
		}
		// Acked on its own clock: a text that was handled is handled even
		// if Ctrl-C arrived while it was, and the seen set covers the rest.
		ackCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err = r.d.Edge.Ack(ackCtx, done)
		cancel()
		if err != nil && onError != nil {
			onError(fmt.Errorf("ack: %w", err))
		}
	}
}
