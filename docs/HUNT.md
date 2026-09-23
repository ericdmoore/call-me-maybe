# The hunt

A scavenger hunt that walks every feature of the house phone, played by a
child holding a cordless handset, and won by paging the whole house. Setting
it up takes a parent fifteen minutes and a phone. The prize was settled before
the design was: extra rice at dinner.

It doubles as the first-day verification. `RUNBOOK.md` §3 and `FIRST-BOOT.md`
§8 walk the same ladder with a terminal; this walks it with envelopes, and the
operator learns the same things while the family learns the phone.

## How it works

**The answer is what you dial.** A clue asks a question whose answer is a
number — count the chairs, the stairs, the letters in the dog's name. The
player dials `*6` followed by the answer. If a mailbox with that number has a
greeting, the phone plays it: the next clue, in a parent's own voice. If not,
the phone says that is not a valid extension, and the player counts again.

Nothing in doorman knows a hunt is running. The mechanism is four commented
lines of dialplan that play a greeting file and hang up; the content is paper
and a parent's voice. Progress lives in the envelopes, which is where a
scavenger hunt keeps it anyway — a child who finds envelope four first has
skipped ahead, and that is a feature.

## Set it up in fifteen minutes

1. **Turn the line on.** In `/etc/asterisk/extensions.conf`, find "The hunt"
   under `[features-internal]` and uncomment the four `exten`/`same` lines.
   Then `sudo asterisk -rx 'dialplan reload'`.
2. **Add a mailbox per recorded clue**, named for its answer, in
   `/etc/asterisk/voicemail.conf` under `[household]`. The example file has a
   commented block to copy:

   ```
   6  => 7391,Hunt: chairs
   14 => 2685,Hunt: stairs
   ```

   Pick your own random PINs. They are for recording and they are yours alone
   (see "The one rule" below). No email address: the clue is the greeting and
   nobody leaves messages here. `sudo asterisk -rx 'voicemail reload'`.
3. **Record each clue as that mailbox's greeting.** From any handset: dial
   `*97`, enter the mailbox number, enter its PIN, press `0` for mailbox
   options, then `1` to record the unavailable greeting. Say the clue, press
   `#`, then `1` to accept. Hang up. Dial `*6` and the number to hear it back.
4. **Hide the envelopes.** Print or copy the cards in `docs/hunt/cards.md`,
   fill in your house's answers, and put each one where its clue sends the
   player. The blanks are the point: the kit has no idea how many chairs you
   own.
5. **Start the hunt** by calling the house from a mobile on the allow-list.
   The player answers in the kitchen, where envelope 1 is waiting.

To play again with new clues, re-record the greetings. To retire the hunt,
comment the dialplan lines back out; the mailboxes can stay.

## The ladder

Fill in the right-hand column for your house. Every row is a feature, and by
dinner every one has been exercised by somebody who never read a runbook.

| Envelope | Says | Proves | Your answer |
|---|---|---|---|
| — | A parent's mobile rings the house: "answer it in the kitchen" | inbound; the allow-list; a name on the screen | |
| 1 (kitchen table) | "Count the chairs where the family eats. Dial `*6` and the number." | the hunt line; the first keypad answer | `*6` + ____ |
| 2 (that greeting) | "Take this phone to the theater. Call `101` and ask for the password." | handset to handset; a human in the loop | password: ____ |
| 3 (under a theater seat) | "Everyone dial `600` and say the password together." | the party line | |
| 4 (that greeting) | "Call Grandma. She knows how many stairs there are." | outbound; the phonebook | who to call: ____ |
| 5 (top of the stairs) | "Dial `*6` and the number of stairs." | a second keypad answer | `*6` + ____ |
| 6 (that greeting) | "Dial `500` and tell the whole house you won." | paging; auto-answer | |
| 7 (dinner) | extra rice | the prize | |

Swap rooms and people freely. The shape that matters is: a recorded clue, a
place, a person, the party line, an outside call, and the page at the end.
Envelope 2's password is whatever the person in the theater decides; envelope
3 is what makes them say it together.

## The one rule

**Children never hold a credential.** A hunt must never use a lobby extension
PIN, a mailbox PIN, or an admin password as an answer or as a reward. The
hunt mailboxes get random PINs that only the parents know, for recording; a
player only ever dials `*6` and a number. Extensions are credentials and the
lobby treats them as such — a hunt that taught a child that a PIN is a game
token would undo that. The cards say this in one line so nobody improvises a
"secret code" that is the house's real one.

## The hunt as verification

| Envelope | RUNBOOK §3 rung |
|---|---|
| the opening call | rung 2 (trunk registered), rung 5 (doorman answering), rung 7 (an actual call) |
| 1 and 5 | rung 3 (handsets registered) and the internal dialplan |
| 2 | handset-to-handset audio, both directions |
| 3 | the `family` conference bridge |
| 4 | outbound through the trunk, with the line's caller ID |
| 6 | paging and auto-answer on every handset with `page = true` |

A rung that fails during the hunt is a finding, exactly as it would be from a
terminal: the phone was at fault, and it goes in the ledger. A card that was
unclear is a finding too, and it goes here.

## Reserved feature codes

The dialplan reserves these whether or not a house uses them. The next
feature must not land on one.

| Code | What |
|---|---|
| `100` | ring every handset |
| `101`–`1NN` | handsets, by `number` |
| `500` | page every handset |
| `555` | Home Assistant Assist (optional) |
| `600` | the family conference bridge |
| `700`–`720` | call parking |
| `*4` | the outbound console |
| `*6` + digits | **the hunt** |
| `*97` | voicemail |
| `9196`, `9197` | echo test; the time |
| `911` | emergency, always, never through anything else |

## What the kit does not contain

No audio. Every family records its own clues, in its own voices, on its own
box — a synthetic voice hiding envelopes in a family's house is exactly the
uncanny thing this project avoids everywhere else, and the clue that sends a
child to the theater should sound like the person who hid it there. No
scoring, no timer, no progress: the house has that. No winner's fanfare:
`500` is a live page, and the winner announces the winner.

The cards under `docs/hunt/` are CC BY-SA 4.0, like the bundled prompt pack.
They name no people and no characters — archetypes only, with blanks for your
own.
