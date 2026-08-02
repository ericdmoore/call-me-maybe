# Runbook

## Config interfaces

Three files, three cadences, three failure domains:

| File | Holds | Changes | Editable by |
|---|---|---|---|
| `.env` | secrets (ARI, handset SIP passwords) + tuning | almost never | you |
| `handsets.toml` | the hardware: ids, endpoints, internal numbers, page/MWI membership | when you buy phones | you |
| `policy.toml` | the rules: allow-list, extensions, ladders, schedules | weekly | anyone in the house |

`handsets.toml` is the single source of truth for the phone plant:
`doorman render` generates the per-handset Asterisk config from it
(`pjsip_handsets.conf`, `extensions_handsets.conf`), so the SIP endpoint,
the internal number, the BLF hint, and the policy reference can never drift
apart. Never hand-edit the generated files — the header says so and means it.

`policy.toml` cross-references handsets by id and is validated against them
on every reload, but a typo in a bedtime can no longer invalidate the
handset inventory: the failure domains are separate. Legacy single-file
policy.toml (handsets inline) still loads; `doorman check` nudges.


Everything operational: provisioning, verification, troubleshooting, day-2
tasks. Commands assume the repo is at `/opt/call-me-maybe` on the Pi.

Notation: `$` runs as your user, `#` needs root, `*CLI>` is the Asterisk console.

---

## 1. VoIP.ms portal

Do this first — the values feed straight into `pjsip.conf`. All of it is click
work in the portal at <https://voip.ms>; there is no API step required.

| Step | Where | What |
|---|---|---|
| 1 | Main Menu → Sub Accounts → Create Sub Account | Auth type **User/Password**, device type **generic ATA/IP phone**, **NAT: yes**. Note the username (`123456_home`) and set a long password. |
| 2 | Same page → Codecs | **ulaw** first, **g722** second, disable everything else. Never enable g729 — it costs money and the Pi would transcode. |
| 3 | Main Menu → DIDs → Manage DID | Route the DID to the sub account from step 1. |
| 4 | Same page → E911 | Register the service address. A couple of dollars a month, and it is the one line item worth not skipping on a house phone. |
| 5 | Main Menu → Servers | Pick the POP nearest you. Use that exact hostname everywhere — mixing POPs between `server_uri` and `contact` causes registration flapping that looks like a network problem. |

**Do not** put your main VoIP.ms login on the Pi. If the Pi is compromised, a
sub account can be disabled without losing the DID or the balance.

Sanity check before touching the Pi: set a **spending limit** on the account.
A misconfigured dialplan that loops on outbound calls is a real way to spend
real money overnight.

---

## 2. Provision the Pi

```bash
# Base packages. Asterisk 20+ has the PJSIP features used here.
$ sudo apt update && sudo apt install -y asterisk rsync
$ asterisk -V                        # want 20.x or newer

# Service account with no login and no home.
$ sudo useradd --system --no-create-home --shell /usr/sbin/nologin doorman

# Layout. The repo on the Pi carries config + scripts; the daemon itself is
# ONE static binary, cross-compiled elsewhere. No Go toolchain, no runtime,
# nothing to apt-install for doorman.
$ sudo mkdir -p /opt/call-me-maybe
$ sudo chown "$USER" /opt/call-me-maybe
$ git clone https://github.com/YOU/call-me-maybe /opt/call-me-maybe

# On a WORKSTATION (not the Pi), build and ship the binary. Check the Pi arch
# first: uname -m says aarch64 -> arm64 build, armv7l -> armv7 build.
$ make cross
$ scp bin/doorman-linux-arm64 pi@raspberrypi:/opt/call-me-maybe/bin/doorman
$ ssh pi@raspberrypi /opt/call-me-maybe/bin/doorman version
```

### Asterisk config

