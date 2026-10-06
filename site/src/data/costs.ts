// The cost calculator on the providers page, and the arithmetic behind it.
//
// This file is imported twice: by the page at build time, so the default
// household is in the static HTML, and by the calculator script in the
// browser, so changing an input recomputes the same way. Keep every number
// and every formula here; the page only places the results.
//
// Rates come from providers.ts and hardware.ts rather than being copied, so
// a price change there changes this table too. Tin Can is not a SIP
// provider — it is the other answer to "a phone for a kid without a
// smartphone", a handset with the service built in — so its rate card lives
// here, structured the way its storefront prices it.

import { hardware } from './hardware';
import { providers, checkedAt } from './providers';

/** What the calculator asks. */
export interface Inputs {
  /** Handsets in the house, kitchen included. */
  handsets: number;
  /** How many of those are a kid's; one Tin Can each on the other side. */
  kids: number;
  /** Minutes a month across every handset, both directions together. */
  minutes: number;
  /** Share of the chats with people who do not have a Tin Can. */
  outsidePct: number;
}

/** The standard-issue house: a kitchen phone and two kids. */
export const defaults: Inputs = {
  handsets: 3,
  kids: 2,
  // Two kids at half an hour a week each, 52/12 weeks to the month.
  minutes: Math.round(2 * 30 * (52 / 12)),
  outsidePct: 50,
};

/**
 * The light-use month the page has always quoted: one number, one phone,
 * 100 minutes in and 100 out. Expressed as calculator inputs so the same
 * function prices it; the one Tin Can is assumed to call ordinary numbers.
 */
export const lightUse: Inputs = { handsets: 1, kids: 1, minutes: 200, outsidePct: 100 };

export const limits = {
  handsets: { min: 1, max: 12 },
  minutes: { min: 0, max: 20000 },
};

export const fullSetup = {
  /** Half the chats are placed from the house, half rung in by the friend. */
  outgoingShare: 0.5,
  /** The PhoneBrain and the handset the project is tested against. */
  brain: 'Raspberry Pi 5',
  handset: 'Grandstream WP826',
};

export const tinCan = {
  name: 'Tin Can',
  url: 'https://tincan.kids/',
  pricingUrl: 'https://tincan.kids/products/tin-can',
  /** US storefront, per device. */
  device: 100,
  /** Party Line, per Tin Can with its own number. First month free. */
  partyLine: 9.99,
  freeMonths: 1,
  sources: [
    { label: 'Product and plans', url: 'https://tincan.kids/products/tin-can' },
    { label: 'Can 2 Can vs Party Line', url: 'https://faq.tincan.com/t/35yp5d7/what-is-the-difference-between-can-2-can-and-the-party-line-subscription' },
    { label: 'One number per device, or shared', url: 'https://faq.tincan.com/t/x2yp5wt/will-each-device-in-my-home-have-a-separate-number' },
  ],
};

// hardware.ts prices are display strings. Parsing the two this table relies
// on, strictly, means a renamed entry or a reformatted price fails the build
// instead of silently costing the setup with a stale number.
function approxPrice(name: string): number {
  const item = hardware.find((h) => h.name === name);
  if (!item) throw new Error(`costs.ts: hardware.ts has no entry named ${name}`);
  const m = /^~\$(\d+)$/.exec(item.approxPrice);
  if (!m) throw new Error(`costs.ts: cannot read a single price from "${item.approxPrice}" for ${name}`);
  return Number(m[1]);
}

export const hardwareCost = {
  brain: approxPrice(fullSetup.brain),
  handset: approxPrice(fullSetup.handset),
};

export function usd(n: number): string {
  return `$${n.toFixed(2)}`;
}

/** Per-minute rates have a third decimal the two-decimal form would hide. */
function usd3(n: number): string {
  const s = n.toFixed(4).replace(/0+$/, '');
  return `$${s.endsWith('.') ? s + '0' : s}`;
}

