---
title: The phone calls you back
tagline: Dial *80, say a time, leave a message. At 7:30 the phone rings and plays it back to you.
code: "*80"
audience: Anyone who has said "remind me at seven" to a room with nobody in it.
order: 55
---

## The problem

Reminders live on the device you are trying to put down, or on a speaker that
is listening the rest of the time. A house phone is in every room, already
powered, and knows which room you are in because that is the handset you
picked up.

## What it does

Three codes, dialled from any handset:

- **\*80** — call me back **today** at a time. Choose AM or PM, then the time:
  `730#` for 7:30. A time that has passed is refused.
- **\*81** — call me back **in** a while. One digit for hours, then minutes,
  then `#`: `1`, `99#` is two hours and thirty-nine minutes.
- **\*82** — call me back **tomorrow** at a time, once. It does not repeat.

After the time, press `1` to record up to a minute of your own voice, or wait
and the phone uses its standard line: *"This is the call you scheduled by
dialing star eight zero."* The phone reads back the date and time, and you hang
up.

At the hour, that handset and only that handset rings. It rings for thirty
seconds, and if nobody answers it tries twice more, three minutes apart.
Answer, and it says what you told it to say.

## Details worth knowing

**It rings through quiet.** A scheduled call ignores [quiet hours](/features/quiet-hours/),
a room's do-not-disturb and the handset's [bedtime](/features/bedtime/). The
phone's own mute switch still wins, so do not rely on it as an alarm that can
override a silenced phone.

**It survives a reboot.** Asterisk's own call-file queue owns the scheduling
and the retries, so a pending call outlives a restart of the daemon or the
whole box. A call that falls due during an outage rings late rather than never.

**It is private to the handset.** Up to ten calls can be pending per phone.
Dial the code again and it reads out what is pending; `1` cancels one. Another
handset cannot see, hear or cancel them. Recordings live in Asterisk's own
spool, readable by nobody else on the box, and are deleted the moment the
call is answered or cancelled.

**Nothing leaves the house.** No carrier call, no paid service, no cloud voice.
The standard message is a pre-recorded file like every other prompt here.
