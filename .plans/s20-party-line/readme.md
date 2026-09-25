# s20 · The party line — a room is a PIN with a suffix

**Status:** planned (2026-09-25). Drafted from a conversation that started at
"there used to be 1-800 conference lines" and ended somewhere better: a
child telling her friends the house phone can hold twenty of them at once,
call at 4:30, we will plan the sleepover.

## What done looks like

Grace picks up the handset in her room and dials `600 * 107`. Room 107 is
new, so it becomes hers, and she is in it. She tells her friends "call my
house and type six seven six seven six seven, star, one oh seven". At 4:30
they do, from their parents' phones; the lobby answers them like any
stranger, they type it, and instead of ringing the kitchen they land in the
room, each saying their name on the way in. Anyone who is early hears
music until Grace is there. When the last house handset leaves, the room
ends for everyone. Two weeks without a call and the room forgets itself;
`676767 * 107` stops working until Grace makes it again. A parent sees who
called and when in `doorman calls`, and nobody can listen in. Twenty
callers is nothing for the box; the line's simultaneous-call limit is the
real ceiling, and it is two until somebody pays for more (see the open
questions).

## Starting point

- `600` is a ConfBridge on Asterisk's default profile; the hunt uses it as
  the party line. No `confbridge.conf` ships.
- The lobby already turns a PIN into a destination, and a destination can
  be a pseudo-handset that is a dialplan context. An outside caller with a
  PIN targeting `Local/600@internal` is a guest today, by hand.
- Extension PINs are credentials (invariant 5) and stay exact-match; the
  lobby collects a fixed number of digits (`EXTENSION_LENGTH`).
- `docs/product-extensions.md` sketches an SMB version of this — named
  rooms, moderators, recording, stand-ups. This stream is not that; if
  that ever ships it sits on top of this.

## Decisions and invariants

**A room belongs to a PIN, and by default to the handset's PIN.** Rooms are
made from a house handset by dialling `600 * <room>`. The room inherits
the PIN of the extension that is that handset's own — the extension whose
ladder is exactly this one phone. A handset with no such extension, or
more than one, is asked for a PIN once. Grace can instead give the room a
PIN of its own at creation, after which guests type that and her extension
PIN never leaves the house. Either way the guest form is the same shape:
`<pin> * <room>`.

**The room number is the suffix; the PIN is the key.** The lobby's digit
collection learns one thing: after a valid PIN, a `*` continues into a
room number, terminated by `#` or silence. PIN alone behaves exactly as
today. A PIN plus a room nobody owns is a wrong PIN, counted by the
limiter like any other, because a room suffix must not be a cheaper way
to probe the PIN space.

**Hosts are house handsets; guests are the lobby.** Anyone dialling from
inside is a marked user; everyone from the lobby is not. Guests wait to
music until a host is present and the room ends when the last host leaves,
so there is never an unattended bridge. Joins and leaves are announced by
a name each guest says on the way in. No listen-in, no recording, no
moderator menu beyond the host's own mute: those are the SMB stream's.

**Rooms are a file, not a database.** `rooms.toml`, written by the daemon
the way `rotate` writes `policy.toml`: room, owner PIN reference (by
extension label, never the PIN), created, last used. `doorman check`
lists them; a parent can edit or delete one; a stale room is swept on the
same keep-alive policy the file declares. The daemon reads it like policy,
hot, and keeps the last good copy on a bad edit.

**Keep-alive.** A room's last-used moves on every join. `room_ttl`
(default fourteen days) without a join removes it. `600 * 107 * #` from a
host drops it now. Nothing is remembered about who was in it beyond the
call log everyone already has.

**Capacity is the line's, and it is printed.** `doorman check` shows the
trunk's concurrent-call setting when the provider client can read it, and
the plan's first task is to find VoIP.ms's number for this line. `600`
itself gets `max_members` from the same figure so the twenty-first caller
hears "the room is full" instead of silence.

## What is deliberately out of scope

- Moderators, recording, stand-ups, named business rooms — the product
  extension, later, on top of this.
- Scheduling. "Call at 4:30" is a text between friends.
- A parent listening. The call log says who; the room is theirs.
- Rooms that outlive their PIN: rotate the owning PIN and the room goes
  with it.

## Open questions

1. ~~VoIP.ms's simultaneous inbound call limit on this line~~ — answered
   2026-09-25: the API does not expose it, and VoIP.ms's published default
   is **two simultaneous calls per DID** on every plan. So today the party
   line holds two friends, not twenty. VoIP.ms sells more only as a
   Virtual PRI — $23 per channel per month, ordered through sales, channels
   shared across the account's DIDs, with a "burstable" option from about
   a dollar per channel per day — which prices a twenty-kid room at $460 a
   month standing, or roughly a dollar per friend per party-day if the
   burst option works the way it reads. The better answer is the one s01
   already built for: **a second DID at a provider that does not meter
   channels** (Telnyx or Flowroute, per-minute, concurrency limits in the
   dozens by default), on its own trunk in `trunks.toml`, as the party
   line's own number — a dollar a month for the number and about a dollar
   for a ten-friend half hour. The house number stays what it is. The `600` bridge's
   `max_members` is set from whatever the line actually allows, so the
   third caller hears "the room is full" rather than a busy signal.
2. Which prompt says "enter the room"? The pack contract is six names;
   the room prompts are the lobby's existing ones plus SayDigits until a
   deliberate pack bump adds "say your name" and "the room is full".
3. Whether a guest may create a room from the lobby with a PIN. Lean: no.
   Rooms are made from inside the house.

## Milestones and acceptance criteria

### M1 · The bridge

`confbridge.conf` shipped with the family bridge, a host profile
(marked), a guest profile (`wait_marked`, `end_marked`, announce join and
leave with a recorded name, music when empty), `max_members` from config;
`600 * <room>` from a handset joins room `<room>` as host. Done when two
handsets and one outside caller through a hand-written guest PIN share a
room, the guest waited to music, and the room ended when the handsets
left.

### M2 · Rooms

`rooms.toml`; creation from a handset with the inherited or a chosen PIN;
the lobby's `*` continuation; the limiter counting a bad room like a bad
PIN; keep-alive and `#` to drop; `doorman check` listing rooms. Done when
Grace makes a room from her handset, a friend joins with `pin * room` from
the lobby, a wrong room costs a limiter failure, and a room untouched for
the TTL is gone.

### M3 · The words and the card

The registry (s14) knows rooms; `411` and the phone book card say "the
party line is 600 star a room"; `docs/RUNBOOK.md` and `HUNT.md` gain the
paragraph. Done when a child can explain it to a friend from the card.

## Alternatives rejected

- **Daily derived codes.** Elegant and stateless, and wrong for this: a
  room a child made should keep working until it is forgotten, not change
  at midnight.
- **The SMB feature set first.** Moderators and recording are for meetings;
  a sleepover needs a room that waits for its host and ends when she goes.
- **Rooms in memory only.** Lost on restart, invisible to `check`, and
  nobody could edit one. A file is state a parent can read.

## Rollout order

M1 is Asterisk config and a Saturday's test. M2 is the real code: the
lobby's continuation and the rooms file. M3 rides with s14.
