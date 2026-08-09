package lobby

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"callmemaybe/internal/calls"
	"callmemaybe/internal/policy"
)

// The outbound console: the lobby running backwards.
//
// A stranger dials in and is connected to a handset. Here a handset dials *4
// and is connected to a stranger — same primitives (answer, play, collect,
// release), opposite direction. It is a sibling of Session rather than a mode
// of it: everything Session is built around is inbound. It has a caller ID to
// normalise, an allow-list to check it against, a rate-limit budget, and a ring
// plan. A console call has none of those, and bending Session around a call
// with no caller would cost far more than the fifty lines the two have in
// common.
//
// It does keep a call record, and that turns out to make the same point. The
// two share a type and almost no fields: this one has a direction, a number
// this house dialled, and an outcome of "placed" — because doorman hands the
// channel to the dialplan and is gone before the far end rings, so every word
// the inbound side has for how a call ended is a word about who answered here.
//
// What is deliberately identical is the discipline: one goroutine, all state
// owned by it, cancellation *is* the state, and exactly one teardown path —
// the deferred cleanup in Run.
//
// What the console does NOT do is dial the trunk itself. It sets the caller ID
// and the trunk on the handset's own channel and releases it into the dialplan,
// exactly as sendToVoicemail releases a caller into [voicemail-drop]. Three
// things follow from that and each is worth more than the bridge it replaces:
//
//  1. The trunk dial string stays in extensions.conf. doorman names a trunk —
//     an id out of trunks.toml — but never learns how to address one, so
//     nothing here knows what a PJSIP dial string looks like.
//  2. The console path and the plain _NXXNXXXXXX path dial through the *same*
//     dialplan lines — literally the same [cmm-outbound] context — differing
//     only in which two channel variables the channel carries. The enhanced
//     path is not a parallel implementation that can drift.
//  3. Once released, doorman is out of the call. A restart mid-conversation
//     cannot touch it.

const (
	// outboundCIDVar is the channel variable the dialplan reads to decide what
	// a call presents. It arrives three ways, later wins: a per-handset
	// set_var in the generated PJSIP config, this console setting it
	// explicitly, or unset — which is the trunk default and is what every
	// outbound call did before any of this existed.
	outboundCIDVar = "OUTBOUND_CID"
	// outboundTrunkVar is the channel variable that decides which provider the
	// call leaves by. It arrives the same three ways, and unset means the
	// dialplan's DEFAULT_TRUNK — one provider, and nothing to choose.
	//
	// It is set with the caller ID and never apart from it. A provider will not
	// carry a number its account does not own, so presenting one line's caller
	// ID down another line's trunk is a call that is rejected or silently
	// rewritten, and neither is visible from this end.
	outboundTrunkVar = "OUTBOUND_TRUNK"
	// outboundContext is where the console releases the handset. It is
	// deliberately not [internal]: see emergencyNumbers.
	outboundContext = "outbound-console"
)

// The console speaks with Asterisk's own voice rather than the prompt pack.
//
// A pack is the *lobby's* personality — what a stranger hears at the door —
// and the console is on the other side of the house entirely, talking to the
// household on the way out. More decisively: the six names in prompts.go are a
// contract, and adding a seventh breaks every pack that exists. A pack that
// half-works is the one thing docs/PACKS.md refuses. So the console uses
// sounds that ship with Asterisk, which every install already has because *97
// and voicemail need them.
//
// Every utterance is built so its meaning survives a missing clip: the digits
// carry the information and the words are glue. A sound Asterisk cannot find
// degrades to silence rather than a stuck call, so the part that must not go
// missing is the part that is spoken as digits.
const (
	sayEnterNumber = "sound:vm-enter-num-to-call" // "please enter the number you wish to call"
	sayThenPound   = "sound:vm-then-pound"        // "then press pound"
	sayNumberIs    = "sound:vm-num-i-have"        // "the number I have is"
	sayInvalid     = "sound:pbx-invalid"          // "that is not a valid extension"
	sayGoodbye     = "sound:vm-goodbye"           // "goodbye"
	sayBeep        = "sound:beep"                 // the unmistakable one: your turn
)

