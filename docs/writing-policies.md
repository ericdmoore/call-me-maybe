# Writing policies

`policy.toml` is the file that decides who gets connected. It changes weekly,
it is meant to be edited by hand at 9pm, and a file that fails validation is
rejected on reload while the previous good policy stays in service — so you can
edit it on a live phone system without holding your breath.

This is the reference. Two things are more authoritative than it:

```bash
doorman schema policy   # every key, type, pattern, and cross-file reference
doorman check           # whether *your* file is actually valid
```

`doorman schema` describes shape. **`doorman check` is the authority on
validity**, because roughly thirty semantic rules — cross-file references,
mutual exclusions, "a caller needs somewhere to go" — cannot be expressed as a
schema. Run it before you rely on anything.

---

## The three files, and why they are three

| File | Holds | Changes |
|---|---|---|
| `.env` | secrets and tuning | rarely |
| `handsets.toml` | the hardware — what phones exist | when you buy a phone |
| `policy.toml` | the rules — who gets in, what rings, when | weekly |

They are split by **cadence and author**, not by tidiness. A bedtime edit to
`policy.toml` cannot invalidate your handset inventory, and `doorman render`
generates the Asterisk side from `handsets.toml` so the inventory and the
dialplan cannot drift apart.

Every handset id used in `policy.toml` must exist in `handsets.toml`. That is
checked at load, so a typo is a startup error rather than a phone that silently
never rings.

---

## `[house]` — what a known caller gets

```toml
[house]
handsets = ["kitchen", "living-room", "office", "primary-bed"]
voicemail = "family"
caller_id_format = "Lobby: {name} <{number}>"
```

| Key | Type | Meaning |
|---|---|---|
| `handsets` | list | Handset **or group** ids that ring together. Required. |
| `voicemail` | string | Mailbox for callers nobody answers. Omit and they get a polite dismissal instead. |
| `caller_id_format` | string | What the handset display shows. `{name}` is the caller or extension label; `{number}` is the normalised number. |

This is the whole-house ring for people on the allow-list. Everyone else meets
the lobby.

---

## `[[people]]` — the allow-list

```toml
[[people]]
name = "Grandma"
numbers = ["512-555-0100"]

[[people]]
name = "Eric"
numbers = ["+15125550101", "512-555-0102"]
notes = "mobile + work line"
```

| Key | Type | Meaning |
|---|---|---|
| `name` | string | Shown on the handset display and in logs. Required. |
| `numbers` | list | Any format. Required. |
| `notes` | string | Free text for whoever edits this next. |

**Numbers are normalised to E.164 at load.** Write them however you like —
`512-555-0100`, `(512) 555-0100`, `+15125550101` — because the same person
arrives in a different format depending on the originating carrier, and
comparing raw strings is how Grandma ends up talking to a bouncer. Probe any
value:

```bash
doorman e164 "(512) 555-0100"
```

A number that cannot be parsed is a **load error**, not a silent never-match.

> **Caller ID is not authentication.** On the phone network it is asserted by
> the caller, not proven. The allow-list decides who *skips the lobby*; it
> should never be the only thing between a caller and something consequential.
> See the threat model in [RUNBOOK.md](RUNBOOK.md).

---

## `[[schedules]]` — named time windows

```toml
[[schedules]]
id = "school-night"
enabled = true
start = "20:30"
end = "07:00"
days = ["SU", "MO", "TU", "WE", "TH"]
```

| Key | Type | Meaning |
|---|---|---|
| `id` | string | Referenced by extensions. Lowercase alphanumeric, dash, underscore. Required. |
| `start`, `end` | `HH:MM` | Local time. Required. |
| `days` | list | `SU MO TU WE TH FR SA`. Empty means every day. |
| `enabled` | bool | Defaults true. False makes every reference inert **without deleting the definition**. |

Two things worth knowing.

**A window that crosses midnight belongs to the day it starts.** `20:30`–`07:00`
on `MO` covers Monday evening through Tuesday morning. That is why the example
lists `SU` through `TH` for school nights: Friday and Saturday evenings stay
open.

**`enabled = false` is the holiday switch.** One edit turns bedtime off for
spring break without hunting through every extension that references it, and
turns it back on without reconstructing the times from memory.

---

## `[[extensions]]` — what unknown callers can dial

An extension is a destination in the lobby. Callers who are not on the
allow-list dial one of these.

```toml
[[extensions]]
pin = "428917"
label = "Kitchen"
handsets = ["kitchen"]
```

| Key | Type | Meaning |
|---|---|---|
| `pin` | string | Digits only, minimum 4. Must be unique. Required. |
| `label` | string | Human name; used by `doorman rotate <label>`. Required. |
| `handsets` | list | Simple form: one ring stage. **Mutually exclusive with `steps`.** |
| `steps` | list | Ladder form: escalating stages. **Mutually exclusive with `handsets`.** |
| `voicemail` | string | Mailbox when nobody answers, and the afterhours destination. |
| `afterhours` | string | A `[[schedules]]` id. |
| `afterhours_ring` | list | Ring these during the window instead of taking a message. |
| `enabled` | bool | Defaults true. |

### Choosing a PIN

**Pick one you will remember.** An extension is meant to be given out — a kid
tells a friend, a friend saves it — and a number nobody can recite is a number
nobody uses. Memorable is a feature.

What the loader refuses is much narrower than "anything a human chose":

| Refused | Because |
|---|---|
| Fewer than 4 digits | Too small a space to be worth anything |
| `123456`, `654321`, `456789` | Sequences |
| `111111`, `000000` | Every digit the same |
| `121212`, `123123` | A short block repeated |
| A handful of famous ones | They are the first thing anyone tries |

