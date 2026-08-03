# Story packs

A story pack is an interactive audio story the phone plays: the caller listens,
presses a digit to choose, and the story branches. It is written as **one
Markdown file** and built into audio by `doorman pack build`.

This document is the format specification. **The specification is CC0** —
implement it, generate packs, publish and sell them, no permission needed.

Read `PACKS.md` first: it is the parent format, and everything about
`pack.json`, audio formats, installation and the archetypes rule applies here
unchanged. This document describes only what `kind: story` adds.

---

## What it is for

A phone extension that tells a child a story they steer. Something in the
spirit of a Toniebox, minus the figurine and the plush: the hardware is a
handset that already exists in the house, the story is a file, and picking up
the phone is the interaction.

That framing drives several decisions below — particularly the limits, which
are generous because a child will happily listen for an hour, and the sandbox,
which exists so that a small person mashing the keypad ends up somewhere in the
story rather than somewhere in the phone system.

It is not only for children. The same mechanism carries a text adventure, an
audio tour, a triage tree, or a murder mystery for a dinner party. But the
child case is the one that sets the constraints, because it is the one where
being wrong matters most.

---

## Why Markdown

Three reasons, in order of importance.

**The sandbox is a property of the format, not a rule we enforce.** Branching is
a document-relative anchor — `[Go left](#cave)`. An anchor cannot dial a phone
number, cannot reach another pack, cannot name a handset. There is no syntax
for escaping the document, so there is nothing to validate against and nothing
to get wrong. A hostile story pack can, at worst, tell you a bad story.

**A story is prose and should be written as prose.** Authoring a branching
narrative in JSON is miserable and the misery shows in the writing. A Markdown
file renders in any viewer, so the author, an editor, and a parent can all read
the story and follow its links before a single character is sent to a TTS
vendor.

**It reviews like a document.** A pack arrives as a pull request a human can
read.

### Prior art

This is a well-trodden path and the format borrows deliberately:

- **Twine/Harlowe** and **Ink** (inkle) are the two dominant interactive-fiction
  tools, and both converged on links between named passages. That is the
  structure here.
- **VoiceXML** contributes the event model. Its most durable idea is that
  *no input* and *unrecognised input* are different events deserving different
  responses — a distinction doorman's lobby already makes between `no-digits`
  and `invalid-extension`.
- **TwiML** contributes vocabulary and the instinct that a small set of verbs
  beats a general language.

What is deliberately **not** borrowed is Asterisk dialplan. This project does
not write dialplan in Go, and a story is not a call flow.

---

## The file

One Markdown file, `story.md`, beside `pack.json`.

````markdown
---
story:
  start: entrance
  title: The Threshold

voices:
  NARRATOR: { backend: elevenlabs, id: 21m00Tcm4TlvDq8ikWAM }
  DOORMAN:  { backend: polly, id: Amy, settings: { engine: neural } }
  GHOST:    { backend: openai, id: onyx, settings: { instructions: "hollow, unhurried" } }

defaults:
  timeout: 8s
  retries: 2
  on-timeout: repeat
  on-invalid: reprompt
---

## entrance

NARRATOR: The brass door gives way to a lobby that smells of rain and
floor polish.

DOORMAN: Good evening. I don't believe you're expected.

