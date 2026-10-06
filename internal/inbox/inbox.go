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
	Source       string    `json:"source,omitempty"`
	Phonebooks   []string  `json:"phonebooks,omitempty"`
	ID           string    `json:"id"`
	From         string    `json:"from"`
	To           string    `json:"to"`
	Body         string    `json:"body"`
	ReceivedAt   time.Time `json:"received_at"`
	MediaCount   int       `json:"media_count,omitempty"`
	Contacts     []Contact `json:"contacts,omitempty"`
	ContactError string    `json:"contact_error,omitempty"`
}

// Contact is structured data extracted by the Worker, never a raw attachment.
type Contact struct {
	Name   string `json:"name"`
	Number string `json:"number"`
}

func (m Message) hasContacts() bool {
	return m.MediaCount != 0 || len(m.Contacts) != 0 || m.ContactError != ""
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
	client := *e.client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
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
	if err := e.do(ctx, http.MethodGet, fmt.Sprintf("/inbox/pull?wait=%d&contact_uploads=1", int(wait.Seconds())), nil, &out); err != nil {
		return nil, err
	}
	sort.Slice(out.Messages, func(i, j int) bool { return out.Messages[i].ReceivedAt.Before(out.Messages[j].ReceivedAt) })
	return out.Messages, nil
}

// Ack tells the edge these are handled; it will not hand them over again.
func (e *Edge) Ack(ctx context.Context, ids []string) error {
	return e.ackContacts(ctx, ids, nil)
}

