# W7 storage handoff

W7 remains incomplete. This session replaces the source-bundle client and
deletes the legacy byte client and cache-grant request code. Earlier blob
backends, artifacts, memo, depcache, binary client and cache-service deletions
remain in place. Frozen contracts were not changed.

## Implemented

- `bincache.SourceData` binds `blob.Store`, `RecordStore`, authority, uploader
  identity and a caller-supplied read budget in memory.
- `UploadSourceBundle` obtains the exact uploader receipt before committing a
  `source_bundle` record. The server-derived key uses the snapshot commit as
  its logical name. Receipt and returned record identity/digest mismatches
  fail integrity. A wrong owner generation fails before opening the file.
- `FetchSourceBundle` reads the sealed digest through the host's authority.
  It consumes verified EOF and closes the reader before Git opens the bundle.
  It checks the snapshot commit, restores raw tree bytes, refuses a symlinked
  pipeline directory and publishes the checkout after validation. Failure
  removes staging and preserves an existing checkout.
- Git uses the existing isolated environment and process cleanup helpers.
  Cancellation retains `context.Canceled`. Deadline errors retain
  `context.DeadlineExceeded` and classify as `blob.ErrBudget`, including during
  Git execution. No hardcoded source timeout or byte cap was added.
- Binary and source clients share file receive/upload helpers. Both require
  EOF verification and successful close before publishing downloads.
- The CLI source adapter and both old host callers use the new API. Their
  production calls pass no storage binding. **Working-tree triggers fail
  before upload or submission. Their execution is not integrated.** Ordinary
  Git acquisition and local binary builds retain their existing paths.

## Deleted

Five whole files, **495 lines**, are deleted:

| File | Lines |
| --- | ---: |
| `internal/bincache/grant.go` | 79 |
| `internal/bincache/grant_test.go` | 103 |
| `internal/directdata/client.go` | 215 |
| `internal/directdata/client_test.go` | 74 |
| `internal/orchestrator/cache_grant.go` | 24 |

Retained files also lose `FetchSourceBundleDirect`, the source object-key parser,
duplicated verification, pretrigger capability negotiation/upload, and the CLI
HTTP fixture that required uploading before admission. No Go consumer remains
for those deleted clients. Controller-side grants and old data handlers remain.

## Acceptance and test ports

**Native storage acceptance: 0 activated. All 424 target cells remain skipped.**
`acceptance.New` and `Run` remain stubs. Component fakes do not count as native
store, controller, S3, Windows or placement evidence.

Ported the two old source-import tests to the real signed blob client with fake
signer/transfer contracts and real Git/files:

- `TestDirectSourceBundleImportsExactSnapshotWithoutOrigin` becomes
  `TestSourceBundleImportsExactSnapshotWithoutOrigin`, adding wrong-snapshot
  refusal, staging cleanup and preservation of an existing checkout.
- `TestDirectSourceBundleRefusesSymlinkedSparkwing` becomes
  `TestSourceBundleRefusesSymlinkedSparkwing`, retaining external, relative
  external and internal symlink cases.

New tests cover corrupt/short/long/unverified/missing/unreadable bytes, failed
close, budgets, cancellation, exact upload identity, receipt/record substitution,
wrong owner generation and failed uploads. The signal-driven Git cancellation
regression failed before the fix and passes afterward. Tests use no sleeps.

The existing `blob.TestSignedTransferHTTP` retains signed-transfer isolation
coverage from the deleted direct client. Capability-negotiation tests assert an
obsolete protocol and are removed. The CLI preadmission-upload success assertion
conflicts with the uploader contract and is removed. Its failure-before-admission
assertion remains.

No additional full T1 port is claimed closed. In particular,
`TestDataDownloadClaimUsesCloudProvenance` still needs native execution,
source authority and controller provenance coverage.

## Verification

All Go commands used `GOWORK=off`, `GOFLAGS=-buildvcs=false`, caches/temp files
under `/tmp/sw-astra-W7`, and `go test -p 2`.

Passed on the final code:

- Whole-repository `go build -p 2 ./...`.
- `go vet -p 2 ./internal/bincache ./internal/cluster ./internal/orchestrator ./cmd/sparkwing`.
- `go test -p 2 ./internal/bincache -run '^Test(SourceBundle|Binary)' -count=1`.
- Full `go test -p 2 ./internal/blob/... ./internal/artifacts`.
- Test compilation for `cmd/sparkwing`, `internal/cluster` and
  `internal/orchestrator` with `-run '^$'`.
