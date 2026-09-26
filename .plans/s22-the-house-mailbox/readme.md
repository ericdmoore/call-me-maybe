# s22 · The house mailbox — one address hears everything the house hears

**Status:** planned (2026-09-25, late). From the user: "can we treat
midbury@bullmoose.cc as a complete audit log? All voicemails? All SMS?
Etc?" — and, on how the box would send: "JMAP has all HTTP endpoints, and
we are well acquainted with the developer. Could install the Bullmoose CLI
on the jepsen box too." Backoff on the PIN limiter was raised in the same
conversation and settled as not needed; that decision is recorded in s08's
neighbourhood, not here.

## What done looks like

Everything the house hears lands in one mailbox, as mail, from four feeds:

- **A voicemail** arrives minutes after it is left: "Voicemail for Master
  bedroom from +1 512 555 0142", the recording attached, the room's own box
  named (s21). Sent by the box through the bullmoose CLI, not SMTP.
- **A text to the house number** arrives as the carrier forwards it, with
  the sender's number. That is VoIP.ms's own forwarding; the box is not
  involved and cannot lose it.
- **A reply the house sent** — "pong", "the garage is closing" — arrives as
  its own mail, so the thread of what the house said is as complete as what
  it was told.
- **Yesterday, every morning:** who rang, who was let in, who was dismissed
  and why, which texts acted, which failed. Rendered from the journal, sent
  as one mail. The durable record stays the journal; the mail is the copy a
  human reads.

Nothing else. Never a PIN, a password, a token, or a handset's SIP secret.
The mailbox becomes the most sensitive thing in the house — recordings and
full caller numbers — and it is treated as such: a scoped, revocable device
token, and no other credential of the house's in the same place.

## Starting point

- Asterisk's `app_voicemail` emails every message with the WAV attached
  when `mailcmd` points at something sendmail-shaped. jepsen has no relay:
  `mailcmd` is commented out and there is no msmtp. `externnotify` fires a
  command per new message with the context, mailbox and counts; TASKS §2
  already earmarks it for transcription.
