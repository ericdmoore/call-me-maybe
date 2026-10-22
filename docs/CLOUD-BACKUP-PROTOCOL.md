# Cloud backup and recovery integration

The CLI supports device-code login and encrypted cloud uploads, alongside file/S3
backups, local restore, and printed-phrase cloud recovery into an inactive inspection
directory. Activation is a separate operation.

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
| 401 | box_removed | The owner removed this box. Forget its credentials, stop uploading, report it as stopped (not failed), and keep local copies; only a new login resumes. |
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

## Printed-phrase recovery

The two printed sheets have different jobs. **Account sign-in codes** are eight
single-use codes, each two groups of four characters; enter one in the browser at
`login.dialdoorman.cc/recover`. The **Backup decryption phrase** is one phrase of
eight groups of four characters; enter the entire phrase at the CLI's hidden
prompt. Account sign-in does not decrypt backups, and a backup phrase does not
sign you in. Updating the printed layout changes neither credential.

This version uses `doorman recovery`; `doorman restore` still operates on local
bundles. Passkey PRF and FIDO2 unlock factors follow. An account passkey approves
access to encrypted material; it does not decrypt the backup.

On the enrolled source, preserving its existing backup identity:

```sh
doorman recovery authorize --state /var/lib/doorman
doorman recovery setup --state /var/lib/doorman
# Print or safely save the private HTML file whose path is shown.
doorman recovery confirm --state /var/lib/doorman
```

`authorize` displays a code for `https://login.dialdoorman.cc/backup-recovery`.
Approve with the account owner's or co-owner's passkey. The separate grant lasts
30 minutes and never expands the ordinary upload credential. Setup writes the
printable sheet privately before preparing either hosted copy. Confirmation asks
for the **entire new phrase**, using hidden terminal input. It downloads the
prepared envelope and proves it opens locally before sending only its ciphertext
hash to the confirmation endpoint. The previous phrase remains current until then.

On a replacement, log in independently to the **same house** first. Never copy a
source device's account directory or transport key. Then:

```sh
doorman recovery authorize --state /var/lib/doorman
doorman recovery restore --state /var/lib/doorman --root /srv/backup-inspection/run-1
# Enter the OLD printed phrase at the hidden prompt.
# Print or safely save the NEW sheet at the displayed private path.
sudo doorman recovery confirm --state /var/lib/doorman
# Enter the NEW phrase. Root restores the recorded owners and groups.
```

Restore chooses the newest retained bundle, downloads ciphertext with the separate
grant, checks the account/house/generation and age recipient, and authenticates the
whole encrypted stream. Encrypted bundles are bounded at 200 MB and expanded
archives at 512 MiB. Duplicate/unsafe paths, account credentials and the standalone
backup identity are refused. The new root must not already exist; all files are
staged beside it, verified for bytes, modes, owners and groups, and fsynced before
an atomic directory rename. A resumed confirmation checks an already published
root and refuses extra or altered files. Required users/groups must exist.

No service is started, no installed configuration is overwritten, and no source
device is retired. The restored identity is **not** installed as the destination's
live backup identity. Keep the fresh sheet for later recovery; activation requires
an explicit, separate procedure. Root-run confirmation preserves the account
user's ownership of credential state so ordinary backup jobs can continue.

For controlled noninteractive use, `--phrase-file` accepts a regular file with no
symlink and no group/other permissions. Never put the factor in command arguments,
logs, chat, source control or shell history. HTML sheets are mode 0600 and remain
in the excluded account directory until their owner chooses where to retain them.
The command cannot prove that a human printed a sheet; confirmation must follow
actually saving or printing it.

`doorman recovery status` reports generation IDs and verified-copy counts.
`doorman recovery cancel` abandons an unconfirmed local operation, preserving the
current generation; it refuses an already confirmed sheet. Retry the same command
after a transient failure. Pending ciphertext and operation metadata are durable;
a lost confirmation response is reconciled with the current generation on retry.
Reauthorize after expiry. Do not delete account state to clear a pending operation.

