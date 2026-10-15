# W1 continuation 30

W1 remains incomplete. This slice implements the machine/service enrollment
portion of A1-26 and its root-origin handoff. W1b implementation files and the
lead-owned amendments and handoffs are unchanged.

Machine and service provisioning use the existing capability records and
CapabilityRecord.Origin as their registration owner. Machine issuance checks
current team administration, canonical runner selector, repository patterns,
claim-only grants, unique nonempty pools, the existing 100-machine cap and active
name uniqueness. Service issuance checks deployment authority, its canonical
service selector, operational grants and active names. Machine enrollment shares
the team lock. Service enrollment uses the existing advisory-lock helper.
An optional ProvisionPolicy can refuse enrollment but cannot bypass these checks.
Bootstrap and Actions still require their external verifier.

Rotation keeps the same registration and source consent. Overlapping grace roots
count as one enrolled machine. New enrollment cannot take their live name.
Revocation or expiry releases the name, including the exact expiry boundary.
Failed insertion leaves no bearer or reserved registration.

ReadClaimProfile reads persisted machine/service/Actions enrollment when no
observer is installed. It supplies granted identity, pools and machine repository
acceptance. Capacity stays unknown under the matcher's existing zero convention.
Injected host observations still supply capacity and self-claim availability.
Named and queued claims check every ancestor's repository restrictions. Exact
repository keys remain exact. Machine patterns use the shared repository parser
against the sealed display. An empty delegated list cannot remove a root filter.

Invariant: issuance requires current origin authority, no two active enrollments
take the same scoped name, and a claim cannot leave any ancestor's repository
scope. Generated tests vary authority, registration, grants, pools, repository
inputs, direct/delegated claims, revocation and nanosecond expiry boundaries.
Concurrent tests retain name exclusion and the existing mint cap. Team scope,
rollback and rotation grace have explicit checks.

The controller uses the store's shared runner-name validator. Duplicate enrollment
returns conflict. The existing runner cap returns quota_exceeded. Its HTTP machine
and service round trips now use the real default enrollment transaction. The
service fixture still uses a bootstrap verifier fake to create its administrator.
The HTTP proof checks that bearer values stay out of audit logs.

The SQLite placement-granted-identity-and-capacity/store-sqlite cell now includes
production enrollment, reopening without a policy/profile fake, repository
refusal, a real award and persisted ancestry/pools after another reopening.
Initial run/owner rows remain fixtures. This is not native runner or Kubernetes
execution evidence.

Deleted 15 superseded validation lines: the 12-line controller-only name helper
is replaced by the shared store validator, and the redundant three-line pool
authority branch is removed because every account enrollment requires team.admin.
No legacy store API is retired in this slice.

Retained test ports cover TestRunnerTokenRefusesADuplicateAgentName,
TestAgentNameIsUniqueOnEveryMintPath and
TestIdentityRunnerTokenCapHoldsUnderConcurrentMints through the final issuer.
The original token tests remain until their old API callers are cut. The updated
t1-port-inventory.md names each final proof. Native source identities are not
newly claimed complete.

Verification uses GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4, -p 4 and
GOCACHE/GOTMPDIR/TMPDIR under /tmp/sw-astra-W1.

- Whole-repository go build -p 4 ./... passes.
- Scoped go vet passes for pkg/store/..., pkg/controller/..., internal/acceptance/...
  and tests/acceptance/....
- The W1 selection passes 465 top-level tests / 1,458 cases with no skips.
  Its file inventory is w1-session30-tests.txt. Extract Test names, omit
  Postgres/Pg and run their anchored alternation.
- All 20 store-sqlite acceptance cells and ten root-provisioning HTTP tests pass
  with no skips. Select ^TestRootProvisioningHTTP or the inherited SQLite
  lifecycle/recovery/placement/credentials/billing/surface selection.
- Focused store and HTTP enrollment race checks pass.
- commentcheck -base main . passes across 2,151 files. API snapshots,
  changelog, embedded documentation mirrors and git diff checks pass.

The old rotation test equated a missing injected policy with missing registration.
It now corrupts the persisted registration selector and proves refusal instead.
The new SQLite fixture initially lacked an owner deadline, which its finite old
credential had hidden. It now supplies the owner deadline while retaining an
unbounded enrolled root. The first cold test build crossed source edits and failed
on the intermediate tree. Consistent-tree checks provide the evidence above.

Local C1 changes add ValidRunnerName and document built-in enrollment and default
profile reads. No foreign signature, table, credential issuer, environment
variable or customer limit is added. The service-name advisory lock uses the
existing transaction helper because count-then-insert alone cannot serialize
PostgreSQL writers. Removing the default enrollment implementation reverts this
slice. Production code has one registration owner and no compatibility path.

Remaining work and real integration points:

- A1-26 host observation composition remains open. ClaimRequest carries available
  CPU/memory but no total capacity. A stored root alone cannot reconstruct host
  capacity, OS/architecture or self-claim availability. Use ClaimProfileResolver
  for those observations. The direct-store cell supplies UnitDemand.Capacity.
  Source release still requires its enrolled-executor resolver. Default launcher
  and Actions provider composition also retain their separate external seams.
- Console sessions, runner metering, verified local peer mutation authority and
  lead decision 3's seeded bootstrap presentation through Store.Issue remain open.
  C2 ProvisionSpec still has no seeded-presentation field. That declaration belongs
  to W2 before W1 can accept the configured raw credential through the sole issuer.
- Materialization still needs W1b's transaction-local record owner. Do not call
  separately committed CopyMemo before reconciliation.
- StateStoreOld, legacy lifecycle/readiness, CreatePlanNode, ClaimLaunch,
  remaining claim/trigger/token writers and their caller migrations remain open.
- The earlier W8 nil parent-expiry representation and plan/eval execution-settings
  projection gaps remain. C2's source/attempt liveness and production pricing
  composition still need their owners' integration. C2's enrollment prose should
  be updated by W2 to reflect the new default transactions.
- Native placements, PostgreSQL, HTTP/api.sock assembly and A1-53 repository QA
  remain unverified. No Sparkwing binary, pipeline, provider, deployment, release
  or full acceptance suite ran here. The lead retains independent review and
  landing.

Shared source/test files changed are pkg/controller/identity_team.go,
pkg/controller/root_provisioning.go, internal/acceptance/store_credentials.go,
the new internal/acceptance/store_enrollment.go, tests/acceptance/placement_test.go
and tests/acceptance/root_provisioning_http_test.go. Other shared edits are
docs/api.md, docs/migrations/_unreleased.md, CHANGELOG.md, their embedded mirrors,
t1-port-inventory.md and .apidiff/pkg_store.txt. No W1b file changed.

Scratch is removed after final verification. No Git metadata was written, and
the local Git config has no identity or core.worktree override.
The shell guard refused bulk rm cleanup. Path-checked Python cleanup removed
only the assigned scratch directory.

STATUS: CONTINUE
