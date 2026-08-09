# s01 · Multiple lines — development plan

Build one box that answers several phone numbers, each with its own rules —
first on one provider, then across several.

Reasoning and rejected alternatives: [`arch.md`](arch.md).
Backlog with acceptance criteria: `docs/TASKS.md` §7.

**Status:** **Phase 1 is complete** — M1.1, M1.2, M1.3 and M1.4 have all
landed. A household can buy a second number today. Phase 2 (several providers)
is planned.

---

## What "done" looks like

A household can buy a second number, point it at the same Pi, and give it
different behaviour — a curt doorman on the home line, a courteous concierge on
the business one — without either line being able to break the other. Later,
that second number can come from a different company entirely.

Two phases, because the second is a materially harder problem and the first is
worth shipping on its own.

| | Phase 1 | Phase 2 |
|---|---|---|
| Numbers | several | several |
| Providers | **one** | **several** |
| Inbound | one registration carries every DID | one registration per provider |
| Outbound | one trunk, per-line caller ID | **trunk chosen by the line's provider** |
| Unlocks | business line, kids' line, per-line hours | redundancy, rate shopping, porting without downtime |
| Hard part | policy multiplexing | outbound routing and emergency calling |

Phase 1 is a config-surface change plus a small router change. Phase 2 changes
how a call *leaves* the building, which is where the money and the 911 calls
are.

---

# Phase 1 · Several lines, one provider

One VoIP.ms registration already carries as many DIDs as you buy. The dialled
number arrives as `${EXTEN}` and is currently logged and thrown away.

## M1.1 · Routing spine — **done**

*The prerequisite. Nothing below starts until this lands.*

The dialplan names the line; doorman keeps one policy store per line; the
router picks the right `Deps` at `StasisStart`.

```
exten => _X.,1,Answer()
 same => n,Stasis(${DOORMAN_APP},line,biz)
```

**Build**

- Parse `line,<name>` beside the existing `leg,<id>` argument in `route`.
- Discover `policy.<line>.toml` at startup; bare `policy.toml` is the default.
- One `policy.Store` per line.
- Unknown line name → default line + a loud log. **Never drop the call.**
- Line included in the rate-limit key.
- `doorman check` lists every line and what each resolves to.

**Deliverable:** two numbers, two policy files, two behaviours.

**Verify**

- An install with no `line` argument behaves byte-identically to today. This is
  the compatibility gate — if it fails, stop.
- `internal/lobby` has no diff. If it needs one, the design is wrong.
- Corrupting `policy.biz.toml` leaves the home line answering.
- Rate-limit budgets do not bleed between lines.

## M1.2 · Line identity — **done**

**Build**

