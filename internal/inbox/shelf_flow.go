package inbox

import (
	"context"
	"errors"
	"strings"
	"time"

	"callmemaybe/internal/policy"
)

const noReadyCard = "There is no vCard ready to save. Please resend one, quickly followed by instructions."

type contactOutcome struct {
	message Message
	outcome Outcome
}

func (r *Reader) isContactCommand(m Message) bool {
	if m.hasContacts() {
		return true
	}
	text := strings.ToLower(strings.TrimSpace(m.Body))
	if prefix, _, ok := strings.Cut(text, ":"); ok && strings.Join(strings.Fields(prefix), " ") == "add to" {
		return true
	}
	if text == "cancel" && r.d.Shelf != nil {
		return true
	}
	word, ok := r.d.Messages.Lookup(strings.TrimRight(text, ".!? "))
	return ok && word.Phonebook != ""
}

func retryContacts(out Outcome, err error) Outcome {
	out.Result = "failed"
	out.Detail = err.Error()
	out.Retry = true
	return out
}

func (r *Reader) contactKey(m Message, out Outcome) (string, string, error) {
	to := ""
	if m.To != "" {
		n := policy.NormaliseCallerID(m.To, r.d.CountryCode)
		if n.Kind != policy.KindE164 {
			return "", "", errors.New("invalid contact recipient")
		}
		to = n.Value
	}
	return shelfKey(out.To, to), to, nil
}

func (r *Reader) pendingContacts(ctx context.Context, m Message, out Outcome) (string, contactBatch, bool, error) {
	key, _, err := r.contactKey(m, out)
	if err != nil {
		return "", contactBatch{}, false, err
	}
	b, ok := r.d.Shelf.batches[key]
	if !ok {
		return key, b, false, nil
	}
	arrived := m.ReceivedAt
	if arrived.IsZero() {
		arrived = r.d.Now()
	}
	if !arrived.Before(b.Deadline) {
		result := r.advanceContactBatch(ctx, key, b)
		if result != nil {
			r.deferredContacts = append(r.deferredContacts, *result)
			if result.outcome.Retry {
				return key, b, false, errors.New("could not finish previous contact batch")
			}
		}
		_, stillPending := r.d.Shelf.batches[key]
		if stillPending {
			return key, b, false, errors.New("previous contact batch is still pending")
		}
		return key, contactBatch{}, false, nil
	}
	return key, b, true, nil
}

func (r *Reader) stopContactDefault(m Message, out Outcome) error {
	if r.d.Shelf == nil {
		return nil
	}
	key, _, err := r.contactKey(m, out)
	if err != nil {
		return err
	}
	b, ok := r.d.Shelf.batches[key]
	if !ok {
		return nil
	}
	arrived := m.ReceivedAt
	if arrived.IsZero() {
		arrived = r.d.Now()
	}
	if !arrived.Before(b.Deadline) || (!b.Message.ReceivedAt.IsZero() && arrived.Before(b.Message.ReceivedAt)) || len(b.Words) > 0 {
		return nil
	}
	b.NoDefault = true
	b.DefaultWord = ""
	b.NoticeAt = r.d.Now()
	b.Deadline = r.d.Now().Add(contactChoice)
	return r.d.Shelf.put(key, b)
}

func (r *Reader) shelveContacts(ctx context.Context, m Message, out Outcome, caller policy.KnownCaller) Outcome {
	if !r.mayImportContacts(caller) {
		out.Result = "not-allowed"
		return out
	}
	if r.d.Phonebooks == "" {
		out.Result = "failed"
		r.reply(ctx, &out, out.To, "Contact imports are not set up yet.")
		return out
	}
	entries, problem := r.checkedContacts(m)
	if problem != nil {
		out.Result = "failed"
		out.Detail = problem.detail
		r.reply(ctx, &out, out.To, problem.reply)
		return out
	}
	key, b, exists, err := r.pendingContacts(ctx, m, out)
	if err != nil {
		return retryContacts(out, err)
	}
	if exists {
		for _, id := range b.IDs {
			if id == m.ID {
				out.Result = "duplicate"
				return out
			}
		}
		if len(b.Words) > 0 {
			return retryContacts(out, errors.New("previous contact selection must finish first"))
		}
	} else {
		_, to, _ := r.contactKey(m, out)
		b = contactBatch{Message: Message{ID: m.ID, From: out.To, To: to, ReceivedAt: m.ReceivedAt}, Deadline: r.d.Now().Add(15 * time.Minute)}
	}
	combined := append([]Contact(nil), b.Message.Contacts...)
	have := map[string]bool{}
	for _, c := range combined {
		have[c.Number] = true
	}
	for _, e := range entries {
		if !have[e.E164] {
			combined = append(combined, Contact{Name: e.Name, Number: e.E164})
			have[e.E164] = true
		}
	}
	if b.Message.MediaCount+m.MediaCount > 3 || len(combined) > 100 || len(b.IDs) >= 100 {
		out.Result = "failed"
		out.Detail = "pending contact limit"
		r.reply(ctx, &out, out.To, "Only three vCards and 100 numbers can wait at once. Send this card after the pending batch is saved or cancelled.")
		return out
	}
	b.Message.Contacts = combined
	b.Message.MediaCount += m.MediaCount
	b.IDs = append(append([]string(nil), b.IDs...), m.ID)
	b.QuietUntil = r.d.Now().Add(contactQuiet)
	// New cards restart the silence period, and must receive a fresh notice
	// before a fresh two-minute default window. An invalid explicit choice
	// still suppresses the default for the whole batch.
	if !b.NoticeAt.IsZero() {
		b.Deadline = r.d.Now().Add(15 * time.Minute)
	}
	b.NoticeAt = time.Time{}
	b.DefaultWord = ""
	if err := r.d.Shelf.put(key, b); err != nil {
		return retryContacts(out, err)
	}
	out.Result = "pending"
	out.Detail = "contact cards waiting for instructions"
	return out
}

