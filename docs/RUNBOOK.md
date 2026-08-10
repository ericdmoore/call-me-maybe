# Runbook

## Config interfaces

Three files, three cadences, three failure domains — plus two optional
inventories:

| File | Holds | Changes | Editable by |
|---|---|---|---|
| `.env` | secrets (ARI, handset and trunk SIP passwords) + tuning | almost never | you |
| `handsets.toml` | the hardware: ids, endpoints, internal numbers, page/MWI membership | when you buy phones | you |
| `policy.toml` | the rules: allow-list, extensions, ladders, schedules | weekly | anyone in the house |
| `trunks.toml` | **optional** — the providers: hosts, sub-accounts, which trunk carries 911 | when you buy a number somewhere new | you |
| `contacts.toml` | **optional** — the address books: which vCard exports to read | when somebody new joins the household | you |

`handsets.toml` is the single source of truth for the phone plant:
`doorman render` generates the per-handset Asterisk config from it
(`pjsip_handsets.conf`, `extensions_handsets.conf`), so the SIP endpoint,
the internal number, the BLF hint, and the policy reference can never drift
apart. Never hand-edit the generated files — the header says so and means it.

`trunks.toml` does the same for the provider side, and **not having it is the
normal state.** One provider fits comfortably in a hand-written
`asterisk/pjsip.conf` — that is what `pjsip.conf.example` is, and it keeps
working untouched. Create the file when a second provider turns copying PJSIP
blocks into a chore; then `doorman render` also writes `pjsip_trunks.conf` and
`extensions_trunks.conf`. See "Add a second provider" in §6. Deleting the file
is the whole rollback.

`contacts.toml` is the address-book inventory, and **not having it is likewise
the normal state**: with no file nothing is read and `doorman check` prints
nothing about contacts. Each `[[sources]]` block names a vCard export by
`path` — a relative one resolves against `contacts.toml` itself — and
`kind = "block"` marks one as the nuisance list. `doorman check` reports what
each source contributed: cards read, and how many numbers came out personal,
published, blocked or skipped, with counts only and never a name or a number.
Today that report is all it does; nothing on a call path consults a contact
set, and `[[people]]` in `policy.toml` remains the only list that admits
anybody. **The exports are the most personal data on this box** — several
people's entire address books — so keep them mode 0600, outside any
repository, and never in a payload that leaves. Deleting the file is the whole
rollback.

`policy.toml` cross-references handsets by id and is validated against them
on every reload, but a typo in a bedtime can no longer invalidate the
handset inventory: the failure domains are separate. Legacy single-file
policy.toml (handsets inline) still loads; `doorman check` nudges.

If the box answers more than one phone number, each extra one gets a
`policy.<line>.toml` beside `policy.toml`, and the dialplan says which line a
call arrived on. Handsets stay shared; rules, PINs and rate-limit budgets are
per line, with a separate failure domain each. A `[line]` section gives each
number a name, its own prompt pack, and its own answer to "what happens to a
caller who dials nothing" — which is what makes one number a curt doorman and
another a courteous concierge. Plain `policy.toml` is the default line and is
what a call with no line named gets — which is every call on an install that
has one number. See "Add a second number" in §6.


Everything operational: provisioning, verification, troubleshooting, day-2
tasks. Commands assume the repo is at `/opt/call-me-maybe` on the Pi.

**First time, with a Pi still in the box?** Start at
[FIRST-BOOT.md](FIRST-BOOT.md) — the day-one path in order, with the hardware
checks that prevent the two commonest week-one failures. Come back here for
depth.

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

Optional, and worth doing on a prepaid account: **Main Menu → Account Settings
→ API** turns on API access and sets a separate API password, and the same page
holds the allow-list of IPs permitted to use it. That is what `doorman balance`
needs — see "Watch a prepaid trunk's balance" in §6. Add the address of the
machine that will run the check, which should not be the Pi: the API login is
the main account login this section just told you to keep off it.

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

### Rung 5b — 911 has a route

```bash
$ sudo asterisk -rx "dialplan show cmm-emergency"   # several providers
$ sudo asterisk -rx "dialplan show 911@internal"    # one provider
```

Exactly one of these shows a `Dial(PJSIP/911@…)`. Which one is correct depends
on whether you have a `trunks.toml`: the generated `[cmm-emergency]` ladder
appears once `extensions_trunks.conf` is `#include`d, and until then `_911` in
`[internal]` dials `DEFAULT_TRUNK`. **Neither showing one is a failure** —
911 would go nowhere.

Then confirm the endpoints it names actually exist, including the fallbacks;
the fallback is the half nobody exercises, so it is the half that rots:

```bash
$ sudo asterisk -rx "pjsip show endpoint voipms"    # per trunk in the ladder
```

