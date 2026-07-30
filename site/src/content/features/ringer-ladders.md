---
title: Ringer ladders
tagline: Ring the right phone first, and the whole house only if nobody picks up.
audience: Households where a call for one person wakes everybody.
order: 10
---

## The problem

A landline rings everywhere. That was fine when a house had one phone and one
number, and it is why most people quietly stopped answering the landline: every
call was for someone else, and every ring interrupted everyone.

Mobile phones solved it by giving each person their own number — and created a
different problem. Now a child needs a phone to be reachable at home, a
grandparent has to remember which of five numbers reaches which grandchild, and
the house itself has no way to be called.

## What a ladder does

A ladder rings in stages. A call for the kids rings **their room** for three
cycles. If nobody answers, it escalates to **the adults** for four more. If
nobody answers that either, it takes a message.

```toml
[[extensions]]
pin = "902118"
label = "Kids"
voicemail = "kids"

  [[extensions.steps]]
  handsets = ["kids-room"]
  rings = 3

  [[extensions.steps]]
  handsets = ["adults"]
  rings = 4
```

Nobody downstairs is interrupted by a call that was answered upstairs in six
seconds. And nothing is missed, because the escalation is automatic rather than
depending on somebody shouting.

## Why this is nicer than a mobile

The caller does not have to know who is home, which phone anyone is holding, or
whether a battery is flat. They dial one number and the house works out where
the call should go.

That is the part that is genuinely hard to buy. Carriers sell you *lines*;
ladders are about *routing*, and the routing rules are yours.

## Details worth knowing

**Stages are measured in ring cycles or in seconds.** `rings = 3` is roughly
eighteen seconds. Use `seconds` when the number matters — someone who needs
longer to reach a phone reads better as `seconds = 45` than as a ring count.

**Groups work anywhere a handset does.** `adults` above is a named set defined
once in `handsets.toml`, so changing who counts as an adult is one edit rather
than a search.

**The first stage is the interesting one.** Most households find that ringing
*one* likely phone first — the kitchen — and only then the whole house removes
most of the annoyance without missing anything.
