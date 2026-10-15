# W1 continuation 29

W1 remains incomplete. This slice completes the store/controller part of A1-38
and lead handoff decision 10. W1b implementation files and the lead-owned
amendments and handoffs are unchanged.

Store.ReadLauncherAwards now reads launcher ancestry, current unit fences,
request/spec/plan seals, job settings and parent expiry in one transaction.
It selects only awards belonging to the persisted root and service subject.
Plan and eval claims retain their owner fences. Eval checks its independent
claim generation, immutable source generation, unresolved expansion and current
controller lease. Expired, cancelled, terminal and replaced work is excluded.
Bootstrap exchange keeps the award identity and supplies the current credential
row without returning reusable bearer material.

The controller's default LauncherSyncReader uses that snapshot. It validates
unique Job and award observations and returns each exact matching award or the
observed namespace/name/UID for cleanup. Sync describes the original bootstrap
delivery even after exchange. It changes no lifecycle state, lease, reservation
or billing row. The elected reaper remains the recovery writer. Failed snapshot
reads return no decisions.

Invariant: synchronization exposes only current awards owned by the live launcher,
and cleanup selects only the reported Job instance without changing another
award. Generated tests vary holder, kind, unit ID, claim generation, nanosecond
lease/deadline boundaries, cancellation, terminal state and eval ownership.
Tests also cover roots sharing a service name, narrowed launcher delegation,
ancestor revocation, storage errors and malformed persisted execution settings.

Deleted 220 superseded source/test lines: pkg/store/launch_sync.go (154),
Client.LauncherSync in pkg/controller/client/claim.go (13), and
TestLaunchBilling_ReleaseRefundsAndTheFirstBeatBills in
pkg/store/claim_run_test.go (53). LaunchJob, LaunchJobResult, LaunchJobState,
their constants, CapacityWaitDetail, ReleaseBackoff, cancelLaunchClaim and
releaseLaunchClaim are gone. There are no remaining Go references. The remaining
test in claim_run_test.go is unchanged.

The removed billing test is ported to the existing
TestMeteredUnitUnstartedCloudReleaseRefundsReservation and
TestMeteredUnitCloudRenewalStartsComputeWithoutAnAttempt. Before compute,
release refunds the reservation. After the first Cloud renewal, release settles
the job minimum without consuming an execution attempt. The retired sync-side
blanket refusal is replaced by the final ReleaseUnit/accounting transaction.
No billing implementation was changed.

The new placement-run-host-to-launcher/store-sqlite acceptance cell exercises real
claim, exchange, release, owner redispatch, replacement and default controller
synchronization through three reopenings. It checks exact old-Job cleanup and
unchanged replacement generation, capability and attempt ordinal. Its enrollment
and initial run/owner rows are fixtures. It does not claim native placement,
Kubernetes execution, W8 consumer composition or installed-client evidence.

Verification uses GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4, -p 4 and
GOCACHE/GOTMPDIR/TMPDIR under /tmp/sw-astra-W1.

- Whole-repository go build -p 4 ./... passes.
- Scoped go vet passes for pkg/store/..., pkg/controller/..., internal/acceptance/...
  and tests/acceptance/....
- The inherited W1 selection plus launcher snapshot tests passes 457 top-level
  tests / 1,448 cases with no skips. The file inventory is w1-session29-tests.txt.
  Extract Test names, exclude Postgres/Pg and run their anchored alternation.
- All 20 store-sqlite acceptance cells pass with no skips. Select
  ^(TestLifecycle|TestRecovery|TestPlacement|TestCredentials|TestBilling|TestSurface)$/.*/^store-sqlite$.
- Focused store, controller, client, SQL-scope and bearer-omission checks pass.
  The final launcher snapshot and HTTP selection also passes with -race.
- commentcheck -base main . passes across 2,148 files. API snapshots,
  changelog, documentation mirrors and git diff checks pass.

The acceptance fixture initially omitted owner redispatch after ReleaseUnit.
Adding the required public readiness transition fixed its replacement claim.
The delegation fixture also needed an explicit expiry within its finite parent.
One broad run crossed a source edit: the SQL-scope test read the new correlated
query against its earlier compiled exemption. The exemption is removed and the
consistent-tree rerun passes. The final query scopes each resource lookup to
the candidate capability's team and binding and needs no new SQL exemption.

The local C1 contract adds LauncherAward and Store.ReadLauncherAwards. It fills
C2's declared snapshot seam because separate public reads cannot provide one
ancestry/resource snapshot. C2's interface and the shared wire are unchanged.
No table, service, credential issuer, environment variable or customer limit is
added. Reverting this slice removes the snapshot and its default adapter.

Contract changes needed in other areas: W8's CloudJobBinding and cloudBinding
require a nonzero ParentCapabilityExpiry. C2 permits provisioned service roots
with nil ExpiresAt. The existing claim projection and this snapshot preserve that
nil value. The HTTP fixture proves the store/controller path accepts such a root,
while W8's constructor rejects the resulting shape by inspection. W8/W2 should
represent an absent parent expiry through the Cloud binding rather than invent
a root lifetime. No foreign signature was changed. C2's prose saying C1 still
needs a launcher snapshot also needs its owner's update.

Remaining W1 work from the earlier handoff remains open:

- A1-26 production machine/service provisioning, active-name/limit checks and
  ClaimProfileResolver composition. Claims in these tests use the contract fake.
- Console session authority and runner metering, verified-local mutation adapters
  and lead decision 3's gated seeded bootstrap presentation through Store.Issue.
- Transaction-bound materialization integration with W1b's record owner. Do not
  call the separately committed public CopyMemo before reconciliation.
- StateStoreOld, legacy lifecycle/readiness, claim/trigger and token surfaces and
  their remaining callers. ClaimLaunch and its old controller fixtures remain.
- The existing controller claim projection clears Execution for plan/eval, while
  W8's Cloud claim adapter requires resolved resources. That adapter composition
  and production origin/pricing composition still need real integration.
- Native Kubernetes observation/adoption/UID-fenced teardown, PostgreSQL,
  HTTP/api.sock placement assembly and A1-53 real-repository QA. No full acceptance
  suite, Sparkwing binary, pipeline, provider, deployment or release ran here.

A1-27's lifecycle/accounting join and earlier completed amendment entries remain
covered by the inherited tests. A1-28/29/31-35 remain with W1b under the ownership
split. The previously reported C2 sameFence attempt-zero issue and broader host,
wire and masking acceptance failures were not changed or reclassified here.

Shared source/test files touched are pkg/controller/engine_launcher_sync.go,
pkg/controller/engine_launcher_sync_test.go, the new
pkg/controller/engine_launcher_store_test.go, pkg/controller/client/claim.go,
the new internal/acceptance/store_launcher_sync.go and
tests/acceptance/placement_test.go. The only W1b caller-fixture edit is deletion
of the superseded billing test from pkg/store/claim_run_test.go, under the lead's
caller-migration exception. Other shared edits are c1-data.md,
t1-port-inventory.md, docs/api.md, CHANGELOG.md, their two embedded mirrors and
the three changed .apidiff snapshots. No lead ledger changed.

The lead retains independent review, pipelines, Git staging/commit and landing.
The existing broad host/wire failures and required native cells still prevent
an area-complete claim. Scratch is removed after final verification. No Git
metadata was written, and no local identity or core.worktree override exists.

STATUS: CONTINUE
