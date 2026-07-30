---
title: Voicemail
tagline: Per-mailbox, with the message delivered to email — and lamps that follow whichever box you point a phone at.
code: "*97"
audience: Households where "did you get my message?" is a recurring conversation.
order: 50
---

## The problem

Carrier voicemail is a walled garden: dial in, listen linearly, press 7 to
delete, and hope you remember what was said. It cannot be searched, forwarded,
or read in a meeting. And on a shared house line, everyone's messages are in one
pile.

## What this does

Separate mailboxes — `kids`, `adults`, `family` — each with its own greeting and
its own message-waiting lamp. A ladder that goes unanswered lands in the mailbox
that belongs to whoever it was for.

Messages are **delivered to email**, with the audio attached. That means they
are searchable, forwardable, and readable on a train.

## Why per-mailbox matters more than it sounds

The lamp is the useful part. A handset with a lit MWI is telling one specific
person that something is waiting for *them*. On a shared mailbox the lamp means
"somebody has a message", which is the same as no information at all.

`mailbox = "adults"` on a handset decides which lamp it follows, so a phone in a
shared room can still point at a specific box.

## Details worth knowing

**Dial `*97` from any handset** to reach the mailbox that handset is pointed at
— no PIN dance to get to your own messages from your own phone.

**Change the PINs.** The shipped examples use an obvious placeholder, and
`scripts/smoke.sh` fails if they are still in place, because a comment saying
"change this" is not a check.

**The mailbox is the fallback for everything.** House ring, ladder, quiet hours —
all of them end in a mailbox if one is configured, and in a polite dismissal if
not. Which of those you want is a real choice, not a default.
