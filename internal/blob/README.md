# Blob storage

`NewSignedStore` uses a controller `DataSigner` for upload tickets, commits,
downloads and presence queries. The caller supplies authority in memory.
`TransferClient` sends only the signed headers to the object URL and refuses
redirects. It supports S3 and filesystem signing through the same HTTP path.

All physical keys are relative to the configured storage root. For
`--cache-blob-store=s3://bucket/controller`, CAS blobs, artifact members and
manifests, source bundles, pipeline binaries, staged uploads and logs live under
`controller/`. Records remain database rows referring to those bytes. S3 copy
sources, signed downloads (including CloudFront), log listings and deletion use
that same prefix. Set bucket measurement to the same URL and scope IAM object
permissions to `controller/*`. Log lifecycle rules cover
`controller/logs/<class>/`. CloudFront uses an empty origin path because its
signed paths include the prefix. The filesystem backend anchors every key to
its configured data directory through one directory handle.


Uploads check the signed digest and size, consume and verify the source, then
commit the ticket. The returned receipt must match the signed uploader and
digest. Downloads verify size and SHA-256 at EOF. Reading the expected number
of bytes and closing is insufficient. Consumers must read EOF and check errors.
Cancelled uploads stop before ticket commit. Downloads retain their caller's
context through EOF verification and fail if it is cancelled during a read.
`HasMany` carries the caller's deadline and rejects unsolicited result digests.
The controller remains responsible for filtering its results by the live read set.

`OutputData.Put` checks the shared 16 MiB output cap and the exact job or imported
publication binding before upload. It creates a `KindOutput` record after the
verified receipt. `ResultRecordKey` uses `Name=result` and a scope key containing
run, attempt and generation, alongside repo, ref, pipeline and producer job.
Empty output is an explicit
blob and record. The store still rechecks liveness and reference authority in
the record transaction.

`OutputData.Read` requires an authorized output record lookup and consumes its
bytes under the lookup budget. It returns bytes only after EOF verification and
successful close. Missing records and missing objects both fail. Memo reads use
the same bounded output reader. Hosts must attach the resulting record key in
their result transition. `DataController.BindData` supplies the authenticated
signer and record store. Production host construction must supply that binding.
The common host output publisher calls `OutputData.Put` after checking SDK JSON
and its exact-attempt result key.

`AuthorizeUpload` and `AuthorizeEdge` operate on server-resolved facts. They do
not authenticate a bearer or query the store. Call them inside the transaction
that checks liveness, blob existence and receipt ownership. Owner and publication
writes need `Authority.Upload`, because an attempt cannot represent those fences.
Job receipts require the exact job, attempt, generation, principal and capability.
User remote-cache reads have no job attempt. Their server-derived `memo_scope`
edges require the same repository and verified record ref. They authorize only
outputs, manifests, memo and depcache. Owner upload bindings cannot use this path.

Memo and depcache namespaces encode each component separately. Manual refs carry
an explicit trust tag, distinct from a verified ref whose literal name starts
with `manual:`. `ResolveWriteKey` derives the repo and ref from authority. The
record transaction also checks the pipeline and job against the sealed plan.

`OpenRemoteCache` requires a profile, repository-bound user authority, a byte
store and a record store. Its lookups set `VerifiedDefaultOnly`, which requires
the record owner to resolve the verified default branch from sealed evidence.
The client also checks returned evidence and verifies the bytes. A memo lookup
without a matching record becomes a miss. Memo and depcache failures while
reading a referenced digest remain errors. Missing objects return `ErrNotFound`.
The local key's ref is not a remote selector. Remote lookups validate their
logical key fields and leave the verified default ref to the server, including
when the local reader has no ref.
Caller cancellation remains an error. Memo lookups use `MemoRecordName=output`,
shared with the writer and persisted memo lookup. A hit must pass output-size and manifest/member agreement
checks before it grants artifact access.
Artifact reads are limited to the output, manifest, member and depcache digests
of accepted cache lookups.

