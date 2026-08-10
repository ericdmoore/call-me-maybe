# Examples

Two kinds of file live here, and the difference matters.

**The templates** — `policy.example.toml`, `handsets.example.toml`,
`trunks.example.toml`, `.env.example` — are a tour of the syntax. Every
section appears at least once, whether or not any one household would want
it. This is what `doorman init` and the runbook copy from.

**The scenarios**, under `scenarios/`, are worked configurations: a named
household or business with stated requirements, and only the keys that
household actually needs. Start here if one of them resembles you.

| Scenario | For | Ships |
|---|---|---|
| [`scenarios/solo-business/`](scenarios/solo-business/) | One person, one number, working from home. Business hours, clients on the allow-list, an extension per concern ending in a mailbox rather than a dismissal | today |
| [`scenarios/family-line/`](scenarios/family-line/) | A household with one number and eight phones. Known callers ring everything; strangers get the lobby and then "good day" | today |

Home *and* business on one box, several ventures on several numbers, and an
out-of-hours answering flow are planned — see
`.plans/s02-home-and-office-config-examples/`.

## The layout

```
examples/
├── policy.example.toml          the templates: one of everything
├── handsets.example.toml
├── trunks.example.toml
├── .env.example
└── scenarios/
    └── <slug>/                  one directory per scenario
        ├── policy.example.toml      required — this is what makes it an example
        ├── handsets.example.toml    required — a policy that cannot be copied is not an example
        └── trunks.example.toml      optional — most installs have one provider
```

The rules, which are also how the tests find them:

- **A directory holding a `policy.example.toml` is an example.** CI and
  `TestShippedExamplesHaveNoUnknownKeys` discover them by that filename;
  there is no list to keep up to date. Adding a directory adds it to the
  build.
- **Filenames match the templates**, so the copy instruction is the same
  everywhere and the `.example` infix keeps the files clear of the
  `policy.toml` / `handsets.toml` / `trunks.toml` gitignore rules that exist
  to stop a real config being committed.
- **A scenario answering several numbers** puts the extra lines in
  `policy.example.<line>.toml` beside the primary file — the same
  `policy.<line>.toml` convention `doorman check` discovers at runtime.
- **The directory slug names the situation**, not the plan item:
  `solo-business`, not `e2`.

## Every example carries a header

Who it is for, what it needs that does not exist yet, and what it
deliberately does not do. The third one is the load-bearing part: an example
that quietly requires an unbuilt feature is worse than no example, and an
example that quietly *fails* at something is how somebody discovers a limit
from a phone call.

## Every example is checked

Two assertions in CI, both over the whole set:

```bash
# every example must load
doorman check --allow-placeholders --handsets <dir>/handsets.example.toml <dir>/policy.example.toml

# and none of them may pass a strict check
doorman check --handsets <dir>/handsets.example.toml <dir>/policy.example.toml   # must fail
```

The second is not a formality. Every PIN in every example is the `CHANGEME`
sentinel — not a weak PIN, an impossible one, because the lobby compares
keypad digits exactly and no sequence of keypresses produces a letter. Copy
an example and it refuses to load until `doorman init` replaces them. A
placeholder that merely *looks* wrong (4242) works silently forever.

Phone numbers are all in `555-01xx`, which is reserved for fiction.

## These are starting points, not supported configurations

Copy one, run `doorman init`, then `doorman check` and read what it tells you
it resolved to — including the defaults you did not write. Nobody is
maintaining your fork of it, and no example is a promise about anything.
