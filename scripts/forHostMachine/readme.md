# Doorman host machine scripts

First draft of an offline contact-snapshot pipeline. Requires Bash and jq;
works on Linux and macOS. Run as the account owning the snapshots, in a private
directory outside the checkout (directory mode 0700). New files are mode 0600.
No live provider requests, account changes, or daemon restarts are installed
by adding these scripts.

## Current boundary

**Doorman currently reads vCard files, not these NDJSON snapshots.** Its contact
set is loaded at startup; these scripts do not add a contact reload mechanism,
per-person routing, or automatic admission. Do not point `contacts.toml` at the
NDJSON output. Bullmoose's default `contacts export` emits vCard if you need a
format today's importer understands, but today's admission rules are global.

The local Bullmoose CLI docs and `cli-go/internal/cmd/contacts.go` confirm that
`contacts export --json` already emits JSContact NDJSON and exports all pages.
No `.[]` jq conversion is needed. The exporter must emit only contact objects
on stdout, exit nonzero on errors, and return a complete selected book. A
successful empty export intentionally clears that source (including deletions).

## Refresh one source

```bash
./fetchContacts.sh mk-personal mk /var/lib/doorman/contacts/mk.ndjson -- \
  /usr/local/bin/bullmoose contacts export --book Personal --json
```

`SOURCE` identifies one account/book selection; `OWNER` identifies the person,
not a physical handset. Use a different source ID for every selection. Configure
Bullmoose's endpoint, account and credentials through its own supported profile
or environment before running. For multiple accounts, use separate local wrapper
executables that select each account explicitly; do not let every source silently
use the same default account. No endpoint or authentication flags are invented
here. Never pass token values as command arguments or enable shell tracing.

An exporter wrapper can also load a trusted, private environment file and apply
a timeout (for example Linux `timeout 120s`). An exporter failure, invalid JSON,
missing identity, or duplicate identity leaves the previous snapshot untouched.
Raw exporter/parser errors are suppressed because they can contain private data.
Debug the exporter separately in a private terminal if necessary.

Each output line is a draft, versioned envelope:

```json
{"version":1,"source":"mk-personal","owner":"mk","contact":{"uid":"example-contact","name":{"full":"Example Friend"}}}
```

The complete contact object is retained, including phone labels, organisation
and address-book membership if the exporter supplies them. This is sensitive
contact data, not just phone numbers. No number is promoted to trusted here.

## Merge selected sources

```bash
./concatMultipleContactFiles.sh /var/lib/doorman/contacts/household.ndjson \
  /var/lib/doorman/contacts/mk.ndjson \
  /var/lib/doorman/contacts/parent.ndjson
```

List inputs explicitly: a glob could accidentally include the output or an old
source whose access was revoked. The same number in two books stays two records;
future doorman indexing must preserve both relationships. Duplicate source/contact
identities are rejected. Missing or invalid inputs leave the prior merge intact.
Removing a source from this input list removes it from the next successful merge.

Both commands print `updated` or `unchanged` and return 0 on success (including no
change), 2 for usage errors, and nonzero on failure. Sorted records/object keys
and byte comparison avoid publishing when only export order changed. Arrays retain
their original order. Snapshot mtime therefore means content changed, not last
successful sync; record successful runs in the scheduler journal separately.

## Nightly orchestration

Install the scripts at a fixed location and make a private runner, for example:

```bash
#!/usr/bin/env bash
set -euo pipefail
umask 077
scripts=/opt/call-me-maybe/scripts/forHostMachine
cache=/var/lib/doorman/contacts
"$scripts/fetchContacts.sh" mk-personal mk "$cache/mk.ndjson" -- \
  /usr/local/libexec/export-mk-contacts
"$scripts/fetchContacts.sh" parent-personal parent "$cache/parent.ndjson" -- \
  /usr/local/libexec/export-parent-contacts
"$scripts/concatMultipleContactFiles.sh" "$cache/household.ndjson" \
  "$cache/mk.ndjson" "$cache/parent.ndjson"
```

The wrappers must be supplied locally; they export a full book as NDJSON using
that person's configured Bullmoose credentials. The runner stops on any failed
source, so a partial run cannot publish a new household snapshot. Successful
per-source snapshots remain available for retry.

Run the runner manually to validate before scheduling. On Linux, a systemd
oneshot service can use `UMask=0077`, `TimeoutStartSec=5min`, and the appropriate
`User=`; pair it with a timer using `OnCalendar=*-*-* 03:00:00` and
`Persistent=true`. Use absolute paths and an explicit PATH. A timer for the same
service will not overlap it. For cron, wrap the entire runner in `flock -n` and
capture failures in your normal monitoring. Also use that orchestration lock for
manual refreshes; per-output locks alone do not make a multi-source run coherent.

Each script additionally uses an atomic mkdir lock and publishes with a rename
on the destination filesystem. A forced kill or machine crash can leave a lock
or private temporary directory; confirm no job is running before removing it.
No automatic lock stealing, daemon reload, or schedule is installed by this draft.

## Incremental sync: next step

This version downloads full exports; `unchanged` saves writes, not network traffic.
Bullmoose's server implements `ContactCard/changes`, but these scripts do not
pretend `contacts export` accepts a since-state flag. Incremental sync belongs in
a stateful Bullmoose sync/export command or a dedicated adapter:

1. Bootstrap all selected cards and capture a consistent server state.
2. Request changes, handling pagination; fetch created/updated cards and remove
   destroyed cards or cards that no longer belong to the selected books.
3. Commit contact data and cursor together, only after the full update succeeds.
4. Fully resync when the server returns `cannotCalculateChanges`.
5. Distinguish temporary outages from revoked access, and expose last successful
   sync time so stale data cannot quietly become permanent admission permission.

A future doorman reader must validate this envelope, normalise numbers, apply its
personal/published/block rules, preserve ownership and atomically reload the
complete household index. Concatenation alone is never an allow-list decision.
