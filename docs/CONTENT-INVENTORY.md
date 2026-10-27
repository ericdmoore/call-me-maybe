# Content inventory

The existing `doorman sync run` process measures one explicitly configured local
library every 30 seconds and includes the result in its ordinary status report.
It does not run in the call daemon, upload media, install packs, or infer phone
mounts. No additional service or database is required.

The scan runs on its own goroutine. Each status exchange sends the last finished
report and never waits on the disk, so a slow or hung library mount makes the
inventory stale (its `sampledAt` stops advancing) without delaying configuration
sync. Until the first scan finishes the status omits the inventory.

The default root is `<state>/content/library`; `--content directory` overrides it.
Inspect a report without signing in or sending anything:

```sh
doorman sync inventory --content /path/to/library
```

Each top-level directory or file is one item. Directory contents contribute file
counts and sizes. Symlinks, FIFOs, sockets and device files inside an item are
skipped: they are neither followed nor counted, and the item keeps its sizes.
A top-level symlink or special file is reported `unreadable`. Logical bytes and allocated disk blocks are reported separately;
the library total counts hard-linked objects once, including small directory and
metadata overhead. Capacity, used, available, and reserved bytes describe the
filesystem containing the root, not all disks attached to the machine.

The optional `.content-source.json` records importer provenance:

```json
{
  "schema": 1,
  "label": "My box",
  "kind": "audiobook",
  "origin": "cli",
  "items": [
    { "path": "first-book", "title": "The First Book" }
  ]
}
```

The root defaults apply to actual entries; an item's `kind` or `origin` can
override them. Kinds are `audiobook`, `game`, `interactive`, and `file`; origins
are `store`, `cli`, `browser`, and `unknown`. A dedicated CLI import directory
may declare `cli` as its default. For mixed directories use `unknown` and record
each known origin individually. Missing metadata means unknown origin, never a
store purchase. Metadata does not grant entitlements or prove purchases.

Titles and paths are local library metadata sent to the signed-in account.
Only relative top-level paths are reported, never the absolute root. The scanner
does not read media bodies, follow symlinks, or search unrelated directories.
Files are `stored` or `unreadable`; neither means mounted on a phone.

Top-level names that are not valid UTF-8 are skipped and make the report
`partial`. JSON would turn their invalid bytes into U+FFFD, so two distinct
files could arrive under one id; rename such files to list them.

Bounds: 128 items, 64 KiB of closed-schema metadata, 20,000 walked nodes, 64
directory levels, and a two-second walk budget. All top-level names (up to the
node budget) are sorted before the first 128 are taken, so the same library
always yields the same items. Missing roots are `unavailable`; invalid metadata,
unreadable entries, skipped names, and scan limits produce `partial` reports with
only known allocation. An item the budget stopped is still `stored`, with its
file count and sizes omitted rather than guessed. Before a report is sent it is
checked against the server's schema (unique ids, field lengths, enums, integer
ranges); an item that fails is dropped and the report becomes `partial`.
Time budgeting cannot interrupt a blocked filesystem syscall, but such a scan
only delays the next report. Older boxes can omit content inventory entirely.

For an existing media directory outside the sync service's filesystem sandbox,
use an explicit read-only bind in a `doorman-sync.service` systemd drop-in:

```ini
[Service]
BindReadOnlyPaths=/srv/my-library:/var/lib/doorman/content/library
```

Create the destination directory first, keep the existing service user and
hardening, and restart only sync after `systemctl daemon-reload`. The source must
be readable by that user through the mount. The bind is private to the service,
does not duplicate media, and survives ordinary service definition updates.
New files and removals appear on the next scan.