func (r *Reader) selectContacts(ctx context.Context, m Message, out Outcome, words []policy.Word) Outcome {
	key, b, exists, err := r.pendingContacts(ctx, m, out)
	if err != nil {
		return retryContacts(out, err)
	}
	if !m.hasContacts() && !exists {
		out.Result = "failed"
		out.Detail = "no pending contact card"
		r.reply(ctx, &out, out.To, noReadyCard)
		return out
	}
	if m.hasContacts() {
		// A complete card+caption still imports immediately. With pending cards,
		// the explicit choice applies to the batch and the new card together.
		if !exists {
			return r.importContacts(ctx, m, out, words...)
		}
		_, problem := r.checkedContacts(m)
		if problem != nil {
			if err := r.stopContactDefault(m, out); err != nil {
				return retryContacts(out, err)
			}
			out.Result = "failed"
			out.Detail = problem.detail
			r.reply(ctx, &out, out.To, problem.reply)
			return out
		}
		m.Contacts = append(append([]Contact(nil), b.Message.Contacts...), m.Contacts...)
		m.MediaCount += b.Message.MediaCount
		if _, problem = r.checkedContacts(m); problem != nil {
			if err := r.stopContactDefault(m, out); err != nil {
				return retryContacts(out, err)
			}
			out.Result = "failed"
			out.Detail = problem.detail
			r.reply(ctx, &out, out.To, problem.reply)
			return out
		}
		b.Message.Contacts = m.Contacts
		b.Message.MediaCount = m.MediaCount
	}
	// A stale caption cannot select a newer card from a different exchange.
	if !m.ReceivedAt.IsZero() && !b.Message.ReceivedAt.IsZero() && m.ReceivedAt.Before(b.Message.ReceivedAt) {
		out.Result = "failed"
		out.Detail = "no pending contact card"
		r.reply(ctx, &out, out.To, noReadyCard)
		return out
	}
	b.Words = contactWordNames(words)
	b.Targets = nil
	for _, w := range words {
		b.Targets = append(b.Targets, w.Phonebook)
	}
	b.DefaultWord = ""
	if err := r.d.Shelf.put(key, b); err != nil {
		return retryContacts(out, err)
	}
	return r.finishContacts(ctx, key, b, out)
}

func (r *Reader) finishContacts(ctx context.Context, key string, b contactBatch, out Outcome) Outcome {
	caller, known := r.d.Policy.LookupCaller(b.Message.From)
	words, result := r.resolveContactWords(strings.Join(b.Words, ","), caller)
	unchanged := known && result == "" && len(words) == len(b.Targets)
	if unchanged {
		for i, w := range words {
			if w.Phonebook != b.Targets[i] {
				unchanged = false
				break
			}
		}
	}
	if !unchanged {
		if err := r.d.Shelf.remove(key); err != nil {
			return retryContacts(out, err)
		}
		out.Result = "not-allowed"
		out.Detail = "pending contact permissions changed"
		return out
	}
	out.Word = strings.Join(b.Words, ",")
	out = r.importContacts(ctx, b.Message, out, words...)
	if out.Retry {
		return out
	}
	if err := r.d.Shelf.remove(key); err != nil {
		return retryContacts(out, err)
	}
	return out
}

func (r *Reader) cancelContacts(ctx context.Context, m Message, out Outcome) (Outcome, bool) {
	key, b, exists, err := r.pendingContacts(ctx, m, out)
	if err != nil {
		return retryContacts(out, err), true
	}
	if !exists || (!m.ReceivedAt.IsZero() && !b.Message.ReceivedAt.IsZero() && m.ReceivedAt.Before(b.Message.ReceivedAt)) {
		return out, false
	}
	if err := r.d.Shelf.remove(key); err != nil {
		return retryContacts(out, err), true
	}
	out.Result = "cancelled"
	r.reply(ctx, &out, out.To, "Contact import cancelled. No pending contacts were saved.")
	return out, true
}