`_911` reaches the generated ladder through `DIALPLAN_EXISTS`, so
`func_dialplan.so` has to be loaded — it is by default. Without it the `GotoIf`
is false and 911 falls through to `DEFAULT_TRUNK`, which still connects on a
box whose primary trunk is the emergency one but is not what the ladder says.
`sudo asterisk -rx "core show function DIALPLAN_EXISTS"` confirms it.

`./scripts/smoke.sh` does all of this. See "Which trunk carries 911" in §6 for
what to do when the answer is wrong, and read the supplementary-phone framing
there before you rely on any of it.

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
any of it (the one exception is noted below), and every number below is also a
valid **transfer target**.

| Dial | What happens |
|---|---|
| 100 | Ring every handset |
| 101–105 | Kitchen / living room / office / primary bed / kids' room |
| 500 | **Page**: every handset auto-answers on speakerphone |
| 600 | **Family conference** bridge |
| 700 | (as a transfer target) **park** the call; Asterisk announces a slot |
| 701–720 | Pick up a parked call from any handset |
| *4 | **Outbound console**: call as another one of your numbers. Only interesting with more than one line, and it refuses 911 — see "Outbound caller ID" below. This is the one that goes through doorman |
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

It prints the settings you *didn't* write too, as `(none — ...)`. That is
deliberate and it is how you catch a misspelled key: `voicmail = "kids"` is a
legal TOML key that no field claims, so before it was reported the mailbox
simply never existed and the file looked fine. Check now rejects a key it
does not recognise and suggests the nearest real one; if you would rather see
it than be stopped by it, that is what the daemon's log does — it warns and
keeps the phone up (see *An invalid policy must never take the phone down*).

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

Also worth a look: `unrecognised key in config, ignoring it`. The daemon warns
rather than refusing, so an edit that misspelled a key *does* reload — it just
does not do what you meant. `doorman check` turns the same warning into an
error and tells you which key it should have been.

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

### Add a second number

One box, several phone numbers, each with its own rules — a curt doorman on
the home line, a courteous concierge on the business one. One VoIP.ms
registration already carries every DID you buy, so this is config on both
sides and no new trunk.

**Nothing changes for an install that does not do this.** A call arriving with
no line named gets `policy.toml`, exactly as it always has.

1. **Buy the DID** in the VoIP.ms portal and point it at the same sub-account
   as the first. Nothing new to register.

2. **Find out what digits arrive.** Providers differ — 10 digits, 11, or full
   E.164 — and the dialplan match has to be exact:

   ```bash
   $ sudo asterisk -rvvv          # then ring the new number
                                  # want: "Inbound from <caller> to 5125550142"
   ```

3. **Write the line's policy**, beside the existing one. The name in the
   filename is the name the dialplan will use:

   ```bash
   $ cd /opt/call-me-maybe
   $ sudo -u doorman cp policy.toml policy.biz.toml
   $ sudo -u doorman nano policy.biz.toml      # its own allow-list, its own
                                               # extensions, its own ladders
   $ sudo -u doorman ./bin/doorman rotate -policy policy.biz.toml
   ```

   Handsets are **shared** — one `handsets.toml`, one phone plant, and both
   lines may ring the office. Everything else is per line: allow-list,
   extensions, PINs, schedules, and the rate-limit budget.

   Give the line its identity and its disposition at the top of the file.
   This is the part that makes the second number worth having — the home line
   dismisses a stranger who dials nothing, the business one takes a message:

   ```toml
   [line]
   label       = "Mertaugh Enterprises"   # shown by `doorman check`, and on the
                                          # handset when a caller reaches the
                                          # house without dialling anything
   number      = "+15125550142"           # identity; nothing routes on it
   prompts     = "concierge"              # optional: this line's own pack
   on_no_input = "voicemail"              # dismiss | ring-house | voicemail
   ```

   `on_no_input = "voicemail"` needs a `[house] voicemail` to land in, and
   `doorman check` refuses the combination without one. `prompts` needs the
   pack installed at `/var/lib/asterisk/sounds/concierge/`; omit it and the
   line speaks with the house voice. `ring-house` is the third option and
   means anybody patient enough to say nothing reaches the house — a real
   choice, and one to make deliberately.

4. **Route it in the dialplan.** In `/etc/asterisk/extensions.conf`, send the
   new DID to its own context from `[inbound-trunk]` — an exact match beats
   the `_X.` pattern — and name the line as the Stasis argument:

   ```
   [inbound-trunk]
   exten => 5125550142,1,Goto(from-trunk-biz,${EXTEN},1)
   ; ... the existing _X. lines stay exactly as they are ...

   [from-trunk-biz]
   exten => _X.,1,NoOp(Inbound from ${CALLERID(num)} to ${EXTEN} on line biz)
    same => n,Set(CHANNEL(hangup_handler_push)=cmm-hangup,s,1)
    same => n,Answer()
    same => n,Stasis(${DOORMAN_APP},line,biz)
    same => n,Hangup()
   ```

