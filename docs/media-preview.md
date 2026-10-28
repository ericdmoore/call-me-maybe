# Opt-in sandbox preview delivery

This command-side lane connects a family's hosted ownership to the existing
local Books player. It is separate from configuration sync and call admission.
It is never enabled by a remote response, an ordinary sync pass, or a purchase
alone. The operator explicitly approves the HTTPS source that will receive the
box's sync credential. Only use a service you trust with that credential.

```sh
doorman sync media list --source https://sandbox.example.invalid
doorman sync media once --source https://sandbox.example.invalid
```

`--state` defaults to `/var/lib/doorman`; `--library` defaults to
`/var/spool/asterisk/cmm-books/library`. Run as an operator with permission to
read the private account state and write the Books library. This initial
rehearsal command is manual; it does not install a timer or restart a service.
The command reports installation, not evidence of handset listening.

The `sandbox-pack-delivery-v1` service must independently check the current box
credential, family ownership and sandbox authority on every request. List,
download and receipts use the sync bearer credential. Downloads must remain at
the approved origin, use the fixed sandbox download path and carry the declared
`sync-bearer` authorization mode. Redirects are refused. Credentials, response
bodies and signed addresses never enter errors, command output or the receipt
journal. A refusal from a media source cannot change account enrolment or revoke
its credential; authoritative account sync owns those decisions.

The client bounds downloads to 1 MiB and checks their exact size and SHA-256.
The preview importer checks the ZIP entries and every SHA256SUMS member, bounds
expanded files, checks the manifest and decodes each Opus segment with bounded
FFmpeg processing. Publication uses immutable recording directories and an
atomic catalog replacement; previous books remain available. Repeated imports
verify the existing revision against freshly validated output before reuse.
The library writers share a lock. Conversion, title prompts and common prompts
are prepared before publication; no call performs speech synthesis.

This importer accepts at most two minutes, eight chapters and sixteen segments
per chapter. It intentionally produces bounded PCM for the existing player.
**Full novels are refused.** A full-book implementation must retain compressed
baseline audio and bound playback processing/storage instead of expanding the
whole novel through this preview path.

## Complete recorded audiobooks

The separate full-book mode is explicit and keeps the preview limits above:

```sh
doorman sync media list --source https://sandbox.example.invalid \
  --audiobook-sku sandbox-recorded-book-v1
doorman sync media once --source https://sandbox.example.invalid \
  --audiobook-sku sandbox-recorded-book-v1 --menu-bundle ./prepared-menu
```

The selected SKU is sent on the listing request and the client negotiates
`X-Doorman-Media-Format: audiobook-v1`. It accepts only that SKU in a
`sandbox-pack-delivery-v2` response. Download and receipt requests retain the
same opt-in header, approved origin, sync bearer and redirect refusal. Old
preview clients continue using v1 and are not silently offered large archives.

The archive streams into a private mode-0600 temporary file, capped at 256 MiB.
Both transport and receiver verify the exact size and SHA-256 before import.
Before ZIP parsing, the importer bounds the real central directory to 4,096 entries
and 8 MiB of metadata, refusing ZIP64/multi-disk forms. It validates at most 512 MiB expanded,
a 2 MiB manifest and 1 MiB for each other member. Every member must be checksummed;
every audio segment must be referenced exactly once and decode within 120 ms of
its declared length. Segments are at most 45 seconds; recordings are at most 48
hours, with at most 999 chapters. Independent millisecond rounding at adjacent
manifest timestamps is allowed within 1 ms, not as an arbitrary timing gap.

Compressed Opus segments remain the baseline. Generated, path-restricted chapter
playlists feed the existing FFmpeg player, which retains only its playing and
prefetched 30-second PCM windows. The whole novel is never expanded to PCM or
duplicated per speed/handset. Import has a 30-minute deadline and per-segment decode
limits. Temporary downloads are removed on every completed/failed attempt; a subsequent
locked pass removes archives abandoned by a killed downloader.
interrupted receipts retain their original sequence and repeated imports verify
the immutable revision before reuse. Old revisions remain for active calls, so
operators must budget their retained library as well as download/import staging.

The prepared-menu safeguard applies unchanged: a changed catalog exports
`preview-catalog.pending.json` and refuses activation until a matching offline
bundle is supplied. Render it with the intended voice (for example
`Zoe (Premium)` on macOS), then retry with `--menu-bundle`. Neither receipt nor
catalog publication is proof of real handset listening.

The same mechanism works free and offline, without a Store purchase:

```sh
doorman books install-audiobook --archive ./recorded.cmmpack \
  --library ./library --menu-bundle ./prepared-menu
```

This local command validates/imports the archive but does not send commerce
receipts or grant hosted ownership. Deploy only a published release binary per
the upgrade runbook before rehearsing on a real box.

## Receipts and offline activation

Receipt states are downloading, downloaded, installing, installed and failed.
An fsynced outbox under `<state>/media-preview` precedes each POST. Restart
reconciles against the server and resends an unresolved receipt with its original
sequence. Conflicting equal sequences or a newer unrelated server transition
stop for operator investigation rather than overwriting history. Installed is
terminal for that delivery; this is not a reinstall/uninstall protocol.

A free local mechanism is available without an account or a store purchase:

```sh
doorman books install-preview --archive ./example.cmmpack --library ./library
```

This verifies the local archive and applies the same preview limits. It does not
prove hosted ownership or send a receipt. After either installation, the normal
local Books handset menu can browse and play the recording offline. Existing
prepared voice bundles are preserved. If a catalog change would invalidate one,
installation stops before activation and exports `preview-catalog.pending.json`.
On a Mac, copy that candidate as `catalog.json` into an offline staging library
and render the exact desired voice:

```sh
doorman books prompts --library ./next-menu-library --voice 'Zoe (Premium)'
```

Transfer `menus.json` and `menu-audio/` together back to the appliance, then retry
`sync media once` (or `books install-preview`) with `--menu-bundle` naming that
directory. The importer verifies it matches the exact next catalog before
activation. Existing calls retain their immutable audio. A first library with no
prepared voice bundle can use the compatibility prompts; a prepared voice is
never silently replaced by them.

Rehearse a successful install and repeated pass, a failed checksum/conversion,
receipt loss/replay and interrupted publication. Listen on a real handset,
including pause/resume and speed controls, before claiming playback acceptance.
A simulated AGI test or an installed receipt alone is insufficient.
