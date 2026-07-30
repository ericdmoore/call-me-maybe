---
title: Hold music
tagline: Because a silent hold is indistinguishable from a dropped call.
audience: Anyone who has said "hello? hello?" into a phone that was working fine.
order: 60
---

## The problem

Silence on a phone line means one of two things — you are on hold, or the call
is dead — and there is no way to tell which. People hang up on working calls
several times a day because of it.

## What this does

Music, or any audio you like, plays while a call is parked or held. The caller
knows the line is alive and stays on it.

## Why it is worth a paragraph at all

Because it is the smallest possible example of what this project is for. Nobody
would buy a "hold music" product. It is a two-line dialplan change that fixes a
real irritation, and it exists here because the platform is yours and the
mechanism is free.

That is the whole argument in miniature: the interesting features of a phone
system are not individually big enough to sell, which is exactly why they were
never available to households.

## Details worth knowing

**It is a folder of audio.** Point `musiconhold.conf` at a directory and that is
the hold experience. Seasonal, silly, or actually pleasant — the mechanism does
not care.

**You cannot ship music you do not own.** Commission it, or use a CC0 source. A
hold bed is the one place people reach for a favourite track and create a
licensing problem for themselves.
