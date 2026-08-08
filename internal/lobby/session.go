// Package lobby holds every decision this system makes about who gets
// connected. One inbound call is one Session running in one goroutine;
// Asterisk (via the ARI interface below) handles SIP, RTP, and DTMF
// detection, and this package decides what to do with them.
package lobby

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"callmemaybe/internal/calls"
	"callmemaybe/internal/notify"
	"callmemaybe/internal/policy"
)

// ARI is the surface of the Asterisk client the lobby actually uses. It is an
// interface so tests can drive the state machine with a fake — no Asterisk,
// no network, no trunk.
type ARI interface {
	AppName() string
	Play(ctx context.Context, channelID, media, playbackID string) error
	StopPlayback(ctx context.Context, playbackID string) error
	Ring(ctx context.Context, channelID string) error
	RingStop(ctx context.Context, channelID string) error
	Hangup(ctx context.Context, channelID string) error
	Originate(ctx context.Context, p OriginateParams) (channelID string, err error)
	SetChannelVar(ctx context.Context, channelID, name, value string) error
	ContinueToDialplan(ctx context.Context, channelID, dpContext, extension string, priority int) error
	CreateBridge(ctx context.Context) (bridgeID string, err error)
	AddToBridge(ctx context.Context, bridgeID, channelID string) error
	DestroyBridge(ctx context.Context, bridgeID string) error
}

// OriginateParams mirrors ari.OriginateParams without importing it, keeping
// this package free of any dependency on the concrete client.
type OriginateParams struct {
	Endpoint   string
	AppArgs    string
	CallerID   string
	Timeout    int
	Originator string
}

// Config is the subset of runtime configuration the state machine needs.
type Config struct {
	DefaultCountryCode string
	ExtensionLength    int
	FirstDigitTimeout  time.Duration
	InterDigitTimeout  time.Duration
	RingTimeout        time.Duration
	// RingCycle is the length of one ring cadence, used only when a ladder
	// step was written as "rings = N". Production: 6s. Tests: milliseconds.
	RingCycle      time.Duration
	MaxPinAttempts int
	RedactCallerID bool
	// MaxConcurrentCalls is read by the event router, not the state machine,
	// but lives here so there is one Config the daemon threads through.
	MaxConcurrentCalls int
}

// Deps wires a Session to the world. Policy is a func so each session
// captures the store's current policy exactly once at call start — a mid-call
// reload never changes the rules under a caller.
// A Recorder is the call log sink. An interface rather than *calls.Writer
// for the same reason ARI is one: the state machine has to be drivable by a
// test that can see what it recorded.
type Recorder interface{ Post(calls.Record) }

// A Notifier is the event webhook sink.
//
// Two interfaces rather than one with two implementations, because the two
// sinks are not the same shape and forcing them together would cost something
// real. A Recorder is posted to exactly once, from the single teardown path —
// that is what makes "one record per call" true, and a Recorder that also
// fired at ring time would break it. A Notifier fires at both ends, because a
// notification is only useful while the phone is still ringing. They also
// carry different payloads: a record is a full history for an operator, an
// event is a small statement of fact for another program.
type Notifier interface{ Post(notify.Event) }

type Deps struct {
	ARI     ARI
	Policy  func() *policy.Policy
	Limiter *RateLimiter
	Prompts *Prompts
	Log     *slog.Logger
	Cfg     Config
	// Calls receives one record per completed call. Nil disables the call
	// log entirely, which is why every use is behind a nil check rather
	// than a config flag.
	Calls Recorder
	// Notify receives call events for the house to react to. Nil disables
	// the webhook, same as Calls — absent config means the feature is off.
	Notify Notifier

	// OnLegCreated lets the event router map an originated leg's channel ID
	// back to this session.
	OnLegCreated func(legChannelID string, s *Session)
	// OnFinished lets the router drop all of this session's registrations.
	OnFinished func(s *Session)
	// Now is the clock; nil means time.Now. Injected so afterhours windows
	// are testable.
	Now func() time.Time
}

type evKind int

const (
	evDtmf evKind = iota
	evPlaybackDone
	evLegAnswered
	evLegGone
)

type event struct {
	kind  evKind
	value string
}

