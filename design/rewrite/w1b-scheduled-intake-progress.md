# W1b scheduled intake and concurrency continuation

Tenant implements the six ScheduleWriter methods, ScheduledRunWriter and all
eight ConcurrencyStore methods. Controller compile-time assertions cover all
three interfaces.

Schedule replacement commits versioned arming consent, declarations, withdrawals
and the existing Cloud caps atomically. Local registration commits the prepared
checkout, configured entrypoint/source ref and captured environment snapshot in
that transaction. Local pause, override and deletion use authorized transactions.
Explicit empty zones, empty args and zero catch-up survive persistence.

Firing checks current consent and cadence, resolves source outside write locks,
checks the persisted snapshot again, and uses sealed intake. Run allocation,
queueing, history and cursor commit together. Replay is scoped by tenant,
schedule and due instant or manual key. Revocation and membership loss refuse
replay. Scheduled origin grants no App provenance or pool access.

Concurrency transactions use accepted declarations, full authority ancestry,
exact holder generations and declared read edges. Release and promotion commit
together. Promotions cannot outlive the recipient's work lease. Claim group
reads preserve capacity accounting while removing unauthorized leader references
and undeclared memo keys. Existing accounting and lock helpers serve both native
and HTTP callers.

## Verification

Passed with GOWORK=off, GOFLAGS=-buildvcs=false, GOCACHE=/tmp/sw-gocache:

- `go build ./...`.
- `go vet ./internal/store ./internal/controller ./internal/crons`.
- Focused schedule, concurrency, capacity and tenant SQL guard tests across those
  three packages. Store took 13.559s, controller 0.487s and crons 0.005s.
- The complete internal/crons suite passed earlier in the session.
- `TestStoreScheduleBoundary`, the existing schedules/store-sqlite proof, passes.
- `TestDeclaredRepoGuard_OnlyReviewedFilesReadTheField` passes after removing the
  stale handlers.go entry.
- New generated proofs cover revoked or changed consent during source resolution,
  concurrent firing, membership changes, forged local principals, holder fences,
  declaration forgery, leader edges and authority changes before commit.
- `TestControllerScheduleFiresThroughConcreteStore` uses real controller routing
  and store transactions for a pinned Cloud source. It verifies sealed intake,
  queueing and replay. It does not execute a Cloud worker or fetch that repository.
- The local firing proof passes over the in-memory transport. The real api.sock
  variant uses wingd.ListenSocket and kernel peer identity. This sandbox cannot
  run it because socket creation returns operation not permitted. The lead must
  run `TestNativeLocalControllerScheduleFiresOneSealedRunAtDueInstant`.
- `go run ./internal/commentcheck -base main .` and `git diff --check` pass.

No acceptance cells were unskipped and no retained retry tests were removed.
The lead's latest native report still contains FAIL entries. It predates these
edits and remains the completion gate.

## Contract changes made

C1 adds ScheduleSourcePolicy, private versioned consent, the local batch writer,
and CronOverride.TZSet. The C2 writer signatures are unchanged. C1 and C2 notes
now point to the concrete transactions and name the remaining gaps. Lead-owned
handoffs and amendments are unchanged.

## Remaining work and contract changes needed

- Automatic GitHub App schedule discovery still uses legacy ArmPushed and has no
  persisted arming consent. It cannot fire through the concrete writer. Migrate
  that origin and delete the raw arming surface together.
- Private remote branch following needs audited controller credentials during
  revision lookup. The current resolver uses public transport without ambient
  credentials. Pinned private source acquisition remains on the existing audited
  post-admission release path.
- C1/C2 need persisted host identity for HTTP box groups and accepted namespace
  declarations for memo fields. Missing authority returns binding refusal.
  Native box admission remains available. Legacy memo and inheritance branches
  remain pending their caller cut.
- Native api.sock evidence and the existing failures in the lead report remain
  with the lead harness. PostgreSQL concurrency and schedule races need its native
  checks too.

## Shared files and volume

