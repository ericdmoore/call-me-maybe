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

## 6. Home Assistant notification — DONE

Landed as `internal/notify`: doorman POSTs one JSON object when the house
starts ringing and one when a call ends. This is the single integration point
that makes everything downstream — Sonos, Cast, MQTT, Alexa via HA — work
without doorman knowing any of them exist. See
`.plans/s06-speaker-page-targets` for why that indirection is deliberate.

- [x] Fire-and-forget: `Post` hands off to a buffered channel drained by one
      goroutine, exactly as `internal/calls` does. A full buffer drops and
      counts. Proven by `TestPostNeverBlocks`.
- [x] Failures logged at `warn` and counted (`Failed()`); the call is
      unaffected. A dead endpoint is also bounded at shutdown rather than
      holding the process open for buffer-depth × timeout.
- [x] Off by default. `WEBHOOK_URL` unset means the sink is nil and no code
      runs; a URL that cannot work is fatal at startup, where an operator sees
      it, rather than a warn line after the first call nobody heard announced.

**Two events, not one.** `ringing` fires from `runPlan` the moment handsets
start ringing — for a welcomed known caller *and* for a stranger who dialled a
valid extension — because "call from Grandma" is worth announcing while
somebody can still reach a handset and worth nothing afterwards. `completed`
fires from the deferred `cleanup`, the single teardown path, so there is
exactly one per call however it ended. A caller who is dismissed rings nothing
and gets no `ringing` event.

**What it never carries.** No `media_player` entities, speaker names, or
routing: the payload is an event and HA decides who hears it. No entered
digits or PINs — structurally, because `notify.FromRecord` is the only
constructor and a `calls.Record` has no field one could occupy. Caller IDs are
**redacted by default**, the opposite of the call log and deliberately so: the
call log is a 0600 file that stays on the box, this is handed to another
program over the network. `WEBHOOK_REDACT_CALLER_ID=false` opts in.

**Keys:** `WEBHOOK_URL`, `WEBHOOK_TOKEN`, `WEBHOOK_TIMEOUT_MS`,
`WEBHOOK_REDACT_CALLER_ID`. The URL is a credential — an HA webhook id is the
whole secret — so doorman logs only its host, and `TestTheURLNeverReachesTheLog`
guards the `net/url` error path that would otherwise leak it.

**Files:** `internal/notify/`, `internal/lobby/session.go`,
`internal/config/config.go`, `internal/schema/schema.go`,
`cmd/doorman/main.go`, `docs/RUNBOOK.md` §6b.

**Remaining, and it belongs to s06 M3 rather than here:** worked HA automations
for Sonos and Cast in the runbook, including the ducking difference.

---

## 7. Multi-DID — one box, several numbers

**Why:** the single change that turns a house phone into a house phone *plus*
however many business lines the household needs, without becoming a carrier.
It multiplies office hours, IVR, distinctive ring and voicemail — each of those
gets more useful once there is a second number. See
`docs/product-extensions.md` §9.

The design is settled: **the dialplan names the line, one policy file per line,
the router multiplexes.** Asterisk already knows the dialled number
(`${EXTEN}`, currently logged and discarded), and `route` already branches on
`ev.Args[0] == "leg"` — the line arrives the same way.

Ordered into four groups. **7a is a hard prerequisite for the rest; 7b–7d are
independent of each other.**

**All four have landed** — one box, several numbers, one provider. What is left
under 7b is the schedules-at-line-scope item, which is a `[[schedules]]`
question rather than a line one.

Several *providers* is Phase 2 of `.plans/s01-multiple-DIDs/readme.md`, and it
has now landed but for per-provider health. `trunks.toml` is a provider
inventory rendered the way `handsets.toml` is, `[line] trunk` names which
provider a number arrives on, and `doorman render` generates the registrations,
one inbound context per trunk, and the emergency ladder. The file is optional
and its absence is today's behaviour exactly.

**Outbound now leaves by the line's trunk, on both paths.** A second channel
variable, `OUTBOUND_TRUNK`, arrives the same two ways `OUTBOUND_CID` does — a
`set_var` per endpoint from `doorman render`, and the `*4` console setting it
per call — and `[internal]` and `[outbound-console]` both include one shared
`[cmm-outbound]` context so the two cannot drift. `outbound_cid` is checked
against the trunk that will carry it: a number this config declares at another
provider is refused by `doorman check` and `doorman render`, and a number
nothing declares is reported, because no static check can ask a provider what
an account owns. And 911 leaves by the designated trunk, falls over to the
others in turn if that one cannot carry it, presents no caller ID on any path,
and fails audibly rather than silently. Undecided is now an error in both
`check` and `render`.

What is left under Phase 2 is per-provider balance (§8 below, per trunk).

---

### 7a. The routing spine

One context per DID in the dialplan passes the line name:

```
exten => _X.,1,Answer()
 same => n,Stasis(${DOORMAN_APP},line,biz)
```

