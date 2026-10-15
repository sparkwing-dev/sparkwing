# Data storage

Data routes use live controller capabilities. A job's `data.write` authority
names its exact run, job, attempt and generation. A run owner can upload only
the record kinds its award permits. Local publication uses the authorized
publication run. The controller derives the team, uploader, repository and ref
from persisted facts. Machine roots need an award before they can upload.

1. `POST /api/v1/data/upload` takes `{"digest":"<sha256>","size":N}`. The
   digest is lowercase SHA-256 and size is nonnegative. The controller reserves
   quota before signing a PUT for that digest and size. The response contains
   `upload_id`, `url`, `headers`, `expires_at`, `sha256` and `size`.
2. PUT exactly those bytes to the URL with the returned headers. Send only the
   signed headers to object storage. Keep the controller bearer private.
3. `POST /api/v1/data/commit` takes `{"upload_id":"<ticket>"}`. It resolves
   the original reservation, rechecks authority and verifies the physical
   bytes before committing existence and an uploader receipt. The response
   contains `digest` and `created_at`. A failed verification grants no receipt.

Signing failures release their reservations. A physical object left by an
interrupted commit grants no read authority. A later commit must verify its
bytes and pass the transaction's authority checks. Storage quota failures use
`quota_exceeded`. Bad bytes use `integrity_failure`. Private backend errors
return the common `internal` envelope.

`POST /api/v1/data/records` creates an authorized logical reference. It takes
`kind`, `pipeline`, `job`, `key`, `name`, `digest`, optional `manifest_digest`,
`members` and optional `expires_at`. A member contains `digest`, `path`, `size`
and `mode`. Record kinds are `output`, `manifest`, `memo`, `depcache`, `binary`
and `source_bundle`. The controller supplies each member's team and manifest
identity. It checks every primary, manifest and member digest before calling
the store's committing transaction. Job A's receipt never grants unrelated
job B authority to reference the same bytes.

`GET /api/v1/data/records` selects `kind`, `ref`, `pipeline`, `job`, `key` and
`name`. Optional `deadline` is an RFC3339 timestamp. Optional
`verified_default_only=true` narrows the lookup. Repeated or unknown selectors
fail with `bad_request`. A miss returns `{"found":false,"members":[]}`.
A hit returns public record metadata and authorized member metadata. It omits
private authority references, uploader identities and read-set snapshots.

`POST /api/v1/data/memo/lookup` takes `pipeline`, `job`, `hash` and optional
RFC3339 `deadline`. `POST /api/v1/data/depcache/lookup` takes `name`, `key`
and optional `deadline`. The store applies memo's own-ref/default fallback
and depcache's own-ref/PR-base/default order. Remote-cache credentials read
only verified default-branch entries. Missing or corrupt cache data is a miss.

`POST /api/v1/data/memo/copy` takes the memo selectors. It requires both
`data.read` and `data.write` and copies authorized output and manifest edges
to the claim's current attempt. The caller cannot choose the destination.

These requests accept only unique, known string fields. Memo responses carry
`found`, optional `output` and `manifest` digests, and public `members`.
Depcache hits carry public `record` metadata and the winning restore `source`.
Read-set snapshots and uploader identities stay private.

Write requests cannot choose a team, repository, ref verification flag or
provenance. Submitter-vouched writes use the sealed manual ref namespace.
Remote-cache credentials always require verified default-branch records and
cannot write. See [Data downloads](api.md#data-downloads) for byte reads and
presence queries.

`GET /api/v1/services` advertises `direct_data` when the authenticated upload
adapters are configured. `GET /api/v1/data/capabilities` reports upload and
download availability separately. An embedded controller supplies these
adapters through `WithDataController`. Missing wiring fails closed. The
signing and transaction contracts serve S3 and filesystem backends. Native
transfer and client integration must be verified before deployment.

## Verified byte transfers

The byte-storage components share `blob.Store` for binaries, source bundles,
outputs, artifact manifests, memo and dependency caches. `NewSignedStore` uses
an authenticated `DataSigner` for upload tickets, commits, downloads and presence
queries. `RecordStore` owns logical names and authorized reference edges.

The filesystem and S3 backends sign one-object transfers. A PUT binds the expected
size and SHA-256 to a staging object. Commit verifies stored bytes through EOF
and successful close before publishing an immutable `teams/<team>/cas/<sha256>`
object. Retrying an interrupted commit verifies an existing final object too.
Object metadata and a successful physical copy cannot establish a receipt.
The store rechecks live authority and settles the reservation before it
records existence and the exact uploader's receipt.

Downloads verify size and SHA-256 at EOF. Artifact staging fails on missing or
corrupt bytes. Memo becomes a miss and dependency caches run cold. HasMany reports
presence only within the caller's authorized read set.

Local remote-cache reads require explicit profile opt-in. They read authorized
verified-default-branch memo, artifacts and dependency caches, never write shared
records, and never fetch executable pipeline binaries.

Logs use a separate signed segment path and controller-proxied reads. Tagged
subjects distinguish job attempts from trigger, plan and eval units.

The `swcg` cache-grant protocol and `cache-grant-key` credential are removed.
Authenticated data routes use live capabilities. There is no compatibility
endpoint or cache-service fallback.

## Local cache reads and publication

`run --sw-cache-from PROFILE`, or the machine's `run.cache_from`, opts into
repo-bound reads under the user's existing authority. Eligible entries come from
verified default-branch runs. The interface exposes memo, artifact and depcache
reads. Pipeline binaries stay in the local binary cache.

`run --publish PROFILE` is the separate one-way path for a local run record,
results and logs under `trust_local_builds`. Publication failure is reported
independently and does not change the completed local run outcome. Source-bundle
intake and publication result-copy composition still need their owner bindings.