The dependency archive backend lives in `internal/blob/depcache`. Hosts construct
it with their bound clients and install it through `depcache.WithBackend`.
The executor cache layer uses the artifact binding for signed-data restores and
refreshes it after successful execution before saving. A refused binding runs
cold or skips the save. A download is verified before archive extraction publishes
files. Without an injected backend, dependency caches use local files.
The SDK and host share `internal/depcachekey` for workspace-contained lockfile
reads and full SHA-256 keys. The common executor hashes captured lockfile bytes
through that same key function. Candidate inputs outside the workspace, including
escaping symlinks, disable caching. The protocol's Go resolver is `go-modules`.
`ExtractArchiveStaged(ctx, reader, dir)` consumes the gzip trailer before
publishing, so checksum errors and truncated trailers preserve the destination.
Cancellation during extraction also preserves it. The common executor restores
through `depcacheblob.RestoreArchive`, sharing verification and staged extraction
with local and remote archive backends. A failed restore runs cold
and can replace the failed archive after successful work.

The archive acceptance checks join signed filesystem and S3 transfers to SQLite
upload reservations, receipt settlement and record transactions. They reopen the
store before restoring, reject forged repository/ref/attempt selectors, and
preserve destination files after equal-length corruption or missing objects.
A failed body leaves its cache record unchanged. A successful cold execution
can publish a replacement archive. Authentication and host construction remain
contract fixtures in these checks.

Record transactions, read-set construction, unique-byte accounting, GC and
retention belong to `internal/store`. Controller authorization and upload tickets belong
to the controller and capability packages.

`ObjectBackend.VerifyManifest` streams the selected tenant's CAS bytes against
members already in the commit request. Decoder memory depends on that submitted
metadata, including its worst-case JSON escaping, rather than object size.
Whitespace runs outside strings consume constant memory, including when reading
admitted records. Unknown fields and metadata disagreements fail integrity checks.
Successful admission requires the original raw length, digest, verified EOF and
successful close. There is no manifest byte-size cap.

`storage.BlobManifests` supplies separate verification and admitted-member read
methods to `Tenant.BlobRecords`. The record transaction authorizes the manifest
first, verifies the submitted list, then checks each member's existence and
current authority before mutation. `ReadManifest` streams admitted metadata for
record lookups. These physical reads open no nested database transaction.
`Server.WithDataObjects` installs the adapter for authenticated record operations.

The filesystem and S3 manifest boundaries generate file metadata and reject
tenant substitution, corrupt bytes, truncation and malformed members. The SQLite
record boundaries reject forged metadata and borrowed same-run upload receipts,
then admit independently verified member uploads and read them after reopening.
Record fixtures, writers and store checks share `ResultRecordKey`.

Artifact capture lives in `internal/artifacts`.
`nodehost.ArtifactRecords.Stage` stages authorized manifest members.
The common host artifact publisher calls `Capture` with its current storage
binding and the final result key. It writes the shared `Manifest` format, which
the real store verifies before publishing member edges. Its `Data` value binds
the byte store, record store and authority to the same job. Capture uploads each
file even if another principal stored identical bytes, because an existence
answer does not establish an upload receipt. It commits the manifest only after
all uploads succeed. The stager looks up an authorized record, validates its
admitted member metadata,
and verifies the manifest bytes and each member through EOF and Close before
replacing that member's destination file.

Artifact records use `KindManifest` and the exact-attempt `ResultRecordKey`
layout. A memo copy uses that same layout for its destination manifest.
Attempt and generation also stay in `Record.Source`.
`Manifest` defines the manifest bytes, including each file's path, digest, size
and mode. Memo reads check those bytes against the record's members with the
package's private decoder. Staging (`nodehost.ArtifactRecords.Stage`) never
decodes the manifest body: it takes members from the admitted record metadata,
checks their paths and digests, and verifies the manifest bytes exist.

Publication component checks exercise output and artifact writes through signed
filesystem and S3 transfers with imported attempt identities. Forged uploader
selectors fail before upload. Refused record updates return errors and preserve
the preceding records, readable output and local files. Record authority is a
contract fake. Controller intake, local provenance, `trust_local_builds` and
the CLI's separate publication result still require their owning integrations.

Memo hits are materialized by the store's `ReconcileNode` transaction, which
owns result edges, authority checks and the terminal lifecycle event. Missing or
corrupt bytes become a miss; authorization and claim-fence failures remain errors.
A host cannot turn a failed record copy into successful execution.
The host resolves a fresh storage binding after execution before publishing
artifacts, because an application retry advances the upload attempt.

`depcacheblob.NewRemoteReader` adapts an opted-in `RemoteCache` to dependency
archive restores under an explicit repository and lookup budget. It uses
`RemoteCache.Depcache` to resolve verified default-branch records and
`RemoteCache.Artifact` to read only the accepted digest. The download must reach
verified EOF and close successfully before extraction replaces local files.
`depcache.WithRemoteReader` checks local archives first and uses that reader
after a local miss. Saves remain on local disk because the remote reader has no
write method. W3/W9 still need to install this binding from the selected cache
profile. Cloud and hosted-job writes continue through the separate signed-data
backend.

