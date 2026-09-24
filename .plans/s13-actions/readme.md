# s13 · Actions — a word, a digit or a passkey moves something in the house

**Status:** planned (2026-09-24, evening). Drafted the hour the house first
texted back, when `ping` → `pong` proved the text surface and the next word,
`garage`, had nowhere to go. s15 has the text, s19 has the passkey, s14 has
the menu; this stream is the thing they all point at.

## What done looks like

Gabi texts `garage` and, a second or two later, the door is moving and her
phone says "The garage is open". Eric, in the lobby on a call from a number
nobody listed, dials the family extension and then a digit, and the same
door moves. A passkey on a web page does it too, once s19 exists. Every one
of those is the same *action* — one entry in one registry, named `garage`,
that says what it does, who may do it, what it says back, and whether it
must be confirmed — and every one of them leaves the same line in the
journal: `action.performed { action: garage, by: gabi, via: sms }`. doorman
never learns what a garage is. Home Assistant does, and HA keeps the right
to say no: not after midnight, not while the car is out.

## Starting point

- **HA is on the tailnet** as `homeassistant`, `http://homeassistant:8123`
  answers from jepsen (200, 2026-09-24). The ring webhook (`docs/TASKS.md`
  §6, `WEBHOOK_URL`) already posts to it from the daemon, so the
  "HA decides, doorman asks" shape exists and its credential rule is
  written: an HA webhook id is the whole secret, logged as a host only.
- **s15 shipped words with a `webhook` on each** — a stopgap: the word
  carries the URL. It works and it is the wrong place for it, because the
  same door will be reached from the lobby (s14) and from a passkey (s19),
  and three files naming one webhook is three ways to drift.
- **s19 decided** consequential words are intent, not authority: they
  confirm with a passkey. That is a property of the action, not of the
  word — which is the other reason the registry belongs here.
- **The ratgdo** is the first actuator: a garage-door board HA speaks to as
  a `cover`. Whether it is already in the house's HA, and as what entity,
  is an open question below.
- **The journal has one writer**, the daemon. `doorman inbox` is a second
  process; today it journals nothing, and the two `message.*` event types
  are reserved for exactly this stream.

## Decisions and invariants

**One registry: `[[actions]]` in `policy.toml`.** An action is
`id`, `label`, `webhook` (HA's), `reply` (the boring rule from s15),
`people` (ids, or `"*"`), and `confirm` (`"none"` | `"passkey"`). Words in
`messages.toml` say `action = "garage"` and nothing else about it; a lobby
leaf (s14) and a passkey button (s19) name the same id. `doorman check`
refuses a word, leaf or button that names an action nobody declared, and an
action nobody can reach is reported, not refused. The `webhook` and `reply`
keys on a word stay accepted for one release and warn; then they go.

**HA acts; doorman asks; HA may refuse.** The webhook body is the action
id, the person id, the transport (`sms`, `lobby`, `passkey`) and a message
id — never the text, never a number. HA's automation is the policy engine
for the physical world: time of day, the car, a second button. doorman
reads HA's answer only to phrase the reply; it never decides for it.

**State is read from HA, not remembered.** `garage?` asks HA. Two ways,
decided in M3: the webhook's response body carries the state, or the box
holds an HA long-lived token in `.env` and reads the entity. The token is
LAN infrastructure like the ARI password, not a provider key, and is
allowed on the box; the question is whether it is needed at all.

**Who may do what lives here, not with the transport.** The people on an
action are the people on the action, whether they text, dial or tap.
Transports may only narrow: a word may list fewer people than its action,
never more.

**Every performance is one journal line.** `action.performed` with the
action, the person id and the transport, and `action.refused` when HA said
no or the person may not. Which process writes it is M5's decision: the
daemon gains a loopback door for its sibling processes, or the journal
learns a second writer, or `doorman inbox` hands performances to the daemon
over ARI's own channel. Nothing on the call path waits on any of it.

## What is deliberately out of scope

- doorman knowing what any actuator is. No `cover`, no `light`, no MQTT.
- Actions that need a conversation ("which door?"). A word is a word.
- Toggles. `garage` opens, `close` closes, and closing is its own action.
- Speakers and announcements — s06.

## Open questions (to settle before M2)

1. **Is the ratgdo in HA already**, and as which entity — a `cover` with
   open/close, or a `button`? If not, that is a Saturday of hardware first.
2. **Who writes the HA automations?** Two webhook triggers, open and close,
   each calling the cover service with whatever conditions the house wants.
   I can hand over the YAML; somebody has to paste it.
3. **State readback:** webhook response or long-lived token on the box?
   The token is simpler and more honest ("is it open now"); the response is
   fewer secrets. Lean: token, because `garage?` is the one query worth
   having and a lie about a door is worse than a secret on the LAN.
4. **Until s19 exists, may `garage` fire on a text alone?** The registry
   can say `confirm = "passkey"` from day one and refuse until the passkey
   is possible, or `confirm = "none"` for the rehearsal and be tightened
   later. Lean: `"none"` for the rehearsal, tightened the day s19 M2 lands,
   because a door that cannot open yet teaches nothing.
5. **Which process journals** (M5's three options above). Lean: the journal
   learns a second writer; SQLite has always been fine with it and the lock
   file was a choice, not a law.
6. **Day-one actions beyond the garage?** Lean: none. One door, done right.

## Milestones and acceptance criteria

### M1 · The registry

`[[actions]]` in policy, schema, `doorman check` (cross-refs from words;
warns on the word-level webhook); `messages.toml` words reference actions;
`doorman inbox` performs an action by its registry entry. Done when the
`ping` word is an action with no webhook and the tests that covered words
cover actions.

### M2 · The garage

The ratgdo in HA, two webhook automations, `garage` and `close` in
`policy.toml`, Gabi and Eric on them. Done when a text moves the door and
the reply arrives, and a text from Grandma, who is on the allow-list but
not on the action, gets nothing.

### M3 · `garage?`

State from HA. Done when the answer is true when the door is open and true
when it is closed, and a text of `garage` to an open door answers "It was
already open" without moving anything.

### M4 · From the lobby

An extension leaf that performs an action, through s14's registry and the
existing PIN. Done when a caller in the lobby with the family PIN opens the
garage by digit and the journal says `via: lobby`.

### M5 · The journal

`action.performed` and `action.refused`, written by whichever process the
decision in question 5 chose. Done when `doorman events` shows the door
and who asked for it, from a text and from the lobby.

### M6 · The documents

RUNBOOK "Actions": the registry, the HA automations with their YAML, the
credential rule, the confirmation rule. Done when a reader can add a second
action from the runbook alone.

## Alternatives rejected

- **Webhooks on words, leaves and buttons separately.** Shipped as a
  stopgap in s15; three files naming one door drift.
- **doorman speaking MQTT or ESPHome to the ratgdo directly.** Fewer moving
  parts and a worse house: HA already models the door, its history, and
  the conditions, and the family already looks there.
- **Remembering the door's state in doorman.** The call-log rule, applied
  to a door: state you remember is state you get wrong.

## Rollout order

M1 now — it is mostly moving a key. M2 needs the ratgdo answer. M3 and M4
are independent of each other. M5 needs a decision more than code. M6 last.