5. **Confirm and reload:**

   ```bash
   $ ./bin/doorman check                       # lists every line it found
   $ sudo asterisk -rx 'dialplan reload'
   $ sudo systemctl restart doorman            # new files need a restart;
                                               # edits to them do not
   $ journalctl -u doorman -n 20 | grep line
   ```

   `doorman check` prints a `Lines:` block naming every line, its file, its
   `[line]` label and number, and whether it loads — then, per line, what a
   caller who dials nothing gets and which prompt pack it speaks from. A new
   policy file is discovered at startup, so adding a line is the one policy
   change that needs a restart; editing one that already exists is picked up
   live like any other, `[line]` included.

   The startup log carries the same two facts per line, because both fail
   quietly: a line meant to take messages that silently dismisses looks
   exactly like a line nobody has called yet, and a missing prompt pack
   degrades to silence rather than an error.

   ```bash
   $ journalctl -u doorman | grep "policy loaded"
   # policy loaded line=biz onNoInput=voicemail prompts=concierge ...
   ```

6. **Ring both numbers.** The home line should behave exactly as it did
   before you started.

**When it goes wrong.** The name in the dialplan and the name in the filename
have to match, and doorman cannot read the dialplan — so it cannot tell you at
startup that they do not. Instead the call is answered by the default line and
the log says so:

```bash
$ journalctl -u doorman | grep "no policy file"
# dialplan named a line that has no policy file, using the default line
#   line=bizz known="biz, default"
```

That is deliberate: a typo in a config file must never turn a number somebody
is paying for into a number that does not answer.

A line whose policy will not load behaves the same way — that line falls back
to the default and everything else keeps working:

```bash
$ journalctl -u doorman | grep "will not load"
```

This is invariant 4 generalised. Each line gets its own policy store, which is
the whole reason lines are separate files rather than sections of one: a stray
bracket in the business policy cannot stop the house phone ringing.

**Rolling back** is deleting the `Goto` line from `[inbound-trunk]`, or
deleting `policy.biz.toml`. There is no migration and no stored state.

That covers calls coming *in*. Calls going out still present the trunk's
default until you set an outbound caller ID — next section.

### Add a second provider

A different thing from a second *number*. A second number on the same provider
is config on both sides and no new trunk (above). A second provider is a second
registration, which changes where inbound calls arrive and eventually how they
leave.

Why bother: **redundancy** is the real prize — one provider having a bad
afternoon stops being your bad afternoon. Then rate shopping, porting a number
without downtime, and coverage, since one company may do E911 in your area and
another may have the number you want.

**Nothing changes for an install that does not do this.** With no `trunks.toml`
the hand-written `pjsip.conf` keeps working, `doorman render` generates only
the handset files, and nothing in `doorman check` mentions trunks.

1. **Declare the trunks you have — including the one you already run.**

   ```bash
   $ cd /opt/call-me-maybe
   $ sudo -u doorman cp examples/trunks.example.toml trunks.toml
   $ sudo -u doorman nano trunks.toml
   ```

   ```toml
   emergency_trunk = "voipms"          # which trunk carries 911; see below

   [[trunks]]
   id       = "voipms"                 # keep the id your dialplan already
                                       # dials — PJSIP/1${EXTEN}@voipms — and
                                       # no dialplan edit is needed
   provider = "voip.ms"
   host     = "chicago.voip.ms"
   username = "123456_home"            # the SUB ACCOUNT, never the main login
   password_env = "TRUNK_VOIPMS_PASSWORD"
   e911     = true

   [[trunks]]
   id       = "telnyx"
   provider = "telnyx"
   host     = "sip.telnyx.com"
   username = "cmm-home"
   password_env = "TRUNK_TELNYX_PASSWORD"
   e911     = false
   ```

   **The passwords are not in this file and must never be.** `password_env`
   names a `.env` variable and `doorman render` substitutes it; the loader
   refuses a `password_env` that is not shaped like a variable name, so a
   password pasted there fails loudly rather than reaching a commit.

   ```bash
   $ sudo -u doorman nano .env      # TRUNK_VOIPMS_PASSWORD=…
                                    # TRUNK_TELNYX_PASSWORD=…
   ```

   Before trusting a provider, check the list in issue #5: registration or
   credential auth (the load-bearing one), **E911 on the DID**, per-DID routing
   to a sub-account, ulaw and g722 without forced transcoding, NAT tolerance,
   and whether it tolerates a 300s registration expiry.

2. **Point each line at its provider.** In the line's policy file:

   ```toml
   [line]
   label  = "Mertaugh Enterprises"
   number = "+15125550142"          # the DID — needed for a route to exist
   trunk  = "telnyx"                # an id from trunks.toml
   ```

   Both keys or no route. A line with a `trunk` and no `number` gets a context
   but nothing routed into it, and `doorman check` names it.

