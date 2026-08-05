# s02 · Home and office config examples — development plan

Worked, shipped, tested example configurations that a person or a model can
start from instead of assembling one from a schema.

**Status:** planned. Nothing started.
Related: `llms-policy.txt`, `examples/`, `docs/writing-policies.md`,
`.plans/s01-multiple-DIDs` (several of these examples need it).

---

## Why

`doorman schema` describes shape and `doorman check` decides validity, but
neither shows a person what a *good* configuration looks like. There is one
example in the tree (`examples/policy.example.toml`) and it is a demo of the
syntax rather than a household anyone actually has.

Two audiences, and they fail differently:

- **A person** starts from a blank file, gets the section-per-file rule wrong,
  and gives up before the phone rings.
- **A model** helping a founder invents plausible keys. Unknown keys are
  silently ignored, so the config validates and quietly does nothing — the
  worst outcome available.

An example that is *tested* fixes both, because it cannot rot without CI
noticing.

---

## The examples to ship

Drawn from a real specification rather than invented, so they cover the corners
that made the primitives list.

### E1 · The family line

Handsets: kitchen, five bedrooms, office, playroom.

- **Known caller** hears a welcome, then may dial an extension *or say nothing*
  — silence rings the whole house.
- **Unknown caller** gets the lobby, ten seconds, then "Good day."

**Needs, beyond today:** a per-caller-class no-input destination (silence means
*admit* for a known caller, *dismiss* for a stranger). That is s01 M1.2's
`on_no_input`, and this example is the reason it exists.

### E2 · A one-person business line

Business hours from `[[schedules]]`, allow-listed clients ringing the office
handset directly, an extension per concern with a ladder ending in a mailbox
rather than a dismissal.

**Ships today.** Everything above already works — this one can land first and
should, because it is the honest answer to "can I use this for work" and it
does not wait on s01.

**Documents its own limit:** a stranger who dials nothing is still dismissed.
Say so in the file rather than implying otherwise.

### E3 · Home plus business on one box

E1 and E2 together, two policy files, two numbers. **Needs s01 Phase 1.**

Also the first example where `*4` and the primary-line rule appear, and the
right place to show that a kid never meets either.

### E4 · The multi-venture operator

One home line plus several business lines, per-handset default identities.
**Needs s01 Phase 1 including M1.3.**

### E5 · An answering service

Out-of-hours flow: greeting with hours, "is this number good for a callback",
capture a number if not, take a message.

**Needs the most:** variable-length `#`-terminated collection, and voicemail
metadata to carry the answer. Listed so the example set has a stated ceiling
rather than trailing off.

---

## What makes these different from `examples/`

Not just more files.

1. **Each is a scenario, not a syntax demo.** A named household with stated
   requirements, so a reader can find the one that resembles them.
2. **Each states what it needs that does not exist yet**, at the top, in the
   file. An example that quietly requires an unbuilt feature is worse than no
   example — see the unknown-key defect.
3. **Each is validated in CI.** `doorman check --allow-placeholders` already
   runs against the shipped example; extend it to every one of these. An
   example that stops loading must fail the build.
4. **Each is reachable by a model.** Published beside the schema at
   `callmemaybe.cc/examples/…` via `make site-assets`, and linked from
   `llms-policy.txt`.

---

## Milestones

### M1 · The framework

- Directory layout and a naming convention under `examples/`.
- CI validates every example, not just the one.
- Each file carries a header: who it is for, what it needs, what it does not do.

### M2 · Ship E2, then E1

E2 first because it needs nothing. E1 needs s01 M1.2 for the known-caller
no-input behaviour, so it lands with that.

### M3 · Publish for models

- `make site-assets` copies examples beside the schema.
- `llms-policy.txt` links them and says which is which — it already carries two
  worked examples inline, which should become pointers once these exist so
  there is one copy rather than two.

### M4 · E3 and E4, with s01

### M5 · E5, when the primitives land

---

## Risks

| Risk | Mitigation |
|---|---|
| **Examples drift from the schema** | CI validates every one. This is the whole point |
| An example implies an unbuilt feature | Header states requirements; unbuilt keys never appear, because they are silently ignored rather than rejected |
| Two copies of the same example (file + `llms-policy.txt`) | Make the guide point at the files. Drift between a doc and a file is exactly what cost four red builds on `llms.txt` |
| A real PIN or number in an example | `555-01xx` only, reserved for fiction. The secrets check already runs pre-push |
| Examples become a support surface | They are starting points, not supported configurations. Say so once |

---

## Out of scope

A config generator UI · per-provider examples (that is issue #5 and belongs in
the runbook) · anything that ships a real credential.
