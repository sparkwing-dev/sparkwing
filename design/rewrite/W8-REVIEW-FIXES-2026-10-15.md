# W8 adversarial review fixes

Eight assigned findings are fixed in code. This follow-up closes finding 5.
Finding 3 remains blocked. Findings 6, 7, 8 and 9 were left to W9.
No git writes were performed.

| Finding | Invariant | Status and evidence |
| --- | --- | --- |
| 1 | Runnable Jobs and Pods retain the validated execution, isolation and cleanup constraints. | Implemented the review's verified admission exemption option. LaunchOne refuses before claiming unless the exact denying guard is installed and an actual dry-run request proves enforcement. The guard preserves canonical namespace exclusions for later webhook/binding changes, namespace status/finalize edits and LimitRanges. Existing bindings require exclusions even when their policy is presently unrelated. Job/Pod CREATE/UPDATE and arbitrary subresource rules share resource-prefix matching. The initial real LaunchOne regression ran red. Guard absence, weakening, inactive enforcement, mutable-label exclusions and future policy widening have regressions. Both charts ship matching guard objects and minimum RBAC. Native Kubernetes admission execution remains for the lead. |
| 2 | Cleanup deletes only this publication's Secret instance. | Fixed. Both fuzz seeds ran red through LaunchOne: a renamed invalid Job and Secret AlreadyExists deleted unrelated Secrets. Teardown no longer invents a Secret name. Successful creation records its exact UID before activation, and explicit deletion always uses that UID. Uncertain creation relies on Job owner garbage collection. A suspended-Job test now uses real publication rather than injecting missing cleanup ownership. |
| 3 | Every placement reaches concrete shared owners through its installed entrypoint. | Blocked. The executable subprocess test calls MainWithVersion without factory replacement. All five commands remain red behind rewrite_integration. Missing contracts are listed below. No replacement factories that merely return another unavailable error were added. |
| 4 | The canonical award supplies every immutable input required by its executor. | Fixed. Real store, registered controller HTTP handlers, shared client and launcher claim/exchange reproduce missing plan/eval resources before the fix. All three kinds now pass. The producer projects sealed request or accepted-run resources for owner units and retains persisted job resources for work. |
| 5 | Optional ancestor expiry constrains authority, and expired unit ownership cannot retain capacity. Only the reservation's own award can release it. | Fixed. KubernetesBoundary uses the common bounded deadline projection and accepts an unexpiring service root. Nodes persist the exact award LeaseRef and binding. Sync observes reservation-only ownership through the real store and retires only matching award/NodeUID/ReservationID references after Job absence and container vacancy. The original root-expiry regression and new real-store crash-before-Job regression ran red, then green. Forged fences, foreign launcher ancestry, stale generations, cancellation, exchange and mismatched cleanup replies have property coverage. |
| 10 | Effective build inputs, compilation and executable lifetime have one owner. | Fixed. Real Go fixtures first reproduced missing checksums and then the pruneable returned path. Shared materialization prepares effective checksums before hashing, preserves overlays and verifies exact SDK selection. The selected engine copies the executable while holding its cache lease. Source acquisition, verification and cache warm close the caller-owned copy after use. Tests cover SDK replacement refusal and execution after cache pruning. |
| 11 | Pin discovery, mutation, verification and application address the same pipeline directory. | Fixed. Corrected Go/TypeScript fixtures use the CLI's checkout-root input and failed before the fix. The engine reads/edits .sparkwing, preserves root application manifests and passes the staged checkout root required by the actual verifier. Concurrent-edit and failed-verification fencing pass. |
| 12 | Documented bootstrap commands provision a valid administrator. | Fixed. Both documented commands failed the real validator before correction. Tests execute the swk_ generators through a real store's EnsureBootstrapAdminToken and verify administrator readiness. |
| 13 | Completion has identical scheduling effects through every receive path. | Fixed. A deterministic Loop.Run regression reproduced idle exit after an empty claim raced with completion. Common finish now resets polling. The regression and its race run pass. |
| Stale comments | Operator guidance and comments describe the current contracts. | Fixed all ten table rows, related chart assertions and the full-chart README bootstrap/auth narrative. Wingd edits only remove or correct comments. |

