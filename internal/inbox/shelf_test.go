package inbox

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"callmemaybe/internal/ownbook"
	"callmemaybe/internal/policy"
)

type shelfHarness struct {
	*harness
	now      time.Time
	state    string
	outcomes []Outcome
}

func newShelfHarness(t *testing.T) *shelfHarness {
	t.Helper()
	h := &shelfHarness{harness: contactHarness(t), now: time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC), state: t.TempDir()}
	h.reader.d.Now = func() time.Time { return h.now }
	var err error
	h.reader.d.Shelf, err = OpenContactShelf(h.state)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.reader.d.Shelf.Close() })
	return h
}
func (h *shelfHarness) card(id string) Message {
	return Message{ID: id, From: "15125550101", To: "15125550100", ReceivedAt: h.now, MediaCount: 1, Contacts: sampleContacts}
}
func (h *shelfHarness) text(id, body string) Message {
	return Message{ID: id, From: "15125550101", To: "15125550100", ReceivedAt: h.now, Body: body}
}
func (h *shelfHarness) handle(m Message) Outcome { return h.reader.Handle(context.Background(), m) }
func (h *shelfHarness) tick(d time.Duration) {
	h.now = h.now.Add(d)
	h.reader.AdvanceContacts(context.Background(), func(_ Message, o Outcome) { h.outcomes = append(h.outcomes, o) })
}
func (h *shelfHarness) entries(t *testing.T, target string) int {
	t.Helper()
	dir, id, _ := ownbook.SharedLocation(h.reader.d.Phonebooks, target)
	b, err := ownbook.Load(dir, id)
	if err != nil {
		t.Fatal(err)
	}
	return len(b.Entries)
}
func (h *shelfHarness) reopen(t *testing.T) {
	t.Helper()
	h.reader.d.Shelf.Close()
	s, err := OpenContactShelf(h.state)
	if err != nil {
		t.Fatal(err)
	}
	h.reader.d.Shelf = s
}

func TestContactShelfSilenceNoticeAndHouseDefault(t *testing.T) {
	h := newShelfHarness(t)
	if out := h.handle(h.card("card")); out.Result != "pending" || out.Reply != "" {
		t.Fatalf("shelve: %+v", out)
	}
	h.tick(14 * time.Second)
	if len(h.edge.sent) != 0 || h.entries(t, "house") != 0 {
		t.Fatal("acted before silence")
	}
	h.tick(time.Second)
	if len(h.edge.sent) != 1 || !strings.Contains(h.edge.sent[0]["message"], "House") || policy.ReplyProblem(h.edge.sent[0]["message"]) != "" {
		t.Fatal("missing default notice")
	}
	h.tick(119 * time.Second)
	if h.entries(t, "house") != 0 {
		t.Fatal("saved before choice deadline")
	}
	h.tick(time.Second)
	if h.entries(t, "house") != 2 || h.entries(t, "handset:kitchen") != 0 || len(h.reader.d.Shelf.batches) != 0 {
		t.Fatal("default did not save only House")
	}
	if len(h.edge.sent) != 2 || h.outcomes[len(h.outcomes)-1].Result != "acted" || !h.outcomes[len(h.outcomes)-1].Deferred {
		t.Fatal("default outcome missing")
	}
	h.tick(time.Minute)
	if len(h.edge.sent) != 2 {
		t.Fatal("default repeated")
	}
}

func TestContactShelfSeparateInstructionsReplaceHouse(t *testing.T) {
	for _, delay := range []time.Duration{5 * time.Second, 30 * time.Second} {
		t.Run(delay.String(), func(t *testing.T) {
			h := newShelfHarness(t)
			h.handle(h.card("card"))
			h.tick(delay)
			out := h.handle(h.text("selection", "Add to: Kitchen"))
			if out.Result != "acted" || h.entries(t, "handset:kitchen") != 2 || h.entries(t, "house") != 0 {
				t.Fatalf("selection: %+v", out)
			}
			h.tick(3 * time.Minute)
			if h.entries(t, "house") != 0 {
				t.Fatal("explicit choice later defaulted to House")
			}
		})
	}
}

func TestContactShelfLateInstructionsNeverApplyToNextCard(t *testing.T) {
	h := newShelfHarness(t)
	h.handle(h.card("card"))
	h.tick(contactQuiet)
	h.now = h.now.Add(contactChoice)
	// Even before the periodic sweep, a late instruction cannot redirect the card.
	out := h.handle(h.text("late", "Add to: kitchen"))
	if out.Reply != noReadyCard || h.entries(t, "house") != 2 || h.entries(t, "handset:kitchen") != 0 {
		t.Fatalf("late: %+v", out)
	}
	next := h.card("next")
	next.Contacts = []Contact{{Name: "Next", Number: "+15125550125"}}
	h.handle(next)
	h.tick(contactQuiet)
	h.tick(contactChoice)
	if h.entries(t, "house") != 3 || h.entries(t, "handset:kitchen") != 0 {
		t.Fatal("late instruction became a standing instruction")
	}
}