// emergencyNumbers are refused by the console outright.
//
// This is the most consequential rule in this package. E911 is registered per
// DID against a street address, so an emergency call must leave by the trunk
// whose address is on file — and placing a call as some *other* number is the
// entire purpose of this console. A 911 call that went out as the business
// line would put somebody else's address on a dispatcher's screen.
//
// So two independent things have to be wrong before an emergency call can
// leave this way: the console refuses here, and [outbound-console] in the
// dialplan contains no emergency pattern at all. `_911` in [internal] is
// untouched and remains the correct route — which is exactly what the refusal
// leaves the caller free to do, immediately, by hanging up and dialling it.
//
// The list is short and international because none of these is a dialable
// destination anywhere: refusing 112 in North America costs nothing and saves
// somebody who learned to dial it.
var emergencyNumbers = map[string]bool{
	"911": true, // North America
	"112": true, // GSM and most of Europe
	"999": true, // UK, Ireland, Hong Kong
	"000": true, // Australia
}

const (
	// minDialledDigits rejects a slip of the thumb before it becomes a call.
	minDialledDigits = 3
	// maxDialledDigits is E.164's ceiling plus room for a dial-out prefix. It
	// exists because this runs on an open phone line, where a stuck keypad
	// would otherwise accumulate for as long as the timer allows.
	maxDialledDigits = 20
	// consoleAttempts is how many times you may fumble a menu choice or a
	// number before the console gives up. Not configurable: the audience is
	// the household on its own handsets, not a stranger at the door, so there
	// is no budget here to defend and no reason for a knob.
	consoleAttempts = 3
	// consoleMaxLines is how many lines fit on a keypad menu. More than nine
	// and the tenth would need a two-digit choice, which is a different (and
	// so far imaginary) problem.
	consoleMaxLines = 9
)

// ConsoleLine is one line as the outbound console offers it.
type ConsoleLine struct {
	// Name is the line name — "default", "biz" — for logs.
	Name string
	// Label is the [line] label, for logs. The console never speaks it: there
	// is no recording of an operator's own words for it to play.
	Label string
	// CallerID is what a call placed as this line presents, in E.164, or empty
	// for the trunk default. This is what the menu reads aloud, because it is
	// the one fact the operator is choosing between — and the only one that
	// can be spoken without recording anything.
	CallerID string
	// Trunk is the provider a call placed as this line leaves by — a
	// trunks.toml id — or empty for the dialplan's default trunk, which is
	// every install with one provider. Never spoken: the person at the keypad
	// is choosing an identity, and which company carries it is the part that
	// has to follow from that choice rather than be made separately.
	Trunk string
}

// ConsoleDeps wires a Console to the world.
type ConsoleDeps struct {
	ARI ARI
	Log *slog.Logger
	Cfg Config
	// Lines is read once per console call, so an operator who edits a policy
	// file gets the new caller ID on the next *4 without a restart — the same
	// reason Deps.Policy is a func.
	Lines func() []ConsoleLine
	// Calls receives one record per console call, exactly as a Session's does
	// and from the same single teardown path. Nil disables it.
	//
	// There is deliberately no Notifier here. The webhook's two events are
	// "the house is ringing" and "the call ended", and an outbound call rings
	// nothing in this house and ends somewhere doorman cannot see. Sending one
	// anyway would mean announcing a call that is not happening here.
	Calls Recorder
	// OnFinished lets the router drop this console's registration.
	OnFinished func(*Console)
}

// Console is one *4 call, start to finish.
type Console struct {
	// ID is a short handle used in logs.
	ID string
	// ChannelID is the handset's Asterisk channel.
	ChannelID string

	deps ConsoleDeps
	log  *slog.Logger

	ctx    context.Context
	cancel context.CancelFunc
	events chan event
	// detached: the channel left our app alive, which for a console means the
	// call was placed and the dialplan owns it now. The final hangup must be
	// suppressed or the outbound call dies the instant it is made.
	detached atomic.Bool

	pbSeq int

	// rec accumulates the call log entry, written from this goroutine only and
	// never read back — an output, not state, exactly as Session.rec is.
	rec       calls.Record
	startedAt time.Time
}

