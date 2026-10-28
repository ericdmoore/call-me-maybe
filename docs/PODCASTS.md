# Podcasts pilot (*PODS / *7637)

The opt-in pilot subscribes **handsets** to Brains On! and caches two fixed normal
episodes once per box: “Can dogs read our minds?” and “The hows and whys of hair.”
These are technical trial selections, not an assurance that publisher content or
inserted advertisements have been reviewed for your family.

This implements [#166](https://github.com/ericdmoore/call-me-maybe/issues/166).
No Asterisk upgrade, speech recognition service or conversational model is needed.

## Flow and installed paths

The account WebUI sends the single `podcast.subscribe` verb with `handset`,
`feed: "brains-on"` and `enabled`. The existing sync machine checks its generation
and base revision, validates the handset, durably writes the JSON file and sends
an **applied configuration receipt**. This means subscribed, not downloaded.
Status reports separately carry `podcasts.state`, `readyEpisodes`, `bytes` and
`updatedAt`; states are idle, preparing, ready and error. Older boxes must not be
sent this verb unless their status advertises the `podcast.subscribe` capability.

- `/var/spool/call-me-maybe/podcasts/subscriptions.json`: durable configuration;
  missing file means no subscriptions. The shared spool is accessible to both
  service accounts; `init services` creates the directory and service sandbox paths.
- `/var/spool/asterisk/cmm-media-cache/podcasts/library`: shared validated Ogg Opus
  files and small prepared PCM menu prompts. Audio uses 48 kbit/s VBR stereo. There
  is no retained full-episode PCM recording and no source MP3 after successful work.
- `.../podcasts/progress`: small per-handset / episode / audio-revision bookmarks.
- `.../podcasts/status.json`: preparation report, readable by sync.

`doorman-podcasts.timer` invokes the separate worker about once a minute, including
after reboot. No subscription file means its service is skipped; an empty set
means no network or preparation work. The worker coalesces handset demand under
one file lock, fetches bounded RSS with gofeed v1.5.0, finds the two pinned GUIDs,
downloads each enclosure, converts with FFmpeg, checks codec/container/duration and
full decode, prepares menu titles, and atomically publishes the catalog. A failed
job leaves the previous catalog; the next timer run retries. The download path
accepts public HTTPS only, pins resolved public IPs and checks every redirect.
The pilot allows up to 12 redirects because this feed's publisher enclosures use
several analytics redirectors. No private feeds, tokens or arbitrary URLs yet.

The pool has a conservative 512 MiB ceiling including staging reservations;
enclosures are capped at 64 MiB each and episodes at two hours. The two permanent
artifacts are capped at 64 MiB each; small prompts and progress are additional.
The UI's byte count is shared **episode audio**, counted once, rather than staging
or a per-subscriber total. On a low-space or damaged cache the worker reports an
error; this pilot does not automatically evict or repair damaged editions.

Once both pinned episodes are ready, subsequent timer runs reuse them without
network access or transcoding. This is deliberately a fixed trial, not automatic
new-release discovery, feed polling with ETags or an expanding archive. Removing
one subscription changes that handset's access immediately; the other handset
still uses the same files. Removing every subscription stops work and retains
the two files for reuse. There is no automatic history deletion or eviction.

## Install and trial

Use the normal verified **release** installation procedure in RUNBOOK. Do not copy
a development binary to an appliance. Install the matching hosted sync / app
changes before enabling the new client capability. The WebUI prototype control
is under **Integrations → Brains On!** for the selected box.

1. Install FFmpeg (with libopus and ffprobe) and espeak-ng, then run
   `sudo doorman init services`. The new timer is enabled; without subscriptions
   it performs no media work. The service runs as asterisk with group doorman.
2. Run `doorman render` against the real inventory, review its output, and install
   `pjsip_books.conf` and `extensions_books.conf` through the existing Books include
   procedure. Reload Asterisk and refresh handset directories. The generated
   endpoint marker authenticates the phone; the route is inside the existing
   handset curfew gate. A directory label alone does not install the handler.
3. Subscribe Kitchen in the WebUI. Confirm a sync receipt, then a later preparation
   report with two ready episodes. `doorman podcasts status` shows the local report.
4. Dial `*7637`: hear the episode menu, select an episode, press 2 to start, 5 to
   pause, 4/6 to seek, 2/8 during playback to change speed, and 0 to go back. Hang up,
   redial and choose 1 to resume. The bounded PCM playback windows are disposable.
5. Subscribe a second handset. Confirm one shared copy per episode and separate
   positions. Unsubscribe Kitchen and verify Bedroom still plays. An unsubscribed
   phone must not receive the catalog even while its audio is cached.
6. Restart the worker / box and confirm subscriptions, bookmarks and ready files
   survive. Test missing audio and a failed download without affecting regular calls.

For a local development machine, `doorman podcasts refresh --config ./subscriptions.json
--pool ./podcast-pool` uses isolated paths. This does not install a dialplan or play
a phone. `doorman schema podcasts` describes the JSON. A local CLI subscription
file has this shape (use real existing inventory ids):

```json
{"schema":1,"subscriptions":[{"handset":"kitchen","feed":"brains-on"}]}
```

Validate with `doorman podcasts check --config ./subscriptions.json --handsets
./handsets.toml`. The sync machine also rejects unknown inventory ids. Invalid
subscription files fail closed for podcast access without involving call admission.
Back up this configuration and bookmarks. The configuration is inside the existing
backup spool; the new audio pool and its bookmarks are outside existing automatic
backup sources. **Bookmark backup integration remains follow-up work**: copy that
small directory separately before relying on disaster recovery. Do not back up
re-fetchable episode audio merely to preserve subscriptions.

## Validation and remaining work

The test suite covers shared cache reuse, failed download and retry, Opus validation
and bounded decode, handset authentication, unsubscribe isolation, independent
bookmarks, stale proposals, idempotent sync and generated routing. A real handset
trial is still required after release installation; tests simulate Asterisk's AGI
protocol and do not establish SIP playback on hardware.

Publisher-transcript discovery and generated transcripts, automatic new episodes,
episode selection controls, arbitrary/private feeds, eviction/pinning, backoff,
conditional RSS fetches and progress backup are follow-ups. Podcast transcripts
must remain independent from audio readiness.
