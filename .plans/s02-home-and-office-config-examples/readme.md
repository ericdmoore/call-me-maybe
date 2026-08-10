# s02 · Home and office config examples — development plan

Worked, shipped, tested example configurations that a person or a model can
start from instead of assembling one from a schema.

**Status:** **M1 and M2 are complete.** The framework is in
`examples/README.md`, and both examples that need nothing unbuilt have
shipped: `examples/scenarios/solo-business/` (E2) and
`examples/scenarios/family-line/` (E1). M3 (publishing to the site) is next
and untouched. E3 and E4 want s01 Phase 1, which has since landed, so M4 is
now unblocked; E5 still waits on primitives that do not exist.
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
- **A model** helping a founder invents plausible keys. Unknown keys used to
  be silently ignored, so the config validated and quietly did nothing — the
  worst outcome available.

An example that is *tested* fixes both, because it cannot rot without CI
noticing.

**Since this was written, the loader changed underneath it.** `doorman check`
now refuses an unknown key and suggests the nearest real one, and prints every
extension with the defaults it fell back to. That moves the value of a worked
example: it is no longer the only defence against an invented key, it is the
thing that shows what a *good* answer looks like once the invented ones are
impossible. It also means an example cannot smuggle in a key that does not
exist — writing one is now a failed build rather than a silent no-op, which is
what makes "each example states what it needs that does not exist yet" a claim
about prose rather than about config.

---

## The examples to ship

Drawn from a real specification rather than invented, so they cover the corners
that made the primitives list.

### E1 · The family line — **shipped**

`examples/scenarios/family-line/`. Handsets: kitchen, five bedrooms, office,
playroom.

- **Known caller** hears a welcome, then may dial an extension *or say nothing*
  — silence rings the whole house.
- **Unknown caller** gets the lobby, ten seconds, then "Good day."

**Needed, and now has:** a per-caller-class no-input destination (silence means
*admit* for a known caller, *dismiss* for a stranger). That is s01 M1.2's
`on_no_input`, and this example is the reason it exists. It landed, and E1 is
expressible in full — `doorman check` accepts it.

Two things the sketch above implied that the file has to correct, both
verified against the state machine rather than assumed:

- "hears a welcome, **then** may dial" is not what happens. The welcome prompt
  *is* the dial window and the whole of it: a known caller barges in with a
  digit over the greeting, and the house rings the instant it ends otherwise.
  There is deliberately no tail of silence — see s01 M1.2.
- "ten seconds" is `FIRST_DIGIT_TIMEOUT_MS`, one install-wide `.env` value. No
  policy key expresses it, per line or per caller class.

Stated limits in the file: no schedule reaches `[house]`, so an allow-listed
caller rings five bedrooms at 02:00; and the allow-list is one class with one
destination, so "Grandma rings the kitchen" is not expressible.

### E2 · A one-person business line — **shipped**

`examples/scenarios/solo-business/`. Business hours from `[[schedules]]`,
allow-listed clients ringing both working handsets, four extensions — one per
concern — each ladder ending in its own mailbox rather than a dismissal.

**Shipped first, and needed nothing unbuilt.**

**The limit this was supposed to document is no longer true.** A stranger who
dials nothing is *not* dismissed here: `on_no_input = "voicemail"` shipped with
s01 M1.2 and takes the message. The limits that are real today, and are the
ones written in the file:

- **It cannot close at weekends.** `afterhours` takes one schedule id, and one
  `[[schedules]]` window is at most a single crossing of midnight, so "shut
  from Friday 17:30 until Monday 08:30" cannot be written. Evenings close;
  Saturday morning rings.
- **Business hours do not reach the allow-list.** `[[schedules]]` apply to
  extensions only.
- **A stranger who guesses at the keypad is still dismissed.** `on_no_input`
  governs an empty dial window; exhausting `MAX_PIN_ATTEMPTS` ends in
  "good day", and a rate-limited caller never hears the greeting at all.

### E3 · Home plus business on one box