func NewConsole(channelID string, deps ConsoleDeps) *Console {
	ctx, cancel := context.WithCancel(context.Background())
	id := shortID(channelID)
	// A console call begins when the handset reaches us, not when its goroutine
	// happens to be scheduled. No injected clock: nothing here turns on the
	// time of day, so there is no window to make testable.
	started := time.Now()
	return &Console{
		ID:        id,
		ChannelID: channelID,
		deps:      deps,
		log:       deps.Log.With("consoleId", id),
		ctx:       ctx,
		cancel:    cancel,
		events:    make(chan event, 64),
		startedAt: started,
		// Abandoned is the zero outcome here for the same reason it is inbound:
		// a handset that hangs up mid-menu reaches cleanup by cancellation and
		// nothing else, and it should be recorded truthfully without any exit
		// path having to remember to say so.
		rec: calls.Record{
			ID:        id,
			Start:     started,
			Direction: calls.DirectionOutbound,
			Outcome:   calls.OutcomeAbandoned,
		},
	}
}

// ── Inputs from the event router ─────────────────────────────────────────

// CallerGone cancels the console: the handset's channel was destroyed.
func (c *Console) CallerGone() { c.cancel() }

// CallerLeft handles the handset's channel leaving Stasis while still alive.
// After the call is placed that is the normal, successful ending — the channel
// is in [outbound-console] talking to a trunk, and hanging it up here would
// drop a call that has just been connected. Invariant 8: StasisEnd is not
// ChannelDestroyed, and only the dead may be hung up by cleanup.
func (c *Console) CallerLeft() {
	c.detached.Store(true)
	c.cancel()
}

func (c *Console) Dtmf(digit string)          { c.post(event{evDtmf, digit}) }
func (c *Console) PlaybackFinished(id string) { c.post(event{evPlaybackDone, id}) }

func (c *Console) post(ev event) {
	select {
	case c.events <- ev:
	default:
		c.log.Warn("event dropped", "kind", ev.kind)
	}
}

// ── The call ─────────────────────────────────────────────────────────────

// Run drives the whole console call and returns when it is over. Every exit
// funnels through the deferred cleanup, including the operator hanging up
// mid-menu, which arrives as cancellation and nothing else.
func (c *Console) Run() {
	defer c.cleanup()

	lines := c.deps.Lines()
	if len(lines) > consoleMaxLines {
		c.log.Warn("more lines than the console menu can offer, listing the first few",
			"lines", len(lines), "offered", consoleMaxLines)
		lines = lines[:consoleMaxLines]
	}
	if len(lines) == 0 {
		// Not reachable through the daemon, which always has a default line.
		c.log.Error("outbound console has no lines to offer")
		return
	}

	line, ok := c.chooseLine(lines)
	if !ok {
		return
	}
	// The confirmation comes here, between the choice and the number, rather
	// than after it. "Calling as this number" is only useful while there is
	// still time to act on it, and hearing it before you have dialled anything
	// means a wrong choice costs a keypress rather than a call to a customer
	// who then sees the wrong number.
	c.confirm(line)

	dialled, ok := c.takeNumber()
	if !ok {
		return
	}
	c.place(line, dialled)
}