func (r *Reader) advanceContactBatch(ctx context.Context, key string, b contactBatch) *contactOutcome {
	now := r.d.Now()
	out := Outcome{ID: b.Message.ID, Via: "sms", To: b.Message.From, Deferred: true}
	caller, known := r.d.Policy.LookupCaller(b.Message.From)
	out.Person = caller.ID
	if out.Person == "" {
		out.Person = caller.Name
	}
	done := func(o Outcome) *contactOutcome { return &contactOutcome{b.Message, o} }
	if !known || !r.mayImportContacts(caller) {
		if err := r.d.Shelf.remove(key); err != nil {
			return done(retryContacts(out, err))
		}
		out.Result = "not-allowed"
		return done(out)
	}
	if len(b.Words) > 0 {
		return done(r.finishContacts(ctx, key, b, out))
	}
	if !now.Before(b.Deadline) {
		if b.NoticeAt.IsZero() || b.NoDefault || b.DefaultWord == "" {
			if err := r.d.Shelf.remove(key); err != nil {
				return done(retryContacts(out, err))
			}
			out.Result = "failed"
			out.Detail = "contact choice expired"
			r.reply(ctx, &out, out.To, "The contact choice window expired without a phone book selection. No contacts were saved.")
			return done(out)
		}
		// Pin the announced House choice before saving, just as for explicit choices.
		b.Words = []string{b.DefaultWord}
		b.Targets = []string{"house"}
		if err := r.d.Shelf.put(key, b); err != nil {
			return done(retryContacts(out, err))
		}
		return done(r.finishContacts(ctx, key, b, out))
	}
	if !b.NoticeAt.IsZero() || now.Before(b.QuietUntil) {
		return nil
	}
	defaultWord := ""
	if !b.NoDefault {
		for _, w := range r.d.Messages.Words() {
			if w.Phonebook == "house" && allowed(w.People, caller) {
				defaultWord = w.Word
				break
			}
		}
	}
	notice := "These contacts will be added to phone book (House) in 2 minutes unless you reply Add to: followed by phone book names. Reply cancel to discard."
	if defaultWord == "" {
		notice = "Contact received. Reply Add to: followed by permitted phone book names within 2 minutes, or cancel. Without a choice, no contacts will be saved."
	}
	out.Result = "pending"
	r.reply(ctx, &out, out.To, notice)
	if out.Reply == "" {
		// No automatic import until the carrier has accepted the warning.
		b.QuietUntil = now.Add(contactQuiet)
		if err := r.d.Shelf.put(key, b); err != nil {
			return done(retryContacts(out, err))
		}
		return done(out)
	}
	b.NoticeAt = now
	b.Deadline = r.d.Now().Add(contactChoice)
	b.DefaultWord = defaultWord
	if err := r.d.Shelf.put(key, b); err != nil {
		return done(retryContacts(out, err))
	}
	return done(out)
}

// AdvanceContacts runs after a successful queue drain. If the edge is down,
// defer defaults rather than saving while destination texts may be waiting.
func (r *Reader) AdvanceContacts(ctx context.Context, onOutcome func(Message, Outcome)) bool {
	return r.advanceContacts(ctx, onOutcome, true)
}

func (r *Reader) advanceContacts(ctx context.Context, onOutcome func(Message, Outcome), allowDefaults bool) (retry bool) {
	for _, result := range r.deferredContacts {
		retry = retry || result.outcome.Retry
		if onOutcome != nil {
			onOutcome(result.message, result.outcome)
		}
	}
	r.deferredContacts = nil
	if r.d.Shelf == nil {
		return retry
	}
	for _, key := range r.d.Shelf.keys() {
		if !allowDefaults && len(r.d.Shelf.batches[key].Words) == 0 {
			continue
		}
		if ctx.Err() != nil {
			return retry
		}
		if result := r.advanceContactBatch(ctx, key, r.d.Shelf.batches[key]); result != nil {
			retry = retry || result.outcome.Retry
			if onOutcome != nil {
				onOutcome(result.message, result.outcome)
			}
		}
	}
	return retry
}

func (r *Reader) contactWait(wait time.Duration) time.Duration {
	if r.d.Shelf == nil || wait <= 0 {
		return wait
	}
	for _, b := range r.d.Shelf.batches {
		due := b.Deadline
		if len(b.Words) > 0 {
			return 0
		}
		if b.NoticeAt.IsZero() && b.QuietUntil.Before(due) {
			due = b.QuietUntil
		}
		remaining := due.Sub(r.d.Now())
		if remaining <= 0 {
			return 0
		}
		// Edge waits in whole seconds; round upward to avoid a busy loop during
		// the last fraction of a second before a shelf deadline.
		rounded := ((remaining + time.Second - 1) / time.Second) * time.Second
		if rounded < wait {
			wait = rounded
		}
	}
	return wait
}

func (r *Reader) mayImportContacts(caller policy.KnownCaller) bool {
	for _, w := range r.d.Messages.Words() {
		if w.Phonebook != "" && allowed(w.People, caller) {
			return true
		}
	}
	return false
}
