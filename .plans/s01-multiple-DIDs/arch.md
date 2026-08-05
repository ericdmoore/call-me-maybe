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

- a dial prefix (`*3` then the number) chooses a line for one call
- a per-handset default covers the common case of not prefixing
- `doorman check` must catch a prefix that collides with an extension or a
  feature code

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

### Emergency calling is a design decision, not a detail

With one trunk, `911` has one way out. With several, something must choose —
and the wrong choice is the most consequential bug this project could ship.

**Chosen: a designated emergency trunk, named explicitly in configuration.**
Not inferred from the line the caller is on, because a caller picks up whatever
handset is nearest and has no idea which line they are on; and because E911 is
registered per DID with a street address, so the trunk that carries it must be
the one whose address is on file.

Startup warns when the designated trunk has no registered address, and the
runbook has to say plainly that a provider without E911 in your area means a
phone that cannot call for help.

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
| 6 | Failed PIN attempts always call `Limiter.Failure` | Unchanged; only the key gains a prefix |
| 1 | Never log a full caller ID above info | Unchanged. The `line` field is not a caller identifier |
| 2 | Never widen the ARI bind | Untouched |
| 3 | Teardown is the deferred cleanup | Untouched — `internal/lobby` does not change |

No invariant is weakened. One is generalised: *an invalid policy must never
take down **any** line it does not belong to.*

Phase 2 adds one of its own, worth stating in the same terms once it ships:
*a call must never leave by a trunk that does not own the number it presents* —
and its safety-critical sibling, *emergency calls leave by the designated
trunk, or fail loudly.*
