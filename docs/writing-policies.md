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

## `[line]` — what this number is, and how it treats a stranger

Optional, and every key in it is optional. **A file with no `[line]` section
behaves exactly as it did before the section existed**, so nothing here is
something you have to learn to keep a working phone working.

```toml
[line]
label             = "Mertaugh Enterprises"
number            = "+15125550142"
prompts           = "concierge"
on_no_input       = "voicemail"
outbound_cid      = "+15125550142"
outbound_handsets = ["office"]
```

| Key | Type | Meaning |
|---|---|---|
| `label` | string | Human name for this number. Shown by `doorman check`, and displayed on the ringing handset when a caller reaches the house without dialling an extension — so whoever picks up knows which line to answer as. |
| `number` | string | The DID this line answers, in any format; normalised to E.164 at load. **Identity, not routing** — the dialplan already said which line a call arrived on, so nothing matches against this. |
| `prompts` | string | Prompt pack for this line, overriding `PROMPT_MEDIA_PREFIX`. An Asterisk media prefix under `/var/lib/asterisk/sounds/`, not a filesystem path. |
| `on_no_input` | enum | What a caller who dials nothing gets: `dismiss` (default), `voicemail`, or `ring-house`. |
| `outbound_cid` | string | What a call *placed as this line* shows the person being rung. Any format; normalised to E.164. Absent means whatever the trunk sends. |
| `outbound_handsets` | list | Handsets (or groups) that call as this line without being asked. Requires `outbound_cid`. |

This is the section that makes one number a curt doorman and another a
courteous concierge on the same binary. On a box answering several numbers it
goes in each `policy.<line>.toml`; see "Add a second number" in the runbook.

### `on_no_input`

```
dismiss      (default)  the parting prompt, then a hangup — today's behaviour
voicemail               they land in the [house] mailbox: never drop a lead
ring-house              the house rings for a caller who dialled nothing
```

`voicemail` requires `[house] voicemail`, for the same reason `afterhours`
requires one: a caller you send to a mailbox needs a mailbox to land in.
`doorman check` refuses the combination rather than discovering it mid-call.

**`ring-house` is worth pausing over.** It means anybody patient enough to say
nothing reaches the house, so on that line the allow-list is a shortcut past
the lobby rather than a gate in front of it. That is exactly what some
households want — a home line where a caller who is confused by the menu still
gets a human — and it should be a decision rather than a surprise. `doorman
check` prints the consequence next to the setting.

Two things it deliberately does **not** do:

- **It is not a rate-limit bypass.** A caller who has burned their failure
  budget is dismissed before the lobby opens, so the disposition never gets a
  say.
- **It does not apply to exhausted PIN attempts.** Someone guessing wrong
  until their attempts run out is still dismissed. The key governs an empty
  dial window, which is what its name says.

### `outbound_cid` and `outbound_handsets`

The first four keys are about calls coming *in*. These two are about calls
going *out*, and without them every outbound call presents whatever the trunk
sends — invisible with one number, and with several it means every customer
you ring back saves the wrong one.

```toml
# policy.toml — the primary line
[line]
outbound_cid = "+15125550100"

# policy.biz.toml
[line]
outbound_cid      = "+15125550142"
outbound_handsets = ["office"]
```

That is the whole configuration. The office phone picks up, dials, and the
callee sees the business number. Every other phone in the house presents
`+15125550100`, because **a handset no line claims presents the primary line**
— plain `policy.toml`, the file with no line name in it.

**One rule, two jobs.** The primary line is also the route 911 leaves by. So a
child who picks up the nearest phone and dials 911 goes out by the trunk whose
registered address is this house, and there is no arrangement of keypresses
that puts them one digit from an emergency call leaving as a business line
with somebody else's address on file. The two defaults being one rule is what
guarantees that.

**Why the claim lives here** and not as a `line = "biz"` key on each handset:
`handsets.toml` is one shared hardware inventory and cannot hold something that
is true of only one line. Writing it in the line's own file also means deleting
the file removes the claim, and it makes "two lines claim the same phone" a
question something can answer — `doorman check` reports it as an error, and so
does `doorman render`. The daemon warns instead and gives the phone to the
primary line, because a bad edit must never take a phone down.

