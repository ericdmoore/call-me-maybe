# s27 · Backup — the house as one sealed file, and a new box from it

**Status:** M1–M3 built 2026-10-03 (the package, `backup init|run|list|verify`,
the file destination, retention, the timer under `init services`, the check
line, `restore`); M4 (s3) and M5 (jepsen) next. Planned the same day from
issue #29.

## What done looks like

Every night at 05:10 jepsen writes `callmemaybe-jepsen-20261004T1010Z.age`
— `.env`, the TOMLs, `/var/lib/doorman`, voicemail, un-named `*88` clips;
about 3.5 MB — encrypted to a key the box holds only the public half of,
and delivers it to every destination `.env` names: a directory, an
S3-compatible bucket, the account. `doorman check` says "last backup: 6h
ago, 3 destinations" or warns. The card dies; a fresh install runs
`doorman restore --from s3 --latest`, pastes the private key, and the
phones reconnect to the same box they knew, without being re-provisioned.

## Decisions and invariants

**The bundle carries the secrets; the box cannot open it.** `.env` is in
the archive — a restore without it is a reinstall. So `doorman backup init`
makes an X25519 keypair (age), keeps the public half in `.env`
(`BACKUP_RECIPIENT`), and prints the private half once, as `rotate` prints
PINs. The box encrypts nightly and can never decrypt; a destination holds
blobs it cannot read; a stolen box cannot read its own backups. Lose the
key and every bundle made with it is a brick — by design, and said so at
`init`. Escrow of that key is the account's paid feature (#29), sealed by
a customer-only secret, never readable by the service.

**What is in it** — the house, not the software:
`.env`, `policy*.toml`, `handsets.toml`, `trunks.toml`, `contacts.toml`,
`messages.toml`; `/var/lib/doorman/` (journal via `VACUUM INTO` for a
consistent copy, the provisioning certificate and state the phones already
trust, the inbox seen-set, the `*88` own books); the voicemail spool;
`/var/spool/call-me-maybe/` (clips still waiting for a name). **Not** the
binary, the prompts, the OS, or the generated `*_handsets.conf` /
`*_trunks.conf` — `render` reproduces those. `/etc/asterisk` only where a
file differs from what this binary shipped (by SHA; marked `customised` in
the manifest) — the drift finding on #29.

**Format.** A tar of files at their absolute paths (so `restore` needs no
map), gzip (stdlib), age (X25519 recipient). `MANIFEST.json` first in the
tar: format version, doorman version, hostname, timestamps, per-file
sha256 and size, exclusions and why. `restore --dry-run` reads only the
manifest and says what it is holding before touching anything.

**Destinations are backends behind one interface**, several at once:
- `file` — `BACKUP_PATH`, a directory on the box or a mounted drive.
- `s3` — one backend for every S3-compatible store (AWS, R2, B2, MinIO):
  `BACKUP_S3_ENDPOINT`, `_BUCKET`, `_REGION`, `_KEY_ID`, `_SECRET`; present
  means enabled. SigV4 for four operations (PUT, GET, LIST, DELETE) on
  `net/http` — a few hundred lines, testable against a fake, and no SDK.
- `cloud` — the account: `doorman backup login` prints a device code, the
  page shows a token once, `BACKUP_CLOUD_TOKEN`. Built when the account
  exists; the interface is shaped for it now.

**Retention** is the destination's job on each run: keep `BACKUP_KEEP_DAILY`
(7) and `BACKUP_KEEP_WEEKLY` (8); prune the rest. Voicemail is the bulk:
`--no-voicemail` exists.

**`restore` is the real deliverable.** Verify the manifest; decrypt with the
private key (typed, or `--identity file`); refuse a box that already has a
`.env` unless `--force`, and then back up first; place files; `render`;
print the reloads as `render` does; `init services`. Across versions
through the current binary's `check`.

**It tells you.** `backup.completed` / `backup.failed` journal events
(destination named, never a path with a secret in it), a line in the
morning digest, `doorman check` warning past a day. `doorman-backup.timer`
nightly after the digest, with jitter; `init services` enables it when
`BACKUP_RECIPIENT` and at least one destination are set.

**Rejected: zstd and an S3 SDK.** gzip is stdlib and 3.5 MB does not care;
`aws-sdk-go-v2` is tens of megabytes of dependency for four requests.
**Rejected: the box keeping the private key "for convenience".** Then the
bundle is only as safe as the box, which is the thing that just died.

## Milestones

- **M1** `internal/backup`: manifest, collect (with `VACUUM INTO` and the
  shipped-file SHA rule), bundle (tar+gzip+age), unbundle, verify. Tests on
  a temp tree; a round trip restores byte-identical files.
- **M2** `doorman backup init|run|list|verify`, the `file` destination,
  retention, journal events, the `check` line, the timer unit and its
  `init services` rule.
- **M3** `doorman restore` with `--dry-run`, `--force`, `--identity`.
- **M4** the `s3` destination (SigV4, fake-server tests), proven against
  R2 or B2 with a real bucket once.
- **M5** jepsen: `backup init`, nightly to a directory on jepsen2 (the one
  box this house has that is not jepsen) and to a bucket; a restore
  rehearsal onto jepsen2 that ends with the phones registered to it.
