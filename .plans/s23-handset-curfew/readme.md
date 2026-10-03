# s23 · Curfew — a handset goes dark at bedtime

**Status:** M1–M3 built 2026-10-02, the evening the five new handsets
(103–107) were provisioned on jepsen and the first question after "they
ring" was "how do they stop". M4 (jepsen) next.

## What done looks like

At 22:45 on a school night Mary Kate's handset (106) hangs up whatever call it
is on, stops ringing for anything — the house ring, an extension, a ladder
stage, a page — and refuses to dial out, except 911. At 05:00 it is a phone
again. Friday and Saturday the hour is 23:30. Annabelle and Norah (104, 105)
have the same shape at 21:00. Nothing on the phone is touched: the curfew is
the house's rule, declared once, and `doorman check` says for each handset
whether it is asleep *right now*.

```toml
# policy.toml — the hours, as [[schedules]] already expresses them
[[schedules]]
id = "late-weeknights"
start = "22:45"
end = "05:00"                      # crosses midnight
days = ["SU", "MO", "TU", "WE", "TH"]   # the days the window STARTS on

[[schedules]]
id = "late-weekends"
start = "23:30"
end = "05:00"
days = ["FR", "SA"]

[[schedules]]
id = "early"
start = "21:00"
end = "05:00"

# handsets.toml — who the hours apply to
[[handsets]]
id = "mary-kate"
curfew = ["late-weeknights", "late-weekends"]   # dark while ANY is active
```

## Decisions and invariants

**The hours live in `[[schedules]]`; the handset names them.** One schedule
vocabulary for the whole file — `afterhours` on an extension and `curfew` on
a handset read the same blocks — and the per-day shape falls out of what
`days` already means. A curfew is a *list* because "22:45 Sun–Thu, 23:30
Fri–Sat" is two windows, and TASKS §3f already concluded that naming several
schedules composes better than teaching one window to span a weekend.

**A dangling id fails `doorman check`.** handsets.toml referencing a schedule
that policy.toml does not define is the same class as a policy extension
naming a handset that does not exist: cross-file, caught at load, named in
the message. The loader already walks both files together.

**Three effects, each in the place that already owns it.**

1. *No ring in* — doorman. The lobby already skips a do-not-disturb handset
   before every Originate (s12); a curfewed handset is skipped by the same
   gate for one more reason. Pages included: a sleeping room is not paged.
   The page-override handset (s12) does **not** pierce a curfew — DND is the
   room's own choice and may be overridden by a parent; a curfew *is* the
   parent's choice.
2. *No call out* — Asterisk. `render` writes a time condition
   (`GotoIfTime`) into the handset's outbound path, so the dialplan refuses
   with a short prompt. **911 is unconditional**: it lives in `[internal]`
   outside anything the curfew touches (invariant 11), so the carve-out is
   topological, not a special case. Changing the *hours* therefore needs a
   render + reload, same as changing a handset; `doorman check` says so.
3. *Hang up at the hour* — the daemon, once a minute. The plan first said
   "a deadline in the session's select", and the code map said why not: a
   room-to-room or outbound call never passes through a session, and the
   lobby only sees calls it placed. So the daemon, which owns the clock and
   an ARI client, keeps a `curfewKeeper`: every minute it asks each line's
   policy which handsets are asleep, and a handset that *just* fell asleep
   has its channels dropped — whatever placed the call. Nothing is
   remembered across restarts: a handset found already asleep at startup
   keeps its call, because the hour that would have dropped it has passed.
   One ARI call was added for it (`Channels`, GET /channels).

Also learned from the map: pages and ring-all are dialplan-only, so "no
ring in" is both the lobby gate *and* a time condition in the generated
`100` and `500`; and Asterisk tests weekday and clock separately, so a
window crossing midnight is rendered as two `IFTIME` specs — its evening on
the start days, its morning on the days after — exactly as
`Afterhours.Active` reads it.

**Rejected: a `curfew` key on `[[people]]` or `[house]`.** The thing that
sleeps is the handset in the room, whatever rings it. Putting the rule on
the caller side would need it repeated for every path to that phone.

**Rejected: doing it on the phone (Grandstream's own DND schedule).** It
would not stop doorman ringing it, would not stop the house page, and would
be seven phones' settings instead of one file.

**Rejected: an Asterisk global like DND's.** DND is set *by the phone* at
runtime, so Asterisk owning the state is right. A curfew is configuration;
the state is the clock, and both doorman and the dialplan can read a clock.

**Interaction with s05 (per-person routing), decided 2026-10-02:** the
schedule wins. A caller's ladder is filtered to the handsets not asleep.

## Milestones

- **M1** policy: `curfew` on Handset (list of schedule ids), cross-file
  validation, `check` output with ACTIVE NOW; schema + docs + man page.
- **M2** lobby: skip asleep handsets in every ring and page; deadline on a
  bridged call. Tests through the fake ARI harness with short windows.
- **M3** render: `GotoIfTime` on the outbound path; 911 untouched, asserted
  by test; golden files.
- **M4** jepsen: the three schedules above, the four handsets, render,
  reload, and a 21:00 that actually happens.
