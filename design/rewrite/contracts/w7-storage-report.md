# W7 storage handoff

W7 remains incomplete. This session implements S3 tagged-log PUT signing and
carries required signed headers through log batches and uploads. Earlier blob,
artifact, memo, depcache, binary, source-bundle, tagged-log and direct-Git work
remains committed on this branch. Do not redo those cuts.

## Implemented and deleted

- `blob.S3.SignLogPut` signs one log key with `If-None-Match: *` and the shared
  fifteen-minute lifetime. Invalid keys, expiry and cancellation fail before
  signing. Presigning makes no object-store request.
- Segment slots carry `Headers`. The manifest carries `ManifestHeaders`.
  `BatchSigner` and `SubjectUploader` propagate them through the real signed
  HTTP transport. Filesystem signing uses the same header-bearing interface.
- S3 data and log signing share header extraction. Host remains in the URL.
  Future log slots carry no fabricated empty-body checksum or content length.
- Conflicting segments and manifests remain unchanged. Lost PUT responses and
  failed record commits cannot turn an HTTP 412 into a successful segment or
  final commit. This is failure evidence, not completed replay recovery.
- Removed the URL-only `LogPutSigner` signature and migrated every caller.
  No old subsystem was deleted this session. The Go diff replaces **72 lines**
  and adds **379 lines**, including tests. Runtime code replaces **26 lines**
  and adds **70 lines**, a net increase of **44**. Markdown is excluded.

## Local contract repair

W7 changes its own `internal/blob/log_contract.go`: add
`LogUploadURL.Headers` and `LogUploadBatch.ManifestHeaders`. The storage-owned
`LogPutSigner.SignLogPut` returns `(string, map[string]string, error)` instead
of `(string, error)`. `c1-data.md` records the header and HTTP 412 semantics.
There is no compatibility signature or alternate transport.

The decision is to carry signed headers because S3's create-only condition
cannot survive a URL-only response. It assumes the controller sends these maps
unchanged after authorization. Undo is a revert of this session. No new option,
limit, environment variable, dependency, service or retry loop was introduced.

## Verification

All Go commands use `GOWORK=off`, `GOFLAGS=-buildvcs=false`, `-p 2`, and
`GOCACHE`, `GOTMPDIR` and `TMPDIR` below `/tmp/sw-astra-W7`.

Passed:

- Whole-repository `go build -p 2 ./...`.
- `go vet -p 2 ./internal/blob/... ./pkg/storage/logseg`.
- Full tests for `internal/blob/...`, `internal/artifacts`, `internal/depcache`
  and `pkg/storage/logseg`.
- The new S3-signing tests first failed to compile because the S3 signer did
  not implement log signing. They pass with its implementation.
- Acceptance catalog, invariant and port-inventory checks.
- Storage acceptance enumeration: **0 passing, 424 skipped, 0 failing target
  cells**. `acceptance.New` and `Run` remain stubs. No cell was unskipped and
  no fake-backed component proof counts as native acceptance.
- Changed-line commentcheck and sleepcheck, including new untracked Go files.
  Formatting and `git diff --check` pass. Shared Git config has no test identity
  or `core.worktree` override.

Full race tests pass for `internal/blob/...` and `pkg/storage/logseg`.

Five new regression functions cover S3 header signing and refusals, signed-header
transport, tagged S3 round trips and conflict/uncertain-write refusal. Round trips
cover job, trigger, plan and eval subjects through the real presigner, transfer
client, uploader and committed reader. Object responses, controller transport,
authorization and record persistence are fakes. The filesystem round trips still
pass. New T1 inventory ports: **0**.

## Contract changes needed in other areas

The signed-header gap is repaired locally. These earlier gaps remain:

1. **W1: atomic upload lifecycle.** Define immutable ticket creation,
   commit/replay and expiry release. Commit must reauthorize the exact uploader,
   publish the live blob/receipt and settle unique-byte quota together.
2. **W1/W2/W9: working-tree submission.** Define upload authority before a run
   exists and when execution may observe its sealed source digest.
3. **W1/W2: log signing projection.** Expose the immutable stored retention class
   with current writer authority. BatchSigner consumes that projection and
   lifecycle readiness without recomputing standing.
4. **W1/W2/storage: S3 log replay and admission.** Define digest-verified replay
   after uncertain PUTs and a failed record commit. A 412 grants no matching-byte
   proof. Future slots still lack admitted payload size/digest metadata. S3 PUT
   signing cannot enforce the filesystem's upload-size bound without those facts.

## Real integration and remaining cuts

- W1 must implement RecordStore, read sets, memo-copy transactions, live blobs,
  upload tickets, unique-byte quota, retention and tombstones. Storage components
  remain tested against those interfaces with fakes.
- W2 must bind capability-aware data/record routes and tagged log signing,
  commits and proxied reads. Reauthorize after bodies and at commit. Meter
  emitted read bytes on success and failure with the log-cap exemption.
  Wire S3 lifecycle/read methods and leased filesystem expiry.
- W1/W2 must enforce exact persisted writers, final-job grace, live ancestry
  and membership, and no newer claim. Eval keeps its own lease rules.
- W3/W8 must provide fresh attempt bindings for artifacts, memo, depcache,
  binaries, source bundles and tagged logs, including retries and reconstruction.
  W4 must finish Cache integration and declaration linting.
- Replace/delete the old controller/client attempt-only signer, sealing,
  stream/ring authority and orchestrator log paths. Remove AttemptPrefix and
  legacy manifest fields after callers move.
- Replace/delete old data handlers, cache grants and service-token accounting
  after final routes bind. Native acceptance drivers remain open.

No new product question arose. S3 conflict recovery and upload admission remain
explicit integration work. This session does not claim working Cloud log replay.

## Shared files and friction

No runtime caller outside W7 needed a change. The shared contract prose changed
in `design/rewrite/contracts/c1-data.md`. Storage documentation changed in
`internal/blob/README.md` and `pkg/storage/logseg/README.md`, plus this report.
Those files have no generated documentation mirrors.

The brief reserves pipelines, native execution and independent review for the
lead. No Sparkwing binary, daemon, service, Git write, landing or release ran.
The sandbox's socket restriction prevents native HTTP/service evidence, so S3
uses injected responses. Cross-area contracts and bindings above remain open.
`/tmp/sw-astra-W7` is deleted. All check processes finished before cleanup.

STATUS: CONTINUE
