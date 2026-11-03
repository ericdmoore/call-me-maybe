# Optional recorded-audio activity: contract preparation

Status: draft protocol only. `internal/usewire` does not record activity, read
configuration, send reports, advertise capabilities, or enable sharing. There
is no implemented HTTP endpoint or owner-facing setting. This work does not
complete optional device-use reporting.

The proposed invitation is:

> Help make Doorman better. Share a small daily summary of recorded-audio use
> to help us decide what to improve. We don't include phone numbers, contacts,
> audio or transcripts. Sharing is optional, off by default, and you can turn
> it off anytime.

An enrollment, purchase, terms acceptance, or receiver capability is not consent.
A future owner-controlled grant must be explicit, versioned, and tied to the
current ownership/source epoch. Both epochs are independent random UUIDs,
never account IDs, phone numbers, or hardware identifiers. A changed owner or
source invalidates the grant. Turning sharing off must stop collection and
uploads and discard unsent optional summaries; turning it on again starts a
new consent epoch. Receiver shutdown must also stop uploads.

## Proposed transport

`GET /v1/sync/usage/capabilities` and `POST /v1/sync/usage` are reserved draft
paths, not live routes. Keep optional reporting separate from existing strict
snapshot, queue, and receipt payloads. Missing, disabled, malformed or unknown
capabilities mean no upload. An implementation must authenticate the existing
device binding, check current ownership and explicit consent, and apply bounded
rate limits before accepting reports. Capabilities alone cannot enable sharing.

[schema.json](../internal/usewire/schema.json) closes every object to extra
fields. `ReadCapabilities` and `ReadBatch` additionally reject duplicate keys,
concatenated JSON values, payloads above 2,048 bytes, and invalid semantics.
The validator does not expose rejected input in errors.

A batch contains one to seven cumulative UTC daily snapshots, each with a
positive revision and `recordedAudioProgress` count from 1 through 1,000. Only
today and the preceding six UTC days are accepted. Dates must be unique and
epochs distinct. A receiving implementation must replace the same authenticated
source/consent/day only with a higher revision, accept exact retries without
addition, reject conflicting equal revisions, and never sum cumulative retries.
Do not send zero-use days: absence means unknown, not no use.

## Meaning and integration still required

The proposed metric counts private recorded-audio playback invocations that
produce positive playback progress, once per invocation. It does not count
installed books, menu entries, answered calls, preview plays, or public-line
listening. It does not establish that a person heard or finished anything.
The audio-player hook is not implemented in this change. Existing call event
records are not a substitute for authoritative playback progress.

Before implementation is accepted, add and verify:

- Local owner controls and schema-backed CLI configuration, all default off,
  with README, command help, schema, manual and model-facing docs in agreement.
- A bounded local daily accumulator outside the call-critical path, with no
  network operation, journal scan, or admission dependency during calls.
- Receiver capability negotiation, ownership/consent revocation, retry and
  cumulative-replacement semantics, retention and staff aggregate reports.
- Tests for off, missing capability, receiver shutdown, storage/network failure,
  stale ownership, replay, disable/re-enable and transport privacy.
- A tagged, checksum-verified device release and an explicit household opt-in
  rehearsal. Never copy a development binary onto a deployed box.

No household has opted in through these draft types. No device was changed.
