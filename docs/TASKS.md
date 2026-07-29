# Tasks

Ordered. Each has enough context to start without asking questions. Acceptance
criteria are the definition of done — if they all hold, the task is finished.

Ground rules for every task: `make check` green (vet + test + build), no
secrets committed, and the invariants in `CLAUDE.md` intact.

---

## 1. Mock ARI harness — DONE (shipped with the Go port)

Delivered as `internal/lobby/fake_ari_test.go` plus `session_test.go`: nine
scenarios — known caller, valid PIN, silent timeout, wrong-PIN retries,
barge-in, ring-group first-answer-wins, late-answer race, caller hangup
mid-greeting, and rate-limited fast dismissal — all with no Asterisk. Extend
these tests for any state machine change.

---

## 2. Voicemail — MOSTLY DONE (app_voicemail + ladder fallback)

Landed with the handset-features work: Asterisk's `app_voicemail` records,
stores, emails with the WAV attached (`attach=yes`), and lights MWI lamps;
doorman releases unanswered and afterhours callers into `[voicemail-drop]`
via ARI continue with `MAILBOX` set. Per-extension and house mailboxes are
policy (`voicemail = "kids"`).

**Remaining: transcription.** Wire `externnotify` in `voicemail.conf` to a
small script that fires per new message, POSTs the WAV to the whisper
endpoint, and emails the transcript (the audio email already went out —
transcription is enrichment, never a gate).

- [ ] Script is fire-and-forget and cannot delay or fail message delivery.
- [ ] STT endpoint down → message flow completely unaffected.
- [ ] No recordings deleted; retention stays a separate decision (2c below).

**2c. Retention** still stands: configurable window, default 90 days, sweep
on a timer, decided before the disk fills.

## 3. Quiet hours — DONE for extensions; house variant open

Per-extension afterhours shipped: `[extensions.afterhours]` with
start/end/days, midnight-wrap with start-day semantics, `enabled` toggle,
straight-to-voicemail behaviour, `doorman check` showing ACTIVE NOW. Tested
across midnight and day boundaries in `internal/policy/policy_test.go`.

**Remaining:** an optional `[house.afterhours]` that swaps the known-caller
ring group for a smaller night set (e.g. primary-bed only) rather than
voicemail — different semantics from the extension version (known callers at
3am might be emergencies; they should still ring *something*).

- [ ] Absent the block, behaviour is byte-identical to today.
- [ ] Reuses the compiled Afterhours type; no second time-window parser.

## 3b. Rotation mechanism — unlocks every content pack

**Why:** a joke told identically every time is not a joke twice. Rotation is
the one missing mechanism standing between the current system and an entire
category of content packs (jokes, riddles, facts, sound effects). It is also
tiny — no Go changes, four lines of dialplan.

```ini
exten => 611,1,Answer()
 same => n,Set(N=${RAND(1,42)})
 same => n,Playback(cmm-jokes/joke-${N})
 same => n,Hangup()
```

- [ ] A `rotation` pack kind: numbered clips plus a count in `pack.json`.
- [ ] Dialplan reads the count rather than hardcoding it, so adding clips
      does not require editing config.
- [ ] Ships with a small free rotation pack so the mechanism is complete
      without a purchase (see the mechanisms/content line in `docs/PACKS.md`).
- [ ] Avoid immediate repeats if it is cheap to do so; a caller hearing the
      same joke twice running is the whole failure mode.

**Files:** `asterisk/extensions.conf`, `docs/PACKS.md`.

---

## 3c. Distinctive ring for allow-listed callers

**Why:** "Grandma's calls sound different from everyone else's" is the single
most-wanted thing on the ringtone list, and the only item there that needs a
code change rather than a file.

doorman sets an `Alert-Info` header when originating ring-group legs; the
Grandstreams map header values to ringtones. The ARI originate call needs a
`variables` body, which the current client does not send.

- [ ] `Originate` accepts channel variables; `SIPADDHEADER` or equivalent
      sets `Alert-Info` per leg.
