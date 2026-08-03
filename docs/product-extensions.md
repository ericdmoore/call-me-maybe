# Product extension ideas

Ideas for stretching Call Me Maybe from a household lobby into something a
home-office or small team would keep, without becoming FreePBX or a hosted
carrier. This is a **menu of directions**, not a committed backlog —
acceptance-criteria work stays in `docs/TASKS.md` and GitHub issues.

Ground rules that still apply:

- **Mechanisms free, content paid** — see `docs/PACKS.md`.
- **Prompts are pre-rendered WAVs** on any path that can make the house
  unreachable. Live STT/LLM/TTS is optional and fail-closed — see #7.
- **Allow-list is convenience, not auth** — see #12.
- **Outbound costs money** — anything that originates needs the rate caps in
  #15.
- Honest market read: `docs/SUSTAINABILITY.md`. This list should not assume a
  multi-tenant SaaS.

Already in the tree today (seed, not productised): family ConfBridge `600`,
park `700`/`701–720`, page-all `500`, shallow outbound NANP, voicemail `*97`,
lobby PINs that can target a conference pseudo-handset.

---

## 1. Conference calling (SMB lead feature)

Productise the existing bridge rather than inventing one.

| Piece | Why |
|---|---|
| Named rooms (host PIN + guest PIN) | Clients join without ringing the house |
| Ephemeral invite codes | Standing PSTN PINs are an abuse magnet (same rule as game invites) |
| Mute / moderator basics | Makes “regular meeting” usable |
| Optional recording | Announce consent first; short retention |
| Dial-in externals, originate internals | Cheap + safe; see player-identity notes on #7 |

**Differentiation vs Meet/Zoom:** PSTN + desk handsets + no app. That is the
asymmetry already named in #16.

---

## 2. Stand-up meeting

Facilitated mode on the conference engine — same primitives as group bedtime /
Mad Gab (ConfBridge mute/unmute, channel-only prompts, `#` to finish turn),
aimed at SOHO rather than kids.

Flow sketch:

1. Join window — handsets opt in (endpoint = seat); guests get an ephemeral PIN.
2. At schedule (or on demand): originate to opted-in handsets; guests dial in.
3. Doorman facilitates: short intro → unmute one seat at a time (“yesterday /
   today / blockers”, soft per-turn cap, `#` or timeout → next).
4. End — optional blockers clip or Home Assistant webhook. No CRM required.

| | Plain conference `600` | Stand-up |
|---|---|---|
| Model | Free-for-all bridge | Turn-timed facilitator |
| Schedule | Ad hoc | Cron / `[[schedules]]` |
| Value | “Meet here” | “Make the meeting short” |

Ship named rooms first; stand-up is a thin policy + prompt pack
(`kind: standup` or equivalent) on that engine. No live LLM required for v1;
offline summary of blockers is an L5 optional later.

