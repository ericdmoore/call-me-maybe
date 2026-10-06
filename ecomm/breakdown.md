# eComm — the breakdown of issue #7

Issue [#7](https://github.com/ericdmoore/call-me-maybe/issues/7) ("eComm Arch
Design") grew fifteen comments and about twenty ideas between the first
sketch and 2026-10-06. On that day it was broken into sub-issues so the parts
already built can be crossed off and the rest worked one at a time. This file
is the map that was posted as a comment on #7, kept here so it can be read
without GitHub. The sub-issues themselves hold the detail; this file does not
repeat it.

Settled the same day: **Stripe Managed Payments**, with Stripe as merchant of
record. It covers digital products and subscriptions and excludes physical
goods and services, so the store has two checkout modes (see #64).

## The map

The sub-issue list on #7 is the checklist. This file is the
map: what is already done, what was decided and needs no issue, what lives
elsewhere, and the order.

### Done (closed sub-issues, so they show as crossed off)

- #60 — pack authoring: `doorman pack check|build|voices`, DSP, five TTS backends
- #61 — story packs: the CC0 format and the interpreter library (`internal/story`)
- #62 — delayed reminders: `*80/*81/*82` (PR #38)
- #29 — backup and restore on the box (s27 M1–M5, live on jepsen)

Also done with nothing to file: `hold` and `ringback` mechanisms (`musiconhold.conf`) — they only need #65 and #66 to be sellable — and the actions registry (s13), which is where smart-home access by phone lives.

### Decided in this thread; recorded, no issue

- Player identity: the handset endpoint is the player, the channel is the primitive, no Player ID entity. Internal handsets are originated to; external players dial in on a per-session code.
- Mad Gab mechanics: channel-only whisper, snoop-record, `#` ends a turn, playlist of `sound:` + `recording:`, announce recording, short retention.
- **Payments: Stripe Managed Payments, Stripe as merchant of record** (2026-10-06). Checked the same day: it covers digital products and subscriptions and **excludes physical goods and services**, so packs and the backup subscription go through it and the SD card, USB stick and MiniPC need an ordinary Checkout where we register and remit. Two checkout modes; details in #64. That also resolves the registration half of the tax note from @ericlakich, with product tax codes per kind still ours to set.
- The conversational handset is not eComm: split to #86 as the thread asked.

### Tracked elsewhere

- Smart-home / VRBO menus → #16 (tiers, dial-back) and s13; not a pack.
- Nominations → #6. Outbound alerting and its rate cap → #15.
- The UI's first feature (the address table, s28) → #59.

### Open, by layer

| Layer | Sub-issues |
|---|---|
| Decisions | #63 standard vs paid principle · #64 SKU model + MoR |
| L0 packaging and install | #65 `.cmmpack` + `pack lint\|install\|list\|use` |
| Store plumbing | #66 Cloudflare store, R2 signed delivery, real ESP |
| L1 passive content | #67 rotation · #68 sound board |
| L2 single-caller | #69 wire the story player to an extension |
| L3 primitives | #70 record → play back → confirm · #71 multi-party shell |
| L4 games | #72 Mad Gab · #73 group bedtime / Book Buddy · #74 Packing for Paris · #75 Daily Detective · #76 standing catch-up with Grandma |
| L5 offline generation | #77 daily briefing |
| HA | #78 Home Assistant voice bridge |
| Account and UI (thread B) | #79 account · #80 backup service · #81 UI shell (over #59) |
| Physical goods | #82 install media · #83 preconfigured PhoneBrain |
| Services, one day | #84 reselling VoIP.ms |
| Growth | #85 the greeting names callmemaybe.cc |

### Order

1. #63 then #64 — the principle and the SKU model; everything prices off them.
2. #65 then #66 — L0 and the thin store. `voice`, `hold` and `ringback` packs are sellable with no account and no UI: email, signed link, `pack install`.
3. #67, #69 — rotation and the story player, each with a free sample, which makes jokes and stories listable.
4. #79 then #81 and #80 — the account, the UI shell over s28, the backup service. On "UI before the store?": the first SKUs need no UI; the UI earns its place at re-download and backup status, both of which need the account, so it comes right after the thin store unless the account existing matters more than the first sale.
5. #70, #71, #72 — the primitives and Mad Gab; then #73, #74; #75 later.
6. #77, #78 in parallel with the games; #82/#83/#84 after #63 settles the SUSTAINABILITY.md lines they touch.

## Every sub-issue

| # | State | | Title |
|---|---|---|---|
| #29 | closed | sub-issue | backup/restore: the box's state as one encrypted archive, and a new box from it |
| #60 | closed | sub-issue | Pack authoring: `doorman pack check\|build\|voices`, the DSP pipeline and five TTS backends |
| #61 | closed | sub-issue | Story packs: the CC0 Markdown format and the graph interpreter library |
| #62 | closed | sub-issue | Delayed reminders: *80/*81/*82 handset callbacks and wake-up calls |
| #63 | open | sub-issue | Guiding principle: what is standard, what is paid, and what the store exists to fund |
| #64 | open | sub-issue | SKU model: pack.json as the catalogue, Stripe Managed Payments as merchant of record, two checkout modes |
| #65 | open | sub-issue | L0 · `.cmmpack`: one-file pack format, and `doorman pack lint\|install\|list\|use` |
| #66 | open | sub-issue | Store on Cloudflare: store.callmemaybe.cc — catalogue pages, Checkout, R2 signed delivery, receipts through a real ESP |
| #67 | open | sub-issue | L1 · Rotation: the `rotation` pack kind, a dialplan that reads its count, and a free joke/riddle sample |
| #68 | open | sub-issue | L1 · Sound board: digits play clips into the call, the `sfx` kind, and a free sample |
| #69 | open | sub-issue | L2 · Reach a story from a handset: the ARI Teller, a story extension in policy.toml, and a free sample story |
| #70 | open | sub-issue | L3 · Record → play back → confirm: one helper for spoken names, greetings and game turns |
| #71 | open | sub-issue | L3 · Multi-party session shell: ConfBridge control over ARI, the join window, and an ephemeral invite PIN |
| #72 | open | sub-issue | L4 · Mad Gab: the engine, the `madgab` template kind, and a free template |
| #73 | open | sub-issue | L4 · Group bedtime story / Book Buddy: chapter nights on the multi-party engine |
| #74 | open | sub-issue | L4 · Packing for Paris: a memory-chain game on the Mad Gab primitives |
| #75 | open | sub-issue | L4 · Daily Detective: a season format with durable seats, a daily drop and a who-dun-it poll |
| #76 | open | sub-issue | Standing catch-up with Grandma: a recurring call the house places and bridges |
| #77 | open | sub-issue | L5 · Daily briefing: generated overnight, delivered as a wake-up call or a call-in extension |
| #78 | open | sub-issue | Home Assistant voice bridge: an extension that hands a handset to HA Assist |
| #79 | open | sub-issue | Account: one identity for purchases, the backup cloud destination, the family network and the UI |
| #80 | open | sub-issue | Backup service: the cloud destination, key escrow (BYO storage), an included quota and overage |
| #81 | open | sub-issue | The doorman UI shell: account, boxes, purchased packs and backup status — s28 (#59) is its first feature |
| #82 | open | sub-issue | Physical · Install media: an SD card or USB stick carrying the OS wizard (Ubuntu + doorman) |
| #83 | open | sub-issue | Physical · Preconfigured PhoneBrain: a MiniPC that rings out of the box |
| #84 | open | sub-issue | Service · Reselling or skinning VoIP.ms with a small markup (one day) |
| #85 | open | sub-issue | Growth: the bundled lobby greeting names callmemaybe.cc |
| #86 | open | standalone | Conversational handset: an optional, PIN-gated, fail-closed AI capability plane (split from #7) |

#86 is not a sub-issue by the thread's own decision: the conversational
handset is a different product surface, not eComm.
