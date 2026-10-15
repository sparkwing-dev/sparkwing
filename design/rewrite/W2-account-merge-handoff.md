# W2 account authorization and integration merge

<!-- design:begin -->

Starts from 994107032 with bw-3095-rewrite already merging. No Git writes.

## Implemented and deleted

All 25 conflicted paths have resolved contents. The two modify/delete test
conflicts keep W2's deletion. handoffs.md matches the integration stage byte
for byte. Amendment files are unchanged. The lead must stage the resolutions
and finish the merge because the swarm brief prohibits Git writes. The index
still reports all 25 paths as unmerged. No conflict marker remains.

W2's final routes and handlers survive. W1's removed heartbeat, raw status,
offer-round, storage-counter and concurrency APIs stay removed. The old data
download handler, service payment routes, old reporting paths and duplicate
engine registrations were not restored. The artifact-manifest client/storage
method from integration survives. API references and snapshots were regenerated.

GET /me, /me/identities and /me/team-deletions now use the shared account-session
policy. Their subject comes from Authorized. The read-only AccountReader uses
existing Store methods by default and permits fake readers without SQL copies.
GET /me no longer renews or switches the session. Memberships must belong to
the selected account. These routes retain their existing public projections.

GET /credits/settings now uses the member runs.read policy. Authentication and
authorization precede the billing-feature check. The legacy route registrations,
tenant entries and handler identity checks are removed for all four reads.
Their replacement removes 40 old handler lines and eight registration/catalog
lines. This count excludes the integration branch's independent removals.

DELETE /me is removed. The frozen design makes account deletion console-only
under /operator/accounts/{account}. Its handler, email-confirmation body and
recent-sign-in constant delete 39 production lines. The obsolete 47-line
TestDeleteAccountNeedsTheEmailAndNoOrphanedTeam is removed. The existing console
path retains the last-owner refusal. Successful account deletion still requires
C1's final-schema transaction, described below. No compatibility route is added.

The 60-line account adapter and changed handler bodies replace the removed read
authorization. This is a partial legacy cut. Bearer/claim middleware and other
legacy controller registrations remain.

## Evidence and ports

Invariant: account discovery reads only the authenticated account's records and
changes no session or account state. Generated forged selectors exercise all
three reads. Real SQLite sessions prove identity, unchanged expiry/team, empty
identity/deletion collections and HEAD refusal. Live unrelated authority returns
403. Dead authority returns 401 before account reads. Backend failures use the
canonical envelope without private diagnostics.

TestAccountReadHTTPRefusesUnrelatedAuthority ports the GET /me bearer refusal
from TestIdentityRoutesRefuseABearerToken. Its POST /teams assertion remains.
TestSelfServiceAccountDeletionIsGoneProperty varies email forms and presentations
and checks account/session state after the retired method returns canonical 405.
TestConsoleAccountDeletionRetainsLastOwnerConflict ports the HTTP 409 and affected
team details through the real default console adapter and SQLite. Existing
operator-policy tests retain refusal of bearer, member and peer deletion.
The removed self-service email and recent-sign-in requirements have no final
equivalent. W1's existing deletion tests retain successful-deletion invariants.

The two profile tests introduced by the conflict remain retired in favor of the
existing sizing ports. W1's property ports the removed raw unstarted-requeue
test. No full native credential matrix cell is newly unskipped.

## Verification

Commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4, GOCACHE under
/tmp/sw-astra-W2/cache and GOTMPDIR/TMPDIR under /tmp/sw-astra-W2/tmp. Go commands
use the assigned worktree through go -C. Tests use -p 4.

- Whole-repository go build passes after the merge and final deletion cut.
- Scoped vet passes for controller, controller/client, capability, apierr,
  executable acceptance and orchestrator.
- W2 executable acceptance selection passes, including account, console,
  credential, session, data, source, payment and retired-deletion proofs.
- Controller policy, handler-policy and tenant/route guards pass. The daemon's
  independent actual-registration classification passes after stale entries
  are removed and current GitHub-runner/metering entries are added.
- Full capability, apierr, apiroutes, apiref and apispec tests pass.
- API snapshot, API generators, docs/mirror/changelog consistency, embedded-doc
  scrub and git diff --check pass. commentcheck against main reports clean
  across 1476 files.
- Native Linux TypeScript setup and tests pass in 55.858 seconds:
  go test -p 4 ./sdk/typescript/test/nativehost -run '^TestNative' -count=1.
  No dependency-setup failure occurred.

