# s11 · The hunt — the verification ladder, played by children

**Status:** planned (2026-09-21, late). Drafted the night the first customer's
phone rang in both directions. The idea arrived as "a scavenger hunt that hits
every checklist feature, ending with dialling 500 to tell the house you won,"
and the prize was settled before the design was: extra rice at dinner.

## What done looks like

A family that installed the phone system on Saturday morning plays a game on
Saturday afternoon and, without reading a runbook, has answered an inbound
call, called room to room, retrieved voicemail, joined the party line, called
a grandparent, and paged the house. Every rung of `RUNBOOK.md` §3 has been
exercised by a child holding a cordless handset, and the operator learned
which rung was broken from the child rather than from a log.

Nothing in doorman knows the game exists. The phone system contributes one
optional dialplan line and a handful of mailboxes; the game lives in envelopes
around the house and in a parent's recorded voice. The shipped kit is a
document and a set of printable cards with generic clues, no audio, CC BY-SA,
archetypes only. The finale card says "extra rice."

## Starting point

- **Every checkpoint feature already exists.** `500` pages every handset with
  `page = true`, auto-answered on speaker; `*97` is `VoiceMailMain(@household)`
  and asks for a mailbox and PIN; `600` is the `family` conference bridge;
  handsets dial each other by `number`; a known caller rings the house with
  their name on the screen; a handset dials out with the line's caller ID.
  Verified on jepsen 2026-09-21, all of it.
- **Mailboxes are cheap and have greetings.** `voicemail.conf` holds any
  number of `[household]` mailboxes; a mailbox owner records its greeting by
  logging in through `*97` and pressing `0`. A greeting is a WAV in a parent's
  own voice, on the parent's own box — not a pack, not TTS.
- **There is no way for a handset to reach an arbitrary mailbox's greeting.**
  `[voicemail-drop]` runs `VoiceMail(${MAILBOX}@household,u)` only when doorman
  releases an unanswered caller into it. That is the one missing line.
- **The story engine (`internal/story`) is a graph of pre-rendered clips with
  single-digit choices, stateless by design,** reached as a lobby extension.
  It could host a hunt, and the first draft of this idea put it there. It is
  the wrong tool for this one — see the rejected alternative — and stays the
  right tool for bedtime stories.
- **Feature codes already taken:** `*4` (outbound console), `*97`, `500`,
  `555` (HA Assist, optional), `600`, `9196` (echo test), `101`–`1NN`
  (handsets), `_911`.

## Decisions and invariants

**The answer is what you dial.** A clue asks a question whose answer is a
number — count the chairs, the stairs, the letters in the dog's name. The
player dials `*6` followed by the answer, and the dialplan plays the greeting
of the mailbox with that number:

```
; ── The hunt (optional) ──────────────────────────────────────
; *6 followed by digits plays that mailbox's greeting and hangs up. A parent
; records each clue as a greeting (*97, the mailbox, its PIN, then 0); a child
; dials *6 and an answer. A wrong answer is a mailbox that does not exist.
; See docs/HUNT.md. Leave commented unless you are playing.
;exten => _*6X.,1,Answer()
; same => n,VoiceMail(${EXTEN:2}@household,u)
; same => n,Hangup()
```

Right answer: the next clue, in Dad's voice. Wrong answer: Asterisk says
there is no such mailbox. The gating costs nothing and cannot be wrong, because
it is the existence of a mailbox. Two-digit answers are `*6` and two digits;
the mailbox is named after the answer, so the dialplan never changes.

**Zero mechanism in doorman.** The daemon never learns a hunt is running,
never records, never plays, never stores progress. This is the project's
"mechanisms are free, content is optional" line taken to its limit — the
mechanism is three commented lines of dialplan, and the content is paper. If
a future hunt wants doorman involved, it starts a new stream with a reason.

**State lives in the house, not the software.** The story engine keeps no
state so its graphs stay checkable; a hunt needs state — where the player is
in the sequence — and the envelopes hold it. A child who finds envelope four
first has skipped ahead, which is a feature of scavenger hunts, and none of
it touches a file on the box.

**Recorded voices, never TTS.** Invariant 7 says prompts are pre-rendered,
and a parent's greeting is that. But the reason is better than compliance:
the clue that sends a child to the theater should sound like the person who
hid it there. The shipped kit therefore contains **no audio at all** — the
pack rule against living people is not even in play, because nothing is
distributed; every family records its own.

**Children never hold a credential.** Invariant 5: extension PINs are
credentials and stay exact-match. A hunt must never use a lobby PIN, a
mailbox PIN, or an admin password as an answer or a reward. The hunt
mailboxes get random PINs that only the parents know, for recording; a
player only ever dials `*6` and a number. The kit's cards say this in one
line so nobody improvises a "secret code" that is the house's real one.

