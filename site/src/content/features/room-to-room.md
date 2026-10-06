---
title: Room to room
tagline: Dial 101 for the kitchen. The call never leaves the house and never touches the carrier.
code: "101"
audience: Houses where a conversation currently involves stairs.
order: 35
---

## The problem

Somebody upstairs needs somebody downstairs. The choices are shouting, walking,
or a group chat on the phones the house was trying to use less. None of them
reaches the garage.

## What room to room does

Every handset has a short number, 101 for the kitchen, 102 for the living
room, and so on through the rooms, set in `handsets.toml`. Pick up any
handset, dial the room, and that phone rings like an ordinary call. Dial 100
and every handset rings.

The call is handled entirely inside the house. It does not touch the provider,
costs nothing, and still works when the internet is down. The doorman is not
involved either: this is Asterisk doing what a small office switchboard has
done for fifty years.

## Why it is different from paging

[Paging](/features/paging/) makes every handset answer itself on speaker, for
announcements. A room call rings one phone and waits for a hand to pick it up,
for conversations. Children discover the second one within a day and use it
far more than the adults expect.

## Details worth knowing

**The lamp tells you before you dial.** A handset's keys can watch the other
rooms. The key lights when that room is already on a call, and one touch dials
it. See [busy lamps](/features/busy-lamps/).

**A quiet room says so.** A room that has dialled *78 for half an hour of
peace answers a room call with how long is left and offers its mailbox. A
call that rings out lands in that room's [voicemail](/features/voicemail/) too.

**Every room number is a transfer target.** An outside call for somebody
upstairs is transferred to their room, or [parked](/features/call-parking/)
on 700 and picked up from any handset.

**Text works too.** A message typed on a handset to a room number reaches that
phone, and to 100 reaches every phone, without a carrier in the middle.
