# Product extensions — concept grading

A grading of every idea in [`product-extensions.md`](product-extensions.md)
against one question: **how much of this already exists as a concept, and how
much has to be invented?**

An idea that is a new *arrangement* of concepts already in the tree is cheap
and safe. An idea that needs a concept nobody has designed yet is where the
cost and the risk live — and where an invariant usually gets bent.

## How to read the columns

**`conceptsNeed`** — what the feature requires, marked by where it stands:

- **built** — in the tree and working today
- **doc'd** — specified in a doc, a TASKS entry, or a GitHub issue, but not
  implemented
- **new** — nobody has designed this yet

**`usesNewConcepts`** — No / Partial / Yes. Anything above "No" needs a design
conversation before an acceptance criterion.

**`alignmentScore`** (0–10) — fit with what this project has decided to be, not
how good the idea is. Four things pull it down:

1. **Invariant collision.** Especially *no database* (invariant: rate-limit
   state is in memory and *meant* to be lost), *ARI never leaves 127.0.0.1*,
   and *no runtime TTS on a path that can make the house unreachable*.
2. **Crossing the mechanisms/content line** — a mechanism that only works with
   a purchased pack.
3. **Drift toward the "do not chase" list** — FreePBX parity, multi-tenant
   SaaS, call-centre surfaces.
4. **Distance from the household premise.** SOHO features score fine; features
   that only make sense for a company with an office score lower.

A 9–10 is a weekend of config and documentation. A 4–5 is a real design
problem with a values question inside it.

---

## The table

