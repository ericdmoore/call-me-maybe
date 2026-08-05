# Sustainability

An honest assessment of whether this project can pay for itself, written down
so future decisions start from a realistic baseline rather than optimism.

## The market, plainly

The addressable audience is the intersection of: people who still want a home
phone, who own or will buy SIP handsets, who have a spare Raspberry Pi, and
who will configure Asterisk. Each filter cuts hard, and the first is in
secular decline.

Asterisk does most of the actual telephony work and is free. What this
project adds is a policy layer and unusually good documentation. Both are
genuinely valuable; neither is a moat.

**Conclusion: this is not a business.** It can plausibly cover its own costs
and be a strong portfolio artifact. Plan accordingly, and do not make product
decisions that only make sense if it were a business.

## What has the right cost structure

**Content packs** (see `docs/PACKS.md`). Generate once, sell many. No
inventory, no support burden, no regulatory exposure — they are audio files.
They also monetise the part people actually love, since the bouncer's
personality is what makes the project shareable. Impulse pricing, not
subscription.

Adjacent, same structure:

- **Seasonal packs** — a spooky bouncer in October, a festive one in
  December. Recurring without a subscription, because people come back
  annually.
- **Language packs** — a Spanish lobby greeting is the one item on the list
  that is utility rather than fun, and probably has the widest real demand.
- **Themed bundles** — voice + hold + ringback + ringtone as a set.

**GitHub Sponsors.** Realistic for a project documented this well. Coffee
money, no conflict of interest.

## What to avoid, and why

**Affiliate/referral links for VoIP providers.** The referral programs exist
and the traffic would be pre-qualified, but the runbook currently gives
unbiased provider advice — including "avoid Twilio Elastic SIP for this
architecture." A referral link puts a thumb on that scale, and the honest
advice is worth more than the revenue. If this is ever revisited, disclose it
prominently.

**A hardware appliance.** The pattern works (Home Assistant Green is the
reference case) and a pre-flashed Pi with a bundled handset would sell. It
also brings inventory, returns, support, and — the real blocker — the moment
you sell a phone product rather than publish code, E911 becomes your legal
obligation rather than a runbook step, with STIR/SHAKEN and FCC exposure
behind it. That barrier is why this space has fewer independent players than
you would expect.

**Hosted or managed service.** Contradicts the premise. Strip out
self-hosting and you are a small VoIP provider competing with Google Voice,
in a regulated category.

**Anything that cripples a mechanism.** See the mechanisms/content split in
`docs/PACKS.md`. A feature that requires a purchase to function damages the
project more than the revenue is worth.

## The one genuinely commercial thread

**Scam-call screening for aging parents** is a real, painful, well-funded
problem — adult children will pay to stop their mother being defrauded by
phone. The lobby/bouncer model is precisely the right primitive for it.

But it is a different product, not a monetisation of this one: the buyer is
not the user, the user cannot configure anything, and it must be hosted with a
phone-based setup flow. Worth noting that the good idea transfers; it should
not distort this repository's design.

## Where this is a complement, not a replacement

Worth writing down before someone is disappointed. A person running several
small ventures can put every number on this box and pay a fraction of a hosted
service — the voice side is genuinely solved and genuinely cheaper.

**SMS is not.** Customers text small businesses, and for many ventures it is
now the primary channel. Asterisk does not do SMS at all; it is out of band,
and while polling a provider's API avoids every architectural problem, there is
no thread UI at the end of it. Messages land in email or a chat bridge.

So the honest sentence is: **for a venture whose customers mostly text, this is
a complement to a hosted service, not a replacement.** Voice comes home, texting
stays where it is.

The second honest sentence is that cost is not the argument. Six numbers here
run maybe a third of what a hosted plan costs, but for someone juggling five
things, paying more for something that Just Works with an app is a rational
trade. The argument is ownership, programmability, real handsets in a real
house, and never paying per seat. Cost is a tiebreaker.

---

## Design lines worth keeping

**The parent stays the admin.** A child's extension having its own PIN,
ladder, and afterhours window is good. The parent authoring `policy.toml`,
the ladder escalating to adult handsets, and unknown callers meeting the
lobby first are what keep it wholesome. A feature that lets an extension
bypass the lobby, or hides who called, quietly makes this a different and
worse product.

**Honest documentation is the asset.** The runbook's value is that it tells
you what actually breaks — DTMF mode, `line=yes`, prompt sample rates. Every
commercial decision that would compromise that costs more than it earns.
