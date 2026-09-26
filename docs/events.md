# Durable events and webhook doorbells

Doorman can keep a local SQLite journal of call decisions and operational
observations. Consumers ask the CLI for JSON pages; they do not need SQL access,
a database port, or an HTTP service. The `public_events_v1` SQL view is internal
implementation machinery behind that versioned output contract.

## Enable

Set these in the daemon's environment (normally its `.env` EnvironmentFile):

```sh
EVENT_JOURNAL_PATH=/var/lib/doorman/journal/events.db
EVENT_JOURNAL_MAX_BYTES=67108864
EVENT_JOURNAL_MAX_EVENTS=100000
EVENT_JOURNAL_MAX_AGE_DAYS=90
```

The dedicated journal directory must be 0700 and owned by the daemon user. The
writer creates it privately if absent, and refuses an existing shared directory
rather than changing somebody else's permissions. The database is 0600; keeping
the directory private also protects its WAL, shared-memory file, and owner lock.
Only one Doorman writer may own a journal. Run `doorman check` to validate tuning,
then restart the daemon. Empty `EVENT_JOURNAL_PATH` disables the feature.

This is observational storage. It never supplies admission decisions, PINs, or
rate-limit state. When the journal is enabled it is the source for `doorman calls`; the daemon
stops writing the legacy `CALL_LOG_PATH` JSONL file. Existing files remain
readable explicitly. There is no automatic historical import or silent fallback
to JSONL when journal storage fails.

## Upgrade and validation

Reinstall `scripts/doorman.service` and run `systemctl daemon-reload` when
upgrading an older unit: `StateDirectory=doorman` grants journal writes under
`ProtectSystem=strict`. systemd reapplies `StateDirectoryMode=0700` on each start;
other programs writing under `/var/lib/doorman` must use compatible ownership or
a separate directory. Run `sudo -u doorman doorman check` from the configured
working directory. It reports environment/storage issues after policy validation,
including the effective page budget and whether a writer owns the journal.
`check --policy-only` validates example policies independently of local service
credentials, paths, or storage; CI and the pre-push hook use it.

When both history paths are set, startup explicitly reports that the journal
replaces JSONL writes. A failed initial journal open refuses startup, rather
than silently disabling the only working history sink.

## Read a batch

```sh
doorman events --json --after 1842 --limit 500 --eventType call.answered
```

`--path` overrides `EVENT_JOURNAL_PATH`; otherwise the command reads the process
environment and then `.env`. It needs neither ARI credentials nor a running daemon.

- `--after` is exclusive; omitted or `0` explicitly starts with the oldest retained
  event. Sequence order is commit order, not occurrence-time order.
- `--through` fixes an inclusive upper cursor for a batch. Omit it on the first
  page, then pass the returned `through` on subsequent pages; new commits do not
  extend that batch. An expired or future bound fails. Explicit `0` is an empty batch.
- `--direction inbound|outbound`, `--line name`, and `--call full-channel-id`
  can be combined with the event-type filter. Direction selects call records,
  excluding system events. `--line default` selects the default line.
- `--limit` defaults to 100 and accepts 1–10000 returned events.
- `--eventType` is one case-sensitive enum value. Omit it for every event type.
  Unknown values fail. `--help` lists the current vocabulary.
- Numbers are redacted unless `--no-redact` is explicitly supplied. PINs and
  entered digits are never recorded. Full accepted outbound destinations are
  phone numbers, not credentials, and receive the same redaction as caller IDs.
- `--journal` and `--generation` assert the expected identity saved with a cursor.

Output is one JSON object, not JSON Lines:

```json
{
  "journal_id": "opaque-journal-id",
  "generation": "opaque-generation-id",
  "events": [],
  "next_cursor": "1842",
  "earliest_available": "1200",
  "high_watermark": "1842",
  "through": "1842",
  "retention_floor": "1199"
}
```

Each event has `sequence`, `event_id`, `type`, `version`, `occurred_at`,
`recorded_at`, `source`, optional `call_id` and `line`, and an object `payload`.
Call payloads include a snapshot under `record`; its outcome is only final on
`session.finished`, `call.finished`, or a handoff. `call_id` is the full Asterisk session channel ID;
`record.id` retains the old call log's short diagnostic handle. Sequence numbers
and cursors are decimal **strings**, so JavaScript consumers retain precision.

