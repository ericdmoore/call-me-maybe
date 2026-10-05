// Public rate cards checked 2026-10-05. USD, US local voice examples.
// Provider links are attribution only: no affiliate or referral payments.
// Update prices, checked date and sources together; registration is not a live test.
export const checkedAt = 'October 5, 2026';

export interface Provider {
  name: string;
  url: string;
  pricingUrl: string;
  summary: string;
  registration: 'yes' | 'unverified';
  billing: string;
  pros: string[];
  cons: string[];
  weUseThis?: boolean;
  number: string;
  incoming: string;
  outgoing: string;
  emergency: string;
  setup: string;
  sources: { label: string; url: string }[];
}

/** Attribution for the destination; UTMs alone do not measure our clicks/signups. */
export function track(url: string, content: string): string {
  const u = new URL(url);
  u.searchParams.set('utm_source', 'callmemaybe.cc');
  u.searchParams.set('utm_medium', 'referral');
  u.searchParams.set('utm_campaign', 'providers');
  u.searchParams.set('utm_content', content);
  return u.toString();
}

export const providers: Provider[] = [
  {
    name: 'VoIP.ms', url: 'https://voip.ms/', pricingUrl: 'https://voip.ms/pricing',
    summary: 'Start here for the project’s worked setup. This is the provider the project runs and tests against.',
    registration: 'yes', billing: 'prepaid', weUseThis: true,
    number: 'From $1.10/month', incoming: 'From $0.009/min', outgoing: 'From $0.005/min',
    emergency: '$1.50/month per enabled number; confirm activation charge',
    setup: 'Number setup from $0.40; prepaid funding also required',
    pros: ['Separate SIP sub-accounts keep the phone credentials apart from the portal login.', 'Per-number routing and a choice of regional servers.', 'Call Me Maybe can check the account balance and ring a handset when credit is low.'],
    cons: ['Check the actual destination rate: the advertised starting rate is not a quote for every US call.', 'Keep credit funded and configure E911 for the number and address you use.'],
    sources: [{ label: 'Rates and fees', url: 'https://voip.ms/pricing' }, { label: 'SIP setup', url: 'https://wiki.voip.ms/article/How_it_works' }],
  },
  {
    name: 'Telnyx', url: 'https://telnyx.com/', pricingUrl: 'https://telnyx.com/pricing/elastic-sip',
    summary: 'Worth evaluating if you want carrier APIs alongside your phone system. Registration is documented; this is not a Call Me Maybe certification.',
    registration: 'yes', billing: 'confirm account terms',
    number: 'From $1.00/month', incoming: 'From $0.0032/min', outgoing: 'From $0.005/min',
    emergency: '$1.50/month per enabled number', setup: 'Confirm number, verification and funding requirements',
    pros: ['Credential-based SIP registration is documented.', 'Secure trunking and T.38 fax support are listed without an extra feature charge.'],
    cons: ['Use SIP trunk pricing, not Voice API pricing; they are different products.', 'No live Call Me Maybe validation recorded here; verify incoming audio, keypad tones and outgoing caller ID.'],
    sources: [{ label: 'SIP rates and emergency calling', url: 'https://telnyx.com/pricing/elastic-sip' }, { label: 'Registration options', url: 'https://support.telnyx.com/en/articles/4245868-sip-connection-types' }],
  },
  {
    name: 'Flowroute', url: 'https://flowroute.com/', pricingUrl: 'https://flowroute.com/pricing-details/',
    summary: 'A metered carrier option for people comfortable configuring a SIP trunk. Check account funding and emergency fees together.',
    registration: 'unverified', billing: 'prepaid accounts documented',
    number: 'From $1.00/number (confirm monthly charge)', incoming: 'From $0.005/min', outgoing: 'From $0.00833/min (US lower 48 / Canada)',
    emergency: '$1.39 association + $1.50 mandatory US E911 fee/month per enabled number',
    setup: 'Confirm number setup and initial funding in the portal',
    pros: ['Published metered voice rates and number-management APIs.', 'Provider offers balance notifications and optional auto-replenishment.'],
    cons: ['Call Me Maybe currently labels Flowroute postpaid in its balance CLI. Do not rely on that result; use provider balance alerts.', 'Live compatibility with this configuration needs verification.'],
    sources: [{ label: 'Rates and fees', url: 'https://flowroute.com/pricing-details/' }, { label: 'Account funding', url: 'https://support.bcmone.com/flowroute-support/docs/set-up-auto-replenishment' }, { label: 'Recurring charges', url: 'https://support.bcmone.com/flowroute-support/docs/what-is-mrc-and-nrc' }],
  },
  {
    name: 'Callcentric', url: 'https://www.callcentric.com/', pricingUrl: 'https://www.callcentric.com/compare_rate_plans/',
    summary: 'Worth evaluating for a household that prefers a small outgoing-minute bundle. Choose incoming and outgoing plans separately.',
    registration: 'yes', billing: 'separate incoming / outgoing plans',
    number: '$1.95/month Pay Per Minute DID', incoming: '$0.015/min on that DID',
    outgoing: '$1.95/month North America Basic: 120 minutes; then $0.0198/min to US / Canada',
    emergency: 'Included in North America Basic for US / Canada addresses',
    setup: '$3.95 DID + $1.50 North America Basic setup',
    pros: ['Provider publishes an Asterisk PJSIP registration guide.', 'North America Basic includes E911 and 120 outgoing minutes to US, Canada and Puerto Rico.'],
    cons: ['An outgoing plan does not include an incoming number.', 'Live Call Me Maybe compatibility is unverified. The $1 Dollar Unlimited DID is a separate residential offer with limited geographic availability.'],
    sources: [{ label: 'Outgoing plans', url: 'https://www.callcentric.com/compare_rate_plans/' }, { label: 'Incoming number plans', url: 'https://www.callcentric.com/compare_did/' }, { label: 'Asterisk PJSIP setup', url: 'https://www.callcentric.com/support/device/asterisk/17_pjsip' }],
  },
];
