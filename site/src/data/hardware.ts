// The hardware list, as data rather than markup.
//
// KNOWN DUPLICATION: README.md carries the same table, hand-maintained. Two
// copies of a price-sensitive affiliate list will drift, and the drift will be
// invisible — a dead link on the site while the README looks fine. The fix is
// to make one generate the other (this file is the better source, since it is
// already structured) and have CI fail when they disagree. Not built yet; do
// not add a third copy in the meantime.
//
// Amazon links are affiliate links. That is disclosed on the page and in the
// README; do not remove the disclosure.

export type Kind = 'pi' | 'wifi' | 'ata' | 'dect' | 'desk';

export interface Item {
  name: string;
  kind: Kind;
  /** Short label for the type column. */
  type: string;
  /** Why someone would pick this one. Written to help, not to sell. */
  note: string;
  url?: string;
}

export const kindLabels: Record<Kind, string> = {
  pi: 'The Pi',
  wifi: 'Wi-Fi handsets',
  ata: 'Reuse the phones you own',
  dect: 'Cordless, done properly',
  desk: 'Desk phones',
};

export const hardware: Item[] = [
  // ── The Pi ────────────────────────────────────────────────────────────
  {
    name: 'Raspberry Pi 5',
    kind: 'pi',
    type: 'Pi',
    note: 'The most headroom. Wants active cooling and the 5 V/5 A supply.',
    url: 'https://amzn.to/3S3E5vV',
  },
  {
    name: 'Raspberry Pi 4',
    kind: 'pi',
    type: 'Pi',
    note: 'The sweet spot. 2 GB is ample, and a PoE HAT fits so one cable does power and network.',
    url: 'https://amzn.to/3RYUgdU',
  },
  {
    name: 'Raspberry Pi 3',
    kind: 'pi',
    type: 'Pi',
    note: 'The cheapest that still has wired Ethernet — which matters more than the model.',
    url: 'https://amzn.to/4xghXxv',
  },

  // ── Wi-Fi handsets ───────────────────────────────────────────────────
  {
    name: 'Grandstream WP826',
    kind: 'wifi',
    type: 'Cordless Wi-Fi',
    note: 'What this was built and tested against.',
    url: 'https://amzn.to/44YDPRP',
  },
  {
    name: 'Grandstream WP816',
    kind: 'wifi',
    type: 'Cordless Wi-Fi',
    note: 'Compact and portable. Same family, smaller.',
    url: 'https://amzn.to/4yMiB7h',
  },

  // ── ATA ──────────────────────────────────────────────────────────────
  {
    name: 'Grandstream HT812 V2',
    kind: 'ata',
    type: 'ATA · 2× FXS',
    note: 'Puts analog phones you already own on the lobby — an existing cordless base, or a 1950s rotary. Two ports, so two handset entries. Usually the cheapest way to cover rooms, and the most fun.',
    url: 'https://amzn.to/4yS8Wwc',
  },

  // ── DECT ─────────────────────────────────────────────────────────────
  {
    name: 'Grandstream DP752',
    kind: 'dect',
    type: 'DECT base',
    note: 'Carries several DP7xx handsets, each registering as its own SIP account — one entry per handset, exactly like a desk phone.',
    url: 'https://amzn.to/4ftcHAr',
  },
  {
    name: 'Grandstream DP730',
    kind: 'dect',
    type: 'DECT handset',
    note: 'Pairs to the DP752. Buy one per room.',
    url: 'https://amzn.to/4vYvEzV',
  },
  {
    name: 'Yealink W73P',
    kind: 'dect',
    type: 'DECT bundle',
    note: 'W70B base plus a W73H handset. The straightforward place to start.',
    url: 'https://amzn.to/4wyMO8q',
  },
  {
    name: 'Yealink W73P + extra W73H',
    kind: 'dect',
    type: 'DECT bundle',
    note: 'Same, with a second handset in the box — cheaper than adding one later.',
    url: 'https://amzn.to/3TLJSXn',
  },
  {
    name: 'Yealink W79P',
    kind: 'dect',
    type: 'DECT bundle',
    note: 'W70B base plus the ruggedised W59R — the one to pick if it is going to get dropped.',
    url: 'https://amzn.to/4fMKbZx',
  },
  {
    name: 'Yealink W79P + 1 extra W59R',
    kind: 'dect',
    type: 'DECT bundle',
    note: 'Two rugged handsets.',
    url: 'https://amzn.to/4h4kR3x',
  },
  {
    name: 'Yealink W79P + 2 extra W59R',
    kind: 'dect',
    type: 'DECT bundle',
    note: 'Three handsets — the cheapest route to a whole-house cordless set.',
    url: 'https://amzn.to/4pMK04N',
  },

  // ── Desk ─────────────────────────────────────────────────────────────
  {
    name: 'Grandstream GRP2601P',
    kind: 'desk',
    type: 'Desk · PoE',
    note: 'Entry desk phone. The P is PoE, so one cable carries power and network.',
    url: 'https://amzn.to/4fLsxFy',
  },
  {
    name: 'Grandstream GRP2602P',
    kind: 'desk',
    type: 'Desk · PoE',
    note: 'Same family, the higher tier of the two.',
    url: 'https://amzn.to/4hEZPZm',
  },
  {
    name: 'Grandstream GRP2602W',
    kind: 'desk',
    type: 'Desk · Wi-Fi',
    note: 'The W is built-in Wi-Fi, for a room with no Ethernet drop. Prefer the P anywhere you have a cable.',
    url: 'https://amzn.to/3S3jI1R',
  },
  {
    name: 'Yealink T31P',
    kind: 'desk',
    type: 'Desk · PoE',
    note: 'Very common, well built, cheap. A safe default.',
    url: 'https://amzn.to/4pKnQAe',
  },
  {
    name: 'Fanvil X3U',
    kind: 'desk',
    type: 'Desk · PoE',
    note: 'About as cheap as this gets while still being pleasant to use.',
    url: 'https://amzn.to/4xbLUyA',
  },
  {
    name: 'Cisco SPA504',
    kind: 'desk',
    type: 'Desk · PoE',
    note: 'The classic four-line workhorse, and abundant secondhand. Factory-reset anything used — it may still be provisioned to its last owner.',
    url: 'https://amzn.to/44Ww1QC',
  },
];

export const byKind = (kind: Kind): Item[] => hardware.filter((h) => h.kind === kind);
