# Linux phone-hub installers

Prepare a **Linux host running systemd** for Asterisk and doorman side by side.
These scripts install prerequisites and the doorman service layout; the
household's configuration and call activation follow in the runbook.
They are not an OS installer or a Docker image.

## Choose a distribution

| Entrypoint | Target | Asterisk path |
|---|---|---|
| `ubuntu.sh` | Ubuntu Server 24.04 / 26.04 LTS | Native package, candidate must be 20+; Universe must be enabled |
| `debian.sh` | Debian 12 / 13 | Checks configured apt repositories; normally provision 20+ separately and use `--existing-asterisk` |
| `centos.sh` | CentOS Stream 9 / 10; Rocky, AlmaLinux, RHEL 9 / 10 | Checks enabled dnf repositories; use `--existing-asterisk` when unavailable or too old |
| `fedora.sh` | Fedora with dnf and systemd | Checks the native package version before installation |
| `arch.sh` | Arch Linux | Requires an existing 20+ install and `asterisk.service`; never runs an AUR helper as root |

Ubuntu is the initial rehearsal target. All other entries are provisioning
adapters, **not claims of completed live distro testing**. Arch performs a full
`pacman -Syu` transaction to avoid an unsupported partial upgrade. The scripts
add no third-party repositories and never build Asterisk from downloaded source.

Package research (September 2026): Ubuntu 24.04 packages Asterisk 20.6;
EPEL 9 lists Asterisk 18, below our minimum. Debian removed Asterisk from testing
before Bookworm, so do not assume `apt install asterisk` works there. Arch's
Asterisk recipe is in the AUR. Install a maintained 20+ package/build yourself
on these systems, with PJSIP, ARI, voicemail and a systemd unit, then use
`--existing-asterisk`. A version check alone does not prove the modules or calls
work: complete the runbook's verification ladder.

Sources: [Ubuntu package](https://packages.ubuntu.com/noble/asterisk),
[Fedora/EPEL packages](https://packages.fedoraproject.org/pkgs/asterisk/asterisk/),
[Debian removal](https://tracker.debian.org/news/1428001/asterisk-removed-from-testing/),
[Arch AUR](https://aur.archlinux.org/packages/asterisk),
[CentOS lifecycle](https://www.centos.org/centos-linux/).
Legacy CentOS Linux 7/8 and Stream 8 are deliberately rejected. Other common
choices include openSUSE; Alpine uses OpenRC by default and would need a
separate service integration, so neither has an adapter here yet.

## Prepare the files

Use a binary from the **same release** as this checkout. The normal way is the
installer, run on the hub as the operator — it picks the build for the host
and verifies its SHA-256 against the release's checksums. The hub needs no
Go compiler:

```bash
git clone https://github.com/ericdmoore/call-me-maybe && cd call-me-maybe
curl -fsSL https://callmemaybe.cc/install.sh | bash      # → ~/.local/bin/doorman
git checkout "v$(~/.local/bin/doorman version | awk '{print $2}')"
```

`make cross` on a workstation and copying `bin/doorman-linux-amd64` over is the
development alternative. Either way, do not transfer a workstation's real
`.env`, `policy.toml` or SIP credentials.

Then, from that checkout:

```bash
# Preview works without root or networking, including on a Mac.
bash install-scripts/ubuntu.sh --dry-run --binary ~/.local/bin/doorman
sudo bash install-scripts/ubuntu.sh --binary ~/.local/bin/doorman

# Other hosts, after separately installing a suitable Asterisk and unit:
sudo bash install-scripts/centos.sh --existing-asterisk --binary ~/.local/bin/doorman
sudo bash install-scripts/arch.sh --existing-asterisk --binary ~/.local/bin/doorman
```

Apply mode verifies the binary runs before installing packages. Dry-run prints a conditional plan; it does not certify package
availability, distro compatibility, privileges or binary architecture.

## What gets installed

- Asterisk from the configured package manager, unless `--existing-asterisk`.
- CA certificates, rsync, OpenSSL and the SQLite CLI.
- A system `doorman` account with no login and no created home.
- `/opt/call-me-maybe` (0755, owned by `doorman`): `bin/doorman`, public
  examples, templates, Asterisk configuration templates, docs, smoke script
  and CEL schema.
- The same binary at `/usr/local/bin/doorman`, so `doorman` resolves on the
  operator's PATH and on sudo's `secure_path`. The unit runs the `/opt` copy.
- The repository's `doorman.service` and `doorman-directory.service` in
  `/etc/systemd/system/` (neither is enabled; the directory unit is the
  phones' phone book and exits quietly until `PROVISION_ADDRESS` is set).
- Private `/var/lib/doorman` and `/var/lib/doorman/journal` directories (0700).

The scripts preserve existing config. Different installed binaries, units or
managed templates cause a refusal instead of an implicit upgrade. Public docs
and templates copied by rsync keep destination edits. This is repeatable host
preparation, not a transactional rollback or an upgrade tool: a failure can
leave prerequisite packages and earlier steps installed. Follow the runbook
for upgrades and rollback.

They do not write to `/etc/asterisk`, generate credentials, install prompt audio,
initialise the CEL database, open firewall ports, disable SELinux/AppArmor, or
enable/start doorman. A distro package's own hooks may start Asterisk. Use a
fresh host for the initial installation. Keep host security controls enabled;
RPM-based installations may need local SELinux policy work after configuration.

## Finish configuration

The service account owns the working directory so `init`, `render` and atomic
policy rotation can create files it can later read. `sudo -u doorman` is about
ownership, not privilege — it just runs the CLI as that account:

```bash
cd /opt/call-me-maybe
sudo -u doorman doorman init --rooms 'Kitchen,Office'
sudo -u doorman doorman check
```

Record the generated PINs securely. Continue with [RUNBOOK §2](../docs/RUNBOOK.md):
set your people/provider/handset configuration, install Asterisk configuration,
put the same generated ARI credentials in `.env` and `ari.conf`, and keep
`http.conf` bound to **127.0.0.1:8088**. Render as doorman and install its generated
files with the permissions described there. Copy your prerecorded prompts.

Review optional journal/CEL setup in [events.md](../docs/events.md). Enabling CEL
requires its own schema, file access and module checks; the installer does not
automatically grant doorman access to Asterisk's logs or voicemail recordings.

After configuration and validation, explicitly activate services:

```bash
sudo systemctl enable --now asterisk
sudo systemctl restart asterisk  # loads the configuration you just installed
sudo systemctl enable --now doorman
sudo bash scripts/smoke.sh
```

Do this before putting the hub into use; restarting Asterisk interrupts calls.
Follow the runbook for LAN/provider-specific SIP/RTP firewall access. The final
acceptance test is real, non-emergency calls in both directions, including audio,
DTMF, voicemail and transfers.

## Verification

`go test ./install-scripts` exercises argument handling, every distro's dry-run,
package-version rejection, and preservation of existing files without root or
network access. CI also runs ShellCheck on every script. These checks are not
live installation tests; a fresh Ubuntu VM rehearsal is still pending.
