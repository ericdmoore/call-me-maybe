# s08 · Durable event journal and webhook doorbells

**Status: closed 2026-09-23 — archived.** M1–M4 and the compatibility half of
M6 are shipped and were verified live on the first customer's box (jepsen,
Ubuntu 26.04, Asterisk 22.5.2) on 2026-09-23:

- M1–M2: the journal, switched on with `EVENT_JOURNAL_PATH`, recorded
  `daemon.started`, `ari.connected`, and for a lobby call driven by an
  originated `Local` channel with no caller ID, `call.observed →
  admission.decided → call.handed_off → session.finished`.
- M4: CEL enabled by the procedure in `docs/events.md` exactly as written
  (spool initialised as `asterisk`, the two conf files, the `setfacl` grants —
  the permission question is answered there, and `acl` is a package the host
  needs). A dialplan-only echo call doorman never saw produced nine CEL rows
  and the journal ingested them as `channel.*` events after an honest
  `journal.note: cel-observation-started; prior system coverage unknown`.
  doorman restarted six seconds into a twenty-second call: source sequences
  10–18 journaled once each, no duplicates, no gaps, restart markers in place.
- M6: SQLite's online backup read back with the same journal identity,
  generation and watermark (in-place restore is unsupported by design and
  documented as such); the reference consumer caught up from nothing, did
  nothing on a second run, and after one more call processed only the new
  events, with `after`, `journal_id` and `generation` in its checkpoint.

Decisions that close the rest, recorded so they are not rediscovered:

- **M5 replicas: dropped.** Reopened as its own stream when a concrete
  destination exists. **Multiple doorbell targets: dropped** for the same
  reason. **Backup/restore generation renewal: documented, not managed** —
  the procedure in `events.md` is the feature.
- **The HTTP endpoint: not planned.** The CLI is the interface; a remote
  consumer runs the CLI over the tailnet.
- **The bullmoose consumer is bullmoose's backlog.** The in-repo reference
  consumer satisfies M6; the Bash glue "waiting for the Bullmoose CLI
  changes" is tracked there, not here.
- **`WEBHOOK_MODE=legacy` stays the default**; doorbell mode is opt-in. The
  household's Home Assistant uses legacy.
- **Journal and CEL default off until s09's `init` can set them up** — the
  ACLs and the spool initialisation are exactly the kind of root steps that
  belong to `sudo doorman init`, and the first customer's first calls were
  not journaled because nobody had turned it on. s09 carries that.
- One installer gap for the ledger: `install-scripts/` does not copy
  `examples/integrations/`, so the reference consumer was not on the box.
  Moot once s09 deletes the scripts and embeds the examples.

The design below is kept as written: it is the reasoning, and the parts that
were not built say why.

## What done looks like

Doorman records useful system events in a durable SQLite journal. A webhook
announces that committed events are available at a configured storage location;
it does not carry the call event itself. Bullmoose and other consumers retrieve
events after their own saved cursors, choosing their own batching, filtering,
retry, and processing logic. An offline consumer can catch up within retention.

The phone continues working when storage or consumers fail. Such failures are
visible, and the journal never claims to cover intervals it could not observe.
System-wide call coverage includes ordinary outbound calls that bypass Doorman,
with honest distinctions between call termination and leaving Doorman's control.

## Starting point

- `internal/calls` asynchronously appends completed-session summaries to
  `calls.jsonl`, with rotation and dropped/failed counters. This is not SQLite.
- `internal/notify` sends best-effort inbound `ringing` and `completed` payloads
  to one endpoint. It has no durable replay or consumer cursors.
- `internal/lobby/session.go` owns inbound decisions and teardown;
  `console.go` records outbound console handoff as `placed`.
- Ordinary outbound calls execute in Asterisk without Doorman. A console
  handoff does not reveal the eventual answer, duration, or termination.
- Contacts currently read local vCard exports. Remote fetching is separate work
  in s07; this journal supplies observations, not synchronous contact lookups.

## Decisions and invariants

Use one authoritative local SQLite journal, with a pure-Go driver. Validate
driver licensing, maintenance, Go compatibility, and CGO-disabled builds for
the host, Linux arm64, and Linux armv7 before selecting and pinning a version.
An initial candidate to evaluate is `modernc.org/sqlite`; selection is a phase-1
deliverable, not an unverified compatibility claim.

Generalise CLAUDE.md's deliberate “no database” choice: configuration remains
files, rate limits remain in memory, and historical events never decide call
admission. The database is observational state. Update that guidance when the
implementation lands. Preserve nonblocking ARI routing, teardown semantics,
last-good configuration, caller-ID protections, and the absolute PIN prohibition.