func TestContactShelfInstructionsWithoutCardDoNotCreateState(t *testing.T) {
	h := newShelfHarness(t)
	out := h.handle(h.text("early", "Add to: kitchen"))
	if out.Reply != noReadyCard || len(h.reader.d.Shelf.batches) != 0 {
		t.Fatalf("early: %+v", out)
	}
	h.handle(h.card("card"))
	h.tick(contactQuiet)
	h.tick(contactChoice)
	if h.entries(t, "house") != 2 || h.entries(t, "handset:kitchen") != 0 {
		t.Fatal("early instructions stuck around")
	}
}

func TestContactShelfIsolatedBySenderAndRecipient(t *testing.T) {
	h := newShelfHarness(t)
	h.handle(h.card("card"))
	other := h.text("other-did", "Add to: kitchen")
	other.To = "15125550199"
	if out := h.handle(other); out.Reply != noReadyCard {
		t.Fatalf("cross-DID: %+v", out)
	}
	other = h.text("other-sender", "Add to: kitchen")
	other.From = "15125550102"
	if out := h.handle(other); out.Result != "not-allowed" {
		t.Fatalf("cross-sender: %+v", out)
	}
	if out := h.handle(h.text("selection", "Add to: house,kitchen")); out.Result != "acted" {
		t.Fatalf("selection: %+v", out)
	}
	if h.entries(t, "house") != 2 || h.entries(t, "handset:kitchen") != 2 {
		t.Fatal("selected books missing")
	}
}

func TestContactShelfRestartAndReplayPreserveSilenceAndDeadline(t *testing.T) {
	h := newShelfHarness(t)
	card := h.card("card")
	h.handle(card)
	h.tick(10 * time.Second)
	h.reopen(t)
	// Model a crash before the separate seen set was flushed to disk.
	h.reader.d.Seen = nil
	if out := h.handle(card); out.Result != "duplicate" {
		t.Fatalf("replay: %+v", out)
	}
	h.tick(5 * time.Second)
	if len(h.edge.sent) != 1 {
		t.Fatal("replay reset silence")
	}
	h.reopen(t)
	h.tick(119 * time.Second)
	if h.entries(t, "house") != 0 {
		t.Fatal("restart shortened choice")
	}
	h.tick(time.Second)
	if h.entries(t, "house") != 2 || len(h.edge.sent) != 2 {
		t.Fatal("restart lost pending import")
	}
}

func TestContactShelfNewCardsResetSilenceAndDuplicateNumbersAreSuppressed(t *testing.T) {
	h := newShelfHarness(t)
	h.handle(h.card("one"))
	h.tick(10 * time.Second)
	two := h.card("two")
	two.Contacts = []Contact{sampleContacts[0], {Name: "Second", Number: "+15125550125"}}
	h.handle(two)
	h.tick(5 * time.Second)
	if len(h.edge.sent) != 0 {
		t.Fatal("notice before new silence period")
	}
	h.tick(10 * time.Second)
	h.tick(contactChoice)
	if h.entries(t, "house") != 3 {
		t.Fatal("batch lost or duplicated a number")
	}
}

func TestContactShelfInvalidChoiceSuppressesDefaultAndAllowsCorrection(t *testing.T) {
	for _, choice := range []string{"Add to: missing", "Add to: house,missing"} {
		t.Run(choice, func(t *testing.T) {
			h := newShelfHarness(t)
			h.handle(h.card("card"))
			h.tick(contactQuiet)
			if out := h.handle(h.text("bad", choice)); out.Result != "unknown-word" {
				t.Fatalf("bad choice: %+v", out)
			}
			h.tick(time.Minute)
			if out := h.handle(h.text("fixed", "Add to: kitchen")); out.Result != "acted" {
				t.Fatalf("correction: %+v", out)
			}
			h.tick(contactChoice)
			if h.entries(t, "house") != 0 || h.entries(t, "handset:kitchen") != 2 {
				t.Fatal("invalid choice fell back to House")
			}
		})
	}
	h := newShelfHarness(t)
	h.handle(h.card("card"))
	h.tick(contactQuiet)
	h.handle(h.text("bad", "Add to: missing"))
	h.tick(contactChoice)
	if h.entries(t, "house") != 0 || len(h.reader.d.Shelf.batches) != 0 {
		t.Fatal("invalid choice defaulted")
	}
}