// Session is one inbound call, start to finish. All state is owned by the
// single goroutine running Run; the exported methods below are the only way
// in, and they communicate over a channel. The caller hanging up cancels the
// session context, which unwinds every wait in Run — there is no state flag
// to check because cancellation *is* the state.
type Session struct {
	// ID is a short handle used in logs and as the appArgs correlation token
	// on originated legs.
	ID string
	// ChannelID is the inbound caller's Asterisk channel.
	ChannelID string

	callerRaw  string
	callerE164 string

	deps Deps
	log  *slog.Logger
	pol  *policy.Policy

	ctx    context.Context
	cancel context.CancelFunc
	events chan event
	// detached: the caller's channel left our app alive (transfer); the
	// final cleanup hangup must be suppressed. Written by the event router,
	// read by the session goroutine, hence atomic.
	detached atomic.Bool

	bridgeID string
	legs     map[string]bool
	pbSeq    int

	// rec accumulates the call log entry. Written from the session
	// goroutine only, and never read back by the state machine — it is an
	// output, not state. Cancellation remains the only state there is.
	rec       calls.Record
	startedAt time.Time
}

func NewSession(channelID, callerNumber string, deps Deps) *Session {
	ctx, cancel := context.WithCancel(context.Background())

	pol := deps.Policy()
	e164 := policy.E164OrEmpty(callerNumber, deps.Cfg.DefaultCountryCode)

	id := shortID(channelID)
	// The clock is read here rather than in Run: a call begins when it
	// arrives, not when its goroutine happens to be scheduled.
	started := time.Now()
	if deps.Now != nil {
		started = deps.Now()
	}

	return &Session{
		ID:         id,
		ChannelID:  channelID,
		callerRaw:  callerNumber,
		callerE164: e164,
		deps:       deps,
		// The caller attribute rides on every line this session ever logs, so
		// redaction happens here and is the default; LOG_REDACT_CALLER_ID=false
		// is an explicit opt-out for debugging, never the shipping state.
		//
		// Inlined rather than assigned to a variable on purpose: the narrowing
		// has to be visible at the log site for tools/nologsecrets to verify it,
		// and this attribute is precisely the one that got it wrong before.
		log: deps.Log.With("callId", id, "caller",
			policy.RedactCaller(e164, callerNumber, !deps.Cfg.RedactCallerID)),
		pol:    pol,
		ctx:    ctx,
		cancel: cancel,
		// Buffered so the event router never blocks on a busy session. 64 is
		// far beyond anything a single call can generate in flight.
		events: make(chan event, 64),
		legs:   make(map[string]bool),
		// Abandoned is the zero outcome, so a caller who hangs up mid-lobby
		// is recorded truthfully with nobody having to remember to set it.
		startedAt: started,
		rec: calls.Record{
			ID:      id,
			Start:   started,
			Caller:  e164,
			Outcome: calls.OutcomeAbandoned,
		},
	}
}

func shortID(channelID string) string {
	var b strings.Builder
	for _, r := range channelID {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			b.WriteRune(r)
		}
	}
	s := b.String()
	if len(s) > 10 {
		s = s[len(s)-10:]
	}
	return s
}

// ── Inputs from the event router ─────────────────────────────────────────

// CallerGone cancels the session: the inbound channel was destroyed.
// Everything Run is waiting on unwinds through ctx.Done.
func (s *Session) CallerGone() { s.cancel() }

// CallerLeft handles the caller's channel leaving the Stasis application
// while possibly still alive — which is what a handset-initiated transfer
// looks like from here: the house presses Transfer, Asterisk moves the
// caller's leg into the dialplan at the new target, and we get StasisEnd.
// The session ends, but the final hangup is suppressed: the channel belongs
// to the dialplan now, and hanging it up would kill the transferred call.
func (s *Session) CallerLeft() {
	s.detached.Store(true)
	s.cancel()
}

func (s *Session) Dtmf(digit string)          { s.post(event{evDtmf, digit}) }
func (s *Session) PlaybackFinished(id string) { s.post(event{evPlaybackDone, id}) }
func (s *Session) LegAnswered(chID string)    { s.post(event{evLegAnswered, chID}) }
func (s *Session) LegGone(chID string)        { s.post(event{evLegGone, chID}) }

func (s *Session) post(ev event) {
	select {
	case s.events <- ev:
	default:
		// Only reachable if a session goroutine is wedged; dropping is
		// strictly better than blocking the event router for every call.
		s.log.Warn("event dropped", "kind", ev.kind)
	}
}

