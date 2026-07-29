# Architecture notes

## Why Asterisk instead of a SIP stack of our own

Writing SIP is a trap. NAT traversal, RTP timing, DTMF detection, codec
negotiation and re-INVITE handling are decades of accumulated edge cases, and
getting them subtly wrong produces one-way audio at 2am rather than a stack
trace. Asterisk handles all of it. ARI exposes exactly the seam we want:
Asterisk owns the media, doorman owns the decisions.

The dialplan is four lines and never grows. Every policy question lives in Go
where it can be tested — the whole state machine runs against a fake ARI in
`internal/lobby`, no trunk required.

## Why registration-based trunking

The requirement is no port forwarding, which rules out any provider that wants
to push an INVITE at a static IP. A registration-based trunk inverts it: the Pi
opens the connection outbound, keepalives hold the NAT binding open, and inbound
calls arrive down the pipe the Pi already established.

In PJSIP the load-bearing detail is `line=yes` plus `endpoint=voipms` on the
registration object. That binds inbound traffic on that registration to the
endpoint, so no `identify` block and no provider IP allow-list is needed. Get
this wrong and inbound calls hit the `anonymous` endpoint and vanish.

Viable providers: VoIP.ms, Telnyx, Flowroute, most traditional ITSPs. Not
viable: anything that only does IP authentication to a fixed address.

## Event routing

ARI events arrive on one WebSocket for the whole application, so doorman keeps
a registry mapping channel IDs — inbound callers and the outbound ring-group
legs it originates — to their owning session. One call is one goroutine; the
router hands events to it over a buffered channel and never blocks.

Outbound legs are originated with `appArgs=leg,<sessionId>`. A leg entering
Stasis *means it answered* — that's the entire race resolution: first
`StasisStart` with `leg` wins, gets bridged, and every sibling is hung up.

Teardown has to be idempotent because the caller can hang up at any point.
Caller hangup cancels the session context; every wait in the state machine
selects on it, and the deferred `cleanup` in `Run` is the single teardown
path. Cancellation *is* the state — there is no flag to forget to check.

## Things that will bite you

- **`dtmf_mode=rfc4733`** on both the trunk and the handsets. Without it the
  lobby never hears an extension and every stranger gets "Good day".
- **`Answer()` in the dialplan, not in ARI.** Answering before `Stasis()` means
  the audio path is up when the greeting starts. Answering inside the app adds a
  round trip you can hear.
- **Ringback after answer.** Once the call is answered the carrier stops
  generating ringback, so a caller waiting on the house would sit in silence.
  `channels/{id}/ring` injects the indication; remember to `ringStop` on bridge.
- **`originator`** on originate copies codec and language from the inbound leg,
  which avoids a pointless transcode on the Pi.

## Transfers

Transfer is not a trunk capability and not purely a handset feature: the
handset sends a SIP REFER ("send my other party to 103") and **Asterisk
executes it**. The outside caller's leg stays anchored on the Pi throughout —
the provider never participates and needs nothing enabled.

Practical consequences here:

- The Grandstream Transfer button (blind and attended both) just works. No
  doorman code is involved in the transfer itself.
- The transfer target resolves in the transferring handset's dialplan context,
  which is `internal` — so the 101–105 extensions double as transfer targets,
  and the outbound NANP patterns mean external transfers work too (and cost
  trunk minutes; use a restricted transfer context if that ever matters).
- DTMF feature-code transfers (`features.conf`, `##`/`*2`) do NOT apply to
  ARI-bridged calls. Rely on the phones' Transfer buttons.
- **Doorman must not murder a transferred call.** A transferred caller leaves
  the Stasis app *alive*: Asterisk sends `StasisEnd`, not `ChannelDestroyed`.
  The event router maps StasisEnd on the caller to `CallerLeft()` (session
  ends, final hangup suppressed) and ChannelDestroyed to `CallerGone()`
  (session ends, idempotent hangup allowed). Collapsing those two paths back
  into one reintroduces a bug where every handset transfer drops the call the
  moment it succeeds — covered by
  `TestHandsetTransferDoesNotKillTheTransferredCall`.
