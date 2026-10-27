# Packs

A pack supplies authored text, recorded audio, or both. The phone plays its
complete installed audio; it never waits for synthesis during a call. The bouncer's personality is the
part of this project people remember, and packs are how it becomes swappable:
the same lobby logic can greet strangers as a Victorian doorman, a bureaucrat,
or a ship's computer.

This document is the format specification and the rules for authoring one.
**The specification is CC0** — implement it, generate packs, publish and sell
them, no permission needed.

---

## The design principle: mechanisms are free, content is paid

Everything in this repository is a **mechanism**: the lobby, the ladder, the
bouncer, the sound-board dialplan, the joke-rotation dialplan. Mechanisms are
Apache 2.0 and always will be. What a mechanism *plays* is **content**, and
content can be free (CC BY-SA community packs, including the default one) or
paid.

This is not a pricing tactic, it is what keeps the project healthy. A
mechanism that only works with a purchased pack is a crippled program. A
mechanism that works perfectly with the free pack, and is more fun with a
paid one, is a project people will contribute to. Concretely:

| Mechanism (free, in repo) | Content (packs) |
|---|---|
| Lobby + bouncer prompts | Voice/archetype packs |
| Hold music playback | Themed hold beds |
| Ringback | Themed ringback |
| Sound board extension | Sound effect packs |
| Joke/riddle rotation | Joke and riddle packs |
| Voicemail greetings | Greeting packs |
| Story interpreter | Story packs (`story`) |

If you find yourself designing a feature that is useless without buying
something, you have put the line in the wrong place.

---

## Format

Authors work in a source directory. Distribution uses a `.cmmpack` ZIP;
installation produces a directory with one pair of audio files per clip.
Source-only archives are valid: missing audio renders during installation.
The following tree shows a complete voice pack:

```
victorian-doorman/
├── pack.json
├── welcome-known.wav       8 kHz  mono 16-bit PCM
├── welcome-known.wav16     16 kHz mono 16-bit PCM
├── lobby-greeting.wav
├── lobby-greeting.wav16
├── invalid-extension.wav
├── invalid-extension.wav16
├── good-day.wav
├── good-day.wav16
├── no-answer.wav
├── no-answer.wav16
├── connecting.wav
└── connecting.wav16
```

Both sample rates are required: 8 kHz is played on ulaw calls, 16 kHz on g722
(wideband). Asterisk picks whichever needs less transcoding — shipping only
one forces the Pi to resample on every call.

### `pack.json`

```json
{
  "id": "victorian-doorman",
  "name": "The Victorian Doorman",
  "version": "1.0.0",
  "author": "Your Name",
  "license": "CC BY-SA 4.0",
  "kind": "voice",
  "description": "Courteous, formal, and entirely unmoved by your excuses.",
  "voice": "en_GB-alba-medium",
  "prompts": {
    "welcome-known": "Ah — you are expected. One moment, if you please.",
    "lobby-greeting": "You have reached the lobby. Kindly present your extension within ten seconds.",
    "invalid-extension": "That is not an extension known to this house. You may try once more.",
    "good-day": "I regret the hour has passed. Good day.",
    "no-answer": "The household is not receiving at present.",
    "connecting": "Announcing you now."
  }
}
```

`prompts` carries the text as well as the audio. It is what makes a pack
readable, reviewable, and rebuildable — and it lets a pack be translated
without re-recording from scratch.

### The prompt-name contract

The six names above are fixed. They are compiled into the binary
(`internal/lobby/prompts.go`), which is what makes packs interchangeable: any
pack supplying all six works with any version of doorman that knows them.

A pack **must** supply all six. It may not add new ones — a new prompt name
requires a code change, which is deliberate: the contract is the thing that
guarantees a pack cannot half-work.

**The house's own phrases are not part of it.** Things doorman says to the
household rather than to a caller — the low-balance call's *"the calling
credit is running low, it is down to about"* — live in the bundled pack's
`system/` directory (`prompts/manifest.json` under `"system"`) and are
played from there whatever `PROMPT_MEDIA_PREFIX` says. A pack does not
supply them and cannot silence them; the lobby's voice is the pack's, the
house's is its own. Dial-a-Story's limits speak from there too, to a caller
on a public line — *"this call will end in one minute"*, the goodbye at the
limit, *"all our listening lines are busy"* — because they belong to a
mechanism every pack must leave working, not to a pack.

