# W7 storage handoff

W7 remains incomplete. Session 2 adds the physical object backends behind the
frozen signing interface and removes more cache-service clients. Native
acceptance, transactional integration and the remaining artifact/memo/source
cut still need work. No git writes or Sparkwing executable invocations occurred.

## Implemented in session 2

- Filesystem and S3 implementations of `capability.DataSigner` in `internal/blob`.
  PUTs bind the key, size, SHA-256 and expiry. GETs name immutable CAS objects,
  exclude logs and enforce the sixty-second maximum from `internal/limits`.
- Filesystem HTTP transfers use HMAC-signed URLs, confined directory operations,
  verified staging and create-only publication. Bad, truncated or oversized
  uploads leave no staging file. Tampered, expired and wrong-method URLs fail.
- S3 PUT signatures include content length and SHA-256. Commit checks the object
  checksum and length, fences the source ETag and creates the final key only if
  absent. Both backends verify an existing final object before retry adoption.
- `ObjectBackend` exposes signing, physical commit and deletion. It creates no
  live rows or receipts. The authenticated controller/store transaction owns
  those operations, as the frozen contract requires.
- SDK git clone/fetch use upstream directly. Removed the cache probe, URL rewrite,
  credential injection and fallback machinery.
- Binary helpers take an injected data client and use only signed controller
  routes. Removed `/bin` transfers and the older discovery/signing fallback.
  Binary installation now checks exact size as well as SHA-256 and EOF. The new
  short/long tests failed before that repair and pass afterward.
- Removed the SDK lint-cache HTTP API and its pipeline calls. Lint retains its
  local tool cache. Remote declaration through final `.Cache(Dir(...))` remains
  open because that SDK method is a stub and the pipeline is a separate module.

Session 1's signed byte client, edge predicates, scoped keys, remote-cache reader,
depcache backend, cache-service deletion and chart/package changes remain in the
committed branch. They were not rebuilt from scratch.

## Deleted

Session 2 removes six complete files containing **1,079 lines**:

- `internal/bincache/{signed_download.go,signed_download_test.go,publication_test.go}`.
  The publication-failure test was ported to `direct_data_test.go`.
- `sparkwing/{toolcache_blobstore.go,toolcache_blobstore_test.go,toolcache_archive_test.go}`.

It also removes obsolete binary transfer functions and tests, the SDK gitcache
client, the CLI's fake cache-server tests and the removed SDK API documentation.
Before this report update, the tracked session diff contained 2,298 removed lines.
Session 1 removed 59 complete files containing 12,787 lines. Across both sessions,
65 complete files containing 13,866 lines have been removed.

The hard cut remains open for the gitcache source helpers in `internal/bincache`,
old artifact/memo consumers, cache grants and old controller data handlers. The
binary data client still uses `internal/directdata`; W2's final authenticated
transport must replace it with `blob.Store`/`blob.RecordStore` bindings.

## Acceptance and test ports

**Native storage acceptance: 0 activated, 424 cells skipped.**
`acceptance.New` and `acceptance.Run` still panic. Catalog and port-inventory
checks pass, which does not prove native execution. No fake result was labelled
as a passing SQLite, Postgres, S3 or placement acceptance cell.

Component evidence added:

- Filesystem signing, verified publication, interrupted-commit adoption, corrupt
  final refusal, bad-body cleanup, expiry, tamper, path and log-download refusal.
- S3 signing and commit through the real AWS SDK with a fake HTTP service.
  Checks signed size/checksum headers, source/destination copy conditions,
  deduplication, interrupted-commit adoption and corrupt final refusal.
- Signed binary transfers with fake HTTP: exact size/digest, missing data,
  cancellation, failed publication cleanup, and bearer isolation on object PUT.
- Direct SDK clone, upstream fetch and preservation of an existing destination.

The interrupted-upload and conflicting-checksum cases cover the physical-object
parts of `TestDirectCommitAdoptsVerifiedCopyAfterOldReservationExpires` and
`TestDirectCommitRefusesExistingObjectWithDifferentChecksum`. Their transactional
uploader/provenance obligations remain open, so these are partial ports. The
prior presence-budget port remains in `TestHasManyBudget`.

The follower-output, refused manifest-copy, retry materialization, quota, GC,
server-owned scope, same-run laundering and native host ports still require
transactional/runtime integration. They are not claimed complete.

## Contracts and real-integration points

Local W7 addition: `internal/blob/objects.go` declares `ObjectBackend`, implemented
by `NewFilesystem` and `NewS3`. The object-signing types are unchanged from W2.
The physical commit/deletion boundary is needed because neither signing nor
`blob.RecordStore` owns object publication. No schema or another area's contract
was changed. Removing these new backend files reverses this addition.

