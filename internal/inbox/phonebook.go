package inbox

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"callmemaybe/internal/ownbook"
	"callmemaybe/internal/policy"
)

// Resolve the whole caption before saving anything. A typo must not
// silently file a card to just some of the requested destinations, and every
// alias needs its own permission even if two aliases point at the same book.
func (r *Reader) resolveContactWords(destinations string, caller policy.KnownCaller) ([]policy.Word, string) {
	var words []policy.Word
	seen := map[string]bool{}
	invalid, denied := false, false
	for _, name := range strings.Split(destinations, ",") {
		word, ok := r.d.Messages.Lookup(strings.TrimSpace(name))
		if !ok || word.Phonebook == "" {
			invalid = true
			continue
		}
		if !allowed(word.People, caller) {
			denied = true
		}
		if !seen[word.Phonebook] {
			seen[word.Phonebook] = true
			words = append(words, word)
		}
	}
	if denied {
		return nil, "not-allowed"
	}
	if invalid {
		return nil, "unknown-word"
	}
	return words, ""
}

func contactWordNames(words []policy.Word) []string {
	names := make([]string, 0, len(words))
	for _, word := range words {
		names = append(names, word.Word)
	}
	return names
}

func (r *Reader) routeContacts(ctx context.Context, m Message, out Outcome, destinations string, caller policy.KnownCaller) Outcome {
	words, result := r.resolveContactWords(destinations, caller)
	if result != "" {
		// An explicit but invalid choice cancels the House default. A typo must
		// never cause a contact to be filed somewhere the sender just excluded.
		if err := r.stopContactDefault(m, out); err != nil {
			return retryContacts(out, err)
		}
		out.Result = result
		if result == "unknown-word" {
			r.reply(ctx, &out, out.To, "Use Add to: followed by configured phone book names, separated by commas. One name was empty or unknown. No contacts were saved.")
		}
		return out
	}
	out.Word = strings.Join(contactWordNames(words), ",")
	if r.d.Shelf != nil {
		return r.selectContacts(ctx, m, out, words)
	}
	return r.importContacts(ctx, m, out, words...)
}

func (r *Reader) importContacts(ctx context.Context, m Message, out Outcome, words ...policy.Word) Outcome {
	fail := func(detail, reply string) Outcome {
		out.Result, out.Detail = "failed", detail
		r.reply(ctx, &out, out.To, reply)
		return out
	}
	if r.d.Phonebooks == "" {
		return fail("phone book imports are not configured", "Contact imports are not set up yet.")
	}
	for _, word := range words {
		if word.Phonebook != "house" && r.d.Policy.HandsetEndpoint(strings.TrimPrefix(word.Phonebook, "handset:")) == "" {
			return fail("unknown phone book", "That phone book is not configured. No contacts were saved.")
		}
	}
	entries, problem := r.checkedContacts(m)
	if problem != nil {
		return fail(problem.detail, problem.reply)
	}
	added := 0
	for i, word := range words {
		n, err := ownbook.Import(r.d.Phonebooks, word.Phonebook, entries)
		if err != nil {
			// Pending selections are durable and idempotent. Keep them for retry
			// without sending repeated failure texts while storage is unavailable.
			if r.d.Shelf != nil || m.Source == "contact-upload" {
				return retryContacts(out, errors.New("could not save phone book"))
			}
			// Multiple files cannot commit atomically. Be precise about which
			// books finished, and let an idempotent resend complete the rest.
			return fail("could not save phone book", fmt.Sprintf("Updated %d of %d phone books before a save failed. Please resend the same message; existing entries will be kept.", i, len(words)))
		}
		added += n
	}
	out.Result = "acted"
	if len(words) > 1 {
		r.reply(ctx, &out, out.To, fmt.Sprintf("Saved %d new entries across %d phone books; %d already present. Phones update on their next directory refresh.", added, len(words), len(entries)*len(words)-added))
		return out
	}
	label := "the house phone book"
	if words[0].Phonebook != "house" {
		label = "the handset phone book"
	}
	r.reply(ctx, &out, out.To, fmt.Sprintf("Saved %d new number(s) to %s; %d already present. Phones update on their next directory refresh.", added, label, len(entries)-added))
	return out
}

type contactInputError struct{ detail, reply string }

func (r *Reader) checkedContacts(m Message) ([]ownbook.Entry, *contactInputError) {
	fail := func(detail, reply string) ([]ownbook.Entry, *contactInputError) {
		return nil, &contactInputError{detail, reply}
	}
	if m.MediaCount == 0 {
		return fail("no contact attachment received", "No contact attachment reached the house. If you sent one, the carrier may not have forwarded it. No contacts were saved.")
	}
	if m.MediaCount < 0 || m.MediaCount > 3 {
		return fail("expected one to three contact cards", "Send one to three vCard attachments. Follow them with Add to: and the phone book names. No contacts were saved.")
	}
	if m.ContactError == "too-many-contacts" || len(m.Contacts) > 100 {
		return fail("too many numbers", "Please share at most 100 phone numbers at a time. No contacts were saved.")
	}
	if m.ContactError != "" {
		return fail("edge could not read contact cards", "I need complete vCard files with phone numbers, at most 1300 KiB each. No contacts were saved.")
	}
	entries, err := r.contactEntries(m.Contacts)
	if err != nil {
		return fail(err.Error(), "The contact data was invalid or missing. Please resend the card. No contacts were saved.")
	}
	return entries, nil
}

var contactNumber = regexp.MustCompile(`^\+?[0-9]{7,15}$`)
var contactE164 = regexp.MustCompile(`^\+[1-9][0-9]{7,14}$`)

// Treat even authenticated queue data as untrusted. Only bounded display
// names and dialable numbers reach disk; the original vCard is never fetched.
func (r *Reader) contactEntries(contacts []Contact) ([]ownbook.Entry, error) {
	if len(contacts) == 0 || len(contacts) > 100 {
		return nil, errors.New("invalid contact count")
	}
	var entries []ownbook.Entry
	seen := map[string]bool{}
	for _, c := range contacts {
		if !utf8.ValidString(c.Name) || utf8.RuneCountInString(c.Name) > 200 ||
			strings.ContainsFunc(c.Name, func(ch rune) bool { return unicode.IsControl(ch) || ch == '\ufffe' || ch == '\uffff' }) ||
			!contactNumber.MatchString(c.Number) {
			return nil, errors.New("invalid contact data")
		}
		n := policy.NormaliseCallerID(c.Number, r.d.CountryCode)
		if n.Kind != policy.KindE164 || !contactE164.MatchString(n.Value) {
			return nil, errors.New("contact number cannot be dialled")
		}
		if seen[n.Value] {
			continue
		}
		seen[n.Value] = true
		name := strings.Join(strings.Fields(c.Name), " ")
		if name == "" {
			name = n.Value
		}
		entries = append(entries, ownbook.Entry{Name: name, E164: n.Value, Added: r.d.Now()})
	}
	return entries, nil
}
