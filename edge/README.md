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
`https://edge.callmemaybe.cc/h/midbury/sms/<callback token>?from={FROM}&to={TO}&message={MESSAGE}&id={ID}&media={MEDIA}&timestamp={TIMESTAMP}`
and the box's `.env` gets `INBOX_URL=https://edge.callmemaybe.cc/h/midbury`
and `INBOX_TOKEN=<inbox token>`. The VoIP.ms API IP allow-list must admit
the Worker's egress, which is Cloudflare's address space — and it calls
VoIP.ms over IPv6, from `2a06:98c0:3600::/40` (inside Cloudflare's published
`2a06:98c0::/29`), so the IPv6 range is the one that matters. Ask the Worker
what the carrier sees: `GET /h/<house>/inbox/whoami` with the inbox token.
Or `0.0.0.0` to disable the restriction.

This URL requests all six fields in the carrier's [documented SMS/MMS callback](https://wiki.voip.ms/article/SMS-MMS#Configuring_the_SMS.2FMMS_service).
Leave the separate SMS/MMS Webhook URL empty; this Worker accepts the GET
callback, not the alternative POST/JSON webhook.

`timestamp={TIMESTAMP}` is stored in D1 and returned by inbox pulls as
`provider_timestamp`, separately from the Worker's UTC `received_at`. The
carrier's format and timezone are unspecified, so the value is preserved
verbatim, never parsed or used to order messages. Missing values, an unexpanded
`{TIMESTAMP}`, values over 64 characters, and values containing ASCII control
characters become an empty string without rejecting the message. Existing
rows also get an empty string. Duplicate callbacks preserve the first accepted
message and both timestamps. The field follows the queue's existing retention:
acknowledged rows older than seven days are pruned on subsequent acknowledgements.
Existing home inbox binaries safely ignore the added response field.

For an existing deployment, run `npm run db:migrate` before `npm run deploy`,
then append `&timestamp={TIMESTAMP}` to the carrier callback. This timestamp
update requires no home-service restart.


## Contact-card attachments

The callback's `media={MEDIA}` supplies the carrier's comma-separated media
URLs. The Worker downloads and parses up to three UTF-8 vCards (2.1, 3.0 or
4.0) before queuing the message. It permits HTTPS media on `voip.ms` or its
subdomains, rechecks every redirect, limits each file to 1300 KiB and each
fetch to 15 seconds. Files are read in Worker memory only.

D1 stores the caption, attachment count, and at most 100 distinct name/number
pairs. Original files, signed media URLs, photos, email addresses and arbitrary
vCard fields are neither stored in the queue nor returned to the house. There
is no raw-media endpoint. A pull returns `contacts: [{name, number}]` and a
`contact_error` code alongside the normal message fields. Pulls are limited to
ten messages so contact metadata stays within the local client's 1 MiB limit.
Names are limited to 200 characters. Phone numbers are reduced to an optional
plus sign and digits; the house applies its own country-code normalisation.

All attachments must parse before any contacts are queued. Permanent failures
queue an empty contact list and a fixed error code (`invalid-card`,
`too-many-contacts`, `media-too-large` or `unsupported-media`). Transient media
fetch failures return HTTP 503 without queuing a partial message; enable the
carrier's callback retry setting. Successful duplicate callbacks return `ok`
without fetching expiring URLs again.

The local inbox receives only structured data. It checks sender permissions
and all requested destinations, validates the names and numbers again, then
writes the selected directories. Parsing at the edge grants no permission:
unknown or unauthorised senders never import a contact or receive a reply.
The Worker processes attachments before those local permission checks.

For an **existing edge database**, apply the migration before deploying the
new Worker:

```sh
npm ci
npm run db:migrate
npm run deploy
```

For a new database, `npm run db:init` applies both the base schema and the
migrations. Include `&media={MEDIA}&timestamp={TIMESTAMP}` in the DID's callback in the VoIP.ms portal,
enable callback retries, then update the box's binary, configure the phone book
words in `messages.toml`, run `doorman check`, and restart
`doorman-inbox.service`.
The directory service also needs the updated binary and a restart to display
received cards. See RUNBOOK, "Share a contact to a phone book".

VoIP.ms documents the media callback but does not document incoming vCard
support. Before relying on this flow, share a fictional contact from the
actual sending phone to the DID and verify that the queued message contains
the expected structured contacts and the house replies with the import count.
Message Center alone may not display the original file. A successful SMS alone
does not prove MMS contact-card delivery. If the carrier strips the
attachment, the importer asks for the missing card rather than claiming it
was saved. An edge-only test can verify structured capture; automatic imports
also require the updated local inbox and directory services. Pause the old
inbox consumer during an edge-only test to avoid it acknowledging the message
before the new importer can process it. Reference: <https://wiki.voip.ms/article/MMS>.

Local checks (Node.js 22.18+): `npm run check && npm test`. Tests use a local
SQLite database and mocked media responses; they never contact a carrier or
deploy the Worker.
