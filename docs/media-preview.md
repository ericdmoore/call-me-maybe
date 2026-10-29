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
Transport enforces the exact size; the receiver verifies SHA-256 while staging before import.
Before ZIP parsing, the importer bounds the real central directory to 4,096 entries
and 8 MiB of metadata, refusing ZIP64/multi-disk forms and ambiguous extra EOCD signatures. It validates at most 512 MiB expanded,
a 2 MiB manifest and 1 MiB for each other member. Every member must be checksummed;
members are inflated once for verification and staging. Every audio segment must be referenced exactly once and decode within 120 ms of
its declared length. Segments are at most 45 seconds; recordings are at most 48
hours, with at most 999 chapters. Independent millisecond rounding at adjacent
manifest timestamps is allowed within 1 ms, not as an arbitrary timing gap.

Full recordings use a consistently namespaced ID, independently of manifest ID length.
A preview cannot replace a full recording, including through a legacy ID collision.
Catalog count and conservative JSON-size checks run before staging/decoding. A locked
import sweeps abandoned `.audiobook-*` and `.preview-*` stages without following links.
Missing `ffmpeg` / `espeak-ng` is reported as unsupported tooling; disk exhaustion is
reported as `no_space`, rather than a network failure.

Compressed Opus segments remain the baseline. Generated, path-restricted chapter
playlists use decoded sample durations (also used for chapter lengths) and feed the existing FFmpeg player, which retains only its playing and
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

Prepared menu bundles may contain up to 256 MiB of referenced PCM clips, with
at most 8 MiB per clip. This includes menus for the existing library, not just
the new book; clips are copied one at a time rather than loaded as a bundle.

The same mechanism works free and offline, without a Store purchase:

```sh
doorman books install-audiobook --archive ./recorded.cmmpack \
  --library ./library --menu-bundle ./prepared-menu
```

The receiver hashes while writing its private staging file. The importer performs
one on-disk archive hash for its immutable revision; the local CLI does not pre-hash
a file just to compare it to itself.

This local command validates/imports the archive but does not send commerce
receipts or grant hosted ownership. Deploy only a published release binary per
the upgrade runbook before rehearsing on a real box.

## Receipts and offline activation

Receipt states are downloading, downloaded, installing, installed and failed.
An fsynced outbox under `<state>/media-preview` (preview/recorded-v1) or
`<state>/media-audiobook` (audiobook-v1) precedes each POST. Restart
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

## Recorded-v1 compatibility mode

The `--full-books` mode uses the separate `recorded-v1` protocol for services
that implement it. It does not negotiate the SKU-selected `audiobook-v1` format
above; the two flags cannot be combined. Both formats can coexist in one local
library and use the same bounded player and durable receipt handling.

Complete Ogg Opus audiobooks use an explicit operator opt-in:

```sh
doorman sync media once --source https://sandbox.example.invalid --full-books
# Free local installation, with no account or hosted purchase:
doorman books install-recorded --archive ./book.cmmpack --library ./library
```

The full-book lane downloads at most 256 MiB into private scratch storage, hashes
while writing, and independently checks size/hash again before installation. It
retains the same durable receipt outbox as previews; interrupted transfers restart
from the beginning and abandoned scratch files are removed under the receiver's
lock. Redirect refusal, origin pinning and credential handling are unchanged.
The ordinary media command and `install-preview` retain their preview bounds.
`--full-books` declares `X-Doorman-Media: recorded-v1`; services must withhold
larger offers and their delivery rows from older clients. Preview clients skip
otherwise valid larger offers while preserving compatible previews.

Excerpt provenance, rather than compressed byte count, selects the preview
installer. A small complete recording retains its full-book identity. Before a
streamed GET, expiring signed URLs are refreshed. Transfers have a one-minute
inactivity deadline and caller cancellation, with no fixed total time limit.

Segment details live in a checksummed `recording.json` per immutable book. The
shared catalog holds chapter summaries and the index hash, keeping a large shelf
within its existing size bound. Copying, decoding and chapter speech finish in a
private stage before the publication lock is acquired; the current catalog is
loaded under that lock to preserve concurrent changes.

Recorded archives allow at most 4,096 regular ZIP members, 2 MiB per member,
300 MiB expanded compressed data, 999 chapters and 48 hours total. Every member
must be checksummed, paths must be safe, chapter numbers must be contiguous,
and segment timing must be coherent. Starts and lengths rounded independently
by the builder may differ by at most one millisecond at a boundary. FFmpeg
actually decodes each segment with a 30-second process deadline and at most
46 seconds of output, checking its declared duration within 120 milliseconds.
No decoded book PCM is retained at installation.

Each immutable revision contains the verified Opus segments and one generated
FFmpeg concat playlist per chapter. Playlists use only validated relative audio
paths. Playback decodes and applies pitch-preserving tempo processing into the
existing two-window buffer (playing plus prefetched, 30 seconds each), rather
than storing an entire decoded novel or one copy per speed. Seeking, pause,
chapter navigation, cancellation and revision-specific bookmarks use the same
player as existing PCM books. Local and hosted imports use identical playback;
origin records where the import came from, not permission to play it.

The existing prepared-menu safeguard also covers full books. An unmatched menu
bundle leaves the current catalog active and exports
`preview-catalog.pending.json` (the compatibility filename also used for full
books). Render that candidate offline and retry with `--menu-bundle`. Failed
imports never replace the previous playable revision. Immutable unactivated or
superseded revisions are retained; operators should remove them only when no
active call uses them. Installation is not evidence of a handset listening test.
