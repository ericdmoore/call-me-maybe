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
- **Multi-DID** — see §9; it outgrew this list.
- **Click-to-dial from HA or a tiny local UI** — optional originate; rate-capped.
  - Very interesting (not sure I understand the use case)
- **Voicemail email + off-box transcript** — `docs/roadmap.md` phase 2.
- **Single shallow queue** — “someone will be with you” wait bed; full ACD is out.
- **Fax → email** — only if users ask; Asterisk can, UX usually hurts.

---

## 9. The multi-line operator

A distinct persona from the household and from "a business": one person
supporting several small ventures, each with few customers, who wants one
number per venture plus a home line. Six DIDs, one trunk registration, one Pi.
This is the strongest SOHO case because it needs no team features at all — the
per-seat collaboration that hosted services charge for is exactly what a solo
operator is paying for and not using.

Technically it is cheaper than it looks: **one registration carries many DIDs**,
and the dialled number arrives as `${EXTEN}`, which is the discriminator
multi-DID keys on.

| Piece | Why |
|---|---|
| **Line → policy mapping** | The spine. Everything below depends on it |
| **Outbound caller ID per line** | Ring a customer back and they see *that venture's* number |
| Per-line prompts, hours, disposition | A curt doorman on the home line, a courteous concierge on the business ones — same engine, opposite defaults |
| Per-line voicemail | Already policy (`voicemail = "kids"`); needs line scope |
| Per-line call history | The `line` field in the call log |
| Softphone over the tailnet | All six numbers on a mobile without writing an app |

**Outbound caller ID is the sleeper.** The outbound dialplan sets no CID today
(`Dial(PJSIP/1${EXTEN}@voipms,60)`), so every call out presents the trunk
default. Invisible for one household; across five ventures it means every
customer you call back saves the wrong number. Small to build, and the rest
does not land without it.

**The honest limit is SMS.** Customers text small businesses, and for many
ventures it is now the primary channel. Asterisk does not do SMS at all — it is
out of band. VoIP.ms has an API, and polling it avoids an inbound port
entirely (no collision with the ARI bind rule), but there is no thread UI at
the end: messages land in email or a chat bridge. For a venture whose customers
mostly text, this is a complement to a hosted service, not a replacement. See
`SUSTAINABILITY.md`.

**Not** a reason to become a carrier or grow a per-seat plan. The pitch is
ownership, programmability, real handsets in a real house, and no per-seat
pricing — cost is a tiebreaker, not the argument.

---

## 10. Provider account health — the check has shipped

**`doorman balance` exists** as of the s03 M1 / s01 M2.4 milestone: per trunk,
non-zero exit below a threshold, and a provider that invoices reported as
postpaid rather than as a zero. The credential lives on `[[trunks]]` as
`api_username` + `api_password_env` and the daemon never reads it. What remains
open below is the gauge and the alert.

Prepaid trunks fail in the worst way this project has: **the balance hits zero
and the phone simply stops ringing.** Nothing errors, nothing lights up, and
"nobody called today" is indistinguishable from a quiet Tuesday. It is the same
silent-failure shape as an IMAP loop that dies while still reporting connected.

For a multi-line operator it is five businesses' inbound at once.

- **Balance is a capability, not a provider feature.** The same optional-interface
  shape as the voice backends: providers that are prepaid implement it, invoiced
  ones do not and say so. Ties to #5 (more providers).
- **Expose it as a gauge, do not build notification.** Metrics (§4 in TASKS) plus
  whatever alerting already exists beats teaching doorman SMTP, webhooks, retry
  and threshold config. Thresholds are an operator decision.
- **The irony that sets the design:** if the balance is zero, an outbound alert
  cannot call you to say so. The alert path must not be the phone, and the
  threshold must fire well before empty.
- **Where the credential lives matters.** A provider's API key usually manages
  DIDs and sub-accounts, not just reads a number — higher privilege than the SIP
  sub-account password the RUNBOOK already says to keep off the Pi. Prefer
  running the check from wherever alerting already lives.

---

## 11. Do not chase

- FreePBX feature parity, large hunt groups, call-centre wallboards, CRM hubs
- Hosted multi-tenant PBX (regulated; contradicts the premise)
- Video meetings as a product surface
- Softphone-as-the-product (BYO softphone is fine; we sell the house brain + packs)
- Live TTS/STT on the DID greeting path
- Anything that cripples a mechanism behind a paid pack

---

## Suggested sequence if SOHO is a goal

Revised after the grading pass in `product-extensions-grades.md`, which found
that call history and voicemail are already built and that multi-DID multiplies
everything under it:

1. **Multi-DID** (TASKS §7a) — the spine; everything below is better with it
2. **Outbound caller ID per line** (§7c) — do not ship 1 without this
3. **Office hours at line scope + shallow IVR** — the IVR is the existing graph
   interpreter plus routing verbs
4. **Provider balance as a metric** (TASKS §8) — cheap, and it closes a silent
   failure
5. **Named conference rooms** + guest PIN (productise `600`) — design the
   derived-code scheme first
6. **Follow-me** (after #15)
7. **Per-handset DND + sibling intercom**
8. **Stand-up facilitator mode** on the conference engine
9. Remote actions / alerts / allow-list step-up for the workshop-and-house crowd

~~Call history~~ shipped — `internal/calls`, `doorman calls`.
~~Voicemail~~ shipped — only transcription remains (TASKS §2).

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
