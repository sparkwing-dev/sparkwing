# W1 continuation 28

W1 remains incomplete. This pass implements capability inventory and rotation
behind C2's existing CapabilityManager. W1b implementation files and the
lead-owned amendments and handoffs are unchanged by this session.

All nine merge conflicts have resolved file contents. API/auth/GitHub documents
use the integration version. Both migration-note blocks survive. API references
and OpenAPI are regenerated, and embedded mirrors are synchronized. The index
still lists nine unmerged paths because the brief forbids Git writes. The lead
must stage those resolutions and finish the merge.

Tenant and Operator now implement ListCapabilities, ReadCapability and
RotateCapability. Each operation rechecks the selected policy alternative,
live ancestry, membership and ownership in its transaction. Tenant inventories
contain owned account/machine controller credentials, or the team's inventory
for a current administrator. Deployment inventories contain service/operator
roots. No execution award, push-bound credential, node-host credential or
private authority field is returned.

Rotation uses Store.Issue with the original parent or provisioning origin.
It preserves owner, registration, grants, repository/data scope, pools, metering
and source consent. Its predecessor gets one bounded grace deadline and an
authority-generation advance in the same transaction. Repeated rotation,
expired targets, failed origin checks and failed writes return no bearer.
An expiry during origin verification also rolls back. Explicit revocation ends
grace and descendants immediately while leaving the replacement independent.
Owned-credential revocation now applies the existing token-owner relation,
including a member's own provisioned root.

Invariant: metadata and replacements remain within current ownership and scope,
and no failed transaction exposes a replacement. Generated properties vary
ownership, tenant, subject, audience, binding, revocation, membership, selected
policy, grants, expiry and nanosecond grace boundaries. Independent SQLite
connections test competing rotations and explicit revocation. No new test sleeps.

The local C1 contract adds CapabilityRecord.Origin with provisioning kind and
registration ID. It uses the existing authority envelope and adds no table or
foreign signature. A locked-root context in the issuing transaction permits
replacement through the existing root provisioner without widening scope.
It retains ordinary session-only member-root creation. The smaller option of
ordinary delegation cannot replace a root without changing its ancestry.
Bootstrap-root rotation uses current deployment token authority rather than
rerunning the one-use bootstrap gate. Machine/service rotations call the declared
origin policy. That production policy still needs enrollment/limit composition
and must recognize the locked replacement when checking active registration
names. Component tests use its contract fake. Removing the manager and this
transaction context reverses the implementation.

Deleted 150 legacy test lines across tokens_test.go and revocation_test.go.
The four ports are TestRotateToken,
TestRotateToken_ARevokeDuringTheMintIsNotUndone,
TestRevokeToken_ClampsRotationGrace and
TestRevokeToken_AlreadyEffectiveIsRejected. Their retained behavior lives in
capability_inventory_test.go, including stronger descendant, rollback and
concurrent checks. No legacy production API is deleted in this continuation.
Raw token/claim/trigger and StateStoreOld cuts remain open.

Verification uses GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4, -p 4 and
GOCACHE/GOTMPDIR/TMPDIR under /tmp/sw-astra-W1.

- Whole-repository go build -p 4 ./... passes.
- Scoped go vet passes for pkg/store/..., pkg/controller/..., internal/acceptance/...
  and tests/acceptance/....
- The inherited W1 selection plus the new manager tests passes 452 top-level
  tests / 1,435 cases with no skips. w1-session28-tests.txt lists the files.
  Extract Test names, exclude Postgres/Pg names and run their anchored alternation
  against pkg/store. The focused manager selection also passes with -race.
- All nineteen store-sqlite acceptance cells pass with no skips. Select
  ^(TestLifecycle|TestRecovery|TestPlacement|TestCredentials|TestBilling|TestSurface)$/.*/^store-sqlite$.
  The added cell is credentials-delegation-subset/store-sqlite. It verifies
  inventory, rotation, ancestry, origin persistence and revocation through two
  reopenings. It adds component evidence rather than native placement evidence.
- TestCapabilityManagementHTTPUsesStoreTransactions exercises the default store
  manager through the real controller policy, account session, CSRF, issuance,
  metadata, zero-grace rotation, replay refusal and owner revocation.
  The complete TestCapabilityManagement, TestGitHubRunnerAdminHTTP and
  TestPortDestinations selections pass after the two fixture/inventory corrections.