func (s *Session) limitKey() string {
	if s.callerE164 != "" {
		return s.callerE164
	}
	// Anonymous callers share one bucket by design: one persistent blocked
	// caller makes the lobby stricter for every withheld number, which is the
	// intended trade.
	return "anonymous"
}

func (s *Session) now() time.Time {
	if s.deps.Now != nil {
		return s.deps.Now()
	}
	return time.Now()
}

// notify hands one event to the webhook. It is a projection of the call
// record rather than a payload assembled here, so the webhook cannot carry
// anything the record could not — which is what keeps entered digits out of
// it structurally instead of by memory.
//
// Post never blocks and this is the only thing that touches the network on
// the call path, so a dead Home Assistant is invisible to the caller.
func (s *Session) notify(kind string) {
	if s.deps.Notify == nil {
		return
	}
	s.deps.Notify.Post(notify.FromRecord(kind, s.now(), s.rec))
}

func (s *Session) callerNumberForDisplay() string {
	if s.callerE164 != "" {
		return s.callerE164
	}
	return s.callerRaw
}

// ── The call ─────────────────────────────────────────────────────────────

// Run drives the whole call and returns when it is over. Every exit path —
// dismissal, bridged call ending, caller hanging up mid-anything — funnels
// through the deferred cleanup.
func (s *Session) Run() {
	defer s.cleanup()

	if known, ok := s.pol.LookupCaller(s.callerE164); ok {
		s.log.Info("known caller, welcoming", "name", known.Name)
		s.rec.Known = known.Name
		s.deps.Limiter.Success(s.limitKey())
		s.play(PromptWelcomeKnown, false)
		if s.ctx.Err() != nil {
			return
		}
		s.runPlan(s.pol.HousePlan(), known.Name)
		return
	}

	if s.deps.Limiter.Blocked(s.limitKey(), time.Now()) {
		s.log.Warn("caller rate limited, dismissing without greeting")
		s.dismiss("rate-limited")
		return
	}

	s.log.Info("unknown caller, opening lobby")
	// Digits during the greeting barge in: a caller who already knows their
	// extension should not have to sit through the pitch.
	seed := s.play(PromptLobbyGreeting, true)
	if s.ctx.Err() != nil {
		return
	}
	s.collect(seed)
}

// play starts a prompt and waits for it to finish. With bargeIn, a DTMF digit
// stops the prompt and is returned as the first collected digit; otherwise
// digits during playback are ignored. Returns "" when the prompt completed
// (or failed, or the session ended) without a barge.
func (s *Session) play(name Prompt, bargeIn bool) string {
	if s.ctx.Err() != nil {
		return ""
	}
	s.pbSeq++
	pbID := fmt.Sprintf("pb-%s-%d", s.ID, s.pbSeq)

	if err := s.deps.ARI.Play(s.ctx, s.ChannelID, s.deps.Prompts.URI(name), pbID); err != nil {
		// A missing prompt file should degrade to silence, not a stuck call.
		s.log.Warn("playback failed", "prompt", string(name), "err", err)
		return ""
	}

	for {
		select {
		case ev := <-s.events:
			switch ev.kind {
			case evPlaybackDone:
				if ev.value == pbID {
					return ""
				}
			case evDtmf:
				if bargeIn {
					_ = s.deps.ARI.StopPlayback(s.ctx, pbID)
					return ev.value
				}
			}
		case <-s.ctx.Done():
			return ""
		}
	}
}

// collect gathers an extension. The clock is generous where humans fumble and
// strict where machines probe: FirstDigitTimeout to start dialling, then
// InterDigitTimeout between digits. seed is a digit already captured by
// barge-in during the greeting.
func (s *Session) collect(seed string) {
	cfg := s.deps.Cfg
	attempts := 0
	digits := ""

	timer := time.NewTimer(cfg.FirstDigitTimeout)
	defer timer.Stop()
	reset := func(d time.Duration) {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(d)
	}

	target := cfg.ExtensionLength
	if s.pol.PinLength > 0 {
		// Uniform PIN length: fire the moment the last digit lands instead
		// of waiting out the inter-digit timer.
		target = s.pol.PinLength
	}

	// handle returns true when the call has moved on (ringing or dismissed).
	handle := func(d string) bool {
		switch {
		case d == "*":
			digits = ""
			reset(cfg.InterDigitTimeout)
		case d == "#":
			return s.evaluate(&digits, &attempts, reset)
		case len(d) == 1 && d[0] >= '0' && d[0] <= '9':
			digits += d
			if len(digits) >= target {
				return s.evaluate(&digits, &attempts, reset)
			}
			reset(cfg.InterDigitTimeout)
		}
		return false
	}

	if seed != "" && handle(seed) {
		return
	}

	for {
		select {
		case ev := <-s.events:
			if ev.kind != evDtmf {
				continue
			}
			if handle(ev.value) {
				return
			}
		case <-timer.C:
			s.log.Info("dial window elapsed", "digits", len(digits))
			s.dismiss("no-digits")
			return
		case <-s.ctx.Done():
			return
		}
	}
}

