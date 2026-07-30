# Contributing

Bug reports, fixes, docs, and packs all welcome.

## Before you start

Read `CLAUDE.md` — it has the hard invariants, and several of them fail in
ways that look like working software. In particular: caller IDs and PINs must
never reach the logs, session teardown must stay in the deferred `cleanup`,
and StasisEnd must stay distinct from ChannelDestroyed (collapsing them
breaks every transfer and every voicemail handoff).

## Ground rules

```bash
make hooks      # once, per clone — installs the pre-push gate
make check      # gofmt + vet + test + build; must be green
make cover      # tests with -race, plus per-package coverage floors
```

`make hooks` points `core.hooksPath` at the versioned `.githooks/`, so the
pre-push gate travels with the repo. It refuses a push on unformatted code,
vet findings, failing tests, an example config that no longer validates, or a
secret file that has been force-added past `.gitignore`. `git push
--no-verify` bypasses it for a genuine emergency; CI runs the same checks, so
the bypass only buys you the walk of shame in a pull request.

Coverage floors are per-package rather than one global number, because the
global figure is dominated by `cmd/` and `internal/ari` — thin glue over
Asterisk, covered by `scripts/smoke.sh` on real hardware instead of unit
tests. Floors live in `scripts/coverage.sh`. Raise them when you add tests;
do not lower one to get a push through.

- No new dependencies without a strong reason. There are two, both
  permissive, and the stdlib covers the rest.
- Tests use stdlib `testing`, colocated with the code. State machine changes
  go through the fake-ARI harness in `internal/lobby` — never add a test that
  needs a live Asterisk.
- Never commit real credentials: not a VoIP.ms sub-account password, a
  handset SIP password, an ARI password, or a real extension PIN. Test
  fixtures use `555-01xx` numbers, which are reserved for fiction.

## Licensing of contributions

**There is no CLA.** Apache 2.0 §5 already provides that a contribution is
licensed under the same terms as the project unless you say otherwise, which
covers the ordinary case without paperwork. This project will not be
relicensed or dual-licensed, so the rights a CLA would grant are rights
nobody needs.

DCO sign-off is welcome but not enforced:

```bash
git commit -s
```

That adds a `Signed-off-by:` line asserting you wrote the code or have the
right to submit it. It is an assertion, not a rights transfer.

By contributing you agree your code is licensed under Apache 2.0 and any
audio under CC BY-SA 4.0. See `LICENSES.md`.

## Contributing a pack

See `docs/PACKS.md` for the format. Two things get a pack rejected:

1. **Named characters, living people, or recognisable performer voices.**
   Archetypes only — this is the one hard rule and the reasoning is in the
   pack docs.
2. **Audio you do not have the rights to distribute**, including music you
   did not commission or source from a CC0 library, and output generated on a
   TTS free tier (which typically carries no commercial licence).

Community packs are CC BY-SA 4.0. Declare the licence in `pack.json`.
