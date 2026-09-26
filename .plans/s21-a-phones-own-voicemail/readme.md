# s21 · A phone's own voicemail — the mailbox is a fact about the handset

**Status:** M1 + M2 shipped in v0.11.0 (2026-09-25, late night); M3 docs
shipped with them. One correction to the sketch below: the provisioning
template already set the phone's voicemail access number (P33 = `*97`),
so the key worked all along — what it opened was the "mailbox?" menu, and
`family` cannot be typed on a keypad. Originally planned the same evening. From the user: "Do you think we
could configure each handset to have its own voicemail? The WP826 has a VM
icon — it would be nice if that was *for that handset*." And the shape of
adding a handset, as they see it: a name, a number, an address book, a
voicemail. Drafted the night s01, s02 and s03 closed.

## What done looks like

Adding a phone is four facts in `handsets.toml` — and they are the four keys
that already exist, one of which starts meaning what it says:

```toml
[[handsets]]
id = "master-bed"
label = "Master bedroom"       # the name
number = 103                   # the number
phonebook = ["house", "caroline"]   # the address book(s) it shows
mailbox = "master-bed"         # ITS OWN voicemail
```

Then `doorman render`, copy, reload, and:

- Somebody in the kitchen dials `103`, nobody answers in thirty seconds (or
  the phone is busy), and they are talking to the master bedroom's mailbox.
  Not the family's. Not a dead line, which is what happens today.
- The master bedroom's phone lights its lamp, and pressing its voicemail key
  opens *that* box, no questions asked: the phone is the credential, the same
  trust the `*4` console already inherits from the LAN. `*98` still reaches
  any box with a PIN, from any phone.
- A stranger who dials the master bedroom's lobby extension and rings out
  lands in the same box, because `policy.toml` says `voicemail = "master-bed"`
  and that is now a box the tool made rather than one somebody remembered to
  add to `voicemail.conf`.
- The kitchen and the theater say `mailbox = "whole-house"` and share one
  box, both lamps lighting; the house's own calls (a known caller nobody
  answered) go on landing in `[house] voicemail`, which may be that same box.
- `doorman init --rooms Kitchen,Theater,MasterBed` writes a mailbox per room
  by default, with a generated PIN each in `.env`, and never again prints
  "set this in voicemail.conf".

## Starting point

- Asterisk's `app_voicemail` does everything: record, store, email the WAV,
  MWI. doorman only releases a lobby caller into `[voicemail-drop]` with
  `MAILBOX` set. That stays.
- `voicemail.conf` is hand-written from the example (`kids`, `adults`,
  `family`); jepsen has one box, `family`. `doorman init` already generates a
  PIN for the house mailbox and prints it with "set this in voicemail.conf".
- `handsets.toml` already carries `label`, `number`, `phonebook` and
  `mailbox` — the user's four facts — but `mailbox` means only "which lamp".
- The generated dialplan rings a room and then ends: `exten => 103,1,Dial(
  PJSIP/master-bed,30)` and nothing after. Room-to-room voicemail does not
  exist.
- `*97` is `VoiceMailMain(@household)`: it asks which box and for a PIN.
- **The provisioning template does not set the phone's voicemail access
  number** (s10 rehearsal: the operator typed `*97` into the web UI on the
  first, hand-configured pass; the zero-touch pass never had it). On a phone
  provisioned by `doorman provision` the voicemail key most likely does
  nothing today. First-customer finding, filed here.
- The pattern for "generated Asterisk config beside a hand-written file" is
  established twice: `pjsip_handsets.conf` and `extensions_handsets.conf`,
  reached by `#tryinclude`.

## Decisions and invariants

**`mailbox` on a handset is the phone's own box, and render makes it.** A
new generated file, `voicemail_handsets.conf`, holds one line per distinct
mailbox any handset names, under `[household](+)` — the standard config
parser's append syntax, so the hand-written `[household]` keeps `family` and
whatever else the operator wrote, and `voicemail.conf` gains one
`#tryinclude`. Two phones naming the same box get one line and two lamps.