**Nor is the arithmetic challenge.** A public line that asks "what is three
times six" before a phone rings speaks it from the bundled pack's
`challenge/` directory (`prompts/manifest.json` under `"challenge"`: an
explanation, "what is", "times", "that's right", "try again", and the digits
zero to nine), rendered by `prompts/build.sh` with the rest of the bundled
pack, in the same voice, and played whatever pack
the line uses. It is a second, optional clip set beside the contract, so a
pack that predates it keeps working, and the challenge can never half-work
for want of a pack's copy. A pack supplying its own `challenge/` is a later,
opt-in step, not a requirement.

**A line's own words are not a pack either.** A front menu's clip
(`<prefix>/menus/<id>`) and its recordings (`<prefix>/recordings/<id>`) are
operator content — "press 1 for orders" is specific to one business — and
live beneath the line's prefix in subdirectories no pack contract names.

### Archive version 1

A `.cmmpack` is a ZIP with `pack.json` and `SHA256SUMS` at its root, plus
only the declared story source and audio. The manifest carries
`"archive_version": 1`. Source directories may omit this field; archives
must include it. Unknown archive versions and unsupported kinds fail
explicitly. This installer supports **voice and story**. The story handset
player remains separate work (#69); rotation and sound-board players remain
#67 and #68.

`id` is 1–64 lowercase letters, digits or hyphens, beginning with a letter
or digit. `name`, `version` and `license` are required. The version is a
publisher label; immutable installation identity also includes source and
audio hashes. `voice` may be a Piper model name or an object:

```json
"voice": {
  "backend": "piper",
  "id": "en_GB-alba-medium",
  "settings": {}
}
```

Supported renderers are `piper`, `exec`, `elevenlabs`, `openai` and `polly`.
Downloaded Piper packs use model names, without paths or `.onnx` extensions;
`PIPER_VOICE_DIR` selects the operator's model directory. Provider keys,
endpoints and executable configuration never come from the pack. They come
from the operator's environment or `--env` file (environment takes priority).
An exec wrapper is operator-installed and receives the voice ID as data;
never interpolate downloaded values into shell commands.

An optional `audio` map declares each supplied **pair**:

```json
"audio": {
  "good-day": { "origin": "human", "source_hash": "<clip source hash>" }
}
```

The key is the extensionless clip name: one of the six lobby names, a story
utterance such as `audio/entrance-01`, or `sfx/door-creak`. It maps exactly to
`<name>.wav` and `<name>.wav16` beside the manifest. `origin` is `human` or
`rendered`. If the clip also has script text, `source_hash` binds the audio
to that text, backend, voice and settings. Obtain it with
`doorman pack check <dir> --hashes` **before adding the audio declaration**.
After editing a script, remove its old audio declaration before computing the
new hash or rendering it again. The binding detects stale audio; it does not
prove that a recording speaks the supplied words. Audio-only lobby prompts
may omit text and must omit `source_hash`. All six still need a pair.
Story scripts are required; effects have no script/hash.

`SHA256SUMS` covers every payload file except itself, with one lowercase
SHA-256, two spaces, and relative filename per line. Checksums detect damage;
they do not authenticate publishers or implement DRM. The builder only
packages declared content: it never copies `.env`, models or unrelated
files from the source directory. The reader rejects extra payload, links,
duplicate/case-colliding names, absolute paths, traversal and nonregular
entries. Names use slash-separated ASCII components beginning with a letter
or digit, then letters, digits, dots, underscores or hyphens (128 characters
per component, 240 total). Story source filenames end in `.md`.

Limits are 256 MiB for compressed and total uncompressed archive size,
64 MiB per audio file, 1 MiB per manifest/story/checksum file and 4,096 entries.
Do not include ZIP directory entries. Archive validation and extraction use
private temporary storage; allow room for that plus audio, the render cache
and an installed copy. Only complete PCM pairs become playable.

### Building an archive

```bash
doorman pack check ./victorian-doorman
# Package scripts and declared recordings; no renderer or credentials needed.
doorman pack build ./victorian-doorman --source-only --archive victorian.cmmpack
# Or render on the authoring machine and ship all audio with its scripts.
doorman pack build ./victorian-doorman --archive victorian.cmmpack
doorman pack lint victorian.cmmpack
```

Plain `pack build <dir>` still writes `<dir>/build` (override with `-o`).
`--backend` applies to loose directory builds only; archive builds use the
manifest voice so their source binding remains accurate. Scripts are authored
by people: the command generates speech from them, not the scripts themselves.

### Installing and selecting

No account, network catalogue or DDcc service is required. A source pack
needs its named renderer; bundled audio needs none. Run from the appliance's
configuration directory, or supply its actual environment path:

```bash
doorman pack lint victorian.cmmpack
sudo doorman pack install victorian.cmmpack --env /etc/doorman/.env --dry-run
sudo doorman pack install victorian.cmmpack --env /etc/doorman/.env
sudo doorman pack list --env /etc/doorman/.env
sudo doorman pack use victorian-doorman --env /etc/doorman/.env
doorman check --env /etc/doorman/.env
```

`--sounds-dir` defaults to `/var/lib/asterisk/sounds`. Before synthesis, install
reports bundled/cached/missing clip counts and available estimates in characters
and requests; vendor prices are not hardcoded. There is no automatic voice
substitution. Install renders sequentially, with reduced process priority and
one Go CPU worker, leaving the previous selection in place. External renderers
may manage their own threads; on a small appliance, use a bounded-thread
wrapper or ship audio rendered on a workstation. No renderer runs in the daemon.

Local state is `staged` → `rendering` (if needed) → `installed`, or `failed`.
Progress includes completed/total clips, current clip and a readable error.
`pack list --json` exposes the latest attempt, installed version and configured
active prefix for a later sync adapter. Interrupted jobs can remain marked
staged/rendering until retried; rerun the same install. Completed valid renders
are reused. Credentials and provider error bodies are not saved in progress.

Generated clips cache by clip name, text, backend, voice, settings and
`--renderer-revision` (default: this doorman build's version). Change that
revision after replacing local models or changing an operator wrapper/provider
model that the manifest does not identify. A new doorman version conservatively
invalidates the cache. The source hash uses the v2 framed format, so older
loose-build caches rebuild once. Bundled recordings are copied and checked
again; they are never silently regenerated or normalised.

Playback is installed under
`doorman-packs/<id>/versions/<content-digest>/` in the sounds root. Private
progress, installation records and the reusable cache live in
`.doorman-packs/` (0700). Each clip records origin, source hash, renderer
revision where applicable, voice and exact audio checksums. Playback directories
are 0755 and audio 0644 so Asterisk can read them; run with permission to write
the sounds root and environment file. Install never edits the active prefix.
Old versions remain available, including while a replacement renders or fails.

`use` verifies every installed clip, atomically changes `PROMPT_MEDIA_PREFIX`
and restarts a running `doorman.service`. It checks that systemd reads the
specified environment file; a stopped service takes the selection on its next
start. A failed restart restores the old file and attempts to restart that
configuration, reporting any recovery failure. Concurrent manual edits prevent
automatic rollback rather than being overwritten. Owner and permissions of the
environment file are preserved. Use `--no-restart` only for offline/manual
configuration, then restart your supervisor to activate it.

`pack list` and `doorman check` report the **configured** default. Per-line
`prompts` overrides still take precedence; a successfully installed upgrade
can coexist with an older active selection until `use` is run again. Story
packs install alongside voices and cannot be selected as the lobby voice.
The bundled `call-me-maybe` folder and its system phrases are never overwritten.
To restore that default, set `PROMPT_MEDIA_PREFIX=call-me-maybe` and restart.
Family overlays (s16) remain separate future work.

### Audio validation and levels

Both files must be complete RIFF/WAVE, mono, signed 16-bit PCM: 8 kHz in
`.wav` and 16 kHz in **`.wav16`**, Asterisk's wideband filename convention.
Compressed WAV, truncated/duplicate chunks, partial pairs and digital silence
are rejected. Lint checks supplied files without synthesis; install also
checks generated/cached output before committing it.

The shell pipeline (`prompts/build.sh` with ffmpeg) targets **−18 LUFS and
−2 dBTP**. The Go processor approximates speech loudness with **−18 dBFS RMS
and a −2 dBFS sample-peak ceiling**. RMS is not LUFS and sample peak is not true
peak. Lint warns outside −18 ±6 dBFS RMS or above −1.5 dBFS sample peak
(0.5 dB tolerance around the ceiling); these level checks are warnings because
recordings and crest-limited speech can legitimately miss the RMS target.
Listen to a pack before selecting it. The validator does not rewrite recordings.

---

## Authoring

### With piper (free, local)

`prompts/build.sh` is the reference pipeline: piper renders, ffmpeg
normalises loudness and emits both sample rates. Point it at a `pack.json`
instead of `prompts/manifest.json` and it produces a complete pack. Voice
models ship permissive, but check the specific model's licence before
publishing.

### With a commercial TTS service

Better voices, and the licensing has one clause that matters more than the
rest. Taking ElevenLabs as the common example (verify current terms — these
change):

- Paid plans include a commercial licence; the **free plan does not** and
  requires attribution. Starter is a few dollars a month.
- Audio generated while subscribed stays commercially licensed after you
  cancel.
- The commercial licence applies *provided you hold the intellectual
  property rights to the content*. **This is the load-bearing clause.** A TTS
  provider's licence covers their model's output; it cannot grant you rights
  to someone else's character, name, or voice.

Six prompts of roughly fifteen words each is a trivial character count — one
month of the cheapest tier renders dozens of packs. Marginal cost is
effectively zero, which is what makes this a sane thing to sell.

### With a voice actor

The premium option, and increasingly the differentiated one: better
performance, no AI-voice ambiguity, and "recorded by a human" is now itself
a selling point. Give them `pack.json` as the script.

---

## Rule: archetypes, not characters

**Do not build a pack around a named character, a living person, or a
recognisable performer's voice.** This is the one hard rule, and it is the
difference between a product and a cease-and-desist.

- **Fine:** public-domain figures and authors (Dickens died in 1870;
  Shakespeare's porter at the gate is literally a bouncer), and *archetypes* —
  the retired professional, the noir gumshoe, the bureaucrat, the drill
  sergeant, the ship's computer, the 1950s switchboard operator.
- **Not fine:** a named film or game character, an impression of a specific
  actor, a cloned voice of any real person without their written consent.

Trademark and right-of-publicity both bite here, and at least twelve US
states now have voice-cloning statutes (California, New York, Tennessee's
ELVIS Act among them). Major TTS providers prohibit cloning public figures
and have suspended accounts for it.

The creative cost of this rule is close to zero. "The Retired Professional —
quiet, courteous, and very good at endings" delivers the same joke as naming
a franchise, with none of the exposure. Write the archetype, not the
copyright.

---

## Pack kinds

`kind` in `pack.json` describes what the pack replaces:

- **`voice`** — the six lobby prompts. The core kind, works today.
- **`hold`** — music-on-hold beds. Works today via `musiconhold.conf`. Note
  you cannot ship music you do not own: commission it, or use CC0 sources.
  The stronger product is a themed hold *experience* — the archetype voice
  over a bed — which pairs with a voice pack.
- **`ringback`** — what an outside caller hears while the house rings.
- **`sfx`** — numbered clips for the sound board.
- **`rotation`** — numbered clips played at random (jokes, riddles, facts).
- **`greeting`** — voicemail greetings.
- **`story`** — an interactive audio story the caller steers by keypad,
  written as one Markdown file. Its own specification, because it is the one
  kind whose clip names are defined by the pack rather than by the binary:
  see **[STORY-PACKS.md](STORY-PACKS.md)**.

The local archive installer currently accepts only `voice` and `story`.
Other kinds and the story handset player need separate mechanisms — see
`docs/TASKS.md`.
The rotation mechanism in particular is small (a `RAND()` in the dialplan)
and unlocks every content pack that would otherwise be identical on repeat: a
joke is not a joke the second time.

---

## Licensing your pack

Community packs: CC BY-SA 4.0 keeps derivatives in the commons and matches
the bundled pack.

Paid packs: a short proprietary end-user licence — personal use in the
purchaser's own installation, no redistribution, no resale. Avoid CC-NC; its
definition of "non-commercial" is ambiguous enough to create support
questions you do not want for a small download.

State the licence in `pack.json` either way. See `LICENSES.md`.
