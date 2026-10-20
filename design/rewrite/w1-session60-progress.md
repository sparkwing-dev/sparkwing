# W1 session 60

The fresh starting store suite had **35 failing top-level tests** and 1,178
passing tests. The ending full run had **16 failures and 1,195 passing tests**
in 280.6 seconds. The supplied 214-test list predates session 59.
The exact remaining errors are in [w1-session60-store-failures.txt](w1-session60-store-failures.txt).
W1b-owned files are unchanged.

## Implemented and ported

Sealed intake now enforces the existing hourly guards in its transaction before
inserting either a run or its owner. Replay spends no second slot. The guard
uses authenticated subject kind and ID within one tenant, so an account and a
machine sharing an ID have separate budgets. Its count is serialized across
replicas and does not use summed usage accounting. Refusals retain ErrComputeLimit
and expose canonical rate_limited with the existing five-second retry delay.
The original focused tests reproduced over-admission, a missing canonical error
and account/machine budget collisions before their fixes.

Eleven legacy run-rate tests are replaced by four sealed-intake tests. They
cover the rolling window, independent subjects and teams, concurrent admission,
atomic run/owner insertion and replay. Token-metered-only intake, raw pending-row
attribution and the uncalled oldest-ready diagnostic are retired. The concurrent
test now checks every error and requires exactly three successful admissions.

Deletion fixtures now provision member roots through real account sessions,
check current capability rows, request cooperative cancellation, seed whole-cent
checkouts and use the final repository and job fields. The watermark test now
checks replica storage accounting. The post-purge test proves the foreign key
refuses a deleted tenant's new row before its scheduled recheck. Business-event
expectations use the verified provider actor and nullable deployment team.

Tenant now implements C2's RunControlWriter. Debug pause creation and approval
resolution reauthorize the current member/local actor and exact tenant/run/job.
Text masks classified arguments and store-selected masked secrets through the
shared masker and existing PlanSecretOpener. Approval resolution uses the
existing node transition and derives the approver from the authenticated subject.
Resume shares the same committing authority check.

Generated properties cover selector substitution, membership loss, account/machine
identity collisions, encoded secret values and exact-body replay. Rollback tests
reject a public event or the final receipt update after state mutation, then
verify state, events and receipts all remain unchanged. The actual controller
routes reach these transactions in TestRunControlHTTPUsesStoreTransactions,
including real member issuance, replay, changed-body conflict and revocation.

## Local contract correction and mechanism added

C1 now declares run_control_requests, keyed by tenant, run, subject kind/ID,
operation and replay key. It persists the exact body hash and masked result in
the same transaction as state/events. It cascades with its run and participates
in tenant purge. Account deletion drops only the deleted account's receipts,
with a generated property preserving the surviving member's replay. A private receipt is needed because pauses can be replaced or
resumed while older request keys must still reject changed bodies. Putting the
hash in a public event would expose a digest of private input. The two existing
mutable control rows cannot retain all earlier keys. No setting or foreign
interface was added. C2's RunControlWriter signature is unchanged.

## Deleted and volume

Removed OldestWaitingReadyNodeForPrincipal, the token-table metering lookup from
run-rate admission and duplicated resume authorization. The removed legacy rate tests and helpers
occupied 316 lines. This session removes 78 production Go
lines and adds 251, for **+173 net production Go lines** in internal/store
under the lead's filter. The controller conformance declaration adds one shared
production line. No entire production file is deleted.

Combined W1+W1b store delta against main is **-3,375 lines**, still **875
lines over** the -4,250 target. Legacy intake/token writers still prevent the
larger cuts. This is not a budget waiver or a completion claim.

## Verification

- Whole-repository go build ./... passes with workspace and VCS stamping disabled.
- Scoped go vet passes for internal/store/... and internal/controller.
- Focused race checks pass for run-control, resume, sealed intake and typed subject isolation and account-private receipt deletion.
- Real HTTP-to-store run-control checks pass, along with their existing HTTP
  authority/refusal and resume checks.
- Store helper packages pass. The store's acceptance cells are reported from
  the final suite: EnrolledClaims, GeneratedDependencies, CacheScope, ReferenceEdges, RecoveryCapacityAdoption, RecoveryCapacityMaintenance, Transitions, Sessions, ProvisioningOrigins, ScheduleBoundary. No native-platform cell or registry was changed.
- Commentcheck against main and diff whitespace checks pass. API snapshots and
  source/embedded migration and changelog copies match. Go formatting is clean.
- A broader controller filter also selected the pre-existing
  TestTeamPurgeWaitsForDeletionDelay. It still fails in its old tokens-table
  fixture before reaching deletion. Its source is unchanged. The scoped
  run-control/controller rerun passes.
- PostgreSQL, native listeners and Sparkwing pipelines remain lead checks under
  the swarm brief. No service or candidate binary was started.

## Remaining handoffs and real integration

Fifteen of the remaining store failures belong to W1b's files or its dead baseline tables.
The sixteenth is TestDefaultTeamTwinsAreCalledOnlyWhereTheDefaultTeamIsMeant,
which failed in Go package loading with `could not import bytes (invalid package
name)`. Its isolated rerun passed in 7.841 seconds. The full-run failure remains
in the count and inventory. No guard or source was changed to suppress it.
The failure inventory preserves each test and first error. Twelve billing-rate,
refund and reservation tests still use tokens or credit_charges.token_prefix.
The collection-binding property seeds an invalid provider correlation. The log
standing fuzz seeds disagree with the retained-byte inventory. The writer guard
now reports only storage_reservations and storage_commit_receipts. W1's
previous debug_pauses writer finding is closed. No invariant or guard was relaxed.

Trusted schedule/App intake remains blocked on the existing cross-area contract
request: a tagged committing origin in C4 and transaction-readable verified App
delivery facts from W2. IntakeRequest.Authority and the sealed AuthorityRef alone
cannot represent a scheduled origin without inventing credential authority.
No such credential or foreign contract extension was added. The legacy
cronLauncher/startGitHubAppRun -> triggerIntake/triggerRows -> raw store insertion
chain stays open until that contract and caller migration are complete.

Ordered placement preferences, rotation grace and the SyncLauncher store fence
remain implemented from earlier sessions. Their regression properties are included
in the final store suite. Remaining W1 work also includes other missing controller
adapters and removal of raw run/trigger/token surfaces. W1b's fixture ports and
legacy backing-helper removal must coordinate with those cuts.

## Shared files and friction

Shared schema files internal/store/schema.sql and internal/store/schema.go add only the
private run-control receipt table. Shared production file
internal/controller/run_control_routes.go adds one compile-time
RunControlWriter conformance declaration. Shared tests:
internal/controller/run_control_routes_test.go (explicitly missing adapter case) and
internal/controller/run_control_store_test.go (real transaction proof). No production
caller was otherwise migrated. Shared documents are C1, t1-port-inventory,
CHANGELOG.md, the migration guide, their embedded copies and .apidiff/pkg_store.txt.
Lead-owned handoffs and amendments files are unchanged.

Friction came from obsolete fixture shapes and the remaining W1b ownership
boundary. The current store refuses those shortcuts, so W1b must port or retire
its tests. The larger controller fixture backlog also remains visible rather
than being reported as green. No git write ran. HEAD remains 6241e173c. Scratch
is removed after checks finish and /tmp/sw-gocache is retained.

STATUS: CONTINUE
