# Call Me Maybe

![Carly Rae Jepson](https://media4.giphy.com/media/v1.Y2lkPTc5MGI3NjExcjhoaTRpN3EzaDVpbGF6eW1qMm5qbWRwMnNtb2h2NW1udWlzeTd3dSZlcD12MV9pbnRlcm5hbF9naWZfYnlfaWQmY3Q9Zw/kGdRnb1kF4OmQ/giphy.gif)

**A programmable home phone for a spare Raspberry Pi. Known callers ring the
whole house; everyone else meets the bouncer.**

[callmemaybe.cc](https://callmemaybe.cc)



*The home phone my kids never saw coming.*

A programmable home phone that runs on a spare Raspberry Pi.

Known callers hear *"Welcome, I'll connect you"* and the whole house rings. Everyone else meets the lobby: dial a 6-digit extension, or the bouncer says *"Good day"* and hangs up.

No port forwarding. No inbound firewall rules. The Pi registers **outbound** to a VoIP provider and the trunk rides that registration back in.

```
                 ┌─ known caller ──→ "Welcome, I'll connect you" ──→ 🔔 whole house
inbound call ──→ ┤
                 └─ unknown ───────→ "Welcome to the phone lobby.
                                      Please dial an extension." ──→ ✓ that handset rings
                                                                 └─→ ✗ "Good day." *click*
```

---

<sub>**Repo metadata** — description: *A programmable home phone for a spare
Raspberry Pi. Known callers ring the whole house; everyone else meets the
bouncer.* · topics: `asterisk` `voip` `sip` `raspberry-pi` `pbx` `homelab`
`self-hosted` `home-automation` `golang` `telephony`</sub>

## How it fits together

Asterisk does SIP, RTP, and DTMF detection. It is very good at those and there is no reason to reimplement them. Everything about *who gets in* lives in `doorman`, a single static Go binary that talks to Asterisk over ARI on loopback — one call is one goroutine, and the caller hanging up cancels a context that unwinds everything.

```mermaid
graph LR
    subgraph provider["VoIP.ms"]
        T["SIP trunk<br/>(registration-based)"]
    end

    subgraph pi["Raspberry Pi"]
        A["Asterisk<br/>SIP · RTP · DTMF"]
        D["doorman<br/>one static Go binary"]
        P[("policy.toml<br/>allow-list · PINs")]
        W["prompts/*.wav<br/>pre-rendered"]
        A <-->|"ARI · ws + REST<br/>127.0.0.1:8088"| D
        D --> P
        A --> W
    end

    subgraph lan["LAN"]
        H["Grandstream handsets<br/>register inward"]
    end

    T <-->|"outbound REGISTER<br/>+ 30s keepalive"| A
    A <--> H
```

The only connection that crosses the WAN is one the Pi opens itself.

Beyond the lobby, the handsets get the classic PBX toolkit — paging/intercom
(500), call parking (700), a family conference (600), BLF busy lamps, hold
music, voicemail with email delivery (*97), ringer ladders that escalate
kids → adults → voicemail, per-extension afterhours windows, and an optional
dial-555 hook into Home Assistant's Assist. Details in
[`docs/RUNBOOK.md`](docs/RUNBOOK.md).

### A call, end to end

```mermaid
sequenceDiagram
    autonumber
    participant C as Caller
    participant A as Asterisk
    participant D as doorman
    participant H as Handsets

    C->>A: INVITE (via trunk)
    A->>A: Answer()
    A->>D: StasisStart

    D->>D: normalise caller ID → E.164
    D->>D: look up in allow-list

    alt Known caller
        D->>C: ▶ "Welcome, I'll connect you"
        D->>A: create bridge + ring indication
        D->>H: originate all house endpoints
        H-->>D: StasisStart (first to answer wins)
        D->>A: bridge winner, hang up the rest
        Note over C,H: 🔊 talking
    else Unknown caller
        D->>C: ▶ "Welcome to the phone lobby…"
        loop 10s for first digit, 3s between
            C->>A: DTMF
            A->>D: ChannelDtmfReceived
        end
        alt Valid 6-digit extension
            D->>H: ring that handset/group
        else Timeout, or PIN attempts exhausted
            D->>C: ▶ "Good day."
            D->>A: hangup
        end
    end
```

### Lobby state machine

```mermaid
stateDiagram-v2
    [*] --> starting
    starting --> greeting_known: on allow-list
    starting --> greeting_lobby: unknown
    starting --> dismissing: rate limited

    greeting_known --> ringing

    greeting_lobby --> collecting: greeting ends
    greeting_lobby --> collecting: caller barges in

    collecting --> ringing: valid PIN
    collecting --> collecting: wrong PIN, attempts left
    collecting --> dismissing: 10s silence
    collecting --> dismissing: attempts exhausted

    ringing --> bridged: handset answers
    ringing --> dismissing: nobody home

    bridged --> [*]: hangup
    dismissing --> [*]: "Good day"
```

---

## Repo layout

```
call-me-maybe/
├── CLAUDE.md              project memory — invariants, conventions, commands
├── .claude/skills/        /diagnose-call, /add-person, /ship
├── cmd/doorman/           entrypoint, subcommands, ARI event router
├── internal/render/       handsets.toml → generated Asterisk config
├── internal/lobby/        the state machine — lobby, bouncer, ring groups + fake-ARI tests
├── internal/policy/       allow-list, PINs, E.164, hot reload, PIN rotation
├── internal/ari/          thin typed ARI client (REST + reconnecting WebSocket)
├── internal/config/       env parsing, names match .env.example
├── asterisk/              pjsip / extensions / ari / http / rtp config
├── prompts/               prompt text + piper build script
├── scripts/               systemd unit + smoke.sh
└── docs/                  RUNBOOK · TASKS · architecture · roadmap
```

`lobby` and `bouncer` aren't separate services — they're two branches of one state machine, so splitting them would add a network hop and no seam. `speech` isn't a service either: prompts are rendered once with piper and committed as WAVs, so the Pi never does TTS and a broken speech service can never make the phone unreachable. And doorman ships as one statically linked binary (`make cross` covers both 64-bit and 32-bit Pi OS) — nothing to install on the Pi but the file itself.

---

## Setup

### 1. VoIP.ms

Do this first; the values feed straight into `pjsip.conf`.

- **Sub Accounts → Create Sub Account.** Don't put your main login on the Pi. Auth type *User/Password*, device type generic ATA/IP phone, **NAT: yes**. You'll get a username like `123456_home`.
- **Pick the POP nearest you** and use that exact hostname everywhere.
- **DIDs → Manage DID → route it to that sub account.**
- **Codecs:** ulaw first, g722 second, disable the rest. A Pi has no business transcoding.
- **Enable E911 on the DID.** It's a couple of dollars a month and it's the one line item worth not skipping on a house phone.

### 2. Asterisk on the Pi

```bash
sudo apt install asterisk
cd /etc/asterisk
sudo cp /opt/call-me-maybe/asterisk/*.conf .
sudo cp /opt/call-me-maybe/asterisk/pjsip.conf.example pjsip.conf
sudo cp /opt/call-me-maybe/asterisk/ari.conf.example ari.conf
# edit pjsip.conf (sub account, POP, handset passwords) and ari.conf (password)
sudo systemctl restart asterisk
```

Confirm the trunk came up:

```bash
sudo asterisk -rx "pjsip show registrations"    # want: Registered
sudo asterisk -rx "pjsip show endpoints"
```

### 3. Prompts

On a workstation with piper and ffmpeg (not the Pi):

```bash
bash prompts/build.sh
rsync -av prompts/build/ pi@raspberrypi:/tmp/cmm-prompts/
```

Then on the Pi:

```bash
sudo mkdir -p /var/lib/asterisk/sounds/call-me-maybe
sudo cp /tmp/cmm-prompts/* /var/lib/asterisk/sounds/call-me-maybe/
sudo chown -R asterisk:asterisk /var/lib/asterisk/sounds/call-me-maybe
```

### 4. doorman

Configuration is three files with three jobs: **`.env`** holds secrets,
**`handsets.toml`** holds the hardware (and `doorman render` generates the
per-handset Asterisk config from it, so inventory and dialplan can't drift),
and **`policy.toml`** holds the rules — allow-list, extensions, ladders, and
named `[[schedules]]` referenced as `afterhours = "school-night"`.

Editing those files gets IDE support: `doorman lsp` is a language server
whose diagnostics come from the same validator that guards the daemon —
unknown handset ids, bad schedule references, and duplicate PINs get
squiggles as you type, with completions for every cross-reference. Works in
Neovim over SSH on the Pi with zero extra installation. See
[`docs/editor.md`](docs/editor.md).

On a workstation:

```bash
make cross
scp bin/doorman-linux-arm64 pi@raspberrypi:/opt/call-me-maybe/bin/doorman
# (uname -m says armv7l on the Pi? use bin/doorman-linux-armv7 instead)
```

On the Pi:

```bash
cp .env.example .env                    # set ARI_PASSWORD to match ari.conf
cp policy.example.toml policy.toml      # allow-list + extensions
./bin/doorman check
```

Then `sudo cp scripts/doorman.service /etc/systemd/system/` and enable it.

Or skip the build and take a release binary:

```bash
curl -fsSL https://raw.githubusercontent.com/ericdmoore/call-me-maybe/main/install.sh | bash
```

It detects your OS and architecture, verifies the SHA-256 against the
published checksums, and installs to `/usr/local/bin` (or `~/.local/bin` when
that isn't writable). `--version`, `--prefix`, and `--force` all work.

---

## Suggested hardware

`doorman` has no opinion about brands. Anything that registers to Asterisk as
a SIP endpoint works — `handsets.toml` only needs a `PJSIP/<id>`, and
`doorman render` generates the rest. So buy on ergonomics and price, not
compatibility.

### The Pi

Asterisk plus one Go binary is a light load; the phone plant is idle almost
all the time. **A Pi 4 (2 GB) or Pi 5 is plenty, and a 3B+ is fine** — this
is a genuinely good use for the one already in your drawer.

#### Raspberry Pi

* [Pi 5 on Amazon](https://amzn.to/3S3E5vV) — most headroom, wants active cooling and the 5 V/5 A supply
* [Pi 4 on Amazon](https://amzn.to/3RYUgdU) — the sweet spot; 2 GB is ample, and a PoE HAT fits
* [Pi 3 on Amazon](https://amzn.to/4xghXxv) — cheapest that still has wired Ethernet

What actually matters more than the model:

- **Use wired Ethernet for the Pi itself.** WiFi jitter on the box doing RTP
  is the difference between clear audio and someone sounding underwater. This
  is the one hardware choice worth being rigid about. (A Pi Zero 2 W has no
  Ethernet port — it needs a USB adapter, at which point buy a 4.)
- **Boot from a USB SSD if you can, or use an A2 microSD.** A 24/7 service
  writing logs and voicemail is the classic way to wear out a cheap card, and
  the failure looks like a phone that mysteriously stopped answering.
- **A PoE HAT** on a Pi 4 gets you power and network over one cable, which is
  what you want if it lives in a closet next to the switch.
- **Put it on a small UPS.** A house phone that dies in a power cut is worse
  than no house phone, because you thought you had one. (VoIP still dies with
  your internet — which is exactly why the E911 note above matters.)

### Handsets

Grandstream's Wi-Fi handsets are what this was built and tested against:

#### Grandstream Cordless WiFi IP Phone WP826 SIP Phone
[on Amazon](https://amzn.to/44YDPRP)

#### Grandstream WiFi Phone, 2.8 in Screen, Bluetooth WP836

#### Grandstream WP816 Compact Portable Wi-Fi Phone
[on Amazon](https://amzn.to/4yMiB7h)

### Other good-value options

Prices move, so these are categories rather than quotes. Every one of them
registers over SIP and drops into `handsets.toml` unchanged.

| Want | Look at | Why |
|---|---|---|
| **Cheapest desk phone** | Grandstream GRP2601/2602, Yealink T31P, Fanvil X3U | PoE, wired, one cable, nothing to charge. The most reliable phone per dollar. |
| **Cordless done properly** | Grandstream DP752 base + DP722/DP730 handsets; Yealink W73P/W79P | DECT, not Wi-Fi: days of battery, and it roams between rooms without dropping the call. One base carries several handsets, each registering as its own SIP account — so one `[[handsets]]` entry per handset, exactly like a desk phone. |
| **Reuse the phones you own** | Grandstream HT802 (2 FXS ports), Obihai OBi200 | An ATA turns your existing analog cordless base — or a 1950s rotary phone — into a SIP endpoint. Often the cheapest path in the house, and the most fun. |
| **Nicest per dollar** | Used Poly VVX 250/350/450, Cisco SPA504G | Office decommissions flood the used market at a fraction of new. Budget an hour for factory-resetting units that were provisioned by their last owner. |
| **Free** | Linphone, or Groundwire on a kid's phone | A softphone is a real endpoint. Good way to test a ladder before buying anything. |

**Wi-Fi vs DECT vs wired, honestly:** Wi-Fi handsets are convenient and give
you one less base station, but they hand off between access points poorly —
walk from the kitchen to the garage mid-call and you may lose it. DECT is the
technically better answer for cordless and its batteries last far longer.
Wired PoE desk phones never surprise you at all. A sensible house is usually
a couple of wired phones in fixed spots plus one cordless system.

<sub>Amazon links above are affiliate links — if you buy through them the
project earns a commission at no cost to you. They do not affect which
hardware is recommended; nothing here is sponsored, and the alternatives
table exists precisely so the recommendation isn't just the thing with a
link.</sub>

---

## Design notes

**Ten seconds is for the first digit, not all six.** A stranger reading a PIN off a card needs longer than a stopwatch allows. The window is 10s to the *first* digit, then 3s between digits — so a confident caller is through in two seconds and a fumbling one still gets a fair shot. Both are tunable in `.env`.

**Extensions are PINs, so treat them like passwords.** A 6-digit keypad exposed to the PSTN is a million combinations — plenty against a human, not much against a machine that can redial. After three failed calls in an hour, a number skips the greeting entirely and goes straight to "Good day": no prompt, no dial window, nothing to brute-force against. And don't pick PINs at all: `doorman rotate` generates them with crypto/rand, rewrites `policy.toml` in place (comments intact), and the running daemon picks the change up within a second.

**Caller ID is normalised before anything looks at it.** VoIP.ms passes the calling number roughly as the originating carrier hands it over, so the same person shows up as `5125550100`, `15125550100`, and `+15125550100` on different days. Compare raw strings and Grandma meets the bouncer about a third of the time. Everything folds to E.164 first — probe any value with `doorman e164 "(512) 555-0100"`.

**A bad `policy.toml` can't take the phone down.** Edits are picked up live, but a file that fails validation is logged and discarded — the last good policy stays in service. Run `doorman check` before you rely on it anyway.

**Withheld caller ID always meets the bouncer.** Anonymous callers share a single rate-limit bucket, which means one persistent blocked caller can dismiss others faster. That's the intended trade.

---

## Working on this

`CLAUDE.md` is loaded automatically by Claude Code and carries the invariants —
the things that, if broken, fail in ways that look like working software.
[`docs/RUNBOOK.md`](docs/RUNBOOK.md) has provisioning, a bottom-up verification
ladder, a symptom-to-cause troubleshooting table, and the raw ARI calls for
probing by hand. [`docs/TASKS.md`](docs/TASKS.md) is the backlog with acceptance
criteria.

Three skills ship with the repo: `/diagnose-call`, `/add-person`, `/ship`.

```bash
make hooks                # once per clone — installs the pre-push gate
make check                # gofmt + vet + test + build
make cover                # -race, plus per-package coverage floors
./scripts/smoke.sh        # on the Pi — verifies the whole chain
```

The pre-push hook (`.githooks/pre-push`, versioned with the code) blocks a
push on unformatted code, vet findings, failing tests, an example config that
stopped validating, or a secret force-added past `.gitignore`. CI runs the
same checks on every push and pull request, cross-compiles both Pi targets,
and shellchecks the shipped scripts. Tagging with `make release-tag TAG=x.y.z`
builds all five targets, stamps the version into each binary, and publishes a
release with SHA-256 checksums that `install.sh` verifies.

The state machine in `internal/lobby/session.go` is fully covered through the
fake-ARI harness in `internal/lobby/fake_ari_test.go` — known callers,
valid/invalid PINs, timeouts, barge-in, ring-group races, and mid-call hangups
all run without an Asterisk.

## Not built yet

Voicemail is sketched but not wired: `record` on the caller channel → whisper on a beefier box → email with the transcript and the original audio attached. The `.env` block for it is parsed and ignored so the shape is settled. See [`docs/roadmap.md`](docs/roadmap.md).

## Packs

The bouncer's personality is a swappable folder of audio. `PROMPT_MEDIA_PREFIX`
points at a pack; drop in another and the lobby greets strangers as a Victorian
doorman, a bureaucrat, or a ship's computer instead. The format is specified
and CC0 — see [`docs/PACKS.md`](docs/PACKS.md) to build one.

The line this project holds: **mechanisms are free, content is optional.**
Every mechanism — lobby, bouncer, ladders, hold, sound board — is Apache 2.0
and works completely with the bundled pack. Packs make it more fun; nothing
requires buying one. See [`docs/SUSTAINABILITY.md`](docs/SUSTAINABILITY.md)
for how that squares with paying for the project's upkeep.

## License

Code is [Apache 2.0](LICENSE). Bundled audio and the pack format are licensed
separately — see [`LICENSES.md`](LICENSES.md) for the full split, and
[`CONTRIBUTING.md`](CONTRIBUTING.md) (no CLA required).
