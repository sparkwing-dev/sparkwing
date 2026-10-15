# Direct data uploads

The controller authorizes blobs and logical records through `/api/v1/data`.
S3 and filesystem backends implement the same signed-object contract. A runner
uses its current capability. Local publication uses run-bound publisher authority.
The signed object client receives only the returned transfer URL and headers.

This page describes the frozen data contract. Native signer, transaction and
backend integration require separate evidence. The shared wire client and its
transport tests do not establish those server guarantees.

<!-- design:begin -->

## Authorization

Repository identity, ref facts, uploader and read edges come from authenticated,
persisted state. Request fields select values to compare with that state.
Possession of a digest or another receipt from the same run grants no access.
Each signing, commit, record lookup and record write rechecks current authority.

A job upload binds team, run, job, attempt and generation. Owner-unit receipts
and non-job publication receipts cover binary/source bundles. Imported publication
attempts may attach output and manifest records only for their bound published job.
The server assigns provenance independently of caller-supplied metadata.

## Transfer messages

All names use snake_case. The shared messages live in `internal/controllerwire`.

| Operation | Request | Response |
| --- | --- | --- |
| `POST /api/v1/data/upload` | `DataUploadRequest` | `DataUploadTicket` |
| `POST /api/v1/data/commit` | `DataCommitRequest` | `DataReceipt` |
| `POST /api/v1/data/download` | `DataDownloadRequest` | `DataSignedTransfer` |
| `POST /api/v1/data/has-many` | `DataHasManyRequest` | `DataPresence` |
| `GET /api/v1/data/records` | `lookup` and URL-escaped `selector` JSON | Matching record, memo or depcache result |
| `POST /api/v1/data/records` | `DataRecordWriteRequest` | Committed record or copied memo hit |

`DataUploadRequest` carries `authority`, `identity`, `digest` and `size`.
The digest is a lowercase SHA-256 string. The controller reserves quota before
signing. The ticket contains `id` and a `transfer` with `url`, `headers`,
`expires_at`, `digest` and `size`.

The client sends the exact bytes and signed headers to that one-object URL. It
then commits with `authority` and `ticket_id`. Commit verifies physical size and
digest, repeats current authorization and publishes the immutable object and
receipt. A reservation or an uploaded temporary object alone is not a receipt.
Retries retain the upload idempotency key and the exact commit body.

Downloads select `authority` and `digest`. The server checks the live readable
set before returning a transfer. The client verifies size and digest through EOF.
Closing a partial reader does not establish a successful verified read. A signed
URL remains a temporary credential until its expiry. Revocation stops new signing
and commits but cannot revoke a URL already issued.

The object client refuses reusable authorization, cookie and proxy credentials
in signed transfer headers. It does not forward the controller bearer to storage.
Redirects cannot move those headers to another destination.

`HasMany` reports readable live blobs only. Unauthorized and absent digests are
indistinguishable. A deadline or storage budget failure remains an error rather
than successful absence.

## Logical records and reuse

Record lookup selects `lookup=record`, `memo` or `depcache` with the matching
selector payload. `verified_default_only` is explicit. Responses retain authorized
member metadata, read edges and provenance. A client cannot submit response read
sets or ref facts as authorization proof.

Record writes tag `operation=commit` or `copy_memo` and contain exactly the matching
payload. The store checks record, member and copy edges in the mutation transaction.
A memo hit copies output and manifest records and installs read edges atomically.

Memo scope contains repository, ref, pipeline, job and hash. Depcache restores the
own ref, verified pull-request base, then verified default branch. Memo has no
pull-request-base fallback. A submitter-vouched ref writes under `manual:<ref>`.
It cannot overwrite a verified ref. Missing or corrupt memo becomes a miss,
depcache runs cold, and required output or artifact reads fail loudly.

## Local cache reads and publication

`run --sw-cache-from PROFILE`, or the machine's `run.cache_from`, opts into
repo-bound reads under the user's existing authority. Eligible entries come from
verified default-branch runs. The interface exposes memo, artifact and depcache
reads. Pipeline binaries stay in the local binary cache.

`run --publish PROFILE` is the separate one-way path for a local run record,
results and logs under `trust_local_builds`. Publication failure is reported
independently and does not change the completed local run outcome. Source-bundle
intake and publication result-copy composition still need their owner bindings.

Logs use their own signed upload and controller-proxied read path. They are not
blob data downloads. See [Controller HTTP API](api.md), [Observability](observability.md)
and [Storage limits](limits.md).

<!-- design:end -->
