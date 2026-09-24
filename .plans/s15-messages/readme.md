# s15 · Messages — the house answers texts

**Status:** planned (2026-09-22). Drafted the afternoon SMS was switched on for
the house number: inbound worked at once, the first outbound vanished into a
carrier's spam filter for containing a phone number, the second, plainer one
arrived, and it became clear that "the house texts back" is a real surface —
a different shape from the retro handsets, and worth keeping deliberately
separate from them.

## What done looks like

A text to the house number is an email in the house's mail, with any photo
attached — the archive, always. Certain words from certain people also *do*
things: Gabi texts `garage` and the door opens about a second later, and the
house replies "The garage is open." Anyone provisioned texts `menu` and gets
the same tree `411` speaks, typed; a leaf name descends or acts. A stranger's
text is archived and never answered. Nothing about any of this runs on jepsen,
touches doorman, or holds a credential on the box: the reader lives where the
API credentials are allowed to live, receives pushes, and asks Home Assistant
to act. The handsets, separately, can text each other over the LAN with no
carrier involved at all.

## Starting point

- **SMS is on** for 972-290-1973 (set through the API on 2026-09-22).
  Inbound lands in VoIP.ms's message centre; nothing forwards it yet.
  Outbound via `sendSMS` works. Two facts learned the honest way: **`status:
  success` means accepted, not delivered** — there are no receipts — and
  **carriers silently drop "spammy" content** from VoIP numbers: a text with a
  phone number and "for the first time" in it never arrived; "Yes. I can see
  both of those." did.
- **Push exists.** Per DID, VoIP.ms will call an HTTPS URL with `{FROM}`,
  `{TO}`, `{MESSAGE}`, `{ID}` (with a retry flag), or forward to an email
  address, or deliver over an SMPP bind. Nothing here requires polling.
- **Billing is by segment**: 160 GSM-7 characters, 153 when concatenated; one
  emoji or curly quote flips the message to UCS-2 and 70. Cheap either way;
  shape-relevant for a bot.
- **10DLC** applies to US local numbers; it was *not* what blocked the first
  message, but it hangs over any outbound volume. Toll-free numbers use a
  simpler verification. Neither is needed for a house replying to its family.
- **The rules that shape where code lives:** provider API credentials stay
  off the hub (`internal/provider` is reachable only from `doorman balance`,
  by test); doorman has no public listeners; ARI is loopback. So no part of
  this runs as doorman or on jepsen.
- **Already-owned pieces:** the allow-list of people (`policy.toml`), the
  feature registry and its tree (s14), actions and their journal event
  (s13), HA as the thing that actuates (s13), bullmoose as public mail that
  Hermes already watches, Tailscale on every host, and a Cloudflare Worker
  serving the site.
- **The WP826 has a messages app** speaking SIP MESSAGE, and Asterisk routes
  those between endpoints with one context. Unrelated to carriers; related
  to the kids.

## Decisions and invariants

**Push, never poll.** The DID gets two deliveries: **email forwarding** to
a house mailbox in bullmoose — the archive, every text, every MMS photo,
permanent — and a **URL callback** to the reader — the trigger. Email is the
system of record and needs no code; the callback is what makes a door move
in a second. Polling `getSMS` is rejected below.

**The reader lives on the workstation, reachable through Tailscale Funnel.**
A small program (`cmd/textline`, built from this module, never installed on
the hub) listens on one HTTPS path that `tailscale funnel` exposes at the
host's `*.ts.net` name. VoIP.ms cannot sign callbacks, so **the URL is the
credential**: a random path segment, treated exactly as `internal/provider`
treats API URLs — never logged, never in an error. The reader holds the API
credentials for replies, the same place `doorman balance` holds them. jepsen
is not involved; doorman is not involved.

**Words, per person, per capability.** A sender list maps a number to the
words it may use. It is *not* the ring allow-list: who may open the garage is
a stricter set than who may ring the house, and the two lists are allowed to
differ. `garage` opens; `close` closes and may be adults-only; `garage?`
answers with the state and does nothing; `menu` and the tree's words are for
everyone on the list. **A text from a number not on the list is archived by
the email path and never answered** — a reply tells a stranger the number is
live, and silence is the right answer.

**Open, not toggle.** A toggle is a function of state the sender cannot see;
`open` is idempotent and texting it twice is harmless. HA knows the state
through the ratgdo, so the reply is honest: "The garage is open" or "It was
already open." Closing is its own word because closing a door nobody is
watching is the one thing to make deliberate.

**HA acts; the reader asks.** The reader POSTs to HA's webhook over the
tailnet; HA opens the door and may add its own conditions (not after
midnight; not if the car is out). The reader never knows what a garage is,
in the same sentence as doorman never knowing what a speaker is. When s13
lands, the reader also reports `action.performed { by, via: sms }` so the
house's journal has the line.

**Replies are boring on purpose.** Plain ASCII, under 160 characters, no
numbers, no links, no exclamation marks. This is one rule with two reasons:
one segment on the bill, and past the carrier's spam filter. The `411` tree's
lines already fit; the reader refuses to send a reply that does not.

**Never assume a reply arrived.** No receipts exist. The door's state is the
truth; the text is a courtesy. Nothing in the reader waits for or depends on
its own message being read.

**Idempotent by message id.** VoIP.ms retries callbacks; a callback can also
arrive twice. The reader remembers ids it has acted on and never opens a
door twice for one text. Its own state is that id set and nothing else.

