# First boot — a Pi, out of the box, to a ringing phone

A day-one path. `RUNBOOK.md` is the reference; this is the order to do things
in, with the checks that catch a problem before it costs you an afternoon.

Roughly 90 minutes, most of it waiting on downloads and DNS. The only step
with a deadline is the VoIP.ms one — E911 registration can take a little while
to become live, so do it first and let it settle while you build the rest.

---

## 0 · Before you power anything on

Five minutes with the hardware in your hand saves the two most common
week-one failures: audio that sounds bad, and an SD card that dies quietly.

| Check | Want | Why |
|---|---|---|
| **Wired ethernet** | yes | The big one. Wi-Fi jitter is choppy audio and one-way calls, and a phone that sounds bad stops getting picked up. Rules out a Zero 2 W |
| **Boot media** | USB SSD, not microSD | SD corruption is the classic Pi failure and it is *silent* — the worst kind here. A Pi 4 boots from USB; a Pi 5 can do NVMe |
| **RAM** | 2 GB is ample | Asterisk + a Go binary + Pi OS Lite is a few hundred MB. 8 GB buys nothing on this workload |
| **PoE** | if you can | One cable for power and network — and a UPS on the switch keeps the phone alive through an outage, which is most of the argument for having a landline |
| **Power supply** | the official one | A Pi 5 wants 5 V/5 A. Undervolting shows up as random reboots that look like software faults |
| **Cooling** | Pi 5: active | Only if you went that way; a Pi 4 in a passive case is fine here |

**Do not buy CPU for this.** No TTS, no STT, no LLM, no database — prompts are
pre-rendered WAVs and the Pi only reads files and relays RTP. Asterisk ran this
workload on 500 MHz boxes for twenty years.

---

## 1 · VoIP.ms, before you touch the Pi

Do this first. The values feed straight into config, and E911 wants time.

Full table in `RUNBOOK.md` §1; the short version:

1. **Sub Accounts → Create Sub Account.** Auth **User/Password**, device type
   **generic ATA/IP phone**, **NAT: yes**. Note the username — it looks like
   `123456_home` — and set a long password.
2. **Codecs**: `ulaw` first, `g722` second, everything else off. Never g729;
   it costs money and the Pi would transcode.
3. **DIDs → Manage DID**: route your number to that sub account.
4. **E911**: register the service address. A couple of dollars a month, and
   the one line item worth not skipping on a phone your family will grab in a
   hurry.
5. **Servers**: pick the nearest POP and use that exact hostname everywhere.
   Mixing POPs causes registration flapping that looks like a network fault.

**Two things that are not optional.**

**Never put your main VoIP.ms login on the Pi.** A sub account can be disabled
without losing the DID or the balance. The Pi is a device in a cupboard; treat
its credentials as disposable.

**Set a spending limit on the account.** A misconfigured dialplan that loops on
outbound is a real way to spend real money overnight, and you will not find out
until the balance mail arrives.

### Which credential is which

Three different secrets get confused constantly:

| Credential | Used by | Goes where |
|---|---|---|
| Portal login | you, in a browser | **nowhere on the Pi** |
| Sub-account username + password | Asterisk, to register the trunk | `.env`, substituted into generated PJSIP at render time |
| API username + password | only if you use `doorman balance` later | not needed on day one; higher privilege than the sub account, so prefer keeping it off the Pi |

---

## 2 · The OS

Raspberry Pi OS **Lite**, 64-bit. No desktop — nothing here needs one, and the
smaller image is less to keep patched.

Use Raspberry Pi Imager and set, before writing: hostname, your SSH public key,
locale and timezone. Timezone matters more than it looks — `[[schedules]]`
quiet hours are local time, so a Pi left on UTC puts the kids' line to bed at
the wrong hour.

Then:

```bash
ssh pi@raspberrypi.local
sudo apt update && sudo apt full-upgrade -y && sudo reboot
```

---

## 3 · Compatibility checks on the box

Run these before installing anything. Each has a specific failure it prevents.

```bash
uname -m          # aarch64 = arm64 build; armv7l = the 32-bit build
free -m           # a few hundred MB free is plenty
ip -br addr       # confirm you are on ethernet, not wlan0
timedatectl       # timezone correct, NTP synchronised
findmnt /         # is / on the SSD you think it is?
```

**`uname -m` decides which binary you need** — `doorman-linux-arm64` or
`doorman-linux-armv7`. `install.sh` picks for you; this is for when something
looks wrong later.

**Clock sync is not cosmetic.** Schedules, the call log and TLS all depend on
it, and a Pi with no RTC comes up in 1970 until NTP lands.

Then confirm Asterisk is available from your distribution:

```bash
apt-cache policy asterisk
```

