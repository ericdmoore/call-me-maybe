---
title: Paging and intercom
tagline: Dial 500 and every handset opens at once. Dinner is ready without shouting up the stairs.
code: "500"
audience: Anyone who currently communicates between floors by yelling.
order: 30
---

## The problem

Every house has a shouting protocol. It works badly, it does not reach the
garage, and it is unusable when somebody is asleep or on a call.

The modern replacement is a smart speaker in every room, which means a
subscription, a microphone that is always listening, an account for each family
member, and a company that can change the terms.

## What paging does

Dial **500**. Every handset marked for paging answers automatically and opens
its speaker. Say the thing. Hang up.

No wake word. No account. No microphone open when you did not dial in. The
phones are doing exactly what an office intercom did in 1975, which is the point
— it is a solved problem, and the solution predates the surveillance.

## Why the phone is the right device for this

It is already in every room, already powered, already yours, and already knows
who is calling. A page from the kitchen handset is announced as the kitchen.

And it is a *pull* interface. Nothing is listening until somebody dials, which
is a meaningfully different privacy story from a device whose business model
requires a microphone in your kitchen.

## Details worth knowing

**Page-all is per handset.** `page = true` in `handsets.toml` decides who is in
the group, so the guest room does not have to blare at midnight.

**Intercom is the same mechanism, aimed.** Dial one handset's internal number
and you have a two-way conversation between rooms, which is the version children
enjoy far more than the adults expect.

**Auto-answer is a header, not a hack.** Asterisk sends the phone an Alert-Info
that tells it to pick up. Grandstream and Yealink handsets honour it out of the
box.
