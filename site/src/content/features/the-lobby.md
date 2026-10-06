---
title: The lobby
tagline: The people you list ring the house. Everyone else meets the doorman, dials an extension, or hears "Good day."
audience: Anyone who stopped answering the landline because every call was for somebody else, or for nobody at all.
order: 5
---

## The problem

The landline died of spam. Not of mobiles — of the fact that picking it up
became a gamble, and a house full of people learned to let it ring. A phone
nobody answers is a phone nobody calls, and a phone nobody calls is the one a
child cannot use to reach a friend.

## What the lobby does

A call arrives. If the number is on the house's list, the caller hears
*"Welcome, I'll connect you"* and the house rings — every handset, or the
rooms a [ringer ladder](/features/ringer-ladders/) names first.

Everyone else hears *"Welcome to the phone lobby. Please dial an extension."*
A friend with the six digits for a room dials them and that room rings. A
caller with nothing to dial, or the wrong thing, hears *"Good day."* and a
click.

That is the whole mechanism. There is no spam list to maintain and no
guessing at which area codes are safe. The doorman does not decide who is a
spammer; it decides who is expected, and treats the rest with courtesy and
brevity.

## Why it holds up

**Extensions are passwords.** A six-digit extension exposed to the whole phone
network is a million combinations, which is plenty against a person and not
much against a machine that redials. So after three failed calls in an hour a
number skips the greeting and goes straight to "Good day": no prompt, no dial
window, nothing left to guess at. The extensions themselves are generated at
random, never chosen, and `doorman rotate` replaces them in a second.

**Ten seconds is for the first digit, not all six.** Somebody reading a number
off a card gets ten seconds to start and three between digits, so a confident
friend is through in two seconds and a fumbling one still gets a fair go.

**Nothing a stranger types is written down.** A wrong extension is logged as
wrong, never as what it was: a near miss is most of a credential.

**Numbers are tidied before they are compared.** Carriers hand over the same
caller as `5125550100`, `15125550100` and `+15125550100` on different days.
Everything folds to one form first, so Grandma does not meet the doorman a
third of the time.

## Details worth knowing

**Caller ID is presentation, not proof.** A known caller skips the lobby on
the strength of a number anyone with the right equipment can assert. That is
why the list decides who skips a greeting and nothing more; anything that
*acts* on a caller's word needs a second factor, and gets one.

**The lobby screens incoming calls only.** It is not an approved-contacts
dialler. Outside a [bedtime](/features/bedtime/), the supplied dialplan lets a
handset call ordinary US and Canadian numbers.

**Withheld caller ID always meets the doorman**, and all anonymous callers
share one failure budget. One persistent blocked caller can dismiss the others
faster. That is the intended trade.

**The voice is yours to choose.** The doorman's lines are a folder of audio.
Swap in another [pack](/features/packs/) and a Victorian doorman, a bureaucrat
or a ship's computer meets your strangers instead.