Readers use a bounded snapshot. A filtered empty page can still advance
`next_cursor`: up to 10000 rows are scanned per invocation. Keep reading until
that cursor reaches `through`, passing that same bound on each invocation. Save the returned cursor, not
an offset calculated from the number of returned records. A nonzero cursor behind
`retention_floor`, a future cursor, or an asserted identity mismatch fails with
nonzero exit status and a stderr diagnostic; no success JSON is printed.

Each consumer owns its checkpoint, batch size, filters, and retry schedule. Save
progress only after processing succeeds, and make effects idempotent using
`event_id`: crashing between the effect and the checkpoint can replay an event.
Use a separate checkpoint when changing filters. Doorman does not keep consumer
acknowledgements or hold retention open for slow consumers.

The runnable [Python example](../examples/integrations/consume-events.py) needs
only Python's standard library and the Doorman executable:

```sh
python3 examples/integrations/consume-events.py \
  --doorman ./bin/doorman --path /var/lib/doorman/journal/events.db \
  --checkpoint /path/to/bullmoose/checkpoint.json --limit 500 --once
```

Its processing action prints each event; replace that function with your own
idempotent operation. `--once` catches up through a captured watermark and exits.
Without it, the example checks periodically. Serialize invocations sharing a
checkpoint. A remote consumer can invoke the CLI over an existing SSH connection;
Doorman introduces no public storage endpoint.

## Call summaries

```sh
doorman calls --json --direction inbound --line default
doorman calls --since 7d --outcome answered
doorman calls --source jsonl --path /var/lib/doorman/calls.jsonl
```

`doorman calls` projects `session.finished` and CEL-derived `call.finished`
records through the internal `public_calls_v1` view, returning one summary per
full root channel ID. A Doorman summary retains its line, known name, admission
details and outcome (including console `placed`). Outbound summaries take their
duration from CEL once completion arrives, regardless of ingestion order. CEL
bridge/answer/end observations remain separately queryable; they do not replace
the console outcome or break `--line biz --outcome placed` filters. Repeated summaries
of the same kind select the latest. Active calls do not yet have
a summary. The existing table and JSON Lines formats, redaction, `--caller`,
`--since`, `--outcome`, `--line`, `--direction`, and `-n` filters are preserved.
The newest matching records are displayed oldest-first.

`--source auto` (the default) prefers `EVENT_JOURNAL_PATH`, otherwise
`CALL_LOG_PATH`. With an explicit `--path`, auto detects SQLite versus JSONL.
Use `--source journal|jsonl` to select a source explicitly. Missing or corrupt
journal storage fails visibly; it does not quietly show stale JSONL history.
Retention and retained coverage-gap notices produce warnings on stderr, leaving
JSON stdout usable by scripts. Summaries share journal retention; they are not
a separate permanent call-history table. Use `doorman events` and its cursor
contract for reliable consumer replay.

The writer migrates older journal schemas transactionally without changing
journal identity, sequence positions, or doorbell progress. Read-only CLI queries
can read the original schema without migrating it.

## Ring the doorbell

```sh
WEBHOOK_MODE=doorbell
WEBHOOK_URL=https://consumer.example/phone-doorbell
WEBHOOK_TOKEN=your-secret
WEBHOOK_TIMEOUT_MS=3000
```

`WEBHOOK_MODE=legacy` is the default and preserves the existing inbound
`ringing`/`completed` payloads. Changing modes is deliberate: update the receiver
before switching. Doorbell mode requires a configured journal and uses the same
single webhook endpoint. Fan-out to additional destinations is future work.

A doorbell contains only availability metadata:

```json
{
  "type": "journal.available",
  "version": 1,
  "journal_id": "opaque-journal-id",
  "generation": "opaque-generation-id",
  "locations": [{"id": "primary", "through": "1842"}]
}
```

`primary` identifies this local journal. The receiver maps it to its configured
CLI invocation; neither credentials nor phone numbers travel in the notification.
The notifier observes committed watermarks, coalesces pending changes, persists
successful announcement progress, and retries failures with bounded exponential
backoff and jitter. Slow webhook delivery never holds a database transaction or
blocks the writer. Redirects are not followed.

