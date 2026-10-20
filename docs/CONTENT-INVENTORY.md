# Content inventory

The existing `doorman sync run` process measures one explicitly configured local
library every 30 seconds and includes the result in its ordinary status report.
It does not run in the call daemon, upload media, install packs, or infer phone
mounts. No additional service or database is required.

The default root is `<state>/content/library`; `--content directory` overrides it.
Inspect a report without signing in or sending anything:

```sh
doorman sync inventory --content /path/to/library
```

Each top-level directory or file is one item. Directory contents contribute file
counts and sizes. Logical bytes and allocated disk blocks are reported separately;
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

Bounds: 128 items, 64 KiB of closed-schema metadata, 20,000 walked nodes, 64
directory levels, and a two-second walk budget. Missing roots are `unavailable`;
invalid metadata, unreadable entries, and scan limits produce `partial` reports
with only known allocation. Time budgeting cannot interrupt a blocked filesystem
syscall; use local storage. Older boxes can omit content inventory entirely.

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