```bash
$ cd /opt/call-me-maybe
$ sudo cp asterisk/extensions.conf asterisk/http.conf asterisk/rtp.conf /etc/asterisk/
$ sudo cp asterisk/pjsip.conf.example /etc/asterisk/pjsip.conf
$ sudo cp asterisk/ari.conf.example   /etc/asterisk/ari.conf

# Generate an ARI password and put the SAME value in both places.
$ openssl rand -base64 24
$ sudo nano /etc/asterisk/ari.conf     # password = <that value>
$ sudo nano /etc/asterisk/pjsip.conf   # sub account, POP, handset passwords

$ sudo chown asterisk:asterisk /etc/asterisk/*.conf
$ sudo chmod 640 /etc/asterisk/pjsip.conf /etc/asterisk/ari.conf
$ sudo systemctl restart asterisk
```

### doorman config

```bash
$ cd /opt/call-me-maybe
$ doorman init                         # interview: which rooms get a phone
                                       # generates every secret with crypto/rand,
                                       # writes .env, handsets.toml, policy.toml,
                                       # and prints the PINs once. Write them down.
$ nano policy.toml                     # add your people to the allow-list

# Non-interactive, for a scripted build:
$ doorman init --rooms "Kitchen,Living Room,Kids Room,Office"
$ ./bin/doorman check

# Generate the per-handset Asterisk config and install it:
$ ./bin/doorman render
$ sudo cp asterisk/generated/*_handsets.conf /etc/asterisk/
$ sudo chown asterisk:asterisk /etc/asterisk/*_handsets.conf
$ sudo chmod 640 /etc/asterisk/*_handsets.conf
$ sudo asterisk -rx 'pjsip reload' && sudo asterisk -rx 'dialplan reload'

# Never hand-pick PINs. Rotate every example PIN to a crypto/rand one in a
# single step (comments and formatting in the file survive):
$ ./bin/doorman rotate

$ sudo chown -R doorman:doorman /opt/call-me-maybe
$ sudo chmod 600 /opt/call-me-maybe/.env /opt/call-me-maybe/policy.toml
```

### Prompts

Built on a workstation (piper is not worth installing on a Pi), copied over.

```bash
# On the workstation:
$ bash prompts/build.sh
$ rsync -av prompts/build/ pi@raspberrypi:/tmp/cmm-prompts/

# On the Pi:
$ sudo mkdir -p /var/lib/asterisk/sounds/call-me-maybe
$ sudo cp /tmp/cmm-prompts/* /var/lib/asterisk/sounds/call-me-maybe/
$ sudo chown -R asterisk:asterisk /var/lib/asterisk/sounds/call-me-maybe
$ rm -rf /tmp/cmm-prompts
```

### Service

```bash
$ sudo cp scripts/doorman.service /etc/systemd/system/
$ sudo systemctl daemon-reload
$ sudo systemctl enable --now doorman
$ sudo systemctl status doorman
$ journalctl -u doorman -f
```

---

## 3. Verification ladder

Run `./scripts/smoke.sh` to do all of this at once. When something fails, walk
the rungs by hand from the bottom — each depends on the ones below it.

### Rung 1 — Asterisk is alive

```bash
$ sudo systemctl is-active asterisk           # active
$ sudo asterisk -rx "core show version"
```

### Rung 2 — the trunk registered

```bash
$ sudo asterisk -rx "pjsip show registrations"
```

Want `Registered`. Anything else and inbound calls cannot arrive at all.

| Status | Means |
|---|---|
| `Registered` | Good. |
| `Rejected` | Wrong sub-account username or password. |
| `Unregistered` | Cannot reach the POP — DNS or egress firewall. |
| `Stopped` | `retry_interval` exhausted; check logs and restart Asterisk. |

### Rung 3 — handsets registered

```bash
$ sudo asterisk -rx "pjsip show endpoints"
$ sudo asterisk -rx "pjsip show contacts"
```

Each handset should be `Avail`. `Unavail` means the phone has not registered —
check the phone's own config, not Asterisk.

### Rung 4 — ARI answers

```bash
$ curl -s -u doorman:YOUR_PASS http://127.0.0.1:8088/ari/asterisk/info | head -c 200
```