Avoid: video, Jira sync, permanent “standup PIN” on the DID, default dial-out
to everyone’s mobile (trunk cost + #15).

---

## 3. VIP Escalation

Per-person ring order for allow-listed (or PIN-authenticated) callers:
house → mobile → spouse → voicemail. Reuses the ring ladder; trigger becomes
“this person is calling” rather than only inbound DID policy. Depends on
outbound origination and hard rate caps (#15).

Highest-value “I run a business from home” feature after conference rooms.

---

## 4. Office hours and a shallow day IVR

House-level open/closed schedule (related to quiet-hours work in `docs/TASKS.md`
and `docs/roadmap.md`):

- Open: normal lobby / allow-list behaviour (or a short day greeting).
- Closed: pre-rendered closed message → voicemail, or “press 1 for after-hours
  emergency” into a small ring set.

**IVR depth one, maybe two — not a tree farm.** Example: `1` house, `2`
mailbox, `3` conference join, `0` attendant. Policy-driven prompts; deeper
menus are a different product.

---

## 5. Desk-phone hygiene (mostly documentation + small mechanisms)

| Feature | Notes  |
|---------|--------|
| Park / pick-up polish | Already in dialplan; teach it; soft-key cheat-sheet in RUNBOOK |
| Sibling intercom / per-room page | Already on `docs/TASKS.md` |
| Per-handset DND | “In a meeting” → VM or soft reject; kid-DND variant also in TASKS |
| Attended transfer clarity | Transfers work today; restricted transfer context if toll fraud matters (`docs/architecture.md`) |
| Distinctive ring / per-person routing | TASKS + roadmap — client A ≠ school |

---

## 6. Call history the household can use

Local disposition log: who rang (redacted), known / PIN / dismissed /
rate-limited, which handset answered, duration. 
Answers “who called while I was out?” before a CRM.
Feeds a future Prometheus endpoint (`docs/TASKS.md`); 
**no caller identifiers in metric labels**.

---

## 7. Differentiating house features (already issue-shaped)

These are not generic PBX checkboxes; they are why this project is not
Asterisk-with-a-wiki.

| Idea | Issue / doc | One-liner |
|------|-------------|-----------|
| Outbound alert ladders | #15 | Water / alarm / “server down” rings you with DTMF ack |
| Remote actions + dial-back | #16 | Contractor opens a gate; ceremony matches consequence |
| Allow-list step-up + “wasn’t them” taint | #12 | Spoofed “vendor” / “grandma”; partial trust messaged loudly |
| Nominate to allow-list | #6 | Handset `*8`-family flow with parent approval |
| Delayed reminders | #7 thread | Mom leaves a timed “start the pasta” call to a handset set |
| Pack store / games / briefings | #7, `docs/PACKS.md` | Content SKUs on free mechanisms |

General Shape:
> Trigger -> Destination Ladder -> Message

---

## 8. Worth supporting, keep thin

- **Hold / ringback packs** — already in `docs/PACKS.md`; “office” themed bundle
  is an easy SKU.
  - Rotating Voice Messages During HOLD
- **Multi-DID** — personal vs business line, different lobby or IVR; still a
  registration trunk, not a carrier.
  - THIS ONE IS HUGE
- **Click-to-dial from HA or a tiny local UI** — optional originate; rate-capped.
  - Very interesting (not sure I understand the use case)
- **Voicemail email + off-box transcript** — `docs/roadmap.md` phase 2.
- **Single shallow queue** — “someone will be with you” wait bed; full ACD is out.
- **Fax → email** — only if users ask; Asterisk can, UX usually hurts.

---

## 9. Do not chase

- FreePBX feature parity, large hunt groups, call-centre wallboards, CRM hubs
- Hosted multi-tenant PBX (regulated; contradicts the premise)
- Video meetings as a product surface
- Softphone-as-the-product (BYO softphone is fine; we sell the house brain + packs)
- Live TTS/STT on the DID greeting path
- Anything that cripples a mechanism behind a paid pack

---

## Suggested sequence if SOHO is a goal

1. **Named conference rooms** + guest PIN + docs (productise `600`)
2. **Office hours + shallow IVR**
3. **Follow-me** (after #15)
4. **Per-handset DND + sibling intercom**
5. **Call history**
6. **Stand-up facilitator mode** on the conference engine
7. Remote actions / alerts / allow-list step-up for workshop-and-house crowd

Packs, games, and offline AI briefings stay on the #7 track; they monetise
personality and ritual, not SMB checkbox parity.

---

## Related reading

- `docs/TASKS.md` — prioritised work with acceptance criteria
- `docs/roadmap.md` — phase framing
- `docs/PACKS.md` — content kinds and the mechanisms/content line
- `docs/SUSTAINABILITY.md` — what not to turn into a business
- `docs/architecture.md` — transfers, trunk model, StasisEnd discipline
- GitHub #7 (eComm / packs / games), #12 (allow-list trust), #15 (outbound),
  #16 (remote actions), #17 (security epic)