**The PIN is a secret and lives where secrets live.** `VOICEMAIL_<MAILBOX>_PIN`
in `.env`, by the same convention as `HANDSET_<ID>_PASSWORD`, substituted by
render and never written into `handsets.toml`. `doorman init` generates one
per mailbox; `doorman rotate --voicemail` rotates them; both print the new
PIN once, the one place a PIN may ever be printed (invariant 1). The
generated file holds them in plain text because Asterisk reads it — same
handling as `pjsip_handsets.conf`: root-owned, 0640, never committed.
`[general] passwordlocation = spooldir` goes into the example, so a PIN
changed from the phone's menu lands in the spool and is not silently undone
by the next render. VERIFY on the box.

**A room call falls into the room's box.** Render emits, for a handset with a
mailbox, `Dial(...,30)` then `VoiceMail(<box>@household,u)` — and the `b`
greeting when `DIALSTATUS` is `BUSY`. A handset with no mailbox keeps today's
dead end, so nothing changes for a phone nobody gave a box. `100`
(ring-all) and `500` (page) are untouched: neither has a single box to land
in, and `[house] voicemail` is policy's answer for the house.

**The voicemail key opens the phone's own box.** Render writes
`set_var=CMM_MAILBOX=<box>` on the endpoint beside `mailboxes=`; `*97`
becomes `VoiceMailMain(${CMM_MAILBOX}@household,s)` when the channel carries
one and the old prompt when it does not, so an unprovisioned phone still
gets a menu. The `s` skips the PIN from the phone that owns the box: a
handset on the LAN is already trusted to place calls as the house and to
page every room; asking it to prove it owns its own voicemail protects
nothing. `*98` is the old behaviour by name — any box, PIN required — for
the parent checking the kids' box from the kitchen. The provisioning
template sets the phone's voicemail access number to `*97` (Grandstream
P33 on account 1 — VERIFY on the WP826, the same way the s10 values were).

**`doorman check` says what it can see.** Every `voicemail = "..."` in policy
is reported as a handset's own box, a box shared by handsets, or "not
generated — make sure it exists in voicemail.conf". A typo in a mailbox name
today is a caller who hears "the person at extension … is unavailable" and
is hung up on; this is the first place it becomes visible before a call.

**Email per box is optional and on the handset.** `email = "..."` on a
handset becomes the mailbox's address in the generated line, so
`attach = yes` sends that person their messages. Absent means no email for
that box, which is what the kitchen wants.

**Nothing about outside callers changes shape.** The lobby's ladders,
`afterhours`, `on_no_input` and `[house] voicemail` all name a box exactly as
they do now; the boxes are simply real.

## What is deliberately out of scope

- Transcription (TASKS §2) and retention (§2c): unchanged and still open.
- Visual voicemail on the handset, or reading the box out on the display:
  the WP826's key dials a number and that is the whole interface.
- Moving `family`/`kids`/`adults` into generation. Policy-only boxes stay the
  operator's; `check` names them rather than owning them.
- Do not disturb (s12) — which now has what it was waiting for.

## Milestones and acceptance criteria

### M1 · The box exists and the room call lands in it — **done** (v0.11.0)

**As built.** `render.Mailboxes` lists every box the handsets name; a box
whose `VOICEMAIL_<BOX>_PIN` is in `.env` is written to
`voicemail_handsets.conf` under `[household](+)` (one handset → its label
and `email`; shared → the id made readable), a box without one is named
in a comment and left to the hand-written file — which is how `family` on
every older install keeps working untouched, and why render never fails
over a box it did not make. The endpoint gains `set_var=CMM_MAILBOX`; the
room's Dial gains `ANSWER→done`, `BUSY→busy`, `VoiceMail(box,u)`,
`VoiceMail(box,b)`. `doorman check` prints a Mailboxes block: each box,
whose it is, whether render writes it, and where policy sends callers.
`doorman init` gives every room its own box with a PIN in `.env` and its
extension pointing there; `doorman rotate --voicemail [box …]` sets or
rotates PINs. `voicemail.conf.example` ends with the `#tryinclude` and sets
`passwordlocation = spooldir` so a PIN changed from a phone is not undone
by the next render.