- The carrier forwards received texts by email once `setSMS` names an
  address; the callback half is live and the email half waits on the
  mailbox existing. The edge keeps inbound only ("the carrier's email
  forwarding is the archive, not this table"); replies are kept nowhere.
- The journal (`EVENT_JOURNAL_PATH`, on at jepsen) records every call's
  admission, ring, outcome and reason, and `doorman inbox` decides texts
  but journals nothing (single-writer rule; `message.*` events reserved).
- The bullmoose CLI is one static binary with a `linux_amd64` release,
  `send` (body on stdin or a Markdown file; a linked local file attaches),
  `token create --scopes send` for a send-only device credential, and
  `init --base … --token …` to configure a box from a token with no
  password. It also has `contacts export`: vCard 3.0 on stdout.
- The `doorman` service account has no HOME of its own (`/opt/call-me-maybe`)
  and a private `/var/lib/doorman`.

## Decisions and invariants

**doorman does not learn to send mail.** Not SMTP, not JMAP, not a
bullmoose dependency in the binary. Every feed is either the carrier's,
Asterisk's, or "doorman prints, a hook sends" — the shape `balance --prom`
already has. The hook is one executable path in `.env`, `MAIL_HOOK`, which
receives a subject and a Markdown body on stdin and exits non-zero if it
could not send. The shipped hook script calls `bullmoose send`; somebody
without bullmoose writes a ten-line wrapper around anything else. Nothing on
the call path invokes it (invariants 3 and 7 territory: a mail hook that
hangs must not hold a call, so it is only ever run by the CLI and by
Asterisk's own after-the-fact hook).

**Voicemail goes by `externnotify`, not `mailcmd`.** `mailcmd` expects a
sendmail that reads a finished MIME message, which the CLI is not, and
parsing Asterisk's email to re-send it is the wrong direction. `externnotify`
runs after the message is saved, off the call, with the mailbox in hand:
the script finds the newest recording in that box's spool, writes a short
Markdown note (room, caller, duration, time) with the WAV linked so the CLI
attaches it, and runs the hook. The attachment stays in the spool; email is
the copy. If the hook fails the message is still in the box and the lamp is
still lit — email is a feed, never the record.

**A send-only token, minted for the box, revocable alone.** On the
workstation: `bullmoose admin token create <house> --name jepsen --scopes
draft,send` — `send` alone cannot create the message it would submit
(found live: `Email/set` refuses without `draft`); neither scope reads. On the
box, as the service account: `bullmoose init --base <jmap> --token bm_…`,
with the CLI's config directed under `/var/lib/doorman/bullmoose` (0700).
That token can send mail as the house and do nothing else; losing the box
means revoking one token. It is the only bullmoose credential the box holds.

**Replies and the digest come from doorman as text.** `doorman inbox`
gains a per-outcome hook call — subject "The house replied to +1 512…",
body the reply — off the decision path, best-effort, and it still does not
journal (that stays s13 M5). `doorman digest` is new: yesterday's calls and
texts from the journal as Markdown on stdout, redacted by the same default
`doorman calls` uses unless `--full`, run by `doorman-digest.timer` each
morning and piped into the hook. The journal stays the source; the digest
is a query over it and never an input to anything (invariant 10).

**The carrier feed is configuration.** `setSMS … email_enabled=1
email=<house mailbox>` the day the mailbox exists. Recorded here because it
is the one feed with no code and no box.

**Bonus, and worth a line: the whole-home phone book.** `bullmoose
contacts export` is a vCard on stdout, which is exactly what a `contacts.toml`
`path` source reads. A timer on the box that writes it to
`/var/lib/doorman/contacts/house.vcf` is the whole-home book the user asked
for on 2026-09-24, with no new source kind in doorman and the house's
address book living where the rest of the house's data does. Read scope
only; a second token, or the same one widened to `read,send` — the user's
call, and the narrower answer is two tokens.

## What is deliberately out of scope

- Transcription (TASKS §2): the same `externnotify` script is where it
  would go, later; this stream ships the hook without it.
- Two-way: reading the mailbox from the box, or acting on mail. The house
  hears; it does not read its own audit log.
- Any mail path inside the daemon.
- Retention of the mailbox: bullmoose's business.

## Milestones and acceptance criteria

### M1 · The hook and the voicemail feed — **done, live** (2026-09-25 night)

**Verified end to end on jepsen:** a message dropped into the family box
from the console (a Local channel into `[voicemail-drop]` with a global
`MAILBOX`) reached `midbury@bullmoose.cc` within seconds as "Voicemail for
family from Unknown", 94 s, recording linked and attached; the kitchen and
theater lamps lit; the user heard the voicemail key answer. Read back from
alpaca with a throwaway read-only token, revoked after.

**Four findings on the way, all shipped (v0.9.1, v0.9.2):**

1. **Voicemail had never worked on the rebuilt box.** Asterisk 22 builds
   `app_voicemail` three ways; with all three autoloaded, the ODBC one
   fails and, on declining, *unregisters* `VoiceMail()` while the file one
   shows Running. Every caller sent to voicemail since 2026-09-23 was hung
   up on with one warning in `messages.log`. The installer now `noload`s
   the ODBC and IMAP variants and `smoke.sh` checks the application is
   registered.
2. **The installer's `noload` lines were in the wrong section.** The
   distro's `modules.conf` ends with `[global]`; appending put them where
   Asterisk ignores them, so the AEL/Lua set-aside only ever worked because
   the sample files had been moved. They go after `autoload=yes` now, and
   a rerun repairs an earlier install.
3. **The token scope is `draft,send`.** `send` alone cannot create the
   message it would submit (`Email/set` refuses without `draft`). Neither
   reads.
4. **The CLI's repository is private**, so the binary is fetched on the
   workstation with `gh` and copied; and the bootstrap bundle must be
   handed to the asterisk user with `install -o asterisk`, not copied.

One more, fixed the same night in v0.9.4: the CEL spool that feeds the
journal had never been set up on the rebuilt box (the distro's disabled
`cel.conf` in place, no `master.db`, `CEL_SPOOL_PATH` unset), which M3's
digest and s01's "which trunk carried it" both lean on. It is host
preparation now — the installer stages the two files, initialises the
spool, grants the read ACL and reloads; `doorman init` points `.env` at
it; `smoke.sh` has a capture rung — and jepsen records every channel since
2026-09-25 21:24. And `*97` asks "mailbox?" on a keypad that cannot type
`family`: mailbox names in the example are words, which s21 M2 resolves by
sending the phone straight into its own box.

**Done so far.** The account `midbury@bullmoose.cc` exists (created with
the operator CLI, tenant `t_bullmoose`), with read grants for the user's own
account and for alpaca's CLI login so the feed can be verified from here.
Two `draft,send` tokens were minted for the box — one for Asterisk's user,
one for doorman's — after the first, `send`-only pair was found unable to
create a message and revoked; stored only in 0600 files on alpaca until
placed. `scripts/voicemail-notify` (externnotify: newest message in the
box, Markdown note, recording linked so the CLI attaches it, `timeout 60`,
outcome to journald under `cmm-voicemail`) and `scripts/mail-hook-bullmoose`
(subject as the argument, Markdown on stdin, `MAIL_TO` from the
environment) ship with the installer; `voicemail.conf.example` carries the
commented `externnotify` line; RUNBOOK "The house mailbox" is the
procedure. One change from the sketch: Asterisk's hook runs as the
`asterisk` user, which cannot read `.env`, so its address lives in
`/etc/asterisk/cmm-mail.env` (0640 root:asterisk) and its CLI login under
`/var/lib/asterisk/.bullmoose`; `MAIL_HOOK`/`MAIL_TO` in `.env` arrive with
M2, when doorman's own feeds start reading them. Verified on the
workstation: a Markdown link to a local WAV attaches ("1 attached").

`MAIL_HOOK` in `.env` (schema, template, man page); `scripts/mail-hook-
bullmoose` (the shipped hook) and `scripts/voicemail-notify` (the
`externnotify` script); `voicemail.conf.example` gains the `externnotify`
line commented; RUNBOOK "The house mailbox": install the CLI on the box,
mint the token, `init` as the service account, test with one message. Done
when a voicemail left on jepsen arrives in the mailbox with the recording
attached, and a hook that fails leaves the message and the lamp untouched.

### M2 · The carrier feed and the replies — replies shipped (v0.10.0, 2026-09-25); the carrier half is a portal switch

**As built.** `doorman inbox` reads `MAIL_HOOK`/`MAIL_TO` and, after each
text is handled, copies any reply the house sent to the mailbox — subject
"The house replied to gabi (garage): The garage is open", body with the
number, word, action, result — in a goroutine, best-effort, never on the
decision path. It also keeps its own outcome log
(`<state>/outcomes.jsonl`, 0600, rotated at 8 MiB), because the journal
has one writer and this process is not it: the morning digest reads it.
The service account gets its own `draft,send` token and CLI login under
`/var/lib/doorman/.bullmoose`, which is why the inbox unit now sets
`HOME=/var/lib/doorman`. The carrier feed is the one piece with no code:
VoIP.ms email forwarding for the DID, a portal switch (or `setSMS
email_enabled=1`), which the user flips because the API password lives
only in the portal and the edge.

`setSMS` email forwarding on (one API call, recorded in RUNBOOK beside the
callback); `doorman inbox` runs the hook per reply, best-effort, never on
the decision path. Done when a `ping` produces two mails: the carrier's copy
of "ping" and the house's copy of "pong".

### M3 · The digest — **done** (v0.10.0, 2026-09-25)

**As built.** `doorman digest [--since 24h] [--full] [--mail]` renders the
day as Markdown: calls from the journal (or the legacy log; a box with
neither is told to set `EVENT_JOURNAL_PATH` rather than failing the
morning), texts from the inbox's outcome log. Redacted on stdout as
`doorman calls` is; `--full` for whole numbers. `doorman-digest.timer`
runs it at 07:30 with `--mail --full`, because the house mailbox is the
audit log and the other feeds already carry whole numbers there. Pure
renderer, tested without a journal; the mail hook runner is shared with
the inbox and tested against a shell script.

`doorman digest [--since] [--full]` over the journal; `doorman-digest.timer`;
the hook. Done when the morning mail lists yesterday's calls and texts,
redacted, and matches `doorman calls` for the same day.

### M4 · The phone book, if the user wants it

`bullmoose contacts export` on a timer into a `path` source; `doorman check`
shows its age like any source. Done when a contact added in bullmoose is on
both handsets after the next refresh and notify.

## Alternatives rejected

- **msmtp → the SMTP shim on alpaca.** Works, is generic, and is a second
  credential in a second format for a mail system that already has a CLI
  with scoped tokens. The port also did not answer from jepsen when probed.
- **doorman speaking JMAP.** A mail client in the phone daemon, keyed to
  one mail system. The hook keeps doorman ignorant of where mail goes.
- **The edge sending the reply copy.** The Worker holds the carrier key
  and should hold nothing else; the box already knows what it replied.
- **Treating the mailbox as the record.** Email can be dropped or
  delayed; the journal cannot be delayed by anything on the call path and
  keeps its own retention. Both, with the roles named.

## Rollout order

M1 needs the mailbox to exist and the CLI on the box — both the user's, then
mine. M2's carrier half is one call the moment the mailbox exists. M3 is
repo-only. M4 waits on the phone-book decision. s21 makes M1's subject
lines say which room.