3. **Render.**

   ```bash
   $ ./bin/doorman check      # lists the trunks, which lines land on each,
                              # and which trunk would carry 911
   $ ./bin/doorman render
   ```

   Four files now instead of two. `pjsip_trunks.conf` holds real registration
   passwords; `extensions_trunks.conf` holds one inbound context per trunk with
   each DID routed to its line, matched in **every digit format a provider might
   send** — 10-digit, 11-digit and full E.164 — so there is nothing to discover
   about which one arrives.

4. **Switch Asterisk over to the generated files.** This is the only fiddly
   step, and it is a one-time one.

   ```bash
   $ sudo cp asterisk/generated/*_trunks.conf /etc/asterisk/
   $ sudo chown asterisk:asterisk /etc/asterisk/*_trunks.conf
   $ sudo chmod 640 /etc/asterisk/*_trunks.conf
   ```

   **First, make sure `/etc/asterisk/extensions.conf` has the `[cmm-outbound]`
   context.** Boxes provisioned before outbound routing by trunk have the dial
   patterns written out twice instead, and those copies ignore
   `OUTBOUND_TRUNK` — so every call would still leave by the old trunk while
   presenting each line's caller ID, which is exactly the mismatch a provider
   rejects or silently rewrites. Check with
   `grep -c 'include => cmm-outbound' /etc/asterisk/extensions.conf`, which
   should print 2 — one for `[internal]`, one for `[outbound-console]`. If it
   prints 0, take the shipped file and re-apply your local edits:

   ```bash
   $ diff /etc/asterisk/extensions.conf asterisk/extensions.conf
   $ sudo cp asterisk/extensions.conf /etc/asterisk/    # then redo your edits
   ```

   In `/etc/asterisk/pjsip.conf`: **delete** the hand-written `[voipms_auth]`,
   `[voipms_reg]`, `[voipms_aor]` and `[voipms]` blocks and uncomment
   `#include "pjsip_trunks.conf"`. Leaving both in place registers twice.

   In `/etc/asterisk/extensions.conf`: **delete** `[inbound-trunk]` and any
   `[from-trunk-<line>]` contexts you added for a second number, and uncomment
   `#include "extensions_trunks.conf"`. The generated contexts replace them and
   the default line's `Stasis()` call is byte-identical to the one you are
   removing.

   Then set `DEFAULT_TRUNK` in `[globals]` to the id `doorman render` printed —
   the trunk that carries 911. It is what a channel with no trunk of its own
   leaves by, and leaving it pointing at an endpoint you have just deleted is
   the one way to end up with a dialplan that cannot dial.

   Both includes ship commented out on purpose: Asterisk refuses to start on a
   missing `#include`, so an unconditional one would break every install that
   does not use `trunks.toml`.

   **Nothing else in the dialplan changes.** The outbound patterns already read
   `OUTBOUND_TRUNK`, and `doorman render` writes it onto each handset from its
   line's `trunk`. `_911` already reaches the generated `[cmm-emergency]` when
   there is one and dials `DEFAULT_TRUNK` when there is not, so `#include`ing
   the file *is* the switch — there is no second edit to forget.

   ```bash
   $ sudo asterisk -rx 'pjsip reload' && sudo asterisk -rx 'dialplan reload'
   $ sudo asterisk -rx 'pjsip show registrations'   # want Registered on both
   $ sudo asterisk -rx 'dialplan show cmm-emergency' # want the ladder, not "no such context"
   ```

5. **Ring every number, and place a call from each line.** The one on the old
   provider should behave exactly as it did before you started.

   Outbound needs checking at **both** ends and either alone can look right
   while the other is wrong:

   - the receiving phone shows the number you expect, and
   - the provider's own CDR shows the call on the account you expect.

   A call leaving by the wrong trunk usually still connects — the provider
   rewrites the caller ID to a number that account owns — so the only symptom
   is a customer saving a number you did not choose.

**When inbound goes silent, this is why.** The generated registration carries
`line=yes` and `endpoint=<id>`, and that pair is what binds inbound traffic on
a registration to its endpoint. Without both, calls hit the `anonymous`
endpoint and vanish — no error, no log line, just a number that never rings.
Never hand-edit the generated file, and if you write a trunk by hand instead,
copy those two lines first.

```bash
$ sudo asterisk -rx 'pjsip show registrations'
$ sudo asterisk -rvvv                      # then ring the number: you want a
                                           # NoOp naming the line, not silence
```

### Which trunk carries 911

**Read this section before you add a second provider, not after.** It is where
911 stops being "whatever the dialplan hard-codes" and becomes a decision.

**How to check.** Two commands, and they answer different questions:

```bash
$ ./bin/doorman check                          # what the CONFIG says
$ sudo asterisk -rx 'dialplan show cmm-emergency'  # what ASTERISK LOADED
```

