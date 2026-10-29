# Handset podcasts (*PODS / *7637)

Podcasts are opt-in local media, prepared by a background worker. A handset
subscribes to a show; the box shares its prepared Opus audio, while bookmarks
remain independent per handset and episode/audio revision. No directory account,
Asterisk upgrade or live speech service is needed for local subscriptions.

This extends the [pilot (#166)](https://github.com/ericdmoore/call-me-maybe/issues/166)
with [public feed subscriptions (#172)](https://github.com/ericdmoore/call-me-maybe/issues/172).

## Configuration and compatibility

Start with `doorman schema podcasts`. The authoritative validator is
`doorman podcasts check --config <file> --handsets <inventory>`.

Schema 1 retains the built-in `brains-on` feed and two pinned episodes: “Can dogs
read our minds?” and “The hows and whys of hair.” Its existing `podcast.subscribe`
verb and capability remain valid. A pilot-only file is not automatically upgraded.

Schema 2 adds 1–8 public feed definitions and up to 100 unique handset/feed pairs.
A handset can subscribe to several shows. Each definition contains `id`, `url`
and `title`; its ID is `feed-` plus the first 32 lowercase SHA-256 hex characters
of the canonical feed URL. The known Brains On URL retains ID `brains-on`.
URLs use HTTPS, ASCII hostnames, no credentials, query, fragment, nonstandard
port or path traversal. Default port 443 is removed and an empty path becomes
`/`; path case and escaped separators remain meaningful. Keep publisher moves
behind redirects rather than changing the source identity unnecessarily.

For a local public feed:

```sh
doorman podcasts subscribe --handset kitchen \
  --feed-url https://publisher.example.org/show.xml --title 'Nature Show'
doorman podcasts check
doorman podcasts refresh
doorman podcasts status
doorman podcasts unsubscribe --handset kitchen \
  --feed-url https://publisher.example.org/show.xml
```

Use `--config` and `--handsets` for nondefault paths. CLI edits validate the
same source and handset rules as sync and publish atomically. Unsubscribing one
pair preserves other feeds and handsets. An unused generic definition is removed;
removing the last generic subscription returns the file to schema 1. Removing all
subscriptions publishes an empty catalog and reclaims audio/RSS caches after
active playback leases end; small bookmarks are not deleted.

The new `podcast.subscribeFeed` verb contains `handset`, `enabled` and a `feed`
object `{id, url, title}`. It requires the separately advertised capability of
the same name. New snapshots include `podcastFeeds` alongside `podcasts`.
The existing generation/base-revision checks, rejection and idempotent receipt
rules apply. An applied receipt confirms saved configuration, not playable audio.
Host support must be deployed before boxes advertise/send the expanded projection;
the host must withhold generic verbs if a box downgrades. Older boxes must never
receive a generic verb just because they support the pilot.

## Background refresh and safe failure

`doorman-podcasts.timer` invokes `doorman podcasts refresh --scheduled` about once
a minute to notice subscription changes. Existing feeds poll at most hourly,
using ETag/Last-Modified conditional GET. Failed sources retry after five minutes.
An explicit `doorman podcasts refresh` bypasses the waiting interval.
The separate oneshot service runs as asterisk, with read-only subscription access,
a 512 MiB memory limit and low priority. The calling daemon and menu never fetch
feeds, download episodes or transcode audio. Missing configuration skips the
service; zero subscriptions produce no network work.

One worker lock coalesces every handset's demand. Generic feeds select the first two playable eligible entries in publisher order. Tagged
trailers/bonus entries are skipped; absent tags do not certify ordinary content
or absence of ads. Episode identity is scoped by feed plus GUID, with publisher
item link as fallback. Missing identity, invalid metadata or an unsafe enclosure skips that item.
Unchanged selected episodes reuse verified immutable artifacts; a changed
source URL/date/length signature triggers preparation again. This is metadata
change detection, not a promise to detect changed bytes behind an unchanged URL.
Expiring signature/token query parameters do not cause reconversion; meaningful
query parameters remain in the source signature. A show title change refreshes
spoken titles. The fixed pilot skips network after both pinned episodes are
ready, including when other feeds are configured. Menu order groups by stable
feed ID, then publisher episode order, regardless of which editions were new.

Fetching accepts public HTTPS only. Every DNS answer must be public, and the
validated IP is the one dialled; TLS still validates the publisher hostname.
Loopback, LAN, link-local, shared-address, documentation and transition ranges
are rejected. Every redirect is checked, with a limit of 12; RSS redirects
cannot introduce query credentials. Public publisher enclosure signatures may
use query parameters, but no URLs or provider error bodies are logged. Proxy
environment settings are not inherited by this transport.

Bounds: 8 MiB per feed, at most 5,000 exposed items, 64 MiB per enclosure and two
hours per episode. The shared disk pool has a conservative 512 MiB ceiling,
including staging reservations for conversion and prompts. The worker prepares
48 kbit/s stereo Opus and verifies codec, duration and full decode. No source MP3
or full-episode PCM is retained after successful preparation. One episode is downloaded/prepared at a time; the disk budget reserves a single
working set, rather than multiplying a worst-case reservation by the catalog
size. At most four candidate preparations per source are attempted per pass.
Failed sources retain their own last-good episodes while healthy sources update;
bad audio is skipped so older eligible episodes can fill the slots. Source
failure/retry metadata lives in protected `.feed-<id>.json` cache records. A new
complete menu is published once after the pass; unchanged menus and status
records are not rewritten. Aggregate error status can coexist with playable
new episodes from healthy sources. Source duration is measured from a bounded,
discarded decoded sample stream, avoiding inaccurate VBR container estimates.

Cache collection removes unreferenced immutable podcast editions, retaining the
current catalog. A shared playback lease covers a menu's entire frozen list;
collection waits until no active menu can reference retired files. This may delay
reclamation and cause a bounded low-space failure rather than interrupt playback.
Progress is separate and survives eviction. A source's permission is checked
before listing, selection and each playback window; cache presence alone does
not grant another handset access. Generic show/episode titles use offline prompts
in `prompts-podcasts-v2`, with legacy pilot prompts still readable.

## Installed paths and status

- `/var/spool/call-me-maybe/podcasts/subscriptions.json`: durable configuration,
  mode 0644, public feed metadata only, written by sync/CLI. The installed parent
  directories restrict traversal to Asterisk/doorman. Read permission allows the
  Asterisk AGI UID to enforce handset subscriptions without inheriting all daemon
  group access. `doorman init services` migrates existing metadata permissions.
- `/var/spool/asterisk/cmm-media-cache/podcasts/library`: shared prepared audio,
  immutable edition directories, menu prompts and transactional catalog.
- `.../podcasts/progress`: per-handset episode/revision bookmarks.
- `.../podcasts/status.json`: idle/preparing/ready/error, ready episode count,
  shared episode-audio bytes and observation timestamp. Bytes do not include
  temporary sources or multiply by handset count. The count is box-wide.

Install the new binary and run `doorman init services`, review the generated
optional unit/timer, then enable `doorman-podcasts.timer` after checking a public
subscription. Render/provision the existing Podcasts phonebook/dialplan entry.
The CLI can verify preparation without placing a call; a real handset smoke test
must still verify menu intelligibility, selection, seek/resume and hangup.
Do not claim hardware playback from a successful source build or sync receipt.

Automated tests exercise generic sync and replay, old-capability withholding on
the host, public-address/DNS/redirect limits, shared downloads, last-good failure,
per-feed menu isolation, separate bookmarks and playback-safe eviction. Before
release run `make check` and `make cover`; no production host is changed by tests.

Optional-media failures do not block other sync. An invalid/unreadable podcast
file omits the podcast projection (and disables podcast edits until repaired),
while policy/phonebook/webhook sync continues. A removed handset's subscription
is excluded from projection; its durable file is not silently rewritten. New
podcast edits still require a known handset. The sync unit's podcast write path
is optional so hosts without that directory can still start sync.