A 2xx response acknowledges receipt of the doorbell, **not** event processing.
Duplicate, delayed, and out-of-order notifications are harmless. Check on consumer
startup and periodically even when receiving doorbells: a receiver can crash
after acknowledging one. On recovery, catch up from your own cursor through the
CLI. Changing the webhook URL starts fresh announcement progress for that target.

## Current coverage

The vocabulary includes `call.observed`, `admission.decided`, `ring.started`,
`ring.stage_finished`, `call.answered`, `call.handed_off`, `session.finished`,
`config.reloaded`, `config.reload_failed`, `contacts.refreshed`,
`contacts.refresh_failed`, `ari.connected`, `ari.disconnected`, `daemon.started`,
`daemon.stopping`, and `journal.coverage_gap`.

A `call.answered` event means an originated handset leg answered, not a guarantee
that bridging succeeded. Ring-stage durations and outcomes are recorded as stages
finish. Admission events report the reason and credential verdict, never digits.
Contact events describe the startup snapshot; this does not add remote contact
fetching or a new contacts reload loop.

Without `CEL_SPOOL_PATH`, coverage remains Doorman's own observations: ordinary
outbound calls bypass it and console records describe placement/handoff only.
With the CEL adapter below, channel start, answer, hangup, end, bridge entry/exit,
transfer, and linked-ID retirement are available even when Doorman was offline.
The source vocabulary adds `channel.started`, `channel.answered`,
`channel.hungup`, `channel.ended`, `channel.bridge_entered`,
`channel.bridge_exited`, `channel.transfer`, `channel.linked_ended`,
`channel.dial_started`, `channel.application`, and derived `call.finished`.

## Asterisk-wide capture (CEL)

Asterisk writes its own local SQLite CEL spool. Doorman polls bounded read-only
snapshots and commits observations, correlation state, and the source cursor in
one journal transaction. Restarting either side does not require an in-memory
call map; a failed journal write leaves the source cursor unchanged for retry.
No routing or emergency dialplan changes are required.

The supplied configuration targets Asterisk 20/22's `cel_sqlite3_custom` backend.
The [upstream backend](https://github.com/asterisk/asterisk/blob/20.7.0/cel/cel_sqlite3_custom.c)
writes `master.db` under Asterisk's `astlogdir` (usually
`/var/log/asterisk`). Confirm the installed module is available before enabling:

```sh
sudo asterisk -rx 'module show like cel_sqlite3_custom'
```

**On a host prepared by `install-scripts/`, all of this is done:** the two
configuration files are installed (the distro samples kept as `.distro`), the
spool is initialised once as the asterisk user, the service account is granted
read access, and `doorman init` writes `CEL_SPOOL_PATH` and
`EVENT_JOURNAL_PATH` into `.env` because the places exist. A rerun is
idempotent and repairs a host prepared before 2026-09-25. `scripts/smoke.sh`
reports the capture state as its own rung. What follows is the same procedure
by hand, for a host prepared some other way.

During a maintenance window, stop Asterisk, initialise the spool, then install
the configuration files. Substitute the actual `astlogdir` and service user.
The `sqlite3` executable is an operator setup tool; Doorman remains a static,
pure-Go binary.

```sh
sudo systemctl stop asterisk
sudo -u asterisk sh -c 'umask 077; sqlite3 /var/log/asterisk/master.db' < scripts/cel-spool.sql
sudo install -m 0644 asterisk/cel.conf /etc/asterisk/cel.conf
sudo install -m 0644 asterisk/cel_sqlite3_custom.conf /etc/asterisk/cel_sqlite3_custom.conf
# Allow Doorman to traverse the directory and read the spool and its sidecars.
# Default ACLs cover WAL/SHM files recreated by Asterisk after restart.
sudo setfacl -m u:doorman:rx,d:u:doorman:rX /var/log/asterisk
sudo setfacl -m u:doorman:r /var/log/asterisk/master.db
sudo systemctl start asterisk
sudo asterisk -rx 'module load cel_sqlite3_custom.so'
sudo asterisk -rx 'cel show status'
```

If the backend ran before initialisation, it may have created `doorman_cel`
without AUTOINCREMENT. Re-running the initialiser does not repair that table.
Stop Asterisk and preserve the database and its sidecars first. Either initialise
a fresh spool with a new source identity, or archive/rename the incorrect table,
remove its old retention trigger and `doorman_cel_meta` row, then re-run
`scripts/cel-spool.sql` to create the correct table and a new identity. Do not
remove other applications' tables from a shared `master.db`.