JSON with a version means good. `401` means `ari.conf` and `.env` disagree.
Connection refused means `http.conf` is not loaded — `sudo asterisk -rx "http show status"`.

### Rung 5 — doorman connected and Stasis registered

```bash
$ sudo systemctl is-active doorman
$ sudo asterisk -rx "ari show apps"           # want: doorman
$ journalctl -u doorman -n 20 --no-pager
```

`ari show apps` listing `doorman` is the single best proof the whole chain is
up. If Asterisk is running and doorman is running but the app is absent, the
WebSocket did not connect — check `ARI_APP` matches `Stasis()` in the dialplan.

### Rung 6 — prompts are present and in the right format

```bash
$ ls -la /var/lib/asterisk/sounds/call-me-maybe/
$ soxi /var/lib/asterisk/sounds/call-me-maybe/good-day.wav
```

Want 8000 Hz, 1 channel, 16-bit. A prompt at 22050 Hz will either fail to play
or burn CPU transcoding on every single call.

### Rung 7 — an actual call

Call the DID from a number **not** in `policy.toml` and confirm the lobby
greeting, then the dismissal. Then add your mobile to `policy.toml` (no restart
needed) and call again — the house should ring.

```bash
$ journalctl -u doorman -f -o cat
$ sudo asterisk -rx "core show channels"      # during the call
```

---

## 4. Troubleshooting

### Inbound calls never arrive

Caller hears ringing forever, or a provider error.

1. `pjsip show registrations` — if not `Registered`, stop here and fix that.
2. `sudo asterisk -rvvv` then place a call. No output at all means the packets
   are not arriving: check the DID routing in the portal.
3. Output showing endpoint `anonymous` means the inbound call did not match the
   trunk endpoint. **This is almost always a missing `line=yes` or
   `endpoint=voipms` on the registration object.** That pairing is what binds
   inbound traffic to the endpoint without an `identify` block.
4. `sudo asterisk -rx "dialplan show inbound-trunk"` — confirm the context
   exists and matches `context=` on the endpoint.

### Every caller gets "Good day", including ones who dial correctly

The lobby is not hearing DTMF. This is the classic failure and it looks exactly
like working software.

```bash
$ sudo asterisk -rx "pjsip show endpoint voipms" | grep -i dtmf   # want rfc4733
```

Then watch the frames during a call:

```bash
$ sudo asterisk -rvvv
*CLI> pjsip set logger on
```

Press digits from the calling phone. If no DTMF frames appear, the mode is
wrong on the trunk. If they appear but doorman logs nothing, the mismatch is
between `ChannelDtmfReceived` routing and the session map — check
the registry lookups in `route` (`cmd/doorman/main.go`).

### Known caller gets the lobby instead of the welcome

Caller ID normalisation. Find what actually arrived:

```bash
$ journalctl -u doorman | grep -i "unknown caller"
$ sudo asterisk -rx "core show channels verbose"
```

Then check it against the allow-list logic directly:

```bash
$ ./bin/doorman e164 "5125550100"
```

If the raw value comes back `unparseable`, add a case and a test in
`internal/policy/e164_test.go`.
Withheld caller ID is `anonymous` and always meets the bouncer by design.

### Caller hears silence instead of a greeting

```bash
$ sudo asterisk -rx "core show channels"
$ ls -la /var/lib/asterisk/sounds/call-me-maybe/
$ journalctl -u doorman | grep "playback failed"
```

Usually one of: prompts not installed, wrong ownership (`asterisk:asterisk`),
or wrong sample rate. `PROMPT_MEDIA_PREFIX` in `.env` must match the directory
name under `/var/lib/asterisk/sounds/`.

### One-way audio

Media path, not signalling. Check `rtp_symmetric=yes`, `force_rport=yes`, and
`rewrite_contact=yes` on both the trunk and the handsets. If the Pi is behind a
router doing SIP ALG, disable the ALG — it helps nothing and breaks this.