Everything else is yours. `428917`, a date, the year you moved in, the number of
the house — all fine.

**Why that line and not "never choose"?** The rate limiter caps a caller at a
few failures an hour, which makes searching a million combinations hopeless. It
does nothing at all against `123456`, because guessing that does not require
searching — it requires one guess. So the rule is not *do not choose*, it is
*not the ones everybody tries first*.

Dates are deliberately allowed. They are weaker than random, but there is no way
to tell `090317` from any other number without knowing your family, and refusing
every date-shaped PIN would reject a large slice of the memorable ones for a
guess.

### When to run `doorman rotate`

Rotation is **manual and deliberate**. The daemon never rotates on its own,
because changing a PIN breaks everyone who has the old one — which is exactly
the population you gave it to on purpose.

Run it when:

- **A PIN has leaked.** Written on a whiteboard, forwarded in a group chat,
  committed to a repository, or given to someone who should no longer have it.
- **A social circle changed** and the old crowd should not keep access.
- **You would rather not choose.** `doorman rotate "Kids"` picks one with
  `crypto/rand`, and it will never generate something the loader would refuse.

Do **not** run it on a schedule. Rotating a house extension every ninety days is
security theatre with a real cost: everybody has to learn a new number, and the
number is the whole point. Rotate on a reason, not on a calendar.

It rewrites `policy.toml` in place with comments intact, validates before
writing, writes atomically, and the running daemon picks the change up within a
second. New PINs print to stdout once — the only place a PIN may ever appear.
They are **never logged**, and that is enforced by a static analysis pass rather
than by convention.

### Ladders: `[[extensions.steps]]`

```toml
[[extensions]]
pin = "902118"
label = "Kids"
voicemail = "kids"

  [[extensions.steps]]
  handsets = ["kids-room"]
  rings = 3

  [[extensions.steps]]
  handsets = ["adults"]      # a group
  rings = 4
```

Stages ring in order, escalating on no-answer, then the mailbox.

| Key | Type | Meaning |
|---|---|---|
| `handsets` | list | Handset or group ids. Required. |
| `rings` | int | Duration in ring cycles of roughly six seconds. |
| `seconds` | int | Exact duration, overriding `rings`. |

Use `seconds` when the number matters — a longer window for someone who needs
time to reach a phone reads better as `seconds = 45` than as a ring count.

### Quiet hours, and redirecting instead of silencing

```toml
afterhours = "school-night"
afterhours_ring = ["adults"]
```

With `afterhours` alone, a caller during the window goes **straight to
voicemail** — the line does not ring at all.

With `afterhours_ring`, the call rings there instead. If nobody answers it still
falls back to the mailbox, so the redirect narrows what happens during the
window rather than removing the safety net.

That one field is what expresses:

- **Homework hours** — 4pm to 6pm, ring the adults rather than the kid's room.
- **A rotating night shift** — one parent Monday to Wednesday, the other the
  rest, without the caller needing to know whose night it is.
- **Babysitter forwarding** — the evening window rings a different handset.

`afterhours` requires **either** `voicemail` or `afterhours_ring`. A caller
during quiet hours has to have somewhere to go.

---

## Groups

Groups live in `handsets.toml`, and a group id works **anywhere a handset id
does** — `[house]`, `extensions.handsets`, ladder steps, `afterhours_ring`.

```toml
# handsets.toml
[[groups]]
id = "adults"
label = "Adults"
handsets = ["kitchen", "primary-bed"]
```

Groups may contain handsets only, never other groups. No cycles, no surprises,
and the expansion happens once at load.

---

## Templates

You do not have to write a ladder by hand:

```bash
doorman template list
doorman template show kids-line
doorman template apply kids-line          # prints the TOML
doorman template apply kids-line --apply  # appends it
```

A template asks questions and emits **ordinary policy TOML** — nothing it writes
is special, and you can edit or delete it afterwards like anything else in the
file. Templates may emit extensions and schedules only; one that could write
`[[people]]` could add its own author to your allow-list.

The format is published, and a template can be checked before you trust it —
including one piped in from somewhere else, without writing it to disk first:

```bash
doorman schema template                  # the format, as JSON Schema
doorman template lint kids.toml          # validate on disk
curl -fsSL https://…/kids.toml | doorman template lint -
doorman template apply --file kids.toml  # use a file, not an installed id
```

Linting works because a template is data: unknown question types, dangling
`$references`, and hardcoded PINs are all caught at parse time. A text-templating
format could not do this — its validity would depend on the answers.

---

## How a file is validated

Run `doorman check`. It applies every rule below and reports each problem
separately, so you fix them in one pass rather than one restart at a time. The
same validator runs in the daemon and behind `doorman lsp`, so your editor
underlines the same mistakes as you type.

Rules worth knowing about before you hit them:

- Every handset or group id referenced here must exist in `handsets.toml`.
- Extension PINs must be unique, digits only, and at least four digits.
- `handsets` and `steps` are mutually exclusive; exactly one is required.
- Each ladder step needs `rings` or `seconds`.
- `afterhours` must name a defined schedule, and needs `voicemail` or
  `afterhours_ring`.
- `afterhours_ring` without `afterhours` is an error — there is no window for it
  to apply to.
- Mailbox and schedule ids are lowercase alphanumeric, dash, underscore.
- Allow-list numbers must parse to E.164.

## Editing a live system

`POLICY_WATCH=true` re-reads the file on change, so edits go live within a
second without a restart.

**An invalid file cannot take the phone down.** The reload is rejected, the
problem is logged, and the last good policy stays in service. That is a
deliberate property, not a happy accident — but it is not a reason to skip
`doorman check`, because a rejected reload means your edit is not live and the
only sign is a log line.