func TestContactShelfCancellationAndPermissionRecheck(t *testing.T) {
	h := newShelfHarness(t)
	h.handle(h.card("card"))
	if out := h.handle(h.text("cancel", "cancel")); out.Result != "cancelled" {
		t.Fatalf("cancel: %+v", out)
	}
	h.tick(3 * time.Minute)
	if h.entries(t, "house") != 0 {
		t.Fatal("cancelled batch saved")
	}
	h.handle(h.card("next"))
	h.tick(contactQuiet)
	msgs, err := policy.MessagesFromTOML([]byte(houseWords))
	if err != nil {
		t.Fatal(err)
	}
	h.reader.d.Messages = msgs
	before := len(h.edge.sent)
	h.tick(contactChoice)
	if h.entries(t, "house") != 0 || len(h.edge.sent) != before {
		t.Fatal("revoked sender imported or got a reply")
	}
}

func TestContactShelfDoesNotDefaultWithoutHousePermission(t *testing.T) {
	h := newShelfHarness(t)
	msgs, err := policy.MessagesFromTOML([]byte(houseWords + strings.Replace(contactWords, "word = \"house\"\npeople = [\"gabi\"]", "word = \"house\"\npeople = [\"someoneelse\"]", 1)))
	if err != nil {
		t.Fatal(err)
	}
	h.reader.d.Messages = msgs
	h.handle(h.card("card"))
	h.tick(contactQuiet)
	if strings.Contains(h.edge.sent[0]["message"], "(House)") {
		t.Fatal("promised an unauthorized default")
	}
	h.tick(contactChoice)
	if h.entries(t, "house") != 0 || h.entries(t, "handset:kitchen") != 0 {
		t.Fatal("guessed a permitted default")
	}
}

type failingTransport struct{}

func (failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("offline")
}
func TestContactShelfFailedWarningCannotStartAutomaticImport(t *testing.T) {
	h := newShelfHarness(t)
	h.handle(h.card("card"))
	h.reader.d.Edge.Client = &http.Client{Transport: failingTransport{}}
	h.tick(contactQuiet)
	h.tick(3 * time.Minute)
	if h.entries(t, "house") != 0 {
		t.Fatal("defaulted without an accepted warning")
	}
	h.reader.d.Edge.Client = nil
	h.tick(contactQuiet)
	h.tick(119 * time.Second)
	if h.entries(t, "house") != 0 {
		t.Fatal("choice deadline started before warning")
	}
	h.tick(time.Second)
	if h.entries(t, "house") != 2 {
		t.Fatal("warning retry did not recover")
	}
}

