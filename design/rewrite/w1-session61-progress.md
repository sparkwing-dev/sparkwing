# W1 session 61

Store failures start at **15** and end at **15**, with **1,203 passing top-level
tests** in the final full run (221.941 seconds). The supplied 214-test list is
superseded by the fresh runs. The same fifteen tests fail at both ends. They
belong to W1b's fixtures or retired baseline tables. W1b's files are unchanged.
Exact errors are in [w1-session61-store-failures.txt](w1-session61-store-failures.txt).

## Implemented

Tenant now implements C2's RunnerInventoryReader. ReadRunners reads live machine
ancestry, accepted capacity polls and current owner, job and eval awards under
one store snapshot and clock. The controller's default adapter now works.
Runner lists, queue headroom and runner gauges consume that existing adapter.

Invariant: only current same-team authority contributes runner activity, and
equal names never merge unrelated authority. Roots use their persisted identity.
Cloud awards keep their billing reservation through exchange. Here jobs group
under their original here award, separately from a requesting machine's
registration. Revoked ancestry, expired leases, replaced generations and stale
eval source/controller fences contribute no activity. A cancelled live holder
stays visible during drain. Corrupt observations return no partial snapshot.

The single issuance path records accepted award liveness. Node, owner and eval
renewal record accepted liveness in their transactions, including the direct
Tenant renewal methods. Renewal leaves saved labels and capacity observations
unchanged. Unknown headroom stays absent. Placement still checks the poll's own
timestamp, so renewal does not extend the placement hold's capacity observation.
An observation-write failure rolls back its award or renewal.

The first new test reproduced the missing tenant adapter. Generated tests cover
tenant/name isolation and current fences. The real storetest fixture uses sealed
admission, plan acceptance, enrollment, claims and both renewal interfaces.
The real HTTP handler reads the default store adapter. Existing fake-reader
checks remain because they test missing dependencies and corrupt foreign results.
Their missing-reader case now explicitly injects an absent reader.

## Deleted and volume

Removed 11 production Go lines while sharing the registered-agent selection
between its existing reader and the new snapshot. No legacy API or production
file is retired this session. Store production adds 201 lines and removes 11,
for **+190 net production Go lines** against starting HEAD under the lead's filter,
including the untracked implementation file. The shared controller conformance
declaration adds two lines, including its blank separator.

Combined W1/W1b store delta against main is **-3,185 lines**, still **1,065 lines
over** the -4,250 budget. The added code supplies the previously missing C2
snapshot and accepted-renewal input using existing capability fields. Deriving
liveness from lease deadlines or joining holder names would violate that contract.
No new table, setting, environment variable, registry or foreign interface was
added. This is not a budget waiver or a completion claim. The larger cuts remain
in the raw intake and credential APIs and their callers.

## Verification

All Go commands disable workspace/VCS stamping, use /tmp/sw-gocache and run
tests with -p 4. The final full suite has the fifteen inherited W1b failures.
The tenancy guard caught an unscoped last-used UPDATE during the first ending
run. Its predicate now includes the resolved ancestor's team. The guard passes
without an exception, including in the final full run.

- Whole-repository go build ./... and scoped store/controller vet pass.
- Inventory, rollback, local/here, claim, renewal and registered-agent checks pass.
  The generated inventory property and real default HTTP adapter pass.
- Focused store/controller race checks pass. The final eval-source and controller
  fence refinements also pass their focused ordinary checks.
- Controller claim/renewal, queue/inventory and launcher/W8 acceptance selections
  pass with fake Kubernetes boundaries. No native placement cell was unskipped.
- Existing W1 cells pass in the final suite: EnrolledClaims,
  GeneratedDependencies, CacheScope, ReferenceEdges, RecoveryCapacityAdoption,
  RecoveryCapacityMaintenance, Transitions, Sessions, ProvisioningOrigins,
  ScheduleBoundary, LauncherSync, PlanSeals and ClaimMatching.
- Commentcheck against main, formatting, whitespace, store API snapshot and
  source/embedded documentation mirror checks pass.
- PostgreSQL, native sockets, Kubernetes and product pipelines remain lead checks
  under the swarm brief. No service or Sparkwing candidate was started.

## Contracts and remaining integration

C1 documents the implemented reader and accepted observation behavior. No foreign
contract signature changed. C2/C4 still need an enrolled maximum-concurrency input.
W8/W9 still need the already-requested queue-depth observation input. Both public
numeric fields remain zero until those contracts can carry the facts. Capacity
polls supply only cores and memory. Full capacity reporting is therefore open.

Trusted schedule/App intake remains open on the existing C4 tagged committing
origin and W2 transaction-readable verified-delivery contracts. An arming account
or verified delivery is not a credential. AuthorityRef alone cannot represent
those origins. No credential was synthesized and no foreign contract was extended.
The triggerIntake/triggerRows/raw insertion cut must follow that integration.

Ordered placement, rotation grace, Cloud metering, owner recovery and SyncLauncher
fences retain their earlier implementations and full-suite regression coverage.
The credential mutation, maintenance, secret-read, team, GitHub App and run-control
adapters already exist and were not rebuilt. Remaining W1 work also includes
sealed GitHub completion facts instead of TriggerEnv, publisher metric binding,
usage retry identity and the raw run/trigger/token API cut. W1b owns its remaining
fixture ports, obsolete storage tables and billing/blob/log integrations.

## Shared files and friction

Shared production file: pkg/controller/agents.go adds compile-time conformance.
Shared test: pkg/controller/runner_inventory_test.go adds a real default-adapter
proof and makes the missing dependency case explicit. No production caller was
migrated or removed outside pkg/store. Shared documents are C1, t1-port-inventory,
CHANGELOG.md, the migration guide, their embedded copies and .apidiff/pkg_store.txt.
Lead-owned handoffs and amendments are unchanged.

Friction is the W1b ownership boundary and the missing foreign intake/observation
inputs above. No invariant or guard was relaxed. No Git writes ran. HEAD remains
375325ec8 and local Git config has no user or core.worktree override. Scratch is
removed after final checks, and /tmp/sw-gocache is retained.

STATUS: CONTINUE