Shared edits outside the new store transactions are internal/crons arm.go,
crons.go, local.go, local_test.go, override.go and view.go, plus controller
intake_policy.go, schedule_routes.go, schedule_routes_test.go, schedules_local.go,
schedules_local_test.go and the three new schedule source/proof files. The
concurrency cut also changes authority_binding.go, capacity_owner.go,
concurrency.go and the recovery test fixture. The intake join changes
intake_runtime.go and run_spec.go. schema.sql adds one consent column. crons.go
extracts transaction helpers instead of duplicating its persistence SQL.

This session replaces 175 production Go lines and adds 1,355 across the touched
areas, net +1,180. Store alone is +1,073 this session. Combined W1/W1b store is
-1,516 versus main under the lead's production-only exclusions, 2,734 lines over
the -4,250 budget. No whole legacy file was retired.

The added volume implements the launch-scope transaction adapters that were
previously fake. It reuses intake, billing, accounting and timer ownership rather
than adding another execution engine, database or ticker. The overage remains
open because raw App arming and memo callers still prevent the corresponding
legacy deletion. It needs the lead's review and continued cuts.

Friction: native sockets are forbidden in the sandbox. Git fixtures must disable
inherited commit signing. A stale test assumed Tenant could not implement the
writer and now supplies an explicitly missing resolver instead.

STATUS: CONTINUE


## Merge reconciliation continuation, 2026-10-17

Adopted W1's private schedule proof and shared sealed intake. Removed the
second scheduled admission implementation, its private IntakeRequest selector,
separate idempotency scope and ScheduledSourceVerified path. The administration
transactions remain because W1's adapter does not implement those six mutations.
Automatic App schedules use their persisted declaration and current automation
consent. API/local registrations retain captured source inputs and current armer
authority checks. ArmedBy stays attribution through the schedule service proof.
No credential or session is fabricated from it.

The launcher reloads the tenant's stored schedule before admission and rejects
missing or wrong-team transaction adapters. Local and Cloud schedule proofs pass
through the concrete store. The local proof arms through the real local-peer
router, advances the store clock to just before and exactly at due, ticks twice,
and verifies one sealed run and one fire. The native variant uses api.sock.

Replay uses the shared intake scope, keyed by schedule identity while retaining
ArmedBy for rate attribution. Moving a followed branch retains the first seal.
Changing pinned execution inputs conflicts. Replacing the armer cannot create a
second run for the same due instant. Added regressions for followed automatic
and manual replay and for armer replacement. The follow regression failed with
idempotency conflict before the correction and passes afterward. Declaration,
saved source consent and caller authority are checked under the final row lock.

The concrete ConcurrencyStore remains the existing declared-group and read-edge
transaction owner. Its HTTP, current-authority, holder-generation, cancellation,
force-release, promotion and undeclared-edge properties pass. Removed the unused
production ConcurrencyHolder reader. Legacy unit fixtures retain that reader in
_test.go. Its sole controller-test caller now checks the exact holder, run and
live expiry through the current group reader.

### Security-test reconciliation

Preserved both sides' generated schedule security tests. The retired raw helper
test file stays deleted as on integration. Existing stronger replacements keep
these incoming cases: raw node insertion cap -> plan insertion cap; runner
creation -> tenant member admission and team runner claim; 404 route matrix ->
foreign/missing response equality; lease-token environment stripping -> rejection
of the entire untrusted environment surface. Reintroduced the incoming named
child-ancestry case as a generated test on the current sealed child route. Own
pipeline cycles refuse; foreign and missing ancestors return identical errors,
reveal no private pipeline and commit no child.

Reconciled six additional controller fixture groups without production changes:
current token enrollment/revocation, console-only account deletion, the 60-second
purge delay, current error envelopes, verified paid settlement, explicit source
consent, signup active-team selection and stable-key checkout recovery. The
retired self-deletion route refuses both fresh and old sessions without mutation.
Retired refund routes stay absent; owners and runners cannot invoke console
refunds and cannot change the paid ledger. Ambiguous checkout failures retain
one reservation and retry it under the same key. The shared in-process identity
fixture supplies http.NoBody, matching actual server requests.

