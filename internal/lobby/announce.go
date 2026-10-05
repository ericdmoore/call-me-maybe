package lobby

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"
)

// An announcement: doorman rings a handset, says one thing, and hangs up.
//
// It is the smallest form of the "live call" that .plans/s05-system-alerts
// describes, built first for the balance alert (s03 M2), and the property
// that makes it worth having is the one that makes it possible at all: an
// internal call never touches a trunk. No provider, no credit, no
// registration — a dead account can still ring the kitchen to say it is dead.
//
// A sibling of Session and Console, not a mode of either. There is no caller
// to admit, no keypad to read and no dialplan to release into; what is
// identical is the discipline — one goroutine, cancellation is the state,
// and exactly one teardown path (invariant 3). What is deliberately narrow is
// the vocabulary: a fixed catalogue of things doorman can say, each assembled
// from a pre-rendered clip and Asterisk's own number reader, never from
// synthesis (invariant 7). An announcement is a template with a slot, and
// refusing arbitrary text is what keeps a speech service off a path that
// runs unattended at nine in the morning.
//
// Nothing here reads a balance, or anything else about a provider. The CLI
// that does (`doorman balance`) originates the call and hands the number to
// say in the Stasis arguments; the daemon knows how to say a number and no
// more. That is what keeps the provider API key out of this process.

// BundledMediaPrefix is where the bundled prompt pack is installed under
// Asterisk's sounds directory. It is also the default PROMPT_MEDIA_PREFIX in
// internal/config, and a test in cmd/doorman keeps the two agreeing, because
// the system phrases below come from here whatever pack the lobby speaks
// with: a pack supplies the six lobby prompts and nothing else
// (docs/PACKS.md), so swapping the house voice must never silence an alert.
const BundledMediaPrefix = "call-me-maybe"

// systemMediaPrefix holds the system phrases, beside the lobby prompts and
// outside the pack contract.
const systemMediaPrefix = BundledMediaPrefix + "/system"

// The catalogue. Each kind is one announcement the daemon can make; the value
// is its slot. Both travel as Stasis arguments — announce,<kind>,<value> —
// which is the whole protocol between the CLI that originates and the daemon
// that speaks.
const (
	AnnounceArg = "announce"
	// AnnounceKindBalance says the calling credit is low and reads a whole
	// number: "…down to about" twelve. Units are deliberately unspoken —
	// dollars, Canadian dollars, euros — because the clip would have to be
	// recorded per currency and the person hearing it knows which one they
	// pay in.
	AnnounceKindBalance = "balance"
)

// announceClipTimeout bounds one clip. A PlaybackFinished that never arrives
// — Asterisk restarted underneath us, a clip Asterisk half-found — must not
// hold a handset open, and no system phrase is anywhere near this long.
const announceClipTimeout = 60 * time.Second

// AnnounceMedia turns a catalogue entry and its slot value into the media
// URIs to play, in order, or false when the daemon has no such announcement.
// False is the answer for an unknown kind and for a value that does not fit
// its slot: a caller who can originate through ARI already owns this box, so
// this is not a security boundary, but it is the reason a bad argument ends
// in a warning and a hangup rather than in Asterisk reading garbage aloud.
func AnnounceMedia(kind, value string) ([]string, bool) {
	switch kind {
	case AnnounceKindBalance:
		n, err := strconv.Atoi(value)
		if err != nil || n < 0 || len(value) > 7 {
			return nil, false
		}
		return []string{
			"sound:" + systemMediaPrefix + "/balance-low",
			"number:" + strconv.Itoa(n),
			sayGoodbye,
		}, true
	}
	return nil, false
}

// AnnounceDeps wires an Announcement to the world. There is deliberately no
// call recorder, event sink or notifier: this is the house talking to itself,
// and the call log is for calls somebody placed.
type AnnounceDeps struct {
	ARI ARI
	Log *slog.Logger
	// OnFinished lets the router drop this announcement's registration.
	OnFinished func(*Announcement)
}

// Announcement is one such call, from the handset answering to the hangup.
type Announcement struct {
	// ID is a short handle used in logs.
	ID string
	// ChannelID is the handset's Asterisk channel.
	ChannelID string

	media []string
	deps  AnnounceDeps
	log   *slog.Logger

	ctx    context.Context
	cancel context.CancelFunc
	events chan event
	pbSeq  int
}

// NewAnnouncement prepares to say media, in order, on channelID. Run starts
// it; the channel is already answered by the time it reaches us, because an
// originated channel enters Stasis when the far end picks up.
func NewAnnouncement(channelID string, media []string, deps AnnounceDeps) *Announcement {
	ctx, cancel := context.WithCancel(context.Background())
	id := shortID(channelID)
	return &Announcement{
		ID:        id,
		ChannelID: channelID,
		media:     media,
		deps:      deps,
		log:       deps.Log.With("announceId", id),
		ctx:       ctx,
		cancel:    cancel,
		events:    make(chan event, 16),
	}
}

// ── Inputs from the event router ─────────────────────────────────────────

// CallerGone cancels the announcement: the handset hung up, or the channel
// left our app. Unlike a console, an announcement never detaches — leaving
// Stasis alive is not a success it can have — so both events mean the same
// thing here and the router may send either.
func (a *Announcement) CallerGone() { a.cancel() }

func (a *Announcement) PlaybackFinished(id string) { a.post(event{evPlaybackDone, id}) }

func (a *Announcement) post(ev event) {
	select {
	case a.events <- ev:
	default:
		a.log.Warn("event dropped", "kind", ev.kind)
	}
}

// ── The call ─────────────────────────────────────────────────────────────

// Run says every clip in turn and returns when it is over. Every exit funnels
// through the deferred cleanup, including the handset hanging up mid-word,
// which arrives as cancellation and nothing else.
func (a *Announcement) Run() {
	defer a.cleanup()
	a.log.Info("announcing", "clips", len(a.media))
	for _, m := range a.media {
		if a.ctx.Err() != nil {
			return
		}
		a.play(m)
	}
}

// play starts one media URI and waits for it to finish. A clip Asterisk
// cannot find degrades to silence and the next clip, never a stuck call —
// the same rule the lobby applies to a missing prompt.
func (a *Announcement) play(media string) {
	a.pbSeq++
	pbID := fmt.Sprintf("an-%s-%d", a.ID, a.pbSeq)
	if err := a.deps.ARI.Play(a.ctx, a.ChannelID, media, pbID); err != nil {
		a.log.Warn("playback failed", "media", media, "err", err)
		return
	}
	deadline := time.NewTimer(announceClipTimeout)
	defer deadline.Stop()
	for {
		select {
		case ev := <-a.events:
			if ev.kind == evPlaybackDone && ev.value == pbID {
				return
			}
		case <-deadline.C:
			a.log.Warn("playback never finished, moving on", "media", media)
			_ = a.deps.ARI.StopPlayback(a.ctx, pbID)
			return
		case <-a.ctx.Done():
			return
		}
	}
}

// cleanup is the single teardown path, on a fresh context because the
// announcement's own is usually already cancelled by the time we get here.
// The hangup is unconditional: an announcement that has said its piece has
// nothing further to do with the handset, and one that was cut short has
// nothing to hang up but tries anyway, because a channel that is already
// gone is not an error worth propagating.
func (a *Announcement) cleanup() {
	a.cancel()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = a.deps.ARI.Hangup(ctx, a.ChannelID)
	if a.deps.OnFinished != nil {
		a.deps.OnFinished(a)
	}
	a.log.Info("announcement finished")
}
