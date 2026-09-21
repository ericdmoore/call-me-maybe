# s09 · One binary that installs itself, and the channels it ships through

**Status:** planned (2026-09-21). Drafted the evening the first customer
install happened — jepsen, an Ubuntu 26.04 x86 box, provisioned only from
published releases through `callmemaybe.cc/install.sh` and
`install-scripts/ubuntu.sh`. That rehearsal produced v0.5.0, v0.5.1 and v0.5.2
in one afternoon, one defect per release, every one in an apply path that
`--dry-run` and `go test` cannot reach. The conclusion is not "fix the
scripts"; it is that host preparation is in the wrong place.

## What done looks like

On a fresh Linux box with systemd, a person who has never seen this repository
types two commands:

```bash
curl -fsSL https://callmemaybe.cc/install.sh | bash
sudo doorman init
```

and ends with a registered trunk, handsets that register, and a phone that
rings. There is no git checkout on the hub, no `install-scripts/` directory,
and nothing on the box that did not come out of a release. `doorman init
--dry-run` prints exactly what the apply would do, from the same code, and the
apply path runs in CI on a real Ubuntu runner on every tag — not on a
customer's afternoon.

The same binary is also reachable the way people already install things:
`go install`, a Homebrew tap, a `.deb`/`.rpm` on the release, the AUR. A
package's post-install does the same host preparation by calling the same
code. Upgrades are `apt upgrade` or `brew upgrade`, and the daemon's release
notice names the channel the operator actually used.

"One static binary, nothing to install but the file" stops being an aspiration
about the binary and becomes a statement about the whole system: the file
installs everything else.

## Starting point

- `install.sh` fetches the release binary and man page, verifies the SHA-256,
  and offers to apt-install Asterisk. It is correct and stays.
- `install-scripts/` (landed 2026-09-18) is five distro entry points over one
  `common.sh`: packages, a `doorman` account, `/opt/call-me-maybe`, the unit.
  It requires the repository checkout on the hub and a binary "from the same
  checkout", which contradicts the release-only rule the first customer runs
  under. Its README said a fresh-VM rehearsal was pending. The rehearsal found:
  a `-r /dev/tty` guard that passes without a terminal (`33a7746`); awk
  `exit` under `pipefail` killing every Ubuntu apply with SIGPIPE right after
  `apt-get update` (v0.5.1); `/opt/call-me-maybe` at 0750 locking the operator
  out of its own next step and the CLI absent from sudo's `secure_path`
  (v0.5.2); and a rerun that refuses on `docs/INSTALL-LINUX.md` because a
  documentation file is copied with refuse-on-diff semantics (open).
- `doorman init` reads `examples/.env.example` **from disk**. Nothing in the
  binary is `embed`ded: the Asterisk templates, the examples, the unit and the
  smoke script all live in the checkout. This single fact is why the checkout
  has to be on the hub.
- The bundled prompt pack is **not in the tree and not on the release**.
  `prompts/build.sh` renders six WAVs with piper into the gitignored
  `prompts/build/`; FIRST-BOOT §7 says "the bundled pack ships with the repo",
  and it does not. A release-only customer has no greeting.
- `release.yml` is a hand-rolled five-target build loop with checksums; the
  asset names (`doorman-linux-amd64`, `checksums.txt`, `doorman.1`) are the
  contract `install.sh` depends on.
- The module is still named `callmemaybe`, so `go install` is impossible.
- `modernc.org/sqlite` (s08) is now a direct dependency — a transpiled-C
  pure-Go SQLite. It builds with `CGO_ENABLED=0` and is fine, but it is the one
  dependency that makes "boring static binary" heavier than it was in July, and
  the one that makes Debian-proper packaging a project of its own.
