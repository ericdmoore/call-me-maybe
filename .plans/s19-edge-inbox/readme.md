# s19 · The edge inbox — the house's public front door, and who may knock

**Status:** M1 shipped 2026-09-24 (`edge/`, deployed as `callmemaybe-edge`
at `edge.callmemaybe.cc`); M2 planned. Drafted the afternoon Tailscale
Funnel turned out not to be available and the question "where does a reply
to the house go" was answered with "the house number is a NAT".

## What done looks like

Every way the outside world reaches the house other than a phone call goes
through one small Worker at the edge, and the house *pulls* from it: the
carrier's SMS callback lands there, and later a person's passkey-signed
command lands there too, in the same queue, and `doorman inbox` on the hub
cannot tell which transport a message came by. The Worker holds the one
credential that must not be on the hub — the carrier API key — and offers
the hub two scoped calls: give me my texts, send this reply. A person who
wants to talk to the house from outside enrols once, at
`login.callmemaybe.cc`, with their phone number and a passkey, and every
Call Me Maybe house that person is listed in can trust that enrolment
without any house depending on the cloud at the moment a door opens.

## Starting point

- Tailscale Funnel is not available on this tailnet, so the hub has no
  public listener and will not get one. doorman's rules already say so:
  no public listeners, ARI on loopback, provider credentials off the hub.
- VoIP.ms delivers texts by calling an HTTPS URL with `{FROM}`, `{TO}`,
  `{MESSAGE}`, `{ID}` in the query string, unsigned, and retries. Its API
  restricts callers by IP allow-list; `0.0.0.0` disables the restriction and
  CIDR ranges are accepted.
- A Cloudflare Worker already serves the site; the account has D1, KV,
  Queues. Wrangler is authenticated from the workstation.
- s15 decided "one number, one purpose": texts to the house number are
  control plane and archive, never conversation. The queue carries commands.

## Decisions and invariants

**One Worker, one queue, per house.** `edge/` is a Worker with a D1 table
of pending messages keyed by house and message id. Three doors, each behind
its own credential: the carrier's callback (`/h/<house>/sms/<token>` — the
token is a path segment because VoIP.ms cannot sign; the URL is the
credential and is never logged), the hub's pull and ack (a bearer token
scoped to that house), and the hub's send (the same token; the Worker calls
`sendSMS` with the carrier key it alone holds). Idempotent by `(house, id)`
on insert, at-least-once on pull; the hub's seen set makes it exactly-once
where it matters. Acked rows are deleted after a week: the carrier's email
forwarding is the archive, not this table.

**The hub pulls; nothing pushes to the hub.** `doorman inbox` long-polls
`/inbox/pull?wait=20`; the Worker answers the moment a row exists or when
the wait is up. One or two seconds end to end, and no listener anywhere the
house owns.

**The reply rule is enforced twice.** Plain ASCII, one segment, no digits,
no links, no exclamation marks — the hub refuses before sending and the
Worker refuses again, because the Worker is the one holding the key.

**A house is a name and three secrets.** `HOUSE_<NAME>_CALLBACK_TOKEN`,
`HOUSE_<NAME>_INBOX_TOKEN`, `HOUSE_<NAME>_DID`, as Worker secrets. That is
already multi-tenant in shape; a second house is three more secrets. It is
not yet multi-tenant in operation — one carrier key serves all houses —
and that is the first thing M2 changes.

**Verifier, not authorizer (M2).** `login.callmemaybe.cc` enrols a person
once: a text to their number with a unique link, a code back, a passkey
registered. It issues a signed attestation — *this passkey controls this
number, verified on this date* — that a house checks offline with a key
compiled into the binary. Whether that number may open your garage stays
your house's decision in your `messages.toml`. No door waits on a cloud
call, and a house with no internet identity works exactly as today. Cross-
house trust falls out: your brother's install trusts the attestation and
applies its own list.

**Commands from a passkey land in the same queue.** A signed command
(`garage`, from an enrolled passkey, for house `midbury`) is a row like any
text: `doorman inbox` reads it, applies the same words and the same people,
and never learns it was not an SMS. SMS becomes one transport among two,
and the remote-management surface s15 floated is this, with no new hub
code.

## What is deliberately out of scope