1. [Say you are expected anyway](#bluff)
2. [Ask who *is* expected](#question)
3. [Leave](#politely-out)

## bluff

<choices timeout="12s" on-timeout="#the-doorman-waits"/>

DOORMAN: Expected. I see. And by whom, precisely?

NARRATOR: His pencil has not moved.

1. [Invent a name](#invent)
2. [Admit it](#admit)
0. [Hear that again](#bluff)

## politely-out

DOORMAN: Very good. Mind the step.

<end/>
````

### Nodes

A level-2 heading (`##`) starts a node. Its slug is the anchor: `## bluff` is
reached by `[…](#bluff)`. Slugs are lowercased, spaces become hyphens — the
same rule every Markdown renderer already uses for heading anchors, so the
links work in a browser too.

`story.start` in the frontmatter names the first node.

### Speech

A line beginning `NAME:` is spoken by that voice. Names are matched against
`voices` in the frontmatter and are conventionally uppercase.

A paragraph with no speaker label is spoken by the **first** voice declared,
which by convention is the narrator.

Emphasis is left alone for what emphasis is for. `*expected*` is a stressed
word, not a change of speaker — which is precisely why speaker labels carry
identity instead. Whether emphasis reaches the audio depends on the backend
(OpenAI takes direction in words, Polly takes SSML); the format preserves it
either way.

### Choices

An **ordered list whose items are links** is the choice point. **The list
number is the DTMF digit.** No separate gather syntax: the list renders as a
numbered menu in any Markdown viewer *and* is the menu the caller hears.

Digits 0–9 are yours. Numbering need not be contiguous or in order — `0.` for
"hear that again" as a self-link is the idiom, and it costs nothing.

`*` is reserved: it repeats the current node from anywhere, always, without the
author writing anything. Children need it and it requires no state. `#` is
reserved and currently unused.

A node with no choice list and no `<goto/>` is an ending.

### Verbs

XML earns its place only for what links cannot say. HTML in Markdown is legal
and passes through renderers untouched.

| Verb | Meaning |
|---|---|
| `<choices .../>` | override the frontmatter defaults for this node's choice point |
| `<goto anchor="#x"/>` | play this node, then continue with no prompt |
| `<pause seconds="2"/>` | a beat |
| `<sfx name="door-creak"/>` | play a clip from this pack's own `sfx/` directory |
| `<end/>` | finish the story and hang up politely |

`<choices>` attributes: `timeout`, `retries`, `on-timeout`, `on-invalid`. The
last two take `repeat`, `reprompt`, `#anchor`, or `end`.

There is no verb that dials, transfers, records to a mailbox, or reads
configuration. That is the point.

---

## What happens when nobody presses anything

Inherited from VoiceXML, because it is the part everyone gets wrong by
collapsing two different situations into one:

- **No input** — the caller said nothing. Usually they are thinking, or a child
  has wandered off. Default `repeat`: play the node again.
- **Unrecognised input** — the caller pressed 7 and there is no 7. They are
  engaged but wrong. Default `reprompt`: say the choices again without
  replaying the whole scene, which is what an impatient listener wants.

After `retries` of either, the story goes to `on-timeout` / `on-invalid`. Give
a children's story a gentle terminal node rather than a hangup:

```markdown
<choices retries="3" on-timeout="#tucked-in" on-invalid="reprompt"/>
```

---

## Limits

Almost everything is checked at build time, so a broken story fails on a
workstation rather than in a child's ear.

**`doorman pack check` (static):**

| Limit | Value |
|---|---|
| Nodes | 1,000 |
| Utterances per node | 20 |
| Choices per node | 10 (one per digit) |
| Dangling anchors | none permitted |
| Unreachable nodes | warned, not fatal — drafts have offcuts |
| Missing audio | none permitted |

A thousand nodes is roomy: a large Twine story is around two hundred passages.

**At runtime, only two — and only because cycles are legitimate.** "You wander
back to the entrance" is the genre, so a loop cannot be statically rejected. It
gets a budget instead:

| Limit | Value |
|---|---|
| Node transitions per call | 5,000 |
| Wall clock per call | 60 minutes |

A person pressing digits cannot approach five thousand transitions; a runaway
cycle reaches it immediately. Sixty minutes is deliberately past the point a
child will sit still, because being cut off mid-story is a worse failure than a
long call, and the alternative failure — a cycle holding the line open all
night — is what the transition budget already catches.

The interpreter gets teardown for free: the caller hanging up cancels the
session context and every wait selects on it (CLAUDE.md invariant 3). These
limits exist only for the call where nobody hangs up.

---

## No state, deliberately

There are no variables, no inventory, no "if you have the key". Two reasons:

**This project has no database** and a story is not the place to acquire one.
Rate-limit state is in memory and meant to be lost; story state would be the
first thing that needed to persist across a call, and then across calls.

**A stateless graph is completely checkable.** Every path can be walked at build
time. Add variables and "does this story have an unreachable ending" becomes
undecidable in the general case, which means `doorman pack check` stops being
able to promise a story cannot dead-end.

Most of what state buys can be written into the graph — a `#cellar-with-lamp`
node separate from `#cellar-dark` — which is what Twine authors do anyway. If
that becomes genuinely limiting it is a considered change to this spec, not a
field somebody adds.

---

## Building

The Markdown is **source**. `doorman pack build` walks the graph, renders every
utterance through the backends named in `voices`, normalises loudness, and
writes the 8 kHz/16 kHz pair per clip along with a compiled graph.

Nothing synthesises during a call, ever (CLAUDE.md invariant 7). A story pack
installs as audio files exactly like a voice pack.

```
the-threshold/
├── pack.json
├── story.md            the source, shipped so the pack is rebuildable
├── story.json          the compiled graph
├── audio/
│   ├── entrance-01.wav
│   ├── entrance-01.wav16
│   └── …
└── sfx/
    └── door-creak.wav
```

Rendering is content-addressed on `(text, voice, settings)`, so editing one
line re-renders one clip rather than the whole story. On a paid backend that is
the difference between a tool you use and one you are afraid of.

---

## How this relates to voice packs

Story packs are a different `kind`, and the reason is namespace ownership:

| | `kind: voice` | `kind: story` |
|---|---|---|
| Who defines the clip names | the binary (`prompts.go`) | the pack |
| How many | exactly six, forever | unbounded |
| Shared between packs | identical in all | no overlap |
| "Complete" means | all six present | every anchor resolves |

The six-name contract works *because* it is closed and code-side; that is what
makes swapping `PROMPT_MEDIA_PREFIX` a drop-in change. A story's inventory is
open and content-side by nature.

The decisive point is that **they are active at the same time**. A caller hears
your doorman pack's `lobby-greeting`, dials an extension, and lands in the
story — both packs live in the same call. They are not alternatives, so they
cannot share a namespace. Merged, `PROMPT_MEDIA_PREFIX=ghost-story` would give
you a phone with no `good-day` prompt: a pack that half-works, which is the
exact failure the prompt-name contract exists to prevent.

They share everything else: the `pack.json` envelope, the audio formats, the
install path, the render pipeline, and the archetypes-not-characters rule.

### Reaching a story

A story is an extension in `policy.toml` like any other. It needs no allow-list
entry and rings no handset — the caller is connected to the story instead.

Extensions are credentials (CLAUDE.md invariant 5), and a story extension is a
credential a child has to remember and recite to a friend. `doorman rotate` is
the wrong tool here: rotating it breaks every kid who knows it. Let them choose
one, subject to the rules in `writing-policies.md`.

---

## Rule: archetypes, not characters

`PACKS.md`'s rule applies with more force here, because a story is longer and
the temptation is greater. No named film or game character, no impression of a
specific performer, no cloned voice of a real person. Write the archetype — the
doorman, the ferryman, the lighthouse keeper — not the copyright.

A children's story carries a second obligation the licence cannot express:
whoever ships it is responsible for what a child hears at the end of every
branch. Walk all of them. `doorman pack check` will tell you which ones you
have.

---

## Licensing

As `PACKS.md`: community packs CC BY-SA 4.0, paid packs a short proprietary
end-user licence. State it in `pack.json`. The format specification here is
CC0.

Ship `story.md` inside the pack even for a paid one. It is what makes a pack
translatable, re-renderable in a voice the buyer prefers, and readable by a
parent deciding whether to play it. Withholding it protects nothing — the audio
is the thing that took work — and it turns a pack into something nobody can
inspect before giving it to a child.
