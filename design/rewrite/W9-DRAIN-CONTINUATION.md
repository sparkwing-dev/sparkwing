# W9 drain continuation

Base: `86e35c0f6`. The lead's native local-controller and local-schedule passes
stand. The local-run SDK replacement policy remains unchanged and awaits Korey.

## Implemented

The shutdown invariant is that closing the controller API for a drain must not
cancel the admission connection before its drain acknowledgement is written.
The local runtime now includes the daemon's existing draining state when
classifying service shutdown. Previously, closing the API listener could return
before `quit` closed. The runtime treated that expected closure as a service
failure, cancelled the daemon, and closed the admission connection while its
acknowledgement was still being written. Genuine shutdown failures still return
errors through the existing joined cleanup.

`TestLocalRuntimeDrainAcknowledgesBeforeClosingConnection` exercises the real
runtime and admission connection with in-memory listeners and `net.Pipe`. It
holds the acknowledgement reader until API shutdown runs, then requires the
acknowledgement and joined controller cleanup. The test failed before the fix
with `read frame: EOF` and passes afterward, including under the race detector.
It uses no sleeps or real sockets. The controller lifecycle document describes
the ordering and this regression.

## Verification

All Go commands used GOWORK=off, GOFLAGS=-buildvcs=false,
GOCACHE=/tmp/sw-gocache, GOTMPDIR=/tmp/sw-astra-W9/go and
TMPDIR=/tmp/sw-astra-W9. Package workers were limited to four.

Passed:

- Whole-repository `go build -p 4 ./...`.
- `go vet -p 4 ./internal/wingd/... ./cmd/sparkwing ./internal/cluster ./internal/controller/client`.
- `go test -p 4 -race ./internal/wingd -run '^TestLocalRuntime' -count=1`.
- Focused TestLocalRuntime, TestLocalDrain, TestLocalInstall, TestLocalStop,
  TestLocalDoctor, TestDoctorCommand, TestDoctorJSON, TestDoctorFormats,
  TestPeerIntakeNativeCommitProperty and TestNative selections across wingd,
  wingd/client, localcontroller, controller and the CLI.
- `go test -p 4 ./tests/acceptance -run '^Test(Surface|EngineAmendments)' -count=1`.
- Full doccheck, commentcheck across 2,345 files and `git diff --check`.

The existing doctor HTTP test still sweeps a pruned run and reports
`unrecorded_step_outcome`. The public CLI warning tests pass. The peer-intake
property still commits through HTTP and SQLite with generated peer identities,
actor forgery, replay, foreign-team rejection and revocation checks.

The complete installed-runner rewrite_integration probe was run. Start, warm,
fetch-source and run-node pass their first production HTTP-operation assertions.
Launch still fails with `launcher runtime is unavailable`. Its assertion and
production error are unchanged. These probes establish composition and the first
external operation, not completed runs.

No archived test was newly ported and no native acceptance cell is newly claimed.
The lead must rerun TestReviewPublicDoctorSweepsAndWarnsForPrunedRun natively to
verify both the sweep assertion and daemon cleanup together. Native sockets,
service managers and platform execution remain outside this sandbox's evidence.

## Contract changes needed and open integrations

No foreign contract changed. The outstanding requests in
W9-LOCAL-SDK-CONTINUATION.md and W9-RUNNER-TRANSPORT-CONTINUATION.md remain.

1. C2/W3 must declare an owner/eval process credential and source-job relation
   independent of node-attempt authority. CredentialNodeProcess still requires
   a positive attempt. ProcessAuthorities.Delegate still requires the service
   job to equal the capability's bound node. W9's separate evaluation binder
   passes its fake-contract test before any work award. Production delegation
   cannot use that contract without fabricating a work attempt. This piece
   remains stopped at the foreign contract boundary.
2. W8 must supply KubernetesBoundary.Probe and Clear with actual network
   enforcement evidence and reservation-scoped cleanup. The only constructions
   are in tests. ProductionRuntime composes the other four runner commands, but
   cannot safely compose launch without these deployment implementations.
3. Canonical remote placement eligibility, Cloud plan/eval hosting, cross-run
   record/read-set proofs, bootstrap checkout consumption, sealed scheduled
   intake, public step/receipt projections and earlier here/cache/publication
   bindings remain recorded in the earlier reports. No new completion is claimed
   for them. Findings 7-9's process/storage, shared log and service-handoff
   implementations are retained and their focused tests pass.

## Scope, size and friction

Shared files touched: internal/wingd/local_runtime.go, local_runtime_test.go and
LOCAL-CONTROLLER.md. The lead-owned handoffs and amendments are unchanged.
No API was removed, so no caller migration was needed. No legacy code was newly
deleted: this continuation adds six production lines and deletes zero.
W9-owned production paths versus main remain +16,876/-23,797, net **-6,921**,
within the -2,900 budget. Tests, testdata and contract declarations are excluded.
The six shared wingd lines are reported separately.

No option, timeout, retry, interface, authority grant or compatibility path was
added. No Git write, candidate installation, operational Sparkwing command,
pipeline, deployment or release ran. Git still reads the configured user name
from the user's global configuration and has no core.worktree override.
Worker scratch is removed after checks join. The shared build cache is retained.
Friction: native sockets and service managers are unavailable, Overwing reports
installed-binary source lag, and the owner/eval and launcher dependencies above
prevent a complete area claim.

STATUS: CONTINUE
