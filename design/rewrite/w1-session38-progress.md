# W1 continuation 38

W1 remains incomplete. This session starts at 3bdd4bad0. The lead owns review,
committing, pipelines and landing. W1b implementation and test files are unchanged.

Removed the unused Tenant CreateMemberToken, CreateMachineToken, TeamTokens,
TeamToken and RevokeTeamToken APIs. Removed TokenFamily and its constants,
TeamTokenFamily, Token.Family and MachinePrincipal, their private validators,
readers and mint loop. Eleven exported symbols leave the API snapshot.
Production consumers already use capability provisioning and management.
The remaining legacy scope and acting-kind helpers move into tokens.go.

The new TestProvisionMemberCurrentScopesProperty generates 40 cases over
current roles, grant subsets and removed memberships. It checks exact requested
subsets, immutable input and no persisted credential or returned bearer after
refusal. Its expected permissions are independent of the implementation's role
lookup. Existing enrollment properties retain machine names, scope, current
administration, caps, rotation and reuse. The membership-drop race now calls
ProvisionCapability for both root kinds, with a sealed session key and real
account session. PostgreSQL execution is pending because its test URL is absent.

Superseded raw-token tests retire with their APIs. The port inventory names the
retained current-authority proofs. Generic identity/deletion fixtures still
exercising raw tokens now seed with the existing CreateTokenWith API. The
legacy sliding-expiry assertions remain. No new production shim or member-root
quota is introduced. The unused seedMinter fixture is deleted; seedMatchNode
moves beside its remaining claim-attention consumers.

Net production Go lines, excluding tests, testdata, acceptance and declarations:

- This session deletes 333 and adds 21, net -312. All are in internal/store.
- W1+W1b is +3,261 against main, versus its -4,250 budget: 7,511 lines over.
  No budget waiver is requested.

Verification uses GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-gocache and GOTMPDIR/TMPDIR beneath /tmp/sw-astra-W1.
Create that scratch directory before reproducing checks. The test manifest
w1-session38-tests.txt gives anchored selectors for 78 distinct passing current
top-level tests in internal/store and internal/controller. PostgreSQL skips are excluded
from that passing manifest.

- Whole-repository go build -p 4 ./... passes. Scoped vet passes for internal/store/...
  and internal/controller/.... Both changed test packages compile.
- Current authority, enrollment, inventory, rotation, revocation and HTTP member
  issuance tests pass. The SQLite credentials-delegation-subset proof passes.
- SQLite plan seals, transitions, generated dependencies, ordered matching,
  launcher synchronization, owner recovery and eight recovery proofs pass.
  These retain lifecycle-request-and-plan-seals, lifecycle-transition-replay-and-fencing,
  lifecycle-generator-barrier, placement-granted-identity-and-capacity and
  placement-run-host-to-launcher coverage. No native cell is newly activated.
- W7's terminal record selector, materialization and reconciliation properties
  pass. The record-copy seam remains a transaction-local fake in these checks.
- Rotation deadline, the five Operator credit settings and launcher snapshot
  properties pass. The named lead decisions were completed before this session.
  The moved W1 harness checks and six dead contract tables are not reimplemented.
- API snapshots, documentation symbols, embedded-document/changelog drift,
  changelog headings, commentcheck -base main and diff whitespace checks pass.

Seven retained tests fail on unchanged baseline behavior. Three identity token
fixtures fail on the absent tokens table; two deletion fixtures fail before
seeding because their session key cipher is missing. The tenant table ownership
and column checks fail because tenantTables/operatorTables still name retired
schema and omit rewrite tables. Restoring the session's original Go files through
an overlay reproduces all seven failures. TestIdentityTeamScopedWritesMissAnotherTeam
now passes after its retired token-inventory section is removed; the current
capability inventory properties retain the tenant boundary. Updating the table
catalog affects production purge and SQL guard coverage and remains W1 work.

Remaining work and real integrations:

- W1b must implement the existing CopyMaterializedRecords transaction interface.
  Physical copies and byte integrity are not proven by W1's fake copier.
- W9 ExecutionJob and its embedded JobDetail still omit committed output and
  manifest selectors. W9 must transport them and W3 must consume them. W7 owns
  subsequent edge authorization and byte verification. No foreign contract edit.
- C2 ProvisionSpec still needs the seeded bootstrap presentation before the
  approved one-use Store.Issue bootstrap flow can be composed.
- W1 retains console/verified-local authority, SetRunnerMetered, console service
  issuance, current host/source observations and production pool-rule composition.
- StateStoreOld, raw lifecycle/readiness, claim/trigger/token APIs and their
  callers remain. W1b credits_test.go still calls Store.RotateToken; its caller
  must migrate before W1 removes that raw rotator. W1b files are untouched.
- PostgreSQL locking, native placements, api.sock, Kubernetes, provider billing
  and whole-repository pipelines/QA remain lead or owner integration evidence.

The only shared consumer file changed is internal/controller/team_tokens_test.go;
its retired member-mint test is covered by the current HTTP issuance race and
new store property. Other shared edits are C1, the test-port inventory,
.apidiff/pkg_store.txt, CHANGELOG.md, the migration guide and embedded mirrors.
Lead-owned handoffs and amendment files are unchanged.

Friction is the reproduced legacy-schema/session-fixture and table-catalog
failures above. No new production mechanism, setting, service or customer
restriction is added. No Git write, Sparkwing binary, pipeline, external message,
deployment, release or harness-memory write runs. Local Git configuration has
no user identity or core.worktree override. Assigned scratch is deleted after
verification; the shared build cache is retained.

STATUS: CONTINUE
