# Agent instructions

This project keeps its agent-facing notes in two files, deliberately not
duplicated here — two copies of the same guidance drift, and the failure mode
is an agent confidently following the stale one.

- **[`CLAUDE.md`](CLAUDE.md)** — the working notes: commands, invariants,
  conventions, and the things that fail in ways which look like working
  software. Read this before changing behaviour.
- **[`llms.txt`](llms.txt)** — orientation if you arrived with no other
  context: what the system is, the three configuration files, and the facts
  worth knowing before suggesting a change.

The single most useful thing to run first:

```bash
doorman schema
```

It prints the entire configuration surface — every key, type, default, and
cross-file reference for `policy.toml`, `handsets.toml`, and the environment —
as JSON Schema. Do not infer the config format from examples or from Go
source; ask the binary. Then validate real files with `doorman check`, which
is the authority on validity.
