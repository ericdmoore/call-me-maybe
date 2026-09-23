# Plans

One folder per work stream. `readme.md` is the phased plan; `arch.md`, where it
exists, is the reasoning — what was chosen, what was rejected, and which
invariant each decision protects.

These are **design documents, not a backlog.** `docs/TASKS.md` holds
prioritised work with acceptance criteria; these hold the thinking that would
otherwise have to be reconstructed from a commit log.

## Streams

| | Stream | Status | Depends on |
|---|---|---|---|
| **s01** | [Multiple lines](s01-multiple-DIDs/) — several numbers, then several providers | **Phase 1 done; Phase 2 all but M2.4** | — |
| **s02** | [Home and office config examples](s02-home-and-office-config-examples/) — worked, tested, published starting points | M1 + M2 shipped (`examples/scenarios/`) | s01 for two of five |
| **s03** | [Provider balance checking](s03-provider-balalnce-checking/) — know the trunk is dying before the phone stops | planned | s01 Phase 2 for per-trunk |
| **s04** | [Network helpers](s04-standarddize-network-helpers/) — TTS, STT and LLM addressed by URL, wherever they run | planned | — |
| **s05** | [System alerts](s05-system-alerts/) — the house phone tells you about the house phone | planned | composite prompts |
| **s06** | [Speakers as page targets](s06-speaker-page-targets/) — Sonos and Cast carry a page; they are not handsets | planned | TASKS §6 (HA webhook) |
| **s09** | [One binary that installs itself](s09-distro-software-release-channel/) — `sudo doorman init` prepares the host; brew, deb/rpm, AUR, `go install` | planned | s08 for the release notice |
| **s11** | [The hunt](s11-the-hunt/) — the verification ladder as a scavenger hunt: envelopes, `*6` + an answer plays a parent's recorded clue, `500` announces the winner; zero mechanism in doorman | M1–M2 shipped (v0.6.2); M3 needs a Saturday | — |
| **s12** | [Do not disturb](s12-do-not-disturb/) — `*78NN` quiets a handset for 15/30/45 min; rings and pages skip it, except a page from an override handset; state is Asterisk's, time-boxed, read by doorman never written | planned | per-handset voicemail; the `100` ring-all group; s10 for line keys |
| **s14** | [411 — the phone explains itself](s14-411-feature-discovery/) — one registry of everything you can dial; a generated, checkable IVR at `411`; the runbook table, a wall card and the soft keys from the same source | planned | the graph-provenance primitive; composite prompts |
| **s15** | [Messages — the house answers texts](s15-messages/) — SMS on the house number: email as the archive, a URL callback through Tailscale Funnel as the trigger; words per person open the garage via HA; `menu` types the `411` tree; handsets text each other over SIP MESSAGE; nothing on the hub | planned | s13 for the journal event; s14 for `menu` |
| **s16** | [The family voice pack](s16-family-voice-pack/) — the bundled voice is the floor; a `family/` overlay one layer deep, recorded from any handset with `*99` (menu) or `*99*N` (straight to one); `#` inside the call drops back to the default; the studio's own lines are an optional second clip set | planned | — |

s01 has an [`arch.md`](s01-multiple-DIDs/arch.md); s03's reasoning is short
enough to live in its plan.

### Archived

| | Stream | Closed |
|---|---|---|
| **s08** | [Durable event journal](_archives/s08-durable-event-journal/) — SQLite history, doorbells, CEL capture, consumer-owned replay | 2026-09-23 — verified live on jepsen; replicas and the HTTP endpoint dropped with reasons |
| **s10** | [Zero-touch handsets](_archives/s10-zero-touch-handsets/) — `render` writes the phone's own file; `doorman provision` is the window, the instructions and the watch; `rotate --phones` re-provisions; the directory unit is the phone book | 2026-09-23 — shipped in v0.6.0 under test; the live rehearsal rides with the jepsen re-provision |
| **s07** | [The contacts ladder](_archives/s07-contacts-ladder/) — address books as admission: vCards parsed and classified, the five-rung ladder, `url` sources fetched into a last-good cache off the call path, `via` on the call record | 2026-09-23 — shipped in v0.6.1; nothing on the box to touch |

---

## Primitives discovered while planning

Small pieces of mechanism that more than one stream turns out to need. Recorded
here so they get built once, deliberately, rather than three times slightly
differently.

### Composite prompts — **three customers**

A prompt assembled from parts rather than one clip: a pre-rendered WAV, then
`digits:` (Asterisk reads a number natively), then another WAV.

`internal/lobby/prompts.go` maps one name to one clip today. Invariant 7 is
untouched — `digits:` is not speech synthesis — but the shape is new.

Wanted by: reading a line's own number back in a greeting (s01) · "call from
area code XXX" in a screened answer · speaking a balance aloud (s03) · "do not
disturb for thirty minutes" (s12) · "the theater is extension one-oh-two" (s14).

### Variable-length, `#`-terminated collection — **two customers**

`collect()` gathers exactly `ExtensionLength` digits for PIN matching. Some
flows need "digits until `#` or timeout" instead.

Wanted by: capturing a callback number in an answering-service flow · taking
the number to dial in the `*4` outbound console (s01 M1.3).

### DTMF from an originated leg

The router does `reg.caller(ev.Channel.ID)` — digits pressed on a *handset* leg
are dropped today. `reg.byChannel` already matches legs; the blocker is that
`Session.Dtmf(digit)` carries no channel, so a session cannot tell which leg
pressed.

