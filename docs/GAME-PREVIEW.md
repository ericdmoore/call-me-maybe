# Local multiplayer game preview

`doorman games` is an opt-in preview Stasis application, separate from the
ordinary lobby. It runs a local pack's `start`, `step` and optional `room_changed`
Starlark functions, plays pre-rendered audio, and collects keypad input. It
requires no hosted account, provider API or speech service.

The initial preview admits one game room at a time and up to eight explicitly
selected internal PJSIP handsets. Each handset is one seat. External callers,
recorded names, persistent scores and hot-seat rotation are future work. The
hot seat stays on the first handset; sideline handsets hear the same playback
and cannot submit answers. This is cooperative hot-seat play, not simultaneous
individual scorekeeping.

## Validate and run

Build the binary with `make build`. Prepare the pack offline, then:

```sh
doorman games check --pack /path/to/local-game
doorman games serve --pack /path/to/local-game \
  --handsets /path/to/handsets.toml --rooms kitchen,office
```

The runner reads `ARI_BASE_URL`, `ARI_USERNAME` and `ARI_PASSWORD` from its
process environment. ARI must remain on `http://127.0.0.1`; do not change its
bind address. Use the existing protected service environment file, rather than
credentials in a command line. The ARI app is `doorman-games-preview`; never
reuse the ordinary lobby's app name.

Copy the validated audio to Asterisk's sounds directory under the pack's exact
`media_prefix` (`games/<pack-id>`). Both 8 kHz `.wav` and 16 kHz `.wav16` mono
PCM16 files are required and checksummed. The `--pack` directory retains the
manifest and Starlark sources as well as the validated audio files. Make them
readable by the preview service and Asterisk, without granting either write
access. No synthesis or conversion runs during calls.

After checking for an existing `*4263` mapping, an operator may opt in through
a separate, hand-maintained include in `extensions.conf`:

```ini
; extensions_games_preview.conf -- internal handsets only
[internal](+)
exten => *4263,1,NoOp(Local game preview)
 same => n,Stasis(doorman-games-preview,new)
 same => n,Hangup()
```

Add `#include "extensions_games_preview.conf"` to the hand-maintained root
file, not a generated handset/trunk file, then reload only the dialplan. The
runner verifies Asterisk's `CHANNEL(endpoint)` against the selected handset
inventory, independent of caller ID or digits. The service must be running
and its ARI app registered before dialing `*GAME` (`*4263`). This preview is a
direct single-pack entry point, not the complete shared Games shelf in #116.

## Inviting and playing

The pack supplies spoken instructions for these phase-scoped menus:

- Lobby: `1` starts, `2` invites a room, `0` finishes.
- Room picker: stable inventory slots `1–8`, followed by each room's internal
  extension; `0` goes back. Joined seats are omitted. Busy/unavailable endpoints
  are not originated to. No user-entered phone number or arbitrary endpoint is
  accepted.
- Invited room: `1` accepts participation, including spoken help; `0` declines.
  Ringing is limited to 20 seconds and acceptance to 15 seconds. Unanswered or
  declined invitations do not become members.
- During a game keypad prompt: `9` opens the social menu; `1` invites another
  room, `2` opens the joined sideline microphones for up to 20 seconds, and `0`
  returns. During the discussion the hot seat can press `0` to end it early.
  Sideline microphones return to muted afterward. This preview does not yet
  enforce limited-use social lifelines or select one helper individually.

Menus play before their keypad windows open; buffered digits from earlier
playback and digits from other seats do not answer the new prompt. The game's
remaining prompt time is preserved across social actions, while the overall
20-minute room cap includes invitations, playback and discussion. The preview
ends and cleans up at that cap rather than resuming after a process restart.

The rules receive only admitted channel IDs through `room_changed`. Pending
invitations never increase the player count. Departures remove seats; the hot
seat leaving ends its room. Every session has deferred cleanup of its bridge
and still-owned call legs. `StasisEnd` detaches a channel without hanging it up,
so a handset transferred out of the application is not killed by cleanup.
Losing the ARI event connection aborts the preview; restart never adopts stale
input windows or existing calls. An event-queue overflow also cancels the room.

