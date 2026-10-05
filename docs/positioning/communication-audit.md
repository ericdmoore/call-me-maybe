# Site versus repository: communication audit

Baseline: October 5, 2026; commit `29adb13`. Paths below are repository-relative.
Live pages: [home](https://callmemaybe.cc/), [providers](https://callmemaybe.cc/providers/).

## What is missing, and what is overstated

| Capability / claim | Baseline website | Repository evidence | Recommended outcome |
| --- | --- | --- | --- |
| Independent family calling | Hero says “programmable home phone”; first interaction teaches incoming screening | `asterisk/extensions.conf` ordinary outbound dialplan; `examples/scenarios/family-line/` | Site update: lead with calling friends, then explain ownership and setup |
| Lobby, known callers and PIN routing | Prominent; “No spam callers” is absolute | `internal/lobby/session.go` admission branches; caller ID and a valid PIN are not verified human identity | Site update: describe screening, acknowledge spoofable caller ID, remove absolute guarantee |
| Default versus configurable silence | Homepage describes dismissal as universal | `[line] on_no_input` in schema; lobby supports voicemail and ring-house | Site update: label dismissal as the default |
| Extension quiet hours | Says a “line does not ring at all” and promises voicemail | `internal/lobby/session.go` afterhours branch; `internal/policy/policy.go` requires voicemail or afterhours routing | Site update: explain extension scope and mailbox dependency |
| Handset bedtime / curfew | Missing from feature index and homepage | `internal/render/render.go` curfew contexts; `internal/lobby/session.go` ring gate; `cmd/doorman` curfew sweep; RUNBOOK Bedtime | Site update: new page, including outgoing restriction, reminder/911 exceptions and minute-resolution sweep |
| Ringer ladders, paging, parking, conference, lamps, voicemail and hold music | Already explained, with individual pages | `asterisk/extensions.conf`, `internal/lobby`, `internal/render`, RUNBOOK | Keep; move technical mechanism below family outcome |
| Home Assistant | Has a feature page but absent from homepage feature cards | `internal/notify`; RUNBOOK event webhook and Assist | Site update: surface existing page; distinguish webhook configuration from built-in device support |
| Contacts and directories | Missing from feature collection | `internal/contacts`, `internal/inbox`, `internal/provision`; RUNBOOK shared contacts | Site update: vCard imports and directory sharing, explicitly separate from admission and outgoing permission |
| Multiple lines/providers and outgoing identity | Not in headline feature set | `internal/render/trunks.go`, `cmd/doorman/outbound.go`, RUNBOOK Add a second provider | Follow-up site page: home/office separation; do not market “any carrier works” |
| Low-credit alerts | Provider page warns of empty balances without showing product response | `cmd/doorman/balance.go`, `internal/provider/voipms.go`; RUNBOOK balance timer | Site update: explain VoIP.ms balance checks and handset alerts; do not generalize to every provider |
| Flowroute billing | Site says postpaid and no risk of empty balance | `internal/provider/flowroute.go` copied that assumption into runtime behavior | Site correction now; priority runtime roadmap nudge below |
| Scheduled reminders | Absent | `internal/reminder`, `internal/render/reminders_test.go`; RUNBOOK Scheduled calls | Follow-up feature page after documented real-handset trial; note curfew exception |
| Text commands, shared cards, daily digest | Absent from main site; some runbook prose still calls shipped functionality planned | `edge/`, `internal/inbox`, `cmd/doorman/digest.go`; RUNBOOK text/digest sections | Follow-up setup guide; disclose carrier + edge dependencies, avoid implying standalone SMS app |
| Easy setup / live changes | “On a Pi that is the whole rung”; “edits go live within a second” | Installer does not provision the full system; render/reload needed for generated config | Site update: distinguish binary installation, policy reload and full phone setup |
| Costs | No provider chart; homepage “a couple of dollars” | Provider rates have separate number, minutes, E911 and tax components | Site update: dated price chart, sources, setup charges and explicit usage examples |
| UTM measurement | Existing helper uses source/medium/campaign/content; page says tags show choices | No first-party click or conversion measurement identified in site source | Retain tags, standardize medium to referral, explain attribution is visible to destination; no inferred conversions |

## Roadmap nudges (proposals, not implemented features)

### P1 — Correct Flowroute balance classification

**Trigger:** `doorman balance` with a Flowroute trunk can report postpaid and
skip checking funds. Provider documentation describes prepaid balances and
monthly deductions. Evidence: `internal/provider/flowroute.go`,
`internal/provider/provider.go`, `cmd/doorman/balance.go`, and the sources in
[provider research](provider-research.md).

**Acceptance:** represent prepaid/account-specific billing truthfully; a
prepaid account with no balance client must report unsupported/unverified and
exit nonzero, not healthy or postpaid. Add regression coverage for table,
JSON and alerting behavior. Audit docs and tests that encode the same claim.
Keep credentials out of daemon/call paths. Site now advises provider alerts
until this runtime behavior is corrected. This review leaves Go behavior
unchanged so this operational fix can be tested and reviewed separately.

### P1 — Decide whether approved-only outgoing calling is a product goal

**Trigger:** families comparing Tin Can may assume “house rules” means an
approved list in both directions. `asterisk/extensions.conf` accepts ordinary
NANP destinations; the people list gates admission, not outbound calls.

**Acceptance if adopted:** opt-in per-handset destination permissions, clear
adult defaults, emergency dialing preserved, coverage for direct dial and
`*4` console paths, explicit denial feedback and migration behavior. Until
then, every comparison must name this difference. Do not disguise it as copy.

### P2 — Publish a carrier validation matrix

**Trigger:** registration support is necessary but insufficient evidence of
interoperability. The site previously collapsed these two claims.

**Acceptance:** provider + account type + Asterisk version + verification date;
inbound routing, two-way audio, DTMF, outbound caller ID, NAT registration
recovery, and provider-approved emergency testing. Start with the existing
VoIP.ms setup. Link to/extend issue #5 rather than creating a duplicate.

### P2 — Make first-family setup reproducible

**Trigger:** a parent can understand the benefit but not estimate the setup
work. Existing `init`, `check`, provisioning and installer tools are useful
building blocks, not yet evidence of a turnkey parent app.

**Acceptance:** one documented supported kit and provider path, measured setup
time from a fresh device, first incoming/outgoing call verification, a second
adult able to recover it using the runbook, and honest maintenance ownership.
A parent UI is a possible follow-up, not a capability this audit found.

### P2 — Keep public claims current

**Acceptance:** provider rows retain checked dates and primary sources; review
before publishing a price change and at a chosen maintenance interval. Add a
release checklist asking which family-facing features need site pages. Track
live handset verification separately from passing software tests. No recurring
automation has been scheduled by this review.

## Site updates made in this change

Homepage family framing and fit criteria; accurate screening/default/reload
copy; bedtime and contacts feature pages; corrected quiet-hours explanation;
Home Assistant discovery; provider chart and light-use examples; corrected
Flowroute advice; SIP-versus-project validation distinction; referral UTMs
on chart, card and source links. Comparison articles and stories remain drafts.