## Contract changes needed

Finding 3 needs authenticated remote private RunSnapshot/NodeRow reads and native
run/job transitions, AuthenticationReader/BindingChecker process authority,
shared source-access client transport, native Cloud plan/eval dispatch, and
production network-probe/node-cleanup integrations. Those are absent behind the
empty Runtime. HOSTING.md and the controller-wire contract already identify
parts of these obligations. A local factory test cannot establish them.

## Approved finding 5 contract amendment

The lead authorized the shared reservation observation and retirement contract.
C3 now permits a reservation-only LauncherObservation with the existing award
LeaseRef, NodeUID and ReservationID and no Job reference. RemoveReservations
returns the exact LauncherReservationRef. C4 records the matching W8 ownership
and vacancy obligations. No lead-owned handoff or amendment file changed.

The store reuses launcherAwardTx for current liveness. For reservation cleanup it
first proves the original bootstrap award belongs to the authenticated launcher's
ancestry. The proof survives an expired unit lease, cancellation, exchange and a
later generation. Unknown or foreign award proof fails closed. Job construction
and restart reconstruction also require the reservation award to equal the Job
binding. CloudJobBinding.LeaseRef owns that projection. The launcher
rejects partial, contradictory or mismatched decisions and rechecks Job absence
before the existing boundary verifies node ownership and container vacancy.

The node stores the binding alongside its reservation so restart uses the original
inputs. The former parent-expiry annotation and opportunistic recoverOrphan path
are removed. Parent expiry can constrain authority, but cannot stand in for unit
liveness. No timeout, renewal, fabricated Job UID or compatibility path was added.

## Shared edits

The only internal/controller edit is engine_claim.go: import runspec and project
plan/eval resources in claimedUnit, while retaining nil execution for trigger
awards. It adds eight net production lines. Claim and exchange share this owner.

Finding 10 also changes internal/bincache/materialize.go, three internal/source
files, and cmd/sparkwing compile/cache-warm/upgrade-verifier code and related
tests. These remove duplicate materialization and carry executable ownership to
its consumers. The W8-local engine contract records ExecutableDirectory,
CachedExecutable and SDKCommand.Close. Lead-owned handoff/amendment files were
not edited.

Finding 5 follow-up files outside W8-owned directories:

- `internal/controllerwire/engine.go`
- `internal/controllerwire/launcher.go`
- `internal/controllerwire/launcher_reservation_test.go`
- `internal/controller/client/engine_methods.go`
- `internal/controller/client/engine_launcher_test.go`
- `internal/store/launcher_snapshot.go`
- `tests/acceptance/bootstrap_store_w8_test.go`
- `tests/acceptance/launcher_w8_test.go`
- `tests/acceptance/launcher_reservation_w8_test.go`
- `design/rewrite/contracts/c3-controller-wire.md`
- `design/rewrite/contracts/c4-surface-config.md`
- This report.

The client validates the new retirement references. The acceptance helpers can
provision an unexpiring service root and preserve the reservation award fence.
Other shared changes are the approved wire, store and contract edits or their
regressions.

## Prior review verification and limits

Repository-wide go build and go vet pass. Focused runner, launcher, cluster,
engine and source suites pass. Focused bincache and CLI installation, cache-warm,
materialization and upgrade checks pass. Full chart tests and focused chart
render/parity checks pass. Commentcheck is clean. Diff checks pass.

The focused acceptance sweep passes all matching TestLauncher tests,
TestStoreLauncherW8, FuzzLauncher and FuzzStoreLauncherAuthorityW8 seeds.
FuzzCloudAwardUnitBindingW8 seeds also pass. These fixtures now supply the
chart's actual guard objects and emulate API-assigned UIDs and dry-run denials.
Restart cleanup publishes through LaunchOne before reading persisted ownership.
Original cleanup, reservation-retention and live-Secret assertions remain intact.
The five changed W8 acceptance files and new shared fake-Kubernetes helper are
test-only shared edits.

