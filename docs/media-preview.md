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

## Complete recorded books

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
When `--full-books` encounters a small preview, it still uses the preview installer.

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