Wanted by: screened answer ("press 1 to accept, 2 for the answering service").
`Play(channelID, …)` already takes a channel, so whispering to one leg needs no
ARI additions — only this.

### Value-or-reference fields

A field that accepts either the thing itself or the name of something defined
elsewhere. Most config languages land here — Compose, Kubernetes, Terraform,
ESPHome — because it is how people actually think about reuse: inline it when
it is used once, name it when it is shared.

**Precedent already in the tree.** `pack.json`'s `voice` accepts a bare string
(a piper model) or an object naming a backend; `Manifest.Resolve()` tries
string-then-object. The polymorphism is not foreign here, it was chosen once
already.

Wanted first by `afterhours`, which today is only ever a reference to a
`[[schedules]]` id:

```toml
afterhours = "school-nights"      # reference — defined once, reused

[extensions.afterhours]           # inline — this extension only
start = "20:30"
end   = "07:00"
days  = ["SU","MO","TU","WE","TH"]
```

The same shape already exists unnamed elsewhere: `handsets = ["kitchen"]`
takes a handset id *or* a group id, both plain strings.

**It also shrinks the typo surface.** An inline schedule has no id to
misspell — no dangling reference, no defined-but-unused block, nothing in the
silent-failure class below. Not a substitute for `Undecoded()`, but pushing the
same direction.

**Consequence to accept up front:** `doorman check` has to print the resolved
schedule identically whichever form produced it. That is the same output work
as showing applied defaults, so one change, two payoffs.

#### Rejected: a general reference syntax

ESPHome-style `!secret` / `!include` / `!lambda` — YAML tag machinery for
referencing values across and within documents — is the obvious next step and
should not be taken.

Once there is a general resolver it needs cycle detection; then `!include` for
splitting files; then interpolation; then somebody wants a conditional, and the
configuration is a programming language with none of the tooling of one.

The line is already drawn elsewhere in this project and should stay in the same
place: `internal/tmpl` uses **references, not interpolation**, specifically so a
template cannot inject anything, and the docs say "templates as data, not text."

| | |
|---|---|
| A field accepts two shapes | bounded, per-field, a `oneOf` in the schema. **Yes** |
| A general `!ref` / `!include` | unbounded, needs a resolver, invites the next four features. **No** |

### Graph interpreter: routing verbs by provenance

`internal/story` parses, validates and interprets a node graph against a
`Teller` interface. A shallow day IVR is the same engine plus verbs that route
a caller to a handset or mailbox — precisely what the story sandbox refuses to
have.

The resolution is that the two graph types differ by **provenance, not shape**:
story graphs come from packs (downloaded, untrusted, sandboxed, no routing);
IVR graphs come from `policy.toml` or from `render` itself (operator- or
renderer-authored, trusted, may route). Same interpreter, verb set selected by
where the graph came from. **First customer: s14's `411` menu**, generated by
`render` from the feature registry and the inventory.

---

## Known defects found while planning

Not features. Things that are wrong now.

### Dry-run and `go test` never reach the apply path of the installers

`install-scripts/` and `install.sh` are tested by dry runs and stubbed
commands; the first live run on real hardware (jepsen, 2026-09-21) found one
apply-only defect per release for three releases. Any provisioning code that
cannot run its apply path in CI will keep doing this. s09 moves the apply into
Go behind a fakeable `Exec` and adds a tag-only rehearsal on a real runner.

### Rerunning `install-scripts/ubuntu.sh` after an upgrade refuses on a doc

`docs/INSTALL-LINUX.md`, `scripts/smoke.sh` and `scripts/cel-spool.sql` are
copied with `install_once` (refuse on any difference), which is the rule meant
for the binary, the unit and the Asterisk templates; every other doc goes
through `rsync --ignore-existing`. The first upgrade (v0.5.1 → v0.5.2) stopped
on the README. Superseded by s09 M3, where the scripts are deleted; fix it
directly only if a release has to ship before then.

### Unknown keys in `policy.toml` are silently ignored

`on_no_input = "voicemail"` passes `doorman check` with a clean bill of health
and does nothing. So does a typo — `handets = ["kitchen"]` validates and rings
nobody.

This is the failure mode the invariants exist to prevent: software that looks
like it is working. It also makes the "do not emit unbuilt keys" section of
`llms-policy.txt` load-bearing, because a model writing `[line]` today produces
a config its owner will believe is live.

`BurntSushi/toml` exposes `md.Undecoded()`, so rejecting or warning is small.
The decision needed first is **reject or warn**, given invariant 4 — a reload
that starts refusing previously-accepted files could take a working phone
offline on upgrade, which argues for warn-then-reject over two releases.

---

## Conventions

- Number streams `sNN`; do not renumber when one is dropped.
- A stream moves to `_archives/` — same folder name, same number — when every
  milestone has either landed or been explicitly dropped in its status line.
  Partly-shipped streams stay here: a plan with one open milestone is still
  the place that milestone is reasoned about. The table above lists archived
  streams under their own heading so the numbering stays legible.
- A plan says what "done" looks like before it says how.
- Record **rejected** alternatives with the reason. The reason is the valuable
  part and it is the thing that gets lost.
- Note which invariant each decision protects, and say plainly when one is
  being generalised rather than pretending nothing moved.