**No feature-code collisions.** `*6X.` is free today. `docs/HUNT.md` lists
every code the dialplan reserves, and adding the hunt line to the reserved
list is part of M1, so the next feature does not land on `*6`.

## The ladder, as a game

| Envelope | Says | Rung it proves |
|---|---|---|
| — | Dad's cell rings the house: "answer it in the kitchen" | inbound, allow-list, caller-ID name on the screen |
| 1 (kitchen table) | "Count the chairs where the family eats. Dial `*6` and the number." | the hunt line; the first keypad answer |
| 2 (greeting of that mailbox) | "Take this phone to the theater. Call `101` and ask for the password." | handset ↔ handset; a human in the loop |
| 3 (under a theater seat) | "Everyone dial `600` and say the password together." | the party line |
| 4 (greeting) | "Call Grandma. She knows how many stairs there are." | outbound; the phonebook (s10 M5) |
| 5 (top of the stairs) | "Dial `*6` and the number of stairs." | a second keypad answer |
| 6 (greeting) | "Dial `500` and tell the whole house you won." | paging, auto-answer |
| 7 (dinner) | extra rice | the prize |

The player carries the cordless handset the whole way; that is why the Wi-Fi
handhelds were chosen, and the hunt is the first thing that uses it.

## What is deliberately out of scope

- **A story-engine hunt.** Possible — the graph gates on digits already — but
  it needs a pack build for the audio (TTS or WAVs, not a greeting recorded
  by phone), multi-digit answers need the `#`-terminated collection primitive,
  and the engine cannot know which phone is calling. Everything a hunt wants,
  voicemail gives for free. Bedtime stories keep the engine.
- **Enforcing which phone answers.** The clue sends the player to a room and
  the answer proves they went. Enforcement would need the sandbox to know the
  caller, which the story spec forbids for good reasons.
- **Scoring, timing, progress.** That is state, and the house has it.
- **doorman announcing the winner.** `500` is a live page: the winner
  announces the winner. A recorded fanfare could be a greeting behind `*60`
  if a family wants one; the kit does not ship one.

## Milestones and acceptance criteria

### M1 · The line

The commented `*6X.` block in `asterisk/extensions.conf`; a commented hunt
section in `voicemail.conf.example` (mailboxes named for answers, random PINs,
one comment saying children never get them); `*6` added to the reserved
feature-code list in `RUNBOOK.md` → "Handset features".

Done when, on jepsen with the block uncommented and mailbox `6` given a
greeting through `*97`, dialling `*66` from a handset plays it and hangs up,
`*67` says there is no such mailbox, `*97` still reaches the family mailbox,
`500`/`600`/`101` are untouched, and `smoke.sh` is unchanged and green.

### M2 · The kit

`docs/HUNT.md`: how to set it up in fifteen minutes (uncomment, add mailboxes,
record greetings, hide envelopes), the ladder table above with blanks for the
house's own answers, the credential rule, and how the hunt doubles as the
first-day verification. A printable card set — generic clues, archetypes,
"extra rice" on the last card — under `docs/hunt/` as CC BY-SA content,
listed in `LICENSES.md`. A site page that shows a Saturday rather than a
feature list.

Done when a reader who has never seen the repository can run the hunt from
`HUNT.md` alone, and `LICENSES.md` names the cards.

### M3 · The rehearsal

The first customer's family plays it. Every stumble is a finding: a card
that was unclear, a rung that failed, a feature that needed a parent to
explain it. Findings go into `HUNT.md` or, when the phone was at fault, into
the ledger like any other first-customer finding.

Done when the house has been paged by a child who won, and the rice was
served.

## Alternatives rejected

- **Clues by TTS.** Works, and is worse: a synthetic voice hiding envelopes in
  a family's house is exactly the uncanny thing this project avoids elsewhere.
  A parent with a handset and `*97` is the whole recording studio.
- **Answers as lobby PINs.** The lobby already gates on a number; using it
  would teach children that a PIN is a game token. Invariant 5 exists to stop
  that.
- **doorman as game master.** A `[[hunts]]` section, progress per player, a
  webhook when someone wins. Every line of it is state and a database, for a
  thing that paper does better.
- **A dedicated hunt extension per clue** (`*61`, `*62`, …). Hard-codes the
  sequence into the dialplan and gives away the order. `*6` + the answer keeps
  the dialplan generic and the sequence on paper.

## Rollout order

M1 is a few lines and can ride in any release after v0.5.3. M2 needs a
Saturday of writing and a card design. M3 needs a Saturday, two handsets, and
rice. The s10 phonebook makes "call Grandma" a name on the screen rather than
a number on a card, but the card can carry the number until then.
