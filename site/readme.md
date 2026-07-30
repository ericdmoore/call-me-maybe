callmemaybe.cc
==============

Astro site
- it explains the system design and features
- it embeds the suggested hardware affiliate links

## Running it

```bash
cd site
npm install
npm run dev        # http://localhost:4321
npm run build      # → dist/
npm run preview    # serve the built output
```

## The design

Art Deco, because the project's own metaphor is a hotel lobby with a doorman
and the period's geometry is the obvious match. Black lacquer, aged brass,
warm ivory, jade for admittance and oxblood for dismissal.

Everything is CSS or small inline SVG — no bitmaps and no webfonts, so nothing
loads and nothing can silently fall back to a wrong face. The sunburst behind
the hero is a `repeating-conic-gradient`, the stepped corners are `clip-path`,
the pilaster fluting is a `repeating-linear-gradient`, and the doorman is built
from circles and trapezoids rather than traced path data.

Type is period-correct rather than fashionable: **Futura** (1927) for display,
set uppercase with wide tracking the way a Deco poster would, a warm
high-contrast serif for reading, and mono with tabular figures for digits. All
system stacks. A licensed display face — Poiret One, Cinzel — could be added
through `@fontsource` later if it earns its download.

Two themes: the night lobby, and the same lobby with the lights on. The second
is not an inversion; it is cream stock with darkened brass, the way Deco
posters were actually printed. Tokens live on `:root`, are redefined under
`prefers-color-scheme`, and again under `[data-theme]` so a viewer's explicit
toggle wins in both directions.

## The keypad

The hero is a working keypad, not an illustration, and it behaves the way the
real lobby behaves: six digits, then a verdict. Ten seconds of silence gets
you dismissed, matching `FIRST_DIGIT_TIMEOUT_MS`. A wrong extension gets the
bouncer's actual line — which teaches the product better than a paragraph
does.

One extension is recognised. The keys carry letters the way a real telephone
keypad does, and that is the entire hint. Someone who thinks in dialpads will
find it; everyone else just learns what the bouncer sounds like.

## Deploying

Cloudflare Workers, serving `dist/` as static assets. There is no `main`
script in `wrangler.jsonc` because the site has no server-side work to do.

Workers rather than Pages for one concrete reason: `wrangler pages` has no
domain subcommand, so on Pages the custom domains could only be attached
through the dashboard and would live nowhere in the repo. As `routes` in
`wrangler.jsonc` they are reviewable and reproducible, which is how the rest of
this project treats configuration.

```bash
npm run cf:preview     # build, then serve through the real Workers runtime
npm run deploy         # build, then publish
npm run cf:whoami      # confirm which account the credentials resolve to
```

`cf:preview` is worth using before a deploy: `astro preview` serves from Node,
while `wrangler dev` runs the actual Workers runtime, so asset routing and the
404 handling behave the way production will.

### Credentials

Either `npx wrangler login` for interactive OAuth, or an API token in the
environment — the token is preferable, since it is the same credential CI would
use:

```bash
CLOUDFLARE_API_TOKEN=...   # legacy CF_API_TOKEN still works, with a warning
CLOUDFLARE_ACCOUNT_ID=...
```

Scopes: **`Account → Workers Scripts → Edit`** is enough to deploy to
`workers.dev`. Attaching a custom domain creates a DNS record, so that
additionally needs **`Zone → DNS → Edit`** on the zone. A token with only read
scopes deploys nothing and fails with an authorization error.

### Domains, in order

`workers_dev` is on, so every deploy gets a working
`callmemaybe.<subdomain>.workers.dev` URL regardless of domain state. Verify
there first.

The `routes` block is commented out on purpose. Both zones are delegated to
Cloudflare and propagated, but Cloudflare reported `callmemaybe.cc` as
`pending` while its own verification caught up — and attaching a domain whose
zone is pending gives you the worst possible outcome: a deploy that reports
success and a hostname that does not resolve. Uncomment once the zone reads
`active`:

```bash
# check without the dashboard
curl -s -H "Authorization: Bearer $CLOUDFLARE_API_TOKEN" \
  "https://api.cloudflare.com/client/v4/zones?name=callmemaybe.cc" \
  | python3 -c 'import json,sys; print(json.load(sys.stdin)["result"][0]["status"])'
```

`dialdoorman.cc` is deliberately not in `routes`. It is active and could serve
today, but two hostnames both serving the site contradicts the `<link
rel="canonical">` in `Base.astro`. If it is a spare rather than a mirror, give
it a Redirect Rule to `callmemaybe.cc`.

### If the domain was somewhere else before

`callmemaybe.cc` came from Route 53. Changing nameservers means any record
Cloudflare's import scan missed is simply gone, and MX, SPF, DKIM, and TXT
verification records are the usual casualties — the symptom is mail vanishing
silently rather than erroring. Worth confirming the record list in the
dashboard if that domain ever carried mail.

## Output mode

`output: 'static'`. Nothing here needs a server — the keypad runs in the
browser and the hardware list is build-time data — and static on Workers still
serves from Cloudflare's edge, so SSR would buy nothing but a cold start.

If a page later does need a server (a live status readout, a form), the change
is small and now unambiguous, because the platform is settled:

```bash
npx astro add cloudflare      # installs @astrojs/cloudflare, sets output
```

Then add `main` to `wrangler.jsonc` pointing at the generated Worker entry.
Nothing about the design or the components has to change.

`build.inlineStylesheets: 'always'` makes each built page one self-contained
file, which is handy for previewing a design with no server at all.

## The 404

`src/pages/404.astro`. A 404 is the bouncer turning someone away, so it uses
the line the project already has — "Good day." — and the doorman renders in
oxblood via the same `data-state` the keypad drives. `not_found_handling:
"404-page"` in `wrangler.jsonc` is what makes Cloudflare serve it.

## Content that lives elsewhere

- `public/llms.txt` is copied from the repository root, so the site serves the
  same orientation file the repo ships. If you change one, change both.
- `src/data/hardware.ts` duplicates the README's hardware table. See the note
  at the top of that file — it should be generated, and is not yet.