The prior local W7 repairs remain: `Authority.Upload`,
`RemoteCacheOptions.Records` and `RecordLookup.VerifiedDefaultOnly`.

Other-area contract/integration work needed:

- **W1:** expose the final tenant-bound upload lifecycle: persist immutable
  tickets and quota reservations, resolve them at commit, then atomically record
  live blob existence and the exact uploader receipt. `uploads` is in the final
  schema, but the target contract files do not declare that lifecycle's methods.
  Do not infer that an existing object is an authorized receipt.
- **W1:** implement the `blob.RecordStore` methods, restricted default-branch
  lookups, read sets, unique-byte accounting and retention. These remain stubs.
- **W2:** authorize and reserve before signing. Mount the filesystem endpoint or
  provide the S3 client, adapt the object backend to `blob.DataSigner`, and
  reauthorize at commit. After the store commits, delete staging. Supply the
  server-resolved uploader binding and final authenticated record transport.
- **W3/W4/W8:** bind bytes and records to each host/attempt; migrate artifact,
  memo, binary and depcache consumers. The final host inputs still do not expose
  the storage binding. Replace the old source-fetch callers so gitcache helpers
  and flags can be cut without adding an adapter.
- **W4:** implement final `.Cache`; then migrate the repo lint declaration with
  a workdir-safe key. Do not restore the deleted lint-cache HTTP API.
- **T1:** implement the native driver and activate the full scenario assertions.

No product design question was resolved outside the frozen architecture. These
are implementation and integration obligations, not permission requests.

## Verification

All Go commands used `GOWORK=off`, `GOFLAGS=-buildvcs=false`, and `GOCACHE`,
`GOTMPDIR`, `TMPDIR` beneath `/tmp/sw-astra-W7`. Parallelism was at most `-p 4`.
The directory is deleted at handoff.

Passed:

- Root `go build -p 4 ./...` and `.sparkwing` module `go build -p 4 ./...`.
- Root vet for `internal/blob/...`, `internal/bincache`, `internal/depcache`,
  `sparkwing/git`, `internal/cluster`, `internal/orchestrator`, `cmd/sparkwing`.
  The `.sparkwing/jobs` vet also passes.
- Full `internal/blob/...` and `internal/depcache` tests.
- Bincache tests matching `TestDownloadBinary`, `TestDirectBinaryUpload`,
  `TestFailedBinaryPublication` and `TestCompilePipelineCancel`.
- SDK cache tests matching `TestCacheDir` and `TestToolCacheDir`; git clone/fetch
  and promptless tests; CLI `TestPipelineCacheKey_HyphenFormat`.
- Pipeline tests matching `TestLint|TestGolangci`.
- Acceptance catalog/port inventory and the explicitly skipped storage matrix.
- Source/embedded docs equality and `git diff --check`.

Limitations and friction:

- Full `sparkwing/git` tests fail in three unchanged filesystem-fallback tests:
  `TestFilesetHashFilesystemFallback`, `TestFilesetHashFramesFileContents`,
  `TestFilesetHashIncludesPermissions`. They report `git ls-files: not a git
  repository`. The sandbox exposes an empty read-only `/tmp/.git`. The changed
  clone/fetch tests pass. This is not a full git-package test pass.
- The API snapshot generator runs, but the existing snapshot has broad rewrite
  drift, including annotations and approvals. Only this session's four removed
  SDK symbols were removed from the snapshot; whole-snapshot equality is not
  claimed.
- Real S3, controller sockets, native placement runs, Postgres, Windows and
  Sparkwing pipelines/gates were not run. HTTP tests use recorders/transports,
  and the S3 test exercises SDK requests against a fake service.
- Directly importing depcache from the pipeline fails Go's internal-package
  boundary. That attempt was removed; the final pipeline build passes.
- Test git fixture commits now disable signing per command. No repository-local
  identity or worktree setting was changed. No shared git metadata was written.

## Shared files touched in session 2

- `internal/cluster/trigger_loop.go`: bind the signed binary data client.
- `internal/orchestrator/{run_node_remote.go,claim_exec.go,pod_node_host_test.go}`:
  migrate binary-helper callers and test doubles to that client.
- `cmd/sparkwing/compile_cache_test.go`: delete tests of the removed `/bin` API.
- `.sparkwing/jobs/lint_contention.go`: remove the deleted lint HTTP client calls.
- `sparkwing/git/{clone.go,clone_test.go,clone_direct_test.go}` and
  `sparkwing/depcache.go`: direct cloning and current storage documentation.
- `.apidiff/sparkwing.txt`, `docs/{sdk.md,sdk-reference.md}`, `CHANGELOG.md` and
  their embedded copies: remove deleted SDK APIs and record the cut.

STATUS: CONTINUE
