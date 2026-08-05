# s03 · Provider balance checking — development plan

Know the trunk is about to die before the phone stops ringing.

**Status:** planned. Nothing started.
Related: `docs/TASKS.md` §8, `docs/product-extensions.md` §10, issue #5,
and `.plans/s01-multiple-DIDs` (Phase 2 makes this per-trunk).

---

## Why

A prepaid trunk that reaches zero does not error. Inbound calls simply stop
arriving, and **"nobody called today" is indistinguishable from a quiet
Tuesday.** There is no log line, no alarm, and no symptom until someone
mentions they tried to reach you last week.

It is the same silent-failure shape as a mail fetch loop that dies while still
reporting healthy — the class of bug where the system looks fine and is not.

For a household that is embarrassing. For someone running several ventures on
one box it is every business's inbound at once, and they find out from a
customer.

---

## The two decisions that shape this

### Balance is a capability, not a provider feature

The same optional-interface shape `internal/voice` already uses for
`Lister` and `Estimator`:

```go
type Balance interface {
    Balance(ctx context.Context) (Amount, error)
}
```

Prepaid providers implement it. Invoiced ones do not, and `doorman balance`
says *"Flowroute is postpaid — no balance to report"* rather than printing a
zero that looks alarming or a blank that looks broken.

This is what keeps it from being a VoIP.ms feature wearing a general name, and
it ties directly to issue #5.

### The alert is a phone call

The obvious design is email, and the obvious objection to *that* is that if the
balance is zero you cannot afford to be told.

Both are wrong, because **an internal call never touches the trunk.** doorman
originates to `PJSIP/kitchen` over the LAN; no provider, no credit, no balance
required. A dead account can still ring the house to say it is dead.

That also dissolves the "don't build notification machinery" objection. Email
would be new machinery — SMTP, retries, configuration. Originating to a handset
and playing a prompt *is already the product*. There is nothing to build that
does not exist.

It gives the alert ladder in #15 a general principle worth stating once:

> **Escalate from free and local to paid and remote.** Rung one is a handset —
> free, works when broke. Rung two is a mobile over the PSTN, which costs money
> and needs the balance the alert is about.

Paging (`500`, auto-answer via `Alert-Info`) means it can simply speak out of
the kitchen without anyone picking up.

---

## What still emits a gauge

Not instead — as well, and for different jobs.

| | Phone call | Gauge |
|---|---|---|
| Answers | "act now" | "what has it been doing" |
| Delivery | rings a handset | Prometheus → whatever alerting exists |
| Thresholds | one, urgent | operator's choice, in their own stack |
| Needs | nothing new | TASKS §4 metrics endpoint |

doorman should not grow SMTP, webhooks, retry logic and threshold
configuration. Thresholds and delivery are operator decisions and there is
already a stack for them.

---

## Milestones

### M1 · The capability

**Build**

- `internal/provider` with the optional `Balance` interface.
- A VoIP.ms implementation. Their REST API, credentials separate from SIP.
- `doorman balance` prints a table; **exits non-zero below a threshold**, so
  cron is a one-liner and nobody needs a wrapper script.
- A provider without the capability says so.

**Verify:** a real account, a real number, and a postpaid provider reporting
"none" rather than zero.

### M2 · The phone call

**Build**

- Threshold in config; crossing it rings a configured handset or paging group.
- Composite prompt to read the amount: a pre-rendered WAV plus `digits:`.
  (**Second customer for that primitive** — the line-identity work needs it to
  read a number back. Build it once.)
- Repeat-suppression, so a low balance does not ring the kitchen hourly.
- Never on the inbound call path; this is a timer, not a call.

**Verify:** drop a test threshold above the real balance and confirm the
kitchen rings and says the number.

### M3 · The gauge

**Build**

- Balance as a gauge on the metrics endpoint (TASKS §4).
- **No account identifiers in labels** — same rule as caller IDs.

### M4 · Per trunk (needs s01 Phase 2)

Several trunks means several balances, and the output has to say *which*
account is low. Falls out of `trunks.toml` almost for free once it exists.

---

## Where the credential lives

**The sharp edge of this feature.** A provider's API key usually manages DIDs,
sub-accounts and billing — considerably more privilege than the SIP
sub-account password, which RUNBOOK §1 already says to keep off the Pi.

- Document it as higher-privilege, explicitly.
- Prefer running the check **wherever alerting already lives** rather than on
  the Pi. `doorman balance --json` from a cron job on another machine needs no
  daemon change and keeps the key off the phone.
- If the daemon does hold it, say so in the runbook and scope the key as far
  down as the provider allows.
- Never logged. `nologsecrets` should cover the new package the same way it
  covers everything else.

---

## Risks

| Risk | Mitigation |
|---|---|
| **The alert cannot fire because the trunk is dead** | Solved by design: the alert is an internal call and never touches a trunk |
| API credential on the Pi widens the blast radius | Run the check off-box; document the privilege difference |
| Vendor API changes shape | One small client per provider, same as the voice backends. A broken balance check must never affect calls |
| Threshold too low to be useful | Default well above zero. The point is warning, not a death rattle |
| Alert fatigue | Repeat-suppression, and one threshold rather than a ladder of them |

---

## Out of scope

Auto top-up · usage forecasting · per-line cost attribution · billing reports ·
anything that spends money without a human.

Auto top-up in particular is tempting and wrong: a loop that buys credit is a
loop that can buy a lot of credit.