func (e *Edge) ackContacts(ctx context.Context, ids []string, results []uploadResult) error {
	if len(ids) == 0 {
		return nil
	}
	body := map[string]any{"ids": ids}
	if len(results) > 0 {
		body["results"] = results
	}
	return e.do(ctx, http.MethodPost, "/inbox/ack", body, nil)
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
// deduplication state. VoIP.ms retries callbacks and the edge is at-least-once, so an id
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

// Has checks for a completed import without consuming it before the file
// is saved. Contact imports are idempotent, so a crash can safely retry.
func (s *Seen) Has(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ids[id]
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
	// Result: pending, cancelled, acted, replied, asked, unchanged, unlisted, unknown-word,
	// not-allowed, needs-confirmation, duplicate, failed.
	Result   string
	Retry    bool // pending state could not be persisted; do not mark or ack
	Deferred bool // a shelf timer outcome, not a new inbound message
	Word     string
	Action   string // the [[actions]] id the word performed, when it named one
	Person   string // [[people]] id or name, when the sender is on the list
	Detail   string
	// To is the sender in E.164 once parsed. Reply is the SMS sent back,
	// or the result message returned to an authenticated Shortcut receipt.
	To    string
	Reply string
	// State is what Home Assistant reported when it was asked — the answer
	// to "garage?" (asked), or the reason nothing moved (unchanged).
	State string
	// Via is the transport, for the journal: sms or shortcut.
	Via string
}

// State is one reading of a Home Assistant entity.
type State struct {
	// Value is HA's state string: open, closed, on, off, unavailable…
	Value string
	// Name is HA's friendly_name, or the entity id when it has none —
	// what the house calls the thing when it answers.
	Name string
}

// HomeAssistant reads entity state with a long-lived token — LAN
// infrastructure like the ARI password, not a provider key. The token
// rides in a header, never in the URL, so an error is safe to log.
type HomeAssistant struct {
	URL    string
	Token  string
	Client *http.Client
}

func (h *HomeAssistant) State(ctx context.Context, entity string) (State, error) {
	client := h.Client
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(h.URL, "/")+"/api/states/"+url.PathEscape(entity), nil)
	if err != nil {
		return State{}, safeErr(err)
	}
	req.Header.Set("Authorization", "Bearer "+h.Token)
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return State{}, safeErr(err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return State{}, fmt.Errorf("home assistant has no entity %s", entity)
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return State{}, errors.New("home assistant refused the token (HA_TOKEN)")
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		return State{}, fmt.Errorf("home assistant answered HTTP %s", resp.Status)
	}
	var body struct {
		State      string `json:"state"`
		Attributes struct {
			FriendlyName string `json:"friendly_name"`
		} `json:"attributes"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return State{}, fmt.Errorf("home assistant: %w", err)
	}
	st := State{Value: strings.TrimSpace(body.State), Name: strings.TrimSpace(body.Attributes.FriendlyName)}
	if st.Value == "" {
		return State{}, errors.New("home assistant reported no state")
	}
	if st.Name == "" {
		st.Name = entity
	}
	return st, nil
}

// Deps is what the reader needs from the house.
type Deps struct {
	Shelf    *ContactShelf // durable split-card conversations; nil disables delayed imports
	Policy   *policy.Policy
	Messages *policy.Messages
	Edge     *Edge
	Seen     *Seen
	// Webhook posts a word's payload; nil means net/http with a timeout.
	Webhook func(ctx context.Context, url string, payload map[string]string) error
	// State reads a Home Assistant entity, for the actions that name one
	// (`garage?`, done_when). nil means no HA_URL/HA_TOKEN: a "?" is then
	// punctuation and done_when is never checked.
	State func(ctx context.Context, entity string) (State, error)
	// CountryCode normalises the sender's number.
	CountryCode string
	// Phonebooks is the directory for received vCards, separate from *88.
	// Empty disables imports (the CLI supplies PHONEBOOK_DIR/shared).
	Phonebooks string
	Now        func() time.Time
}

// Reader is the loop.
type Reader struct {
	deferredContacts []contactOutcome
	// saveAttempts counts phone book writes that failed, per message id, so
	// a storage fault answers the sender after a few tries instead of holding
	// the queue. In memory on purpose: a restart is a fresh set of tries.
	saveAttempts map[string]int
	d            Deps
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
	return &Reader{d: d, saveAttempts: map[string]int{}}
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
// pending contacts are persisted before acknowledgement.
func (r *Reader) Handle(ctx context.Context, m Message) (out Outcome) {
	out = Outcome{ID: m.ID, Via: "sms"}
	if m.Source == "contact-upload" {
		out.Via = "shortcut"
	}
	// Upload imports are idempotent file writes. Replay them until the edge
	// acknowledges the result, so a crash cannot strand a receipt as queued.
	if r.d.Seen != nil && m.Source != "contact-upload" {
		if r.isContactCommand(m) {
			if r.d.Seen.Has(m.ID) {
				out.Result = "duplicate"
				return out
			}
			defer func() {
				if !out.Retry {
					r.d.Seen.Mark(m.ID)
				}
			}()
		} else if !r.d.Seen.Mark(m.ID) {
			out.Result = "duplicate"
			return out
		}
	}
	n := policy.NormaliseCallerID(m.From, r.d.CountryCode)
	if n.Kind != policy.KindE164 {
		out.Result = "unlisted"
		out.Detail = "sender is not a phone number"
		return out
	}
	out.To = n.Value
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
	if m.Source == "contact-upload" {
		return r.importUpload(ctx, m, out, caller)
	}
	text := strings.ToLower(strings.TrimSpace(m.Body))
	if text == "" && !m.hasContacts() {
		out.Result = "failed"
		out.Detail = "empty message; no attachment received"
		r.reply(ctx, &out, n.Value, "An empty message reached the house. No attachment was included. If you shared a contact, I did not receive its vCard.")
		return out
	}
	if r.d.Shelf != nil {
		if text == "" && m.hasContacts() {
			return r.shelveContacts(ctx, m, out, caller)
		}
		if text == "cancel" && !m.hasContacts() {
			if cancelled, ok := r.cancelContacts(ctx, m, out); ok {
				return cancelled
			}
		}
	}
	if prefix, destinations, ok := strings.Cut(text, ":"); ok && strings.Join(strings.Fields(prefix), " ") == "add to" {
		return r.routeContacts(ctx, m, out, destinations, caller)
	}
	// "garage?" is a question when the action can be asked; the "?" is
	// read before the punctuation is stripped, and is punctuation again on
	// a word that has no state to report — "ping?" still answers pong.
	asked := strings.HasSuffix(strings.TrimRight(text, ".! "), "?")
	text = strings.TrimRight(text, ".?! ")
	word, found := r.d.Messages.Lookup(text)
	if !found {
		out.Result = "unknown-word"
		out.Word = "" // never log what they typed
		// A listed person gets the list, once per text.
		reply := "I know these words: " + strings.Join(r.allowedWords(caller), ", ")
		if err := r.d.Edge.Send(ctx, n.Value, reply); err != nil {
			out.Detail = "reply failed: " + err.Error()
		} else {
			out.Reply = reply
		}
		return out
	}
	out.Word = word.Word
	if !allowed(word.People, caller) {
		if word.Phonebook != "" {
			if err := r.stopContactDefault(m, out); err != nil {
				return retryContacts(out, err)
			}
		}
		out.Result = "not-allowed"
		return out
	}
	if word.Phonebook != "" {
		return r.routeContacts(ctx, m, out, word.Word, caller)
	}
	// An attachment caption must not accidentally operate a door.
	if m.hasContacts() {
		out.Result = "failed"
		r.reply(ctx, &out, n.Value, "That word does not accept attachments. Use a phone book word with your contact card.")
		return out
	}
	// What the word does: its action's webhook and reply (s13), or — the
	// stopgap that shipped first — its own.
	webhook, reply, confirm := word.Webhook, word.Reply, policy.ConfirmNone
	if word.Action != "" {
		action, ok := r.d.Policy.LookupAction(word.Action)
		if !ok {
			out.Result = "failed"
			out.Detail = "action " + word.Action + " is not in policy.toml"
			return out
		}
		out.Action = action.ID
		// The action's people are the people; a word may only narrow.
		if !allowed(action.People, caller) {
			out.Result = "not-allowed"
			return out
		}
		webhook, reply, confirm = action.Webhook, action.Reply, action.Confirm
		if reply == "" {
			reply = word.Reply
		}
		if action.State != "" && r.d.State != nil {
			if asked {
				// A question moves nothing and needs no passkey: reading
				// a door is less than opening it.
				return r.answerState(ctx, out, n.Value, action)
			}
			if action.DoneWhen != "" {
				st, err := r.d.State(ctx, action.State)
				if err == nil && st.Value == action.DoneWhen {
					// Already there: say so, and never call the webhook —
					// HA would do nothing, but "asked the garage to open"
					// would be a lie about a door.
					out.Result = "unchanged"
					out.State = st.Value
					r.reply(ctx, &out, n.Value, "It was already "+st.Value)
					return out
				}
				if err != nil {
					// HA could not be read; the webhook decides, as it
					// would without done_when, and the reason is kept.
					out.Detail = "state: " + err.Error()
				}
			}
		}
	}
	if confirm == policy.ConfirmPasskey {
		// Intent, not authority (s19): nothing moves on a text alone. Until
		// the passkey door exists, say so and do nothing.
		out.Result = "needs-confirmation"
		const notYet = "That one needs your passkey, which is not set up yet"
		if r.d.Edge.Send(ctx, n.Value, notYet) == nil {
			out.Reply = notYet
		}
		return out
	}
	if webhook != "" {
		payload := map[string]string{"word": word.Word, "action": out.Action, "person": out.Person, "via": "sms", "id": m.ID}
		if err := r.d.Webhook(ctx, webhook, payload); err != nil {
			out.Result = "failed"
			out.Detail = "webhook: " + err.Error()
			return out
		}
		out.Result = "acted"
	} else {
		out.Result = "replied"
	}
	if reply != "" {
		r.reply(ctx, &out, n.Value, reply)
	}
	return out
}

// reply sends and records; a failed send is a detail, never a different
// outcome, because whatever the word did has been done.
func (r *Reader) reply(ctx context.Context, out *Outcome, to, text string) {
	if out.Via == "shortcut" {
		out.Reply = text
		return
	}
	if err := r.d.Edge.Send(ctx, to, text); err != nil {
		if out.Detail != "" {
			out.Detail += "; "
		}
		out.Detail += "reply failed: " + err.Error()
		return
	}
	out.Reply = text
}

// answerState is the question form: what HA says, in HA's own name for the
// thing, and nothing moved. A read that fails says so rather than guessing
// — a lie about a door is worse than an unanswered question.
func (r *Reader) answerState(ctx context.Context, out Outcome, to string, action policy.Action) Outcome {
	st, err := r.d.State(ctx, action.State)
	if err != nil {
		out.Result = "failed"
		out.Detail = "state: " + err.Error()
		r.reply(ctx, &out, to, "I could not check, Home Assistant did not answer")
		return out
	}
	out.Result = "asked"
	out.State = st.Value
	r.reply(ctx, &out, to, st.Name+" is "+st.Value)
	return out
}

func allowed(people []string, c policy.KnownCaller) bool {
	for _, id := range people {
		if id == "*" || (c.ID != "" && id == c.ID) {
			return true
		}
	}
	return false
}

func (r *Reader) allowedWords(c policy.KnownCaller) []string {
	var out []string
	for _, w := range r.d.Messages.Words() {
		if !allowed(w.People, c) {
			continue
		}
		if w.Action != "" {
			if a, ok := r.d.Policy.LookupAction(w.Action); !ok || !allowed(a.People, c) {
				continue
			}
		}
		out = append(out, w.Word)
	}
	if len(out) == 0 {
		out = []string{"nothing yet"}
	}
	return out
}

// PullOnce pulls once, handles and acks what arrived, and reports how many.
func (r *Reader) PullOnce(ctx context.Context, wait time.Duration, onOutcome func(Message, Outcome)) (int, error) {
	msgs, err := r.d.Edge.Pull(ctx, r.contactWait(wait))
	if err != nil {
		return 0, err
	}
	var done []string
	var results []uploadResult
	retry := false
	for _, m := range msgs {
		o := r.Handle(ctx, m)
		retry = retry || o.Retry
		if onOutcome != nil {
			onOutcome(m, o)
		}
		if !o.Retry {
			done = append(done, m.ID)
			if result, ok := uploadResultFor(m, o); ok {
				results = append(results, result)
			}
		}
	}
	retry = r.advanceContacts(ctx, onOutcome, len(msgs) < 10 && !retry) || retry
	if err := r.d.Edge.ackContacts(ctx, done, results); err != nil {
		return len(msgs), fmt.Errorf("ack: %w", err)
	}
	if retry {
		return len(msgs), errors.New("contact work remains pending for retry")
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
		msgs, err := r.d.Edge.Pull(ctx, r.contactWait(wait))
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
		var results []uploadResult
		retry := false
		for _, m := range msgs {
			o := r.Handle(ctx, m)
			retry = retry || o.Retry
			if onOutcome != nil {
				onOutcome(m, o)
			}
			if !o.Retry {
				done = append(done, m.ID)
				if result, ok := uploadResultFor(m, o); ok {
					results = append(results, result)
				}
			}
		}
		retry = r.advanceContacts(ctx, onOutcome, len(msgs) < 10 && !retry) || retry
		// Acked on its own clock: a text that was handled is handled even
		// if Ctrl-C arrived while it was, and the seen set covers the rest.
		ackCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err = r.d.Edge.ackContacts(ackCtx, done, results)
		cancel()
		if err != nil && onError != nil {
			onError(fmt.Errorf("ack: %w", err))
		}
		if retry {
			// Failed local persistence must not spin on an overdue shelf timer
			// or repeatedly pull an unacknowledged contact command.
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
		}
	}
}
