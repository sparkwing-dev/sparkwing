# W7 storage handoff

W7 remains incomplete. This session implements verified log-upload replay. The
prior output-record, blob, artifact, memo, depcache, binary, source-bundle,
tagged-log, S3 signing and direct-Git work remains on this branch.

Implemented:

- `SubjectUploader` recovers a failed PUT only after an authenticated controller
  operation verifies the exact object's stored size and SHA-256. A lost response
  can recover immediately. A successful PUT followed by a failed record commit
  can recover on the next call despite S3's create-only HTTP 412.
- `logseg.UploadVerifier.Verify` derives the key from the tagged subject, index
  and server-resolved persisted retention class. It reads within the expected
  size and shared object limit, requires successful close and compares SHA-256.
  Index zero selects the manifest. Positive indexes select segments.
- Conflicting bytes, missing objects, failed close and refused current writer
  authority leave the segment or manifest uncommitted. Cancellation prevents
  recovery and record commit. Pending and final payloads retain their existing
  immutable retry checks.

W7-local contract repair: `blob.LogUploadCheck` and the required
`LogTransport.VerifyUpload(context.Context, LogUploadCheck) error` method.
The controller must authorize the exact writer, including final-job tail grace
where applicable, and load its persisted retention class before verifying.
Verification returns no bytes or download URL and creates no reference edge.
The subsequent `RecordSegments` transaction rechecks authority. A HEAD or 412
alone remains insufficient. The C1 contract prose and both storage READMEs
record this boundary. No other area's contract types were edited.

Mechanism added: the authenticated replay operation and one object verifier.
Treating 412 as success was rejected because it cannot distinguish different
bytes. Skipping a repeat PUT after a successful response does not cover a lost
response. Removing this change restores the prior loud replay failure. No new
setting, limit, environment variable, dependency, daemon or retry loop was added.

Deleted: **0 legacy subsystem lines**. This session replaces **9 Go lines**
while adding **337**, including tests and untracked files. Runtime adds **85**
and replaces **4**, net **+81**. Legacy controller and attempt-only log paths
remain. No unrelated-package caller needed migration.

Verification passed with `GOWORK=off`, `GOFLAGS=-buildvcs=false`, `-p 4`, and
`GOCACHE`, `GOTMPDIR` and `TMPDIR` below `/tmp/sw-astra-W7`:

- Whole-repository `go build -p 4 ./...`.
- `go vet -p 4 ./pkg/storage/logseg ./internal/blob/...`.
- Full tests for `pkg/storage/logseg`, `internal/blob/...`, `internal/artifacts`,
  `internal/depcache`, `tests/acceptance` and `internal/acceptance`.
- Full race tests for `pkg/storage/logseg`, followed by the expanded verifier
  refusal cases under race after the last test additions.
- Changed-line commentcheck and sleepcheck with `-base HEAD`, including
  untracked files. Formatting and `git diff --check` pass.
- Shared Git configuration has no test identity or `core.worktree` override.

Tests came first. New tests initially failed to compile without the replay
contract. After adding the verifier, all four S3 recovery cases failed with
HTTP 412 before the uploader change. Five new test functions cover exact
subject/key verification, invalid or corrupt objects, segment and manifest
recovery after lost responses or failed commits, current-authority refusal,
and cancellation. The real S3 presigner, transfer client, uploader and verifier
run against fake object HTTP responses. Persistence and authorization are fakes.
New complete T1 inventory ports: **0**.

Storage acceptance remains **0 passing, 424 skipped, 0 failing target cells**.
`acceptance.New` and `Run` remain stubs. No native cell was unskipped or claimed
from component evidence. The component checks prove neither native OS/SDK
execution nor real controller authorization.

Contract changes needed in other areas:

1. W1 must define immutable upload-ticket creation, atomic commit/replay and
   expiry release. Commit must reauthorize the uploader and publish the live
   blob/receipt with unique-byte quota settlement.
2. W1/W2/W9 must define working-tree upload authority before run creation and
   when execution may observe the sealed source digest.
3. W1/W2 must expose stored log retention class with current writer authority.
   Standing must not be recomputed for an existing run.
4. W2/W9 must add the authenticated transport route and wire for W7's
   `LogTransport.VerifyUpload`. It requires the same writer/fence and bounded
   final-job tail rule as signing, derives the persisted class, then invokes
   `logseg.UploadVerifier`. No route spelling or C2 route policy was invented
   here. Payload admission for future S3 slots remains unresolved because their
   signed URLs have no known size or digest.

Real integration and remaining cuts:

- W1 implements RecordStore, read sets, receipts, live blobs, upload tickets,
  quota, memo-copy transactions, pins, retention and tombstones. Memo copies
  create destination output/manifest records using their documented layouts.
- W2 binds capability-aware data/record and tagged-log routes, reauthorizes at
  commit, meters partial log reads, and wires lifecycle readiness and expiry.
  The replay component is complete against the interface; the actual transport
  and live authority projection remain integration work.
- W3/W8 bind output, artifact, memo, depcache, binary, source and log components
  to fresh attempt authority. OutputData still has no runtime binding. W4
  completes Cache integration and declaration linting.
- Replace/delete old controller/client data and output handlers, cache grants
  and service-token accounting. Replace/delete attempt-only log signing,
  sealing, stream/ring authority and orchestrator paths, then remove
  AttemptPrefix and legacy manifest fields. Native acceptance remains open.

No new product question arose. Shared prose file changed:
`design/rewrite/contracts/c1-data.md`. All Go changes stay in `internal/blob`
and `pkg/storage/logseg`; no other worker's runtime files were edited. The
storage READMEs and C1 contract page have no generated mirrors. Their component
and acceptance catalog checks pass.

Friction: persistence, live controller authorization and native acceptance are
still stubs. The missing upload lifecycle and source/log authority projections
need contracts from their owners. Components use fakes at those boundaries.
The lead retains pipelines, independent review, committing and landing. No
Sparkwing binary, daemon, service or Git write ran. All checks finished before scratch cleanup. `/tmp/sw-astra-W7` is deleted.

STATUS: CONTINUE
