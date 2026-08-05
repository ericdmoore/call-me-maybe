# s01 · Multiple lines — development plan

Build one box that answers several phone numbers, each with its own rules —
first on one provider, then across several.

Reasoning and rejected alternatives: [`arch.md`](arch.md).
Backlog with acceptance criteria: `docs/TASKS.md` §7.

**Status:** planned. Nothing started.

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

## M1.1 · Routing spine

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

## M1.2 · Line identity

**Build**

- `[line]` section: `label`, `number`, `prompts`, `on_no_input`.
- Per-line prompt prefix overriding `PROMPT_MEDIA_PREFIX`.
- `on_no_input` — `dismiss` (today's behaviour, the default), `voicemail`,
  `ring-house`.
- Known callers can reach the collect loop, where a timeout means *admit*
  rather than dismiss.

**Deliverable:** the home line dismisses strangers, the business line takes a
message. Same binary, opposite defaults.

## M1.3 · Outbound caller ID

*Do not ship M1.1 without this.* Today `Dial(PJSIP/1${EXTEN}@voipms,60)` sets
no caller ID at all, so every outbound call presents the trunk default. With
several lines, every customer you ring back saves the wrong number.

**Build**

- `[line] outbound_cid`.
- A dial prefix picks the line for one call (`*3` then the number).
- Per-handset default line, for the common case of not prefixing.
- `doorman check` catches a prefix colliding with an extension or feature code.

**Verify:** place a call with each prefix and read the number off a real
handset. Not from logs — logs show what we sent, not what arrived.

## M1.4 · Per-line observability

**Build**

- `line` on the call record; `doorman calls --line biz`.
- One log file, not one per line, so a whole day still reads in order.

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
- The M1.3 dial prefix now selects a trunk as well as a caller ID.

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
trunk there is. With several:

- Which trunk carries 911, and is it the one whose DID has a registered address?
- Does every provider offer E911 here at all? Several do not.
- If that trunk is unregistered, does the call fail *loudly*?

The decision: one **designated emergency trunk**, named explicitly in config
rather than inferred from whichever line the caller happens to be on, plus a
startup warning when it has no registered address. A house phone that cannot
reach emergency services is something the people in that house must be told
about — in the runbook and on the site, not in a comment.

## Testing

- The state machine does not change, so `internal/lobby` tests keep passing
  untouched. That is the signal the design is right.
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
| **911 leaves by the wrong trunk** | Designated emergency trunk, explicit in config, warned at startup. Blocks Phase 2 |
| Outbound CID rejected by a provider that does not own the number | Validate `outbound_cid` against its trunk at check time |
| Dialplan typo in a line name | Falls back to default and logs. Cannot be caught at startup — doorman cannot read the dialplan. Accepted |
| Prefix collisions (`*3` vs `*97`) | Validated; the numbering scheme wants thought before it is published |
| Generated PJSIP hand-edited | Same rule as handsets: it is an output. Header comment says so |
| Scope creep toward tenancy | Every one is a no — per-line users, per-line auth, per-line concurrency caps, a web UI |

---

# Out of scope

Named conference rooms · screened answer · SMS · porting automation · per-line
concurrency limits · anything multi-tenant.

Related: `docs/product-extensions.md` §9 and §10, issue #5, and
`.plans/s03-provider-balance-checking`.