function clamp(n: number, min: number, max: number, fallback: number): number {
  if (!Number.isFinite(n)) return fallback;
  return Math.min(max, Math.max(min, Math.round(n)));
}

/** Whatever the browser hands over, made into something the model accepts. */
export function sanitise(raw: Partial<Inputs>): Inputs {
  const handsets = clamp(raw.handsets ?? defaults.handsets, limits.handsets.min, limits.handsets.max, defaults.handsets);
  const kids = clamp(raw.kids ?? defaults.kids, 0, handsets, Math.min(defaults.kids, handsets));
  const minutes = clamp(raw.minutes ?? defaults.minutes, limits.minutes.min, limits.minutes.max, defaults.minutes);
  const outsidePct = clamp(raw.outsidePct ?? defaults.outsidePct, 0, 100, defaults.outsidePct);
  return { handsets, kids, minutes, outsidePct };
}

export interface Line {
  key: string;
  name: string;
  /** What the monthly figure is made of, in words a reader can check. */
  basis: string;
  hardware: number;
  setup: number;
  monthly: number;
  yearOne: number;
  yearTwo: number;
  fourYears: number;
}

export interface Estimate {
  inputs: Inputs;
  inMinutes: number;
  outMinutes: number;
  outsideMinutes: number;
  canMinutes: number;
  tinCan: Line & { partyLines: number; perOutsideMinute: number | null };
  carriers: Line[];
}

function years(hardware: number, setup: number, monthly: number, freeMonths = 0): Pick<Line, 'yearOne' | 'yearTwo' | 'fourYears'> {
  const yearOne = hardware + setup + monthly * (12 - freeMonths);
  const yearTwo = monthly * 12;
  return { yearOne, yearTwo, fourYears: yearOne + yearTwo * 3 };
}

export function estimate(raw: Partial<Inputs>): Estimate {
  const inputs = sanitise(raw);
  const { handsets, kids, minutes, outsidePct } = inputs;
  const outMinutes = Math.round(minutes * fullSetup.outgoingShare);
  const inMinutes = minutes - outMinutes;
  // On the Tin Can side every phone is a kid's, so all the chat is theirs.
  const outsideMinutes = Math.round((minutes * outsidePct) / 100);
  const canMinutes = minutes - outsideMinutes;

  // Party Line is a flat fee per Tin Can: it is needed for the first minute
  // of outside chat and costs the same for the last. Each kid keeps their
  // own number here; sharing one across the phones needs one plan (notes).
  const partyLines = outsidePct > 0 ? kids : 0;
  const tcMonthly = partyLines * tinCan.partyLine;
  const tcHardware = kids * tinCan.device;
  const tc = {
    key: 'tincan',
    name: tinCan.name,
    basis:
      kids === 0
        ? 'no kid handsets, so no Tin Cans'
        : partyLines > 0
          ? `${kids} × ${usd(tinCan.device)} + ${partyLines} × ${usd(tinCan.partyLine)} Party Line`
          : `${kids} × ${usd(tinCan.device)}; Can 2 Can only, no plan`,
    hardware: tcHardware,
    setup: 0,
    monthly: tcMonthly,
    ...years(tcHardware, 0, tcMonthly, tinCan.freeMonths),
    partyLines,
    perOutsideMinute: outsideMinutes > 0 ? tcMonthly / outsideMinutes : null,
  };

  const cmmHardware = hardwareCost.brain + hardwareCost.handset * handsets;
  const hardwareBasis = `${usd(hardwareCost.brain)} brain + ${handsets} × ${usd(hardwareCost.handset)}`;
  const carriers: Line[] = [];
  for (const p of providers) {
    const r = p.rates;
    if (!r) continue;
    const incoming = inMinutes * r.incoming;
    let outgoing: number;
    let basis: string;
    if (r.outgoingPlan) {
      const over = Math.max(0, outMinutes - r.outgoingPlan.minutes);
      outgoing = r.outgoingPlan.monthly + over * r.outgoingPlan.overage;
      basis =
        `${usd(r.number)} number + ${inMinutes} × ${usd3(r.incoming)} in + ` +
        `${usd(r.outgoingPlan.monthly)} plan with ${r.outgoingPlan.minutes} min out` +
        (over > 0 ? ` + ${over} × ${usd3(r.outgoingPlan.overage)} over` : '') +
        (r.e911 > 0 ? ` + ${usd(r.e911)} E911` : ', E911 included');
    } else {
      outgoing = outMinutes * r.outgoing;
      basis =
        `${usd(r.number)} number + ${inMinutes} × ${usd3(r.incoming)} in + ` +
        `${outMinutes} × ${usd3(r.outgoing)} out + ${usd(r.e911)} E911`;
    }
    const monthly = r.number + incoming + outgoing + r.e911;
    carriers.push({
      key: p.name.toLowerCase().replace(/[^a-z0-9]+/g, '-'),
      name: p.name,
      basis: `${hardwareBasis}; ${basis}` + (r.setup > 0 ? `; ${usd(r.setup)} setup once` : ''),
      hardware: cmmHardware,
      setup: r.setup,
      monthly,
      ...years(cmmHardware, r.setup, monthly),
    });
  }

  return { inputs, inMinutes, outMinutes, outsideMinutes, canMinutes, tinCan: tc, carriers };
}

