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
verified receipt. The record uses the run ID as `Name`, with repo, ref, pipeline
and producer job in `Scope` and an empty `Scope.Key`. Empty output is an explicit
blob and record. The store still rechecks liveness and reference authority in
the record transaction.

`OutputData.Read` requires an authorized output record lookup and consumes its
bytes under the lookup budget. It returns bytes only after EOF verification and
successful close. Missing records and missing objects both fail. Memo reads use
the same bounded output reader. Hosts must attach the resulting record key in
their result transition. Controller and host output bindings remain pending.

`AuthorizeUpload` and `AuthorizeEdge` operate on server-resolved facts. They do
not authenticate a bearer or query the store. Call them inside the transaction
that checks liveness, blob existence and receipt ownership. Owner and publication
writes need `Authority.Upload`, because an attempt cannot represent those fences.
Job receipts require the exact job, attempt, generation, principal and capability.

Memo and depcache namespaces encode each component separately. Manual refs carry
an explicit trust tag, distinct from a verified ref whose literal name starts
with `manual:`. `ResolveWriteKey` derives the repo and ref from authority. The
record transaction also checks the pipeline and job against the sealed plan.

`OpenRemoteCache` requires a profile, repository-bound user authority, a byte
store and a record store. Its lookups set `VerifiedDefaultOnly`, which requires
the record owner to resolve the verified default branch from sealed evidence.
The client also checks returned evidence and verifies the bytes. A memo lookup
failure becomes a miss. A depcache failure returns `ErrNotFound` for a cold start.
Caller cancellation remains an error. Memo lookups use `MemoRecordName`, shared
with the writer. A hit must pass output-size and manifest/member agreement
checks before it grants artifact access.
Artifact reads are limited to the output, manifest, member and depcache digests
of accepted cache lookups.

The dependency archive backend lives in `internal/blob/depcache`. Hosts construct
it with their bound clients and install it through `depcache.WithBackend`.
`internal/depcache` cannot import blob because protocol declarations import its
cache specification. A download is verified before archive extraction publishes
files. Without an injected backend, dependency caches use local files.
`ExtractArchiveStaged(ctx, reader, dir)` consumes the gzip trailer before
publishing, so checksum errors and truncated trailers preserve the destination.
Cancellation during extraction also preserves it. A failed restore runs cold
and can replace the failed archive after successful work.

Record transactions, read-set construction, unique-byte accounting, GC and
retention belong to `pkg/store`. Controller authorization and upload tickets belong
to the controller and capability packages.

Artifact capture and staging live in `internal/artifacts`. Its `Data` value binds
the byte store, record store and authority to the same job. Capture uploads each
file even if another principal stored identical bytes, because an existence
answer does not establish an upload receipt. It commits the manifest only after
all uploads succeed. Staging requires an authorized record and verifies both
the manifest and each member before replacing a destination file.

Artifact records use `KindManifest`, the run ID as `Name`, and the authorized
repository, ref, pipeline and producer job in `Scope`. `Scope.Key` is empty.
Attempt and generation stay in `Record.Source`. A memo copy must create its
destination manifest under the destination run and job's same key layout.
`Manifest` and `DecodeManifest` define the shared bytes/member agreement for
staging and memo reads. This includes each file's path, digest, size and mode.

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
Both backends verify an existing final object before adopting it on a retry.
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
class. The controller must call it under its maintenance lease before treating
filesystem retention as configured. Identical uploads preserve the file age.
The sweep includes retired classes, ignores upload temporary files and leaves
CAS bytes to their separate reachability rules. Cancellation and filesystem
errors return a count of objects already removed and an error.
`LogClassDays` owns canonical class parsing for both backends and log key builders.
The controller still owns record checks, maintenance scheduling and egress accounting.

An object commit does not create a live blob or a receipt. The controller must
reauthorize the immutable upload ticket, call `CommitObject`, and commit the blob
row and exact uploader receipt in the store transaction. Only afterward may it
delete the pending object. GC calls `DeleteObject` after the store's reachability
and tombstone decision. Provenance stays on records because identical bytes may
be uploaded by principals with different trust.

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

Remote runtime bindings remain pending. The old hosts compile into their
machine's binary cache without a remote storage binding.

`bincache.SourceData` uploads source-bundle records with the exact uploader
receipt and imports a working-tree snapshot by its sealed blob digest.
It consumes the verified stream and closes it before Git opens the bundle.
Import verifies the declared snapshot commit and refuses a symlinked pipeline
directory. The checkout is published only after those checks pass. Failed
transfers or imports remove staging and preserve an existing checkout.

The CLI and old hosts have no source storage binding. Working-tree triggers
fail before upload or submission. Intake must supply live upload authority
before the CLI can commit and seal the source digest. Hosts must pass their
live data authority and sealed digest. The cache-grant request client,
legacy source byte transport and grant-based controller data handlers are
deleted. The authenticated controller adapter and remaining output handlers
still need binding to the store's upload transaction.

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

Local host construction does not open an artifact backend from
`SPARKWING_CACHE_URL` or create an unused raw artifact cache. The final host
contract takes live byte and record clients through `nodehost.StorageBinding`.
Runtime bindings still need integration.
