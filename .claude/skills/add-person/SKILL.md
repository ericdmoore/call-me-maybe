---
description: Add someone to the allow-list, add a handset, or add or rotate an extension PIN in policy.toml. Use for any change to who can reach the house.
allowed-tools: Bash(./bin/doorman *), Bash(make *), Read, Edit, Grep
---

# Change who gets in

Request: $ARGUMENTS

`policy.toml` is gitignored and holds real numbers and real PINs. Edit it
directly; **never** commit it, and never paste its contents into a commit
message, a test fixture, or a PR description.

## Adding a person

Numbers may be written any way — `512-555-0100`, `(512) 555-0100`,
`+15125550100` — they are normalised to E.164 at load. A number that cannot be
normalised is a **load error**, not a silent never-match, so a typo will be
caught by validation rather than by someone getting bounced.

```toml
[[people]]
name = "Full Name"
numbers = ["512-555-0100"]
notes = "why they are on the list"
```

## Adding a handset

Handsets live in `handsets.toml` (the hardware inventory), not policy. One
block + one env var + one render:

1. Add `[[handsets]]` to `handsets.toml`: id, `endpoint = "PJSIP/<id>"`
   (render enforces the naming), number (101-199), page/mailbox as wanted,
   and `password_env` naming a new `HANDSET_*_PASSWORD` in `.env`.
2. `./bin/doorman render` and install per its printed instructions. Never
   hand-edit the generated `*_handsets.conf` files.
3. Reference the id from `policy.toml` where it should ring.

The split loader validates cross-references on every reload, and render
refuses to run with a missing password env — both failures name the fix.

## Adding or rotating a PIN

Never choose PINs by hand and never edit a `pin = ` line yourself — use the
built-in rotation, which generates crypto/rand PINs, preserves the file's
comments, validates before writing, and writes atomically:

```bash
./bin/doorman rotate Kitchen      # one extension, by label (case-insensitive)
./bin/doorman rotate              # everything, including disabled extensions
```

New PINs print to stdout for the operator and must never be logged, committed,
or pasted into a summary of the work. When *adding* a new extension, write it
with a placeholder pin of the right length (e.g. "000000"), run
`./bin/doorman check`, then immediately `rotate <label>` to replace the
placeholder.

Keep every PIN the same length. When all PINs share a length the collector
fires the moment the last digit lands instead of waiting out the inter-digit
timer, which is noticeably snappier for the caller.

## Always finish with

```bash
./bin/doorman check
```

Then confirm the live reload actually happened — a rejected file leaves the
previous policy in service, which is the safe behaviour but means a typo can
look like success:

```bash
journalctl -u doorman -n 5   # want: "policy reloaded"
```

No restart is needed. If `POLICY_WATCH=false`, restart doorman instead.