- Config paths are already environment-driven (`POLICY_PATH`, `HANDSETS_PATH`,
  `TRUNKS_PATH`, `CONTACTS_PATH`; the unit's `EnvironmentFile`), so the layout
  is a decision, not a refactor.

## Decisions and invariants

**Host preparation moves into the binary.** `sudo doorman init` creates the
service account and layout, installs Asterisk through the distro's package
manager, writes configuration as the service account, drops the Asterisk
files into `/etc/asterisk` with the generated ARI credentials already
matching, renders handsets and trunks, reloads Asterisk, installs and enables
the unit. Distro differences become a small table (package manager, package
name, unit name), not five files. Run without root, `init` does the
configuration half and prints the one `sudo` line. This generalises the
"nothing to install but the file" line in CLAUDE.md rather than bending it:
the binary gains privileged host-mutation code, all of it stdlib `os/exec`,
none of it on a call path.

**The root boundary is explicit.** `init` never apt-installs, creates users,
or touches `/etc/asterisk` without saying what it is about to do and getting a
yes — `--yes` for scripts, `--dry-run` for the plan. This is the property
`install.sh` already has for its Asterisk offer ("a piped install must never
silently sudo") extended to everything. Operator-owned files (`pjsip.conf`
where hand-written, `voicemail.conf`, `.env`, the policy files) are never
overwritten; managed files (`http.conf`, `ari.conf`, `extensions.conf`, the
unit, the rendered `*_handsets.conf`/`*_trunks.conf`) carry a generated-by
header and are replaced on rerun. A rerun *is* the upgrade procedure, which
retires the open defect above by construction.

**Everything `init` needs is embedded.** `asterisk/*.conf`, `examples/`,
`templates/`, `scripts/doorman.service`, `scripts/smoke.sh`, and the six
bundled prompt WAVs go into the binary with `embed`. Tests assert invariants
against the embedded copies at build time: `http.conf` binds `127.0.0.1`
(invariant 2), every PJSIP template and every rendered endpoint carries
`dtmf_mode=rfc4733` (invariant 9), every registration carries `line=yes` and
`endpoint=` (9b). `doorman init --export DIR` writes the embedded files out
for anyone who wants to read or fork them. Invariant 9a is unchanged:
generated files remain outputs, now written by `init` instead of copied by
hand.

**The prompt pack is committed and embedded.** Six WAVs at 8 kHz mono are
tens of kilobytes each; that is smaller than the SQLite driver. Committing
them makes FIRST-BOOT §7 true, versions the audio with the prompt-name
contract it implements, and means a greeting never depends on a download.
Invariant 7 is untouched — `init` installs pre-rendered files; nothing
synthesises speech on the box. `prompts/build.sh` remains the way to
regenerate them, and `LICENSES.md` already covers the CC BY-SA split.

**Layout follows the FHS.** Configuration in `/etc/doorman/` (`.env`,
`policy.toml`, `handsets.toml`, optional `trunks.toml`/`contacts.toml`),
state in `/var/lib/doorman/` (journal, call log, generated Asterisk files
before they are installed), the binary at `/usr/local/bin/doorman` — or
`/usr/bin/doorman` from a package. `/opt/call-me-maybe` existed only because
the repository was cloned there; with no checkout on the hub there is nothing
for it to hold. This is what a `.deb` wants anyway, and it removes the
"operator cannot `cd` into the config directory" class of defect: `/etc/doorman`
is `0750 root:doorman` with the operator in the `doorman` group for reading,
and `sudo -u doorman doorman …` remains the way to write, because the daemon
must be able to read and rotate what gets written (invariant 4's atomic
writes are unchanged).

**Releases are produced by GoReleaser, not a hand-written loop.** One YAML
emits the GitHub release with the *same asset names as today* — `install.sh`
must keep working unchanged against a new release — plus a Homebrew formula
pushed to a tap, `.deb`/`.rpm` built by nfpm, and an AUR `PKGBUILD`. It is a
CI-only dependency; nothing about it runs on the box or on a call path, so
the stdlib-first rule is not in play. The package post-install runs
`doorman init --host-only --yes` (account, directories, unit, `daemon-reload`;
no network, no interview, no service start — Debian policy and the root
boundary agree here) and declares `Recommends: asterisk`.

**The first customer is the test protocol.** jepsen runs releases only.
Each milestone below ends with jepsen wiped to a fresh OS and provisioned with
the two commands; anything found becomes a fix and a test before the next
tag. CI additionally gains a tag-only `rehearsal` workflow that runs the
apply path on `ubuntu-latest` end to end — `install.sh`, `sudo doorman init
--yes --rooms …`, `doorman check`, Asterisk active, ARI answering on
`127.0.0.1:8088`, the trunk-less rungs of `smoke.sh`. This deliberately
generalises the "never add integration tests requiring a real Asterisk to CI"
rule: that rule protects the call state machine, which stays fully testable
through `fake_ari_test.go`; the rehearsal tests the *installer* against a
distro package, never a trunk and never a call, and runs on tags rather than
gating every push.

**The module gets its real name first.** `github.com/ericdmoore/call-me-maybe`
touches every import path, so it lands before anything else rebases on it,
and `go install …/cmd/doorman@latest` becomes the zero-infrastructure channel.

## What is deliberately out of scope

- **Debian/Ubuntu archives and homebrew-core.** Debian proper needs a
  sponsor, an ITP, and every Go dependency as its own package; `gorilla/
  websocket`, `toml` and `yaml.v3` are already there, `modernc.org/sqlite` is
  not and is enormous. Asterisk itself was removed from Debian testing, so
  `Recommends: asterisk` cannot be satisfied on Debian anyway. homebrew-core
  has a notability bar (~75 stars, 30 forks, 30 watchers) that is earned, not
  built. Both are revisited when the numbers change, not before.
- **Snap.** Strict confinement fights a system service that talks to a
  package-installed Asterisk; classic confinement needs a review that a
  project this size will not win. No.
- **Building or bundling Asterisk.** Distro packages or nothing. Where a
  distro has no Asterisk 20+, `init` says so and stops; it does not compile.
- **Self-update.** The daemon's release notice (s08 era, #25) tells the
  operator a newer version exists and which channel to use; nothing updates
  automatically. That decision stands.
- **A second `setup` verb.** The ergonomics asked for are one command. Flags
  (`--host-only`, `--no-host`, `--dry-run`, `--yes`, `--export`) carve the
  behaviour; the verb stays `init`.

## Milestones and acceptance criteria

### M0 · The module's real name

Rename to `github.com/ericdmoore/call-me-maybe`; update every import,
`llms.txt`, CLAUDE.md, the man page. Done when `go install
github.com/ericdmoore/call-me-maybe/cmd/doorman@v0.6.0` produces a working
`doorman` on a machine with only Go, `make check` is green, and the
`internal/schema` agreement tests still pass.

### M1 · Embed

Move `asterisk/*.conf`, `examples/`, `templates/`, the unit, `smoke.sh` and the
committed prompt pack into the binary. `init`, `template` and `render` read
embedded defaults; `doorman init --export DIR` writes them out. Commit the six
bundled WAVs and update `LICENSES.md` and FIRST-BOOT §7 to match.

Done when `doorman init` works from an empty directory with no checkout
anywhere; a test fails the build if an embedded `http.conf` binds anything but
`127.0.0.1`, if any embedded or rendered PJSIP endpoint lacks
`dtmf_mode=rfc4733`, or if a registration lacks `line=yes`/`endpoint=`; the
prompt-name contract test in `internal/lobby` checks the six embedded files
exist with the names `prompts.go` expects.

### M2 · `sudo doorman init` prepares the host

Add `internal/host`: distro detection from `/etc/os-release`, a package
table (apt/dnf/pacman; Asterisk 20+ version gate before any install; refusal
with a plain message where no package exists), service account, FHS layout,
`/etc/asterisk` installation with managed/operator file semantics, unit
install, reload and enable. One `Exec` interface, faked in tests the way
`lobby.ARI` is. `--dry-run` renders the plan from the same steps.

Done when fake-exec tests cover the plan for each distro, every refusal, the
non-root path and its printed `sudo` line, rerun idempotence, and the
managed-vs-operator overwrite rules; the tag-only `rehearsal` workflow passes
on `ubuntu-latest`; and jepsen, wiped to a fresh Ubuntu, reaches a registered
handset from the two commands and a VoIP.ms sub-account — with the findings
list empty, or fixed and tested before the tag.

### M3 · Delete the scripts, rewrite the front door

Remove `install-scripts/` and its tests. Rewrite FIRST-BOOT around the two
commands and drop the Raspberry-Pi framing to a hardware note; RUNBOOK §2
becomes "what `init` did and where it put things", the upgrade procedure
becomes "the package manager, or rerun `install.sh` and `sudo doorman
init`"; `pi@raspberrypi` examples go. `install.sh` loses its Asterisk offer —
`init` owns that now, with a dry run and consent — and becomes purely "get
the binary". The update notice names the channel in use.

Done when no document in the tree tells a customer to clone the repository
onto the hub, `scripts/smoke.sh` is reachable as `doorman smoke`, and the
site's install component and `llms.txt` say the same two commands.

### M4 · GoReleaser and the channels

Replace the build loop in `release.yml`. Configure raw-binary artefacts under
the existing names so `install.sh` is unaffected; add nfpm `.deb`/`.rpm` with
the post-install above and `Recommends: asterisk`; a formula pushed to
`ericdmoore/homebrew-tap`; an AUR `doorman-bin` `PKGBUILD`.

Done when, on clean machines, each of `curl … | bash`, `brew install
ericdmoore/tap/doorman`, `sudo apt install ./doorman_*.deb`, and `yay -S
doorman-bin` yields `doorman version`; the `.deb` post-install leaves the
account, directories and disabled unit in place with no service started; and
`install.sh` against the GoReleaser-built release verifies checksums exactly
as before.

### M5 · A signed apt repository (deferred)

`apt.callmemaybe.cc` on the site's Cloudflare account: `reprepro`/`aptly`
output, a signing key held in CI with a written custody and rotation plan,
one `.sources` file for customers. Undertaken when there is a second
customer; until then the `.deb` on the release is enough. Done when `apt
upgrade` upgrades the phone and the release notice points at it.

## Alternatives rejected

- **Keep `install-scripts/` and harden them.** Five apply paths that no
  test executes, a checkout required on the hub, and a dry run that cannot be
  the same code as the apply. Four defects in the first live run is the
  evidence; the fix is structural.
- **Ansible, cloud-init, or a container image.** Each is another runtime on
  the hub, and a container holding Asterisk plus systemd is a worse day than
  the one being avoided. The README already says "not an OS installer or a
  Docker image"; this plan agrees.
- **Download templates and prompts at `init` time instead of embedding.**
  Puts a network fetch between a fresh box and a working phone, and creates a
  second artefact whose version can disagree with the binary's. Embedding
  makes the binary the single source of truth.
- **Keep `/opt/call-me-maybe`.** It is where a git clone went. Packages,
  `EnvironmentFile`, `StateDirectory` and the operator's expectations all
  point at the FHS; keeping `/opt` would only preserve the checkout-shaped
  layout the checkout is leaving.
- **A shell post-install in the `.deb` duplicating the host steps.** Two
  implementations of "create the account and directories" drift. The
  post-install calls the binary's own `--host-only` path.

## Rollout order

M0, then M1, then M2 with the jepsen rehearsal and the CI rehearsal workflow
together — M2 is not done until both pass. M3 follows immediately so no
release ever ships both mechanisms. M4 is independent of M3 and can proceed
in parallel once M2's `--host-only` exists for the post-install. M5 waits
for a reason.

Promote milestones to `docs/TASKS.md` when scheduled; this document records
the design and the evidence for it.