The phrase is 160 uniformly random bits, displayed as eight groups of four
characters from `ABCDEFGHJKLMNPQRSTUVWXYZ23456789`. Spaces, hyphens and case are
ignored during entry. Its QR link places the canonical phrase after `#phrase=`;
the landing page reads it locally, clears the fragment from history, and forbids
network connections. Merely scanning the QR does not consume it. Account sign-in
recovery codes remain separately managed; keep them with the backup sheet.
Rotating the hosted envelope cannot revoke copies downloaded earlier, and account
recovery alone cannot unlock a backup.

### Wire contract: recovery v1

All JSON responses are private/uncached. Request bodies are capped at 32 KiB.

1. Save a fresh random 256-bit secret as `dd_recovery_<base64url-without-padding>`
   before POSTing `{secret}` to `/v1/login/backup-recovery/start`, authenticated
   with the backup capability. The reply contains `userCode`, `userCodeSalt`,
   `requestExpiresAt` (epoch milliseconds), and `interval: 5`. Compute and verify
   the 60-bit receiver-key code using the enrollment algorithm before showing it.
   A repeated start with the same secret recovers its existing request. Requests
   last 15 minutes, with at most six new requests per device per hour.
2. Poll `/v1/login/backup-recovery/poll` with the recovery secret as bearer.
   `202` means pending, `410` expired; `200` returns `{id, account, house, box,
   expiresAt}`. Check all scope fields and expiry. Device revocation, removal of
   the house or loss of the approver's managing role invalidates the grant.
3. POST `/v1/backups/recovery/status` with that same grant for `{account, house,
   box, generations, bundles}`. Generations describe `{id, predecessor, recipient,
   sha256, bytes, state, healthyCopies}`; states returned here are pending/current.
   Bundles contain `{id, bytes, createdAt}` newest first.
4. `/prepare`: `{id, predecessor, recipient, envelope}`. `id` is a new UUID,
   predecessor is the current generation UUID or null for first setup; envelope
   is standard base64. It returns `{generation}` only after both immutable copies
   match SHA-256 and byte count. First setup needs recovery protection entitlement;
   rotation of retained material does not. `/material`: `{id}` returns `{account,
   house, generation, envelope}` after independently checking/repairing copies.
   Either verified copy allows recovery. First material/download access per grant
   records an account security notification.
5. `/confirm`: `{id, sha256}` returns the current `{generation}`. Two healthy copies
   are required for the initial transition. A compare-and-swap retires the exact
   predecessor and promotes the pending generation; retries of a committed
   transition succeed. `/cancel`: `{id}` cancels only pending generations, with
   repeat cancellation allowed. Competing or stale transitions return
   `409 generation_conflict`. The service never receives a phrase or derived key.
6. `/download`: `{id}` for a bundle returns `{id, bytes, downloadUrl}`. The signed
   GET is valid for 60 seconds. Send no account/grant header to storage, refuse
   redirects, and check the exact byte count before authenticating the ciphertext.

A generation is native age v1 with a single scrypt recipient, work factor 18,
containing UTF-8 JSON:

```json
{"format":"doorman-recovery-identity-v1","scope":{"account":"<account UUID>","house":"<house UUID>","generation":"<generation UUID>"},"identity":"<age X25519 secret identity>"}
```

This example is structural, not a real key. Unknown fields/trailing JSON, wrong
scope/recipient, excess size/work, and any stream authentication failure are
rejected. The service limits envelopes to 16 KiB; clients cap plaintext identity
payloads at 4096 bytes. Future factors use separate envelopes over the same
scope-bound payload: native scrypt recipients cannot mix with other age recipient
types in one file. No decryption primitive alone proves the real enrollment,
printing and restore workflow; perform a fresh-device rehearsal before relying
on recovery.
