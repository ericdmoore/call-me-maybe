# s06 · Speakers as page targets — development plan

Let the good speakers people already own carry a page, without pretending they
are telephones.

**Status:** planned. Nothing started.
Related: `docs/TASKS.md` §6 (Home Assistant notification — the prerequisite),
`.plans/s05-system-alerts`, `asterisk/extensions.conf` (`page-500` today).

---

## What this is, and is not

The house already pages: dial `500` and every handset with `page = true` opens
at once, auto-answered via `Alert-Info`. That works and is not changing.

This adds **Sonos and Google Cast speakers as additional page targets**, because
in most houses the kitchen already has a far better speaker than any handset's
speakerphone, and it is already in the room where the page matters.

**It is one-way, permanently.** These are not handsets:

| | Sonos / Cast | SIP handset |
|---|---|---|
| Hear a page | yes | yes |
| Answer a call | **no** | yes |
| Dial out | **no** | yes |
| Be an extension | **no** | yes |
| Two-way audio | **no** | yes |

No SIP, and the microphones on the models that have them are not usable as a
phone. Anything wanting two-way audio needs a handset or a softphone. Saying so
here so nobody spends a week discovering it.

---

## Why these two, and not Alexa

**Sonos and Cast both have local control.** A page reaches the speaker over the
LAN, with no cloud in the path.

**Echo has no local control surface at all.** There is no on-device API; every
announcement routes through Amazon's cloud and back. That means a page from the
kitchen to a bedroom leaves the house, and **fails when the internet does** —
which is the wrong dependency for a telephone, and disqualifying for anything
in `s05` where the rule is that an alert must work when the network is down.
(The reverse-engineered `alexa_remote_control` path that Home Assistant's Alexa
Media Player uses does function, but it needs Amazon credentials, still goes
via the cloud, and breaks when Amazon changes something.)

Recorded as a rejection rather than an omission, because "can we add Alexa" is
a question that will be asked again.

---

## Architecture

Both speaker families work the same way: **they play a URL.** doorman already
produces WAV files — pack prompts today, alert clips under s05 — so paging a
speaker is pointing it at one.

Which raises the only real architectural question: *who serves the file?*

**Not doorman.** Serving audio over HTTP means a new listening port on the Pi.
It is not ARI and not a control surface, but this project has been deliberate
about what listens and where (invariant 2), and adding a web server to play
dinner announcements is a poor trade.

**Home Assistant serves it.** HA already has a media server, already speaks
Sonos and Cast locally, and is already the intended integration point.

```
doorman ──webhook──► Home Assistant ──LAN──► Sonos / Cast / anything else
```

doorman opens nothing. One integration, and every downstream target HA supports
comes free — which is the same argument as `TASKS.md` §6 and now has a second
concrete reason behind it.

### doorman does not know what a speaker is

**Rejected: `media_player` entities in `policy.toml`.** doorman would gain a
speaker inventory, a second discovery mechanism, and a config surface that
duplicates something HA already models properly.

Instead doorman emits an **event** — *a page happened, here is the clip and
which group it was for* — and HA decides which speakers hear it. Routing lives
where the speakers live.

The cost, accepted: paging one specific speaker from a handset is an HA
automation rather than a line in `policy.toml`. For a first version that is the
right side of the trade, and HA expresses that kind of rule better than a
policy file would.

---

## Two details that will bite

**Sonos ducks and resumes; Cast does not.** Sonos supports announcement
playback that lowers whatever is playing, speaks, then restores it. Casting
generally interrupts. That difference is worth surfacing in the runbook,
because "the page killed my album" is how paging gets switched off.

**Telephony audio sounds thin on a good speaker.** Pack clips are 8 kHz and
16 kHz mono, correct for a phone line and poor through a Sonos. The pack
pipeline already holds the source at 22–24 kHz *before* downsampling
(`voice.Prepare` normalises once, then resamples per rate), so a third output
rate for speaker targets is a small change to an existing loop rather than a
new pipeline. Worth doing at the same time; retrofitting means re-rendering
every pack.

---

## Milestones

### M1 · The event (this is `TASKS.md` §6)

- A webhook doorman fires on pages and on `s05` alerts: event type, clip name,
  group, severity.
- Configurable URL; absent means disabled. No speaker knowledge in doorman.
- Non-blocking and fire-and-forget — a dead HA must never delay a call or an
  alert.

**Deliverable:** an HA automation can announce "call from Grandma" on any
speaker in the house.

### M2 · Speaker-quality audio

- A third rate in `voice.PackRates` for non-telephony targets.
- `doorman pack build` emits it; clips stay available at all three.

### M3 · Runbook

- Worked HA automations for Sonos and for Cast, including `announce: true`.
- The ducking difference, stated.
- Volume: a page at 3am at whatever volume the speaker was left on is its own
  problem, and HA can pin it.

---

## Risks

| Risk | Mitigation |
|---|---|
| Latency — a webhook plus discovery plus playback is seconds, not instant | Fine for "dinner's ready", marginal for a doorbell. SIP handsets stay the fast path; speakers are additive |
| A page at full volume at 3am | Volume set per automation; `s05` quiet hours apply to alerts |
| HA becomes a hard dependency | It is optional throughout. No HA means no speaker paging and everything else is unaffected |
| Telephony audio sounds bad and people blame the phone | M2, done alongside rather than after |
| Somebody tries to make a speaker an extension | It cannot be. Stated at the top of this document |

---

## Out of scope

Two-way audio · speakers as extensions or handsets · Alexa · a doorman-side
media server · speaker discovery, inventory or grouping — all of that is Home
Assistant's job and it already does it.