- `[line]` section: `label`, `number`, `prompts`, `on_no_input`.
- Per-line prompt prefix overriding `PROMPT_MEDIA_PREFIX`.
- `on_no_input` — `dismiss` (today's behaviour, the default), `voicemail`,
  `ring-house`.
- Known callers can reach the collect loop, where a timeout means *admit*
  rather than dismiss.

**Deliverable:** the home line dismisses strangers, the business line takes a
message. Same binary, opposite defaults.

**This is the milestone where `internal/lobby` changes**, and the note under
M1.1 that it must not is specific to the routing spine. `Run`, `collect` and
`evaluate` all take a diff here, because the disposition knob and the known
caller's route through the collector are decisions the state machine has to
make. What did *not* change is the shape: no state flag, no exit that skips
the deferred cleanup, every wait still selecting on `ctx.Done()`.

The design question was the known caller. Putting Grandma through a collect
loop would make her wait out a dial window before any phone rings, which is a
regression on the most common path in the system. **The welcome prompt is the
dial window and the whole of it** — barge in with a digit to reach one room,
press nothing and the house rings the instant it ends. Latency added on the
silent path: none. A tail of silence after the greeting was the alternative
and it is paid by every known caller to serve the rare one who dials.

Second rule, which falls out of the first: **nothing at the keypad may cost an
allow-listed caller the house.** No digits, half an extension abandoned, or
attempts exhausted — every exit rings it. They were admitted the moment their
number matched, so `on_no_input` does not apply to them at all; it is the
stranger disposition, and its name means what it says (an empty dial window,
not exhausted attempts, and never a rate-limit failure).

## M1.3 · Outbound line selection — **done**

*Do not ship M1.1 without this.* Today `Dial(PJSIP/1${EXTEN}@voipms,60)` sets
no caller ID at all, so every outbound call presents the trunk default. With
several lines, every customer you ring back saves the wrong number.

Two mechanisms, not three.

**Per-handset default** — the common case, zero friction. The office phone
calls as Venture A and never sees a menu.

**`*4`, the outbound console** — dial it, doorman answers and reads the lines,
press a digit, dial the number, doorman places the call and bridges you in.

This is the lobby state machine running backwards: answer, play, collect,
originate, bridge, all of which already exist. A stranger dials in and is
connected to a handset; here a handset dials in and is connected to a stranger.
No new service — one more dialplan hook into the Stasis app.

**Rejected: a dial prefix (`*3` then the number).** One keypress, and it works
from a handset's own phonebook, which the console does not. But it needs `*1`–`*9`
reserved forever, fighting `*97` and every feature code anyone adds later, and
with five ventures nobody remembers which digit is which. A menu that says the
names out loud beats a mapping you have to memorise. Revisit only if someone
actually misses storing contacts with a line baked in.

**Build**

- `[line] outbound_cid`, and a per-handset default line.
- `*4` console: line menu, then a variable-length `#`-terminated number.
  (Same collection primitive the Line 2 callback-number capture needs — second
  customer for one piece of work.)
- Confirmation before dialling: "calling as Venture A". Catches the wrong
  choice *before* the customer's phone rings.
- Outbound records in the call log, carrying the line.

**What landed, and where it differs from the sketch above.**

The per-handset default is `[line] outbound_handsets` — the line claims the
phones, rather than each handset naming a line. `handsets.toml` is one shared
inventory and cannot hold something true of only one line; the claim disappears
when the line's file is deleted, which is the rollback story everything else
here has; and it makes a contested handset a question `doorman check` can
answer, because the check is the only thing that reads every line at once.

The console **does not originate and bridge.** It sets `OUTBOUND_CID` on the
handset's own channel and releases it into `[outbound-console]`, exactly as
`sendToVoicemail` releases a caller into `[voicemail-drop]`. That keeps the
trunk dial string in the dialplan where Phase 2 will make it vary, makes the
console and the plain path dial through the same lines rather than two
implementations that can drift, and takes doorman out of the call the moment it
is placed. Invariant 8 applies identically: StasisEnd after the release is the
*successful* ending and must not hang the channel up.

The confirmation is spoken **between the choice and the number**, not after
both. "Calling as this number" is only useful while there is still time to act
on it, and hearing it before you have dialled means a wrong choice costs a
keypress rather than a call.

It says the *number*, not the label, and it speaks with Asterisk's own sounds
rather than the prompt pack. There is no recording of anyone saying "Mertaugh
Enterprises" and there cannot be one that ships — but `digits:` reads a number
natively, and the number is what the callee will see anyway. Using the pack
would have meant a seventh prompt name, which breaks every pack in existence
for a mechanism the pack has no business voicing.

Outbound call-log records are **not** in this milestone. They want a direction
field on the record rather than only a line, which is M1.4's shape.

**Keep the plain dialplan path working.** Routing outbound through Stasis puts
doorman on the outbound path, so a crash would stop you *making* calls as well
as receiving them. `_NXXNXXXXXX` must keep working with a house default caller
ID, exactly as `[inbound-fallback]` keeps inbound alive. `*4` is the enhanced
path, not the only one.

A handset with no default that dials a number directly presents the **primary
line** — `policy.toml`, the same file that carries 911. Same rule, one concept
to learn. A call that goes out with a slightly wrong number beats a call that
does not go out.

**Verify:** place a call each way and read the number off a real handset. Not
from logs — logs show what was sent, not what arrived.

## Design principle · Nobody learns a feature they do not need

The measure of this working is that **most of the household never presses
`*4`**, and the kids never learn it exists.

| Who | What they do | What they get |
|---|---|---|
| A kid on the kitchen phone | picks up, dials | the primary line — the home number |
| A parent on the office phone | picks up, dials | that handset's default — Venture A |
| A parent who needs another identity | `*4`, pick, dial | whichever line they chose |

Complexity is opt-in. You meet the console only when you have a reason to, and
a handset default means even the person with five ventures rarely presses it.
That is the inverse of most PBX features, where the configuration surface leaks
into everyday use and everyone in the house has to learn a code.

**It is also the safe arrangement, which is the real argument.** Because the
primary line is both the default outbound identity *and* the 911 route, a child
who picks up the nearest phone and dials 911 goes out the trunk whose
registered address is this house. There is no arrangement of keypresses that
puts a kid one digit away from an emergency call leaving by a business trunk
with somebody else's address on file. The two defaults being one rule is what
guarantees that.

**Use this as a filter on later work.** If a multi-line feature forces itself on
someone who only has one line, it is designed wrong.

## M1.4 · Per-line observability — **done**

**Build**

- `line` on the call record; `doorman calls --line biz`.
- One log file, not one per line, so a whole day still reads in order.

**What landed, and the two things the sketch above did not anticipate.**

**A direction, not only a line.** M1.3 deliberately produced no records for the
`*4` console, because an outbound call needs a direction before a line means
anything. It has one now — `inbound` is the zero value and is written as
absence — and the console posts a record from its single teardown path exactly
as a session does.

The outcome vocabulary did not transfer, and forcing it would have been worse
than admitting so. `answered`, `voicemail` and the rest are all statements
about *who picked up in this house*, and doorman is out of an outbound call
before the far end even rings — it sets the caller ID, hands the channel to the
dialplan, and stops existing as far as that call is concerned. So there is one
new outcome, `placed`, meaning the call left the building and nothing more.
`dismissed` and `abandoned` carry over unchanged because they are the two that
were never about direction: doorman ended it, or the human hung up.

The same rule applies to `ms`. On an outbound record it is how long the console
had the handset, not how long anybody talked, and `doorman calls` refuses to
print it in the column where an inbound row shows exactly that.

**Absence is the compatibility story, twice.** `line` is absent on the default
line and `direction` is absent on an inbound call, so a box answering one
number writes byte-identical records, every `calls.jsonl` in existence keeps
parsing, and both zero values already mean the right thing. `doorman calls`
grows a `LINE` column only once a call has arrived on a line with a name and a
direction column only once something has gone out — the same rule M1.1 gave
`doorman check`, so nobody with one number ever reads the word "line".

The dialled number lands in `dialled`, held in full on disk and narrowed by
`Redacted` exactly like `caller`, because that is what it is: a number that
identifies a person and will appear on a bill. Only a complete number the
console accepted gets there — a half-entry somebody gave up on is forgotten —
and that is where invariant 1's line falls between a destination and a
credential.

The webhook carries `line` on both events, which is the point of it: routing an
announcement by which number was rung is precisely what Home Assistant is for.
It stays inbound-only. Its two events are "the house is ringing" and "the call
ended", and an outbound call rings nothing here and ends somewhere doorman
cannot see.

---

**Phase 1 is complete.** One box, several numbers, one provider: the dialplan
names the line, each line has its own policy file and failure domain, its own
identity and disposition, its own outbound caller ID, and now its own readable
history. Everything below changes how a call *leaves* the building, which is a
different and harder problem.

---

# Phase 2 · Several providers

Phase 1 assumed one trunk. A second provider is not "one more registration" —
it changes how calls leave.

## Why bother

- **Redundancy.** One provider having a bad afternoon stops being your bad
  afternoon. This is the real prize.
- **Rate shopping.** Providers differ by destination.
- **Porting without downtime.** Stand the new one up beside the old, move
  numbers, retire the old registration.
- **Coverage.** One does E911 properly in your area; another has the number you
  want.

## What actually breaks

**Outbound stops being one trunk.** If the business line is a Telnyx number, a
call presenting that caller ID must leave via Telnyx. Over VoIP.ms it would
present a number that account does not own, which providers reject or silently
rewrite as anti-spoofing. So **line → trunk → endpoint** becomes mandatory
rather than an optimisation. This is the whole difficulty of Phase 2.

**Inbound contexts multiply.** Each registration binds to its own endpoint with
its own `context=`, so one `[inbound-trunk]` becomes one context per provider,
each splitting further by DID. That grows as providers × numbers — the argument
for generating it rather than hand-writing it.

**Trunk config stops being a one-off.** `pjsip.conf.example` is one worked
VoIP.ms example. Several providers make it a data problem — which is exactly
what issue #5 asks for.

## M2.1 · `trunks.toml`

Trunks become an inventory, rendered the way `handsets.toml` already is.

```toml
[[trunks]]
id       = "voipms"
provider = "voip.ms"
host     = "chicago.voip.ms"
username = "123456_home"
# password comes from the environment at render time, never from this file
context  = "from-voipms"
codecs   = ["ulaw", "g722"]
```

**Build**

- A `trunks.toml` schema, plus the `doorman schema` entry the drift tests will
  demand.
- `doorman render` emits endpoint, auth, registration and identify blocks per
  trunk — including `line=yes` and `endpoint=`, the load-bearing detail from
  `docs/architecture.md`. Get it wrong and inbound calls hit `anonymous` and
  vanish.
- Secrets substituted from the environment, never stored. Same rule as handsets.
- Generated file is an output: never hand-edited, never committed.

**Deliverable:** adding a provider is editing TOML and re-rendering. Closes the
config half of issue #5.

## M2.2 · Lines belong to trunks

**Build**

- `[line] trunk = "telnyx"` referencing a `trunks.toml` id.
- `doorman check` fails on a line naming a trunk that does not exist — a
  cross-file reference exactly like handsets, and the same validator shape.
- Generated inbound contexts route each provider's DIDs to the right line.

## M2.3 · Outbound by trunk

**Build**

- Outbound routing selects the endpoint from the line's trunk.
- `outbound_cid` validated against the trunk that will carry it. Presenting a
  number the provider does not own is a support ticket, not a feature.
- M1.3's line selection — the `*4` console and each line's
  `outbound_handsets` — now chooses a trunk as well as a caller ID.

**Verify:** place a call on each line and confirm **both** that the receiving
phone shows the right number *and* that the provider's own CDR shows the call
on the right account. Either alone can look correct while the other is wrong.

## M2.4 · Per-provider health

Extends `.plans/s03-provider-balance-checking`. Balance is a capability, not a
provider feature; with several trunks it is per trunk, and the output has to
say *which* account is low.

**Build**

- Balance per trunk, where the provider exposes one.
- `doorman balance` prints a table; non-zero exit if any trunk is under its
  threshold.
- The alert rings a handset. Internal calls never touch a trunk, so a broke
  account can still tell you it is broke.

---

# Phase 3 · Failover — sketch only

Several providers make this possible; it is not part of this feature.

Registration down on trunk A → route outbound via trunk B. Inbound failover is
mostly the provider's job — a failover DID at their end — not ours. Written
down only so nobody builds Phase 2 in a way that forecloses it: **keep trunk
selection a lookup, not a hardcode.**

---

# Cross-cutting

## Emergency calling — settle before Phase 2 ships

**The highest-stakes detail in the feature.** Today `_911` leaves by the only
trunk there is. With several, something has to choose.

### The rule

**There is always an answer, and the CLI always says what it is.**

**`policy.toml` — the unsuffixed file — is the primary line, and the primary
line is the default for everything unqualified.** One concept, two jobs:

- **911 leaves by its trunk.**
- **Pick up and dial** with no `*4` and no per-handset default presents its
  caller ID.

`emergency_trunk` overrides the first if someone needs it; a per-handset
default overrides the second. Neither has to be set, and unset is never
undefined.

```
911            emergency_trunk  →  primary line's trunk
outbound CID   *4 selection     →  handset default  →  primary line
```

Both chains bottom out in the same place, so "which line am I on by default"
is one answer rather than two that can disagree.

**Not "whichever sorts first".** The primary line is `policy.toml` by name —
the file that already means *default* — so adding `policy.aaa.toml` cannot
silently steal 911 or your outbound identity. That kills the order-dependence
worry outright: nothing about the most safety-critical route depends on
filesystem ordering or on where a block sits in a file.

`doorman check` prints both, prominently, and says whether each was **chosen or
inferred**. So does the startup log, every boot. Defaulting rather than
requiring means there is no state where somebody forgot; announcing means the
default is never a surprise.

Not inferred from the line the caller is on: whoever grabs the nearest handset
has no idea which line they are on, and E911 is registered per DID against a
street address, so the trunk carrying it must be the one whose address is
filed.

### When it is not registered

If the designated trunk is down at the moment of the call, **911 falls over to
any registered trunk.** This is a deliberate trade and it deserves stating
plainly: the location data the dispatcher receives may then be wrong, which is
genuinely bad — but a connected call lets a human say their address out loud,
and a failed call gives them nothing at all. Connection first.

### What has to be said out loud

- `doorman check`: which trunk, chosen or inferred, and whether it has a
  registered address.
- Startup: the same, every boot.
- RUNBOOK and the site: that some providers do not offer E911 in every area, so
  a wrong choice of provider means a phone that cannot call for help.
- And the honest framing: **this is a supplementary phone.** Nobody should make
  it the household's only route to emergency services. Saying so is not a
  disclaimer, it is the accurate description of a hobbyist phone on a
  consumer internet connection that stops working in a power cut.

## Testing

- **M1.1:** the state machine does not change, so `internal/lobby` tests keep
  passing untouched. That is the signal the design is right.
- **M1.2:** the state machine does change, and every one of those tests still
  passes *unedited* — which is the same signal in the form available here. New
  tests cover the disposition knob, the per-line pack, and the known caller's
  route through the collector, including a latency assertion that fails if a
  dial window is ever put in front of the house.
- Router: `line,x`, `leg,x`, no args, unknown line.
- Policy: per-line stores, independent failure.
- Render: golden files per trunk, like handsets.
- **No test requires a live trunk.** Phase 2's real verification is manual and
  belongs in the runbook — CI cannot register with Telnyx.

## Documentation, same pass

The drift guards will insist on most of it.

- `docs/RUNBOOK.md` — adding a second number, then a second provider, end to end.
- `examples/.env.example` + `internal/schema` — the schema test fails otherwise.
- `llms-policy.txt` — move `[line]` and `trunk` out of the "do not emit" list.
  **This matters more than it looks:** unknown keys are silently ignored, so a
  model writing `[line]` today produces a config that validates and does
  nothing.
- `make site-assets` republishes the schema.
- `site/src/data/providers.ts` — mark verified providers verified.

## Rollback

Every phase is additive and reversible by deleting config.

- Remove the `line` argument from the dialplan → default line → today.
- Remove `[line]` → inherited defaults.
- Remove `trunks.toml` → hand-written `pjsip.conf`, which is Phase 1's world.

No migration, no persistent state, nothing to un-migrate.

---

# Risks

| Risk | Mitigation |
|---|---|
| **911 leaves by the wrong trunk** | Designated trunk, defaulting to the first declared. `doorman check` and every startup say which, and whether it was chosen or inferred. Blocks Phase 2 |
| **doorman down takes out outbound calling** | The plain `_NXXNXXXXXX` dialplan path keeps working with a house default CID. `*4` is enhancement, not dependency |
| Outbound CID rejected by a provider that does not own the number | Validate `outbound_cid` against its trunk at check time |
| Dialplan typo in a line name | Falls back to default and logs. Cannot be caught at startup — doorman cannot read the dialplan. Accepted |
| Generated PJSIP hand-edited | Same rule as handsets: it is an output. Header comment says so |
| Scope creep toward tenancy | Every one is a no — per-line users, per-line auth, per-line concurrency caps, a web UI |

---

# Out of scope

Named conference rooms · screened answer · SMS · porting automation · per-line
concurrency limits · anything multi-tenant.

Related: `docs/product-extensions.md` §9 and §10, issue #5, and
`.plans/s03-provider-balance-checking`.