`doorman check` prints the trunk, whether it was **chosen** (`emergency_trunk`
in `trunks.toml`) or **inferred** (plain `policy.toml`'s `[line] trunk` — the
primary line, already the default for everything unqualified), whether that
trunk declares a registered street address, and the order it falls over in. So
does the startup log, every boot. Neither key has to be set and unset is never
undefined — but it is deliberately *not* "the only trunk you declared": a
default derived from file order would move silently the day a second block was
added above it.

`dialplan show cmm-emergency` is the other half, because doorman cannot read
the dialplan. **"No such context" means the generated file is not `#include`d
and 911 is leaving by `DEFAULT_TRUNK`**, whatever `doorman check` says. On a
single-provider box that is correct and expected. On a multi-provider one it
means step 4 above is unfinished.

**If the answer is "none".** `doorman check` exits non-zero and `doorman
render` refuses to generate anything — deliberately, because a dialplan that
routes every DID beautifully and has no answer for 911 is the one that gets
installed with confidence. Fix it by setting **either**:

```toml
# trunks.toml — the explicit answer
emergency_trunk = "voipms"
```

```toml
# policy.toml — or let the primary line answer it, like everything else
[line]
trunk = "voipms"
```

Set `emergency_trunk` when the trunk that carries 911 is *not* the one the
house line lives at — a household whose primary number moved to a provider with
no E911 in their area, for instance. Otherwise leave it unset and let the one
rule serve both jobs.

**When the designated trunk is down.** The call falls over to the next trunk,
then the next, and takes the first that connects. This is a deliberate trade
and worth stating plainly: the dispatcher may then see the wrong address, which
is genuinely bad — but a connected call lets a human say their address out
loud, and a failed call gives them nothing. Connection first. The fallback
order puts trunks that declare `e911 = true` ahead of ones that declare
nothing, and a trunk that declares `e911 = false` last of all.

Nothing asks a trunk whether it is registered; it tries it. A provider that
does not answer SIP `OPTIONS` looks unreachable while working perfectly, and
diverting an emergency call off the one trunk whose address is on file — on a
false negative, at the worst possible moment — is exactly what this design
exists to prevent. An unregistered trunk fails the `Dial` in milliseconds.

If **nothing** can carry it, the caller hears congestion rather than silence.
That tone is the signal to reach for a mobile.

**911 never presents a line's `outbound_cid`,** on any path. E911 is registered
per DID against a street address, so an emergency call leaves as the trunk's own
number — never as a business line whose registered address is somebody else's
house. `_911` lives in `[internal]` and not in the shared `[cmm-outbound]`
context precisely so that the `*4` console cannot reach it, and the console
refuses emergency numbers itself. Two independent things have to be wrong.

Two more things to be plain about:

- **Not every provider offers E911 in every area.** A provider without it means
  a phone that cannot call for help. That is a reason to reject the provider,
  not a line item to skip. Declare what you know with `e911 = true|false`;
  `doorman check` reports "unknown" rather than guessing when you have not.
- **This is a supplementary phone.** It stops working in a power cut and on a
  bad internet day. Nobody should make it the household's only route to
  emergency services. Keep a mobile in the house and teach everyone that it is
  the one to reach for. That is not a disclaimer, it is the accurate
  description of a hobbyist phone on a consumer internet connection.

**Rolling back** is deleting `trunks.toml`, re-rendering, and putting the
hand-written blocks back. `_911` returns to dialling `DEFAULT_TRUNK` the moment
the generated context stops being included. There is no migration and no stored
state.

### Outbound caller ID, and the `*4` console

Without this, every outbound call presents whatever the trunk sends. Invisible
with one number; with several it means every customer you ring back saves the
wrong one.

Two mechanisms, and most of the household only ever meets the first.

**1. A default per phone.** Set what each line presents, and say which phones
call as it:

```toml
# policy.toml — the primary line
[line]
outbound_cid = "+15125550100"

# policy.biz.toml
[line]
outbound_cid      = "+15125550142"
outbound_handsets = ["office"]
```

Then re-render and reload, because this half is baked into the generated PJSIP
config rather than read at call time:

```bash
$ ./bin/doorman check            # prints what each line presents, and where
                                 # an unclaimed phone ends up
$ ./bin/doorman render
$ sudo cp asterisk/generated/pjsip_handsets.conf /etc/asterisk/
$ sudo chown asterisk:asterisk /etc/asterisk/pjsip_handsets.conf
$ sudo asterisk -rx 'pjsip reload'
```

The office phone now rings customers back as the business number. **Every
phone no line claims presents `policy.toml`'s `outbound_cid`** — the primary
line — so adding a business line cannot change what the kitchen phone shows.

