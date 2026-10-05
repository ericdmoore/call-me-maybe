---
title: A phone with a bedtime
tagline: Let the afternoon conversation happen. Give ordinary calls a stopping time.
audience: Families who want a child's home handset to follow the household bedtime.
order: 15
---

Your child is halfway through a story when dinner becomes bedtime. A handset
curfew gives the phone a schedule of its own, without relying on someone to
remember to unplug it.

During a configured curfew, that handset skips ordinary incoming calls and
cannot place ordinary outgoing calls. It is left out of house ringing and
paging too. The running daemon checks once a minute and ends ordinary calls
when the handset enters its curfew, so this is not an exact-to-the-second cutoff.
A daemon started after the window begins leaves an existing call alone.

**Emergency dialing and explicitly scheduled reminder calls remain exceptions.**
The local reminder menus and calls (`*80`, `*81`, `*82`) still work. Configure
your provider's emergency service and address; this is a supplementary phone,
not a household's only route to emergency help.

## Quiet hours or bedtime?

[Quiet hours](/features/quiet-hours/) control what happens when a caller reaches
an extension: perhaps an adult answers instead, or the caller leaves a message.
A handset curfew controls the phone itself, including ordinary outgoing calls.
They solve different problems and can be used together.

## What setup involves

An adult defines named schedules in the policy and assigns them to handsets.
Changes to curfews require rendering the configuration and reloading Asterisk;
they are not just a live policy edit. The
[runbook's Bedtime section](https://github.com/ericdmoore/call-me-maybe/blob/main/docs/RUNBOOK.md#bedtime-curfew)
explains the steps and exceptions.

A curfew is a time boundary, not an outgoing contact whitelist. Outside it,
the supplied dialplan permits ordinary US/Canada-format phone numbers.