Generated docs/api-reference.md, api/openapi.yaml and the embedded mirror with
bin/gen-api-docs.sh. Both operator object-store quota routes now carry the final
policy metadata. internal/apiref and internal/apispec tests pass.

### Verification and remaining handoff

Whole-repository build and scoped vet pass. Store schedule/concurrency/guard
selection passes (7.380s), as do the focused race checks (9.724s). Full
internal/crons, internal/apiref, internal/apispec, internal/store/teststore and
internal/wingd/localcontroller suites pass. Schedule transport and concurrency
HTTP acceptance selections pass (0.944s), and TestStoreScheduleBoundary passes
(0.227s). Local/controller schedule, launcher and child-ancestry checks pass
(1.206s). GitHub App cron groups pass (2.765s). The additional fixture selection
passes (3.465s). Commentcheck and whitespace checks pass. No native acceptance
cell is newly marked passed.

HTTP fixture checks use a temporary standard-library httptest net.Pipe overlay.
The actual controller/authenticator/store run, but this is component evidence.
The unmodified native local schedule test fails at setsockopt with operation not
permitted. The lead must run TestNativeLocalControllerScheduleFiresOneSealedRunAtDueInstant.
The supplied native report still contains FAIL and remains the completion gate.

Run-existence privacy tests remain unchanged and fail on this integration state.
Decision 1 at 04:00 assigns that mechanism to W2, including
TestTeamBoundaryOutputHidesRunExistenceProperty and
TestTeamBoundary_EveryRunRouteHidesOtherTeamsResources. Native SSH host-key,
listener write-timeout and metrics-listener tests also require the lead's sockets.
Private remote branch lookup, HTTP box identity and memo namespace integration
remain open as named in C1. No new foreign contract amendment is requested.

All conflict contents are resolved. The Git index still records the merge paths
because the swarm brief prohibits Git writes. The lead must stage resolutions
and commit. Deleted integration files remain deleted. Lead-owned amendments and
handoffs are untouched. C1/C2 record the unified schedule adapter and its amended
signature. Shared caller edits are internal/controller/crons.go, crons_tick_test.go,
schedule_routes_test.go, intake_policy.go, schedule_intake_test.go,
team_boundary_test.go, child_intake_reconciliation_test.go and
identity_fixture_test.go, plus deletion_review_test.go, deletion_test.go,
git_credentials_team_test.go, refund_authority_test.go, signup_gate_test.go and
team_billing_test.go. Generated
references and the reconciliation ledger are also shared edits.

Own store edits after the merge baseline add 166 production lines and delete
231, net -65. This removes the duplicate admission machinery and raw reader.
Combined store is -1,397 production lines versus main under the lead's filter,
still 2,853 over its -4,250 budget. The retained source/administration and
concurrency adapters were missing concrete launch transactions. This session
shrinks them; it does not claim the inherited overage is resolved. No new
option, setting, service, cache or environment variable was introduced.

Friction: native sockets are forbidden and Git staging belongs to the lead.
The shared Git config has no user identity or core.worktree override. Scratch
is removed at session end; /tmp/sw-gocache is retained.


Final controller component selection: 669 top-level groups pass, zero top-level
failures or skips, 87.829s. Selection excludes the other worker's original
GitHubApp/GitHubRunner/GitHubOnboarding/Source/RunSource prefixes, native tests,
the SSH host-key, dashboard listener and metrics listener groups, and the two
unchanged W2 privacy groups named above. GitHub App cron checks run separately
and pass. Existing PostgreSQL subtests still require the lead's configured
server. No native success is inferred from this filtered component run.

Final command uses -json -p 4 -timeout 180s and the temporary net.Pipe overlay.
Its -skip expression is:
`^(Test(Native|GitHubApp|GitHubRunner|GitHubOnboarding|Source|RunSource|ReadHostKey|DashboardStreams|MetricsAddr)|TestTeamBoundaryOutputHidesRunExistenceProperty$|TestTeamBoundary_EveryRunRouteHidesOtherTeamsResources$)`.
Final controller vet also passes. The report and ledger retain results after
scratch removal. The lead's native report remains unchanged and still fails.

STATUS: CONTINUE