### doorman crash-looping

```bash
$ journalctl -u doorman -n 50 --no-pager
$ sudo systemctl status doorman
```

`Invalid environment configuration` is a missing `.env` key and the message
names it. `cannot reach ARI` is rungs 4–5. The unit has
`StartLimitBurst=5`, so it stops rather than looping forever — clear with
`sudo systemctl reset-failed doorman` after fixing.

### Rolling back

```bash
# The binary is the deploy artifact. Keep the previous one around by copying
# bin/doorman to bin/doorman.prev before each deploy; then rollback is:
$ sudo systemctl stop doorman
$ mv /opt/call-me-maybe/bin/doorman.prev /opt/call-me-maybe/bin/doorman
$ sudo systemctl start doorman
```

Asterisk config is not in the deploy path — roll it back separately by editing
`/etc/asterisk/` and running `sudo asterisk -rx "core reload"`.

---

## Handset features

Everything here lives in the `internal` dialplan context — doorman never sees
any of it, and every number below is also a valid **transfer target**.

| Dial | What happens |
|---|---|
| 100 | Ring every handset |
| 101–105 | Kitchen / living room / office / primary bed / kids' room |
| 500 | **Page**: every handset auto-answers on speakerphone |
| 600 | **Family conference** bridge |
| 700 | (as a transfer target) **park** the call; Asterisk announces a slot |
| 701–720 | Pick up a parked call from any handset |
| *97 | Check **voicemail** (prompts for mailbox + password) |
| 9196 | Echo test — your voice comes straight back; isolates RTP problems |
| 9197 | Speaking clock — proves audio path without a second person |
| 555 | **Home Assistant Assist** (optional, disabled by default) |

One-time phone-side setup:

- **Paging** needs "Allow Auto Answer by Call-Info/Alert-Info" enabled in each
  Grandstream's account settings, or 500 just rings them normally.
- **BLF lamps**: configure a phone's VPK/MPK keys as BLF watching 101–105;
  the key lights when that handset is busy and one-touch dials it.
- **Hold music**: `sudo apt install asterisk-moh-opsound-wav`, or point
  `musiconhold.conf` at your own directory of 8 kHz mono WAVs.
- **MWI lamps**: add `mailboxes=kids@household` (etc.) to an endpoint in
  `pjsip.conf` and that phone's message light follows the mailbox.

### Voicemail

`asterisk/voicemail.conf.example` → `/etc/asterisk/voicemail.conf`. The
mailboxes there (`kids`, `adults`, `family` in the `[household]` section) are
what `voicemail = "..."` in `policy.toml` refers to. **Change the placeholder
passwords** — they are dialable from any handset via *97.

`attach = yes` emails every message with the recording attached. For delivery,
install msmtp configured to relay through your provider (SES works fine) and
set `mailcmd = /usr/bin/msmtp -t`. Test end to end:

```bash
$ sudo asterisk -rx "voicemail show users"
# leave yourself a message, then:
$ ls /var/spool/asterisk/voicemail/household/kids/INBOX/
```

Doorman hands callers to voicemail by releasing the channel into the
`[voicemail-drop]` dialplan context with `MAILBOX` set — after that handoff
the call belongs to Asterisk, and doorman deliberately never touches it again.

### Ringer ladders and afterhours

Both are pure `policy.toml`; see the `Kids` extension in
`examples/policy.example.toml` for the shape. A ladder is `[[extensions.steps]]`
blocks — each rings its handsets/groups for `rings` cycles (~6s each, or
`seconds = N` exactly) before escalating; exhausting the ladder lands in the
extension's `voicemail`. Afterhours (`[extensions.afterhours]`) is a window
during which the line skips ringing entirely and goes straight to voicemail —
`start`/`end` in local time (crossing midnight is fine and belongs to the
start day), `days` lists the nights it *begins*. Flip `enabled = false` for
holidays; hot reload applies it within a second.