- commentcheck -base main . passes across 2,143 files. check-api-spec.sh,
  sync-docs.sh --check, check-changelog.sh and git diff --check pass. The store
  API snapshot is regenerated. Git has no local identity or core.worktree override.

The broader acceptance invocation fails. It recorded 5,294 passing cases and
2,991 explicit skeleton skips before returning failure. Its two W1 inventory/
fixture failures are corrected and pass their focused reruns. The route-port
row for the already-removed dependency mutation now uses the catalog's delete
disposition and canonical scenario ID. The GitHub missing-writer fixture now
injects a missing registry rather than assuming Tenant lacks its finished adapter.
The full suite was not rerun after those focused corrections. Remaining observed
failures belong to these integration paths:

- W3/W8 Cloud/bootstrap and hosted lifecycle callers omit SourceAttempt.
  Store holder transitions refuse them. The wire now declares the field and
  the controller forwards it, so the earlier missing-signature blocker is gone.
- Engine-amendment, engine lifecycle-body and removed-node-read fixtures disagree
  with the merged wire/route implementation.
- The OAuth roleless-session refusal fixture expects 403 after the completed
  store implementation now creates that session and returns 200.
- FuzzExecutorFailureMask's two seeds expose an SDK secret in the rendered
  host error. This remains W3's masking boundary.

None of these results is a passing full acceptance suite. No native PostgreSQL,
provider, pod, installed Sparkwing binary or pipeline ran. The lead retains
independent review, pipelines, staging and landing.

Amendment and handoff disposition:

- The earlier W1 work for A1-07/08/11, 13-18, 20-27, 40, 44/45 and 55 remains
  covered by the inherited selection. This pass advances the capability-management
  handoff and A1-20/21/24. Console-session authority remains unimplemented.
- A1-26 still needs production machine/service enrollment and ClaimProfileResolver.
  Console runner metering, verified-local credential mutations and the seeded
  bootstrap presentation remain W1/W2 integration work.
- A1-27's lifecycle helpers remain joined. Production pricing/origin composition
  and broader metered execution evidence remain open. Existing-award billing
  must stay immutable when the root's future-award metering changes.
- A1-28/29/31-35 remain with W1b under the ownership split. Materialization still
  needs W1b's transaction-bound record-copy seam for an unexecuted destination.
  W1 must not replace it with a separately committed CopyMemo call.
- The amended LauncherObservation now carries an award LeaseRef and a Kubernetes
  Job namespace/name/UID. C2 now declares LauncherSyncReader.ReadLauncherSync.
  Its store adapter and production binding remain W1 work. The old report's
  missing-wire-fence blocker no longer applies.
- C2 sameFence still equates claim-wide attempt zero with the current ordinal.
  W2 owns that correction. A1-53's native placement and real-repository QA remain
  with the lead and the owning areas.
- Lead decision 8 is reviewed: keep W1b's five billing-setting methods on
  Operator. They match C1's deployment ownership and retain the shared settings
  validation, locking, rate-table rule and rollback. No W1b file needed correction.

No foreign contract change is requested for the implemented manager. The
transaction-bound materialization seam remains the outstanding cross-area
contract request. No new customer limit, flag, environment variable, service,
credential issuer or compatibility layer was added.

Shared files changed for this slice are new
internal/acceptance/store_capability_management.go,
tests/acceptance/capability_management_store_http_test.go,
tests/acceptance/credentials_test.go and the minimal missing-registry fixture
change in tests/acceptance/github_runner_admin_http_test.go. There are no
production caller migrations outside pkg/store.

Shared prose changes are c1-data.md, t1-port-inventory.md,
t1-credential-routes.md, docs/auth.md and CHANGELOG.md. Generated changes are
.apidiff/pkg_store.txt, API references/OpenAPI and embedded mirrors. The nine
conflict paths are api/openapi.yaml, docs/api-reference.md, docs/auth.md,
docs/github-actions-runners.md, docs/migrations/_unreleased.md and the four
matching pkg/docs/mirror documents. Scratch is deleted after verification.

STATUS: CONTINUE
