# Blob storage

`NewSignedStore` uses a controller `DataSigner` for upload tickets, commits,
downloads and presence queries. The caller supplies authority in memory.
`TransferClient` sends only the signed headers to the object URL and refuses
redirects. It supports S3 and filesystem signing through the same HTTP path.

Uploads check the signed digest and size, consume and verify the source, then
commit the ticket. The returned receipt must match the signed uploader and
digest. Downloads verify size and SHA-256 at EOF. Reading the expected number
of bytes and closing is insufficient. Consumers must read EOF and check errors.
`HasMany` carries the caller's deadline and rejects unsolicited result digests.
The controller remains responsible for filtering its results by the live read set.

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
Artifact reads are limited to the output, manifest, member and depcache digests
of accepted cache lookups.

The dependency archive backend lives in `internal/blob/depcache`. Hosts construct
it with their bound clients and install it through `depcache.WithBackend`.
`internal/depcache` cannot import blob because protocol declarations import its
cache specification. A download is verified before archive extraction publishes
files. Without an injected backend, dependency caches use local files.

Record transactions, read-set construction, unique-byte accounting, GC and
retention belong to `pkg/store`. Controller authorization and upload tickets belong
to the controller and capability packages.

`NewFilesystem` and `NewS3` implement `capability.DataSigner` and `ObjectBackend`.
The controller passes an authorized `ObjectToSign` after reserving quota.
PUT keys are `teams/<team>/pending/<ticket>` and bind the expected size and SHA-256.
GET keys are `teams/<team>/cas/<sha256>`. Logs cannot use this signing surface.
The controller supplies the expiry from the shared limits. Both backends refuse
data downloads lasting longer than sixty seconds.

The filesystem backend serves its configured endpoint using an injected signing
key. It verifies the complete upload before publishing the staging file. S3 PUTs
sign the content length and checksum. Commit checks the S3 checksum and length,
then copies with a source ETag fence and a destination create-only condition.
Both backends verify an existing final object before adopting it on a retry.
Filesystem reads remain subject to the client's EOF verification.

An object commit does not create a live blob or a receipt. The controller must
reauthorize the immutable upload ticket, call `CommitObject`, and commit the blob
row and exact uploader receipt in the store transaction. Only afterward may it
delete the pending object. GC calls `DeleteObject` after the store's reachability
and tombstone decision. Provenance stays on records because identical bytes may
be uploaded by principals with different trust.
