# W7 storage handoff

<!-- design:begin -->

W7 remains incomplete. The final byte, artifact, memo, depcache, binary, source
and tagged-log components are implemented. Legacy output/log retirement and
native integration remain open. Earlier sessions are committed on this branch.
This session does not repeat their implementation or claim native receipts.

## This continuation

Removed the retired mutable-object API from `pkg/storage`: `ETag`,
`ConditionalWriter`, `Conditional`, `GetWithETag`, `PutIfMatch` and
`ConditionalWritesSupported`. Removed their implementations, endpoint probes,
probe state and old conformance suite. No production caller used those operations.
The remaining log writer needs create-only writes. `PutIfAbsent` now returns
only an error, and the controller's conflict mapper accepts that error directly.
There is no forwarding API or compatibility alias.

Removed **774 lines** of legacy storage source and tests, including the complete
283-line `pkg/storage/conformance/conditional.go`. The tracked storage diff is
118 added lines and 774 removed lines. These are gross diff counts, not a claim
that every removed line was executable code. The old raw artifact/log backends
still exist for their remaining consumers.

The create-only invariant is: independently opened handles cannot both create
one key or replace the winner's bytes. A generated filesystem property races
both handles with 32 input pairs. The blocked-lock cancellation test now uses
`testing/synctest`, with no wall-clock wait. S3 tests check the exact create-only
request and propagation of precondition and transport failures against a fake API.

The remote-cache invariant is: a lookup grants bytes only after record scope,
verified-default evidence and all referenced bytes pass validation. A generated
acceptance property uses 128 repeatable identities over real filesystem objects
and signed HTTP transfers. Record lookup and authorized download selection use
explicit fakes. It covers memo and depcache, twelve altered record variants,
corrupt/missing output and corrupt manifest/member bytes. It verifies that a
rejected lookup grants no artifact access even after damaged bytes are restored.
A separate simulated-clock acceptance check proves an exhausted memo-copy budget
returns a miss.

Three negative-control overlays bypassed the default-branch evidence check,
remote byte verification and filesystem create-only refusal. Each failed its
corresponding property on the first generated input. The overlays never changed
repository source and are removed with scratch.

## Acceptance and ports

`TestStorage` has **9 passing W7 storage component checks, 424 skipped native
cells and 0 failures**. W6's repository-identity component passes separately.

The new passing component is
`storage-local-remote-cache-opt-in/storage-components`. The existing
`storage-memo-materialization-failures/storage-components` now includes `budget`.
The eight earlier component scenarios continue to pass. No native target was
unskipped because their transport, transaction and placement drivers are stubs.

New complete T1 inventory ports: **0**. The budget assertion adds the acceptance
component portion of `TestPresentIsBoundedAndAnswersAMissPastItsBudget` beside
the existing `internal/blob/TestHasManyBudget` unit proof. Its native acceptance
mapping remains open. It does not prove controller read-set construction.

## Verification

Go commands use `GOWORK=off GOFLAGS=-buildvcs=false GOMAXPROCS=4`, `-p 4`,
`GOCACHE=/tmp/sw-astra-W7/cache`, `GOTMPDIR=/tmp/sw-astra-W7/tmp` and
`TMPDIR=/tmp/sw-astra-W7/testtmp`.

Passed:

- Whole-repository `go build -p 4 ./...`.
- `go vet -p 4 ./pkg/storage/... ./internal/blob/... ./internal/artifacts
  ./internal/depcache ./pkg/controller/... ./tests/acceptance ./internal/acceptance`.
- Full tests for `pkg/storage`, `pkg/storage/fs`, `pkg/storage/logseg`,
  `internal/blob/...`, `internal/artifacts`, `internal/depcache`,
  `tests/acceptance` and `internal/acceptance` (the last has no tests).
- `go test -p 4 ./pkg/storage/s3 -run '^TestPutIfAbsent$' -count=1`.
- Three negative controls fail at their intended assertions.
- Formatting, `git diff --check`, commentcheck against HEAD and scoped sleepcheck.
- `bin/sync-docs.sh --check` confirms the existing embedded docs remain in sync.

The required `go run ./internal/commentcheck -base main .` still fails on the
existing `internal/controllerwire/contracts.go:47` HereBearer comment. That file
is unchanged. The session diff passes commentcheck.

Full legacy S3/controller socket suites were not run because their fixtures
open network listeners, which this sandbox refuses. The changed S3 request uses
a socket-free API fake. Real S3, native placements and the full gates remain for
the lead. No Sparkwing binary, service, pipeline, Git write or release ran.

## Remaining implementation and real integration

1. W1: upload reservation/commit/abort, existence and exact receipts, read sets,
   atomic memo copies, unique-byte quotas, newest-success pins and reachability GC.
2. W2: `DataController.DataAuthority`, `BindData` and `client.NewDataClient`
   remain panic stubs. Bind authenticated data/records, tagged-log signing and
   upload verification, writer fencing, commits, proxy reads and egress accounting.
3. W3/W8: refresh `StorageBinding` and log bindings for each attempt/generation.
   Attach output records, artifacts, memo, depcache, binary and source clients.
   Carry trigger/plan/eval log subjects through the runtime.
4. W4/W9: SDK Cache/lint and CLI remote-cache/publication consumers. Regenerate
   final SDK/API snapshots and launch documentation with the completed rewrite.
5. W7: retire `pkg/controller/node_output.go`, `output_objects_s3.go`,
   `output_download_s3.go`, `log_segments.go`, `client/output.go`,
   `client/logsegments.go`, `internal/orchestrator/segment_logs.go` and their
   legacy tests as their live consumers move to the declared contracts. Raw
   `pkg/storage` artifact/log consumers remain part of that cut.
6. T1/lead: native HTTP/api.sock, SQLite/Postgres, filesystem/S3, OS/placement and
   real-repository QA. `internal/acceptance.New` and `Run` remain panic stubs.

## Shared files touched

- `pkg/controller/log_segments.go`: one-line caller migration to error-only writes.
- `VERSIONING.md`: remove the deleted conformance API from the current suite table.
- `tests/acceptance/storage_boundaries_test.go`: register remote-cache proof and
  add the memo budget component.
- `tests/acceptance/storage_remote_test.go`: new generated component proof.
- `internal/blob/README.md` and this handoff: describe the measured boundaries.

Other changes are confined to the retired `pkg/storage` byte APIs and tests.
No final contract change in another area or product question is requested.
No new runtime interface, setting, limit, environment variable or adapter was added.
The remaining friction is the unimplemented authenticated/native wiring and the
unrelated commentcheck violation. Components continue through the existing
contract interfaces with fakes. Scratch is deleted at the end.

<!-- design:end -->

STATUS: CONTINUE