The full run used go test -p 4 ./tests/acceptance ./internal/capability
./internal/apierr ./internal/apiroutes ./internal/apispec ./internal/apiref
-count=1 -timeout=180s. The full executable acceptance run is not green. Its lifecycle executor/step
components fail with "holder transition requires a nonnegative source attempt".
The same missing W9 field fails TestEngineJobLifecycleRenewsAndReplaysUnderStoreFences
and TestEngineOwnerReconcilesExecutesAndFinishes in controller. These are the
existing W1 handoff, not grounds to infer an ordinal in W2. The full suite then
reaches its 180-second package timeout while the native TypeScript additive-fields
case has run for two seconds. That timeout is not a TypeScript setup failure.
No unchanged failing full run was repeated.

The API generator first refused the deleted deleteAccountReq still referenced
by the old OpenAPI DELETE /me block. Removing that obsolete source block and
updating the console description closes the drift. Four accounting-test calls
and one credit fixture needed minimal migration after the integration merge.

## Amendments and handoffs

| Assignments | Current W2 disposition |
| --- | --- |
| A1-01/03/05 | Existing node-service policy and strict error decoder retained. W3 owns native service composition. |
| A1-08/09/19/26/38/55 | Existing mixed/named claim, here, exchange, renewal and private-award adapters retained. Production enrollment/profile wiring and immutable launcher-instance correlation remain external. |
| A1-11/14/15/16/17 | Sealed source, failure, cancellation and snapshot/execution projections retained. W9's source-attempt transport is still missing. |
| A1-20/49 | Provisioning remains through the shared issuer. Legacy supplied-token bootstrap delivery and origin/console persistence remain integration work. |
| A1-21/22/23 | Account/credit-settings reads advance the actual policy cutover. Default session/console/CSRF and verified-peer selection remain implemented. C1 peer authority and the cancellation authentication projection remain open. |
| A1-24/25 | Existing source audit, plan-secret opener and binding seams retained. No W2 SQL or second issuer is introduced. |
| A1-27 | Existing capability.SettleBilling is the production calculator used by the merged store billing helpers. No alternate calculator was added. |
| A1-28 | Existing verified checkout callback retained. Final card/dispute event contracts and production financial integrations remain open. |
| A1-29/30/31/32/33/34/35 | Existing data/record/cache adapters retained. Real metadata/read-set, charging, object backend and remote client composition remain external integration work. |
| A1-39/40/53 | Runner/local-controller/native assembly and the full native target matrix remain W3/W8/W9/T1/lead work. No fake proof is promoted to native evidence. |
| W1 owner heartbeat | The final owner heartbeat is already bound to EngineController.Renew, which calls UnitRenewer and preserves CancelReason and watched jobs. |
| W9/W2 typed step reports | Existing C1.RecordStep mapping retained. Captured attempt transport and the typed engine client still need W9 before full remote integration. |

## Foreign contracts and remaining work

- W9 must add the captured source ordinal to JobTransitionRequest and typed
  step transport. W2 then forwards it to C1 rather than guessing from a later
  read. W1's NodeTransition.SourceAttempt contract wins.
- W1 persists cancellation reasons and exposes them through RenewUnit. Its
  authentication_binding.go still does not populate BindingState.CancelReason.
  The existing C2 field already declares job intent precedence over run intent.
  W2 keeps consuming that interface without an SQL projection.
- W1's DeleteAccount still reads and updates the removed tokens table. It also
  lacks an authority-bearing console commit recheck. The passing last-owner
  refusal happens before that code. Successful deletion/revocation remains open.
- The prior console/operator mutation, roleless session, verified-peer actor,
  root origin, bootstrap delivery, link nonce, sizing, enrollment, source/App,
  data/backend and launcher correlation requests remain open. No foreign
  signature or schema was changed here.
- Remaining legacy execution, source/OIDC, concurrency and account/team routes
  must be migrated before the old bearer/claim stack can be deleted. W2 is
  incomplete. There is no new product or architecture decision to ratify.

Shared files touched for merge resolution or the minimal caller migration are
pkg/controller/client/client.go, pkg/storage/state.go,
internal/orchestrator/wingd_api.go and wingd_api_routes_test.go,
internal/apiroutes/apiroutes.go, controller accounting/credit fixtures, source
auth/security/migration docs, API/OpenAPI and .apidiff snapshots, CHANGELOG and
generated docs mirrors. Contract inventory edits record the final path and
test ports. handoffs.md is the integration version unchanged. The lead retains
staging, committing, pipelines and independent review. All owned commands have
finished. /tmp/sw-astra-W2 is deleted. The final W2 selection passes in 17.484
seconds, and the API and changelog drift checks pass.

STATUS: CONTINUE

<!-- design:end -->
