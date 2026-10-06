package inbox

import (
	"context"
	"regexp"
	"strings"

	"callmemaybe/internal/policy"
)

var uploadBookName = regexp.MustCompile(`^[a-z0-9]{1,32}$`)

// A Shortcut supplies its complete selection with the card. It must never
// read, merge, cancel, or extend the separate SMS conversation on the shelf.
func (r *Reader) importUpload(ctx context.Context, m Message, out Outcome, caller policy.KnownCaller) Outcome {
	valid := len(m.Phonebooks) > 0 && len(m.Phonebooks) <= 20 && m.Body == ""
	for _, book := range m.Phonebooks {
		valid = valid && uploadBookName.MatchString(book)
	}
	if !valid {
		out.Result = "failed"
		out.Detail = "invalid upload destinations"
		out.Reply = "Choose one or more phone books in the Shortcut. No contacts were saved."
		return out
	}
	words, problem := r.resolveContactWords(strings.Join(m.Phonebooks, ","), caller)
	if problem != "" {
		out.Result = problem
		out.Reply = "A selected phone book is unavailable or not permitted. No contacts were saved."
		return out
	}
	out.Word = strings.Join(contactWordNames(words), ",")
	return r.importContacts(ctx, m, out, words...)
}

type uploadResult struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

func uploadResultFor(m Message, out Outcome) (uploadResult, bool) {
	if m.Source != "contact-upload" || out.Retry {
		return uploadResult{}, false
	}
	result := uploadResult{ID: m.ID, Status: "failed", Message: out.Reply}
	if out.Result == "acted" {
		result.Status = "saved"
	}
	if result.Message == "" {
		result.Message = "The house refused this contact import. Check your access and selected phone books."
	}
	return result, true
}