**When it takes effect.** `outbound_cid` reaches the `*4` console on the next
policy reload, like any other policy edit. It reaches the plain dial path at
the next `doorman render`, because that path never touches doorman at all — a
handset dialling a number talks to Asterisk and nothing else, which is exactly
what keeps outbound calling working when doorman is down.

### `*4` — calling as another line

Dial `*4` from any handset. It reads out the numbers this box can call as
("one … five one two five five five zero one zero zero; two …"), takes a
digit, reads back the number you will present, then takes the number to dial —
`#` to finish, or just stop dialling. It speaks with Asterisk's own sounds
rather than a prompt pack, so no pack has to supply anything for it to work.

You will rarely press it. A handset default covers the common case, and the
measure of this working is that most of the household never learns `*4` exists.

**It refuses 911, and always will.** E911 is registered per DID against a
street address, so an emergency call placed as another line would reach a
dispatcher with the wrong address on screen. The console beeps once and lets
go of the handset so you can dial 911 directly, which is untouched and correct.
The dialplan context it hands calls to contains no emergency pattern either, so
two independent things would have to be wrong.

### Known callers and the dial window

Someone on the allow-list hears the welcome prompt and rings the house, and
that has not changed — including the timing. What is new is that **the
greeting is now a dial window**: press a digit over it and you land in the
extension collector, so Grandma can reach the kids' room directly instead of
ringing every phone in the house.

Press nothing and the house rings the instant the prompt ends, with nothing
added. The greeting is the whole window and there is no tail of silence after
it, because a pause on a phone call is indistinguishable from a dead line and
every known caller would pay for it to serve the rare one who dials.

**Nothing at the keypad can cost an allow-listed caller the house.** Dial
nothing, dial half an extension and stop, or get it wrong until the attempts
run out — every one of those ends with the house ringing. They were admitted
the moment their number matched; `on_no_input` does not apply to them.

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

| Refused | Example | Because |
|---|---|---|
| Fewer than 4 digits | `123` | Too small a space to be worth anything |
| Every digit the same | `111111` | |
| A run | `123456`, `654321` | Counting up or down, including in twos (`02468`) |
| A short block repeated | `121212`, `123123` | |
| A palindrome | `123321` | Reads the same backwards |
| One digit off all-the-same | `111112`, `011111` | See below |
| One digit off a run | `123457`, `023456` | See below |
| A handful of famous ones | `696969`, `159753` | The first thing anyone tries |

The last two are the ones worth explaining. A real guessing list is not just the
patterns — it is **the patterns plus one typo**. `111112` and `123457` are the
second and third things anyone tries, so they have to go with the first.

Those two rules apply at **six digits and up only**. A four-digit PIN has three
steps in it, so "one away from a run" leaves nothing recognisable — `4821` is one
substitution from `4321` and looks like nothing at all. Applying the rule there
would refuse ordinary choices for no benefit.

**How much does this cost you?** All of the above together refuses **3,083 of
the million** six-digit numbers — 0.31%, about one in every 325. Three quarters
of those are palindromes and repeated blocks, which people rarely land on by
accident. `TestRefusalRateIsSmall` measures it exhaustively on every run, so the
number in this paragraph cannot quietly grow.

Everything else is yours. `428917`, a date, the year you moved in, the number of
the house — all fine.

**Why that line and not "never choose"?** The rate limiter caps a caller at a
few failures an hour, which makes searching a million combinations hopeless. It
does nothing at all against `123456`, because guessing that does not require
searching — it requires one guess. So the rule is not *do not choose*, it is
*not the ones everybody tries first, and not the near-misses of those*.

Some things are deliberately **not** refused, because the rule would cost more
than the guess it prevents:

- **Dates.** There is no way to tell `090317` from any other number without
  knowing your family, and date-shaped PINs are a large slice of the memorable
  ones.
- **Near-misses of a repeated block** — `121213` and friends. Correct in
  principle, but it would refuse roughly six percent of the space, twenty times
  everything else combined, and it would reject numbers that look arbitrary to
  whoever picked them.
- **Runs with a step of three or more.** `147036` is not a pattern anyone
  recognises, so it is not on anybody's list.

If a PIN is refused, `doorman check` says which rule caught it and why — the
message names the pattern, so the fix is obvious rather than a guessing game.

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
