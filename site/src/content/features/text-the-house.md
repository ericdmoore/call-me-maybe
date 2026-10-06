---
title: Text the house
tagline: Text "garage" to the house number and Home Assistant opens it, for the people you list, and says so back.
audience: People who already run Home Assistant and would rather text the house than open an app.
order: 95
---

## The problem

Home automation ends up behind an app, an account and a notification. The
thing you actually wanted was to tell the house to do something from wherever
you are, in one word, and hear back that it did.

## What it does

The house number can receive texts. A known word from a listed person does
one thing and gets one boring reply; everything else is archived by the
carrier's mail forwarding and never answered. A stranger texting the house
gets nothing, not even a refusal.

The words live in `messages.toml`. Each names an **action** in `policy.toml`,
the one registry of things the house can do: what to call in Home Assistant,
what to say back, who may ask, and whether it has to be confirmed. The garage
is the worked example:

| you text   | the house says               | what moved                                   |
|------------|------------------------------|----------------------------------------------|
| `garage?`  | `Large Door Door is closed`  | nothing; Home Assistant was asked             |
| `garage`   | `Asked the garage to open`   | the automation fired; Home Assistant decided  |
| `garage`   | `It was already open`        | nothing; the house checked first              |

Grandma, listed for calls but not on the action, gets no reply at all. A reply
would tell her what the house can do.

## Why it is built this way

**Home Assistant does the deed and keeps the right to say no.** The house
only asks. The automation on the other end is where the rules about the
physical world belong: not after midnight, not while the car is out. The
request it receives names the word, the action and the person, never a phone
number, so an automation can say "only Gabi after ten" without ever seeing
one.

**Closing is its own action.** There are no toggles, because a toggle is a
door that does the opposite of what you asked when the state you remembered
was wrong.

**The carrier key never touches the box.** Texts arrive at a small edge
service that holds the carrier credentials; the house pulls from it over an
authenticated connection and never opens a port. Nothing on the call path
reads a text, and a handset cannot text an outside number through the house
at all.

**Everything is journaled.** Every text and every action is a row in the
house's event journal: who, which word, which action, what happened. Enough
to answer "did the garage open on Tuesday", never enough to reconstruct a
conversation.

## Details worth knowing

**Contact cards work too.** Text the house a contact card and it lands in a
handset's phone book; see [the people your house knows](/features/contacts/).

**Confirmation by passkey is planned.** An action can already be marked as
needing confirmation, and the house answers that it does; the passkey itself
is on the roadmap, and until it ships such an action simply does not fire.
