# The house's edge inbox

The public face of the house number's texting, and the only place the
carrier's API key lives. VoIP.ms calls this Worker with each text; the box
pulls from it with `doorman inbox`; replies go out through it. Design and
reasons: `.plans/s19-edge-inbox/`.

## Shortcut contact uploads

The Shortcut supplies the contact **and its complete phone-book selection**.
It never joins, changes, or waits on the SMS contact shelf. Cancelling its
picker makes no request; an empty destination header is an error, never a
House default. The house imports as soon as it processes the upload and
returns a receipt result over the API. This path sends no SMS replies.

Three endpoints use a dedicated `Authorization: Bearer ddc_<random>` key:

- `GET /api/v1/phonebooks`: the key's phone-book choices as a `phonebooks`
  array of display strings (for example, `House`, `Norah`).
- `POST /api/v1/contact-imports`: raw UTF-8 vCard body, `Content-Type:
  text/vcard`, and required `X-Phonebooks: house,norah`. `Idempotency-Key`
  may be a generated UUID; reuse it only for retries of the same file and
  selection. Returns HTTP 202 and `{status:"queued", receipt, message}`.
- `GET /api/v1/contact-imports/<receipt>?wait=20`: same key, optional
  0–20 second wait, returns `queued`, `saved`, or `failed` with `message`.
  A receipt is `saved` only after the house reports a completed write.
  When the house is offline, it stays `queued`. Results expire with the
  queue's seven-day acknowledged-message retention.

Create 32 random bytes encoded as base64url (43 characters), prefix with
`ddc_`, and keep the token only in the person's Shortcut/private setup file.
The `CONTACT_UPLOAD_KEYS` Worker secret is a JSON object keyed by SHA-256
hex digests of complete tokens:

```json
{
  "<sha256-of-token>": {
    "house": "example",
    "sender": "+15125550101",
    "phonebooks": ["house", "norah"]
  }
}
```

The operator binds the key to an existing person and their permitted
`messages.toml` phone-book words; these are a ceiling, and the house checks
its own permissions again before every import. The upload body cannot
choose a sender, house, action, or SMS caption. Remove a digest to revoke a
key; retain other entries when rotating one person's key. Upload keys cannot
pull messages, send SMS, acknowledge results, or read another key's receipts.

Uploads are limited to 1300 KiB, three cards, 100 numbers, and 30 new
requests per hour / 30 pending requests per person. Only bounded names and
numbers reach the queue. Retries do not rewrite the selection or receipt.

Apply migration `0003_contact_uploads.sql`, deploy the Worker, then deploy
the compatible `doorman inbox` binary before distributing a key. The new
consumer requests `contact_uploads=1` and returns terminal results in its
acknowledgement; old consumers cannot pull or acknowledge uploads. The
consumer keeps uploads out of its SMS seen-id set so an interrupted result
acknowledgement can safely repeat the idempotent directory write.

The phone workflow is: get permitted books → choose multiple → combine
with commas → upload the shared file and selection → check the receipt →
show its message. See RUNBOOK “Share a contact with an iPhone Shortcut”.

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
`too-many-contacts`, `media-too-large`, `unsupported-media`, or `media-gone`
when the carrier's media host answers that the file has expired or been
removed). A `media` value the Worker cannot use at all (a host other than the
carrier's, more than three entries) queues the text with `unsupported-media`
and no attachment count, so the sender still hears from the house; an
unexpanded `{MEDIA}` placeholder counts as no attachment. Transient media
fetch failures (network errors, 5xx, 408, 429) return HTTP 503 without queuing
a partial message; enable the carrier's callback retry setting. Successful
duplicate callbacks return `ok` without fetching expiring URLs again.

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


## Diagnosing contact delivery

The Worker emits one structured `edge.request` event per completed request.
Use Cloudflare Workers Logs or `npx wrangler tail --format json` (from `edge/`)
and select the **console log objects** whose `event` is `edge.request`. Raw
tail envelopes may still include platform request metadata; do not save or
paste their URLs, headers, or full contents into an issue.

For an authenticated callback, `media_field` and `timestamp_field` distinguish
`missing`, `empty`, `unexpanded`, and `present` parameters. `media_count`,
`contact_count`, and `contact_error` show what survived parsing. `outcome`
distinguishes `queued`, `invalid-media`, `invalid-message`, `media-unavailable`,
and duplicates. `message_ref` is a hash of the house and message ID, so carrier
retries can be correlated without logging the supplied ID. `request_ref`
identifies one attempt. No captions, contact names/numbers, credentials, media
URLs, or exception strings are included in these application events.

An `internal-error` includes the fixed `stage` that failed (for example,
`callback-save` or `inbox-pull`) and returns HTTP 500, allowing retry. Automatic
invocation logs are disabled because the callback path contains a credential;
query-string redaction is also enabled. Sanitized application events retain
route, method, status, duration, and failures for every request.

For a fresh `Add to: House, Norah` test with one vCard:

- `media_field=missing`: verify and save `&media={MEDIA}` in the carrier callback.
- `media_field=empty`: the callback supplied no attachment URL; inspect carrier
  delivery and look for a separate callback carrying the card. Separate
  instructions are supported locally once the card reaches the queue.
- `media_field=unexpanded`: the carrier sent the literal placeholder.
- `invalid-media`: the list did not meet the HTTPS carrier-host and count limits.
- `media-unavailable`: a download failed; the Worker returns 503 without queuing.
- `queued` with a `contact_error`: an attachment arrived but could not be imported.
- `queued` with nonzero `contact_count`: check the house inbox outcome next.
- `duplicate-with-new-media`: the same ID was previously accepted without media.
  The new attachment is not imported; inspect callback order and test with a
  **new message**, since both the edge and house suppress handled IDs.

The local inbox pairs cards with subsequent instructions from the same sender
and house number. After 15 seconds of silence it offers a two-minute choice
window before defaulting to permitted House. Instructions without a pending
card request a fresh card; they never select a destination for a future card. Diagnostics do not
make a carrier forward unsupported files. Verify incoming vCard delivery
before relying on MMS; a secure upload fallback is a separate feature.

For a message already in this house's queue, an operator can call
`GET /h/:house/inbox/diagnose?id=<numeric-message-id>` with the usual inbox
bearer token. This read-only diagnostic calls the carrier's `getMMS`, verifies
message ID, incoming direction, DID and sender, then calls `getMediaMMS`.
It returns fixed statuses and attachment counts only. It neither imports nor
requeues messages, downloads media, or returns credentials, contact data or
media URLs. Responses are bounded, time-limited and not cached. Carrier
reference: <https://voip.ms/m/apidocs.php> (sign-in required).

- `message_found=true`, zero `record_media_count` and zero
  `retrievable_media_count`: the carrier APIs expose no retained attachment.
- Nonzero carrier counts with zero `callback_media_count`: investigate callback
  forwarding; a recovery path may be possible using carrier media retrieval.
- `message_found=false` or a failed lookup is inconclusive; do not interpret it
  as proof that the carrier discarded an attachment.

Live status, 2026-10-05: incoming iPhone vCard delivery is still failing through
VoIP.ms. Saved callback settings include the media placeholder, and the carrier
records received MMS, but both API lookups above succeeded with zero media
for the tested messages. Local shelf tests do not establish end-to-end
readiness. Carrier tracing and a successful live import remain required.
