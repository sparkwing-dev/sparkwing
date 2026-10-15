# W1 session 57: committing team mutations

W1 remains CONTINUE. HEAD is 79f4eaaca. No Git writes ran. Lead-owned handoffs
and amendments, W1b production, materialization, billing settlement and blob/log
persistence are unchanged.

## Implemented and deleted

Implemented all six methods of C2 TeamWriter on Tenant. Rename, invitation
creation/withdrawal and build trust serialize on the team and recheck current
administration, full ancestry and membership. Actors and timestamps derive from
committed authority and the store clock. Invitation limits and email rules use
the existing insertion owner. Native HTTP now resolves the concrete adapter.

Invitation replay derives an opaque ID from the tenant, committed account and
request key. The row preserves its original email, role, inviter and expiry.
Changed input conflicts. Replay after withdrawal returns the original result.
Concurrent identical requests write one row. Failed insertion returns no result
and rolls back. This adds no table or configuration knob.

Team deletion keeps owner, last-team, checkout, debt and hold guards. Account
sessions move to a remaining team. Run deletion rechecks runs.control, requires
terminal state and drained node/owner/eval claims, removes operational rows and
the success pin, and preserves financial records. Repeated direct store calls
still require current authority. A native terminal-run/held-owner probe failed
before the claim guard and passes afterward.

Removed Tenant.Rename, RequestDeletion, CreateInvitation, DeleteInvitation,
SetTrustLocalBuilds and DeleteRun. Removed their caller-selected actor/clock
entry points and the deleted concurrency_cache cleanup query. Removed the
obsolete concurrency-memo deletion test and raw run-control fixture. Existing
fixtures now call current transactions with real account sessions. Fixture-only
run removal remains for financial/event tests that do not exercise authorization.

## Volume and assumptions

Store production against HEAD: **210 added, 138 deleted, net +72 Go lines**.
All touched production Go, including controller conformance: **net +74**.
Combined W1/W1b against main: **19,324 added, 23,410 deleted, net -4,086**.
The -4,250 target is **164 lines away**. Tests, testdata, acceptance harness and
contract declaration files are excluded, matching the lead's budget script.

The +72 pays for C2's previously unimplemented committing authority, replay and
lifecycle checks. The implementation shares the administration transaction and
reuses invitation insertion. A second replay table or a generic mutation command
would add surface. No budget waiver or COMPLETE claim is made. Raw token minting
and trusted run/trigger admission still offer larger legacy cuts.

The only added mechanism is invitation identity derived from its existing
idempotency contract. Random IDs alone cannot recognize a retry without another
persistent receipt. Replacing this codec with explicit receipt columns would be
the local reversal. Terminal/drained deletion implements C2's lifecycle guard.
Invitation administration follows C2's existing team.admin route. Legacy editor
invitation fixtures now start from an owner before testing a downgrade.

## Evidence and ports

All Go commands use GOWORK=off, GOFLAGS=-buildvcs=false,
GOCACHE=/tmp/sw-gocache and TMPDIR/GOTMPDIR under /tmp/sw-astra-W1. Tests use
-p 4. The final build and scoped vet pass.

- Whole repository: go build -p 4 ./... . Vet: ./pkg/store ./pkg/controller.
- The 21 established W1 TestStore acceptance functions and insertion-limit
  proof pass in the focused acceptance run (10.795s). No native placement cell
  is newly enabled. Cloud metering, ordered preference and rotation-deadline
  properties also pass as an explicit three-test run (3.181s).
- TestTeamMutationsRequireCurrentAuthorityProperty generates direct/delegated
  authority, foreign tenants, membership/role loss, missing ancestry, expiry and
  wrong selected grants. Refusal preserves tenant rows. Valid mutation,
  original actor/time, replay, withdrawal, insert rollback and concurrent replay
  tests pass.
