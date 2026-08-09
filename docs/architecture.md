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

## Why trunks became an inventory

`asterisk/pjsip.conf.example` is one worked VoIP.ms trunk, hand-maintained.
That is the right answer for one provider and it still is — `trunks.toml` is
optional, and with no such file `doorman render` generates only the handset
config and the hand-written trunk keeps working untouched.

Several providers turn it into a data problem. The same handful of fields per
provider, four PJSIP objects each, and — this is the part that grows — one
inbound dialplan context per registration with one route per DID inside it.
That is providers × numbers, all of it mechanical, all of it derived from
`[line] number` and `[line] trunk`. So `trunks.toml` is rendered exactly the
way `handsets.toml` already is: an inventory, provider-specific fields,
credentials named rather than written (`password_env`), and generated files
that are outputs nobody hand-edits.

The symmetry is the point. It reuses `internal/render`, its tests, and the
rule about generated files, and it turns "support more providers" from a
documentation problem into a data one — a new provider is a TOML block rather
than a wiki page.

**Render generates the inbound contexts, rather than the operator writing
them.** Three reasons, in order of weight. The contexts and their routes are
the part that grows combinatorially, and they are the part that is purely
derived. Hand-writing them means keeping two files in agreement about which
number belongs to which line, which is the drift `doorman render` exists to
remove. And a generated route can match a DID in every digit format a provider
might send — ten digits, eleven, full E.164 — which deletes the "watch
`asterisk -rvvv` to find out which one arrives" step from the runbook
altogether. It costs two extra lines per DID and they never fire.

What is *not* generated: `identify` blocks. With `line=yes` and `endpoint=` on
the registration they are redundant, and an IP allow-list is a list that goes
stale silently the day a provider adds a media server. A provider that can
only do IP authentication is out of scope and needs a hand-written block.

**Rejected: trunk settings inside each policy file.** A trunk is shared
infrastructure that several lines point at, not a property of one line.
Putting it in policy would duplicate host, codecs and credentials per line and
make "change the POP" an edit in N files.

## Which trunk carries 911

With one trunk there is one way out. With several, something has to choose,
and the wrong choice is the most consequential bug this project could ship.

**The primary line is the default for everything unqualified, and the CLI
never lets you wonder which it is.** `emergency_trunk` in `trunks.toml` wins
if it is set; unset, 911 leaves by plain `policy.toml`'s `[line] trunk` — the
unsuffixed file, which is already the default outbound identity. One rule,
two jobs, so "which line am I on when I have not said" has one answer rather
than two that can drift apart. `doorman check` and every startup print which
trunk it is and whether that was **chosen or inferred**.

Requiring an explicit designation was the first instinct and it is wrong: it
creates a state where somebody forgot and 911 has no route. Defaulting to *the
first declared trunk* was the second, and it is also wrong — a default derived
from file order moves silently when a block is added at the top of a file.
Naming `policy.toml` removes the ordering question entirely.

Not inferred from the line the caller is on, for two reasons. Whoever grabs the
nearest handset has no idea which line they are on. And E911 is registered per
DID against a street address, so the trunk carrying it must be the one whose
address is filed — which is why a trunk can declare `e911` and `doorman check`
reports "unknown" rather than guessing when it does not.

**When the designated trunk cannot carry the call, 911 falls over to the next
trunk, and the next.** The dispatcher may then see the wrong address, which is
genuinely bad — but a connected call lets a human say their address out loud
and a failed call gives them nothing. Connection first. The fallback order puts
trunks declaring `e911 = true` ahead of ones declaring nothing, and a trunk
declaring `e911 = false` last. If nothing can carry it the caller hears
congestion, never silence: the tone is what tells somebody to reach for a
mobile.

**Nothing asks whether a trunk is registered; it tries it.** A `DEVICE_STATE`
check before the `Dial` was the obvious design and it is wrong: a provider that
does not answer `OPTIONS` looks unreachable while working perfectly, and
diverting an emergency call off the one trunk whose address is on file — on a
false negative, at the worst possible moment — is the exact failure this design
exists to prevent. An unregistered trunk fails the `Dial` in milliseconds,
which is the same answer arrived at honestly.

The ladder is generated into `[cmm-emergency]` and the hand-written `_911`
reaches it with `DIALPLAN_EXISTS`. That is what makes `#include`ing the
generated file the whole switch: with no `trunks.toml` the context does not
exist, `_911` dials `DEFAULT_TRUNK`, and a single-provider box behaves exactly
as it did before any of this — with no second edit for anybody to forget.

## Outbound leaves by the line's trunk

A provider will not present a caller ID its account does not own: it rejects
the call or silently rewrites it as anti-spoofing, and neither is visible from
this end. So a line's `outbound_cid` and its `trunk` are **one decision**, and
they travel together everywhere — one `render.OutboundIdentity`, two `set_var`
lines on the endpoint, two channel variables set in the same loop by the `*4`
console.

**Both outbound paths share one dialplan context.** `[internal]` and
`[outbound-console]` each `include => cmm-outbound`, which is four lines that
read `OUTBOUND_TRUNK` (falling back to `DEFAULT_TRUNK`) and `OUTBOUND_CID`
(falling back to whatever the trunk sends). The alternative — a copied `Dial`
line in each — is two implementations of the most consequential string in the
file, and the failure mode of them drifting is a call that connects and bills
correctly while showing the wrong number.

`_911` is deliberately *not* in that shared context. It sits in `[internal]`,
which the console never enters, so the console cannot reach an emergency route
even if doorman's own refusal were bypassed.

**What `outbound_cid` validation can and cannot catch.** Nothing here can ask
VoIP.ms or Telnyx which DIDs an account owns, and no config shape would make
that possible. What this box does know is which numbers it answers and which
provider each arrives on, because `[line] number` and `[line] trunk` say so.
That is enough for the one provable mistake: a line presenting a number that
this same config declares at a different provider. `doorman check` and `doorman
render` refuse it. A caller ID matching no declared number is reported and
allowed — a DID you own and do not answer here is an ordinary config and
indistinguishable from a typo, so refusing it would make the check wrong more
often than the file.

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