- Acceptance catalog, port inventory and invariant checks with
  `go test -p 2 ./tests/acceptance -run 'Test(Catalog|Port|Invariant)' -count=1`.
- Changed-line commentcheck/sleepcheck, embedded docs sync and `git diff --check`.

A fresh Astra reviewer used an immutable snapshot and test overlays. It found
that Git cancellation lost the context error, reproduced it, and verified the
fix. The final focused overlay passed with no remaining blockers or should-fix
findings. Its process sweep found no leaked Git or test processes. Windows
cleanup behavior was inspected, not executed.

An initial build caught one unused import after deleting the CLI uploader. It
was removed and the repository build passed. Full CLI/controller/host suites
were not run because their HTTP fixtures require sockets blocked by this
sandbox. No native acceptance or Sparkwing pipeline was run.

## Contract changes needed in other areas

1. **W1: atomic upload lifecycle.** Record methods consume verified receipts.
   Quota reservation/settlement alone does not define the durable immutable
   ticket transaction that reauthorizes its uploader, publishes the live blob
   and exact receipt, and settles unique-byte quota together. Ticket
   lookup/replay, expiry release and interrupted-commit recovery need that seam.
   W7 added no process-memory tickets or separate receipt/quota commits.
2. **W1/W2/W9: ordinary working-tree submission authority.** Final
   `CreateRunRequest` seals `Source.BundleDigest`, but normal
   `CreateRunResponse` returns no source-upload authority. It exposes only the
   optional here bearer. `/data/upload` admits a live claim or run-bound
   publisher, and non-job upload identity still requires a run. The old CLI
   uploaded before admission under a profile credential. Define the authorized
   submission/upload handoff and when queued execution may observe the sealed
   source. W7 has not invented pre-run upload identity or widened route grants.

No frozen signature, permission table or schema was changed to answer these.

## Remaining real integrations and W7 work

- **W1:** records/read sets, memo-copy transactions, blob existence, unique-byte
  accounting, retention/tombstones, upload lifecycle and team CAS/log cleanup.
- **W2/W7:** capability byte/record HTTP binding and final controller handlers.
  Authorize/reserve before signing and reauthorize/settle at commit against
  filesystem/S3. Delete old handlers, grant signing, service-token accounting
  and their replaced tests/docs.
- **W3/W8:** bind per-attempt artifacts, memo, depcache, binaries and source
  acquisition. Source uses live authority and `Source.BundleDigest`; the legacy
  environment object's key is not the final projection. Before remote binary
  reuse, ensure machine-cache hits cannot bypass team/provenance policy.
  Complete memoized/retry/generator materialization.
- **W9:** connect working-tree submission to the authorized upload handoff.
- **W7:** migrate segmented log upload/read/publication through tagged subjects.
  Existing sink/client/controller integration remains legacy. W1/C2 own writer
  fences, final-job tail grace, retention classes and exact egress accounting.
- **W4:** finish `.Cache` and migrate the lint declaration.
- **T1/lead:** implement native drivers and execute every storage matrix cell.

## Assumptions and mechanisms

`SourceData` is a local binding of frozen interfaces because the removed
`directdata.Client` cannot establish exact receipt/record ownership. Shared
file helpers avoid a second EOF/close publication path. No credential, wire
protocol, environment variable, service, compatibility shim or policy limit
was added. The reviewer found no mechanism to simplify.

## Shared files touched

Outside W7's bincache/blob/directdata code:

- `cmd/sparkwing/run_dispatch.go`, `pipeline_trigger_test.go`.
- `internal/cluster/trigger_loop.go`.
- `internal/orchestrator/run_node_remote.go`, `cache_grant.go` (deleted).
- `CHANGELOG.md`, `docs/storage.md`, and generated
  `pkg/docs/changelog.md`, `pkg/docs/mirror/storage.md`.
- This handoff file.

## Friction and cleanup

Socket restrictions exclude native HTTP/service tests. The reviewer's separate
cold Go cache added several minutes. Initial bootstrap ran
`sparkwing info --for-agent` before loading the swarm's no-binary instruction.
No other Sparkwing binary, candidate, daemon or pipeline was run.

No repository Git state was written. Git still resolves Korey Gambill and has
no `core.worktree` override. Writes stayed inside the worktree and
`/tmp/sw-astra-W7`. Scratch, including review snapshots/caches, is removed at
handoff.

STATUS: CONTINUE