// chooseLine reads the menu and takes a digit.
func (c *Console) chooseLine(lines []ConsoleLine) (ConsoleLine, bool) {
	menu := make([]string, 0, len(lines)*2)
	for i, l := range lines {
		menu = append(menu, fmt.Sprintf("digits:%d", i+1))
		// A line with no outbound_cid has nothing to say for itself: it
		// presents whatever the trunk defaults to, and reading its [line]
		// number aloud would name a number the call will not present.
		if spoken := spokenDigits(l.CallerID); spoken != "" {
			menu = append(menu, "digits:"+spoken)
		}
	}

	for attempt := 1; attempt <= consoleAttempts; attempt++ {
		seed := c.say(true, menu...)
		if c.ctx.Err() != nil {
			return ConsoleLine{}, false
		}

		digit, reason := collectDigits(c.ctx, c.events, collectSpec{
			First: c.deps.Cfg.FirstDigitTimeout,
			Inter: c.deps.Cfg.InterDigitTimeout,
			Max:   1,
			Seed:  seed,
		})
		switch reason {
		case endGone:
			return ConsoleLine{}, false
		case endNothing:
			c.log.Info("nobody chose a line, ending the console call")
			c.gaveUp("no-digits")
			c.say(false, sayGoodbye)
			return ConsoleLine{}, false
		}

		// A bare # returns no digit at all, which is why the length is checked
		// before the byte.
		if len(digit) == 1 {
			if n := int(digit[0] - '0'); n >= 1 && n <= len(lines) {
				chosen := lines[n-1]
				c.log.Info("line chosen", "line", chosen.Name, "label", chosen.Label,
					"presents", maskCID(chosen.CallerID), "trunk", orTrunkDefault(chosen.Trunk))
				c.rec.Line = chosen.Name
				return chosen, true
			}
		}
		c.log.Info("no such line on the menu", "attempt", attempt)
		c.say(false, sayInvalid)
	}
	c.gaveUp("too-many-attempts")
	c.say(false, sayGoodbye)
	return ConsoleLine{}, false
}

// confirm reads back the number this call will present. It is the whole point
// of the console being spoken rather than a dial prefix: a wrong choice is
// caught here, by the person who made it, instead of by a customer who saves
// the wrong number.
func (c *Console) confirm(line ConsoleLine) {
	spoken := spokenDigits(line.CallerID)
	if spoken == "" {
		// Nothing to confirm: this line has no outbound identity of its own,
		// so the call presents the trunk default and saying a number would be
		// a lie. `doorman check` is where that gets pointed out.
		return
	}
	c.say(false, sayNumberIs, "digits:"+spoken)
}

// takeNumber collects the number to dial: variable length, # to finish, and a
// pause counts as finished too.
func (c *Console) takeNumber() (string, bool) {
	for attempt := 1; attempt <= consoleAttempts; attempt++ {
		seed := c.say(true, sayEnterNumber, sayThenPound, sayBeep)
		if c.ctx.Err() != nil {
			return "", false
		}

		// dialledDigits is a caller ID in every sense that matters — it
		// identifies a human being and it will appear on somebody's bill. It
		// is named so that tools/nologsecrets treats it as one.
		dialledDigits, reason := collectDigits(c.ctx, c.events, collectSpec{
			First: c.deps.Cfg.FirstDigitTimeout,
			Inter: c.deps.Cfg.InterDigitTimeout,
			Max:   maxDialledDigits,
			Seed:  seed,
		})
		switch reason {
		case endGone:
			return "", false
		case endNothing:
			c.log.Info("no number dialled, ending the console call")
			c.gaveUp("no-digits")
			c.say(false, sayGoodbye)
			return "", false
		}

		if emergencyNumbers[dialledDigits] {
			c.refuseEmergency(dialledDigits)
			return "", false
		}
		if len(dialledDigits) >= minDialledDigits {
			// Recorded here, where a complete entry has been accepted as a
			// destination — never in the loop below, so a fumbled half-number
			// is forgotten rather than logged. See calls.Record.Dialled.
			c.rec.Dialled = dialledDigits
			return dialledDigits, true
		}
		c.log.Info("too few digits to be a number", "digits", len(dialledDigits),
			"attempt", attempt)
		c.say(false, sayInvalid)
	}
	c.gaveUp("too-many-attempts")
	c.say(false, sayGoodbye)
	return "", false
}

// refuseEmergency ends the call as fast as it can.
//
// There is no recording that says "hang up and dial 911 directly", and the
// project will not add one: a pack cannot be required for a mechanism to work.
// So the console does the next best thing and gets out of the way — one beep,
// then the channel is released, which returns the handset to dial tone in
// under a second. Every handset can dial 911 from there and it goes out by the
// trunk whose address is registered, which is the outcome that matters.
//
// The log line is for the operator afterwards, not the caller now — and so is
// the record: this is the one thing the console does that somebody may need to
// account for later, so the number is kept even though no call was placed.
func (c *Console) refuseEmergency(dialledDigits string) {
	c.log.Warn("refused an emergency number on the outbound console — " +
		"emergency calls must leave by the trunk whose address is registered, " +
		"so they are dialled directly from a handset and never placed as another line")
	c.rec.Dialled = dialledDigits
	c.gaveUp("emergency-refused")
	c.say(false, sayBeep)
}

