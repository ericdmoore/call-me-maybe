# s01 · Multiple lines — architecture

How one box serves several phone numbers, each with its own rules, without
becoming a carrier or a multi-tenant anything.

Companion to [`readme.md`](readme.md), which is the phased plan. This file is
the reasoning: what was chosen, what was rejected, and which invariant each
decision protects.

Phase 1 is several numbers on one provider. Phase 2 is several providers, which
changes the outbound path and is treated separately below.

---

## The problem

Today doorman answers one number. `policy.toml` is singular, `PROMPT_MEDIA_PREFIX`
is global, and the lobby has one disposition. A household wanting a second
number — a business line, a kids' line, one per venture — has no way to express
that the two should behave differently.

The requirement is not multi-tenancy. It is **one operator, one box, several
numbers**, where the numbers may share hardware and share nothing else.

---

## How the line arrives

A call reaches Asterisk on the trunk. The dialled number is already known —
`extensions.conf` logs it and discards it:

```
exten => _X.,1,NoOp(Inbound from ${CALLERID(num)} to ${EXTEN})
```

Two ways to get it into doorman.

**Rejected: parse the DID out of the SIP headers.** ARI exposes
`channel.dialplan.exten`, so doorman could read it with no dialplan change.
But what a provider puts in the To header and Request-URI varies, and
supporting more providers is an open issue (#5). A mechanism that works for
VoIP.ms and silently misroutes on Telnyx is worse than one that requires a
config line.

**Chosen: the dialplan names the line.** One context per DID, passing the name
as a Stasis application argument:

```
[from-trunk-home]
exten => _X.,1,Answer()
 same => n,Stasis(${DOORMAN_APP},line,home)

[from-trunk-biz]
exten => _X.,1,Answer()
 same => n,Stasis(${DOORMAN_APP},line,biz)
```

Three reasons:

1. **Provider-independent.** Nothing depends on header formatting.
2. **The dialplan already knows.** It is the operator's file and the one place
   that maps a number to a purpose. Porting a number is a one-line edit with no
   policy change.
3. **The mechanism exists.** `Originate` already passes `AppArgs: "leg,<id>"`
   and the router already branches on `ev.Args[0] == "leg"`. A `line` argument
   is a second case in a switch that is already there.

---

## How a line is configured

**Chosen: one policy file per line.**

```
policy.toml        the default line — unchanged, and what an existing install has
policy.biz.toml    the "biz" line
policy.kids.toml   …
```

**Rejected: `[[lines]]` sections in one file.** The decisive argument is
invariant 4 — *an invalid policy.toml must never take the phone down*.
`policy.Store` already keeps the last good policy on a failed reload. N files
means N stores, and a stray bracket in the business policy cannot stop the
house phone ringing. One file with nested sections makes every line share a
blast radius, which is precisely backwards: the reason for two lines is that
they are independent.

**Rejected: a base policy with per-line overlays.** `doorman check` exists to
"print what it resolves to." The moment values inherit and override, *which
layer won* becomes the support question. This project does not have that
problem today and should not buy it.

The cost of separate files is duplication — a caller allow-listed on two lines
is written twice. That is usually the correct distinction rather than a
nuisance: you may want grandma on the home line and specifically not on the
business one. If it becomes painful the answer is an explicit include, not an
overlay.

---

## What the router does

`internal/lobby` **does not change**. `Deps.Policy` is already
`func() *policy.Policy`, so multi-line is a `map[string]lobby.Deps` selected at
`StasisStart`. The state machine never learns that lines exist.

```
StasisStart ──► args[0] == "leg"  ──► existing leg handling
            └─► args[0] == "line" ──► deps[args[1]]  ──► NewSession(…, deps)
            └─► no args           ──► deps["default"] ──► today's behaviour
```

That last row is the compatibility guarantee: **no app argument means byte-identical
behaviour**, so no existing install changes.

---

## Shared, or per line

| | Scope | Why |
|---|---|---|
| `handsets.toml` | **shared** | One hardware inventory. Both lines can ring the office; that is the point |
| Extensions and PINs | **per line** | Separate namespaces. `doorman check` warns on a cross-line collision, because a human will confuse two identical PINs |
| Rate limiter | **per line** (line in the key) | The lines have opposite dispositions by design; a redialler on a permissive business line must not lock out the house |
| Concurrency cap | **global** | A resource limit on the box, not a policy statement |
| Call log | **one file, `line` field** | So a whole day still reads in order, and `doorman calls --line biz` still works |
| Prompt pack | **per line** | The curt doorman and the courteous concierge are the same engine with different content |

---

## Line identity

A `[line]` section carries what makes a line itself:

```toml
[line]
label       = "Mertaugh Enterprises"
number      = "+15125550142"     # what a digits: prompt reads back
prompts     = "concierge"        # overrides PROMPT_MEDIA_PREFIX
outbound_cid = "+15125550142"
on_no_input = "voicemail"        # dismiss | ring-house | voicemail
```

`on_no_input` is the disposition knob. Today the lobby can only dismiss a
caller who dials nothing; a business line must be able to fall through to a
mailbox. This is what lets one file be a bouncer and another a receptionist
without either being a special case in code.

---

## Outbound identity

**This is not optional, and it is easy to miss.** The outbound dialplan sets no
caller ID at all:

```
exten => _NXXNXXXXXX,1,Dial(PJSIP/1${EXTEN}@voipms,60)
```

Every outbound call presents the trunk default. With one number that is
invisible. Across several it means every customer you ring back saves the
wrong number and the whole arrangement leaks.

Selection is per call, because one desk phone serves every line:

- a per-handset default covers the common case, which is most of them
- `*4` answers, reads the numbers out, and places one call as whichever is
  chosen
- a handset no line claims presents the primary line — `policy.toml`, the same
  file 911 leaves by

**Rejected: a dial prefix** (`*3` then the number). One keypress, and it works
from a handset's own phonebook, which the console does not. But it reserves
`*1`–`*9` forever, fights `*97` and every feature code anyone adds later, and
with five ventures nobody remembers which digit is which.

**The claim lives in the line's file, not on the handset.** `[line]
outbound_handsets` rather than `line = "biz"` in `handsets.toml`, for the same
reason the trunk does not live in a policy file: handsets.toml is one shared
inventory, and a line default is not a property of the hardware. It also gives
`doorman check` — the only thing that reads every line at once — something to
validate, and gives the claim the same rollback story as everything else here:
delete the file.

**The console releases rather than bridges.** It sets the caller ID on the
handset's channel and hands it to `[outbound-console]` via ContinueToDialplan,
the way `sendToVoicemail` hands a caller to `[voicemail-drop]`. Originating and
bridging would put the trunk's dial string into Go, which is precisely the
thing Phase 2 makes vary and precisely the thing `docs/architecture.md` says
Asterisk owns. Releasing keeps both outbound paths on the same dialplan lines
and takes doorman out of the call the moment it is placed.

---

## Phase 2 · What changes when providers multiply

Phase 1 assumes one trunk, so "which line" only ever selected *policy*. With
several providers it also selects *a path out of the building*, and that is a
different kind of decision.

### Outbound is the hard part

A provider will not let you present a caller ID it does not own. So if the
business line is a Telnyx number, a call presenting that number has to leave
via Telnyx — over VoIP.ms it is rejected or silently rewritten. The mapping
becomes:

```
line ──► outbound_cid   (what the callee sees)
     └─► trunk ──► endpoint   (how it gets there)
```

Both halves are mandatory and they must agree. `doorman check` validating
`outbound_cid` against its trunk is not tidiness — a mismatch produces calls
that either fail or present the wrong number, and both look like someone else's
bug.

### Inbound barely changes

The line still arrives as a Stasis argument. What changes is that each
registration binds to its own endpoint with its own `context=`, so instead of
one `[inbound-trunk]` there is one context per provider. The dialplan grows as
providers × numbers, with one line each.

That growth is the argument for **generating** trunk configuration rather than
hand-writing it, which is why `trunks.toml` exists.

### Why trunks become an inventory

**Chosen: `trunks.toml`, rendered like `handsets.toml`.**

The symmetry is the point. Handsets are hardware inventory that becomes
generated PJSIP with secrets substituted from the environment at render time.
Trunks are the same shape of thing: an inventory, provider-specific fields,
credentials that must never sit in a committed file. Reusing the pattern means
reusing `internal/render`, its golden tests, and the rule that generated files
are outputs.

It also turns issue #5 — supporting more providers — from a documentation
problem into a data problem. A new provider is a TOML block, not a wiki page.

**Rejected: leaving trunks in hand-written `pjsip.conf`.** Workable for one, and
it is what exists today. But doorman needs the line → trunk mapping anyway for
outbound routing, so the information has to enter the Go side regardless. Having
it in two places, one of them hand-maintained, is the drift that
`site/public/llms.txt` already demonstrated.

**Rejected: trunk settings inside each policy file.** A trunk is shared
infrastructure that several lines point at, not a property of one line. Putting
it in policy would duplicate host, codecs and credentials per line and make
"change the POP" an edit in N files.

**Built, and the shape it took.** `trunks.toml` is *optional*, and its absence
is the compatibility gate rather than a migration step: no file means nothing
is generated, the hand-written `pjsip.conf` keeps working, and nothing in
`doorman check` says the word trunk. Rollback is deleting the file.

Generating the inbound contexts turned out to have a third argument the plan
did not make. Beyond the combinatorial growth and the drift between two
hand-maintained files, a generated route can match a DID in *every* digit
format a provider might send — ten digits, eleven, full E.164 — which removes
"find out what digits arrive" from the runbook entirely. It costs two lines per
DID that never fire.

`emergency_trunk` lives in `trunks.toml` rather than in a policy file, which
follows from the reasoning below: it is a property of the inventory, and
putting it in a policy file would make it per line — the exact thing this
design refuses, since whoever grabs the nearest handset has no idea which line
they are on. `e911` sits on each trunk for the same reason `password_env` does:
it is a fact about the provider that doorman cannot discover and must not
guess, so unset reads as *unknown* rather than as either answer.

### Emergency calling is a design decision, not a detail

With one trunk, `911` has one way out. With several, something must choose —
and the wrong choice is the most consequential bug this project could ship.

**Chosen: the primary line is the default for everything unqualified, and the
CLI never lets you wonder which it is.** `policy.toml` — the unsuffixed file —
carries 911 *and* supplies the caller ID for a handset that just picks up and
dials. `emergency_trunk` and a per-handset default override each half; neither
must be set, and unset is never undefined.

Requiring an explicit emergency trunk was the first instinct and it is wrong:
it creates a state where somebody forgot and 911 has no route.

Defaulting to *the first declared trunk* was the second, and it is also wrong,
because a default derived from file order moves silently when a block is added
at the top of a file. Naming `policy.toml` removes the ordering question
entirely — it is the file that already means default, so `policy.aaa.toml`
cannot steal the most safety-critical route by sorting early.

One rule serving both defaults is the point. "Which line am I on when I have
not said" has one answer rather than two that can drift apart.

Not inferred from the line the caller is on, for two reasons. Whoever grabs the
nearest handset has no idea which line they are on. And E911 is registered per
DID against a street address, so the trunk carrying it must be the one whose
address is filed.

**When the designated trunk is unregistered, 911 falls over to any registered
one.** The location data may then be wrong, which is genuinely bad — but a
connected call lets a human say their address, and a failed call gives them
nothing. Connection first, and say so in the runbook rather than leaving it as
an accident of the code.

## Failure modes and what happens

| Situation | Behaviour | Why |
|---|---|---|
| `policy.biz.toml` is invalid at startup | that line refuses to start, others serve | fail loudly where an operator sees it |
| `policy.biz.toml` becomes invalid on reload | last good biz policy stays; home unaffected | invariant 4, per line |
| Dialplan passes `line,typo` | falls back to the default line, logs loudly | doorman cannot read the dialplan, so it cannot validate the set at startup. **Never drop the call** |
| No app argument at all | default line | backwards compatibility |
| Two lines define the same PIN | valid, but `doorman check` warns | separate namespaces are correct; confusable ones are worth flagging |

---

## What this does not do

- **Not multi-tenant.** One operator owns every line. There is no account
  boundary, no per-line authentication, no isolation claim beyond config.
- **Not a carrier.** Still one registration-based trunk. See
  `docs/SUSTAINABILITY.md` on why hosted multi-tenant PBX is on the do-not-chase
  list.
- **Not per-line concurrency limits.** The cap stays global until someone has a
  reason.
- **Not SMS.** Out of band, a separate project, and see
  `docs/product-extensions.md` §9.

---

## Invariants touched

| # | Invariant | Effect |
|---|---|---|
| 4 | An invalid policy must never take the phone down | **Strengthened.** Per-line stores mean a bad file has a smaller blast radius than today |
| 5 | PIN comparison stays exact-match against a map | Unchanged — one map per line |
| 6 | Failed PIN attempts always call `Limiter.Failure` | Unchanged; only the key gains a prefix. M1.2 adds a path that reaches the same exit *without* a failed attempt — a caller who says nothing has not guessed at anything, and must not be charged for it |
| 1 | Never log a full caller ID above info | Extended in M1.3. A number somebody *dialled* identifies a human as precisely as one that arrived, so it is normalised and redacted before any log sees it; a line's own `outbound_cid` is redacted too, since the useful key in those lines is the line name |
| 2 | Never widen the ARI bind | Untouched |
| 3 | Teardown is the deferred cleanup | M1.3's console is a sibling of `Session` with the same shape: one goroutine, `defer cleanup()` in `Run`, cancellation as the state, every wait on `ctx.Done()`. Untouched **in M1.1**, live in M1.2. The disposition knob and the known caller's route through the collector are decisions only the state machine can make, so `Run`, `collect` and `evaluate` change. The shape does not: no state flag, no exit that skips the deferred cleanup, every wait still on `ctx.Done()` |
| 8 | StasisEnd and ChannelDestroyed are different | Live in M1.2 and again in M1.3. `on_no_input = "voicemail"` reaches `sendToVoicemail`, which releases the caller via ContinueToDialplan — so nothing may hang the channel up afterwards. The `*4` console does the same thing on the way out: after the release, StasisEnd is the *successful* ending, and collapsing the two would kill every outbound call at the moment it was placed |
| 7 | Prompts are pre-rendered WAVs, never runtime TTS | Unchanged, and M1.3 stayed inside it the hard way: the console speaks Asterisk's own clips plus `digits:`, which is Asterisk reading a number from files, not synthesis. It also stays out of the six-name pack contract, which a seventh prompt would have broken for every pack in existence |

No invariant is weakened. One is generalised: *an invalid policy must never
take down **any** line it does not belong to.*

**"`internal/lobby` does not change" is M1.1's rule, not the feature's.** It is
the falsifiable claim that *routing* is a router concern, and it held. Line
*behaviour* is a different kind of thing, and a design that tried to express
"a silent caller takes a message" outside the state machine would be
contorting itself to protect a slogan.

Phase 2 adds one of its own, worth stating in the same terms once it ships:
*a call must never leave by a trunk that does not own the number it presents* —
and its safety-critical sibling, *emergency calls leave by the designated
trunk, or fail loudly.*
