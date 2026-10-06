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

export type Kind = 'brain' | 'wifi' | 'ata' | 'dect' | 'desk';

export interface Item {
  name: string;
  kind: Kind;
  /** Short label for the type column. */
  type: string;
  /** Why someone would pick this one. Written to help, not to sell. */
  note: string;
  url?: string;
  /** Rounded USD estimate, not a live quote from the affiliate listing. */
  approxPrice: string;
  priceBasis: string;
  priceSource: string;
}

export const kindLabels: Record<Kind, string> = {
  brain: 'The PhoneBrain',
  wifi: 'Wi-Fi handsets',
  ata: 'Reuse the phones you own',
  dect: 'Cordless, done properly',
  desk: 'Desk phones',
};

// Curated for distinct uses; keep each category to at most four entries.
export const priceCheckedDate = '2026-10-05';
export const hardware: Item[] = [
  // ── PhoneBrain ────────────────────────────────────────────────────────────
  {
    name: 'MiniPC',
    approxPrice: '~$310',
    priceBasis: 'BOSGAME E2 · 16 GB / 512 GB',
    priceSource: 'https://www.bosgamepc.com/products/bosgame-mini-pc-e2',
    kind: 'brain',
    type: 'Mini PC',
    note: 'An alternative to a Raspberry Pi for hosting Asterisk and doorman. Install a supported Linux distribution and use wired Ethernet.',
    url: 'https://amzn.to/4dmUIu0',
  },
  {
    name: 'Raspberry Pi 5',
    approxPrice: '~$80',
    priceBasis: '2 GB board only',
    priceSource: 'https://www.raspberrypi.com/news/price-increases-for-2gb-raspberry-pi-4-and-raspberry-pi-5/',
    kind: 'brain',
    type: 'Pi',
    note: 'More headroom than the Pi 4. Wants active cooling and the 5 V/5 A supply.',
    url: 'https://amzn.to/3S3E5vV',
  },
  {
    name: 'Raspberry Pi 4',
    approxPrice: '~$70',
    priceBasis: '2 GB board only',
    priceSource: 'https://www.raspberrypi.com/news/price-increases-for-2gb-raspberry-pi-4-and-raspberry-pi-5/',
    kind: 'brain',
    type: 'Pi',
    note: 'A modest brain with 2 GB of RAM. A PoE HAT can carry power and network over one cable; sold separately.',
    url: 'https://amzn.to/3RYUgdU',
  },
  {
    name: 'Raspberry Pi 3',
    approxPrice: '$35–55',
    priceBasis: 'B / B+ board only',
    priceSource: 'https://www.adafruit.com/product/3775',
    kind: 'brain',
    type: 'Pi',
    note: 'The cheapest that still has wired Ethernet — which matters more than the model.',
    url: 'https://amzn.to/4xghXxv',
  },

  // ── Wi-Fi handsets ───────────────────────────────────────────────────
  {
    name: 'Grandstream WP826',
    approxPrice: '~$110',
    priceBasis: 'Handset + charger',
    priceSource: 'https://www.voipsupply.com/grandstream-wp826-wi-fi-phone',
    kind: 'wifi',
    type: 'Cordless Wi-Fi',
    note: 'What this was built and tested against.',
    url: 'https://amzn.to/44YDPRP',
  },
  {
    name: 'Grandstream WP816',
    approxPrice: '~$65',
    priceBasis: 'Handset + charger',
    priceSource: 'https://www.voipsupply.com/manufacturer/grandstream/phones/wp8xx-wifi-phones',
    kind: 'wifi',
    type: 'Cordless Wi-Fi',
    note: 'Compact and portable. Same family, smaller.',
    url: 'https://amzn.to/4yMiB7h',
  },

  // ── ATA ──────────────────────────────────────────────────────────────
  {
    name: 'Grandstream HT812 V2',
    approxPrice: '~$60',
    priceBasis: 'Adapter only',
    priceSource: 'https://grandstreamdirect.com/products/ht812-v2',
    kind: 'ata',
    type: 'ATA · 2× FXS',
    note: 'Puts analog phones you already own on the lobby — an existing cordless base, or a 1950s rotary. Two ports, so two handset entries. Usually the cheapest way to cover rooms, and the most fun.',
    url: 'https://amzn.to/4yS8Wwc',
  },

  // ── DECT ─────────────────────────────────────────────────────────────
  {
    name: 'Grandstream DP752',
    approxPrice: '~$50',
    priceBasis: 'Base only',
    priceSource: 'https://www.voipsupply.com/grandstream-dp752-dect-voip-base-station',
    kind: 'dect',
    type: 'DECT base',
    note: 'Build a cordless set a room at a time. Pair this base with a DP730 below; the base alone cannot make calls.',
    url: 'https://amzn.to/4ftcHAr',
  },
  {
    name: 'Grandstream DP730',
    approxPrice: '~$95',
    priceBasis: 'Handset + charger; base extra',
    priceSource: 'https://grandstreamdirect.com/products/dp730',
    kind: 'dect',
    type: 'DECT handset',
    note: 'Add another room to a Grandstream cordless setup. Requires a compatible base such as the DP752 above; not a standalone phone.',
    url: 'https://amzn.to/4vYvEzV',
  },
  {
    name: 'Yealink W73P',
    approxPrice: '~$175',
    priceBasis: 'Base + one handset',
    priceSource: 'https://www.voipsupply.com/yealink-dect-ip-phone-with-base',
    kind: 'dect',
    type: 'DECT bundle',
    note: 'W70B base plus a W73H handset. The straightforward place to start.',
    url: 'https://amzn.to/4wyMO8q',
  },
  {
    name: 'Yealink W79P',
    approxPrice: '~$310',
    priceBasis: 'Base + one rugged handset',
    priceSource: 'https://www.voipsupply.com/yealink-w79p-ruggedized-dect-handset-with-base',
    kind: 'dect',
    type: 'DECT bundle',
    note: 'W70B base plus the ruggedised W59R — the one to pick if it is going to get dropped.',
    url: 'https://amzn.to/4fMKbZx',
  },

  // ── Desk ─────────────────────────────────────────────────────────────
  {
    name: 'Grandstream GRP2601P',
    approxPrice: '~$50',
    priceBasis: 'Phone; PoE power extra',
    priceSource: 'https://grandstreamdirect.com/products/grp2601p',
    kind: 'desk',
    type: 'Desk · PoE',
    note: 'Entry desk phone. The P is PoE, so one cable carries power and network.',
    url: 'https://amzn.to/4fLsxFy',
  },
  {
    name: 'Grandstream GRP2602W',
    approxPrice: '~$65',
    priceBasis: 'Phone + power adapter',
    priceSource: 'https://grandstreamdirect.com/products/grp2602w',
    kind: 'desk',
    type: 'Desk · Wi-Fi',
    note: 'The W is built-in Wi-Fi, for a room with no Ethernet drop. Prefer the P anywhere you have a cable.',
    url: 'https://amzn.to/3S3jI1R',
  },
  {
    name: 'Fanvil X3U',
    approxPrice: '~$65',
    priceBasis: 'Phone; power adapter extra',
    priceSource: 'https://www.walmart.com/ip/402598535',
    kind: 'desk',
    type: 'Color · PoE',
    note: 'A color display and Gigabit Ethernet for a desk that doubles as a home office. Use PoE or add a compatible power adapter.',
    url: 'https://amzn.to/4xbLUyA',
  },
];

// The page shows at most four per category. A fifth entry used to be dropped
// silently by a slice; failing the build is the only way anyone notices.
for (const kind of Object.keys(kindLabels) as Kind[]) {
  const count = hardware.filter((h) => h.kind === kind).length;
  if (count > 4) throw new Error(`hardware.ts: ${kind} has ${count} entries; the page shows at most four`);
}

export const byKind = (kind: Kind): Item[] => hardware.filter((h) => h.kind === kind);