// place sets the caller ID and hands the channel to the dialplan.
func (c *Console) place(line ConsoleLine, dialledDigits string) {
	if c.ctx.Err() != nil {
		return
	}

	// Both set unconditionally, including to empty, and both from the same
	// line. A handset that carries its own default from the generated PJSIP
	// config already has these set; leaving either in place when the operator
	// has explicitly chosen another line would place the call as a pairing
	// nobody configured — the business caller ID down the home trunk, or the
	// reverse. Empty is a real answer for both: it means "whatever the dialplan
	// defaults to", which is exactly what an unclaimed handset does.
	//
	// Either failing refuses the call rather than placing it half-configured.
	// A caller ID the carrying trunk does not own is rejected or silently
	// rewritten by the provider, and neither is visible from this end.
	for _, v := range []struct{ name, value, reason string }{
		{outboundTrunkVar, line.Trunk, "trunk-failed"},
		{outboundCIDVar, line.CallerID, "cid-failed"},
	} {
		if err := c.deps.ARI.SetChannelVar(c.ctx, c.ChannelID, v.name, v.value); err != nil {
			c.log.Error("could not set the outbound channel variables, refusing to place the call",
				"line", line.Name, "variable", v.name, "err", err)
			c.gaveUp(v.reason)
			c.say(false, sayInvalid, sayGoodbye)
			return
		}
	}

	c.log.Info("placing an outbound call", "line", line.Name,
		"presents", maskCID(line.CallerID), "trunk", orTrunkDefault(line.Trunk),
		"number", redactDialled(dialledDigits, c.deps.Cfg.DefaultCountryCode))

	// From here the channel belongs to the dialplan. Invariant 8, and the same
	// release sendToVoicemail performs: detach first, undo it if the handoff
	// itself fails, and never hang the channel up afterwards.
	c.detached.Store(true)
	c.rec.Outcome = calls.OutcomePlaced
	if err := c.deps.ARI.ContinueToDialplan(c.ctx, c.ChannelID, outboundContext, dialledDigits, 1); err != nil {
		c.detached.Store(false)
		c.log.Error("outbound handoff failed", "context", outboundContext, "err", err)
		c.gaveUp("handoff-failed")
		c.say(false, sayInvalid, sayGoodbye)
	}
}

// gaveUp records an ending that is not a placed call.
//
// Dismissed rather than a fifth outcome: "doorman ended it deliberately, and
// reason says why" is direction-neutral, and it is what the inbound side
// already does with an infrastructure failure — a voicemail handoff that fails
// is recorded as a dismissal too. The reasons are the console's own vocabulary
// where it has one (emergency-refused, handoff-failed) and the lobby's where
// they mean the same thing (no-digits, too-many-attempts).
func (c *Console) gaveUp(reason string) {
	c.rec.Outcome, c.rec.Reason = calls.OutcomeDismissed, reason
}

// ── Speaking ─────────────────────────────────────────────────────────────

// say plays a sequence of media URIs in order. With bargeIn, the first DTMF
// digit stops the sequence and is returned as the caller's first keypress;
// otherwise digits during playback are ignored.
//
// One playback per URI rather than one comma-separated request, so a clip
// Asterisk cannot find costs exactly that clip. This is the composite prompt
// in its smallest useful form: a spoken frame around a number Asterisk reads
// natively, with no synthesis anywhere near the call path (invariant 7).
func (c *Console) say(bargeIn bool, media ...string) string {
	for _, m := range media {
		if digit := c.play(m, bargeIn); digit != "" {
			return digit
		}
		if c.ctx.Err() != nil {
			return ""
		}
	}
	return ""
}

