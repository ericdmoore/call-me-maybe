---
title: Quiet hours
tagline: Route an extension to voicemail or an adult during its quiet hours.
audience: Anyone whose phone is in a room where someone sleeps.
order: 20
---

## The problem

A phone in a child's room is useful right up until 11pm, when it is a machine
for waking a household on behalf of strangers.

Turning a ringer off is easy to forget. A configured schedule lets you decide
where calls for an extension should go while someone is asleep.

## What quiet hours do

A named window, defined once:

```toml
[[schedules]]
id = "school-night"
start = "20:30"
end = "07:00"
days = ["SU", "MO", "TU", "WE", "TH"]
```

An extension can reference it. During the window, calls routed to that
extension go to its configured voicemail unless an alternate ring group is set.
Configuration validation requires a mailbox or an afterhours destination.
This does not silence every route to the handset or restrict outgoing calls.
For that, use a [handset bedtime](/features/bedtime/).

**Or the call goes somewhere that is awake:**

```toml
afterhours = "school-night"
afterhours_ring = ["adults"]
```

Now a 2am call for the kids' extension rings the adults instead. If nobody answers
*that*, it takes a message if a mailbox is configured, so the redirect narrows what happens during
the window rather than removing the safety net.

## What that one field expresses

- **Homework hours.** Four to six, ring the adults rather than the study.
- **A rotating night shift.** One parent Monday to Wednesday, the other the
  rest, without the caller needing to know whose night it is.
- **A babysitter.** The evening window rings whoever is actually in the house.

## Details worth knowing

**A window that crosses midnight belongs to the day it starts.** `20:30`–`07:00`
on Monday covers Monday evening through Tuesday morning. That is why a
school-night schedule lists Sunday through Thursday: Friday and Saturday
evenings stay open, which is what you meant.

**`enabled = false` is the holiday switch.** One edit turns bedtime off for
spring break without hunting through every extension that references it — and turns
it back on without reconstructing the times from memory.

**Schedules are shared.** Define `school-night` once and every extension that should
respect it points at the same window, so there is one place to change when term
ends.