The journal is a structured system event history, not a dump of every ARI
message or arbitrary slog arguments. Keep ordinary diagnostic logging. Add
explicit schemas for operational events so credentials, DTMF, and PINs cannot
enter payloads through generic serialization.

Consumer batching is entirely consumer-owned. Doorman does not store consumer
business state, wait for consumer acknowledgements, or choose their batch sizes.

## Journal and read contract

Proposed logical tables (physical schema remains private):

- `journal_metadata`: schema version, stable journal identity, generation,
  retention floor, and lifecycle metadata.
- `events`: monotonically increasing, never-reused `sequence`, stable
  `event_id`, event `type` and `version`, occurrence and recording timestamps,
  source, optional call/line correlation, and a typed JSON payload.
- `delivery_state`: per configured doorbell target, the latest successfully
  announced sequence. This is notification progress, not consumer progress.

Allocate sequence numbers in the single writer's transaction order. They order
commits, not wall-clock occurrences across producers. Correlate channel legs
separately from logical calls; preserve Asterisk identities where available.
Do not treat multiple ringing legs as multiple human calls.

Expose JSON through the CLI, reading an internal versioned SQL view named
`public_events_v1`. Consumers need no direct SQL access; the database is private.
Its columns expose the event envelope above, payload JSON, and journal
identity/generation. Preserve v1 across internal migrations; incompatible changes
require a new view version. “Public” means a supported interface, not anonymous
access or automatic redaction. File access exposes full stored identities.

The agreed initial JSON dump interface is:

```sh
doorman events --json --after 1842 --limit 500 --eventType call.answered
```

- Query `public_events_v1`, ordered by ascending sequence.
- `--after` is exclusive; omitted or `0` starts at the earliest retained event.
  This explicit bootstrap request differs from an expired nonzero cursor.
  Consumers retain journal identity/generation alongside their cursor and verify
  it before processing a response.
- `--limit` bounds returned events; proposed default 100, range 1–10000.
- `--eventType` optionally selects one case-sensitive exact enum value. Omission
  includes all types; unknown values fail with a useful error. Parameterize SQL.
- Emit one JSON object containing an `events` array and pagination metadata.
  Diagnostics go to stderr; invalid arguments or read failures return nonzero.
  Payloads are JSON objects, not double-encoded strings.

Keep default CLI redaction and an explicit authorized full-identity option.
The internal view is not a public access endpoint. An authenticated HTTP endpoint can later
reuse the reader when needed; it is not required for the initial increment.
Call/line filters and an upper-watermark option are future extensions.
Responses include events, next cursor, earliest available position, committed
high watermark, and journal identity/generation. Serialize sequence cursors as
decimal strings on JSON interfaces to avoid JavaScript integer precision loss.

Specify filtered pagination explicitly: the next cursor is the last scanned
position, even when no records match; it must never skip an unreturned matching
event. Bound scan work and allow empty pages with forward progress. A fixed
upper watermark lets consumers finish a finite batch while new events arrive.

An expired cursor, wrong journal/generation, or cursor ahead of the source is
an explicit error, not a silently empty success. Consumers checkpoint only after
their own successful processing and use event IDs for idempotency. Exactly-once
external side effects are not promised.

## Storage locations

Start with the local journal exposed through the CLI or read endpoint. This
provides a usable durable location without introducing cloud credentials or a
network filesystem into the call path. The CLI reads `public_events_v1` using
short transactions. Consumers store checkpoints elsewhere and depend on the JSON
contract, not private table layouts or SQL permissions.

Add configured replica destinations in a separate phase using a narrow sink
interface. The first concrete destination should be selected from deployment
needs; do not invent several cloud adapters up front. Replication reads committed
events from the primary and persists independent progress. Replicas preserve
journal identity, generation, event IDs, and sequences. Different journals are
never interchangeable merely because their sequence numbers match.

Each location reports its own durable high watermark and retention floor. A
doorbell advertises only locations that can already serve the announced range;
a lagging replica cannot inherit the primary's availability claim. Replica
failure never prevents primary commits. If replication falls behind primary
retention, report the gap and require explicit reseeding. Slow replicas must not
silently defeat the primary's disk bound.

Backups must include committed WAL contents through a supported SQLite backup
procedure, not a live copy of the main file alone. Restoring an older snapshot
or recreating a journal changes its generation before new writes, preventing
old cursors from silently skipping reused positions.