- Anything conversational through the house number (s15's decision).
- The Worker knowing what a word means. It stores and forwards; the hub
  decides. A Worker that opened doors would be a second policy engine.
- Storing texts at the edge longer than the hub needs them.
- A model anywhere in this path.

## Milestones and acceptance criteria

### M1 · The inbox — **done 2026-09-24**

`edge/`: Worker, D1 schema, README with the whole setup; deployed with the
house's tokens and DID as secrets; `doorman inbox` consuming it. Done when
the callback URL with the token stores a text and answers `ok`, a wrong
token is a 404, a pull with the house token returns it and a pull with a
wrong token is 401, an ack removes it, and `send` refuses a rude reply and
accepts a boring one. Live acceptance is s15 M2's `ping` → `pong`.

### M2 · The login — decided 2026-09-24, not started

**The house introduces the person; the person answers in kind.** `doorman
enroll gabi` on the hub mints a secret phrase — two or three words from a
list, no digits, no link, one segment, past the carrier filter — texts it
to Gabi's number through the edge, and registers at the edge only
`SHA-512(house, number, phrase)`, never the phrase. Gabi proves she holds
the phone by texting the phrase back to the house number: it arrives in the
same inbox as any text, `doorman inbox` sees a listed person answering a
pending introduction, and the intro is complete. No web page is needed for
that half.

**The passkey step is a web page that only a proven phone can reach.**
`login.callmemaybe.cc/enroll`: Gabi enters her number and the phrase, the
Worker hashes and matches against what the hub registered, and only then
offers WebAuthn registration. The passkey's relying party is
`callmemaybe.cc`, so every subdomain — the login page, the edge — can use
it. The phrase is single-use, expires in fifteen minutes, and attempts are
rate-limited at the edge, because a two-word phrase is not a password.

**Per house, no global signing key.** The passkey's public key is stored at
the edge under that house and that number; the hub pulls its enrolled
people the way it pulls texts. Nothing is signed by a central authority,
so there is no key whose leak would mint "verified" people for every house
running the binary, and no rotation story to carry forever. Cross-house
vouching — register once, every house trusts it — is M4 if it ever earns
its keep; until then your brother's house texts your mother its own words.

**Consequential words confirm with a passkey — decided 2026-09-24.** A
stolen phone can text `garage`, and phone possession was the only proof
the text carried. So a word may be marked `confirm = "passkey"` in
`messages.toml`, and then the text is intent, not authority:

```
Gabi  --(garage)-->            house      the intent, by text
house --(Confirm on the login page)--> Gabi   a nudge with no link, no digits
Gabi  --(passkey on login.callmemaybe.cc)-->  edge   proof of presence: the phone
                                                     unlocked by her face or PIN
edge  --(garage, confirmed, gabi)-->  house  one more row in the same inbox
house --(webhook)-->           HA  --> ratgdo
```

No unique link is needed: the passkey identifies her, and the pending
intent waits at the edge under her number for a few minutes. The confirmed
row is indistinguishable to `doorman inbox` from a text except for the
mark it carries, and only a word that asked for confirmation waits for
one; `ping` still answers `pong` on the text alone. Ten to twenty seconds
from text to door, deliberate by design. Revocation is `doorman people
revoke gabi`: the edge forgets her passkeys, and no word she texts opens
anything until she enrols again. The edge is trusted to attest the
assertion — it is the house's own infrastructure and already holds the
carrier key — and that trust is written down here rather than hidden.

**A signed command lands in the same queue.** A passkey assertion for
(house, word), with or without a preceding text, is one more door on the
edge, and the row it stores is indistinguishable to `doorman inbox` from a
text.

Done when a person enrolled once texts nothing and still opens the garage
from a web page with a passkey, and the hub's journal shows the same shape
as an SMS. Ordered after s15 live and s13, because enrolment is a text and
a passkey that can say `garage` needs a door.

### M3 · Tenancy

One carrier key per house, houses created by an operator command, and the
site's install docs pointing a new house at the shared edge. Done when a
second house exists with none of the first house's secrets.

## Alternatives rejected

- **Tailscale Funnel to the hub.** Not available here, and would have put a
  public listener on the box that answers the phone.
- **SQS.** Works only with a public queue policy that lets anyone enqueue,
  and adds an AWS account for one queue when the Cloudflare account already
  has everything.
- **Cloudflare Queues instead of D1.** Fine too; D1 made the long-poll and
  the idempotent insert one statement each, and there is nothing to consume
  at the edge.
- **The hub sending replies directly.** Puts the carrier key on the hub,
  which the provider invariant forbids for a reason unrelated to texting.

## Rollout order

M1 today, because s15 M2 is blocked on it. M2 when someone wants the
garage from a browser. M3 when a second house appears.
