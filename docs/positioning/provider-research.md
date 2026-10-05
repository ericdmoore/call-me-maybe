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

The site's examples use 100 incoming + 100 outgoing minutes, one number and
E911: VoIP.ms **from $4.00**, Telnyx **from $3.32**, Callcentric **$5.40** with
the named incoming/outgoing combination. All exclude setup and taxes. Starting
rates are illustrative lower bounds, not quotes. No Flowroute all-in example
is shown because the selected-number recurring charge should be confirmed.
Callcentric's $1 Dollar Unlimited number is a different, geographically
limited residential plan; it should not silently replace a widely available
DID in an apples-to-apples example. Prepaid deposits are cash needed to start,
not an additional monthly consumption charge.

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