E1 and E2 together, two policy files, two numbers. **Needs s01 Phase 1**, which
has landed — so this is now buildable. The layout already carries it: a second
line is `policy.example.<line>.toml` beside the primary file, which is the same
sibling pattern `DiscoverLines` looks for at runtime.

Also the first example where `*4` and the primary-line rule appear, and the
right place to show that a kid never meets either.

### E4 · The multi-venture operator

One home line plus several business lines, per-handset default identities.
**Needs s01 Phase 1 including M1.3**, which has landed —
`[line] outbound_cid` and `outbound_handsets` both exist, and `doorman check`
already reports a handset claimed by two lines.

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
   file — and, more usefully in practice, what it deliberately does not do.
   An example that quietly requires an unbuilt feature is worse than no
   example, and one that quietly *fails* at something is how somebody learns a
   limit from a phone call.
3. **Each is validated in CI.** `doorman check --allow-placeholders` already
   runs against the shipped example; extend it to every one of these. An
   example that stops loading must fail the build.
4. **Each is reachable by a model.** Published beside the schema at
   `callmemaybe.cc/examples/…` via `make site-assets`, and linked from
   `llms-policy.txt`.

---

## Milestones

### M1 · The framework — **done**

- `examples/scenarios/<slug>/`, holding `policy.example.toml`,
  `handsets.example.toml` and optionally `trunks.example.toml`. The filenames
  match the top-level templates, which keeps them clear of the
  `policy.toml`/`handsets.toml`/`trunks.toml` gitignore rules and makes the
  copy instruction the same everywhere. A slug names the situation, not the
  plan item.
- **A directory holding a `policy.example.toml` is an example.** That filename
  is the entire discovery rule — there is no list to maintain, and adding a
  directory adds it to the build.
- Both existing CI steps were extended to loop over the discovered set rather
  than naming one file: the `--allow-placeholders` validation, and its inverse
  asserting that no example passes a strict check. `.githooks/pre-push` runs
  the same loop, and `TestShippedExamplesHaveNoUnknownKeys` in
  `internal/policy` now walks every example as a subtest, so `go test ./...`
  catches a broken one before a push does. The test fails if discovery finds
  fewer than two — a loop that silently finds nothing is the same defect the
  examples exist to prevent.
- Every file carries a `FOR` / `NEEDS` / `DOES NOT` / `CHECK IT` header, and
  `examples/README.md` documents the layout and the two CI assertions.

### M2 · Ship E2, then E1 — **done**

Both landed together: s01 M1.2 had already shipped, so E1 was no longer
blocked. E2 is `examples/scenarios/solo-business/`, E1 is
`examples/scenarios/family-line/`. Both are accepted by `doorman check
--allow-placeholders` and both refuse a strict check, which is the sentinel
doing its job.

### M3 · Publish for models — next

Untouched. `make site-assets` and `site/public` are deliberately unchanged by
M1/M2, so this is still a clean piece of work.

- `make site-assets` copies examples beside the schema.
- `llms-policy.txt` links them and says which is which — it already carries two
  worked examples inline, which should become pointers now these exist so
  there is one copy rather than two.

### M4 · E3 and E4 — unblocked

s01 Phase 1 and most of Phase 2 have landed since this was written, so both are
buildable now rather than waiting.

### M5 · E5, when the primitives land

Still blocked: variable-length `#`-terminated collection and voicemail metadata
do not exist.

---

## Risks

| Risk | Mitigation |
|---|---|
| **Examples drift from the schema** | CI validates every one. This is the whole point |
| An example implies an unbuilt feature | Header states requirements. An unbuilt *key* can no longer appear at all: `doorman check` rejects unknown keys and CI runs it over every example, so this is now only a risk in the prose |
| Two copies of the same example (file + `llms-policy.txt`) | Make the guide point at the files. Drift between a doc and a file is exactly what cost four red builds on `llms.txt` |
| A real PIN or number in an example | `555-01xx` only, reserved for fiction. The secrets check already runs pre-push |
| Examples become a support surface | They are starting points, not supported configurations. Say so once |

---

## Out of scope

A config generator UI · per-provider examples (that is issue #5 and belongs in
the runbook) · anything that ships a real credential.