**A caller ID and the trunk that carries it are one decision.** With a
`trunks.toml`, `doorman render` writes `set_var=OUTBOUND_TRUNK=` beside
`OUTBOUND_CID` on each endpoint, from the same line's `trunk`, and `*4` sets
both together per call. That is not a convenience: a provider will not present
a number its account does not own — it rejects the call or silently rewrites
the caller ID as anti-spoofing — so the pair must never be assembled from two
places.

`doorman check` refuses a line whose `outbound_cid` is a number this config
declares at a *different* provider, and `doorman render` refuses to generate
one. What it cannot check is a number no `[line] number` declares: that is
often a DID you own and do not answer here, so it is reported and left alone.
Nothing can ask a provider which numbers an account owns.

With no `trunks.toml`, nothing writes `OUTBOUND_TRUNK` at all and every
outbound call leaves by `DEFAULT_TRUNK` in `[globals]`, exactly as it always
has.

`outbound_handsets` is written in the *line's* file and not in
`handsets.toml`, because the inventory is shared by every line and cannot say
something that is true of only one. A handset claimed by two lines is an error
in `doorman check` and in `doorman render`; the running daemon warns instead
and gives the phone to the primary line, because a bad edit must never take a
phone down.

**2. `*4`, for the call that is not the usual one.** Dial it from any handset.
doorman answers, reads out the numbers this box can call as, takes a digit,
reads back the number you will present, then takes the number to dial — `#` to
finish, or just stop dialling and it goes.

It speaks with Asterisk's own sound files rather than a prompt pack, so no
pack has to supply anything and swapping packs cannot break it. The menu says
numbers rather than names for the same reason: there is no recording of
anybody saying "Mertaugh Enterprises", and the number is what the person you
ring is going to see anyway.

The clips it uses are `vm-enter-num-to-call`, `vm-then-pound`, `vm-num-i-have`,
`pbx-invalid`, `vm-goodbye` and `beep`, from `asterisk-core-sounds-en`. If your
sounds package names them differently you will hear the digits and the beep and
none of the words — the console still works, because the digits carry the
meaning and the words are glue. Check what you have with
`ls /var/lib/asterisk/sounds/en/`.

```bash
$ journalctl -u doorman | grep console
# line chosen line=biz presents=+1512•••0142
# placing an outbound call line=biz number=+1512•••0199
```

**`*4` cannot dial 911, by design.** E911 is registered per DID against a
street address, so an emergency call placed as another line would reach a
dispatcher with somebody else's address on screen. The console beeps once and
releases the handset so you can dial 911 directly — which is untouched, and
leaves with the trunk's own caller ID as it always has. Two things guard this:
doorman refuses the number, and the context it releases into contains no
emergency pattern for it to reach. `_911` lives in `[internal]`, which the
console never enters; the outbound patterns both paths share cannot match three
digits.

