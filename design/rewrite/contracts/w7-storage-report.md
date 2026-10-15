# W7 storage handoff

W7 remains incomplete. This session adds filesystem signed log uploads and
batch assembly, with an in-process round trip through the tagged uploader and
committed reader. Earlier storage work is preserved. No frozen interface
signature or schema changed.

## Implemented this session

- `logseg.BatchSigner` derives dense segment keys and one manifest key from a
  copied `blob.LogSubject`. Every URL has the same fifteen-minute expiry.
  Requests cannot exceed sixteen slots or the segment-number range. The caller
  supplies the run's persisted retention class. A required lifecycle callback
  runs before signing. Invalid requests and failed lifecycle checks sign nothing.
- `blob.Filesystem.SignLogPut` uses the existing filesystem endpoint, HMAC and
  immutable writer. It accepts bounded PUTs for log objects and grants no GET
  access. Segment and manifest bounds come from `internal/limits`.
- The first payload at a filesystem log key wins. An identical retry succeeds.
  A different payload fails without replacing the existing object. Concurrent
  writers exercise the same rule. Invalid, oversized and cancelled uploads
  publish no object and leave no upload temporary file.
- `blob.Filesystem.Get` opens an exact log key for a controller-side read. The
  tagged reader's object interface now requires only Get. Listing cannot become
  an accidental dependency of this read path.
- Storage docs, their embedded mirror, the changelog mirror and the logseg API
  snapshot describe the new component boundary.

## Already committed

The blob clients/backends, artifact capture/staging, memo materialization,
depcache backend, remote-cache client, binary/source clients, tagged uploader
and committed reader remain in place. Cache-service, proxy and source-transport
cuts from earlier sessions remain deleted. The prior session's 142-line
export-discovery deletion is already committed and is not counted again.

## Deleted

**Legacy code deleted this session: 0 lines.** No whole file or frozen API was
removed. The filesystem endpoint reuses its existing signing and write code.
Controller/host legacy-log deletion remains open until their tagged bindings
replace the active path. No compatibility adapter was added.

## Tests and acceptance

**Native storage acceptance: 0 passing, 424 skipped, 0 failing cells.**
`acceptance.New` and `Run` still panic as contract stubs. The storage skeleton
was executed and its target-level JSON events counted. No skeleton skip was
removed, because the new component tests do not execute its native placements.

Nine new test functions cover batch identity/ranges/retention checks, expiry,
PUT-only authority, path and size refusal, cancellation, immutable concurrent
uploads and uncertain commits. The round trip uses the real filesystem signer,
HTTP handler, signed-transfer client, sink, compression, uploader and committed
reader for job, trigger, plan and eval subjects. Record persistence and authority
remain fakes. Both segment-commit and final-commit failures retry against the
real immutable endpoint. These tests do not prove native store authorization,
controller metering or retention sweeps.

**Additional T1 inventory ports completed: 0.** Earlier source-import ports are
preserved. The controller/stalled-body ports still need native store/controller
bindings. No existing parked finding is claimed closed by a fake authority check.

## Verification

All Go commands use `GOWORK=off`, `GOFLAGS=-buildvcs=false`, and `GOCACHE`,
`GOTMPDIR` and `TMPDIR` below `/tmp/sw-astra-W7`. Builds/tests use at most
`-p 2` in this session. Source tools use `-p 1`.

Passed:

- Whole-repository `go build -p 2 ./...`.
- `go vet -p 2 ./internal/blob/... ./pkg/storage/logseg`.
- Full `go test -p 2 ./internal/blob/... ./internal/artifacts ./pkg/storage/logseg -count=1`.
- Full `go test -race -p 2 ./internal/blob ./pkg/storage/logseg -count=1`.
- Acceptance catalog, port inventory and invariant checks with
  `go test -p 2 ./tests/acceptance -run 'Test(Catalog|Port|Invariant)' -count=1`.
- Changed-line commentcheck and sleepcheck against HEAD, including untracked Go
  files. Storage/changelog mirrors match their sources. The logseg API snapshot
  matches generated output. `git diff --check` passes.

The new tests first failed compilation because the requested signer and endpoint
methods did not exist. No Sparkwing binary, pipeline, daemon or Git write ran.
Independent review remains with the lead. No whole-repository test/gate or
native Windows/S3 service pass is claimed.

## Contract changes needed

