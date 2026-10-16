# W1 session 62

The fresh starting store suite has **15 failures and 1,203 passing top-level
tests**. The supplied 214-failure inventory is stale. The ending suite has **15 failures and 1,200 passing top-level tests**
(205.358 seconds). The failing-test set is unchanged. Three passing
legacy tests retired with their APIs. The fifteen inherited failures are listed in
[w1-session62-store-failures.txt](w1-session62-store-failures.txt).

## Implemented and deleted

Invariant: a reporting backend reads runs, jobs and events from one explicit
tenant. The controller previously scoped only its run readers. Job and event
readers fell back to the default team. The backend now takes a concrete Tenant
at construction and uses it for every reporting read. Its RunReader interface,
Scoped method and Store accessor are deleted. No replacement interface is added.

TestStoreBackendTenantReadsProperty failed on the original backend's first
nondefault job listing. It now generates 24 tenants and checks their own reads,
default-team isolation and reverse-direction isolation against real SQLite.
The retained backend round-trip fixture uses the current schema.

Store.GetRun, GetNode, ListNodes, ListEventsAfter and GetActiveDebugPause are
deleted. Their callers explicitly select Tenant. Cross-team ListRuns and
GetLatestRun move to Operator. The compiler rejects the removed Store calls.
The internal defaultTenant helper is confined to tests. The two source-scanning
twin guards retire with their production targets and backend fallback.

Tenant.GetSecretForPipeline is deleted. ReadSecret retains current authority,
declared names, pipeline/shared precedence and pool restrictions. Existing
secret-read properties cover that behavior. The old raw fallback test retires.
Encryption and tenant-key tests inspect exact rows with GetSecretRow.

The session removes **88 production Go lines** and adds 16, for **-72 net**
against starting HEAD under the lead's filter. Store alone is **-48 net**.
The three deleted test files contain 287 lines. Combined W1/W1b store production
is **-3,233 lines against main**, still **1,017 lines over** the -4,250 budget.
This session adds no production mechanism, table, setting or environment input.
It is not a completion or budget-waiver claim.

## Verification

All Go commands disable workspace and VCS stamping, share /tmp/sw-gocache and
run tests with -p 4. Whole-repository go build ./... and scoped vet pass.
Backend/capacity checks and the generated reporting isolation race test pass.
Store fixture packages pass. All affected reader packages compile, including
internal/nodehost, controller, CLI and acceptance packages.

Credential default-adapter, credential-maintenance transaction and tenant-bound
Git credential HTTP acceptance checks pass. Controller reporting authority,
retired-route and real dashboard log read/foreign-authority checks pass.
Commentcheck against main, formatting, whitespace, the full API snapshot and
source/embedded documentation drift checks pass.

The broader focused store selection still reports the inherited W1b writer-guard
failure for storage_commit_receipts and storage_reservations. It was not hidden
or relaxed. Native sockets, PostgreSQL, Kubernetes and product pipelines remain
lead checks under the swarm brief. No new native acceptance cell is unskipped. The ending suite retains passing
StoreEnrolledClaims, StoreClaimMatching, StoreLauncherSync, StorePlanSeals,
StoreGeneratedDependencies, StoreScheduleBoundary, StoreSessions,
StoreTransitions and the store's reconciliation/recovery cells.

## Ownership, contracts and remaining work

W1b production is unchanged. Handoffs explicitly authorize minimal caller
migration when W1 removes a store API. The only W1b fixture changes are the
reader substitutions in credit_billing_start_test.go, credit_rates_test.go and
credits_test.go, plus the required teststore import in the first file. They do
not change metering, seeding, expectations or assertions.

Trusted schedule/App admission still needs the previously requested C4 tagged
committing origin and W2 transaction-readable verified delivery contract.
AuthorityRef alone cannot represent an arming account or verified delivery.
No credential is synthesized and no foreign contract is extended. The
cronLauncher/startGitHubAppRun -> triggerIntake/triggerRows -> raw insertion cut
remains open on that contract. Implementation against the current capability
and IntakePolicy interfaces already exists, with fake policies covering verified
facts. That does not establish a real schedule/App integration.

Ordered placement, rotation grace, Cloud metering and the SyncLauncher store
fence remain implemented from earlier sessions. Other remaining integrations
are C2/C4 maximum-concurrency and queue-depth observations, publisher metric
binding, additive usage retry identity and sealed GitHub completion facts.
Raw run/trigger/token API removal and W1b's billing/blob/log retirements remain.
No signature in another area's contract changed.

## Shared files and friction

Shared production changes are internal/backend/store_backend.go and
pkg/controller/browser_reads.go. Both are caller migrations for the store cut.
Backend tests, web capacity/log-pretty fixtures and controller reader fixtures
migrate with them. The exact changed-file list is
[w1-session62-files.txt](w1-session62-files.txt). C1, the test-port inventory,
changelog, migration guide, their generated copies and the store API snapshot
are updated. Lead-owned handoffs and amendment files are unchanged.

Friction remains the W1b fixture ownership and missing foreign intake contracts.
No Git write ran. HEAD is eb69b6998. Local Git config has no user identity or
core.worktree override. Scratch is removed after final verification and the
shared cache is retained.

STATUS: CONTINUE