// evaluate checks a completed entry. Exact match against the policy map —
// extensions are credentials, so no prefix matching and no fuzz. Returns true
// when the collect loop should end.
func (s *Session) evaluate(digits *string, attempts *int, reset func(time.Duration)) bool {
	entered := *digits
	*digits = ""

	if ext, ok := s.pol.LookupExtension(entered); ok {
		s.deps.Limiter.Success(s.limitKey())
		// The label and the verdict, never `entered`.
		s.rec.Extension, s.rec.PIN = ext.Label, "valid"
		if ext.Afterhours.Active(s.now()) {
			// Quiet hours. Either the call is redirected somewhere that is
			// awake — homework hours sending the kids' line to the adults, a
			// rotating night shift — or, with no redirect configured, it goes
			// straight to voicemail, which is the historical behaviour.
			if len(ext.AfterhoursPlan.Steps) > 0 {
				s.log.Info("extension in afterhours, redirecting",
					"extension", ext.Label)
				s.runPlan(ext.AfterhoursPlan, ext.Label)
				return true
			}
			s.log.Info("extension in afterhours, sending to voicemail",
				"extension", ext.Label)
			s.sendToVoicemail(ext.Plan.Mailbox)
			return true
		}
		s.log.Info("extension accepted", "extension", ext.Label)
		s.runPlan(ext.Plan, ext.Label)
		return true
	}

	*attempts++
	s.rec.PIN, s.rec.Attempts = "invalid", *attempts
	failures := s.deps.Limiter.Failure(s.limitKey(), time.Now())
	// The entered digits are never logged — a near-miss is almost a
	// credential.
	s.log.Warn("invalid extension", "attempt", *attempts, "windowFailures", failures)

	if *attempts > s.deps.Cfg.MaxPinAttempts {
		s.dismiss("too-many-attempts")
		return true
	}

	s.play(PromptInvalidExtension, false)
	if s.ctx.Err() != nil {
		return true
	}
	reset(s.deps.Cfg.FirstDigitTimeout)
	return false
}

// runPlan executes a ring plan: each step rings its endpoints for its
// timeout, escalating to the next on no-answer — the ringer ladder. The first
// handset to answer anywhere wins. When every step is exhausted, the caller
// goes to the plan's mailbox, or is dismissed politely if there is none.
func (s *Session) runPlan(plan policy.RingPlan, label string) {
	if len(plan.Steps) == 0 {
		s.log.Error("empty ring plan", "label", label)
		s.fallback(plan.Mailbox)
		return
	}

	bridgeID, err := s.deps.ARI.CreateBridge(s.ctx)
	if err != nil {
		s.log.Error("bridge create failed", "err", err)
		return
	}
	s.bridgeID = bridgeID
	if err := s.deps.ARI.AddToBridge(s.ctx, bridgeID, s.ChannelID); err != nil {
		s.log.Error("could not add caller to bridge", "err", err)
		return
	}
	// Inject ringback so the caller does not sit in silence. It spans the
	// whole ladder — escalation is invisible to the caller.
	_ = s.deps.ARI.Ring(s.ctx, s.ChannelID)

	// The house is ringing now, which is the moment an announcement is worth
	// anything: "call from Grandma" while somebody can still reach a handset
	// beats the same sentence after it stopped. Fired here rather than at
	// StasisStart because this is the single point both routes through — a
	// welcomed known caller and a stranger who dialled a valid extension —
	// and by now the record carries the name or the label that makes the
	// event useful. A caller who is dismissed rings nothing and gets no
	// ringing event, which is the truth.
	s.notify(notify.EventRinging)

	callerID := s.pol.FormatCallerID(label, s.callerNumberForDisplay())

	for i, step := range plan.Steps {
		timeout := step.Timeout
		if timeout == 0 && step.Rings > 0 {
			cycle := s.deps.Cfg.RingCycle
			if cycle == 0 {
				cycle = 6 * time.Second // standard US cadence
			}
			timeout = time.Duration(step.Rings) * cycle
		}
		if timeout == 0 {
			timeout = s.deps.Cfg.RingTimeout
		}
		if answered := s.ringStep(step.Endpoints, label, callerID, timeout, i); answered {
			return
		}
		if s.ctx.Err() != nil {
			return
		}
	}

	s.log.Info("ladder exhausted", "label", label, "steps", len(plan.Steps))
	s.fallback(plan.Mailbox)
}

