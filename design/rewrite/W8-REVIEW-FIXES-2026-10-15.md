# W8 adversarial review fixes

Seven assigned findings are fixed in code. Finding 3 remains blocked. Finding 5
has a partial deadline fix, but its reservation recovery remains blocked. Findings
6, 7, 8 and 9 were left to W9. No git writes were performed.

| Finding | Invariant | Status and evidence |
| --- | --- | --- |
| 1 | Runnable Jobs and Pods retain the validated execution, isolation and cleanup constraints. | Implemented the review's verified admission exemption option. LaunchOne refuses before claiming unless the exact denying guard is installed and an actual dry-run request proves enforcement. The guard preserves canonical namespace exclusions for later webhook/binding changes, namespace status/finalize edits and LimitRanges. Existing bindings require exclusions even when their policy is presently unrelated. Job/Pod CREATE/UPDATE and arbitrary subresource rules share resource-prefix matching. The initial real LaunchOne regression ran red. Guard absence, weakening, inactive enforcement, mutable-label exclusions and future policy widening have regressions. Both charts ship matching guard objects and minimum RBAC. Native Kubernetes admission execution remains for the lead. |
| 2 | Cleanup deletes only this publication's Secret instance. | Fixed. Both fuzz seeds ran red through LaunchOne: a renamed invalid Job and Secret AlreadyExists deleted unrelated Secrets. Teardown no longer invents a Secret name. Successful creation records its exact UID before activation, and explicit deletion always uses that UID. Uncertain creation relies on Job owner garbage collection. A suspended-Job test now uses real publication rather than injecting missing cleanup ownership. |
| 3 | Every placement reaches concrete shared owners through its installed entrypoint. | Blocked. The executable subprocess test calls MainWithVersion without factory replacement. All five commands remain red behind rewrite_integration. Missing contracts are listed below. No replacement factories that merely return another unavailable error were added. |
| 4 | The canonical award supplies every immutable input required by its executor. | Fixed. Real store, registered controller HTTP handlers, shared client and launcher claim/exchange reproduce missing plan/eval resources before the fix. All three kinds now pass. The producer projects sealed request or accepted-run resources for owner units and retains persisted job resources for work. |
| 5 | Optional ancestor expiry constrains authority, and expired unit ownership cannot retain capacity. | Partial. Claim, Job construction, source and bootstrap share the existing deadline projection and accept an absent ancestor expiry when execution is bounded. Corrected tests no longer reject nil root expiry. Real KubernetesBoundary reservation still rejects an unexpiring root, reproduced by a red rewrite_integration test. Recovery cannot check a reservation-only fence through the current sync contract. No substitute timeout or fabricated Job UID was added. |
| 10 | Effective build inputs, compilation and executable lifetime have one owner. | Fixed. Real Go fixtures first reproduced missing checksums and then the pruneable returned path. Shared materialization prepares effective checksums before hashing, preserves overlays and verifies exact SDK selection. The selected engine copies the executable while holding its cache lease. Source acquisition, verification and cache warm close the caller-owned copy after use. Tests cover SDK replacement refusal and execution after cache pruning. |
| 11 | Pin discovery, mutation, verification and application address the same pipeline directory. | Fixed. Corrected Go/TypeScript fixtures use the CLI's checkout-root input and failed before the fix. The engine reads/edits .sparkwing, preserves root application manifests and passes the staged checkout root required by the actual verifier. Concurrent-edit and failed-verification fencing pass. |
| 12 | Documented bootstrap commands provision a valid administrator. | Fixed. Both documented commands failed the real validator before correction. Tests execute the swk_ generators through a real store's EnsureBootstrapAdminToken and verify administrator readiness. |
| 13 | Completion has identical scheduling effects through every receive path. | Fixed. A deterministic Loop.Run regression reproduced idle exit after an empty claim raced with completion. Common finish now resets polling. The regression and its race run pass. |
| Stale comments | Operator guidance and comments describe the current contracts. | Fixed all ten table rows, related chart assertions and the full-chart README bootstrap/auth narrative. Wingd edits only remove or correct comments. |

## Contract changes needed

The brief requires: "If it changes another area's contract, stop that piece and
put it under 'Contract changes needed' in your report." No answer authorizing
these foreign changes arrived during this pass.

Finding 3 needs authenticated remote private RunSnapshot/NodeRow reads and native
run/job transitions, AuthenticationReader/BindingChecker process authority,
shared source-access client transport, native Cloud plan/eval dispatch, and
production network-probe/node-cleanup integrations. Those are absent behind the
empty Runtime. HOSTING.md and the controller-wire contract already identify
parts of these obligations. A local factory test cannot establish them.

Finding 5 needs reservation observations containing the existing award LeaseRef
and exact NodeUID/ReservationID, without a Job reference. Sync needs matching
reservation retirement references. The store can reuse launcherAwardTx under
launcher ancestry. W8 would persist the award fence on the node and release only
a matching stale reservation after Job absence and container vacancy are proved.
The current LauncherObservation.Validate requires an actual Job UID and removal
responses contain only LauncherJobRef. Status reads do not replace this authority
check, and renewal would change ownership rather than observe it.

## Shared edits

The only pkg/controller edit is engine_claim.go: import runspec and project
plan/eval resources in claimedUnit, while retaining nil execution for trigger
awards. It adds eight net production lines. Claim and exchange share this owner.

Finding 10 also changes internal/bincache/materialize.go, three internal/source
files, and cmd/sparkwing compile/cache-warm/upgrade-verifier code and related
tests. These remove duplicate materialization and carry executable ownership to
its consumers. The W8-local engine contract records ExecutableDirectory,
CachedExecutable and SDKCommand.Close. Lead-owned handoff/amendment files were
not edited.

## Verification and limits

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

The explicit rewrite_integration run remains red for findings 3 and 5. No new
acceptance cell is claimed for those paths. Native Kubernetes CEL/admission,
network enforcement and executable deployment remain unverified in this sandbox.
The guard's Forbidden response check follows the documented
[Kubernetes admission-policy denial format](https://kubernetes.io/docs/reference/access-authn-authz/validating-admission-policy/).

GOCACHE is /tmp/sw-gocache throughout. GOTMPDIR directories under
/tmp/sw-astra-W8 are removed at completion. The shared build cache is retained.

## Production delta and mechanisms

W8-owned production Go paths add 297 lines and delete 83 in this pass, net +214.
Against main, excluding tests, testdata, acceptance and contract declaration
paths, W8 adds 10,327 and deletes 7,094, net +3,233. The budget is -2,300, so the
area remains 5,533 lines over budget. It entered this pass at +3,019.

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