1. **W1: atomic upload lifecycle.** Define durable immutable-ticket creation,
   commit/replay and expiry release. Commit must reauthorize the exact uploader,
   publish a live blob and receipt, and settle unique-byte quota together.
   Separate receipt/quota commits and process-memory tickets remain unsuitable.
2. **W1/W2/W9: working-tree submission.** Admission seals a source digest but
   returns no normal upload authority. Define how the submitter obtains upload
   authority and when execution may observe the sealed source.
3. **W1/W2: signing projection.** Expose the run's immutable retention class
   with the current writer authority. The schema has `log_retention_class`, but
   the final run projections provide no named accessor. BatchSigner accepts
   this resolved class and lifecycle check without recomputing standing.
4. **W1/W2/storage: S3 immutable batch uploads and replay.** LogUploadURL and
   LogUploadBatch carry URLs without required header maps. The local AWS SDK
   probe of PresignPutObject with IfNoneMatch returned required header
   `If-None-Match: *` and signed headers `host;if-none-match`. SubjectUploader
   currently cannot carry that header. An uncertain successful S3 PUT would
   then replay as 412, so headers alone are insufficient. Define a
   digest-verified replay/commit path and its commit-time authorization before
   adding the S3 batch backend. Signing future slots also has no digest/length
   metadata, so storage admission/accounting for those slots needs an explicit
   policy. No unsigned overwrite or unchecked 412-as-success was introduced.

The S3 probe used fixture credentials and performed presigning only. It made no
object-store request. The recommendation is to carry signed headers and settle
an existing slot only after verifying its bytes under the expected digest and
current subject authority. The owner of that cross-area commit contract must
supply the transaction boundary.

## Remaining real integrations and W7 work

- W1 must implement records/read sets, memo-copy transactions, live blob rows,
  unique-byte quota, retention/tombstones and the upload lifecycle.
- W2 must bind capability-aware data/record routes, tagged log signing/commits
  and proxied reads. Authorize before signing and reauthorize after request-body
  consumption at commit. Meter ReadResult.Bytes on success and failure with the
  log download-cap exemption. Wire the filesystem endpoint and class lifecycle
  callback. Filesystem log retention expiry/sweeps remain unimplemented here.
- W1/C2 must enforce exact persisted writers, final-job tail grace, current
  ancestry/membership and no newer claim. Eval gets no job grace.
- W3/W8 must bind artifacts, memo, depcache, binary/source and tagged-log clients
  per attempt. Retry/generator/memo materialization and binary provenance must
  execute through those bindings.
- Replace and delete the active controller/client attempt-only signer/sealer,
  controller stream/ring path, orchestrator log path, AttemptPrefix and legacy
  manifest identity fields. Follow/polling and ring fallback stay controller-owned.
- Delete old controller data handlers, cache-grant signing and service-token
  storage accounting after their final replacements bind.
- W4 must finish Cache integration and the lint declaration. T1/lead must
  implement the native acceptance drivers and execute every target cell.

## Local seams and assumptions

BatchSigner and LogPutSigner are storage assembly boundaries after controller
authorization. SegmentObjects narrows the existing reader dependency. They do
not change frozen credential or persistence signatures. They are needed because
future slot signing and exact recorded reads are distinct from CAS data signing
and arbitrary object listing. Removing the two new implementation files and
restoring the small filesystem/reader edits reverts this session's seam.

The filesystem path reuses the existing HMAC and create-once writer because a
second signing service would duplicate that work. No environment variable,
background loop, customer-visible limit or compatibility mode was added.

## Shared files and friction

Outside the owned storage implementation and its package docs:

- `.apidiff/pkg_storage_logseg.txt`.
- `docs/storage.md` and `pkg/docs/mirror/storage.md`.
- `CHANGELOG.md` and `pkg/docs/changelog.md`.
- This handoff file.

**Caller migrations outside W7: none.** All existing callers compile after the
reader's object interface is narrowed. Other areas' frozen contracts, runtime
implementations and API snapshots were left unchanged.

The native acceptance driver remains a stub. Socket-dependent integration and
full pipelines belong to the lead's environment. Windows is unexecuted. The
S3 required-header/replay gap is reproduced above, not inferred from a failed
external service. Git still resolves Korey Gambill and has no core.worktree
override. Writes stayed inside the worktree and the permitted scratch tree.
Scratch and caches are removed at handoff.

STATUS: CONTINUE
