# s14 · 411 — the phone explains itself

**Status:** planned (2026-09-22). Drafted the morning after the first customer's
phone rang, when the count of things a person could dial had reached a dozen
and the only place they were all listed was a hand-maintained table in the
runbook that already disagreed with the dialplan.

## What done looks like

Anyone picks up any handset and dials **`411`**. A voice says what this phone
is, what the other phones are and their numbers, and what else the house can
do — page, party line, voicemail, the games, do not disturb, the actions —
each as a menu the caller can step into, hear the code for, and in most cases
*do* right there ("press 1 to page now"). Nothing in it is typed by hand: the
menu is generated from the same inventory and policy the dialplan is
generated from, so a room added to `handsets.toml` is in the menu on the next
render and a feature that is not configured is not offered.

The same registry prints `doorman features` on the box, renders the
"Handset features" table in the runbook (a test fails when they disagree),
prints a card per handset for the wall next to it, and labels the phones'
soft keys through s10. Four surfaces, one source. A house guest can use the
phone without being told anything except "dial 411."

## Starting point

- **Dial codes live in three places that do not know about each other:**
  `asterisk/extensions.conf` (`500`, `600`, `*97`, `*4`, `9196`, `555`,
  `_911`), the rendered handset dialplan (`101`…), and the runbook's table
  (`RUNBOOK.md` → "Handset features", which also lists `100` "ring every
  handset" and five example rooms). s11 adds `*6`, s12 adds `*78`/`*79`, s13
  adds `*3`. Each stream would add a row by hand, and the table is already
  the kind of document that rots.
- **The graph interpreter exists** (`internal/story`) and the plans README
  records the primitive this stream is the first customer of: the same
  interpreter runs *story* graphs (from packs; sandboxed; no routing) and
  *IVR* graphs (operator-authored; trusted; may route), the verb set chosen by
  provenance. A discovery menu is an IVR graph, and its provenance is the
  renderer.
- **Prompts are pre-rendered and the pack contract is six names.** A menu
  needs more clips than six, and needs to *say room names*, which no pack can
  know. Composite prompts (`digits:`) cover the numbers; the names need a
  per-house build or a family recording.
- `doorman check` already prints what a policy resolves to; `doorman schema`
  already proves that the config surface and its documentation agree by test.
  This stream applies the same idea to the phone.

## Decisions and invariants

**One registry of what you can dial, inside the binary.** Every feature code
is an entry: id, code, label, one-line description, the clip that names it,
and whether it is *built in* (`500`, `*97`), *rendered from inventory* (the
rooms, `100`), or *present only when configured* (the hunt line, DND, an
action extension, a story). `render` emits the dialplan *from* this registry
for the built-in codes it owns, and a test walks the rendered dialplan and
fails on any `exten` that is not in the registry — no undocumented feature
codes, in the same way `internal/schema` allows no undocumented config keys.
The runbook table becomes generated output with an agreement test, like
`llms.txt`.

**`411` is the code.** Directory assistance, for the people who remember it,
and short enough for the people who do not. It is a rendered extension in
`[internal]` — reachable from a handset, never from the lobby (the lobby has
its own greeting, and a stranger is not owed a tour of the house). It enters
the graph interpreter with operator provenance.

**The menu is a graph, generated, checkable.** `render` writes the 411 graph
from the registry and the inventory; the interpreter runs it with the
*trusted* verb set: `say`, `choice`, `goto`, and the routing verbs the story
sandbox refuses — `dial <code>`, `page`, `ring <handset>`. Because it is
generated it is stateless and every path is walked at build time, exactly as
story packs are checked. A leaf that would route to something not configured
is not generated, so the menu never offers a door that is not there.

**Numbers are spoken by `digits:`; names need a source.** "The theater is
extension one-oh-two" is a clip, `digits:102`, a clip. "Theater" itself is
not in any pack. Two sources, both pre-rendered, both invariant 7:
`doorman pack build --house` renders the house's labels with piper on the
workstation into a small house-specific pack; or the family records each
room's name from a handset with the same greeting-record mechanism s11's
`*99` uses, which is the version a child prefers. Missing name clips degrade
to the number alone, never to silence and never to runtime speech.

**The help clips are a second, optional contract.** The six prompt names stay
the load-bearing contract for the lobby. The discovery menu's clips ("to
hear the rooms, press one") are a separate documented set that a voice pack
*may* ship; when it does not, the built-in pack's copies are used, and when
those are missing too the menu still works with numbers and shorter
sentences. Nothing about 411 can make a pack half-work.

**It is also paper and plastic.** `doorman card <handset>` prints the same
registry as a one-page card: this phone's number, the other rooms, the codes,
in the s11 kit's style, for the wall by the phone. And s10's template assigns
the phones' soft keys from the registry — a key labelled "Page," a key
labelled "Help" that dials 411 — so the menu is one press, not a memory.

**Doing beats describing.** Wherever a leaf can act, it does: "press 1 to
page the house now" pages; "press 2 to ring the theater" rings. A menu that
only tells you a code you then have to hang up and dial is a manual read
aloud.

## What is deliberately out of scope

- **Speech recognition.** `555` reaches Home Assistant's Assist for people
  who want to talk to the house; 411 is keys and pre-rendered voice, so it
  works with the daemon's guarantees and no network.
- **Runtime TTS for names.** Invariant 7. Names are built or recorded.
- **Reaching 411 from the lobby.** A stranger gets the lobby. A known caller
  from outside could be offered it later; not in this stream.
- **Per-caller menus.** Everyone hears the same house. DND and actions
  appear or not by whether they are configured, not by who is asking.

## Milestones and acceptance criteria

### M1 · The registry

`internal/features`: the registry type, the built-in entries, the rendered
entries derived from inventory and policy. `doorman features` prints it.
The runbook's handset-features table is generated from it with an agreement
test; a second test walks the rendered dialplan and fails on any extension
absent from the registry. s11, s12 and s13 register their codes here rather
than adding rows by hand.

Done when the table in `RUNBOOK.md` is byte-identical to `doorman features
--markdown` under test, every `exten` in a full render is registered, and
`100` (ring every handset, already documented) is rendered from the
inventory rather than listed by hand.

### M2 · 411, the graph

The provenance primitive lands: the interpreter accepts an operator-provenance
graph and the routing verbs. `render` emits the 411 graph from the registry;
`[internal]` gets the `411` extension; the graph's paths are checked at
render time; leaves act where they can. Clips: numbers via `digits:` (landing
the composite-prompts primitive if s01/s03/s12 have not), help sentences
from the built-in pack, room names degraded to numbers until M3.

Done when, on jepsen, dialling 411 from the kitchen reads the rooms with
their numbers, pages the house from the paging leaf, rings the theater from
the rooms leaf, offers DND and the hunt only if they are configured, and a
fake-ARI test walks every branch of a rendered graph.

### M3 · Names

`doorman pack build --house` renders the house's labels into a house pack on
the workstation; `*99`-style recording per room as the alternative; the
menu says "theater" instead of only "one-oh-two."

### M4 · Paper and keys

`doorman card <handset>` (and `--all`) as printable Markdown/PDF from the
registry, in the s11 kit's style; the s10 template assigns soft keys from
the registry, including a "Help → 411" key. Done when a house guest with
no instruction pages the house within a minute of picking up a handset.

## Alternatives rejected

- **A hand-written IVR in `extensions.conf`.** It would be true for a week.
  Every stream that adds a code would have to remember it, and the table it
  replaces already shows how that goes.
- **A menu in `policy.toml` the operator authors.** Operators author *policy*;
  what the house can do is a fact of the inventory and the binary. Letting
  someone write "press 3 for the garage" when there is no garage is the
  failure this stream exists to prevent.
- **Runtime TTS for room names.** No. Build or record.
- **A web page instead.** The person holding a handset is not holding a
  browser. The card and the soft keys are the non-audio surfaces, and both
  derive from the same registry.

## Rollout order

M1 first and soon — it is the hook the other streams should register into
rather than adding rows. M2 after the provenance primitive, which s14 is now
the reason to build. M3 whenever piper is set up for the house (it already is,
on alpaca). M4 with s10's template work.