`./bin/doorman check` prints each extension's ladder, mailbox, and — the
important one — whether afterhours is **ACTIVE NOW**, which is the first
thing to look at when "the kids' line goes straight to voicemail" is either
the bug or the feature.

### Home Assistant Assist (optional)

HA's native VoIP integration answers SIP calls and runs them through an
Assist pipeline — with your Wyoming whisper/piper stack doing STT/TTS. The
plumbing is a static PJSIP endpoint at HA's IP plus dialplan 555, both
shipped commented-out (`asterisk/pjsip.conf.example`, the 555 line in
`extensions.conf`).

To enable: uncomment both, set HA's IP in the `[homeassistant]` AOR, add the
VoIP integration in HA and toggle **Allow calls** for the device that appears
(it identifies callers by source IP — the Pi). The catch is codecs: HA's
Assist audio wants **Opus**, which stock Debian Asterisk doesn't ship — you
need the Opus codec module so Asterisk can transcode the handsets' ulaw.
Treat the whole thing as a fun add-on rather than infrastructure: community
reports include hangs, and nothing in the phone system depends on it.

---

## 5. ARI by hand

Useful for probing without doorman running. Stop doorman first or you will race
it. `AUTH="doorman:YOUR_PASS"`, `ARI="http://127.0.0.1:8088/ari"`.

```bash
# What Asterisk thinks is happening
curl -s -u "$AUTH" "$ARI/asterisk/info"
curl -s -u "$AUTH" "$ARI/channels"
curl -s -u "$AUTH" "$ARI/bridges"
curl -s -u "$AUTH" "$ARI/endpoints"

# Watch the raw event stream (this is exactly what doorman consumes)
websocat "ws://127.0.0.1:8088/ari/events?app=doorman&subscribeAll=false&api_key=$AUTH"

# Ring a handset from nothing — proves originate works
curl -s -u "$AUTH" -X POST \
  "$ARI/channels?endpoint=PJSIP/kitchen&extension=100&context=internal&callerId=Test"

# Play a prompt to a live channel — proves the media is installed correctly
curl -s -u "$AUTH" -X POST \
  "$ARI/channels/CHANNEL_ID/play?media=sound:call-me-maybe/good-day"

# Hang up
curl -s -u "$AUTH" -X DELETE "$ARI/channels/CHANNEL_ID"
```

`websocat` is worth installing on the Pi (`cargo install websocat` or a release
binary); watching the raw event stream during a call is the fastest way to
understand why the state machine did what it did.

---

## 6. Day-2 tasks

### Add a person to the allow-list

No restart. `POLICY_WATCH=true` picks it up within a second.

```bash
$ sudo -u doorman nano /opt/call-me-maybe/policy.toml
$ ./bin/doorman check
$ journalctl -u doorman -n 5     # want: "policy reloaded"
```

A file that fails validation is rejected and the previous version stays live,
so a typo cannot take the phone down — but check the log to confirm the reload
actually happened rather than silently failing.

### Add a handset

One file, one command — the render step replaces the old three-places-must-
agree procedure:

1. Add a `[[handsets]]` block to `handsets.toml` (id, endpoint `PJSIP/<id>`,
   number, page/mailbox as wanted) and its `HANDSET_*_PASSWORD` to `.env`.
2. `./bin/doorman render`, copy the two generated files to `/etc/asterisk/`,
   `pjsip reload` + `dialplan reload` (render prints the exact commands).
3. Point the phone at the Pi with that username/password; confirm `Avail`
   via `pjsip show contacts`.
4. Reference the id from `policy.toml` ([house], a group, or an extension) —
   live within a second.
5. `./bin/doorman check`

### Rotate a PIN

SSH in, one command, no restart:

```bash
$ ssh pi@raspberrypi
$ cd /opt/call-me-maybe
$ sudo -u doorman ./bin/doorman rotate Kitchen     # one extension by label
$ sudo -u doorman ./bin/doorman rotate             # or everything at once
```