| featureName | shortDescription | conceptsNeed | usesNewConcepts | alignmentScore |
|---|---|---|---|---|
| **Call history** | Local disposition log: who rang, what happened, which handset answered | **built** — `internal/calls`, `doorman calls`, invariant 10 | No | **10** |
| **Park / pick-up polish** | Teach the parking already wired up; soft-key cheat sheet | **built** (`res_parking.conf`, `include => parkedcalls`); docs only | No | **10** |
| **Sibling intercom / per-room page** | Page one room instead of all; page-all 500 already generates | **built** page mechanism + `page = true` render; **doc'd** per-room in TASKS | No | **9** |
| **Hold / ringback packs** | Themed hold beds and ringback as content SKUs | **built** `musiconhold.conf` + pack builder; **doc'd** as `kind: hold`/`ringback` | No | **9** |
| **Rotating hold messages** | Different clip each time so a wait is not identical on repeat | **doc'd** `kind: rotation` — PACKS.md calls it a `RAND()` in the dialplan | No | **9** |
| **Office hours (house level)** | Open/closed at the house, not just per-extension | **built** `[[schedules]]` + `afterhours`; needs the same concept at house scope | No | **9** |
| **Outbound alert ladders (#15)** | Water sensor rings you, DTMF to acknowledge | **built** ring ladder; **doc'd** outbound + rate caps in #15 | Partial — cost governor | **9** |
| **VIP escalation / follow-me** | Per-person ring order: house → mobile → spouse → voicemail | **built** ladder; **doc'd** `ring` on `[[people]]` (TASKS:173) + #15 | Partial — outbound | **9** |
| **Attended transfer clarity** | Document transfers; restricted transfer context against toll fraud | **built** (invariant 8 already protects it); dialplan context + docs | No | **9** |
| **Multi-DID** | Personal vs business line, each with its own lobby | **new** — DID→policy mapping; policy.toml is singular today | Yes — but cleanly | **9** |
| **Outbound caller ID per line** | Call a customer back and they see *that venture's* number | **new** — the outbound dialplan sets no CID at all today; needs `[line] outbound_cid` + a dial prefix to select | Partial — small | **9** |
| **Per-line voicemail** | Each number gets its own mailbox and greeting | **built** — mailboxes are already per-extension/house policy; needs line scope | No | **9** |
| **Provider balance as a metric** | Prepaid trunk hits zero and the phone silently stops ringing | **doc'd** metrics endpoint (TASKS §4); **new** — an optional `Balance` capability per provider, same shape as the voice backends | Partial | **9** |
| **Low-balance notification** | Tell someone before the trunk dies | **new** — but should not be built: emit the gauge and let existing alerting decide thresholds and delivery | Yes | **6** |
| **Softphone over Tailscale** | The six numbers on your mobile, no app to write | **built** — handsets register today; **doc'd** partially on the site | No | **8** |
| **SMS / MMS** | Customers text small businesses; for many it is the primary channel | **new** — poll the VoIP.ms API (no inbound port, no invariant collision); delivery to email or a chat bridge. No thread UI | Yes | **6** |
| **Shallow day IVR** | Depth one or two: 1 house, 2 mailbox, 3 conference, 0 attendant | **built** graph interpreter (`internal/story`) — but needs routing verbs the story sandbox deliberately lacks | Partial | **8** |
| **Distinctive ring / per-person routing** | Client A rings differently from school | **doc'd** TASKS + roadmap; SIP `Alert-Info` on originate (page already uses it) | Partial — small | **8** |
| **Per-handset DND** | "In a meeting" → voicemail or soft reject | **doc'd** TASKS; **new** — mutable runtime handset state (the second such state after the rate limiter) | Partial | **8** |
| **Remote actions + dial-back (#16)** | Contractor opens a gate; ceremony matches consequence | **doc'd** #16; **new** — action registry, dial-back auth | Yes | **8** |
| **Pack store / briefings** | Content SKUs on free mechanisms | **built** — voice + story kinds, `pack build`, four backends | No | **8** |
| **Named conference rooms** | Host PIN + guest PIN so clients join without ringing the house | **built** ConfBridge 600 + PIN matching; **new** — a room *object* with two credential tiers | Partial | **8** |
| **Voicemail email + transcript** | Message lands in a mailbox and an inbox | **built** — app_voicemail records, stores, emails the WAV, lights MWI; doorman hands off via `[voicemail-drop]`. **doc'd** — transcription via `externnotify` (TASKS §2) | No | **9** |
| **Nominate to allow-list (#6)** | Handset flow to add a caller, with parent approval | **built** atomic validated policy.toml writes (`RotatePins`); **doc'd** #6 | Partial | **7** |
| **Allow-list step-up + taint (#12)** | Partial trust for spoofable caller ID, messaged loudly | **doc'd** #12; **new** — a trust tier and taint that has to persist | Yes | **7** |
| **Mute / moderator basics** | Make a recurring meeting usable | **new** — per-participant ConfBridge control over ARI | Yes | **6** |
| **Delayed reminders** | "Start the pasta" call to a handset set at a set time | **doc'd** #7 thread; **new** — scheduled jobs that survive a restart, or admit they do not | Yes | **6** |
| **Games (Mad Gab etc.)** | Multiplayer over handsets and PSTN | **built** story graph; **doc'd** player identity in #7; **new** — per-seat state | Yes | **6** |
| **Stand-up facilitator** | Turn-timed meeting: unmute one seat at a time | **new** — turn state machine on top of per-participant mute | Yes | **6** |
| **Ephemeral invite codes** | Time-boxed conference codes; standing PSTN PINs are an abuse magnet | **new** — credential lifecycle. Collides with *no database*; a derived HMAC-of-(room, window) code would dodge it entirely | Yes | **5** |
| **Click-to-dial from HA** | Originate a call from a local UI or automation | **new** — an inbound control surface, against invariant 2 (ARI never past 127.0.0.1). Tailnet-only is the documented escape | Yes | **5** |
| **Single shallow queue** | "Someone will be with you" with a wait bed | **new** — queue position state | Yes | **4** |
| **Optional call recording** | Announce consent, short retention | **new** — storage, retention policy, consent copy, per-jurisdiction two-party rules | Yes | **4** |
| **Fax → email** | Asterisk can; the UX usually hurts | **new** — T.38 handling | Yes | **2** |

---

## What the grading says

**Seven things are already built and only need documenting or exposing.** Park,
page, hold beds, call history, the pack pipeline, the graph interpreter — and
voicemail, which `CLAUDE.md` described as absent until this pass. Asterisk's
`app_voicemail` has been recording, storing, emailing the WAV and lighting MWI
since the handset-features work; only transcription is open (TASKS §2). Park and page in particular are described in
`product-extensions.md` as needing "polish" — they need a paragraph in the
RUNBOOK, not code.

**The suggested sequence is nearly right, with one swap.** Its step 5 is call
history, which now exists. Its step 1 is named conference rooms, which is the
*second*-cheapest item in the top group rather than the first. Office hours at
house scope is one scope change on a concept that already works, and shallow
IVR is a graph engine that already exists.

**Multi-DID deserves the "THIS ONE IS HUGE" note.** It scores 9 despite needing
a genuinely new concept, because the concept is clean: a mapping from inbound
DID to policy. It does not fight an invariant, it does not need runtime state,
and Asterisk already knows which DID a call arrived on. It also multiplies the
value of everything above it — office hours, IVR, and distinctive ring all get
better with a second number, and it is the difference between "our house phone"
and "my business line that happens to run at home."

**One idea has a much better shape than the one written down.** Ephemeral
invite codes score 5 because storing them collides with *no database* — and if
they live in memory, a restart drops every code mid-meeting. But a code
**derived** as an HMAC of (room id, time window, secret) needs no storage at
all: it can be computed on demand, verified without a lookup, and expires by
arithmetic. That turns the weakest item in the conference group into one that
fits the invariants exactly. Worth designing before naming rooms, because it
changes what a room *is*.

**The shallow IVR is closer than it looks, and the reason matters.**
`internal/story` already parses a node graph, validates every anchor, and
interprets it against a `Teller` interface. A day IVR is the same engine with
one addition: verbs that route a caller to a handset or a mailbox — precisely
what the story sandbox refuses to have. The clean resolution is that the two
graph types differ by **provenance, not shape**: story graphs come from packs
(downloaded, untrusted, sandboxed), IVR graphs come from `policy.toml`
(operator-authored, trusted, may route). Same interpreter, different verb set
selected by where the graph came from. That is a small change and a sharp line.

**The multi-line operator is a real persona and it reorders things.** Someone
supporting several small ventures alone wants six numbers on one box — which
multi-DID gives them — but the feature that actually makes it work is one
nobody had written down: **outbound caller ID per line**. The outbound dialplan
sets no CID today, so every call out presents the trunk default. For one
household that is invisible; across five ventures it means every customer you
ring back saves the wrong number. It is small, and without it the rest does not
land.

The honest limit for that persona is SMS. It scores 6 not because it is hard
but because there is no app at the end of it: polling the VoIP.ms API avoids
every invariant problem, and then the messages have to go somewhere a person
reads. See `SUSTAINABILITY.md` — for a venture whose customers primarily text,
this is a complement to a hosted service, not a replacement.

**The bottom of the table is bottom for values reasons, not difficulty.**
Recording is not hard; it is a consent and retention problem in a house with
children, and the project has no story for either. A queue is easy and is the
first step onto the "do not chase" list. Fax is a solved problem that makes
everyone who touches it unhappy.

---

## Related reading

- [`product-extensions.md`](product-extensions.md) — the ideas being graded
- [`TASKS.md`](TASKS.md) — prioritised work with acceptance criteria
- [`PACKS.md`](PACKS.md) / [`STORY-PACKS.md`](STORY-PACKS.md) — the content kinds
- [`SUSTAINABILITY.md`](SUSTAINABILITY.md) — what not to turn into a business
- `CLAUDE.md` — the invariants the alignment score is measured against
