# Blob storage

`NewSignedStore` uses a controller `DataSigner` for upload tickets, commits,
downloads and presence queries. The caller supplies authority in memory.
`TransferClient` sends only the signed headers to the object URL and refuses
redirects. It supports S3 and filesystem signing through the same HTTP path.

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
failure becomes a miss. A depcache failure returns `ErrNotFound` for a cold start.
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
retention belong to `pkg/store`. Controller authorization and upload tickets belong
to the controller and capability packages.

`ObjectBackend.ReadManifest` reads only the selected tenant's CAS object and
returns its file metadata after verifying length, digest and successful close.
`storage.BlobManifests` supplies that reader to `Tenant.BlobRecords`. The record
transaction authorizes the manifest first and checks every member afterward.
This physical read opens no nested database transaction. `Server.WithDataObjects`
installs it for result verification and authenticated record operations.

The filesystem and S3 manifest boundaries generate file metadata and reject
tenant substitution, corrupt bytes, truncation and malformed members. The SQLite
record boundaries reject forged metadata and borrowed same-run upload receipts,
then admit independently verified member uploads and read them after reopening.
Record fixtures, writers and store checks share `ResultRecordKey`.

Artifact capture and staging live in `internal/artifacts`.
The common host artifact publisher calls `Capture` with its current storage
binding and the final result key. It writes the shared `Manifest` format, which
the real store verifies before publishing member edges. Its `Data` value binds
the byte store, record store and authority to the same job. Capture uploads each
file even if another principal stored identical bytes, because an existence
answer does not establish an upload receipt. It commits the manifest only after
all uploads succeed. Staging requires an authorized record and verifies both
the manifest and each member before replacing a destination file.

Artifact records use `KindManifest` and the exact-attempt `ResultRecordKey`
layout. A memo copy uses that same layout for its destination manifest.
Attempt and generation also stay in `Record.Source`.
`Manifest` and `DecodeManifest` define the shared bytes/member agreement for
staging and memo reads. This includes each file's path, digest, size and mode.

Publication component checks exercise output and artifact writes through signed
filesystem and S3 transfers with imported attempt identities. Forged uploader
selectors fail before upload. Refused record updates return errors and preserve
the preceding records, readable output and local files. Record authority is a
contract fake. Controller intake, local provenance, `trust_local_builds` and
the CLI's separate publication result still require their owning integrations.

`MaterializeMemo` asks `RecordStore.CopyMemo` for an atomic, authorized copy,
then verifies the output, manifest and every member within the lookup budget.
Missing or corrupt bytes, a refused edge or a failed copy produces a miss.
An absent output digest also produces a miss. Empty output remains valid when
its explicit blob and output record were copied.
Caller cancellation remains an error. Hosts query records before coordinating
a cold execution, so verified default-branch fallback does not depend on an
old run or a concurrency-cache row. A promoted waiter checks records again.
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
transfer error, the uploader asks `LogTransport.VerifyUpload` to reauthorize the
writer and verify that exact object's stored size and SHA-256. Only a successful
verification permits the record commit to proceed. `logseg.UploadVerifier`
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
lease. The backend derives that tenant's CAS key, and an absent object succeeds
on replay. The old controller output pruner and bucket-measurement expiry
callback are removed. The controller supplies its configured backend to the
shared leased maintenance pass. Standalone S3 construction installs this backend.
Standalone controllers install either backend through `Server.WithDataObjects`.
That constructor also installs physical manifest verification for terminal results.
The authenticated data bindings and local-controller host construction remain open.

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
Artifact staging applies its caller's explicit budget to record lookup, downloads
and file publication. Cancellation before replacement preserves the existing file.
Capture checks the exact uploader receipt for each file and the manifest, then
checks the committed record's key, digest and source identity before returning it.
Dependency-cache saves check uploader authority before creating the archive. They
check the returned receipt and committed record against the expected digest, key
and exact uploader. Output and dependency-cache writes cancelled after upload
return an error before committing a record.
Archive spools and filesystem upload temporary files report cleanup errors.

`bincache.BinaryData` binds remote pipeline binaries to these same byte and
record contracts. A download checks the exact repo/ref/build record and team
provenance policy before reading its committed digest. It installs the
executable only after EOF verification and successful close. An upload obtains
the exact owner/job receipt before committing its binary record. The record
store assigns provenance. The binding keeps the host's authority in memory.

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
authorization and segment records are fakes in this property.

The sink acceptance properties drive concurrent numbered writers through the
real spool, compression, signed transfers, committed reader and stored manifest.
Each backend receives 128 generated cases with job, trigger, plan and eval
subjects. Duplicate appends appear once, each writer's records retain their
order, live offsets match the final bytes, and closing removes the spool.
Lost PUT replies require exact stored-byte verification. Altered generations
cannot select the committed ranges. Record and authority callbacks are fakes.
The unused filesystem append-log backend and its example are removed.

Local host construction does not open an artifact backend from
`SPARKWING_CACHE_URL` or create an unused raw artifact cache. The final host
contract takes live byte and record clients through `nodehost.StorageBinding`.
Runtime bindings still need integration.

The raw `pkg/storage.ArtifactStore` contract and filesystem adapter are removed.
`pkg/storage/s3.MeasurementStore` exposes physical bucket measurement only.
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
transactions and controller authorization are fakes. Separate generated capture
and staging properties check symlink and path containment on Unix. Native
Windows filesystem evidence remains required.

The filesystem and S3 archive proofs drive the common host output and artifact
publishers through real tenant upload and record transactions. Empty output is
also covered directly through `OutputData.Put`. They commit memo through those
same records. They reopen SQLite, verify
required output bytes, materialize the memo and stage its artifacts. Their data
signer is a contract fixture around real reservations, receipts and physical
verification because authenticated controller binding remains separate.

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
contract files and forwarding declarations are removed. `pkg/storage` has no
legacy state-store interface. Native hosts use their existing tenant boundaries.