## Pack contract, version 1

`game.json` contains:

- `schema_version: 1`, `id`, and `media_prefix: "games/<id>"`.
- `entry` and `sources`: at most eight flat `.star` files, SHA-256 per file,
  no undeclared imports or cyclic loads. Sources are limited to 256 KiB each.
- `clips`: line ID to `{file, sha256, sha256_16, pause_ms}`. Files are exactly
  `<line-id>.wav` and `<line-id>.wav16`; paths and symlinks are rejected. Silence
  after a clip is bounded to ten seconds.
- `questions`: `{id, tier, answer}` entries for the host's random draw, with
  `tiers` (1–6), `per_tier` (1–2), and `openings` naming declared clips.
- `initial_args: ["$player", "$deck", "$keep_wrong", "$opening"]`: `start`
  receives the hot-seat channel ID, distinct questions in tier order, a random
  incorrect survivor per question, and a randomly selected opening.
- Optional `shuffle_answers: true`: each question also supplies `prefix` and
  optional `pins` mapping canonical option keys to fixed spoken buttons.
  All sixteen `<prefix>.choiceN.keyM` clips must be declared. The adapter uses
  the state's `deck` and `rung` to translate only `phase: "answer"` inputs,
  leaving confirmation/social controls intact. Reveals must not name a stale
  option number. Numeric options can pin to their matching buttons. Assignment
  remains fixed within a game, with an avoidable identical shuffle changed on
  the next game in the running preview process.

`start` and `step` return `{state, commands}`. State is an immutable dictionary;
`phase: "done"` drains final playback before cleanup. `step(state,event)`
receives a correlated `{request, kind:"digit", player, value}` event; timeout
uses `None`. Optional `room_changed(state, players)` returns updated state with
only admitted channel IDs. Supported commands are `say`, bounded `pause`, and
one final `ask_digit`; undeclared media or other effects abort the preview.
The host independently enforces player authority and input timing.

These are trusted, locally reviewed preview packs, not a signed marketplace
installer or an unrestricted-code sandbox. File and execution-step limits do
not provide a process memory sandbox for hostile Starlark sources. No pack can
name an outbound endpoint: the host selects only inventory-approved handsets.

## Tests and rollback

Run `make check` and `make cover`. Fake-ARI tests cover accepted and declined
invitations, busy rooms, shared playback, hot-seat authority, stale digits,
help mute restoration, transfer-safe teardown, and pack integrity. Before
calling a deployment handset-verified, use two real handsets to test the lobby,
invitation, acceptance, a question, help and hangup; then verify ordinary calls.

Rollback: stop the preview service, remove its hand-maintained dialplan include,
reload the dialplan, and remove only its preview media/binary. The ordinary
`doorman` service and existing lobby media are not replaced by this preview.

## Handset contact and refresh timing

For a box with this preview installed, run its directory process with
`doorman provision directory --games-preview` (preserving its existing state and
other flags). This adds **Games (*GAME)**, dialing `*4263`, to the House book.
Leave the flag off on boxes without a playable game preview. Phones that omit
the House book do not receive the entry.

The directory's push worker detects changed phonebooks on its one-second loop
and imports them to reachable supported handsets, with retries for busy or
unreachable phones. A successful `phonebook imported <handset>` log confirms the
phone accepted the updated book; the UI may need its Contacts view reopened.
The provisioned fallback poll interval is 60 minutes. A fresh provision or a
manual phonebook download also fetches the current directory. Updating the
preview directory process does not restart ordinary calls or the game process.

If a preview uses a directory-service drop-in pointing to a preview binary,
remove that drop-in and restart the directory service before removing the
preview files. Existing ordinary calling and game calls need no restart for a
phonebook update.