**`*4` inherits whatever trust the LAN handsets have** (see issue #13). It is
not new exposure — a compromised handset can already dial `_NXXNXXXXXX`
straight out — but it is one more thing on the other side of a SIP password.
The threat-model section below is the fuller version.

**Verify it on a real handset, not from the logs.** Logs show what was sent;
only the phone in your hand shows what arrived:

```bash
# From the office phone, ring a mobile. It should show the business number.
# From the kitchen phone, ring the same mobile. It should show the home one.
# Then *4, press the home line's digit from the office phone, ring again.
```

**When it goes wrong.** The most common cause is a render that never made it
to `/etc/asterisk`: `doorman check` and the `*4` console read the policy file
live, but the plain dial path reads the generated PJSIP config, so the two can
disagree until you re-render. Check what the endpoint actually carries:

```bash
$ sudo asterisk -rx 'pjsip show endpoint office' | grep -i set_var
# want both, and from the same line:
#   set_var=OUTBOUND_CID=+15125550142
#   set_var=OUTBOUND_TRUNK=telnyx
```

If your provider rejects the caller ID or silently replaces it, it is because
the account does not own that number — providers do this as anti-spoofing.
Either the DID is on a different account (buy it on this one, or present a
number you own), or the call left by the wrong trunk. Check the second one on
the provider's CDR rather than from here: a rewritten caller ID looks like a
perfectly normal call from this end.

**Rolling back** is removing the two keys and re-rendering. With no
`outbound_cid` anywhere, nothing is generated and every outbound call presents
the trunk default again, exactly as before.

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

### Watch a prepaid trunk's balance

**The failure this prevents is silent by construction.** A prepaid account that
reaches zero does not error. Inbound calls simply stop arriving, so "nobody
called today" is indistinguishable from a quiet Tuesday: no log line, no alarm,
and no symptom at all until somebody mentions they tried to reach you last
week. With several ventures on one box it is every business's inbound at once,
and they find out from a customer.

`doorman balance` asks each provider, prints a table, and **exits non-zero when
any trunk is below its threshold** — so a cron entry is a one-liner and nobody
needs a wrapper script.

```bash
$ doorman balance
$ doorman balance --json          # for a script
$ doorman balance --min 25        # a threshold for trunks that declare none
```

| Exit | Means |
|---|---|
| 0 | everything checkable is above its threshold |
| 1 | a trunk is **below** its threshold — the one to act on |
| 2 | the command line or `trunks.toml` was wrong |
| 3 | nothing was low, but something could not be checked at all |

3 is separate from 1 on purpose: an expired API key and an empty account look
identical from here, and treating one as the other is how a real outage gets
dismissed as a broken cron job. 1 wins when both happen.

**No `trunks.toml` means there is nothing to check**, and the command says so
and exits 0. That is every single-provider install and it is not a mistake.

#### Run it off the Pi

**This credential is not the SIP sub-account password, and the difference is
the point.** A provider's API login manages DIDs, sub-accounts and billing;
the sub-account can only make calls. §1 above already says not to put your main
VoIP.ms login on the Pi — this is that login.

So `doorman balance` is a CLI and **the daemon never reads these keys, and
never polls**. Put the check on whatever machine your alerting already runs on,
with a copy of `trunks.toml` and the key in its environment. Nothing there
needs a daemon, an Asterisk, or a phone:

```cron
0 9 * * *  doorman balance --trunks /etc/doorman/trunks.toml \
             || notify-me "call me maybe: trunk balance"
```

If you do keep it on the Pi, know that you have widened the blast radius of a
compromised Pi from "a sub-account that can make calls" to "the account that
owns the DIDs", and scope the key as far down as the provider allows.

#### Setting it up on VoIP.ms

Three keys on the trunk, and two portal steps people reliably hit:

```toml
[[trunks]]
id = "voipms"
provider = "voip.ms"
# ... registration fields as above ...
api_username     = "you@example.com"            # the ACCOUNT EMAIL, not the sub account
api_password_env = "TRUNK_VOIPMS_API_PASSWORD"  # the NAME of a .env variable
balance_min      = 25.0                         # exit 1 below this
```

1. **Main Menu → Account Settings → API**: switch API access on and set an API
   password there. It is a separate password from the portal login.
2. **Same page**: add the IP address of the machine that will run the check to
   the allow-list. An unlisted address fails *exactly* like a wrong password,
   which is the single most common way this looks broken when it is not.

Pick a `balance_min` that leaves time to act. A warning at zero is a death
rattle, not an alert.

A provider that invoices rather than holding a balance is reported as
"postpaid — no balance to report", never as a zero; a provider doorman has no
client for is reported too, rather than quietly dropped from the table. A row
that silently disappears is the same silence this whole check exists to break.

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
doorman calls                          # last 20, numbers redacted
doorman calls --since 7d               # a duration (24h, 7d) or a date
doorman calls --outcome dismissed      # answered|voicemail|dismissed|abandoned|placed
doorman calls --caller 0100            # match part of a number, either end
doorman calls --line biz               # one line; "default" is plain policy.toml
doorman calls --direction outbound     # inbound|outbound
doorman calls --json | jq .            # JSON Lines for anything else
doorman calls --no-redact              # full numbers
```

**Why this is worth having beyond curiosity.** The `stages` array is the ring
ladder as actually walked, with per-rung timings. That is what tells you a
stage expires before anybody can cross a room, or that the lobby's ten seconds
is too short because most strangers are dismissed with `no-digits` rather than
a wrong extension. Those are policy questions, and they are unanswerable from
the operational log.

### Several lines, one log

A box answering several numbers writes **one** file, with `line` on each
record, rather than one file per line — so a whole day still reads in order,
which is how anybody actually looks for a call. `--line` narrows it after the
fact.

`line` is absent on the default line, the one plain `policy.toml` configures.
That is deliberate on both ends: a box with one number writes exactly the
records it always did, a record written by an older doorman still means what it
says, and `--line default` finds them all. The `LINE` column appears only once
a call has arrived on a line with a name — the same rule `doorman check`
follows, so nobody with one number ever has to read the word "line".

### Outbound calls

A call placed through the `*4` console gets a record too, with
`"direction":"outbound"` and the number in `dialled` rather than `caller`.
Inbound is the absent default, again so nothing changes for an install that
never places one.

Its outcome is `placed` — not `answered`. doorman sets the caller ID, hands the
channel to the dialplan and is out of the call before the far end rings, so
whether anybody picked up and how long they talked are not things it can know.
For the same reason `ms` on an outbound record is how long the console had the
handset, not how long the call lasted, and `doorman calls` does not print it in
the column where an inbound row shows exactly that.

A console call that never got as far as dialling is `dismissed` with a reason:
`no-digits`, `too-many-attempts`, `emergency-refused` (see "Outbound caller ID,
and the `*4` console" above — `*4` cannot dial 911, by design and in two
independent places), `cid-failed` or `handoff-failed`. Only a complete number
the console
accepted reaches `dialled`; a half-entry somebody gave up on is forgotten.

Calls dialled straight from a handset never touch doorman — that is what keeps
outbound working when the daemon is down — so they are not in this log. The
provider's CDR is the record of those.

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

## 6b. The event webhook

Off by default. Set `WEBHOOK_URL` and restart:

```bash
# .env
WEBHOOK_URL=http://homeassistant.local:8123/api/webhook/call-me-maybe
```

doorman then POSTs one JSON object when the house starts ringing and one when
a call ends. If the URL cannot work it refuses to start, for the same reason
the call log does: an operator who asked for announcements and did not get
them should find out now.

**The URL is a credential.** A Home Assistant webhook id is the entire secret
— anyone who has it can trigger the automation. Keep it in `.env`, not in
`policy.toml`, and note that doorman never prints it: log lines and startup
errors carry the host only.

### The two events

```json
{"event":"ringing","at":"2026-08-08T18:04:11Z","call_id":"1754680",
 "caller":"+1512•••0100","known":"Grandma"}

{"event":"completed","at":"2026-08-08T18:04:29Z","call_id":"1754680",
 "caller":"+1512•••0100","known":"Grandma","outcome":"answered",
 "answered_by":"PJSIP/kitchen","ms":18420}
```

`ringing` fires the moment handsets start ringing — for a welcomed known
caller, and for a stranger who dialled a valid extension (`extension` and
`pin: "valid"` instead of `known`). That timing is the point: "call from
Grandma" is useful while somebody can still reach a handset and useless once
the ringing has stopped. A caller who is dismissed rings nothing and produces
only a `completed` event.

`completed` fires once per call from the session's single teardown path,
however the call ended. `outcome` is `answered`, `voicemail`, `dismissed` or
`abandoned`, with `reason` on a dismissal (`no-digits`, `rate-limited`,
`too-many-attempts`, `no-answer`).

On a box answering several numbers both events carry `line` — which is what an
automation routes on, announcing the business line in the office and the house
line everywhere. It is absent on the default line, so a box with one number
sends exactly the payload it always did.

Only inbound calls produce events. An outbound call placed through `*4` rings
nothing in this house and ends somewhere doorman cannot see, so there is
nothing here to announce; it is in the call log instead.

Add `WEBHOOK_TOKEN` if the receiver wants `Authorization: Bearer`. HA's
`/api/webhook/<id>` endpoints do not.

### doorman does not know what a speaker is

The payload names no `media_player` entity, no speaker, and no routing — only
what happened and to whom. Which speakers hear it is a Home Assistant
automation, because HA already models the house's devices and doorman would
only duplicate that badly. That is what makes Sonos, Cast, MQTT and everything
else HA supports work without doorman learning about any of them. The design
argument is in `.plans/s06-speaker-page-targets`.

A minimal automation, to be filled in with your own speakers:

```yaml
automation:
  - trigger:
      - platform: webhook
        webhook_id: call-me-maybe
    condition: "{{ trigger.json.event == 'ringing' and trigger.json.known }}"
    action:
      - service: tts.speak
        data:
          message: "Call from {{ trigger.json.known }}"
```

### What it never sends, and what it redacts

Entered digits and PINs, never, at any setting — a payload says a PIN was
`valid` or `invalid` and never what was typed. There is no field one could go
in: the event is built from the call record, which has no such field either.

Caller IDs are **redacted by default**, which is the opposite of the call log
and deliberate. The call log is a `0600` file that stays on the box; this
payload is handed to another program over the network. Set
`WEBHOOK_REDACT_CALLER_ID=false` when the receiver genuinely needs the number
— announcing an unknown caller, say — and understand that you have decided the
number may leave doorman.

### A dead Home Assistant is invisible

Events are queued and delivered off the call path, so a slow, wedged or
switched-off endpoint can never delay a greeting or a ring. A full buffer
drops events and counts them; failures are logged at `warn` with the host and
counted. `WEBHOOK_TIMEOUT_MS` (3 s default) bounds one attempt and also bounds
shutdown — past that grace period doorman abandons the backlog rather than
holding a restart open.

```bash
$ journalctl -u doorman --since "24 hours ago" | grep "webhook post failed"
```

To turn it off, unset `WEBHOOK_URL` and restart.

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

`*4` inherits exactly that trust and adds none of its own: anything that can
reach the console can already dial `_NXXNXXXXXX` straight out, so the toll-fraud
exposure is the handset password either way. What the console does add is the
choice of which of your numbers a fraudulent call presents — worth knowing
about, and one more reason the handset passwords are the thing to get right.
See issue #13.

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
| The provider API key stays out of the daemon | `internal/provider` is reachable only from `doorman balance`; tests assert nothing on the call path imports it and no other file in `cmd/doorman` reads the credential |
| Every config key is one the schema names | `doorman check` and the LSP reject an unknown key; the daemon warns and keeps running |
