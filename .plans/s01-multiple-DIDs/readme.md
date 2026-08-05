# s01 · Multiple DIDs — development plan

One box, several phone numbers, each with its own rules.

Architecture and the reasoning behind each decision: [`arch.md`](arch.md).
Backlog entry with acceptance criteria: `docs/TASKS.md` §7.

**Status:** planned, not started.

---

## Why this one first

It is the spine. Office hours, a shallow IVR, distinctive ring, per-line
voicemail and the whole "curt doorman on the home line, courteous concierge on
the business line" idea are each *more useful* with a second number and several
are pointless without one.

It also scores 9 on alignment (`docs/product-extensions-grades.md`) despite
needing a new concept, because the concept is clean: a mapping from inbound
number to policy. It fights no invariant, needs no runtime state, and Asterisk
already knows which number was dialled.

---

## Shape of the work

Four groups. **7a is a hard prerequisite. 7b, 7c and 7d are independent of each
other** and can land in any order once 7a exists.

```
7a routing spine ──┬── 7b line identity
                   ├── 7c outbound identity   ← do not ship 7a without this
                   └── 7d per-line observability
```

---

## 7a · Routing spine

The dialplan names the line; doorman keeps one policy store per line; the
router picks the right `Deps` at `StasisStart`.

**Files:** `cmd/doorman/main.go`, `internal/config`, `internal/policy`,
`internal/schema`, `asterisk/extensions.conf`

- [ ] `Stasis(${DOORMAN_APP},line,<name>)` parsed alongside the existing `leg`
      argument.
- [ ] `policy.<line>.toml` discovered at startup; bare `policy.toml` is the
      default line.
- [ ] One `policy.Store` per line — the reason for separate files rather than
      `[[lines]]` sections. A bad business policy cannot take down the house.
- [ ] Unknown line name → default line + a loud log. **Never drops the call.**
- [ ] `internal/lobby` unchanged. If a change is needed there, the design is
      wrong — see `arch.md`.
- [ ] Rate-limit key includes the line.
- [ ] `doorman check` prints every line found and what each resolves to.
- [ ] `doorman schema` gains the `[line]` section; the drift tests will insist.

**Done when:** two policy files, two dialplan contexts, two numbers behaving
differently, and an install with neither still behaves byte-identically.

### Tests

- Router: `line,biz` selects the biz deps; `leg,<id>` still routes to a session;
  no args gets the default.
- An unknown line falls back and does not drop the call.
- An invalid `policy.biz.toml` leaves the home line serving.
- Rate-limit budgets do not bleed between lines.

---

## 7b · Line identity

What makes a line feel like itself rather than a copy of the house.

**Files:** `internal/policy`, `internal/lobby` (prompts only), `internal/schema`

- [ ] `[line]` section: `label`, `number`, `prompts`, `on_no_input`.
- [ ] Per-line prompt prefix overriding `PROMPT_MEDIA_PREFIX`.
- [ ] `on_no_input` — the disposition knob. `dismiss` is today's behaviour and
      the default; `voicemail` and `ring-house` are new.
- [ ] Known callers can reach the collect loop, with a timeout meaning *admit*
      rather than *dismiss* (the home line's "no dial → ring everything").
- [ ] Schedules usable at line scope, not only per-extension.

**Depends on 7a.** Blocked on nothing else.

---

## 7c · Outbound identity

**Do not ship 7a without this.** The outbound dialplan sets no caller ID today,
so every call out presents the trunk default — across several lines, every
customer you ring back saves the wrong number.

**Files:** `asterisk/extensions.conf`, `internal/policy`, `internal/render`

- [ ] `[line] outbound_cid`.
- [ ] A dial prefix selects the line for one call (`*3` + number).
- [ ] Per-handset default line for the common case.
- [ ] `doorman check` catches a prefix colliding with an extension or feature
      code.

---

## 7d · Per-line observability

**Files:** `internal/calls`, `cmd/doorman/calls.go`

- [ ] `line` on the call record. One log, not one per line, so a day still
      reads in order.
- [ ] `doorman calls --line biz`.
- [ ] If metrics land (TASKS §4), a `line` label — and still **no caller
      identifiers in labels**.

---

## Documentation to update in the same pass

The drift guards will catch most of this, which is the point of having them.

- [ ] `docs/RUNBOOK.md` — a section on adding a second number end to end:
      buy the DID, point it at a context, write the policy, verify.
- [ ] `examples/.env.example` and `internal/schema` — the schema test fails if
      these disagree.
- [ ] `llms-policy.txt` — move `[line]` out of the "do not emit" list. **This
      matters more than it looks:** unknown keys are silently ignored, so a
      model writing `[line]` today produces a config that validates and does
      nothing.
- [ ] `llms.txt`, `docs/doorman.1` — `doorman calls --line`.
- [ ] `make site-assets` regenerates the published schema.

---

## Risks

**The dialplan is hand-edited and doorman cannot see it.** A typo'd line name
is invisible until a call arrives. Mitigated by falling back rather than
dropping, and by `doorman check` printing what it knows — but the mismatch
cannot be caught at startup. Accepted.

**Prefix collisions.** `*3` for line 3 has to coexist with `*97` voicemail and
whatever else accumulates. Validation catches it; the numbering scheme wants a
moment's thought before it is published.

**Scope creep toward tenancy.** Every one of these is a "no": per-line users,
per-line authentication, per-line concurrency caps, a web UI for lines. One
operator owns every line. `docs/SUSTAINABILITY.md` explains why hosted
multi-tenant PBX is on the do-not-chase list.

---

## Out of scope

Named conference rooms · screened answer (whisper) · SMS · per-line
transcription · number porting automation.

Related: `docs/product-extensions.md` §9 (the multi-line operator) and §10
(provider account health, which becomes more urgent once several businesses
share one prepaid balance).