## Doorbell contract

Proposed payload, deliberately free of phone numbers and storage credentials:

```json
{
  "type": "journal.available",
  "version": 1,
  "journal_id": "house-phone",
  "generation": "opaque-generation-id",
  "locations": [{"id": "primary", "through": "1842"}]
}
```

Location IDs resolve through consumer configuration; do not ask consumers to
follow arbitrary URLs from notifications. Publish a location descriptor through
the authenticated read service for verification and watermark discovery.

Notify only after commit at the advertised location. Coalesce pending changes
to the newest watermark; duplicate, delayed, or out-of-order doorbells are safe.
Use bounded requests, backoff with jitter, and persisted notification progress.
On restart, compare that progress with committed availability and ring again as
needed. A crash after delivery but before progress persistence yields a harmless
duplicate. A successful HTTP response acknowledges the doorbell, not consumption.

Consumers check on startup and periodically as well as on doorbells, so a lost
notification or a receiver crash after acknowledgement cannot strand events.
Provide a reference consumer demonstrating this and checkpoint-after-processing.

## Durability, failure, and retention

Use one background writer and a bounded producer queue. Commit promptly; any
writer batching is an internal tuning choice unrelated to consumer batching.
Evaluate WAL mode with FULL synchronous commits, bounded busy waits, short read
transactions, and checkpoint policy on the deployment host. Document the tested durability
boundary: committed transactions survive within SQLite/storage guarantees;
events still queued in RAM can be lost on a process crash or power loss.

Queue saturation, disk-full, corruption, and migration/open errors must not delay
calls. Expose degradation through stderr/slog and independent health counters,
with bounded reopen/retry behaviour. Never auto-delete a corrupt database or
silently create a replacement under the old identity. When writes recover,
record a coverage-gap event with known counts and intervals; after an unclean
restart, mark uncertain coverage without claiming an exact lost-event count.
Sequence continuity alone does not prove complete observation.

Stop event producers before draining the writer on graceful shutdown; bound
shutdown time. A storage failure leaves telephony available but reports journal
health separately. Explicit validation commands still fail for invalid journal
configuration. Do not recursively journal failures of the journal itself.

Apply configurable age and disk budgets independent of consumer cursors. Specify
defaults after measuring representative event volume. Delete in bounded chunks;
account for database pages, WAL growth, free space, and reader-held snapshots.
Retention updates its floor atomically with deletion. Test the full disk bound,
not just a row-count proxy. Consumers are told when they have missed retention.

Protect the database directory and SQLite sidecars, not just the main file.
Redact numbers by default on exported reads; full identity requires an explicit
authorized read scope. Doorbells contain no call data. Keep the read service
loopback-bound by default; remote access needs an authenticated protected
transport. Do not widen ARI's bind or reuse its credentials.

## Implementation phases and acceptance criteria

### M1 · Storage foundation and read interface

Add `internal/events` with typed envelopes, transactional writer, migrations,
retention, health, and paginated reader. Select the pure-Go driver through the
build spike above. Add `public_events_v1` and the agreed CLI JSON dump; defer the
HTTP service until a remote consumer requires it.

Done when restart preserves committed events and identity; sequence allocation,
view/CLI parity, enum validation, limit boundaries, JSON output shape,
filtered pagination, expiry, generation mismatch, concurrent readers, and bounded
retention have tests. Kill-process tests demonstrate commit/replay behaviour and
are explicitly distinguished from real power-loss testing. Database errors do
not block fake-ARI call handling. Migration failure preserves the original data.

### M2 · Useful Doorman events

Instrument typed producer interfaces in the lobby, console, configuration and
contacts reload paths, daemon lifecycle, and ARI connection lifecycle. Proposed
vocabulary: `call.observed`, `admission.decided`, `ring.started`,
`ring.stage_finished`, `call.answered`, `call.handed_off`, `session.finished`,
`config.reloaded`, `config.reload_failed`, `contacts.refreshed`,
`contacts.refresh_failed`, `ari.connected`, `ari.disconnected`,
`daemon.started`, `daemon.stopping`, and `journal.coverage_gap`.

Specify payloads and event ownership before adding emission sites. Existing call
summaries can be included in `session.finished`. Handoff to voicemail is not
proof a voicemail was recorded, and session finish is not proof the call ended.

Done when fake-ARI scenarios verify identity and meaningful event ordering for
known callers, blocked callers, PIN success/failure, ladder exhaustion, transfers,
voicemail handoff, and console placement. No duplicate logical events or entered
digits; secret lint and explicit payload tests cover all new producers.

