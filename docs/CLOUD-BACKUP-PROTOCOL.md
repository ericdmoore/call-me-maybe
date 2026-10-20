# Cloud backup and recovery integration

The CLI supports device-code login and encrypted cloud uploads, alongside file/S3
backups and local restore. Customer-authorized `restore --from cloud` remains
integration work.

## On a source device

From the installed configuration directory, run as the backup service user:

```sh
sudo -u doorman /opt/call-me-maybe/bin/doorman login
sudo -u doorman /opt/call-me-maybe/bin/doorman account status
# Set BACKUP_CLOUD=true in .env. Preserve BACKUP_RECIPIENT and existing destinations.
sudo -u doorman /opt/call-me-maybe/bin/doorman backup run
```

Approve the displayed code at `https://login.dialdoorman.cc/device`, choosing the
source house. Login generates an X25519 transport key on that device and verifies
the code's public-key commitment before displaying it. It resumes an unexpired
pending ceremony. The one-shot service response cannot recover credentials after
a crash or network loss during successful polling; re-enrolment is then required.

Account state lives in `<state>/account/tokens.bin`, gzip JSON, mode 0600 in a
0700 directory. Gzip is only an accidental-display guard. State writes use atomic
replacement and fsync; a process lock serializes credential selection, rotation
and use. Run login as the same user as backups so both can read private state.
`--state` defaults to `/var/lib/doorman` and is also accepted by backup commands.

`doorman account rotate --capability backup` saves a pending successor before
using it. Requests use that successor and never retry the predecessor after an
ambiguous result. Reuse/revocation requires new enrollment without changing phone
services. Remote logout/revocation and protocol version negotiation remain
account-service integration work; deleting local files alone is not logout.

Opt in with `BACKUP_CLOUD=true`. Login alone does not upload household content.
The obsolete `BACKUP_CLOUD_TOKEN` setting is not used. Local/S3 delivery runs
independently. Cloud storage reports **stored**, never readback/decryption verified.
The account app shows stored metadata.

Encrypted in-flight bytes and the signed upload reservation persist in the
excluded account directory. A later run attempts completion first, finishes an
interrupted upload, then sends its new bundle. If a declaration response is lost
before its ID is saved, the server's one-house reservation may block retry until
expiry. No authenticated list/read request is used to bypass that boundary.

## Uploads

Use the enrolled device's `backup` capability in an Authorization bearer header.
It is write-only: never use it to list or download bundles or recovery material.
Continue local destinations when the service rejects an upload or is unavailable.

1. Encrypt the existing bundle format on the device. Read the ciphertext's exact
   length and its manifest creation time in Unix milliseconds.
2. POST `https://api.dialdoorman.cc/v1/backups/uploads` with JSON
   `{ "declaredBytes": 3500000, "createdAt": 1792454400000 }`.
3. On 201, read `uploadId`, `uploadUrl`, `uploadHeaders`, `expiresAt` and `maxBytes`.
   PUT ciphertext directly to `uploadUrl` with every returned upload header.
   In this contract these are exact `Content-Length` and `If-None-Match: *`.
   Do not forward the device Authorization header to object storage. Disable
   cross-origin redirects and never log the signed URL or response body.
4. POST `/v1/backups/uploads/complete` with `{ "uploadId": "the returned id" }`
   using the backup capability again. On 200, the returned bundle has `id`,
   `bytes`, `createdAt` and `receivedAt`. This confirms storage, not restoration.

An upload URL lives 15 minutes; the reservation lasts one hour. Retrying the PUT
can return 412 because the object is already present. Retry completion to learn
whether it was accepted; never remove the create-only condition or assume a 412
means your attempted bytes were accepted. A repeated completion is idempotent
while the record is retained. Cloud retention belongs to the service.

Useful refusals:

| HTTP | Error | Client behavior |
| --- | --- | --- |
| 401 | unauthorized, unknown, expired, revoked, reuse | Follow account-client reauthentication/rotation policy; do not loop. |
| 403 | capability | Wrong capability; do not retry with a broader credential. |
| 403 | no_backup_plan, held_in_grace | Report the allowance or hold; local backup continues. |
| 409 | upload_in_progress | A house already has a reservation; finish it or wait for its expiry. |
| 409 | not_uploaded | The service has not found the object; preserve the pending upload id. |
| 409 | size_mismatch | The object was refused; declare a new upload. |
| 410 | expired | The reservation expired; declare again. |
| 413 | too_large | Show `maxBytes` and preserve local copies. |
| 429 | rate_limited | Respect Retry-After. |
| 503 | entitlements_unavailable | Allowance could not be checked; retry later, never report Free. |

Keep credentials in the account client's private state directory. The backup
collector excludes the entire `<state>/account` directory, including future
files, as well as `<state>/backup`. Custom credential locations need an explicit
exclusion before they can be supported. Do not place account secrets in ordinary
configuration files collected as household state.

## Recovery work after uploads

The first factor is a printed recovery phrase. Passkey PRF and FIDO2 follow.
The phrase and plaintext backup identity never reach the hosted service; account
recovery alone cannot unlock a bundle. `internal/backup/recovery.go` contains an **experimental v0** printed-factor
primitive, not a released format or CLI command. It wraps the existing age
identity without changing the bundle recipient, and authenticates the expected
account, house and generation before returning a key. Size and scrypt-work limits
bound untrusted envelope inputs; errors never carry keys or phrases.

The provisional interoperability choice is one age envelope per factor over the
same generation-bound identity payload. Native age passphrase recipients cannot
be mixed with other recipient types in one age file; separate envelopes let
passkey/FIDO2 adapters follow without changing this inner payload. The phrase
must be cryptographically generated on the customer's device; printed-word/QR
encoding, receiver-key authorization and a confirmed generation transition still
need integration before this format can be called stable. No primitive here
uploads, stores or rotates recovery material by itself.

The next client implementation must test the complete sequence:

- Enroll the destination with its own transport key and scoped account tokens.
- Obtain a separate, expiring, house-scoped recovery authorization; upload
  credentials alone cannot gain reads.
- Retrieve ciphertext and the matching encrypted identity generation, then unwrap
  locally using the printed phrase. Check account, house, generation, recipient
  and ciphertext integrity before accepting the identity or writing files.
- Prepare and confirm a fresh phrase/envelope generation before retiring the old
  one. An interruption before confirmation must preserve the working old factor.
  Rotation cannot revoke a ciphertext copy someone already downloaded.
- Restore into an inactive staging root using the existing `restore --root` path;
  preserve the destination's independent account state. Activation and retirement
  of the source are separate, explicit operations.

The synthetic acceptance test must cover wrong account/factor, tampering,
interruption during generation confirmation, missing copies, unauthorized reads,
and restored file bytes/permissions. An operator retrieving a fixture for a
storage checksum check is useful evidence, but does not exercise customer
recovery authorization or replace a fresh-device rehearsal.