Named in the dialplan rather than parsed out of SIP headers, because what a
provider puts in the To header varies (see #5) and the dialplan is the one file
that already knows which number is which.

**Done.** `policy.DiscoverLines` + `cmd/doorman/lines.go`; `doorman check`
grows a `Lines:` block only when there is more than one.

- [x] `policy.<line>.toml` per line; bare `policy.toml` is the default line.
- [x] **No app arg → today's behaviour, byte for byte.** Existing installs do
      not change.
- [x] One `policy.Store` per line, so a syntax error in the business policy
      cannot take down the home line — invariant 4, generalised. This is the
      reason for separate files rather than `[[lines]]` sections.
- [x] An unknown line name falls back to the default line and logs loudly. It
      must never drop the call: doorman cannot read the dialplan, so it cannot
      validate the set at startup.
- [x] `internal/lobby` unchanged. `Deps.Policy` is already
      `func() *policy.Policy`; the router picks per-line `Deps` at StasisStart.
- [x] Rate-limit budget is per line — two lines with opposite dispositions
      must not share one. One `RateLimiter` per line rather than the line in
      the key, because the key is built inside `internal/lobby` and reaching
      in there is the change this design exists to avoid.
- [x] `doorman check` prints the lines it found and what each resolves to.

**Files:** `cmd/doorman/main.go`, `internal/config`, `internal/policy`,
`asterisk/extensions.conf`, `internal/schema`.

### 7b. Line identity

A `[line]` section carrying what makes a line feel like itself:

```toml
[line]
label   = "Mertaugh Enterprises"
number  = "+15125550142"
prompts = "concierge"
on_no_input = "voicemail"   # dismiss | ring-house | voicemail
```

**Done**, except the schedules item. `policy.Line()` + `compileLine`;
`Session.noInput` is the disposition switch.

- [x] Per-line prompt prefix, overriding `PROMPT_MEDIA_PREFIX`. Resolved from
      the policy the session captured rather than fixed in `Deps` at startup,
      so swapping a pack reloads like any other policy edit.
- [x] `on_no_input` — the disposition knob. A curt doorman and a courteous
      concierge are the same engine with opposite defaults; today the lobby can
      only dismiss. `voicemail` requires `[house] voicemail`, same rule as
      `afterhours`. It governs an **empty** dial window only — a caller who
      exhausts their PIN attempts is still dismissed, and a silent caller is
      never charged a rate-limit failure for saying nothing (invariant 6).
- [x] Known callers can reach the collect loop, with timeout meaning *admit*
      rather than *dismiss* (the home line's "no dial → ring all"). **The
      welcome prompt is the dial window and the whole of it** — barge-in, no
      tail — so a known caller who dials nothing rings the house with zero
      added latency, which is the compatibility gate on the commonest path in
      the system. Nothing at the keypad can cost an allow-listed caller the
      house: no digits, half an entry abandoned, and attempts exhausted all
      ring it.
- [ ] Schedules usable at line scope, not only per-extension (relates to §3).
      Not attempted with the rest of 7b — it is a `[[schedules]]` question
      rather than a line-identity one, and it wants the `[house.afterhours]`
      decision in §3 settled first so the two do not disagree about what a
      window at house scope means.

### 7c. Outbound identity — **do not ship 7a without this**

The outbound dialplan set **no caller ID at all**
(`Dial(PJSIP/1${EXTEN}@voipms,60)`), so every outbound call presented the trunk
default. Invisible with one number; across several lines every customer you
ring back saves the wrong one.

**Done.** `[line] outbound_cid` + `outbound_handsets`; `cmd/doorman/outbound.go`
resolves them; `internal/lobby/console.go` is `*4`.

- [x] `[line] outbound_cid`, normalised to E.164 and validated at check time.
- [x] Per-handset default line, for the common case. Written as
      `[line] outbound_handsets` in the *line's* file rather than as a key on
      each handset: `handsets.toml` is one shared inventory and cannot hold
      something true of only one line, deleting the file removes the claim, and
      "two lines claim the same phone" becomes a question `doorman check` can
      answer. It reaches the phone through `doorman render` as a `set_var` on
      the endpoint, because the plain dial path never touches doorman.
- [x] **A handset no line claims presents the primary line** — plain
      `policy.toml`, the same file 911 leaves by. One rule, two jobs.
- [x] `*4`, the outbound console: menu, confirmation, then a variable-length
      `#`-terminated number. A sibling of `Session` in `internal/lobby`, not a
      mode of it — everything `Session` is built around is inbound. It sets the
      caller ID and releases the handset into `[outbound-console]` rather than
      originating and bridging, so the trunk dial string stays in the dialplan
      and both outbound paths dial through the same lines.
- [x] **`*4` refuses emergency numbers**, and the context it releases into
      contains no emergency pattern for a bug to reach. `_911` lives in
      `[internal]`, outside the outbound context both paths share, and sets no
      caller ID.
- [x] The plain `_NXXNXXXXXX` path keeps working with doorman down.
- [x] **Outbound leaves by the line's trunk** (Phase 2, M2.3). `OUTBOUND_TRUNK`
      beside `OUTBOUND_CID`, from the same `[line]`, on both paths — a provider
      will not present a number its account does not own. `doorman check` and
      `doorman render` refuse an `outbound_cid` this config declares at another
      provider; a number nothing declares is reported rather than refused,
      because nothing can ask a provider what an account owns.
- [x] **Rejected: a dial prefix** (`*3` + number). It needs `*1`–`*9` reserved
      forever, fights `*97` and every future feature code, and with five
      ventures nobody remembers which digit is which. A menu that says the
      numbers out loud beats a mapping you have to memorise.
      See `.plans/s01-multiple-DIDs/readme.md`.
- [x] Outbound calls in the call log. Landed in 7d, where it belonged: it
      wanted a direction on the record, not just a line.

**Files:** `asterisk/extensions.conf`, `internal/policy`, `internal/render`,
`internal/lobby`, `cmd/doorman`.

### 7d. Per-line observability

**Done, and this closes Phase 1** — several numbers on one provider, each with
its own rules, its own outbound identity, and now its own readable history.
Phase 2 (several *providers*) is a separate problem and starts at `trunks.toml`.

- [x] `line` field on the call record (one log, not one per line, so a whole
      day still reads in order). Absent for the default line, so a box with one
      number writes byte-identical records and a record from an older doorman
      still means what it says. `calls.Record.LineOrDefault` resolves it.
- [x] `direction` on the record, and outbound console calls recorded. Not in
      7c because the outcome vocabulary there is about who answered an inbound
      call: an outbound call gets `placed` — doorman sets the caller ID, hands
      the channel to the dialplan and is out of the call before the far end
      rings, so "answered" is not a thing it can know. The number goes in
      `dialled` (full on disk, redacted by `Redacted`, exactly like `caller`),
      and only a complete number the console accepted lands there — a fumbled
      half-entry is forgotten, which is where invariant 1's line falls.
- [x] `doorman calls --line biz`, plus `--direction`, and `--caller` now
      matches either end of the call.
- [x] The `LINE` column appears only once a call has arrived on a line with a
      name, and the direction column only once something has gone out — the
      same rule `doorman check` follows, so one number never reads the word
      "line". `CALLER` becomes `NUMBER` in the same breath, because the header
      is only truthful while every row is inbound.
- [x] The webhook carries `line` on both events — routing an announcement by
      which number was rung is exactly what Home Assistant wants — and stays
      inbound-only: an outbound call rings nothing in this house.
- [ ] If §4 metrics land, a `line` label — and still **no caller identifiers
      in labels**.

---

## 8. Provider account health

**Why:** a prepaid trunk that hits zero stops the phone ringing with no error
anywhere. "Nobody called today" is indistinguishable from a quiet Tuesday —
the same silent-failure shape as a fetch loop that dies while still reporting
healthy. With several lines it is every business's inbound at once.

Balance is a **capability, not a provider feature**: the same optional-interface
shape as the voice backends, so prepaid providers implement it and invoiced
ones cleanly say they have none. Ties to #5.

- [ ] `doorman balance` prints it; exit non-zero below a threshold so cron can
      use it directly.
- [ ] A provider without the capability says so, rather than reporting zero.
- [ ] Exposed as a gauge for §4 rather than doorman growing SMTP, webhooks and
      threshold config. Thresholds and delivery are operator decisions.
- [ ] **The alert path must not be the phone.** If the balance is zero an
      outbound call cannot report it, so the threshold fires well before empty
      and the notification leaves by another route.
- [ ] The API credential is documented as higher-privilege than the SIP
      sub-account password — it usually manages DIDs and sub-accounts — and the
      check is recommended to run wherever alerting already lives rather than
      on the Pi. RUNBOOK §1 already says not to put the main login there.

**Files:** `internal/provider` (new), `cmd/doorman`, `docs/RUNBOOK.md`.

---

## 3f. A schedule cannot span a weekend

**Why:** found while writing the solo-business example, which wanted "closed
Friday 17:30 until Monday 08:30" and could not say it.

`afterhours` takes one schedule id, and `Afterhours.Active` handles at most a
single crossing of midnight. The near-miss — `start = "17:30"`, `end = "17:29"`,
`days = ["FR","SA"]` — leaves a one-minute hole on Saturday and closes Monday
morning as well.

- [ ] A window may span more than one midnight, or an extension may name more
      than one schedule. Decide which; naming several is probably smaller and
      composes better with §3's house-scope variant.
- [ ] Absent the new form, behaviour is byte-identical.
- [ ] `doorman check`'s ACTIVE NOW marker stays correct across the longer span.

**Files:** `internal/policy/policy.go`, `internal/policy/policy_test.go`.

Two related limits the same pass surfaced are already tracked: schedules never
reach `[house]` (§3), and the allow-list is one class with one destination, so
"Grandma rings the kitchen, the school rings the office" needs §5.

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