- [ ] Optional per-person `ring` value in policy, defaulting to no header
      (today's behaviour byte-for-byte).
- [ ] Covered by the fake-ARI harness — assert the header is set for a known
      caller and absent for a lobby caller.

**Files:** `internal/ari/client.go`, `internal/lobby/session.go`,
`internal/policy/policy.go`.

---

## 3d. Kid-line features

**Why:** the extensions most likely to make the system beloved rather than
merely useful. All are dialplan; none touch doorman.

- [ ] **Sound board** — an extension where DTMF digits play clips. Expect
      this to be the most-used feature in the house.
- [ ] **Sibling intercom** — page exactly one room rather than all.
- [ ] **Self-recorded voicemail greeting** — `Record()` from the handset, so
      a kid can hear their own voice answer.
- [ ] **Complaint department** — records a grievance, emails the parents.
- [ ] **Kid-controlled DND** — a code that sends their own line to voicemail.
      Real agency over their own line, which teaches the boundary rather than
      imposing it.
- [ ] **Countdown line** — days until a birthday, via `SayNumber`.

Design line, non-negotiable: the parent remains the admin. None of these may
let an extension bypass the lobby or conceal who called.

---

## 3e. Bedtime stories — the flagship

**Why:** the most emotionally compelling thing this system could do, and the
feature most likely to make someone install a home phone in 2026 who
otherwise would not. Grandparents dial in, record a story, it lands on the
kids' line for playback.

This is the voicemail infrastructure inverted: record to a shared mailbox,
play back on demand rather than notify. Build it free and prominent; it sells
everything else by existing.

- [ ] An allow-listed caller can dial a code and record to a story mailbox.
- [ ] A kid's handset dials a code to play the newest, or browse.
- [ ] Stories are retained on a different schedule from voicemail — these are
      keepsakes, not messages. Do not let the voicemail sweep eat them.
- [ ] Recordings are exportable; someone will want these in twenty years.

**Files:** `asterisk/extensions.conf`, `asterisk/voicemail.conf.example`,
`docs/RUNBOOK.md`.

---

## 4. Metrics endpoint

**Why:** right now the only way to know the trunk dropped is that the phone
stopped ringing.

A tiny HTTP server on loopback exposing Prometheus text format, scraped by the
existing homelab collector.

- [ ] Counters: calls by disposition (welcomed / extension / dismissed /
      rate-limited / no-answer), PIN failures, policy reload success and
      failure.
- [ ] Gauges: active calls, ARI connection state, allow-list size.
- [ ] Histogram: ring-to-answer latency.
- [ ] Binds loopback only, port configurable, off by default.
- [ ] **No caller identifiers in labels.** Per-caller cardinality is both a
      privacy leak and a way to blow up the TSDB.

**Files:** new `internal/metrics/`, `cmd/doorman/main.go`,
`internal/config/config.go`.

---

## 5. Per-person routing overrides

**Why:** not every allow-listed caller should ring every handset.

Optional `ring` key on `[[people]]` naming a handset list or an extension label,
overriding `[house]` for that caller.

- [ ] Optional; absent means today's behaviour.
- [ ] Validation catches references to handsets that do not exist, at load time.
- [ ] Interacts sanely with task 3 — decide and document which wins. (Suggest:
      per-person override wins, since it is the more specific statement.)

**Files:** `internal/policy/policy.go`, `internal/lobby/session.go`.

---

## 6. Home Assistant notification

**Why:** cheap, and makes the system feel present in the house.

Fire a webhook on known-caller arrival so HA can announce over speakers or
flash a light.

- [ ] Fire-and-forget with a short timeout. **A slow webhook must never delay
      the greeting** — dispatch it without awaiting on the call path.
- [ ] Failures are logged at `warn` and never affect the call.
- [ ] Off by default; URL from config.

**Files:** new `internal/notify/`, `internal/lobby/session.go`.

---

## Known rough edges

Project direction: code is Apache 2.0 as of v0.4.1, with audio licensed
separately (`LICENSES.md`) and a pack format specified in `docs/PACKS.md`.
Sustainability thinking — what can pay for itself and what to avoid — is in
`docs/SUSTAINABILITY.md`. The load-bearing rule for anything on this list:
**mechanisms are free and must work completely with the bundled pack.**

Config-interface refactor landed (v0.3.0): split handsets.toml/policy.toml
with a legacy fallback, named [[schedules]], and `doorman render` generating
the per-handset Asterisk config. New rough edge for the list below: render
output installation is manual (copy + reload); a `render -install` that does
the copy/chown/reload itself would remove the last hand step.

Not tasks yet, but real. Worth fixing if you are in the area:

- **Anonymous callers share one rate-limit bucket.** Intended, but it means one
  persistent blocked caller makes the lobby stricter for every other withheld
  number. Worth revisiting if it bites.
- **No test covers `policy.Store` hot reload.** The mtime polling and the
  keep-last-good behaviour are both load-bearing and both unverified. (Rotation
  is tested; the reload that consumes its atomic write is not.)
- **DTMF during the invalid-extension prompt is ignored** (no barge-in there,
  unlike the greeting). A caller who instantly re-dials talks over a ~2-second
  prompt and their digits land after it finishes. Fine in practice, worth
  knowing before "the phone ate my first digit" gets debugged as a DTMF fault.
- **`doorman rotate` and a concurrent manual edit race.** Rotation reads,
  edits, and renames; an editor writing in that window loses. Single-operator
  households will never see it; documented so nobody chases it as corruption.