Secret ownership fuzzing completed 301 executions and admission exemption fuzzing
completed 45 executions in the recorded short runs. A fuzz input initially made
the test manufacture an invalid Kubernetes label. The generator now always emits
a valid name, and the rerun passes. No production failure was hidden.

Finding 3 remains red behind rewrite_integration. Finding 5's
TestReservationAcceptsUnexpiringServiceRoot is unskipped and passes in the normal
launcher suite. TestStoreLauncherReservationW8 passes through the real store,
controller handlers, shared client, launcher and KubernetesBoundary. It proves
live retention, stale release, Job absence, container vacancy and cleanup replay.
No new native acceptance cell is claimed. Native Kubernetes CEL/admission,
network enforcement and executable deployment remain unverified in this sandbox.
The guard's Forbidden response check follows the documented
[Kubernetes admission-policy denial format](https://kubernetes.io/docs/reference/access-authn-authz/validating-admission-policy/).

GOCACHE is /tmp/sw-gocache throughout. GOTMPDIR directories under
/tmp/sw-astra-W8 are removed at completion. The shared build cache is retained.

## Finding 5 follow-up verification

Repository-wide go build ./... and go vet ./... pass with GOWORK=off,
GOFLAGS=-buildvcs=false and GOCACHE=/tmp/sw-gocache. The launcher, controllerwire
and controller/client suites pass. Focused controller launcher-sync, store
schema/authentication/provisioning/binding tests and the W8 launcher/store
acceptance sweep pass. Ownership regressions and property seeds pass with -race.
Commentcheck is clean. Diff checks pass.

Short fuzz runs pass: exact controller retirement correlation (4,936 executions),
boundary award ownership (825), real-store reservation fencing (24), and shared
wire validation (11,316). Job/reservation award correlation passes 521 executions.
That property's four changed-fence seeds fail with the ownership check removed
through a Go overlay, then pass with it present. The final real-store seed suite
also includes exchange.

The full controller suite cannot complete because an unrelated httptest listener
is denied by the sandbox. The full store suite remains red on legacy/schema,
metering and tenancy guards, then panics in legacy token tests. A Go source
overlay with the unchanged launcher_snapshot.go reproduces the representative
TestHeartbeatConcurrencySlot_RetriesTransientBusy and
TestAwardScannedNodeHonoursTheQueueRequirement failures. No broad-suite pass is
claimed. Native Kubernetes enforcement is still a lead acceptance obligation.

## Production delta and mechanisms

This finding 5 follow-up adds 121 and deletes 68 W8 production Go lines, net +53.
Across all production Go files it adds 205 and deletes 77, net +128. This includes
shared wire/client/store changes. It removes the 31-line recoverOrphan function
and the parent-expiry reservation mechanism.

Against main, excluding tests, testdata, acceptance and contract declaration
paths, W8 adds 10,391 and deletes 6,825, net +3,566. The budget is -2,300, so the
area remains 5,866 lines over budget. It entered this follow-up at +3,513.
Closing that inherited overage is outside the authorized finding 5 slice.

The lead-approved addition is reservation-only reconciliation with persisted
award evidence. An expiry annotation cannot replace it because a unit lease can
end while its service root remains live or unexpiring. The existing Job sync
transport and boundary release owner are reused. Reverting this contract and
node observation path would restore the capacity leak.

The added mechanism not sized by the frozen design is the admission guard plus
its verifier and enforcement probe. A response check cannot prevent execution,
and a configuration snapshot cannot preserve a later Pod exemption. The guard
is kept for that reason. Removing it would reopen finding 1. Executable ownership
adds a private copy because a selected-engine subprocess cannot transfer an
in-process cache lease to its parent. The copy is made under that lease and has
one explicit cleanup owner. Neither addition justifies the area's pre-existing
budget overage. The review did not establish safe cuts sufficient to close it.

The shared materializer replaces the parallel installer preparation path and
removes CLI duplication. The polling fix adds no mechanism. W9 owns the proposed
private-log and lifecycle cuts, so those paths were not changed.