The remote archive acceptance checks drive signed filesystem and S3 uploads,
default-ref lookup, verified downloads and local restore/save. Generated cases
refuse forged record scopes, preserve destinations after missing or corrupt
bytes, and restore local saves while the remote object remains missing. Logical
records and authentication use contract fakes until controller binding is ready.

`NewFilesystem` and `NewS3` implement `capability.DataSigner` and `ObjectBackend`.
The controller passes an authorized `ObjectToSign` after reserving quota.
PUT keys are `teams/<team>/pending/<ticket>` and bind the expected size and SHA-256.
GET keys are `teams/<team>/cas/<sha256>`. Logs cannot use this signing surface.
The controller supplies the expiry from the shared limits. Both backends refuse
data downloads lasting longer than sixty seconds.

The filesystem backend serves its configured endpoint using an injected signing
key. It verifies the complete upload before publishing the staging file. S3 PUTs
sign the content length and checksum. Commit reads and verifies staging bytes
through EOF and successful close, then copies with the verified source ETag
and a destination create-only condition. It verifies the final bytes too.
Both backends require this ticket's staged bytes even if the final object already
exists, and verify the final object before adopting it. Missing staging is an
error. Before settlement, retries retain and reverify staging. After settlement,
the controller reauthorizes and replays the persisted receipt without calling
physical publication again. Staging cleanup follows settlement.
Filesystem reads remain subject to the client's EOF verification.

Both backends expose `SignLogPut` for the separate tagged-log upload path.
It returns the URL and its required signed headers. These PUT-only slots expire
after fifteen minutes and grant no log download access. The filesystem path
enforces the shared segment or manifest size bound and reuses the immutable
writer. The first payload wins, an identical retry succeeds, and different bytes
cannot replace it.
S3 signs `If-None-Match: *` because future slots have no known payload digest or
length. The batch and uploader carry that header through the PUT. S3 refuses
every existing object with HTTP 412, including an identical retry. After a
transfer error, the uploader asks the controller's `VerifyUpload` route to reauthorize the
writer and verify that exact object's stored size and SHA-256. Only a successful
verification permits the record commit to proceed. `logseg.uploadVerifier`
performs the bounded object read after the controller supplies the run's
persisted retention class. Cancellation, mismatched bytes and failed close
remain errors. A precondition failure alone never proves matching bytes.
The controller route and upload admission for unknown future payloads still
need integration.
`Get` opens a log object only for a controller-side authorized segment read.
The S3 backend opens exact log keys and refuses objects beyond the shared
segment or manifest size bound. The committed reader verifies each persisted
segment before sending bytes. Missing or corrupt objects remain read failures.

`S3.LogClassReady` checks the bucket's lifecycle configuration for the batch
signer's callback. It requires an enabled prefix rule with the class's days.
Rules limited by tags or object size cannot establish expiry for the whole
class. The backend reads the rules and leaves their configuration to the operator.

`Filesystem.ExpireLogs` removes log objects whose file age reaches their stored
class. The controller calls it after renewing its maintenance lease when this
backend is configured. Identical uploads preserve the file age.
The sweep includes retired classes, ignores upload temporary files and leaves
CAS bytes to their separate reachability rules. Cancellation and filesystem
errors return a count of objects already removed and an error.
`LogClassDays` owns canonical class parsing for both backends and log key builders.
The controller still owns record checks, maintenance scheduling and egress accounting.

An object commit does not create a live blob or a receipt. The controller must
reauthorize the immutable upload ticket, call `CommitObject`, and commit the blob
row and exact uploader receipt in the store transaction. Only afterward may it
delete the pending object. GC calls `DeleteBlob` after the store's reachability
and tombstone decision. Provenance stays on records because identical bytes may
be uploaded by principals with different trust.

