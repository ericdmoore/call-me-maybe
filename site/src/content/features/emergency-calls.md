---
title: Emergency calls
tagline: 911 leaves by the trunk whose street address is filed, tried first and never second-guessed.
code: "911"
audience: Every household, before the first handset is handed to a child.
order: 12
---

## The short version

Dial 911 from any handset and the call leaves by the provider that holds the
house's registered emergency address. If that provider cannot carry it, the
call tries every other provider in turn. If nothing can carry it, you hear a
busy tone rather than silence, and that tone means reach for a mobile.

The rest of this page is the reasoning, because on this one subject the
reasoning is the feature.

## What the house does

**The designated trunk goes first, unconditionally.** With one provider there
is nothing to decide. With more than one, `trunks.toml` names which carries
911, or the primary line's provider does by default, and `doorman check`
prints which it is and whether that was chosen or inferred, every time it
runs and every time the daemon starts. Unset is never undefined, and a
configuration with no answer for 911 refuses to render at all.

**Nothing asks the trunk whether it is up. It is tried.** A provider that does
not answer a health check can be working perfectly, and diverting an emergency
call off the one trunk whose address is on file, on a false alarm, at the
worst possible moment, is exactly the failure this design exists to prevent.
A trunk that is really down fails in milliseconds, and the call moves on.

**Then every other trunk, best evidence first.** Providers that declare a
registered address are tried before ones that say nothing, and a provider that
declares it has no emergency service goes last. The dispatcher may then see the
wrong address, which is genuinely bad; a connected call lets a person say
their address out loud, and a failed call gives them nothing. Connection
first.

**The call never borrows another line's number.** Emergency addresses are
registered against a specific number, so 911 always leaves as the trunk's own
number and never as a second line whose address is somebody else's house. The
outbound console that lets a handset call out as another of your numbers
cannot reach 911 at all, by where it lives in the dialplan and by refusing the
number itself. Two independent things have to be wrong.

**Bedtime does not apply.** A handset under [curfew](/features/bedtime/) can
still dial 911.

## What you have to do

1. **Register the address with your provider.** It is a couple of dollars a
   month and the one line item on a house phone worth never skipping. The
   [providers page](/providers/) lists the charge for each.
2. **Say what you know.** In `trunks.toml`, `e911 = true` or `false` per
   provider. `doorman check` reports "unknown" rather than guessing when you
   have not.
3. **Verify it the provider's way.** Most have a test procedure that does not
   involve calling 911. Use it after any change to numbers or providers.
4. **Reject a provider without it.** Not every provider offers emergency
   service in every area. One that does not means a phone that cannot call
   for help, which is a reason to choose a different provider, not a line to
   skip.

## Be plain about this

This is a supplementary phone. It stops working in a power cut and on a bad
internet day. It should never be a household's only route to emergency
services. Keep a mobile in the house and teach everyone, children included,
that it is the one to reach for first. That is not a disclaimer; it is the
accurate description of a hobbyist phone on a consumer internet connection.
