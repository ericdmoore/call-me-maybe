---
description: Pre-commit and deploy check for Call Me Maybe. Use before committing or deploying to the Pi.
allowed-tools: Bash(git *), Bash(grep *), Bash(make *), Bash(scp *), Bash(ssh *), Read, Grep
---

# Ship it

## Before committing

```bash
make check           # vet + test + build, must be green
```

Then check nothing secret is staged. `.env`, `policy.toml`,
`asterisk/pjsip.conf`, and `asterisk/ari.conf` are gitignored — confirm none of
them, and nothing containing their contents, is in the diff:

```bash
git diff --cached --name-only
git diff --cached | grep -inE 'password|secret|[0-9]{6}|voip\.ms' || echo "clean"
```

Phone numbers in tests must be `555-01xx`, which is reserved for fiction. A
real number in a fixture is a leak even if it is your own.

Re-read the invariants in `CLAUDE.md` against the diff. In particular: does any
teardown path bypass `CallSession#finish()`? Does any new log line print an
unredacted caller ID at `info` or above?

## Deploying

The deploy artifact is one binary. Build here, ship the file:

```bash
make cross
ssh pi 'cp /opt/call-me-maybe/bin/doorman /opt/call-me-maybe/bin/doorman.prev'
scp bin/doorman-linux-arm64 pi:/opt/call-me-maybe/bin/doorman   # armv7l Pi → armv7 build
ssh pi 'cd /opt/call-me-maybe && git pull'                      # config, scripts, docs
ssh pi 'sudo systemctl restart doorman && sleep 2 && systemctl is-active doorman'
ssh pi 'cd /opt/call-me-maybe && ./scripts/smoke.sh'
```

Rollback is `mv bin/doorman.prev bin/doorman` plus a restart.

Asterisk config is **not** in the deploy path. If the change touched
`asterisk/*.conf`, copy it to `/etc/asterisk/` and reload explicitly —
otherwise the repo and the running system silently diverge.

Restarting doorman does not drop live calls: channels already bridged stay up
in Asterisk and simply fall out of Stasis when the humans finish. Restarting
*Asterisk* does drop them, so check `core show channels` first.

## After deploying

Place a real test call from a number not on the allow-list. Confirm the lobby
greeting and the dismissal. `smoke.sh` verifies everything except that the
phone actually rings.
