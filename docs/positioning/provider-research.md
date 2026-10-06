# Provider research and link attribution

Checked October 5, 2026. Primary sources only. Prices are USD examples for US
local voice; destination, geography, account terms, taxes and surcharges can
change the result. A documented registration method is not proof of a working
Call Me Maybe deployment. No live trunks were tested in this review.

## Price basis

| Provider | Number | Incoming | Outgoing | Emergency service | Source |
| --- | --- | --- | --- | --- | --- |
| VoIP.ms | From $1.10/month; setup from $0.40 | From $0.009/min | From $0.005/min; destination-specific | $1.50 per enabled DID/month; confirm activation charge | [Rate card](https://voip.ms/pricing?utm_source=callmemaybe.cc&utm_medium=referral&utm_campaign=providers&utm_content=research-rates) |
| Telnyx | From $1/month | From $0.0032/min | From $0.005/min | $1.50 per number/month | [SIP trunk rates](https://telnyx.com/pricing/elastic-sip?utm_source=callmemaybe.cc&utm_medium=referral&utm_campaign=providers&utm_content=research-rates) |
| Flowroute | From $1/number; confirm monthly amount for selected DID | From $0.005/min | From $0.00833/min, US lower 48/Canada | Rate card lists $1.39 association and $1.50 mandatory fee per E911-enabled US DID/month | [Pricing details](https://flowroute.com/pricing-details/?utm_source=callmemaybe.cc&utm_medium=referral&utm_campaign=providers&utm_content=research-rates) |
| Callcentric | Pay Per Minute DID $1.95/month, $3.95 setup | $0.015/min | North America Basic $1.95/month + $1.50 setup, 120 minutes; then $0.0198/min US/Canada | Included with North America Basic for US/Canada addresses | [Outgoing plans](https://www.callcentric.com/compare_rate_plans/?utm_source=callmemaybe.cc&utm_medium=referral&utm_campaign=providers&utm_content=research-outgoing), [DIDs](https://www.callcentric.com/compare_did/?utm_source=callmemaybe.cc&utm_medium=referral&utm_campaign=providers&utm_content=research-incoming) |

The site's light-use example uses 100 incoming + 100 outgoing minutes, one
number and E911: VoIP.ms **from $4.00**, Telnyx **from $3.32**, Callcentric
**$5.40** with the named incoming/outgoing combination. Since October 6, 2026
it is a table rendered from `site/src/data/costs.ts` with hardware (a Pi 5
and one WP826), the monthly figure, year 1 (hardware, listed one-time fees
and twelve months), year 2 and a four-year total, with one Tin Can on Party
Line as a row. Monthly figures exclude setup and taxes. Starting
rates are illustrative lower bounds, not quotes. No Flowroute all-in example
is shown because the selected-number recurring charge should be confirmed.
Callcentric's $1 Dollar Unlimited number is a different, geographically
limited residential plan; it should not silently replace a widely available
DID in an apples-to-apples example. Prepaid deposits are cash needed to start,
not an additional monthly consumption charge.

## Cost calculator basis (added October 6, 2026)

The `/providers/` page also prices a household by the year, so the carriers
and Tin Can can be read side by side. The arithmetic is
`site/src/data/costs.ts`; it renders the default household at build time
and recomputes in the browser from four inputs: handsets, whether the Tin
Cans share one number or have one each, minutes a month (entered for the
house or as an average per handset), and a slider from "zero Tin Can
calls" to "all Tin Can calls" for the share of chats with other Tin Cans. It shows hardware, listed one-time fees, monthly service, year 1,
year 2 and a four-year total.

- Default: three handsets, a kitchen phone for the adults and one for each
  of two kids. Hardware is a Raspberry Pi 5 plus one Grandstream WP826 per
  handset at the `hardware.ts` prices, board and handsets only; two
  dropdowns offer every brain and every standalone (Wi-Fi or desk) handset
  on that list, a price range costing at its upper end. Tin Can is one
  device per handset.
- Default minutes: two kids at 30 minutes a week is 260 minutes a month
  (52/12 weeks); the kitchen phone's calls are on top. Minutes are split
  half outgoing and half incoming. The slider divides the whole figure,
  since on the Tin Can side every phone is a kid's.
- Year 1 is hardware, listed fees and twelve months of service, less Tin
  Can's free first month. Year 2 is service alone. Four years is year 1
  plus three of year 2.
- One number with E911 at each provider's advertised US starting rate.
  Flowroute is omitted for the same reason as the light-use example.
- Tin Can: $100 per device on the US storefront; Party Line $9.99/month per
  Tin Can with its own number, first month free; Can 2 Can (other Tin Cans
  and 911) is free with every device. Several Tin Cans can be linked to one
  number on one Party Line, in which case all of them ring and share a
  contact list and voicemail. The table counts one Tin Can per handset,
  each with its own number by default; a dropdown prices the shared-number
  case at one Party Line. Sources:
  [product and plans](https://tincan.kids/products/tin-can?utm_source=callmemaybe.cc&utm_medium=referral&utm_campaign=providers&utm_content=research-tincan),
  [Can 2 Can vs Party Line](https://faq.tincan.com/t/35yp5d7/what-is-the-difference-between-can-2-can-and-the-party-line-subscription?utm_source=callmemaybe.cc&utm_medium=referral&utm_campaign=providers&utm_content=research-tincan),
  [one number per device, or shared](https://faq.tincan.com/t/x2yp5wt/will-each-device-in-my-home-have-a-separate-number?utm_source=callmemaybe.cc&utm_medium=referral&utm_campaign=providers&utm_content=research-tincan).
  The FAQ says Party Line "costs vary by country"; the price used is the US
  storefront's. An annual Party Line price appears in third-party reviews
  but not on a Tin Can page, so it is not used. Tin Can checked October 6,
  2026; the carriers were not rechecked that day.
- The feature table (`site/src/data/compare.ts`) reads Tin Can's product
  page for quiet hours ("Quiet hours fully disable both incoming and
  outgoing calls"), voicemail ("record your own custom greeting"),
  speakerphone and 2.4 GHz Wi-Fi, and the FAQ for Can 2 Can and linked
  numbers. Intercom, conference calling and smart-home integration are not
  mentioned on those pages and are marked no. The Call Me Maybe column
  marks approved-contacts-only calling "partly": incoming yes, outgoing no
  in the stock dialplan, and marks roadmap items "planned" rather than
  claiming them. No Tin Can was used.
- The slider is the share of the chats that are with other Tin Cans,
  default 50%. It changes only the Tin Can row, and only at "all Tin Can
  calls": Party Line is a flat fee, needed for any outside calling. A Call Me Maybe
  house pays the same carrier minute to a Tin Can friend as to anyone else,
  and reaches that friend only if their family has Party Line.

## Recommendations and evidence boundaries

- **VoIP.ms:** first choice for following this project's existing runbook and
  its implemented balance client. This is an implementation/support-path
  recommendation, not a claim of cheapest or most reliable carrier.
- **Callcentric:** candidate for households preferring a small minute bundle.
  [PJSIP registration guide](https://www.callcentric.com/support/device/asterisk/17_pjsip?utm_source=callmemaybe.cc&utm_medium=referral&utm_campaign=providers&utm_content=research-registration)
  supports the registration claim; Call Me Maybe behavior still needs a live test.
- **Telnyx:** candidate for carrier APIs and documented credential registration.
  [Connection types](https://support.telnyx.com/en/articles/4245868-sip-connection-types?utm_source=callmemaybe.cc&utm_medium=referral&utm_campaign=providers&utm_content=research-registration).
  Corrected the old site's link from Call Control pricing to SIP trunk pricing.
- **Flowroute:** evaluate with explicit funding and emergency fees. Its
  [auto-replenishment guide](https://support.bcmone.com/flowroute-support/docs/set-up-auto-replenishment?utm_source=callmemaybe.cc&utm_medium=referral&utm_campaign=providers&utm_content=research-funding)
  and [recurring-charge guide](https://support.bcmone.com/flowroute-support/docs/what-is-mrc-and-nrc?utm_source=callmemaybe.cc&utm_medium=referral&utm_campaign=providers&utm_content=research-billing)
  contradict the repo's blanket postpaid claim. Registration is marked “verify”
  rather than copying the old site's unsupported certification. Use provider
  alerts while the runtime balance-classification issue remains open.

Do not confuse a provider's SMS, fax or transcription offering with features
implemented in Call Me Maybe for that provider. A carrier may offer all three
without the application integrating them.

## UTM convention

`site/src/data/providers.ts` already centralized card links through `track`.
This change retains that helper and routes comparison and evidence links
through it too:

- `utm_source=callmemaybe.cc`
- `utm_medium=referral` (formerly `web`)
- `utm_campaign=providers`
- `utm_content=<provider>-<placement>` (source links include a stable index)

`URL.searchParams.set` preserves unrelated query parameters and fragments and
replaces existing UTM values without duplicates. These tags provide attribution
to the destination provider. They do not give Call Me Maybe first-party click,
signup or purchase data. No click tracker was added and no affiliate arrangement
is implied. Existing configuration endpoints, API URLs, SIP hostnames and
copied vendor instructions must remain literal; do not append marketing tags
to operational addresses. New editorial provider links in this research file
also carry the same tags.

For future rows: record source, date, geography, currency, price units, E911,
setup and account-funding caveats together. Do not turn an old “from” rate
into a fixed family bill. Recheck before publishing; choose a maintenance
cadence separately if ongoing review is wanted.