The new PINs print to your terminal — the only place they ever appear; they
are never written to logs. The file is validated before it is written and
written atomically, so a failed rotation leaves the old policy untouched. With
`POLICY_WATCH=true` (the default) the running daemon reloads within a second —
confirm with "policy reloaded" in `journalctl -u doorman -n 5`. If you run
`POLICY_WATCH=false`, follow with `sudo systemctl restart doorman`.

The old PINs are dead the moment the reload lands, so hand the new ones out
immediately. Rotate any PIN that has been spoken aloud to someone you would
not add to the allow-list, and rotate everything if the bouncer logs show
sustained probing.

### Rotate the ARI password

```bash
$ openssl rand -base64 24
$ sudo nano /etc/asterisk/ari.conf
$ sudo nano /opt/call-me-maybe/.env
$ sudo asterisk -rx "module reload res_ari.so"
$ sudo systemctl restart doorman
```

### Check on the bouncer

```bash
$ journalctl -u doorman --since "24 hours ago" | grep -c "dismissing caller"
$ journalctl -u doorman --since "24 hours ago" | grep "invalid extension"
$ journalctl -u doorman --since "7 days ago"  | grep "rate limited"
```

Sustained invalid-extension attempts from one number mean someone is probing.
The rate limiter handles it, but it is worth knowing about — and worth
considering whether the PINs in circulation should be rotated.

## 6a. The call log

Off by default. Set `CALL_LOG_PATH` and restart:

```bash
# .env
CALL_LOG_PATH=/var/lib/doorman/calls.jsonl
```

doorman creates it `0600` and appends one JSON line per completed call. If it
cannot open the path it exits at startup rather than running without it — an
operator who asked for a call log and did not get one should find out
immediately, not a month later when they go looking for a call.

```bash
doorman calls                          # last 20, caller IDs redacted
doorman calls --since 7d               # a duration (24h, 7d) or a date
doorman calls --outcome dismissed      # answered|voicemail|dismissed|abandoned
doorman calls --caller 0100            # match part of a number
doorman calls --json | jq .            # JSON Lines for anything else
doorman calls --no-redact              # full numbers
```

**Why this is worth having beyond curiosity.** The `stages` array is the ring
ladder as actually walked, with per-rung timings. That is what tells you a
stage expires before anybody can cross a room, or that the lobby's ten seconds
is too short because most strangers are dismissed with `no-digits` rather than
a wrong extension. Those are policy questions, and they are unanswerable from
the operational log.

### What it holds, and what it never holds

Full caller IDs — that is the point of a call log on a telephone, and it is why
the file is `0600` and `doorman calls` redacts unless you ask. Keep it on the
box; the operational log (which redacts by default) is the one that gets
shipped to a journal.

Entered digits and PINs are **never** recorded, at any level. A record says a
PIN was `valid` or `invalid` and how many attempts there were, never what was
typed — a near-miss is almost a credential. There is no field one could go in.

### Holes are reported, not hidden

Writes happen off the call path through a buffered channel, so a slow disk can
never delay a call. If that buffer fills, records are dropped and counted. A
line torn by power loss is skipped on read and the count printed to stderr, so
"skipped" never silently means "fine".

The file rotates at `CALL_LOG_MAX_BYTES` (32 MB default) keeping one previous
generation, and `doorman calls` reads across the rotation. Twenty calls a day
is roughly 2 MB a year, so the cap is for the case that is not twenty calls a
day — somebody redialling in a loop.

### It is an output, never an input

Nothing on the call path reads this file. If you want "this number has called
five times, admit it", that is a policy change, not a log query — see
CLAUDE.md invariant 10 for why, and note the import graph prevents it
structurally.

To turn it off, unset `CALL_LOG_PATH` and restart. The file stays where it is;
delete it yourself if you mean to.

---

## 7. Threat model

What this system actually defends against, and what it does not. Written down
because a defence you believe in and do not have is worse than one you know you
are missing.

### Caller ID is presentation, not a credential

