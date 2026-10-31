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
`quota_exceeded` (HTTP 429). The message names the free or paid tier, or an
operator override, the limit, current usage including live reservations, and
the requested growth. Add funds to move an automatic free Cloud tier to paid.
Operator overrides still apply after payment. Contact support@sparkwing.dev
for a higher limit. Monthly quota resets at the next UTC calendar month.
Deleting unneeded runs/artifacts frees retained storage, but does not reset
cumulative quota usage. Event-size refusals name their configured limit and
the support contact too. Bad bytes use `integrity_failure`. Private backend errors
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
`verified_default_only=true` selects the repository's persisted verified default
ref. The shared client accepts that ref selection while requiring the repository,
kind, name, pipeline, job and key to match the request. Malformed query escaping,
repeated selectors and unknown selectors fail with `bad_request`.
A miss returns `{"found":false,"members":[]}`.
A hit returns public record metadata and authorized member metadata. It omits
private authority references, uploader identities and read-set snapshots.
Verified-default selections include `verified_default:true`. The controller checks
the persisted branch evidence before returning this observation. The shared cache
reader requires it without receiving delivery keys, source SHAs or uploader facts.
It grants no byte access; each download rechecks the current credential and edges.
Misses omit the observation. With no ref selector, the store selects from the
credential's authorized refs and accepts only persisted verified-default evidence.
The `ref` selector and response metadata use `manual:<writer>:<ref>` for
records an unverified run wrote and the plain ref for records a verified GitHub
App run wrote. The writer is the submitting principal, the member who armed a
schedule, an admin-armed default-branch schedule, the parent or demoted run, or `actions`, as
[caching](caching.md#who-shares-a-cache) lists. Each lookup requires the
matching read grant. Every run holds grants for its own scope and the verified
default branch, and a verified pull request also for its base branch. Selecting a plain ref never
inherits a manual write's scope.

`POST /api/v1/data/memo/lookup` takes `pipeline`, `job`, `hash` and optional
RFC3339 `deadline`. `POST /api/v1/data/depcache/lookup` takes `name`, `key`
and optional `deadline`. The store applies memo's own-ref/default fallback
and depcache's own-ref/PR-base/default order. Remote-cache credentials read
only verified default-branch entries. Missing or corrupt cache data is a miss.
An omitted cache deadline retains the caller's context and cancellation. An
explicit deadline bounds both the lookup and verification of downloaded bytes.

`POST /api/v1/data/memo/copy` takes the memo selectors. It requires both
`data.read` and `data.write` and copies authorized output and manifest edges
to the claim's current attempt. The caller cannot choose the destination.

These requests accept only unique, known string fields. Memo responses carry
`found`, optional `output` and `manifest` digests, and public `members`.
Memo hits also carry `reader`, the authenticated caller's attempt fence. The
server checks that fence against its current binding, and the shared client
rejects a different reader or invalid result metadata. Misses omit the reader
and result references. Copy destination always comes from the authenticated claim.
Depcache hits carry public `record` metadata and the winning restore `source`.
Read-set snapshots and uploader identities stay private.

Write requests cannot choose a team, repository, ref verification flag or
provenance. Unverified writes use the writer's sealed manual scope.
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
subjects distinguish job attempts from trigger, plan and eval units. Application
retries keep their claim and give each attempt its own log stream. A finished
attempt's writer can upload its tail during that attempt's seal grace, including
after the next attempt starts or seals. The controller checks the submitted
attempt's stored team, job, claim generation, holder and credential. Each attempt
must seal successfully before a finished run's aggregate logs are complete.

Cloud source initialization downloads the run's sealed source bundle before
exchanging its bootstrap. The bootstrap's `source.read` grant permits only that
bundle through the download route. It grants no output, cache, secret or upload
access. After exchange, execution uses the awarded claim and its current attempt.

## Local cache reads and publication

`run --sw-cache-from PROFILE`, or the machine's `run.cache_from`, opts into
repo-bound reads under the user's existing authority. Eligible entries come from
verified default-branch runs. The interface exposes memo, artifact and depcache
reads. Pipeline binaries stay in the local binary cache.

`run --publish PROFILE` is the separate one-way path for a local run record,
results and logs. Publication failure is reported
independently and does not change the completed local run outcome. Source-bundle
intake and publication result-copy composition still need their owner bindings.