- TestTeamDeletionRetainsCheckoutAndHoldGuardsOnStoreClock exercises final
  checkout amounts and configured payment mode. TestAuthorizedRunDeletionPreservesBillingAndAllowsCleanupReplay
  completes a sealed run, refuses removal before owner settlement, retains its
  financial grant and checks repeated removal and revoked cleanup authority.
- TestTeamWritesUseNativeTenantTransactions drives all six actual HTTP routes
  through SQLite credential snapshots and committing writes. Only CSRF
  transport is a fixture. It and TestTeamWriteHTTPAuthorityProperty pass.
- Identity invitation, membership, build-trust, nanosecond persistence and
  session ports pass. Focused race tests passed for identity/build trust
  (44.710s), and for the new store transactions (25.072s) and controller routes
  (3.578s). The final property edit only replaces test scaffolding and adds
  the proper runs.control alternative. Its focused normal check passes.
- Commentcheck passes across 2,377 files. Whitespace, changelog and embedded-doc
  checks pass. The changelog check prints its existing rename-detection warning.
- The store API snapshot matches generated output. Imported controller,
  storage and S3 snapshots still drift, as in the prior report. Their owners
  must regenerate them. No whole-repository API gate is claimed.

Two broader probes remain red. The writer guard reports the same three missing
writers: debug_pauses (W1), storage_reservations and storage_commit_receipts
(W1b). A legacy deletion probe fails before mutation in two checkout fixtures
using non-cent amounts and one run fixture inserting removed trigger columns.
Those are not hidden or counted as passing ports. No whole-package legacy suite,
PostgreSQL/native matrix, pipelines, release or independent review is claimed.

## Remaining work and contract requests

Priority Cloud metering, ordered placement, rotation grace, launcher fencing,
harness moves and dead contract-table removal were finished previously and
remain covered. Materialization remains closed.

Trusted schedule/App admission still needs C4's tagged committing origin through
IntakeFacts/SealedRequest and C2's verified-delivery reader usable in the store
transaction. IntakeRequest still demands credential authority before its policy
runs. An arming account or verified delivery is not that credential. No synthetic
credential or foreign contract change was introduced. Once declared, route both
origins through admitRun and delete triggerIntake, triggerRows and raw insertion.

C2's run-target resolver and BindNone resource relation still require a live run
row before DeleteAuthorizedRun. Direct-store cleanup replay works, but an HTTP
retry after deletion cannot reach it. W2 must define the authorized cleanup
selector/route behavior. Recommendation: authorize tenant runs.control before
resource lookup and let the committing deletion own exact row scope, including
already-removed rows. No foreign route policy was changed here.

GitHubAppWriter, RunControlWriter creation/approval masking, trusted schedule
run-now, full runner inventory/capacity and renewal reporting, sealed GitHub
completion facts instead of TriggerEnv, publisher metric binding and usage
report identity remain from the previous report. Raw CreateRun/CreateTrigger,
raw token minting, run-rate token queries and their legacy fixtures still need
retirement. Current local peer composition remains W9's integration. No new
human decision is requested. Billing/blob/log amendments remain W1b's work.

## Shared files and friction

Shared Go files:
- pkg/controller/invitation_lifecycle_team_test.go
- pkg/controller/team_boundary_test.go
- pkg/controller/team_write_routes.go
- pkg/controller/team_native_routes_test.go

The only controller production change is compile-time TeamWriter conformance.
The two external fixture callers now use native invitation authorization.
Within W1b-owned tests, credits_test.go only switches run removal to its fixture
helper. All other Go edits are in W1's store area. C1, the port inventory,
migration source, changelog and embedded copies are updated. The store snapshot
is regenerated.

Friction: creating the first protected CSRF key retires pre-key sessions, so the
membership port obtains its actor from a fresh account session. The final schema
has already deleted concurrency_cache, but the old run remover still queried it.
Legacy deletion seeding and checkout amounts prevent its broader fixtures from
reaching the new transaction. The needed foreign intake declarations remain absent.
Scratch is removed at the end. The shared build cache remains. No local Git
user/worktree overrides were present in the final config check.

STATUS: CONTINUE