func TestContactShelfUnwritableStateIsNotSeenOrAcknowledged(t *testing.T) {
	h := newShelfHarness(t)
	card := h.card("retry")
	h.edge.push(card)
	// A file in place of the directory fails equally under root and regular users.
	h.reader.d.Shelf.dir = filepath.Join(h.state, "not-a-directory")
	if err := os.WriteFile(h.reader.d.Shelf.dir, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	var got Outcome
	_, _ = h.reader.PullOnce(context.Background(), 0, func(_ Message, o Outcome) { got = o })
	if !got.Retry || h.reader.d.Seen.Has(card.ID) || len(h.edge.acked) != 0 {
		t.Fatalf("lost retry: %+v", got)
	}
}

func TestContactShelfLocksAndPrivateStorage(t *testing.T) {
	h := newShelfHarness(t)
	h.handle(h.card("card"))
	if second, err := OpenContactShelf(h.state); err == nil {
		second.Close()
		t.Fatal("second consumer acquired pending contacts")
	}
	paths, _ := filepath.Glob(filepath.Join(h.state, "contacts-pending", "*.json"))
	if len(paths) != 1 {
		t.Fatal("pending state missing")
	}
	info, err := os.Stat(paths[0])
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("pending contact file is not private")
	}
	h.reader.d.Shelf.Close()
	if err := os.WriteFile(paths[0], []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if s, err := OpenContactShelf(h.state); err == nil {
		s.Close()
		t.Fatal("corrupt pending state silently dropped")
	}
}

func TestContactShelfPollDeadlineAndQueuedTimelyInstructions(t *testing.T) {
	h := newShelfHarness(t)
	h.handle(h.card("card"))
	if got := h.reader.contactWait(20 * time.Second); got != 15*time.Second {
		t.Fatalf("quiet wait: %v", got)
	}
	h.tick(contactQuiet)
	timely := h.text("timely", "Add to: kitchen")
	timely.ReceivedAt = h.now.Add(119 * time.Second)
	h.now = h.now.Add(121 * time.Second)
	h.edge.push(timely)
	if _, err := h.reader.PullOnce(context.Background(), 0, nil); err != nil {
		t.Fatal(err)
	}
	if h.entries(t, "house") != 0 || h.entries(t, "handset:kitchen") != 2 {
		t.Fatal("saved before draining timely queued instructions")
	}
}

func TestContactShelfFailedSelectionSurvivesRestartWithoutHouseFallback(t *testing.T) {
	h := newShelfHarness(t)
	h.handle(h.card("card"))
	h.tick(contactQuiet)
	books := h.reader.d.Phonebooks
	broken := filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(broken, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	h.reader.d.Phonebooks = broken
	choice := h.text("choice", "Add to: kitchen")
	if out := h.handle(choice); !out.Retry || h.reader.d.Seen.Has(choice.ID) {
		t.Fatalf("failed selection was consumed: %+v", out)
	}
	h.reopen(t)
	h.reader.d.Phonebooks = books
	h.tick(3 * time.Minute)
	if h.entries(t, "house") != 0 || h.entries(t, "handset:kitchen") != 2 || len(h.reader.d.Shelf.batches) != 0 {
		t.Fatal("recovery lost explicit destination")
	}
}

func TestContactShelfPinnedWordCannotBeRetargetedAfterRestart(t *testing.T) {
	h := newShelfHarness(t)
	h.handle(h.card("card"))
	key := h.reader.d.Shelf.keys()[0]
	b := h.reader.d.Shelf.batches[key]
	b.Words, b.Targets = []string{"kitchen"}, []string{"handset:kitchen"}
	if err := h.reader.d.Shelf.put(key, b); err != nil {
		t.Fatal(err)
	}
	h.reopen(t)
	msgs, err := policy.MessagesFromTOML([]byte(houseWords + strings.ReplaceAll(contactWords, `phonebook = "handset:kitchen"`, `phonebook = "house"`)))
	if err != nil {
		t.Fatal(err)
	}
	h.reader.d.Messages = msgs
	h.tick(0)
	if h.entries(t, "house") != 0 || h.entries(t, "handset:kitchen") != 0 || len(h.reader.d.Shelf.batches) != 0 {
		t.Fatal("recovered selection followed a changed target")
	}
}

func TestContactShelfStaleInvalidChoiceAndCancelDoNotAlterNewCard(t *testing.T) {
	for _, body := range []string{"Add to: missing", "cancel"} {
		t.Run(body, func(t *testing.T) {
			h := newShelfHarness(t)
			stale := h.text("stale", body)
			h.now = h.now.Add(time.Minute)
			h.handle(h.card("card"))
			h.handle(stale)
			h.tick(contactQuiet)
			h.tick(contactChoice)
			if h.entries(t, "house") != 2 {
				t.Fatal("stale message changed new card")
			}
		})
	}
}

func TestContactShelfDrainsFullQueueBeforeDefaulting(t *testing.T) {
	h := newShelfHarness(t)
	h.handle(h.card("card"))
	h.tick(contactQuiet)
	timely := h.text("selection", "Add to: kitchen")
	timely.ReceivedAt = h.now.Add(119 * time.Second)
	h.now = h.now.Add(121 * time.Second)
	// A full edge page means another page might hold timely instructions.
	for i := 0; i < 10; i++ {
		m := h.text(fmt.Sprintf("other-%d", i), "ping")
		h.edge.push(m)
	}
	if _, err := h.reader.PullOnce(context.Background(), 0, nil); err != nil {
		t.Fatal(err)
	}
	if h.entries(t, "house") != 0 {
		t.Fatal("defaulted before draining edge queue")
	}
	h.edge.push(timely)
	if _, err := h.reader.PullOnce(context.Background(), 0, nil); err != nil {
		t.Fatal(err)
	}
	if h.entries(t, "house") != 0 || h.entries(t, "handset:kitchen") != 2 {
		t.Fatal("ignored timely second-page selection")
	}
}

func TestEmptyCarrierMessageReportsMissingAttachmentWithoutCreatingCard(t *testing.T) {
	h := newShelfHarness(t)
	empty := h.text("empty-carrier-message", "")
	out := h.handle(empty)
	if out.Result != "failed" || !strings.Contains(out.Reply, "No attachment was included") || policy.ReplyProblem(out.Reply) != "" {
		t.Fatalf("empty message response: %+v", out)
	}
	h.tick(3 * time.Minute)
	if len(h.reader.d.Shelf.batches) != 0 || h.entries(t, "house") != 0 || len(h.edge.sent) != 1 {
		t.Fatal("empty message created pending contact work")
	}
	// Never answer a stranger, even when their message is empty.
	empty.ID = "stranger-empty"
	empty.From = "15125550999"
	if out := h.handle(empty); out.Result != "unlisted" || out.Reply != "" {
		t.Fatalf("stranger: %+v", out)
	}
}
