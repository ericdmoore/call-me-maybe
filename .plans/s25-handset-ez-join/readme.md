# s25 · Handset Ez Join — a new phone is a question, not a procedure

**Status:** planned 2026-10-03, the morning after five handsets were
provisioned by hand (s10's loop, five times: MAC off the screen, IP from
the owner, a block typed, a path typed into the phone's web page). Drafted
against the product direction set the same morning: a package a
non-technical person could run, where adding a phone is "join the Wi-Fi,
answer some questions" and the questions are eventually asked by a web
page rather than a terminal.

## What done looks like

**M1 — adopt on arrival.** A new WP826 is unboxed, joined to the Wi-Fi
and pointed at the box. The operator has run `doorman provision --adopt`
(or, one day, clicked "open season"). The phone asks for its configuration;
the box, instead of refusing an unknown MAC, says:

```
A new phone is asking: Grandstream WP826, ec:74:d7:a6:d2:66, from 192.168.7.137.
  Room name?        Grace
  Extension number? 107           (101–199; 103–106 are taken)
  Page with 500?    [Y/n]
  Own voicemail?    [Y/n]
  Phone books?      house         (house / house+people / a contacts source)
  Bedtime?          late-weeknights, late-weekends   (or none)
```

and then does everything s10 made separate steps: writes the `[[handsets]]`
block, generates the three `.env` keys and the mailbox PIN, renders, installs
the Asterisk fragments, reloads, serves the phone its file, and watches it
register. One command, five answers, no MAC and no block typed by a person.
The same answers from a form produce the same result, because the form will
call the same function.

**M2 — direction, not deliverable: the box points the phone.** Today the
phone must be told where the box is (a path in its web page, or DHCP
option 66 from a router that can). The eventual shape is the box noticing a
new Grandstream on the LAN and pushing the path into it through the phone's
own HTTP API, with the factory admin password typed once at the prompt and
never stored — the fetched configuration replaces it. Then "join the Wi-Fi"
is the whole procedure. This stream builds M1 so that M2 slots in as one
more step at the top of the same flow, and it ends with the rehearsal that
settles M2's unknowns against a real phone.

## Starting point

- `doorman provision <id>` (s10) opens a window, serves `cfg<mac>.xml` to
  the phones listed in `handsets.toml`, refuses any other MAC and prints
  it, and exits when the listed phone registers. The refusal is where M1
  begins: the box already knows a stranger has arrived and who it is.
- The pieces M1 composes all exist as commands: the block shape
  (`examples/handsets.example.toml`), the keys (`openssl rand`, soon
  `rotate --phones` per the task chip), `rotate --voicemail`, `render`,
  the install lines render prints, `provision notify`. None of them is a
  function yet; each is a `main.go` path with its own flags and printing.
- s10's rehearsal notes say the router "cannot hand out option 66", which
  is why the path is typed per phone. The handset LCD has no config-server
  entry; the web page is the only way in from the phone's side.
- The `*88` work (s24) added `*88` to the house phone-book list, so a
  phone's own directory already says how to add a number; a new room
  reaches every other phone's directory at its next hourly poll with no
  action. Nothing in this stream touches that.

## Decisions and invariants

**Adoption is a function, with the CLI as its first caller.** `internal/
provision/adopt` (or `internal/setup`, which already owns `.env` writing)
gets `Adopt(answers) (Plan, error)` that returns the block, the keys to
create, the files to render and the reloads to run — and `Apply(Plan)` that
does them, atomically where the pieces allow (the `.env` and `handsets.toml`
writes already are). The CLI asks the questions and prints the plan; a web
page asks the same questions and calls the same two functions. Nothing of
the flow lives in `main.go`'s printing.

**The window stays a window.** "Open season" is `--adopt` on the existing
bounded window, not a new always-on listener: a listener that hands out
SIP passwords is open for fifteen minutes because an operator opened it
(s10's invariant), and adoption inherits that. An unknown MAC outside an
adopt window is still refused and printed.

**Taken numbers, taken ids, and the bedtime list come from the files.**
The prompt shows what is free so a person cannot pick a collision; the
validator still refuses one. Bedtime offers the `[[schedules]]` ids
(s23); phone books offer house, people and the contacts sources.

**The three keys and the PIN are generated, never typed, never shown.**
Same rule as `init`: the operator sees "generated" and nothing else; the
phone fetches its SIP password, its web-admin password and its
provisioning credential; the mailbox PIN goes to the same root-only file
`rotate --voicemail` writes today.

**M2's credential rule, decided now so M1 does not paint over it.** The
factory admin password is typed at the prompt for the one HTTP push and
is not written anywhere; the configuration the phone then fetches sets
the generated admin password (it already does, since s10). If the vendor
sticker password is per-device, the operator reads it off the sticker
once — that is still "answer a question", not "find a web page".

**Rejected: a cloud "zero config" service (GDMS).** The vendor's own
answer to this problem is a cloud account that the phones phone home to.
Every rule here says no: nothing about the house phone depends on the
cloud at the moment a phone is added, and the vendor's terms are not the
house's.

**Rejected: mDNS discovery of the box by the phone.** The WP826 does not
look for one, and s10 chose not to depend on it. The box discovering the
phone (M2) is the direction, because the box is ours to change and the
phone is not.

**Interaction with s09 (init installs services, issue #28):** the adopt
flow needs the directory and the window the same way `provision <id>`
does; nothing new to install.

## Milestones

- **M1** `Adopt`/`Apply` as functions; `doorman provision --adopt` asks,
  shows the plan, applies, watches; the refusal path prints "run
  `provision --adopt` to add it". Tests through the provisioning
  harness: an unknown MAC in an adopt window produces a block, keys,
  rendered files and a served configuration; outside one it is refused
  as today; a taken number is refused before anything is written.
- **M1.1** RUNBOOK "Add a handset" becomes two lines; FIRST-BOOT's
  "Phones" step says "join the Wi-Fi, point it at the box, run adopt".
- **M2 rehearsal** with one spare WP826: find it on the LAN by OUI; the
  HTTP API call that sets the config-server path and provisioning mode;
  whether the factory login is per-device; whether a reboot is needed.
  Every answer becomes a line in this file before M2 is built.
- **M2** the push, as one more step at the top of `--adopt`: "found a new
  Grandstream at 192.168.7.137 — point it at this box? [Y/n]".
