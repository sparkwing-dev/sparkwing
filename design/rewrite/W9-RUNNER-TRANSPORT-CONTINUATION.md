# W9 runner transport continuation

Base: `01a9e18f4`. Priorities (1)-(3) remain implemented. This continuation
fixes a dropped HTTP transport in runner hosting and makes four installed-runner
probes reach their real production operations. The full assignment remains open
at the foreign boundaries below.

## Implemented

`productionHosting` now retains `EngineOptions`, including the selected HTTP
client, when constructing private host-state and source clients. Storage and
process services derive from that private client. The adapter replaces the
claimant bearer with the awarded capability and clears launcher selection. Both
production callers use this constructor. No public API or foreign contract changed.

The invariant is that an awarded host retains its selected controller and HTTP
transport while private requests use its award credential, independently of any
ambient claimant credential. A regression exercises trigger and node private
authentication. Its initial run failed with zero calls to the supplied transport
and an attempted default DNS/socket connection. It passes after the fix.

The generated test varies the awarded and ambient credential values and both
unit kinds. It passed 3,500 fuzz executions and the race detector. These tests
stop at private authentication. They do not claim source or storage execution
evidence.

The installed-runner probe supplies private credential files and a valid remote
source award. Previously, start and warm passed after rejecting missing
credentials, and fetch-source passed after rejecting its incomplete source.
Each subprocess now substitutes only the external HTTP transport. This keeps
the production factories, source owner and engine resolver intact while
preventing network access, release downloads or executable replacement.

The probe asserts the exact method, selected host and path:

| Command | Reached operation |
| --- | --- |
| start, warm | GET sparkwing.dev/versions.json through the signed-policy resolver |
| fetch-source | POST controller.example/api/v1/runs/run/source-credential |
| run-node | POST controller.example/api/v1/claims/exchange |
| launch | Still fails at the missing production launcher factory |

The local-execution guide describes transport retention and the separate award
and claimant credentials. Its embedded mirror was regenerated.

## Verification

All Go commands used GOWORK=off, GOFLAGS=-buildvcs=false,
GOCACHE=/tmp/sw-gocache, GOTMPDIR=/tmp/sw-astra-W9/go and
TMPDIR=/tmp/sw-astra-W9. Tests used at most four package workers.

Passed:

- Whole-repository `go build ./...`.
- `go vet -p 4 ./internal/cluster ./internal/runners/... ./cmd/sparkwing ./pkg/controller/client`.
- Full short suites for cluster, runners, controller/client and localcontroller.
- New production transport regressions, race tests and fuzzing.
- `go test -p 4 -tags rewrite_integration ./internal/cluster -run '^TestInstalledRunnerRuntimeComposition/(start|warm|fetch-source|run-node)$' -count=1 -v`.
- Readiness, runtime-fixture cleanup, public doctor, controller sweep and
  peer-intake selections in wingd, CLI and controller.
- Tagged CLI compilation with `-tags rewrite_integration -run '^$'`.
- Full doccheck, embedded-doc mirror drift check, commentcheck and
  `git diff --check`.

Two failures remain visible:

- The full TestInstalledRunnerRuntimeComposition still fails for launch because
  ProductionRuntime.Launcher is unbound. Its missing-binding assertion remains.
- The unchanged launcher short suite fails
  TestCloudAwardResourcesFromControllerThroughLauncher for plan, eval and node:
  each claim returns a nil award. None of the changed cluster files belongs to
  that package's dependency graph. This is additional evidence for W8's existing
  controller-to-launcher resource integration assignment, not a passing check.

Independent read-only review found no production correctness or security defect.
Its requests for exact per-command HTTP assertions and a distinction between
controller bearer calls and signed byte transfers were implemented. No archived
test was newly ported, and no native acceptance cell is newly claimed. The new
probes prove construction and the first external operation, not completed runs.

## Contract changes needed and real integration left open

The outstanding requests in W9-RUN-PAGES-CONTINUATION.md and W9-HANDOFF.md remain:

1. C2/W3 must declare owner/eval process credentials and a separate source-job
   relation. CredentialNodeProcess still requires a positive node attempt, and
   ProcessAuthorities/NativeProcesses couple the SDK job to that node binding.
   NativeConfig.Evaluate retains its separate fake-tested boundary. Implementing
   production delegation requires that foreign contract rather than a fabricated
   work attempt or an authorization bypass.
2. W8 must supply KubernetesBoundary.Probe and Clear with network-enforcement
   evidence and reservation-scoped teardown. There is no safe production launcher
   composition without those deployment implementations. Cloud plan/eval host
   entrypoints and the controller-to-launcher resource failure above also remain.
3. Canonical remote placement eligibility, cross-run record/read-set proofs,
   bootstrap checkout consumption, scheduled sealed intake and the earlier
   here/cache/publication boundaries remain recorded in the previous reports.

The lead should rerun TestReviewProductionExecutionBindings,
TestReviewPublicDoctorSweepsAndWarnsForPrunedRun and
TestLocalClaimantW8UsesPersistedPeerAuthority natively with isolated state and a
release-version stamp. The strengthened installed-runner probes are socket-free
and can run here. Launch must remain red until its deployment binding exists.

## Scope and size

Shared files touched: internal/cluster/production_runtime.go,
production_runtime_integration_test.go and production_transport_test.go. The
private constructor's two production callers were migrated in the same file.
Documentation files are docs/local-execution.md and its generated mirror.
Lead-owned handoffs and amendments are unchanged.

This continuation adds six production lines and deletes five, net +1 in the
shared cluster file. No legacy file was newly deleted. W9-owned production paths
versus main remain +16,876 / -23,797, net **-6,921**, within the -2,900 budget.
Tests, testdata and contract declarations are excluded.

No Git write, candidate installation, operational Sparkwing command, pipeline,
daemon startup, deployment or release ran. No new option, timeout, retry,
interface or credential grant was added. The process sweep found no worker test
daemon or child left running. Worker scratch is removed after checks join, and
the shared build cache is retained. Friction: native sockets and service managers
remain unavailable, and Overwing reports installed-binary source lag.

STATUS: CONTINUE
