# Call Me Maybe — working notes

A programmable home phone on a Raspberry Pi. Asterisk does SIP/RTP/DTMF; a Go
daemon called **doorman** talks to it over ARI and decides who gets connected.
Known callers ring the whole house. Everyone else gets a lobby, dials a 6-digit
extension, or is dismissed.

The project's home is <https://callmemaybe.cc>.

Read `README.md` for the architecture diagrams and `docs/architecture.md` for
the reasoning behind the big choices. `docs/PACKS.md` is the pack format and
the mechanisms/content line, with `docs/STORY-PACKS.md` for the one kind whose
clip names come from the pack rather than the binary; `docs/SUSTAINABILITY.md`
is the honest read on what can and cannot pay for itself. Operational procedures — provisioning,
verification, troubleshooting, day-2 tasks — are in `docs/RUNBOOK.md`. The
prioritised backlog with acceptance criteria is `docs/TASKS.md`.

## Commands

**Start with `./bin/doorman schema`.** It prints the entire configuration
surface — every key, type, default, and cross-file reference for
`policy.toml`, `handsets.toml`, `trunks.toml`, and the environment — as JSON
Schema. Faster and more reliable than reading `internal/policy` to work out
what a valid config looks like. `doorman schema policy|handsets|trunks|env`
narrows it. It
describes *shape*; `doorman check` remains the authority on *validity*,
because JSON Schema cannot express the cross-file references or the ~30
semantic rules (those appear as `x-cross-references` and `x-rules`).

```bash
make check                 # gofmt + vet + lint + test + build; green before any commit
make cover                 # -race plus per-package coverage floors
make lint                  # nilness + the no-secrets-in-logs analyzer
make hooks                 # install the pre-push gate (once per clone)
make build                 # → bin/doorman for the host
make cross                 # → bin/doorman-linux-{arm64,armv7} for the Pi
make man                   # read the man page from the working tree
make run                   # dev run with .env sourced (needs reachable Asterisk)
./bin/doorman schema       # the config surface as JSON Schema — read this first
./bin/doorman check        # validate policy.toml, print what it resolves to
./bin/doorman rotate       # rotate extension PINs (all, or by label)
./bin/doorman render       # handsets.toml (+ trunks.toml) → Asterisk config
./bin/doorman lsp          # language server for the config files (stdio)
./bin/doorman e164 <num>   # show how a raw caller ID normalises
./bin/doorman balance      # prepaid credit per trunk; exit 1 under threshold
./scripts/smoke.sh         # full deployment verification, run ON the Pi
```

`llms.txt` in the repo root is the orientation file for models arriving
without this context; `docs/doorman.1` is the man page. Both lead with
`doorman schema`. If you change the config surface, all three plus
`internal/schema` need to agree — the tests in `internal/schema` fail when
they do not.

## Environment

Go, stdlib-first: `BurntSushi/toml` and `gopkg.in/yaml.v3` for configuration
and templates, `gorilla/websocket` for the ARI event stream. The deliverable is
one static binary — no runtime on the
Pi, no node_modules, nothing to install but the file. This project is a
deliberate exception to the owner's TypeScript-first standard: it is a leaf
appliance, unrelated to the other product codebases, and boring-by-design.

The module is named `callmemaybe` pending a public home; when the repo gets a
real URL, rename in `go.mod` and the `callmemaybe/internal/...` import paths.

Layout:

- `cmd/doorman` — entrypoint, subcommands, and the event router mapping ARI
  events to sessions.
- `internal/lobby` — every decision this system makes. `session.go` is the
  whole call state machine, one goroutine per call. `fake_ari_test.go` is the
  harness that drives it without an Asterisk.
- `internal/policy` — TOML schema, E.164 normalisation, hot reload, PIN
  rotation.
- `internal/ari` — thin typed ARI client: REST + reconnecting WebSocket.
- `internal/lsp` — language server for the config files; diagnostics
  reuse `policy.LintSplit`, one problem per squiggle. stdout is protocol —
  never print to it in lsp mode.