A known caller skips the lobby and rings the house on the strength of their
normalised caller ID alone. **On the PSTN, caller ID is asserted by the calling
party, not proven.** Anyone who can spoof an allow-listed number gets the same
welcome the real person gets — and, because a welcomed caller clears their
failure budget, a spoofer also resets the rate limiter for that number.

This is a trust boundary, not a bug. It is the same reason the phone network has
spent a decade building STIR/SHAKEN. Live with it knowingly:

- **The allow-list is a convenience, not an authorisation.** It decides who
  skips the lobby. It should never be the only thing standing between a caller
  and something consequential.
- Anything that *acts* on a caller's behalf — remote control of the house, for
  instance — needs a second factor. That is why the design in the remote-actions
  work uses dial-back rather than trusting the number in front of it.
- If your provider exposes STIR/SHAKEN attestation, treat a full-attestation
  call as meaningfully better evidence than an unattested one. Do not treat an
  unattested call as proof of anything.

### The LAN is a softer target than the PSTN

Guessing a six-digit extension over the phone is slow and rate-limited. Being on
the house Wi-Fi is neither.

Handsets register over SIP, in the shipped examples without TLS or SRTP, and a
registered handset lands in the `internal` dialplan context. That context can
dial the outbound trunk, page every phone in the house, join the conference, and
reach voicemail. **A compromised handset password is therefore more powerful
than any amount of PIN guessing from outside.**

Mitigations, in the order they are worth doing:

1. **Unique, generated passwords per handset.** `handsets.toml` names an
   environment variable per phone precisely so they can differ. Generate them:
   `openssl rand -base64 18`.
2. **Change the voicemail PINs.** `asterisk/voicemail.conf.example` ships `4242`
   for every mailbox as an obvious placeholder. It is still `4242` until you
   change it, and voicemail is reachable from any handset with `*97`.
3. **Keep SIP off the WAN.** Nothing in this design needs an inbound port. If
   your router forwards 5060 anywhere, undo it.
4. **Put handsets on a network guests do not share.** A guest Wi-Fi password
   handed out at a party is a SIP registration attempt away from the dialplan.
5. **Consider SIP TLS and SRTP** between handsets and Asterisk if your phones
   support it. It costs some configuration and defeats passive sniffing of
   registrations on the LAN.

### The fallback trades security for availability

`[inbound-fallback]` in `asterisk/extensions.conf` rings the house directly when
Stasis is unavailable — doorman crashed, stopped, or unable to reach ARI. That
is deliberate: a phone that fails to a dumb ATA still answers when a relative
calls in an emergency.

**Be clear about what it costs.** For the duration of that outage there is no
lobby, no allow-list, and no rate limiting. Every caller rings the house. An
attacker who can keep doorman down has removed the front door rather than picked
it.

Choose knowingly:

- **Keep it** if availability matters more, which it usually does for a house
  phone. Then treat "doorman is down" as an urgent alert rather than a
  background annoyance.
- **Replace it** if you would rather fail closed. Point the fallback at
  `Congestion()`, or at voicemail, instead of the ring group:

  ```
  [inbound-fallback]
  exten => _X.,1,NoOp(doorman unavailable — failing closed)
   same => n,Answer()
   same => n,Playback(vm-goodbye)
   same => n,Hangup()
  ```

Either way, know which one you have. The failure mode is silent by design.

### What is enforced mechanically

Not everything here is a matter of discipline. These are checked by machine:

| Invariant | Enforced by |
|---|---|
| Caller IDs and PINs never reach logs | `tools/nologsecrets`, in `make lint` and CI |
| Caller IDs redacted unless explicitly opted out | default in `internal/config`, tested |
| ARI host must be loopback | startup refusal, with `ARI_ALLOW_REMOTE` as the documented exception |
| Extension PINs meet a minimum length | `policy.MinPINLength`, on load and in `doorman check` |
| Concurrent calls are capped | admission control in the event router |
| Config cross-references resolve | `doorman check`, the LSP, and the daemon share one validator |
