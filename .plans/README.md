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
| **s01** | [Multiple lines](s01-multiple-DIDs/) — several numbers, then several providers | planned | — |
| **s02** | [Home and office config examples](s02-home-and-office-config-examples/) — worked, tested, published starting points | M1 + M2 shipped (`examples/scenarios/`) | s01 for two of five |
| **s03** | [Provider balance checking](s03-provider-balalnce-checking/) — know the trunk is dying before the phone stops | planned | s01 Phase 2 for per-trunk |
| **s04** | [Network helpers](s04-standarddize-network-helpers/) — TTS, STT and LLM addressed by URL, wherever they run | planned | — |
| **s05** | [System alerts](s05-system-alerts/) — the house phone tells you about the house phone | planned | composite prompts |
| **s06** | [Speakers as page targets](s06-speaker-page-targets/) — Sonos and Cast carry a page; they are not handsets | planned | TASKS §6 (HA webhook) |
| **s07** | [The contacts ladder](s07-contacts-ladder/) — your address book admits people; published numbers still dial in | planned | — |

s01 has an [`arch.md`](s01-multiple-DIDs/arch.md); s03's reasoning is short
enough to live in its plan.

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
area code XXX" in a screened answer · speaking a balance aloud (s03).

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
IVR graphs come from `policy.toml` (operator-authored, trusted, may route).
Same interpreter, verb set selected by where the graph came from.

---

## Known defects found while planning

Not features. Things that are wrong now.

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
- A plan says what "done" looks like before it says how.
- Record **rejected** alternatives with the reason. The reason is the valuable
  part and it is the thing that gets lost.
- Note which invariant each decision protects, and say plainly when one is
  being generalised rather than pretending nothing moved.
