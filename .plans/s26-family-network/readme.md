# s26 · The family network — two houses, one dial plan

**Status:** planned 2026-10-03, not started. Written the morning the product
direction was set (a package a non-technical person could run) and the
store was sketched: backup, house-to-house calling, notification packs,
audio packs, support. This is the house-to-house half: what two doorman
boxes on one account can do that a phone company cannot, and the two
decisions that have to be made before any of it exists.

## What done looks like

Grandma's box and ours are on one dialdoorman account. From any phone in
our house, `203` rings her kitchen; from hers, `105` rings Norah. Her
directory lists our rooms by name and says which are asleep. Her line
admits everyone on our allow-list and sends strangers to her lobby. On
Sunday at ten both kitchens ring and are bridged, nobody having dialled.
A key on her handset pages every phone in our house. If her phone has
placed and answered nothing for three days, we get a text. None of it
needs her to know what a tailnet is, and none of it costs a minute.

## The two decisions

**The overlay is the account's, not the customer's.** Nobody asks grandma
to join a tailnet. A box enrols with its account at setup and receives a
device key for a WireGuard network the service operates (Tailscale with
per-account tags and ACLs, or self-hosted Headscale — the same thing
either way). The ACL says which boxes may ring each other and nothing
else. Voice (~80 kbps) goes peer-to-peer where NATs allow and through a
relay otherwise; the relay is what the paid tier pays for. This keeps the
rule that the box has no public listener: the overlay is outbound-only
from the box's point of view.

**One dial plan, a hundred per house.** Numbers are unique across a
family network: a house owns a block of a hundred (`1XX`, `2XX`, …) and
its rooms are dialled by the same number from every phone in every house,
including its own. Room-to-room across houses is just dialling. Pages,
ring-all and conference stay inside the physical house by default;
reaching another house's is a deliberate code, never the default.

*To settle before the second house exists:* `100`, `500` and `600` are
feature codes in the hundreds that houses would own. The clean fix is
per-house codes — `X00` rings every phone in house X, with `500`/`600`
kept as aliases for house 1 until a major version moves them. Decide once.

## What two boxes can do that POTS cannot

Each item notes what already exists and what is new.

### Rooms, not houses
- **Dial her kitchen, not her house.** The numbering above, over the
  overlay. *New: the overlay enrolment and cross-house routing in the
  dialplan; existing: everything a room already is.*
- **Rooms with state.** Her directory shows our rooms and whether each is
  asleep (s23) or quiet (s12), so nobody rings a child at 21:30.
  *Existing: curfew and DND; new: the directory carrying another house's
  state.*
- **Transfer across the family.** She calls, the kitchen answers, "hang
  on, I'll get Norah" — transfer to `105`. *Existing: handset transfers;
  new: nothing but the number reaching further.*
- **One allow-list, two houses.** Our `[[people]]` admit callers at hers;
  strangers meet her lobby. *Existing: the admission ladder; new: a
  shared source of people across boxes — likely a `contacts.toml` source
  the account serves, which the ladder already knows how to read.*

### Time
- **Standing calls.** "Sunday 10:00: ring both kitchens and bridge
  them." *New: a scheduled originate, two legs, one bridge; the
  `[[schedules]]` vocabulary already says when.*
- **Bedtime stories by the real grandma.** She records a story through
  `*98`; the kids dial `*6` and hear her. *Existing: story packs, the
  hunt's record-a-clue flow; new: a pack whose clips come from a mailbox
  rather than a file.* The curfew can be the cue: at 20:00 the kids'
  phones ring her.
- **Reminder calls in a familiar voice.** The box rings her at nine and
  plays "take the blue pill, Mom" in her son's voice, pre-rendered. *Waits
  on s16 (the family voice pack); new: an originate that plays a clip.*

### Care, with consent
- **Drop-in.** A page from our kitchen auto-answers on hers, speaker on,
  after a chime. Per-house consent; `*78` turns it off on her side.
  *Existing: page-autoanswer (500); new: a page that crosses the overlay
  and a consent flag on the receiving house.*
- **A help key.** One soft key on her handset pages every phone in our
  house and says which room pressed it. *Existing: s10 line keys, 500;
  new: a cross-house page target and a "from grandma's kitchen" prompt.*
- **The absence signal.** The journal knows her phone has placed and
  answered nothing for three days, or that a voicemail has sat unheard.
  A text to us — counts, never numbers. *Existing: the journal, the
  digest's delivery; new: a report over it and a threshold. The first
  notification pack worth shipping.*
- **We keep her phone book.** `*88` in reverse: "Dr. Patel" added from
  our house appears on her handset within the hour. *Existing: own books
  (s24), the directory's hourly poll; new: a book pushed through the
  account rather than filed by her phone.*

### Money
- **Family calls cost nothing.** The overlay carries them; her VoIP.ms
  line is for everyone else. *Existing: nothing to build; it is a
  property of the overlay.*

## What to lead with

The help key and the absence signal. They are what adult children buy,
they are nearly free given what exists, and they need only the overlay
and the numbering — the two decisions above — to work.

## Rejected

- **Each customer runs their own tailnet.** The thing a non-technical
  buyer cannot do, and it would put a vendor's account between two
  family members' phones.
- **Plain SIP between houses over the internet.** Needs an inbound port
  or a registrar in the middle; the first breaks the no-public-listener
  rule and the second is the overlay with worse security.
- **Cross-house pages by default.** A page is loud and immediate; across
  a house boundary it must be asked for and consented to.
