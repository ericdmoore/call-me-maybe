# s24 · *88 — add a number from the phone in your hand

**Status:** M1–M4 built 2026-10-02 (`make check` green); M5 (jepsen) next. Planned and started 2026-10-02, the evening after the curfew
(s23) went live, when the next question was "how does a kid put a friend's
number in her phone". Decisions settled with the user the same evening: the
entry lands in the dialling phone's **own** book; the name is transcribed by
a whisper-style HTTP service on the tailnet (`alpaca`), never on the call;
until then the entry is named by its number.

## What done looks like

Norah picks up her handset, dials `*88`, keys a number and `#`, hears a
beep, says "Maddie", hangs up. Within a minute "Maddie's" number is in the
directory on her phone — under the number itself at first, then under
"Maddie" once the box has had the name transcribed. Nobody else's phone
changes, nobody is admitted to ring the house, and if the transcription
service is off for a week the number is still there to dial.

## Decisions and invariants

**The call is three Asterisk applications and nothing of doorman's.** `*88`
is `Answer`, `Read` (digits until `#`), `Record` (the name, silence-stopped,
15 s at most), `Playback`, `Hangup`. The recording lands in a spool
directory named after the handset, the time and the digits. Invariant 7
(no speech service between a call and anything) holds because speech is
never *on* the call: the box transcribes later, off the call path, and the
lobby never imports the package that does it (asserted by test, like
`provider` and `inbox`).

**`*88` is generated, in `[handsets-internal]`.** Every other star code is
hand-written in `[features-internal]`, which the installer copies once and
refuses to overwrite. This one carries a path from configuration, and a
generated file upgrades by re-rendering. The curfew contexts forward star
codes to `[internal]`, so a sleeping phone can still file a number — no
call leaves the house.

**Filed at once, named later.** `doorman phonebook` — a sibling job on a
one-minute timer, like `doorman inbox` is a sibling of the daemon — reads
the spool and writes the number into the phone's own book immediately, with
the number as its name. Then it asks the STT service and renames the entry
in place when the text comes back, and deletes the audio. A dead service
leaves the recording to retry; after seven days the job gives up on the
name, keeps the number, and says so in the log. The number is never held
hostage by the name.

**The own book is directory-only and implicit.** A phone's additions are a
vCard file per handset under `PHONEBOOK_DIR/own/`, served to that phone by
the directory as a third book beside house and people — no `phonebook`
entry needed, because what you added yourself is not something to opt into.
It is *not* a `contacts.toml` source: those are admission (a personal
contact skips the lobby), and a child filing a friend must not open the
front door. Rejected: writing to `[[people]]`, for the same reason.

**STT is addressed by URL and nothing else.** `STT_ENDPOINT` (the
OpenAI-compatible `/v1/audio/transcriptions` shape `.env.example` has named
since Phase 2), `STT_MODEL`, `STT_TIMEOUT_MS`, read by the job only — the
daemon parses and ignores them as before. `internal/stt` is the narrow
package s04 asked for (one method, one HTTP backend); the exec backend s04
sketched can come when someone has the CLI. No API key on the box.

**Journal.** `phonebook.added` when a number is filed and `phonebook.named`
when its name lands, reason = handset id, through the sibling writer. The
digest can then say "Norah added two numbers". Never the number, never the
transcript.

**Rejected: `Read` with the prompt as its argument.** If the clip is
missing (packs built before it) `Read` fails rather than reading, so the
prompt is a separate `Playback` that degrades to silence, and `Record`'s
own beep is the cue that always exists.

**The spool is its own directory, `/var/spool/call-me-maybe`.** Found on
jepsen the moment the dialplan was live: the daemon's state directory is
0700 to doorman (every unit pins `StateDirectoryMode`) and Asterisk's spool
is closed to doorman, so a spool under either is unreachable by the other
account. One directory of their own — asterisk-owned, doorman group, setgid
— is the smallest thing both can use, and the installer creates it.

**Apple's engine, not Whisper, on alpaca.** The user pointed at SpeechAnalyzer
(macOS 26); nothing served it over HTTP, so `tools/speechd` does — the same
endpoint shape, 250 ms for a phone-quality clip on this Mac. Whisper remains
what the client speaks; the server behind it is nobody's business.

## Milestones

- **M1** `internal/ownbook` (vCard subset with UID: load, upsert, rename,
  atomic save) and `internal/stt` (Whisper over HTTP), both with tests and
  the import-boundary test.
- **M2** render: `*88` in `[handsets-internal]`, `*88` in the house
  features list, `PHONEBOOK_DIR` honoured; the three clips in the manifest.
- **M3** `doorman phonebook`: spool → file → transcribe → rename → delete;
  `doorman-phonebook.{service,timer}`; installer and runbook unit lists;
  journal events.
- **M4** the directory and render serve the own book; `bookCache` watches
  the own files.
- **M5** jepsen: spool directory (asterisk writes, doorman reads), timer
  enabled, `STT_ENDPOINT` → alpaca, a number filed from Norah's phone.