If `master.db-wal` or `master.db-shm` already existed before setting the default
ACL, grant the same read ACL to those files. Protect the spool and Asterisk logs
from other users: they contain full caller IDs. The mapping deliberately omits
application arguments, user fields, arbitrary extensions, channel names, and
DTMF. Its destination expression accepts complete NANP numbers or `911` only;
Doorman independently validates destinations before classifying outbound calls.
Do not replace it with the upstream sample's unrestricted `appdata`/`exten` mapping.
The filtered destination expression also requires `func_uri` (URIDECODE) and
`func_strings` (FILTER). URIDECODE supplies FILTER's comma after the legacy CEL
backend splits columns, and FILTER removes colons before IF evaluates its branches.
The config files are examples to merge with an existing CEL deployment, not a
reason to overwrite another application's CEL settings blindly.

Enable the adapter alongside the journal, then validate as the service user:

```sh
# .env
CEL_SPOOL_PATH=/var/log/asterisk/master.db
EVENT_JOURNAL_PATH=/var/lib/doorman/journal/events.db

sudo -u doorman /opt/call-me-maybe/bin/doorman check
sudo systemctl restart doorman
```

The supplied systemd unit's `StateDirectory=doorman` makes `/var/lib/doorman`
writable under its filesystem sandbox. CEL stays read-only. No SQLite or HTTP
service is exposed. `doorman check` verifies the spool schema and access; it
cannot prove that Asterisk's CEL backend is active. Check `cel show status` and
place a non-emergency test call before relying on coverage.

### Event and summary meaning

CEL events have `source: asterisk.cel`, the full channel ID in `call_id`, and
`payload.channel` containing `source_id`, `source_sequence`, and `linked_id`.
Linked IDs correlate channel legs; transfers can merge or change those groups.
`channel.ended` means that particular channel ended. `channel.linked_ended`
means Asterisk retired that linked ID; neither a transfer nor a Doorman handoff
is itself a hangup. Transfer observations deliberately omit raw transfer targets
and CEL extra data.

Only recognised root dialplan channels produce `call.finished` summaries:
`inbound-trunk`, `inbound-fallback`, `from-trunk-*`, `from-*`, `cmm-line-*`, and validated
Dial attempts in `internal`, `cmm-outbound`, `outbound-console`, or
`cmm-emergency`. Root classification requires `uniqueid == linkedid`; outbound trunk legs can
have an inbound endpoint context and must not become phantom inbound calls.
Handset/trunk legs remain individual channel events, avoiding
one history row for every ringing handset. Custom dialplan contexts need an
explicit adapter extension; they still produce channel events.

A channel answering is not proof a human answered: the inbound lobby answers
before greeting. An outbound root entering a bridge after Dial produces an
`answered` summary. Otherwise CEL reports `ended`, with no invented busy,
no-answer, voicemail-recording, or hangup cause. Inbound CEL summaries always
use `ended`; Doorman's own summary supplies its richer outcome when available.
`ms` is channel lifetime for inbound CEL summaries and time since the first
recognised Dial attempt for outbound ones. It is not billed talk time. Outbound
CEL-only outbound line attribution is `unknown`; the adapter does not infer a
business line from caller ID. Unknown attribution alone does not add a LINE
column to the table; named Doorman lines still do. Unclassified leg events have no direction and are excluded by
`--direction` filters. Use `linked_id` to correlate them with their root.

### Recovery and bounds

The spool retains 100,000 source rows using an insert trigger and monotonic
`AUTOINCREMENT` IDs; its storage is separate from the journal's budget. Monitor
both filesystems. The adapter reads at most 128 rows per second, with a 750 ms
batch deadline; this is sized for a home phone, not a high-volume PBX. Consumers
still use the journal cursor, never the CEL source row number.

A missed source range emits a coverage-gap notice and clears uncertain channel
state. Starting observation records an informational note that earlier coverage is unknown.
Correlations are capped at 4096 channels and expire after seven days with a gap
notice, never a fabricated hangup. Missing/unreadable sources pause ingestion
and report an informational replay-pending note; source rows remain replayable within spool retention.
Malformed rows are reported as gaps and skipped without persisting their content.
Asterisk backend failures before a row is committed cannot be recovered by
Doorman: inspect Asterisk's own logs and backend status.