- `internal/render` — generates per-handset Asterisk config from
  handsets.toml, and the trunk registrations plus inbound contexts from
  trunks.toml; secrets substituted from env, never stored in either file.
- `internal/provider` — what doorman knows about the companies carrying its
  calls, which today is one question: how much credit is left. Optional
  interfaces in the `internal/voice` shape — a prepaid provider implements
  `Balance`, an invoiced one does not and says so. **Reached only from
  `doorman balance`.** Nothing under `internal/` may import it and no other
  file in `cmd/doorman` may name it, both asserted by test: a provider API key
  manages DIDs, sub-accounts and billing, so it stays out of the daemon
  entirely. Its errors never carry a URL — the credentials ride in the query
  string, so the URL *is* the credential.
- `internal/notify` — the event webhook. One endpoint, one JSON event per
  ring and per completed call, so Home Assistant can announce or flash
  something. Same non-blocking shape as `internal/calls`, and doorman
  deliberately knows nothing about speakers — HA decides who hears it.
- `internal/contacts` — address-book exports turned into an admission
  decision: vCard parsing, E.164 normalisation, the published/personal
  classifier, and the merge across sources. The lobby reads it through a
  one-method interface on `Deps` (`lobby.Contacts`), so the state machine
  never imports this package — the adapter in `cmd/doorman` is the one
  translation point, exactly as it is for ARI. Derived and disposable:
  deleting every source only sends those callers back to the lobby.
- `internal/config` — env parsing; names match `examples/.env.example` exactly.

Config interfaces: `.env` (secrets + tuning), `handsets.toml` (hardware
inventory — source of truth for the generated Asterisk config), `policy.toml`
(rules: allow-list, extensions, ladders, `[[schedules]]`), and optionally
`trunks.toml` (the providers — source of truth for the generated trunk config
and inbound contexts) and `contacts.toml` (the address books to read — the
ambient list, where `[[people]]` is the deliberate one). Each file owns its
sections exclusively; the loader rejects sections in the wrong file rather than
merging and names the file they belong in, and the legacy single-file layout
still loads.

**No `contacts.toml` is likewise the normal state and a compatibility gate.**
Without one nothing is read, nothing is logged, and the admission ladder is the
two tiers it has always been: `[[people]]`, then the lobby. With one it becomes
five — a block source, `[[people]]`, a personal contact, a published contact,
everybody else — first match wins. Block beats `[[people]]`, and a number in
both fails `doorman check` rather than being ranked in silence.

**No `trunks.toml` is the normal state and the compatibility gate.** Without
one, `doorman render` generates only the handset files and the hand-written
`asterisk/pjsip.conf` keeps working — which is every single-provider install.
With one, the generated dialplan routes inbound DIDs by `[line] trunk` and
outbound by the same, and `emergency_trunk` settles which trunk carries 911.
The daemon still never reads the inventory — it hands `[line] trunk` to the
dialplan as a string, so a dangling reference is reported, never fatal.

## Hard invariants

Break these and the phone fails in ways that look like working software.

1. **Never log a full caller ID at info or above** unless
   `LOG_REDACT_CALLER_ID=false` is explicitly set — use `policy.Redact`. And
   **never log entered digits or PINs at any level**: a near-miss is almost a
   credential. Rotation prints new PINs to stdout for the operator; that is
   the only place a PIN may ever be printed.
2. **Never widen the ARI HTTP bind past `127.0.0.1`.** ARI is unauthenticated
   in effect — the password is in a plaintext config file. If doorman ever
   moves off-box, it goes over the tailnet, never the LAN.
3. **Session teardown is the deferred `cleanup` in `Run`, and cancellation is
   the state.** The caller hanging up cancels the session context; every wait
   in the state machine selects on `ctx.Done()`. Do not add state flags, and
   do not add an exit path that skips the defer.
