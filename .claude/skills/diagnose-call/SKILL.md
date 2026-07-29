---
description: Diagnose why a call did not behave as expected — wrong greeting, no ring, silence, or an unexpected dismissal. Use when the user reports a call symptom.
allowed-tools: Bash(ssh *), Bash(journalctl *), Bash(asterisk *), Bash(curl *), Bash(./bin/doorman *), Read, Grep, Glob
---

# Diagnose a call

Symptom: $ARGUMENTS

Work the ladder from `docs/RUNBOOK.md` section 3 **bottom-up**. Each rung
depends on the ones below it, so a failure low down explains everything above
it. Do not start theorising about the state machine before confirming the trunk
is registered.

## 1. Establish the layer

Map the symptom to a layer before touching anything:

| Symptom | Layer | Start at |
|---|---|---|
| Call never connects, caller hears carrier error | trunk / registration | rung 2 |
| Everyone gets "Good day", even with a correct PIN | DTMF | rung 2, `dtmf_mode` |
| Known caller gets the lobby | caller ID normalisation | `./bin/doorman e164 <raw value>` |
| Silence instead of a greeting | prompts / media | rung 6 |
| Greeting plays, house never rings | originate / handsets | rung 3 |
| Audio one way only | RTP / NAT | `rtp_symmetric`, `force_rport` |

## 2. Gather before hypothesising

Run `./scripts/smoke.sh` on the Pi first. It covers every rung and usually
names the problem outright.

Then pull the specific call:

```bash
journalctl -u doorman --since '30 minutes ago' -o cat | grep -A20 'callId'
```

Every log line carries `callId` and `caller`. Follow one call end to end and
note which state transitions actually happened versus which you expected. The
state machine is in `internal/lobby/session.go` — read `Run` and follow the
branch.

## 3. Confirm before fixing

State the diagnosis and the evidence for it before changing anything. Guessing
at SIP config and reloading repeatedly makes things worse and is hard to unwind.

If the fix is in Asterisk config, remember it lives in `/etc/asterisk/`, not
just the repo — change both, or the next deploy silently reverts it.

## 4. Add a regression test

If the cause was in Go, add a test before the fix. Caller-ID edge cases go in
`internal/policy/e164_test.go`; state machine bugs get a scenario in
`internal/lobby/session_test.go` driven through the fake-ARI harness — every
diagnosis that reached the state machine should leave a test behind.