// play starts one media URI and waits for it to finish, returning a barged
// digit or "".
func (c *Console) play(media string, bargeIn bool) string {
	if c.ctx.Err() != nil {
		return ""
	}
	c.pbSeq++
	pbID := fmt.Sprintf("cn-%s-%d", c.ID, c.pbSeq)

	if err := c.deps.ARI.Play(c.ctx, c.ChannelID, media, pbID); err != nil {
		// A missing clip degrades to silence, never a stuck call — the same
		// rule the lobby has always applied to a missing prompt.
		c.log.Warn("playback failed", "media", media, "err", err)
		return ""
	}

	for {
		select {
		case ev := <-c.events:
			switch ev.kind {
			case evPlaybackDone:
				if ev.value == pbID {
					return ""
				}
			case evDtmf:
				if bargeIn {
					_ = c.deps.ARI.StopPlayback(c.ctx, pbID)
					return ev.value
				}
			}
		case <-c.ctx.Done():
			return ""
		}
	}
}

// cleanup is the single teardown path, on a fresh context because the console
// context is usually already cancelled by the time we get here.
func (c *Console) cleanup() {
	c.cancel()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if !c.detached.Load() {
		_ = c.deps.ARI.Hangup(ctx, c.ChannelID)
	}

	// One record per console call, from the single teardown path, exactly as a
	// Session does it — including the handset hanging up mid-menu, which gets
	// here by cancellation and no other route. Before OnFinished for the same
	// reason: nothing may be sequenced behind the thing that says this console
	// is done.
	//
	// MS is how long the console had the handset, not how long the call lasted:
	// once ContinueToDialplan returns, doorman is out of it and cannot know.
	c.rec.MS = time.Since(c.startedAt).Milliseconds()
	if c.deps.Calls != nil {
		c.deps.Calls.Post(c.rec)
	}

	if c.deps.OnFinished != nil {
		c.deps.OnFinished(c)
	}
	c.log.Info("console finished", "outcome", c.rec.Outcome, "line", c.rec.Line)
}

// ── Helpers ──────────────────────────────────────────────────────────────

// spokenDigits turns +15125550142 into 15125550142, which is what Asterisk's
// digits: media URI reads aloud one digit at a time. Empty in, empty out —
// there is nothing to say about a line with no outbound identity.
func spokenDigits(e164 string) string {
	var b strings.Builder
	for _, r := range e164 {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// redactDialled is the only form of a dialled number that may be logged.
//
// Invariant 1 is about caller IDs, and a number somebody dialled is one: it
// names a human being just as precisely as an inbound number does, and it ends
// up in journald the same way. Normalising first matters — policy.Redact keeps
// the first five characters, which is "+1512" on an E.164 number and most of
// the area code plus the exchange on a bare ten-digit string. A number that
// will not normalise is logged as a length and nothing else.
//
// tools/nologsecrets recognises this as a narrowing function by name, which is
// what makes a dialled number loggable at all.
func redactDialled(dialledDigits, countryCode string) string {
	if e164 := policy.E164OrEmpty(dialledDigits, countryCode); e164 != "" {
		return policy.Redact(e164)
	}
	return fmt.Sprintf("(%d digits, not a number we could normalise)", len(dialledDigits))
}

// maskCID is how an outbound caller ID reaches a log line.
//
// It is the house's own number rather than a stranger's, and `doorman check`
// prints it in full — but invariant 1 is about what accumulates in journald,
// and a line's DID identifies the household as surely as a caller's number
// identifies the caller. The line *name* is what makes these log lines useful
// anyway, and that is never redacted. Empty is named rather than blank,
// because "" in a log would read as "no caller ID at all" when it means
// "whatever the trunk sends".
func maskCID(cid string) string {
	if cid == "" {
		return "(the trunk default)"
	}
	return policy.Redact(cid)
}

// orTrunkDefault names the provider a call leaves by. A trunk id is inventory
// rather than a caller identifier, so it is logged in full — but empty is
// named rather than blank, because "" in a log would read as "no trunk at all"
// when it means the dialplan's DEFAULT_TRUNK, which is every one-provider box.
func orTrunkDefault(trunk string) string {
	if trunk == "" {
		return "(the dialplan default)"
	}
	return trunk
}