4. **An invalid `policy.toml` must never take the phone down.** The store
   keeps the last good policy on a failed reload, and `RotatePins` validates
   before writing and writes atomically. Preserve both properties on any
   change to those paths.
5. **PIN comparison stays exact-match against a map.** No prefix matching, no
   fuzzy fallback. Extensions are credentials; `RotatePins` generates them
   with crypto/rand for the same reason.
6. **Failed PIN attempts always call `Limiter.Failure`.** The bouncer's only
   defence against a redialler is that budget.
7. **Prompts are pre-rendered WAVs, never runtime TTS.** A speech service must
   never sit between an inbound call and a greeting.
8. **StasisEnd and ChannelDestroyed are different events and must stay
   different.** StasisEnd on the caller means "left our app, possibly alive"
   (handset transfers look like this) and routes to `CallerLeft`, which
   suppresses the final hangup; ChannelDestroyed means dead and routes to
   `CallerGone`. Collapse them and every transfer drops the call the moment
   it completes — and so does every voicemail handoff, which uses the same
   detach: `sendToVoicemail` releases the caller into the dialplan via
   ContinueToDialplan and nothing may hang the channel up afterwards. See
   `TestHandsetTransferDoesNotKillTheTransferredCall` and
   `TestLadderEscalatesKidsToAdultsToVoicemail`.
9. **`dtmf_mode=rfc4733` on the trunk *and* every handset.** Without it the
   lobby is deaf and every stranger is dismissed. No runtime symptom other
   than "nobody can ever get in".

9b. **Every trunk registration carries `line=yes` *and* `endpoint=<id>`.**
   That pair binds inbound traffic on a registration to its endpoint, which
   is what removes the need for an `identify` block, a provider IP allow-list
   and any port forward. Without both, inbound calls hit the `anonymous`
   endpoint and vanish — no error, no log line, just a number that never
   rings. `internal/render.registrationBinding` is the one place that emits
   them and `TestEveryRegistrationBindsInboundToItsEndpoint` is the guard.
   Do not generate an `identify` block instead: an IP allow-list goes stale
   silently the day a provider adds a media server.

11. **A call never leaves by a trunk that does not own the number it
   presents, and emergency calls leave by the designated trunk or fail
   loudly.** Presenting a caller ID a provider does not own gets the call
   rejected or silently rewritten, so `[line] outbound_cid` and
   `[line] trunk` travel together as one value (`render.OutboundIdentity`)
   and `doorman check` refuses a config that contradicts itself. For 911:
   the designated trunk is tried first and unconditionally — **never gated
   on a liveness check**, because a provider that does not answer OPTIONS
   looks unreachable while working perfectly, and diverting an emergency
   call off the one trunk whose street address is filed is the worst
   available failure. An unregistered trunk answers CHANUNAVAIL in
   milliseconds, which is the same conclusion reached honestly. The
   fallback ladder then tries every other trunk before failing audibly.
   `_911` lives in `[internal]`, outside the context the `*4` console
   releases into, so the console cannot reach it topologically rather than
   by convention.

10. **The call log is never an input.** Nothing on the call path may read
   `calls.jsonl` to decide anything. The moment something does — "this
   number has called five times, admit it" — the rate limiter's
   deliberately-in-memory design is undermined and doorman has acquired
   persistent state that can be corrupt, stale, or disagree with
   policy.toml. The import direction enforces it: `internal/calls`
   imports `internal/policy`, so policy can never import calls without a
   cycle. It also holds full caller IDs, which is the point of a call log
   on a telephone — the file is 0600 and `doorman calls` redacts by
   default. Entered digits and PINs are still absolute: a record says
   whether a PIN was valid, never what was typed.

9a. **Generated files are outputs.** `asterisk/generated/*` and the
   installed `*_handsets.conf` and `*_trunks.conf` come from `doorman
   render`; never hand-edit them or commit them (both PJSIP ones hold real
   passwords). Change handsets.toml or trunks.toml and re-render.