Do not truncate or restore a spool under its existing source identity. The
adapter checks for rewinds and changed checkpoint rows and stops on conflict;
it cannot detect every possible history rewrite. Preserve the old database and
initialise a genuinely new spool with `scripts/cel-spool.sql` when replacing it.
A new source identity records an explicit gap, discards old channel correlations,
and begins replay of the new source. Rerunning the setup SQL on an intact spool
preserves its identity and data.

### Deployment verification

On the Linux x86 host, use non-emergency test calls to verify an inbound call,
an ordinary outbound answered call, an unanswered call, and a console handoff.
Compare `doorman events --json --eventType channel.ended` with `doorman calls`.
Exercise a transfer and voicemail handoff: the journal must retain observations
after `session.finished`, without claiming a message was recorded. Stop Doorman,
make an ordinary outbound call, restart it, and confirm catch-up produces one
summary. Stop the webhook receiver temporarily and verify its consumer can catch
up from its own cursor when it returns. Emergency routing is covered by fixtures;
never place an emergency call as a software test.

The implementation is fixture-tested; live validation of the deployed Asterisk
version and the hardware remains part of this deployment checklist. Remote
durable replicas and additional location types remain future work.

## Failure, retention, and operations

One background writer uses pure-Go SQLite, WAL mode, and FULL synchronous
commits. Durable means **committed**, subject to the storage device honouring
flushes. A bounded RAM queue keeps storage off the call path; events still in
that queue can be lost on abrupt termination. No software test here simulates
actual hardware power loss.

An explicitly configured journal must open successfully at startup; failures
report the actual cause and stop startup. Once running, a full queue or failed
write never delays calls: the daemon reports degraded observation and retries.
Startup errors close the journal cleanly, and ARI shutdown joins the disconnect
callback before the journal drains. Recovered writes include a coverage-gap
record with the known lost count; unclean restart records uncertainty without
inventing an exact count. A continuous sequence is not proof every real event
was observed. A clean stop drains for up to three seconds; observations posted
after the writer closes cannot be committed. Producers must stop first: late
posts increment rejected/pending counters and emit an operational error. A loss
noticed during the drain is journaled; a post after the writer has fully exited
cannot retroactively alter its clean-shutdown marker. Informational markers
(`journal.note`, such as CEL observation starting or replay temporarily pausing)
do not trigger the call-history loss warning.

Retention deletes an oldest prefix by age, event count, or physical page pressure.
The byte budget reserves a quarter for database pages and three quarters for
the WAL, allowing a large deletion transaction to coexist with pinned history; small
SQLite/lock sidecars are additional overhead. Long external SQLite readers can
pin WAL pages, so the writer refuses further growth near the WAL budget and
reports degradation. The CLI limits its read lifetime to five seconds. Freed
pages are reused rather than routinely vacuuming the whole database. Lowering
the budget below the existing database's size requires offline compaction or a
new journal; it does not silently delete or rewrite the database.

At the default 64 MiB budget only 16 MiB is available for database pages.
This is often the binding retention limit, **not a promise of 100,000 events or
90 days**. As a rough sizing example, 619 bytes/event fits about 27,000 events;
34 events/call at 20 calls/day is about 40 days. Payloads, indexes and correlation
state change these numbers. Increase the byte budget for the required consumer
outage window and monitor the returned retention floor. Age pruning removes only
a contiguous expired prefix, so a backwards clock step cannot delete fresh rows.

Do not copy just a live `.db` file and assume it is a backup: committed data can
still be in the WAL. Stop Doorman and its readers before a filesystem backup,
or use SQLite's supported online backup tooling. Keep backups private. Restoring
an older snapshot into an actively consumed journal is not supported in this
increment: it can reuse sequence positions under an existing identity. Preserve
such snapshots for read-only historical inspection; start a new journal for new
recording and explicitly reset consumer checkpoints to its new identity.

Never delete a damaged journal to hide an error. Preserve it for recovery, inspect
operational logs, check ownership and free space, and choose a new journal path
explicitly if needed. Telephony and legacy webhook mode remain available without
the journal. Rolling back to the previous binary leaves the database untouched;
restore `WEBHOOK_MODE=legacy` and the receiver's legacy integration together.
