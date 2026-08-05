// SIP trunk providers, as data rather than markup.
//
// NOT AFFILIATE LINKS. docs/SUSTAINABILITY.md rules out referral programs for
// VoIP providers: the runbook gives unbiased provider advice, and a referral
// fee puts a thumb on that scale. These carry UTM parameters only — analytics,
// no money, no incentive to prefer one. If that ever changes it has to be
// disclosed on the page as prominently as the hardware affiliate disclosure.
//
// Rates and auth options move. Nothing here quotes a price; every card links
// to the provider's own pricing page instead, because a stale number on a page
// about honesty is worse than no number.

export interface Provider {
  name: string;
  /** Where to start an account. */
  url: string;
  /** Their own pricing page — always link out rather than quoting figures. */
  pricingUrl: string;
  /** One line: who this is for. */
  summary: string;
  /** Verified against the one requirement that matters. */
  registration: 'yes' | 'unverified';
  billing: 'prepaid' | 'postpaid' | 'both';
  pros: string[];
  cons: string[];
  /** Set when this is what the project itself runs on. */
  weUseThis?: boolean;
}

/** UTM on every outbound link, so we learn what people actually pick. */
export function track(url: string, content: string): string {
  const u = new URL(url);
  u.searchParams.set('utm_source', 'callmemaybe.cc');
  u.searchParams.set('utm_medium', 'web');
  u.searchParams.set('utm_campaign', 'providers');
  u.searchParams.set('utm_content', content);
  return u.toString();
}

export const providers: Provider[] = [
  {
    name: 'VoIP.ms',
    url: 'https://voip.ms/',
    pricingUrl: 'https://voip.ms/rates',
    summary:
      'What this project is built and tested against. The worked example in the runbook is theirs.',
    registration: 'yes',
    billing: 'prepaid',
    weUseThis: true,
    pros: [
      'Sub-accounts are free and separate the Pi’s credentials from your portal login.',
      'Many DIDs can ride one registration, which is what makes several numbers on one box cheap.',
      'Per-DID routing and granular POP selection.',
      'E911 available in most US and Canadian markets.',
    ],
    cons: [
      'Prepaid. If the balance reaches zero the phone stops ringing with no error anywhere — budget alerting matters more here than with a postpaid account.',
      'The portal is functional rather than pleasant, and support is ticket-based.',
      'No SMS thread UI; texting is an API, not an inbox.',
    ],
  },
  {
    name: 'Telnyx',
    url: 'https://telnyx.com/',
    pricingUrl: 'https://telnyx.com/pricing/call-control',
    summary:
      'The developer-first option. Good documentation and a real API if you want to automate around the edges.',
    registration: 'yes',
    billing: 'both',
    pros: [
      'Credential (registration) auth as well as IP auth, so it fits the no-open-ports design.',
      'Documentation is genuinely good, which matters when something is wrong at 11pm.',
      'Modern portal, sane API, programmable messaging if SMS becomes a project.',
    ],
    cons: [
      'Identity verification before you can buy numbers — reasonable, but not a five-minute signup.',
      'Oriented at businesses building products, so some of the surface is irrelevant to one house.',
    ],
  },
  {
    name: 'Flowroute',
    url: 'https://www.flowroute.com/',
    pricingUrl: 'https://www.flowroute.com/pricing/',
    summary:
      'A long-standing wholesale carrier. Unglamorous, stable, and it does the one thing this needs.',
    registration: 'yes',
    billing: 'postpaid',
    pros: [
      'Registration-based trunking is supported and well documented.',
      'HD voice (G.722) end to end, which the wideband prompt pack is built for.',
      'Postpaid, so a zero balance cannot silently take the line down.',
    ],
    cons: [
      'US-focused; look elsewhere for international numbers.',
      'Aimed at carriers and integrators, so the onboarding assumes you know what a trunk is.',
      'Fewer consumer conveniences than the others.',
    ],
  },
  {
    name: 'CallCentric',
    url: 'https://www.callcentric.com/',
    pricingUrl: 'https://www.callcentric.com/pricing/',
    summary:
      'The long-standing choice for home Asterisk. Listed because people ask; not yet verified against this setup.',
    registration: 'unverified',
    billing: 'both',
    pros: [
      'Registration by default and a long history with self-hosted Asterisk.',
      'Per-DID routing and E911.',
    ],
    cons: [
      'Nobody has run the runbook against it yet — see issue #5. A wrong example is worse than no example, so this card stays marked unverified until someone has.',
    ],
  },
];
