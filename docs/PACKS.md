# Packs

A pack is a folder of audio the phone plays. The bouncer's personality is the
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

A pack is a directory with a manifest and one pair of audio files per prompt.

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

### Installing

Packs are directories under Asterisk's sounds tree, selected by the
`PROMPT_MEDIA_PREFIX` environment variable. No code change is needed; this
already works today.

```bash
sudo mkdir -p /var/lib/asterisk/sounds/victorian-doorman
sudo cp victorian-doorman/*.wav* /var/lib/asterisk/sounds/victorian-doorman/
sudo chown -R asterisk:asterisk /var/lib/asterisk/sounds/victorian-doorman

# .env
PROMPT_MEDIA_PREFIX=victorian-doorman
```

Restart doorman. To switch back, change the variable back — packs sit
side by side and cost nothing but disk.

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

The last three need mechanisms that do not exist yet — see `docs/TASKS.md`.
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
