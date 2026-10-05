---
title: The people your house knows
tagline: Reuse an address book, while keeping caller screening and phone directories separate.
audience: Households tired of typing the same family numbers into several places.
order: 25
---

Grandma changes her number. A new friend starts calling. Your phone system's
list should not become a second address book nobody remembers to maintain.

Call Me Maybe can read vCard exports from local files or configured URLs.
URL sources refresh into a local cache; if a refresh fails, the last good
copy remains available. This is an export-based workflow, not a built-in
Google or iCloud account connection.

## Who rings through?

An optional block source is checked first. Your explicit people list takes
precedence over contact classification. Personal contacts can ring through;
published or ambiguous contacts hear the lobby. A contact saved as a business
is not automatically treated like a family member.

The classification uses the card's fields, not an identity-verification
service. Caller ID can be spoofed. Review your exports and use deliberate
policy entries where the distinction matters.

## A directory is a different list

A phone book tells someone what number to dial. It does not grant that number
permission to skip the lobby. The optional messaging integration can receive
shared contact cards for the house or a handset directory, with configured
sender permissions. It requires the edge service, inbox configuration and a
carrier test for incoming contact-card delivery.

Start with the [runbook](https://github.com/ericdmoore/call-me-maybe/blob/main/docs/RUNBOOK.md).
Neither imported contacts nor a handset directory restricts outgoing calls
to approved people.