**`menu` is s14's registry, typed.** The reader holds the house's registry as
the same JSON `doorman features --json` prints, fetched from the hub over
ssh on a schedule (the hub publishes; the edge pulls). Texting `menu` sends
the top level; a digit or a leaf name descends or acts; unknown words get
"I know these words:" and the list. Deterministic before clever: no model in
the loop, ever, for a word that moves a door.

**One number, one purpose — decided 2026-09-24.** A text to the house
number is control plane and archive, never readable content on a handset;
and nothing typed on a handset ever leaves through the house number. The
question that settled it: "the house texts Eric's cell, Eric replies —
where is that readable?" Every answer was wrong on a number that also opens
the garage: the person on the other end sees a conversation and a machine's
replies from one sender, and the box has to guess whether "garage?" is a
question or an instruction. The mechanism could carry both; the policy is
that it does not. The owner's framing, which is the shortest statement of
why: the house number is a NAT. Many handsets inside, one address outside,
and a reply from outside has no inside address to return to. Voice resolves
that by ringing everyone; a text has no such natural resolution, so the
posture is not to attempt one. If the family ever wants conversational texting from
handsets, that is a second DID and a `[line]` of its own — its texts to its
handsets, every handset for a family line, one for a kid's own number, one
shared thread per outside number — and a later milestone here, not a
change to this one. Until then a handset that texts an outside number gets
an error from the phone, not silence.

**Handsets text each other without any of this.** SIP MESSAGE between
PJSIP endpoints is one dialplan context; the WP826s show it in their
messages app; nothing leaves the LAN and nothing is billed. Same stream,
because "the kids can text each other" is the feature people will assume the
house number is for, and it is not.

## What is deliberately out of scope

- **Polling `getSMS`.** Push exists; a poller adds latency and a credential
  that runs all day for nothing.
- **SMPP.** True push, heavier than the house will ever need.
- **The reader on jepsen, or in doorman.** Credentials and listeners in the
  wrong place; the rules exist for this.
- **An agent between a text and a door.** Hermes already reads the house's
  mail and could act on it; it must not be the thing that opens a garage.
  Email is the archive; the callback is the trigger.
- **MMS beyond the archive.** Photos arrive by email. Nothing on the
  handsets or in the reader does anything with them.
- **10DLC / toll-free registration.** Not needed for a house replying to its
  own family. Revisit only if outbound volume or deliverability ever demands
  it; the decision is recorded here so it is not re-discovered.
- **Toggle semantics.** See above.

## Milestones and acceptance criteria

### M1 · The archive (today)

`setSMS` on the DID with `email_enabled` and the house mailbox. Done when a
text and an MMS photo to the house number arrive as mail with the photo
attached, and the sender's number is the subject.

### M2 · The reader and the door

`cmd/textline`: the callback handler, the URL-path credential, the sender
list (`textline.toml`: number → words), word dispatch, the HA webhook call,
the reply with the ASCII/160 guard, the seen-id set, and `sendSMS` through a
client that shares `internal/provider`'s never-log-the-URL discipline.
`tailscale funnel` exposing the one path; the callback set on the DID.
Tests with `httptest`: a valid callback from a listed sender opens (a fake
HA records the call) and replies; the same id twice acts once; an unlisted
sender is a 200 with no action and no reply; a bad path token is a 404 with
nothing logged; a reply that would exceed one segment or leave ASCII is
refused before it is sent.

Done when Gabi texts `garage` and the door opens within two seconds, the
reply arrives, `garage?` answers truthfully, and a text from a number not
on the list produces exactly one email and nothing else.

### M3 · `menu`

The reader fetches `doorman features --json` from the hub on a schedule and
answers `menu`, digits and leaf names from it. Done when texting `menu`
returns the same top level `411` speaks, `2 1` pages the house, and an
unknown word gets the word list. Depends on s14 M1.

### M4 · Handsets texting handsets — **done 2026-09-24 (v0.6.11, v0.6.12)**

The SIP MESSAGE context is generated by `doorman render` (`[cmm-messages]`:
a room number reaches that phone, `100` every phone; `[cmm-message-from]`
rewrites the sender to its room number and label so the receiving phone
names the room and a reply routes); every handset endpoint carries
`message_context`. Verified on jepsen: theater ↔ kitchen both ways, nothing
on the trunk. A text to an outside number fails on the phone (404), per
"one number, one purpose".

### M5 · Journal

When s13 lands: `action.performed` with `via: sms` for every accepted word,
so `doorman events` shows the door and who asked for it.

## Alternatives rejected

- **A poller on the workstation.** Was the first draft; ten seconds of
  latency and an always-running credential to save one `tailscale funnel`
  command. Push was there all along.
- **Reader on the Cloudflare Worker.** Works, and is the right move only if
  the edge ever hosts more of the house than one callback; today it adds a
  second place credentials live for no gain over Funnel.
- **HA as the callback target directly** (Nabu Casa or a tunnel). Viable;
  puts the sender list and the word logic in YAML, where tests do not live.
  The reader is small enough to test properly.
- **Using the ring allow-list as the sender list.** Ringing the house and
  opening its garage are different trusts.
- **Replying to strangers, even politely.** Confirms a live number to
  whoever is scanning.

## Rollout order

M1 is an API call once an address is chosen. M2 is a day, and it is the
whole point. M3 waits on s14's registry. M4 is independent and small. M5
waits on s13. The order costs nothing, because the door does not need the
menu and the menu does not need the door.