## Conventions

- Stdlib first because it is usually enough and never needs updating —
  `log/slog` for logging, `net/http` for REST, `flag` for subcommand flags —
  not because dependencies are suspect. Add one when it makes the thing more
  useful, weighing maintenance, licence, and whether it runs on a call path.
  Do not refuse a useful library to protect a dependency count.
- The event router (`route` in main) runs on the websocket read goroutine and
  must never block; sessions consume through buffered channels and `post` is
  non-blocking by design.
- `internal/lobby` does not import `internal/ari`. The `ARI` interface and the
  mirrored `OriginateParams` exist so the state machine is testable with a
  fake; the adapter in main is the one translation point. Keep it that way.
- Teardown errors are ignored on purpose (`_ =`): a channel that is already
  gone is not an error worth propagating. Do not "fix" these.
- Tests use the stdlib `testing` package only, colocated with the code. State
  machine tests go through `fake_ari_test.go`'s harness and use real-but-short
  timeouts (tens of ms), not a fake clock.
- Comments explain *why*, not *what*.
- British spelling appears in identifiers (`normalise`); match the surrounding
  file rather than converting.

## Development without a phone system

`make run` fails fast at the ARI ping when there is no Asterisk. That is
correct behaviour, not a bug to work around. The entire state machine is
testable without one — `internal/lobby/session_test.go` covers known-caller,
valid/invalid PIN, timeouts, barge-in, ring-group races, and mid-call hangup
through the fake. Extend those tests rather than adding anything that needs a
live trunk; never add integration tests requiring a real Asterisk to CI.

## Things that are deliberately absent

- No SIP stack of our own. Asterisk owns the protocol. Resist any change that
  starts parsing SIP in Go.
- No database. Policy is a file; rate-limit state is in memory and is *meant*
  to be lost on restart.
- No third-party ARI library. The client is ~300 lines covering exactly the
  surface we use, and owning it keeps the interface small enough to fake.
- No separate lobby and bouncer services. They are two branches of one state
  machine.
- No voicemail *in doorman*. Asterisk's `app_voicemail` does the recording,
  storage, WAV-attached email and MWI; doorman only releases the caller into
  `[voicemail-drop]` via ContinueToDialplan with `MAILBOX` set. The
  `VOICEMAIL_*`/`STT_*`/`SMTP_*` keys in `.env` are read by Asterisk and the
  notify hook, never by doorman — which is why they look unused here. See
  `docs/TASKS.md` §2; transcription is the only piece still open.

## Licensing and packs

Code is Apache 2.0; bundled audio is CC BY-SA 4.0; the pack format spec is
CC0. `LICENSES.md` is the authority — keep it accurate when adding files, and
do not assume the root `LICENSE` covers audio. There is deliberately no CLA.

**Mechanisms are free, content is optional.** Every feature must work
completely with the bundled prompt pack. A mechanism that requires a
purchased pack to function is a crippled program and must not be built —
see `docs/PACKS.md`.

**The prompt-name contract is load-bearing.** The six names in
`internal/lobby/prompts.go` are what make packs interchangeable. Adding one
is a breaking change for every existing pack: bump it deliberately, document
it in `docs/PACKS.md`, and never let a pack half-work.

**Packs never contain named characters, living people, or impressions of
recognisable performers** — archetypes only. Trademark and right-of-publicity
exposure, plus TTS provider terms. Reject contributions that cross it.

## Secrets

`.env`, `policy.toml`, `handsets.toml`, `trunks.toml`, `asterisk/pjsip.conf`,
and `asterisk/ari.conf` are gitignored; all but `trunks.toml` contain real
credentials, and that one is gitignored because a sub-account username is
local inventory too. The `.example` variants are the committed ones. Never
commit a real VoIP.ms sub-account password, trunk password, handset password,
ARI password, or a real PIN — including in a test fixture or a commit message. Test fixtures use `555-01xx` numbers, which are reserved for
fiction.