`voicemail_handsets.conf` generated under `[household](+)`; PINs from
`.env` by convention; `voicemail.conf.example` gains the `#tryinclude` and
`passwordlocation = spooldir`; the room's Dial gains its `VoiceMail` step
(busy and unavailable); `check` reports every policy mailbox against the
generated set; `init` writes one box per room and the PINs into `.env`;
`rotate --voicemail`. Done when, on jepsen, `103` from the kitchen rings out
into the master bedroom's greeting and the lamp lights there; a `voicemail =
"typo"` is named by `check`; and the installed `family` box is untouched.

### M2 · The key is the phone's — **done** (v0.11.0)

**As built.** `*97` checks `CMM_MAILBOX` and goes straight into that box
with the `s` option (no PIN — the LAN handset is the credential, as for
`*4`); a handset with no box gets the old menu. `*98` is the old menu from
any phone. The template already dialled `*97` from the key. The hunt's
greeting-recording step moves to `*98`, since a phone with a box no longer
sees the menu on `*97`.

`CMM_MAILBOX` on the endpoint; `*97` opens it without a PIN, `*98` is the
old menu; the template sets the phone's voicemail access number. Done when
pressing the voicemail key on a zero-touch-provisioned WP826 plays that
phone's messages, and `*98` from the same phone reaches another box with its
PIN.

### M3 · Docs and the rehearsal — docs done (v0.11.0); the rehearsal is the next voicemail anyone leaves

**On jepsen, 2026-09-25 22:10:** v0.11.0 installed by the documented
path, the new `extensions.conf` template copied (no local edits), the two
voicemail.conf lines added, rendered and reloaded. Both phones still name
`family` (hand-written, no PIN variable), so render wrote no box and
`check` says so; `102` now carries the VoiceMail steps and the theater's
endpoint carries `CMM_MAILBOX=family`. The ring-out path was driven
through ARI (the console's own 30-second originate timeout killed three
console attempts at exactly the moment the ring gave way to voicemail —
a real caller stays on the line) and CEL shows `Dial` → `VoiceMail`
`APP_START` → `ANSWER`; the announcement used as the "caller" was shorter
than the greeting plus the two-second minimum, so no message was kept.
A person leaving one is the rehearsal.

RUNBOOK "Voicemail" rewritten around the four facts; FIRST-BOOT's phones
step; `examples/handsets.example.toml` and the scenarios give every room a
box; `llms.txt` and the man page; s12 unblocked. Done when a reader adds a
handset from the runbook alone and its voicemail works without opening
`voicemail.conf`.

## Alternatives rejected

- **`doorman init` writes `voicemail.conf` once, whole.** It would own a file
  the operator also edits (email relay, `serveremail`, the hunt's boxes), and
  init runs once; render runs every time a phone is added. Generated
  fragments beside the hand-written file is the pattern that already works.
- **Mailbox id = handset id, no key.** Rules out two phones sharing a box,
  which the user's own example has (kitchen and theater → whole house).
- **A PIN prompt on the phone's own box.** Protects nothing a LAN handset
  cannot already do, and turns a one-press key into a two-step chore the
  kids will not use.
- **doorman answering the room call and deciding.** Room-to-room is dialplan
  and must keep working with the daemon down, like paging and outbound.

## Rollout order

M1 and M2 can ship in one release; M2's template value wants a phone on the
desk to verify P33, exactly as s10's values were. s12 follows.
