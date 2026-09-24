# The house's edge inbox

The public face of the house number's texting, and the only place the
carrier's API key lives. VoIP.ms calls this Worker with each text; the box
pulls from it with `doorman inbox`; replies go out through it. Design and
reasons: `.plans/s19-edge-inbox/`.

    npm install
    npx wrangler d1 create callmemaybe-edge        # once; paste the id into wrangler.jsonc
    npm run db:init                               # the messages table
    npx wrangler secret put VOIPMS_API_USERNAME
    npx wrangler secret put VOIPMS_API_PASSWORD
    npx wrangler secret put HOUSE_MIDBURY_DID             # digits only
    npx wrangler secret put HOUSE_MIDBURY_CALLBACK_TOKEN  # openssl rand -hex 24
    npx wrangler secret put HOUSE_MIDBURY_INBOX_TOKEN     # openssl rand -hex 32
    npm run deploy

Then on VoIP.ms, the DID's SMS URL callback is
`https://edge.callmemaybe.cc/h/midbury/sms/<callback token>?from={FROM}&to={TO}&message={MESSAGE}&id={ID}`
and the box's `.env` gets `INBOX_URL=https://edge.callmemaybe.cc/h/midbury`
and `INBOX_TOKEN=<inbox token>`. The VoIP.ms API IP allow-list must admit
the Worker's egress, which is Cloudflare's address space — and it calls
VoIP.ms over IPv6, from `2a06:98c0:3600::/40` (inside Cloudflare's published
`2a06:98c0::/29`), so the IPv6 range is the one that matters. Ask the Worker
what the carrier sees: `GET /h/<house>/inbox/whoami` with the inbox token.
Or `0.0.0.0` to disable the restriction.
