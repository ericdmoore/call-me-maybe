# s13 · Actions — a word, a digit or a passkey moves something in the house

**Status:** M1 shipped 2026-09-24 (v0.7.2); M3, M5 and M6 shipped
2026-09-27 (v0.13.0) — `garage?`, `done_when`, the journal's second writer,
the RUNBOOK "Actions" section. M2 is wired on jepsen and waits on the
user pasting the automation with the real entity id; M4 waits on s14 (the
lobby menu), which does not exist yet. Drafted the hour the house first
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

**State is read from HA, not remembered.** `garage?` asks HA. Decided in
M3 (the user, 2026-09-26, question 3): the box holds an HA long-lived token
in `.env` (`HA_URL`, `HA_TOKEN`) and reads the entity the action names in
`state`. The token is LAN infrastructure like the ARI password, not a
provider key; read by `doorman inbox`, never by the daemon, sent as a
header. `done_when` is the state in which acting would change nothing —
"open" for an opener — and then the webhook is not called and the house
says "It was already open". A `?` is a question only on a word whose
action names a state; elsewhere it is punctuation, so `ping?` is still
`pong` — and on a box with no HA keys nothing changes from v0.12.

**Who may do what lives here, not with the transport.** The people on an
action are the people on the action, whether they text, dial or tap.
Transports may only narrow: a word may list fewer people than its action,
never more.

**Every performance is one journal line.** `action.performed` with the
action, the person id and the transport, and `action.refused` when HA said
no, the person may not, a passkey is wanted, or the thing was already in
the state asked for. M5's decision (question 5, the user: "a second writer
is fine"): the journal learned a second writer. `events.Sibling` appends
committed rows with the same store and budget, holds no owner lock, never
creates or migrates the journal, and does none of the daemon's
bookkeeping; the daemon's flock guards that bookkeeping, not rows. Its
rows ring no doorbell of their own. `message.received` is written for
every text besides, with the outcome as its reason, so the digest and the
journal agree. Nothing on the call path waits on any of it.

## What is deliberately out of scope

- doorman knowing what any actuator is. No `cover`, no `light`, no MQTT.
- Actions that need a conversation ("which door?"). A word is a word.
- Toggles. `garage` opens, `close` closes, and closing is its own action.
- Speakers and announcements — s06.

## Open questions — **settled by the user, 2026-09-26**

1. The ratgdo is in Home Assistant already, twice, as ESPHome devices —
   two doors, two `cover` entities. Which one `garage` means is the entity
   id the user fills into the automation; a second door is a second action.
2. The automations reach Home Assistant by copy and paste for now, mailed
   from the house mailbox with the YAML attached ("it's begging for an MCP
   or SSH or API connection", later). Sent 2026-09-26.
3. State readback by a long-lived token on the box: `HA_URL` and `HA_TOKEN`
   in `.env`, read by `garage?` (M3) and never by the daemon. Directions
   given; the user mints it in HA under their profile's Security tab.
4. `garage` fires on a text alone until s19 M2 (`confirm = "none"`).
5. The journal takes a second writer ("I think a second writer is fine").
6. Day one is the garage, open only, for Eric and Gabi. Named for later:
   lights, the thermostat, HA security settings.

The original questions, for the record:

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

### M1 · The registry — **done 2026-09-24 (v0.7.2)**

`[[actions]]` in policy (id, label, webhook, reply, people, confirm),
schema, `doorman check` (the registry on its summary line; a word for an
undeclared action or an id nobody carries fails; a word-level webhook is
reported as the stopgap it is); `messages.toml` words name actions;
`doorman inbox` performs from the registry, the action's people win over
the word's, `confirm = "passkey"` moves nothing on a text and says so. The
webhook payload carries action, person, via and id. Tests cover all of it.

### M2 · The garage — in progress (2026-09-26)

The ratgdo in HA, two webhook automations, `garage` and `close` in
`policy.toml`, Gabi and Eric on them. Done when a text moves the door and
the reply arrives, and a text from Grandma, who is on the allow-list but
not on the action, gets nothing.

**Where it stands.** On jepsen: `[[actions]] garage` (label "Open the
garage", webhook `http://homeassistant:8123/api/webhook/cmm-garage-open-…`
over the tailnet — `homeassistant.local` does not resolve from the box —
reply "Asked the garage to open", people eric and gabi, confirm none) and
`[[words]] garage`; `check` clean; inbox restarted. The automation YAML
(webhook trigger, `local_only: false` because a tailnet address is not
"local" to HA, `cover.open_cover` on a placeholder entity) was mailed from
the house mailbox to the user with the file attached. Open only, as the
user asked; `close` is one more entry each side when wanted. Done the day
the YAML is pasted with the real entity id and a text moves the door.

### M3 · `garage?` — **done 2026-09-27 (v0.13.0)**

State from HA. Done when the answer is true when the door is open and true
when it is closed, and a text of `garage` to an open door answers "It was
already open" without moving anything.

**Shipped:** `[[actions]] state` (an HA entity id) and `done_when`;
`HA_URL`/`HA_TOKEN` in `.env`, read by `doorman inbox` only; the `?` read
before punctuation is stripped; the answer in HA's own `friendly_name`
("Large Door Door is closed"); an unreachable HA answers "I could not
check" on a question and lets the webhook decide on a request; `doorman
check` prints `garage? asks cover.…` and refuses a state with no keys;
`doorman inbox` refuses to start the same way. Tests cover open, closed,
already open, HA down, no keys, and the token never in a URL. Live on
jepsen the moment the user mints the token and sets the entity id.

### M4 · From the lobby

An extension leaf that performs an action, through s14's registry and the
existing PIN. Done when a caller in the lobby with the family PIN opens the
garage by digit and the journal says `via: lobby`.

### M5 · The journal — **done 2026-09-27 (v0.13.0), the lobby half with M4**

`action.performed` and `action.refused`, written by whichever process the
decision in question 5 chose. Done when `doorman events` shows the door
and who asked for it, from a text and from the lobby.

**Shipped:** the second writer (`internal/events/sibling.go`), the two
event types plus `payload.action` (action, person id, via, word, message
id, state — never a body, never a number), and `doorman inbox` writing
`message.received` for every text and one of `message.acted` /
`action.performed` / `action.refused` beside it. "From the lobby" is M4's
to write, with `via: lobby`, from the daemon.

### M6 · The documents — **done 2026-09-27 (v0.13.0)**

RUNBOOK "Actions": the registry, the HA automations with their YAML, the
credential rule, the confirmation rule. Done when a reader can add a second
action from the runbook alone.

**Shipped:** `docs/RUNBOOK.md` "Actions" — seven steps from the entry to
the journal row, the automation YAML with `local_only: false`, the two
credential rules, the question-mark rule, the reply table, and "a second
action is steps 1 to 3 again". Schema, man page, llms.txt, `.env.example`,
the example policy and words files, and `docs/events.md` carry the same.

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

**What is left, and what it waits on (2026-09-27).** M2 waits on the user:
paste the mailed automation with the real entity id, mint the HA token
into `.env` as `HA_URL`/`HA_TOKEN`, set `state = "cover.large_door_door"`
and `done_when = "open"` on the garage action, restart `doorman-inbox`,
text `garage?` then `garage`. M4 waits on s14, the lobby menu, which is a
stream of its own and not started; the `via: lobby` journal row lands with
it. Nothing else in this stream is open.
