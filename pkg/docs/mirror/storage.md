# Storage rewrite contract

This page describes the target storage contract. The byte backends,
artifact, binary, source-bundle and tagged-log components implement it. Controller,
tenant-store and host integration are still in progress. Working-tree
triggers require an authorized storage binding and fail before upload or
submission while that binding is absent.

<!-- design:begin -->

Sparkwing's storage contract separates immutable bytes from the records that
authorize a job to read them. The controller signs uploads and downloads.
The object backend is S3 or the filesystem signer.

## Bytes and records

Each object is named by its SHA-256 digest. Uploads verify the expected length
and digest before commit. Downloads verify both at EOF. A missing object or a
checksum mismatch cannot produce a successful output read or artifact stage.
Memo uses a miss and dependency caches start cold when verification fails.
Artifact staging and dependency-cache restore close the verified download
before publishing files. A close failure preserves the destination for that
download and returns an error.

A record names an output, artifact manifest, memo result, dependency cache,
pipeline binary or source bundle. Each reference requires either the exact
uploader's receipt or an authorized read edge. Knowing a digest does not grant
access. Presence queries reveal only objects the caller may read.

The filesystem and S3 manifest readers derive file metadata from the tenant's
immutable object. They verify its full length and digest, then close it before
returning metadata. The record transaction checks that metadata against the
submitted members and verifies each member's existence and authority.
The physical reader adapter is implemented. Its authenticated controller binding
is still pending.

Each upload ticket must stage and verify its own bytes, including when identical
content already exists. Missing staging fails the commit. Before settlement, a
retry keeps the staged object; after settlement, it replays the stored receipt
under current authority. Staging cleanup follows settlement.

Blob collection uses the store's live reference graph and a 24-hour tombstone
grace period. References added after marking protect their bytes at collection.
Object age alone cannot delete a live blob. Both physical backends implement the
leased collector's deletion interface. Replaying a deletion of absent bytes
succeeds. The controller supplies its configured backend to the leased
retained-data pass. Standalone controllers install S3 or, without a bucket,
filesystem storage under their home directory's `data` directory. Filesystem
transfers use signed data upload, object download and log upload routes. The
old filesystem output and raw log routes are removed. Authenticated data and
host bindings remain integration work. Deployment bucket measurement stays
separate from tenant accounting and deletes nothing.

Memo and dependency-cache records carry repository and ref scope. Manual
writes cannot overwrite verified branch entries. Memo reads its own scope
before a verified default-branch fallback. Dependency caches try their own
ref, the pull-request base, then the verified default branch.

Dependency-cache keys use the declaration, platform and the first present
lockfile inside the workspace. The SDK's `DirCache.Key` and host share that
calculation. Moving the checkout preserves its keys. An escaping path or symlink
runs cold. Hosts with a signed-data binding restore through it and refresh the
binding after successful execution, so retries save under the final attempt.
A refused binding runs cold or skips the save. Local runs keep local archives.

## Source and package downloads

Runners clone the source repository directly. The controller releases the
run's permitted Git credential. A machine owner can use `--allow-repo` or
`allow_repos` to restrict builds and permit fallback to that machine's Git
credentials. Working-tree snapshots travel as signed source bundles.

Package managers use their upstream registries. To use a separate registry
mirror, set its tool configuration and list the required environment names
in the operator's `run.submit_env_allow`. Network proxy and CA settings remain
available to pipeline processes. Sparkwing provides no Git mirror service
or package proxy.

Direct source fetches create a separate repository per checkout, with its own
Git objects and no persistent mirror. Failed fetches remove their checkout.
Existing directories are refused and preserved. The runner's cleanup covers
temporary files and finished runs.

Local pipeline binaries stay in the local binary cache. It stores entries under
`cache/pipelines/v1/entries` in the Sparkwing home. Cache status and pruning
operate on that layout. Older layouts
have no migration or retirement path. `cache info -o json` omits
`legacy_entries` and `legacy_bytes`.

Remote binaries use signed transfers and require authorized build provenance
before execution. The binary client implements that check. Host binding is pending, so remote
hosts build into their machine's binary cache without shared binary transfers.
See [Caching](caching.md) for authoring cache declarations.

## Logs

Logs use immutable segments with their own signed upload path. Log reads pass
through the controller for exact egress accounting. They do not use the blob
download path and remain readable when a team reaches its download cap.

A log belongs to one job attempt or one trigger, plan or eval unit. Its
generation fences both signing and segment commits. Eval logs retain a separate
source-job generation. The uploader verifies bytes, uses batches of sixteen
segment URLs and seals the committed segment list with a manifest.

The batch signer derives every key from that identity and uses the run's stored
retention class. It checks the backend lifecycle before returning sixteen PUT
URLs and one manifest URL with a fifteen-minute expiry. The filesystem signer
accepts an identical retry and refuses different bytes under an existing key.
These URLs grant no log download access.

S3 lifecycle readiness requires an enabled prefix rule with the stored class's
days. Tag and size conditions cannot establish expiry for all objects. The
backend reads the rules and leaves their configuration to the operator.
The filesystem backend provides an expiry pass for the controller's maintenance
lease. It expires objects by their stored class, including retired classes.
An identical upload retry preserves the original age. The pass leaves CAS data
to its separate reachability rules. The controller runs this pass after renewing
its maintenance lease when its physical backend is configured. Standalone
filesystem construction remains a host integration point.

The reader uses authorized segment records, verifies each segment before sending
its bytes and resumes at an uncompressed byte offset. It reports how many bytes
it sent even when a later segment fails. Objects without a persisted log record
do not establish a readable log. The tagged components still need controller
and host bindings.
Both backends open exact log keys for this reader. The S3 backend checks response
lengths against the segment or manifest bound before returning a stream.

Run deletion and team purges use the configured physical data backend to remove
logs across stored retention classes, including retired classes. The team recheck
also removes logs uploaded through a still-valid URL after the first purge.
Scoped deletion leaves other tenants, other runs and CAS objects untouched.
Object listings select physical cleanup only and grant no read authority.
The old sharded filesystem object reader and writer, their locks and the raw
artifact plug-in interface are removed. Filesystem deployment measurement uses
`fs.NewUsageReporter` and counts physical files without a logical object layout.
It grants no byte-read or record authority.

`Server.WithDataObjects` installs the same physical backend's manifest reader
through `Store.SetResultBlobRecords` before serving. Terminal result attachment
uses the live tenant transaction and verifies physical manifest bytes. A corrupt
manifest refuses completion without changing job state. Restoring the bytes
permits completion and identical replay. Authenticated signing and record
bindings remain separate.

Physical log keys have one owner in `internal/blob`. Signed slots use
`LogObjectKey`: zero names the final manifest and positive indexes name immutable
segments. The physical uploader-to-store proofs use real tenant segment commits
and reads, reopen SQLite and verify byte offsets with filesystem and S3 backends.
Authentication and award issuance remain fixtures.

<!-- design:end -->