### M3 · Doorbell delivery and consumer example

Implement commit-triggered doorbells and persistent per-target announcement
progress. Support independently configured consumers without requiring a broker.
Add a runnable sample consumer showing small and large batches, filtering,
deduplication, checkpoint persistence, startup catch-up, and periodic checks.

Done when tests cover commit-before-notify, coalescing, duplicate delivery,
receiver outage, crash windows around acknowledgement, and independent cursors.
A consumer offline during calls catches up entirely from storage after restart.

### M4 · Calls outside Doorman

Run a bounded design spike against the deployed Asterisk version: assess CEL,
CDR, and available event interfaces for durable observation of ordinary outbound
calls, answer/end, transfers, and voicemail after handoff. Consult current
official Asterisk documentation here; do not assume ARI subscriptions cover it.
Prefer a recoverable local Asterisk event source where available, and explicitly
describe coverage lost during observer outages if only a live feed is chosen.

Deliver the selected adapter, source deduplication, logical-call/leg correlation,
and truthful `call.ended` events. Its replay cursor must advance atomically with
journal ingestion so crashes cannot silently skip source records. A source
without stable identities requires a documented deduplication strategy.

Done when fixture-driven tests cover ordinary outbound and emergency routes,
console handoff, transfers, inbound failover, repeated source delivery, and
adapter restart. Manually verify on Asterisk using non-emergency test calls;
CI never needs a live trunk or an actual emergency call. No call routing change
is required merely to make a call observable. Report unsupported coverage plainly.

### M5 · Additional durable locations

Implement the first required replica sink and location descriptors. Drive it
from the journal, not a second producer write. Define authentication, retention,
reseed, and backup procedures for that destination.

Done when a consumer can resume against a replica using the same cursor after
checking identity and availability. Tests cover replica lag, outages, retention
overrun, restart, and doorbells never advertising uncommitted replica data.

### M6 · Compatibility, rollout, and documentation

Keep the existing webhook payload mode as an explicit legacy option during
migration; doorbell mode is a new versioned contract, not a silent payload swap.
Keep `doorman calls` behaviour and JSONL support while the journal is introduced.
After parity tests, allow call summaries to be projected from journal events;
do not introduce a second authoritative history. Legacy JSONL imports, if offered,
are explicit, repeat-safe, and labelled as historical summaries without invented
intermediate events. Document differences in retention between old and new logs.

Add configuration through the existing config/schema machinery: journal path
and budgets, read service settings, doorbell targets/mode, and later replica
locations. Exact key names and file ownership are implementation deliverables;
none of the proposed names in this document are currently accepted settings.
Validate references and modes with `doorman check`, including doorbell mode
without readable durable storage. Report resolved locations and degraded health
without printing tokens or credential-bearing URLs.

Update `CLAUDE.md`, `llms.txt`, man page, schema, env examples, architecture,
runbook, and webhook examples together. Promote milestones to `docs/TASKS.md`
when scheduled; this document records design, not backlog priority.

Done when `make check`, appropriate race/coverage checks, and CGO-disabled
target builds pass. Include an operator migration/rollback exercise preserving
the journal and legacy integrations, plus a Bullmoose-style end-to-end test:
commit calls, lose doorbells, restart the consumer, process batches, and resume
without skipped committed events.

## Alternatives rejected

- **Webhook as authoritative event transport:** a missed delivery becomes missing
  history and forces every receiver to solve ingestion durability immediately.
- **Consumer cursors stored in Doorman:** couples storage retention and phone
  operation to arbitrary downstream processing. Consumers own their progress.
- **Remote storage on the call path:** remote outages must not delay telephony.
- **Raw SQLite files shared over a network filesystem:** use supported readers
  and replication, rather than make filesystem locking the integration protocol.
- **Full event sourcing of call control:** this work records observations; it
  does not rebuild admission or active sessions from historical events.
- **All slog/ARI data persisted indiscriminately:** noisy, unstable, and capable
  of leaking secrets. Use explicit useful event schemas.
- **Exactly-once processing claims:** Doorman cannot transactionally commit a
  consumer's external effects. Stable IDs and resumable reads enable idempotency.

## Rollout order

Ship M1–M3 and the relevant M6 compatibility work as the first usable increment.
M4 is required before claiming whole-phone-system call coverage. M5 expands the
configured durable locations once a concrete additional destination is selected.
Online address-book synchronization remains separate s07 work; a consumer can
enrich its own history immediately without changing admission policy.