// ringStep rings one stage. Returns true when a leg answered (the call is
// now bridged and over when this returns); false means escalate.
func (s *Session) ringStep(endpoints []string, label, callerID string, timeout time.Duration, stage int) bool {
	stageStart := s.now()
	dialled := make(map[string]string, len(endpoints)) // legID -> endpoint
	// rang records this rung for the call log. Every exit below takes it, so
	// a ladder in the log is the ladder as actually walked, with the timings
	// that say whether a stage expires before anyone can cross a room.
	rang := func(result string) {
		s.rec.Stages = append(s.rec.Stages, calls.Stage{
			Handsets: endpoints,
			MS:       s.now().Sub(stageStart).Milliseconds(),
			Result:   result,
		})
	}

	// Escalation is a handoff, not a pile-on: the previous stage has already
	// been hung up, so s.legs holds only this stage.
	for _, endpoint := range endpoints {
		legID, err := s.deps.ARI.Originate(s.ctx, OriginateParams{
			Endpoint: endpoint,
			AppArgs:  "leg," + s.ID,
			CallerID: callerID,
			// A few seconds past our own timer so the session timer, not the
			// endpoint's, decides escalation.
			Timeout:    int(timeout.Seconds()) + 5,
			Originator: s.ChannelID,
		})
		if err != nil {
			// One dead handset must not abort the stage.
			s.log.Warn("originate failed", "endpoint", endpoint, "err", err)
			continue
		}
		s.legs[legID] = true
		dialled[legID] = endpoint
		s.deps.OnLegCreated(legID, s)
	}

	if len(s.legs) == 0 {
		s.log.Warn("no legs could be originated", "stage", stage+1)
		rang("failed")
		return false
	}
	s.log.Info("ringing", "label", label, "stage", stage+1, "legs", len(s.legs))

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	for {
		select {
		case ev := <-s.events:
			switch ev.kind {
			case evLegAnswered:
				s.rec.Outcome, s.rec.AnsweredBy = calls.OutcomeAnswered, dialled[ev.value]
				rang("answered")
				s.bridged(ev.value)
				return true
			case evLegGone:
				delete(s.legs, ev.value)
				if len(s.legs) == 0 {
					s.log.Info("every leg failed before answer", "stage", stage+1)
					rang("failed")
					return false
				}
			}
		case <-timer.C:
			s.log.Info("stage timed out", "stage", stage+1)
			rang("timeout")
			s.hangupLegs()
			return false
		case <-s.ctx.Done():
			rang("abandoned")
			return true // session over; runPlan checks ctx and stops
		}
	}
}

func (s *Session) hangupLegs() {
	for legID := range s.legs {
		_ = s.deps.ARI.Hangup(s.ctx, legID)
		delete(s.legs, legID)
	}
}

// fallback is where an unanswered caller lands: the mailbox when one is
// configured, otherwise a polite dismissal.
func (s *Session) fallback(mailbox string) {
	if mailbox != "" {
		s.sendToVoicemail(mailbox)
		return
	}
	s.dismiss("no-answer")
}