Both backends implement `store.BlobDeleter` through `DeleteBlob`. The collector
passes a tenant and digest after rechecking reachability under its maintenance
lease. Before each physical delete it rechecks the live lease and the same
collecting tombstone. S3 observes the object's ETag and deletes with `If-Match`.
A changed ETag or failed conditional delete preserves the tombstone for retry.
The backend derives that tenant's CAS key, and an absent object succeeds
on replay. A process paused after the last check can still race publication,
including identical S3 bytes with the same ETag. Required reads refuse missing
objects, and verified readers refuse incomplete bytes. The old controller output pruner and bucket-measurement expiry
callback are removed. The controller supplies its configured backend to the
shared leased maintenance pass. Standalone S3 construction installs this backend.
Standalone controllers install either backend through `Server.WithDataObjects`.
That constructor also installs physical manifest verification for terminal results.
Authenticated data bindings use the controller's configured replica identity for
upload settlement. Local-controller host construction remains separate.

The obsolete `internal/teamblob` object-key builder is removed. Log team validation
lives beside the tagged log keys. Tenant records own usage and reachability.

The filesystem and S3 collection acceptance boundaries generate twelve pairs of
tenant graphs, including identical digests, cycles, old referenced bytes and
references added after tombstone marking. An independent graph walk checks
which bytes remain. Interrupted collection before and after physical deletion
replays without losing reachable objects. The filesystem proof uses real files.
The S3 proof uses the real signer and SDK with a fake object service. Both use
real SQLite collection transactions and seeded reference graphs. They do not
replace authenticated controller or native placement evidence.

Artifact staging and dependency-cache restore close each verified stream before
publishing the downloaded files. A close failure returns an error and preserves
the destination for that download.
Artifact staging uses its caller's context for record lookup, downloads and
file publication. It adds no separate budget. Cancellation before replacement
preserves the existing file.
Capture checks the exact uploader receipt for each file and the manifest, then
checks the committed record's key, digest and source identity before returning it.
Dependency-cache saves check uploader authority before creating the archive. They
check the returned receipt and committed record against the expected digest, key
and exact uploader. Output and dependency-cache writes cancelled after upload
return an error before committing a record.
Archive spools and filesystem upload temporary files report cleanup errors.

Remote cache construction carries the authenticated team and full
`DataBinding.Authority` in `RemoteCacheOptions`. Record lookups and signed byte
reads retain that exact authority. The returned record must match the selected
team. The local ref never selects the remote default branch.

Remote host construction must supply the live data binding. Local builds keep
their machine's binary cache.

`bincache.SourceData` uploads source-bundle records with the exact uploader
receipt and imports a working-tree snapshot by its sealed blob digest.
It consumes the verified stream and closes it before Git opens the bundle.
Import verifies the declared snapshot commit and refuses a symlinked pipeline
directory. The checkout is published only after those checks pass. Failed
transfers or imports remove staging and preserve an existing checkout.

Source checkout acquisition belongs to `internal/source`. The obsolete
`bincache` direct-fetch clients, credential exchange, SSH-key sweeper and
checkout implementations are removed. The signed bundle importer retains only
local Git commands. Those commands discard inherited Git routing, tracing and
configuration controls. The snapshot baseline type remains for CLI capture.
Its environment transport and remote adoption path are removed.

Working-tree intake must supply live upload authority before committing and
sealing a source digest. Production host construction must pass its current
data authority and sealed digest to the shared source boundary.

The acceptance suite runs nine `storage-components` checks for receipt and
namespace isolation, binary trust, verified streams, presence budgets, memo
failure policy and tagged logs. Generated filesystem properties exercise signed
HTTP handlers, immutable CAS recovery and log uploads/reads on real files.
The remote-cache property reads real filesystem objects through signed transfers.
It rejects altered record scope, missing verified-default evidence and corrupt
or missing bytes before granting artifact access. Its record service is a fake.
The memo budget check drives deadline expiry with a simulated clock.
Controller authorization and transactional records use fakes. These component
checks run separately from the skipped native targets, whose transport,
transaction, backend matrix and placement assertions need their real drivers.

Storage persistence proofs live in `tests/acceptance` as test-only helpers.
The upload-isolation boundary uses real SQLite records with both filesystem
objects and the S3 signer and SDK against a fake object service. Thirty-two
generated uploads cover separate same-run receipts, missing staging and unique
byte accounting. Physical publication grants no receipt. After staging cleanup
and reopening SQLite, exact settlement replay retains its receipt while an
unrelated job still cannot read the bytes. The repository-reader boundary
uses the shared edge rules with a repo-bound reader that has no job attempt and
rejects records without verified default-branch evidence. Authority and graph
intake are seeded fixtures. Neither proof covers authenticated controller routes
or native placements.

