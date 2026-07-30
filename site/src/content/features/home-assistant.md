---
title: Home Assistant
tagline: Dial 555 and ask the house a question — with no microphone listening the rest of the time.
code: "555"
audience: People who want a voice assistant and do not want an always-on microphone.
order: 90
---

## The problem

Voice assistants are genuinely useful and come with a microphone that is always
on, an account, a cloud, and a company whose interests are not yours. For many
households that trade is not worth it, so they go without the useful part.

## What this does

Dial **555** and you are talking to Home Assistant's Assist — your own
automation, running on your own hardware, answering questions about your own
house.

## Why a phone is the right shape for this

**It is a pull interface.** Nothing is listening until you dial. That single
property is the entire difference between this and a smart speaker, and it is
not a setting anyone can change on you.

It also works from every room already, needs no new device, and is usable by
someone who would never speak to a speaker in the corner.

## Details worth knowing

**It is optional and isolated.** The hook lives on its own extension. If Home
Assistant is down, the phone is exactly as good as it was before — the lobby,
the ladders, and the ringing are untouched.

**Your automation, your rules.** Assist runs locally if you configure it locally.
Nothing about this design requires a cloud, and the phone half certainly does
not.