// sendToVoicemail hands the caller to the dialplan's voicemail-drop context
// with MAILBOX set. This is a release, not a hangup: the session detaches
// exactly as it does for a transfer, because after ContinueToDialplan the
// channel belongs to Asterisk's VoiceMail() and killing it would cut the
// caller off mid-greeting.
func (s *Session) sendToVoicemail(mailbox string) {
	s.rec.Outcome, s.rec.Mailbox = calls.OutcomeVoicemail, mailbox
	if s.ctx.Err() != nil {
		return
	}
	s.log.Info("sending caller to voicemail", "mailbox", mailbox)

	s.hangupLegs()
	if s.bridgeID != "" {
		_ = s.deps.ARI.RingStop(s.ctx, s.ChannelID)
		_ = s.deps.ARI.DestroyBridge(s.ctx, s.bridgeID)
		s.bridgeID = ""
	}

	if err := s.deps.ARI.SetChannelVar(s.ctx, s.ChannelID, "MAILBOX", mailbox); err != nil {
		s.log.Error("could not set mailbox variable", "err", err)
	}
	s.detached.Store(true)
	if err := s.deps.ARI.ContinueToDialplan(s.ctx, s.ChannelID, "voicemail-drop", "s", 1); err != nil {
		// The handoff failed; the caller is still ours after all.
		s.detached.Store(false)
		s.log.Error("voicemail handoff failed", "err", err)
		s.dismiss("no-answer")
	}
}

// bridged joins the winning leg and holds the call until either side hangs up.
// First answer wins; every sibling is hung up, and a late answer that raced
// its own cancellation is hung up too.
func (s *Session) bridged(winner string) {
	s.log.Info("leg answered, bridging", "leg", winner)
	_ = s.deps.ARI.RingStop(s.ctx, s.ChannelID)
	if err := s.deps.ARI.AddToBridge(s.ctx, s.bridgeID, winner); err != nil {
		s.log.Error("could not bridge answered leg", "err", err)
		return
	}
	for legID := range s.legs {
		if legID != winner {
			_ = s.deps.ARI.Hangup(s.ctx, legID)
			delete(s.legs, legID)
		}
	}

	for {
		select {
		case ev := <-s.events:
			switch ev.kind {
			case evLegAnswered:
				// Lost the race after we picked a winner.
				_ = s.deps.ARI.Hangup(s.ctx, ev.value)
			case evLegGone:
				if ev.value == winner {
					delete(s.legs, winner)
					s.log.Info("far end hung up")
					return
				}
			}
		case <-s.ctx.Done():
			return
		}
	}
}

// dismiss says goodbye and hangs up. reason selects the parting prompt.
func (s *Session) dismiss(reason string) {
	if s.ctx.Err() != nil {
		return
	}
	s.log.Info("dismissing caller", "reason", reason)
	s.rec.Outcome, s.rec.Reason = calls.OutcomeDismissed, reason

	for legID := range s.legs {
		_ = s.deps.ARI.Hangup(s.ctx, legID)
		delete(s.legs, legID)
	}
	if s.bridgeID != "" {
		_ = s.deps.ARI.RingStop(s.ctx, s.ChannelID)
	}

	prompt := PromptGoodDay
	if reason == "no-answer" {
		prompt = PromptNoAnswer
	}
	s.play(prompt, false)
	_ = s.deps.ARI.Hangup(s.ctx, s.ChannelID)
}

// cleanup is the single teardown path. It runs on a fresh context because the
// session context is usually already cancelled by the time we get here, and
// the whole point is to issue the final hangups and bridge destruction.
// Errors are ignored: a channel that is already gone is not a problem.
func (s *Session) cleanup() {
	s.cancel()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	for legID := range s.legs {
		_ = s.deps.ARI.Hangup(ctx, legID)
	}
	if s.bridgeID != "" {
		_ = s.deps.ARI.DestroyBridge(ctx, s.bridgeID)
	}
	if !s.detached.Load() {
		_ = s.deps.ARI.Hangup(ctx, s.ChannelID)
	}

	// Both sinks are fed from the single teardown path, so there is exactly
	// one record and one completed event per call however the call ended —
	// including the caller hanging up, which reaches here by cancellation and
	// no other route.
	//
	// Before OnFinished, not after: OnFinished is what tells the rest of the
	// system this session is done, so anything sequenced behind it would be
	// racing a caller who is already gone. Neither Post blocks, so this costs
	// the teardown nothing.
	s.rec.MS = s.now().Sub(s.startedAt).Milliseconds()
	if s.deps.Calls != nil {
		s.deps.Calls.Post(s.rec)
	}
	s.notify(notify.EventCompleted)

	s.deps.OnFinished(s)
	s.log.Info("session finished")
}
