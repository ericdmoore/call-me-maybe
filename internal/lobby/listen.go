package lobby

import (
	"context"
	"strconv"
	"strings"
	"time"

	"callmemaybe/internal/calls"
	events "callmemaybe/internal/observation"
	"callmemaybe/internal/policy"
)

// Dial-a-Story (#155): a menu choice that leads to a shelf of the box's
// audiobook library, to browse it or to hear its story of the day.
//
// doorman does not play books. The books menu is the books AGI, the same one
// a handset's *2665 runs, and the caller is released into the dialplan to
// reach it exactly as a caller is released to leave a message: by
// ContinueToDialplan into [cmm-listen], after which the channel is
// Asterisk's and nothing here may hang it up (invariant 8). What the caller
// may hear travels with them as channel variables only doorman sets — the
// shelf's titles, how long the call may still last — and [cmm-listen] holds
// them to it: it counts them against the line's listener cap and sets
// Asterisk's own absolute timeout before the AGI starts.
//
// Two things happen here first, while the caller is still ours. The
// challenge, when the choice is marked, has already been asked by the walker.
// And the listener cap is asked early, so a caller over it hears that the
// listening lines are busy and is offered the menu again rather than being
// released into a context that can only turn them away. [cmm-listen] counts
// again, joining before counting, and is the bound: two callers arriving in
// the same instant may both pass this early look, and one of them is then
// turned away there, with the same phrase and a goodbye.

// ListenContext is the dialplan context a listener is released into, and
// ListenGroupCategory the GROUP() category it counts them in. internal/render
// generates both; cmd/doorman's tests hold the two copies together.
const (
	ListenContext       = "cmm-listen"
	ListenGroupCategory = "cmm-listen"
)

// listenFloor is the least time worth releasing a caller with: less than
// this and the call ends here instead of starting a story it cannot finish.
const listenFloor = 30 * time.Second

// Listening is how this line's listeners are counted.
type Listening struct {
	// Group names this line's listeners in Asterisk's GROUP(): one per line,
	// so the cap is the line's. Empty is "default".
	Group string
	// Count asks Asterisk how many callers are in group now — GROUP_COUNT,
	// read on the caller's own channel. Nil, or an error, skips the early
	// answer; [cmm-listen] still counts.
	Count func(ctx context.Context, channelID, group string) (int, error)
}

func (l Listening) group() string {
	if l.Group == "" {
		return "default"
	}
	return l.Group
}

// listen releases the caller to the shelf a choice names. released says the
// caller has left doorman — or the call is over — and the walker must stop;
// otherwise key is a key pressed over the busy phrase, a choice at the menu
// the walker is about to offer again.
func (s *Session) listen(c policy.ResolvedChoice) (released bool, key string) {
	shelf, ok := s.pol.Shelf(c.Target)
	if !ok {
		return false, "" // refused at load; offered again rather than dropped
	}
	limits := s.pol.Listening()

	// The story of the day is picked here, by the shelf's rule and the
	// line's clock, so the AGI is handed one title and can reach no other.
	mode, titles := "shelf", shelf.Titles
	if c.Kind == policy.ChoiceStory {
		mode, titles = "story", []string{shelf.StoryOn(s.now())}
	}

	// The limit counts from when the call arrived: a channel held in the
	// menu is a channel held. The real clock, for the menu's reason.
	remaining := limits.MaxCall - time.Since(s.held)
	if remaining < listenFloor {
		s.log.Info("too little of the call's time is left to listen", "shelf", shelf.ID)
		s.dismiss("listen-time")
		return true, ""
	}

	group := s.deps.Listening.group()
	if count := s.deps.Listening.Count; count != nil {
		n, err := count(s.ctx, s.ChannelID, group)
		if s.ctx.Err() != nil {
			return true, ""
		}
		switch {
		case err != nil:
			s.log.Warn("could not count listeners; the dialplan will", "err", err)
		case n >= limits.MaxListeners:
			s.log.Info("every listening line is busy, offering the menu again",
				"shelf", shelf.ID, "listeners", n, "max", limits.MaxListeners)
			key := s.playMedia("sound:"+systemMediaPrefix+"/listen-busy", true)
			return s.ctx.Err() != nil, key
		}
	}

	s.observe(events.AdmissionDecided, "menu-listen")
	s.log.Info("releasing caller to listen", "shelf", shelf.ID, "mode", mode,
		"titles", len(titles), "seconds", int(remaining.Seconds()))

	// Every variable or none: [cmm-listen] refuses a missing one, so a
	// caller released half-told would only be hung up.
	vars := [][2]string{
		{"CMM_LISTEN_MODE", mode},
		{"CMM_LISTEN_TITLES", strings.Join(titles, ",")},
		{"CMM_LISTEN_SECONDS", strconv.Itoa(int(remaining.Seconds()))},
		{"CMM_LISTEN_WARNING", strconv.Itoa(int(policy.ListenWarning.Seconds()))},
		{"CMM_LISTEN_MAX", strconv.Itoa(limits.MaxListeners)},
		{"CMM_LISTEN_GROUP", group},
	}
	for _, v := range vars {
		if err := s.deps.ARI.SetChannelVar(s.ctx, s.ChannelID, v[0], v[1]); err != nil {
			s.log.Error("could not hand the shelf over, offering the menu again", "variable", v[0], "err", err)
			return s.ctx.Err() != nil, ""
		}
	}

	// The same release sendToVoicemail makes, and for the same reason it is
	// a release and not a hangup.
	s.rec.Outcome = calls.OutcomeListening
	s.detached.Store(true)
	if err := s.deps.ARI.ContinueToDialplan(s.ctx, s.ChannelID, ListenContext, "s", 1); err != nil {
		// The handoff failed; the caller is still ours after all, and the
		// phone keeps answering: back to the menu.
		s.detached.Store(false)
		s.rec.Outcome = calls.OutcomeAbandoned
		s.log.Error("listening handoff failed, offering the menu again", "err", err)
		return s.ctx.Err() != nil, ""
	}
	return true, ""
}