/**
 * Every figure the page shows for a set of inputs, keyed the way the markup
 * names them in data-cost. The build renders these once; the browser script
 * writes the same keys back. One mapping, so the two cannot differ.
 */
export function cells(raw: Partial<Inputs>): Record<string, string> {
  const e = estimate(raw);
  const c: Record<string, string> = {
    handsets: String(e.inputs.handsets),
    kids: String(e.inputs.kids),
    minutes: String(e.inputs.minutes),
    inMinutes: String(e.inMinutes),
    outMinutes: String(e.outMinutes),
    outsidePct: `${e.inputs.outsidePct}%`,
    outsideMinutes: String(e.outsideMinutes),
    canMinutes: String(e.canMinutes),
  };
  for (const line of [e.tinCan, ...e.carriers]) {
    c[`${line.key}.basis`] = line.basis;
    c[`${line.key}.hardware`] = usd(line.hardware);
    c[`${line.key}.monthly`] = usd(line.monthly);
    c[`${line.key}.yearOne`] = usd(line.yearOne);
    c[`${line.key}.yearTwo`] = usd(line.yearTwo);
    c[`${line.key}.fourYears`] = usd(line.fourYears);
  }
  c['tincan.perOutsideMinute'] =
    e.inputs.kids === 0
      ? 'nothing, because there are no Tin Cans'
      : e.tinCan.perOutsideMinute === null
        ? 'nothing, because there is no outside chat to spread it over'
        : `about ${usd(e.tinCan.perOutsideMinute)} for each minute of outside chat`;

  // Four years is long enough for a hardware gap to close or not. Say which,
  // against the cheapest carrier shown.
  const cheapest = e.carriers.reduce((a, b) => (b.fourYears < a.fourYears ? b : a), e.carriers[0]);
  const diff = e.tinCan.fourYears - cheapest.fourYears;
  c['verdict.carrier'] = cheapest.name;
  if (e.inputs.kids === 0) {
    c['verdict'] = 'With no kid handsets there is no Tin Can to compare.';
  } else if (Math.abs(diff) < 1) {
    c['verdict'] = `Over four years the two come out level.`;
  } else if (diff > 0) {
    c['verdict'] = `Over four years Tin Can costs ${usd(diff)} more than ${cheapest.name} at these settings.`;
  } else {
    c['verdict'] = `Over four years Tin Can costs ${usd(-diff)} less than ${cheapest.name} at these settings.`;
  }
  return c;
}

export { checkedAt };