The tagged-log property runs against filesystem objects and the S3 signer and
SDK. It varies subject identities, retention classes and lost PUT responses.
Batch signing checks S3 lifecycle readiness. Offset reads verify persisted ranges
before emitting bytes, and corrupt objects fail before output. Controller
signing remains a contract fixture. Segment commits and reads use real tenant
transactions and survive reopening SQLite.

The sink acceptance properties drive concurrent numbered writers through the
real spool, compression, signed transfers and tenant segment transactions.
Each backend receives 128 generated cases with job, trigger, plan and eval
subjects. Duplicate appends appear once, each writer's records retain their
order, live offsets match the final bytes, and closing removes the spool.
After reopening SQLite, the committed reader checks exact offsets and rejects
altered generations and revoked readers. Lost PUT replies require exact stored-byte
verification. Claim issuance and authenticated signing remain contract fixtures.
The unused filesystem append-log backend and its example are removed.

Local host construction does not open an artifact backend from
`SPARKWING_CACHE_URL` or create an unused raw artifact cache. The final host
contract takes live byte and record clients through `nodehost.StorageBinding`.
Runtime bindings still need integration.

The raw `internal/storage.ArtifactStore` contract and filesystem adapter are removed.
`internal/storage/s3.MeasurementStore` exposes physical bucket measurement only.
Tagged log reads use the configured blob backend through `logseg.CommittedReader`.
The separate raw S3 reader, listing reader and create-only writer are removed.
Dashboard reads select subjects through `Tenant.ReadRunView`; each byte read and
live-ring continuation rechecks the request actor through `ReadLogSegments`.
Local dashboards use the same path. Run-directory log fallback is removed.

`storeurl.OpenArtifactStore`, `storage.Usage` and the controller's unused
`WithArtifactStore` fallback are removed. `OpenMeasurementStore` and
`WithBucketUsage` expose `storage.UsageReporter`. Measurement retains its page
cap and stays outside the serving request budget. The S3 retry test uses a
simulated clock and an injected HTTP client. It opens no listener and needs no
global limiter override or leak exclusions.

The materialization properties join output writes, artifact capture, signed
transfers, memo verification and artifact staging against filesystem and S3
objects. Each backend receives 128 generated payloads. Missing output records,
missing or corrupt bytes and refused copy transactions produce memo misses.
Required artifact reads fail before replacing the affected file. Record
transactions and controller authorization are fakes. Staging path and symlink
containment is covered by `TestArtifactStaging` in `internal/nodehost`. Native
Windows filesystem evidence remains required.

The filesystem and S3 archive proofs drive the common host output and artifact
publishers through real tenant upload and record transactions. Empty output is
also covered directly through `OutputData.Put`. They commit memo through those
same records. `DataController.BindData` supplies the byte signer and record
store through persisted authority resolution, live data-access checks and tenant
transactions. The proofs reopen SQLite, verify required output bytes, materialize
the memo and stage its artifacts. They reject a forged uploader and a commit
without staging, replay settlement after staging cleanup and reopening, and
refuse reads or receipt replay after revocation. Claim issuance is seeded.
The S3 object service is injected. Native placement remains separate.

The physical log proofs use real tenant segment commits and reads with both
backends. Signed slots and stored references share `LogObjectKey`. Segment
numbering starts at one. They cover replay, reopening, offset reads, retained
bytes and revoked-reader refusal. Authentication and award issuance are seeded
fixtures. Native placement and transport acceptance remain separate.

The controller's retained-data pass now marks and collects blob tombstones through
real operator APIs and its configured `ObjectBackend`. It renews the maintenance
lease before physical work. Filesystem log expiry follows the same pass. The
bucket-reconciliation worker, its free-share log pruning, listing-based counters
and `storage_pass` health projection are removed. Read-only deployment bucket
measurement remains separate from authoritative tenant accounting.

Standalone filesystem construction and signed transfer registration now use the
common physical backend. Existing filesystem acceptance fixtures send transfers
through the controller Handler, including real tenant-record and log-record
proofs. Authentication and award issuance remain seeded or contract fixtures.
Signed paths bind the method and exact object. The old filesystem output MAC,
output handlers, raw log adapter and log PUT handler are removed.

The implemented byte and record types live in `types.go`, tagged log subjects
in `logs.go`, and the physical backend interface in `objects.go`. Their former
contract files and forwarding declarations are removed. `internal/storage` has no
legacy state-store interface. Native hosts use their existing tenant boundaries.
