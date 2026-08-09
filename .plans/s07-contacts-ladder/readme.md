# s07 · The contacts ladder — development plan

Let the people already in your address book reach the house, without letting
anyone who can read a website do the same.

**Status:** planned. Nothing started.
Related: issue #12 (allow-list trusts spoofable caller ID), issue #6 (nominate
to the allow-list), `docs/writing-policies.md`, `.plans/s04-standarddize-network-helpers`
(URL-addressed sources, same instinct).

---

## Why

`[[people]]` is hand-typed and therefore always out of date. Meanwhile every
household already curates a contact list, on phones, continuously, without
being asked. The list exists; the phone just cannot see it.

The obvious objection is that the allow-list bypasses the lobby entirely and
caller ID is spoofable (#12). But that objection is weaker than it looks, and
getting it right is what this whole plan turns on.

---

## The line: is the number published?

**Spoofing requires knowing what to spoof.** Going from six allow-listed
numbers to two hundred does not help an attacker, because the numbers they
could plausibly discover — the school, a business you deal with — are exactly
the ones already on the short list. Nobody guesses their way into your address
book.

So list *size* is not the risk. **Discoverability is.**

> If a stranger can look the number up, it must not be automatic admission.

A plumber's number is on their website. A dentist's is in a directory. Your
sister's mobile is nowhere. The first two are impersonable by anyone with a
search engine; the third requires knowing you.

**And the vCard already carries the signal**, which is the happy accident that
makes this implementable without lookups: the thing that makes a contact "a
business" in an address book is the same thing that makes its number findable.

| Signal | Reads as |
|---|---|
| `ORG` is set | a business — published |
| `TEL;TYPE=work` | probably published |
| Toll-free NPA (800/888/877/866/855/844/833) | never personal |
| A name, `TYPE=cell`/`home`, no `ORG` | a person |

**Fail conservative, because the asymmetry is stark.** Wrong-closed means the
plumber hears a ten-second greeting and dials an extension. Wrong-open means a
findable number rings every phone in the house at 3am. Anything ambiguous gets
the lobby.

---

## The ladder

Evaluated in order; the first match wins.

| | Source | Result |
|---|---|---|
| 1 | **Block-list** | Dismissed. Never reaches the lobby |
| 2 | **`[[people]]`** | Straight through — you said so explicitly |
| 3 | **Contacts, personal** | Straight through |
| 4 | **Contacts, published** | Lobby: dial an extension |
| 5 | Unknown | Lobby |

Three properties worth stating:

**Block beats everything, including `[[people]]`.** If a number is in both, the
block wins and `doorman check` says so loudly — it is a contradiction the
operator should resolve, not something to silently rank.

**`[[people]]` beats the heuristic.** Hand-typed entries are explicit intent. If
you genuinely want the dentist straight through, write them down; the classifier
does not get a vote. This keeps the existing file meaningful rather than
redundant, and gives an override needing no new mechanism.

**A blocked caller does not hear the lobby.** That is the difference between
"not admitted" and "blocked" — the spam case wants the door not to open at all.

---

## Several sources, unioned

The real shape of a household: your contacts, your spouse's, later the kids'.
In an SMB, several employees' exports. Each is its own file from its own
account, and they must merge into one set.

```toml
[contacts]
cache_dir = "/var/lib/doorman/contacts"   # 0600, holds everyone's address book
refresh   = "6h"

[[contacts.sources]]
id        = "eric"
url       = "https://bullmoose.cc/contacts/eric.vcf"
token_env = "CONTACTS_ERIC_TOKEN"

[[contacts.sources]]
id        = "becky"
url       = "https://bullmoose.cc/contacts/becky.vcf"
token_env = "CONTACTS_BECKY_TOKEN"

[[contacts.sources]]
id   = "blocked"
url  = "https://bullmoose.cc/contacts/blocked.vcf"
kind = "block"          # admit (default) | block
```

A source may also be a local path, so a file dropped by any other tool works
and the feature is testable without a network.

### De-duplication

**Key: the phone number, normalised to E.164** with the existing
`policy.E164OrEmpty`. Two vCards carrying the same number are the same
person for admission purposes, whatever they are called.

- One vCard with several numbers becomes several entries sharing a name.
- An unparseable number is skipped and **counted**, and `doorman check` reports
  the count per source — silence there would be the same defect as swallowing
  an unknown config key.

### Conflicts, resolved conservatively

| Conflict | Resolution |
|---|---|
| Same number, different names | first source in declaration order wins |
| One source personal, another published | **published** — the restrictive answer |
| Present in an admit source and a block source | **blocked** |

Declaration order is the only ordering, so "which name shows up" is answerable
by reading the config top to bottom. No scoring, no recency.

---

## Fetching

- **At startup, then every `refresh`.** A background goroutine — never on a
  call path, and no call ever waits on a fetch.
- **Per-source cache file**, raw vCard as fetched. A source that fails keeps its
  last good copy and the others are unaffected. This is invariant 4's posture
  applied to a second kind of input.
- **A source that has never succeeded contributes nothing** and does not stop
  the phone. It is reported, not fatal.
- **Conditional requests** (`If-None-Match` / `If-Modified-Since`) so a
  six-hourly poll of an unchanged book costs almost nothing.
- The merged set is compiled in memory from the caches. At a few hundred
  contacts this is microseconds, and re-deriving beats keeping a second
  artefact that can disagree with the first.

### The URL is a credential

A contacts URL with a token in it *is* the secret. This project has already been
bitten once: every `net/http` transport error wraps into a `*url.Error`
carrying the full URL, so the obvious `log.Warn("fetch failed", "err", err)`
ships it to journald — and `nologsecrets` cannot catch it, because the
offending identifier is `err`. See `internal/notify`, which unwraps and logs
the host alone. Do the same here.

---

## The cache is an optimisation, never a source of truth

This is how the feature sits beside "no database".

Delete `cache_dir` and the phone still works — everyone simply hears the lobby
until the next fetch, and `[[people]]` is untouched because it lives in
`policy.toml` where it always did. Nothing is authoritative that is not a file
the operator wrote.

Mode 0600. It holds several people's entire address books, which is more
personal data than anything else this project stores.

---

## Milestones

### M1 · Parse and classify, from a local file

No network. `contacts.vcf` on disk, parsed, normalised, classified, unioned,
de-duped. `doorman check` prints the resulting counts: admitted, published,
blocked, skipped.

vCard is messier than it looks — 2.1/3.0/4.0, quoted-printable, folded lines,
`TYPE` parameters spelled several ways. Handle what real exporters emit
(iCloud, Google, CardDAV) and skip-and-count the rest.

**Deliverable:** the ladder works end to end for anyone willing to drop a file
in place. Testable with no network and no bullmoose.

### M2 · The ladder in the state machine

Wire tiers 1 and 3–4 into the lookup `internal/lobby` already does for
`[[people]]`. Blocked callers dismissed without the lobby.

Invariant 1 applies: a contact's name and number are caller data. Redact in
logs, and the call record's existing `known` field carries the name exactly as
an allow-list match does.

### M3 · The fetcher

Sources, schedule, per-source cache, conditional requests, last-good-on-failure.
Local paths and URLs by the same config.

### M4 · Visibility

Staleness and provenance where an operator will see them: `doorman check`
showing each source's age and counts, and — the one that prevents the support
call — **a contact admitted by the classifier for the first time should be
visible**, in the call log if not louder. "Why did the phone ring for someone I
never allow-listed" needs an answer that is not "read the source."

---

## Invariants

| # | Effect |
|---|---|
| 1 | Contact names and numbers are caller data — redacted in logs |
| 4 | Extended in posture: a failed fetch keeps the last good cache, per source |
| 5 | Untouched. Contacts admit or block; they never carry PINs |
| 10 | The cache is derived, disposable, and never authoritative. Deleting it degrades to "everyone hears the lobby" |

---

## Risks

| Risk | Mitigation |
|---|---|
| **The classifier admits a published number** | Conservative defaults; `[[people]]` as the explicit override; M4 makes first-time admissions visible |
| A contact deleted upstream silently loses access | Correct behaviour, surprising in the moment. Report it rather than letting it just happen |
| The cache leaks several people's address books | 0600, on the box, never in a payload that leaves — and never committed |
| The contacts URL leaks through an error | Unwrap and log the host, per `internal/notify` |
| Contacts become a second policy surface people edit | They are *ambient*; `[[people]]` stays the deliberate one, and the docs must keep saying which is which |

---

## Out of scope

Writing back to a contacts source · CardDAV as a protocol (fetch an export,
do not implement a sync client) · per-line contact sets (the config shape
leaves room: `[line] contacts = [...]` later, without breaking) · community
blocklists, which hand a stranger the right to decide who may call you.

---

## Do this regardless

Independent of whether any of the above ships, `docs/writing-policies.md`
should carry the rule the whole plan turns on:

> Do not allow-list a number that appears on a website. Anyone who can read it
> can present it.

That is true of `[[people]]` today, costs nothing, and is the thing people get
wrong by default — the plumber is exactly who you would think to add.