Asterisk 18 or newer. The installer offers to install it and **asks first** —
it knows apt, and it will not use it behind your back.

---

## 4 · Install

```bash
curl -fsSL https://callmemaybe.cc/install.sh | bash
```

It fetches the right binary for your architecture, checks it against the
published checksums, and offers to install Asterisk. One static binary, no
runtime, no node_modules.

Verify:

```bash
doorman version
doorman schema          # the whole config surface, as JSON Schema
```

`doorman schema` is also the answer to "what can I put in these files" — it is
the authority, and `doorman check` is the authority on whether a given file is
valid.

---

## 5 · Configure

```bash
doorman init
```

This writes starting `handsets.toml`, `policy.toml` and `.env` with
**error-based placeholders** — values that are structurally impossible rather
than merely wrong, so a config you forgot to finish cannot quietly appear to
work.

Fill in, in this order:

1. **`.env`** — the sub-account username and password from §1, and the POP
   hostname.
2. **`handsets.toml`** — one block per phone. Ids here are referenced by name
   from `policy.toml`.
3. **`policy.toml`** — who gets in and what rings. `docs/writing-policies.md`
   is the guide, and <https://callmemaybe.cc/llms-policy.txt> is the version to
   hand an AI assistant.

Then, every time:

```bash
doorman check
```

It validates both files, prints what they resolve to **including the defaults
it applied**, and **rejects a key it does not recognise** with a suggestion:

```
policy: unknown key "afterhous" in [[extensions]] — did you mean afterhours?
```

That last part matters on day one, because a misspelled optional key used to
validate cleanly and silently do nothing.

**One number means you never touch lines.** Plain `policy.toml` is the default
line and gets every call. Multiple numbers is `docs/RUNBOOK.md` → "Add a second
number"; ignore it until you want one.

---

## 6 · Render and install the Asterisk config

```bash
doorman render
```

Generates the per-handset PJSIP and dialplan from `handsets.toml`, substituting
secrets from `.env` at render time.

**The generated files are outputs.** Never hand-edit them and never commit them
— the PJSIP one holds real handset passwords. Change `handsets.toml` and
re-render.

Copy them into place and reload Asterisk per `RUNBOOK.md` §2.

---

## 7 · Prompts

The bundled pack ships with the repo — six WAVs, free, CC BY-SA. Copy it to
`/var/lib/asterisk/sounds/call-me-maybe/` and you are done.

Rendering your own is a workstation job, never the Pi:

```bash
doorman pack build ./my-pack        # piper is free, local, offline
```

The Pi never synthesises speech. That is why the lobby answers instantly and
why a broken speech service can never make the phone unreachable.

---

## 8 · Verify, in order

```bash
./scripts/smoke.sh
```

Run it **on the Pi**. It walks the whole chain and tells you which rung failed,
which is faster than guessing. `RUNBOOK.md` §3 is the same ladder by hand.

The order that finds problems soonest:

1. Asterisk is running and the module list is sane
2. The trunk is **registered** — not merely configured
3. A handset registers
4. Handset → handset works (proves nothing about the trunk, everything about
   the handsets)
5. Inbound from a mobile reaches the lobby
6. Outbound from a handset connects

If inbound rings nothing, the usual cause is DTMF: **`dtmf_mode=rfc4733` on the
trunk *and* every handset.** Without it the lobby is deaf and every stranger is
dismissed, with no other symptom.

---

## 9 · Before you rely on it

**Test 911 deliberately, once.** Most areas have a non-emergency test path —
ask your provider rather than dialling and hanging up.

And know the limit, because the people in the house should: **this is a
supplementary phone.** It sits on a consumer internet connection and stops
working in a power cut. Nobody should make it a household's only route to
emergency services. That is not a disclaimer, it is what the thing is.

If you later add a second provider, `doorman check` prints which trunk carries
911 and whether that was chosen or inferred. Read it.

**Turn the call log on if you want "who rang while I was out":**

```bash
# .env
CALL_LOG_PATH=/var/lib/doorman/calls.jsonl
```

Off by default because it holds full caller IDs. `doorman calls` reads it and
redacts unless you ask otherwise.

---

## When it does not work

`RUNBOOK.md` §4 is troubleshooting, organised by symptom. The three that catch
almost everyone:

- **Registration flapping** — mixed POP hostnames between `server_uri` and
  `contact`.
- **Inbound arrives but nobody can get in** — DTMF mode, as above.
- **Calls connect but audio is one-way** — NAT. Confirm `NAT: yes` on the sub
  account, and that you are on ethernet.

Read `RUNBOOK.md` §7 before exposing anything beyond the LAN. The short version:
ARI never leaves `127.0.0.1`, and if doorman ever moves off-box it goes over a
tailnet, never the LAN.
