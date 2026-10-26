# C1 data contract

## Request tenant and fleet maintenance

The controller uses one tenant resolver. Policy routing binds a request handle
from the authorized team, and narrow handler ports come from that same handle.
Run selector reads share it with the handler. Embedded claimant requests use the
registered policy router and carry its scoped context into the claim transaction.
A `{run}` endpoint without a target resolver fails policy validation.

The serving mux also supplies route discovery. Probing does not rebuild a second
router. Every call that assembles an HTTP handler installs its serving snapshot.

`Operator.EachTeam` selects live teams with the same predicate as handle
acquisition. Cron, completion, attention and runner sampling continue after a
team callback fails. Pending deletion withdraws stored schedules and run effects
in the deletion transaction. Physical deletion removes the complete team object
prefix, including CAS and pending uploads, and its logs.

Launcher reconciliation isolates persisted award corruption across preparation
renewal, rejection, reservation validation and snapshot reads. A failed award
returns an explicit deferred fence, preserving its Job while healthy awards
continue. Database failures and invalid claimant authority still fail the request.

Transaction commit preserves context cancellation when the database has already
rolled back the cancelled transaction. An owner interrupting its state wait can
continue scheduling without treating that rollback as a database failure.


## Collection and required blob reads

Integration amendment under the store review ruling: collection rechecks the
maintenance lease after digest-lock waits and verifies the same collecting
tombstone immediately before physical deletion. Finalization also requires
that tombstone. S3 deletes use the observed ETag as `If-Match`. Missing ETags,
failed conditions and backend errors retain the tombstone for another pass.
No object generation or schema change is introduced. A process paused after
the final check can still race a later publication, including identical S3
bytes that retain the same ETag. This recoverable-data loss is accepted.

Required reads return errors for missing bytes and refuse successful partial
data. Remote memo and depcache lookups propagate physical verification errors
and grant no artifact edges on failure. An absent logical memo record remains
a cache miss. Materialization retains its explicit cold-execution miss policy.

## Submitted environment retention

Integration amendment: IntakeRequest.SubmittedEnv carries private values outside
the request seal. AdmitRun validates the sealed environment names/digest and
allow-list and encrypts values atomically into runs.submitted_env. The existing
row-bound cipher binds ciphertext to team, execution ID and run-environment
purpose. Values never enter RunRow, receipts, events or public snapshots. Missing
or corrupt values fail before release; no ambient fallback occurs.

Retries and child intake retain the source values and seal identity, encrypting
them under the new execution ID. Replay cannot replace values. Retention follows
the run row, including deletion. `Policies.RunEnvironment` receives the controller's existing credential cipher
during `Open` through `WithPolicies`. ReadSubmittedEnvironment releases only
to the run's live direct trigger, plan, eval or node execution award. The persisted
unit prefix, holder, generation and lease fence the release. Eval also retains
its source and controller expansion leases. SourceRead is independent of
environment release, so eval retains its fixed narrow grants.
Plan acceptance includes these registered masks in identifier refusal.

## Verified-default record observations

`RecordLookupResult.VerifiedDefault` records a successful verified-default
selection. C1 sets it only after persisted branch evidence and live record/member
authorization pass. `Record.VerifiedDefault` validates the record's ref against
its verified default-branch facts, including App delivery and SHA evidence.
Remote-cache authorities enforce this policy even without a caller flag.
An omitted ref selects among the live authority's permitted refs, preserving all
other logical selectors. C3 exports only the boolean observation; each later
download still resolves live authority and persisted read edges.

## Source reads before an execution attempt

A live node award may read its run's submitted `source_bundle` before its
execution attempt starts, including a retry that retains an earlier attempt
ordinal. `ResolveBlobAuthority` returns read authority without an attempt or
upload identity for that pending node. Candidate edges permit only the bundle
digest in the bound run's sealed request. Holder, generation, lease, cancellation,
team and repository checks still apply. Output and cache reads, upload receipts
and record writes continue to require an execution attempt.

`ResolveDataAuthority` selects the grant required by the authorized operation,
so a read does not acquire write authority from the capability's `WriteRef`.
The HTTP policy router applies the existing data error projection to data binding
refusals. An unreadable foreign digest returns `forbidden` rather than `internal`.
The composed HTTP coverage is in `internal/controller/burst_acceptance_test.go`.
The source-read fence matrix is in `internal/store/blob_source_setup_test.go`.

Cloud source initialization precedes bootstrap exchange. A live Node, Plan or
Eval bootstrap resolves `source.read` only for its sealed submitted source
bundle. This projection has no attempt or upload identity. Store read-set
construction derives the grant from persisted bootstrap delivery and excludes
output, binary, cache and foreign-run edges. Generic `data.read`, `data.write`
and all data operations except the exact bundle download remain unavailable.
Exchange revokes the bootstrap's source authority with its other authority.

Fresh node awards carry attempt zero in their data scope even when the node
retains earlier attempt ordinals. Authentication reports no current execution
attempt for a pending node that has not started. Starting execution allocates
the next ordinal, which authentication then projects into the live award.
Exact-body transition proofs use the decoded `source_attempt`, revalidated
against the persisted source ordinal in the committing transaction. An identical
actor and digest may replay before comparison with the advanced ordinal, after
authority is rechecked. A changed source ordinal cannot reuse that proof.

## Here intake reasons

`runspec.Request.Reason` retains the explicit here-trigger reason in the existing
request JSON and canonical seal. Intake, replay and retry preserve it. Changing
that reason under the original idempotency key conflicts. It adds no authority
and does not alter one-time here bearer issuance.

## Committed retry selection

`IntakeRequest.RetryMode` is separate from `Request`. An unset mode normalizes
to `failed`. Other accepted modes are `failed` and `all`. A mode without a retry
source is refused. Retry replay hashes the canonical source run ID and normalized
mode under the existing tenant, subject and pipeline scope.

Admission locks the source run and commits `RetrySelection` in `lineage_json.retry`.
`failed` records each successful or memoized source job's `MaterializedResultSource`
fence. `all` records an empty result map. Selection runs only for a new admission,
after checking replay. Original request, spec, plan and their hashes stay unchanged.

`RunRow.Retry` exposes that selection in private execution snapshots. Manual retry
materialization requires `failed` and an exact committed result fence. A source
job that succeeds after admission cannot become a reuse candidate. Missing result
bytes remain a materialization miss under the existing record-copy policy.

The integration amendment implements the lead's 2026-10-17 retry-selection decision.

## Operator team creation (decision 114)

Store.CreateOperatorTeam(ctx, Authorized, slug, displayName) returns one
capability.Issued only after commit. It re-resolves the operator, creates a
dedicated account and owner membership, and calls Store.issue with the trusted
ProvisionTeamAdmin origin. A transaction-local created-team record confines
that origin to the exact new team and account. The complete owner grant table
applies. Ordinary rotation rechecks current membership.

The existing createTeamTx takes an explicit actor for team.created. Account
creation paths pass their account ID. Operator onboarding passes the verified
operator ID. The registry, account, membership, capability and audit are one
transaction. Existing and tombstoned slugs fail without writes. No schema or
financial grant changes. C2 records the route and full issuance boundary.

## Shared lease fences and sealed credential writes

Ordinary claim, renewal, release, reaper, secret, source and metric transactions
share the controller-owner fence with PostgreSQL `FOR KEY SHARE`. Acquiring,
renewing and releasing singleton leases take `FOR UPDATE`. Lease checks read
database time after lock waits. A database clock read failure refuses commit.
Reaping commits each target independently, continues after a target fails, and
returns committed recoveries alongside the joined errors. Run admission refuses
commit unless exactly one tenant run-number counter advances.

`Tenant.WriteSecret` takes `SealedSecret`. `Tenant.WriteGitCredential` takes
`SealedGitCredential`. Construct them with `store.SealSecret` or
`store.SealGitCredential`, the bound cipher, tenant and row. Their row accessors
return copies. Zero values and values sealed for another tenant are refused.
The credential writer still rechecks current administration inside its transaction.

The raw Store and Tenant `CreateToken` and `CreateTokenWith` methods are removed,
as are `FinalizeNodeCredits`, `TokenMetered`, their mutable node/trigger credit
columns, and `storage_reservations` and `storage_commit_receipts`. Capability
provisioning and immutable billing awards own issuance, charging and refunds.
Compute usage counts live metered node awards. Unused raw token identity, scope
and lease-limit declarations are removed. Controller fixtures issue current
capabilities, and the browser fixture uses the bootstrap-capability gate.


## Tenant event append and deployment compute controls

`Store.AppendEvent` is removed. Select a tenant and use `Tenant.AppendEvent`.
It allocates the sequence and event bytes with the event insert in one
transaction. Missing and foreign runs return `ErrNotFound`. Event timestamps
come from the store clock. Profile drift uses its selected tenant, and the
local session reaper records outcomes through `DefaultTeam`.

`ComputeLimits`, `SetComputeLimits` and `ComputeUsage` move from `Store` to
`Operator`. Select the deployment handle with `AsOperator()`. The unused
`ComputeAlarmState` and single-setting `SetComputeLimit` methods are removed.
Set one limit through the batch writer. Validation, zero-valued removal and
transaction rollback remain unchanged. HTTP mutations still require the
separate authority-bearing settings adapter.

`Operator.CountPendingNodes` is removed. Read the ready count from
`CountNodesByQueueState` with `QueueStateReady`. Both operational gauges use
that one snapshot.

## Account mutations and raw team-switch retirement

`Store` implements C2's `AccountWriter`: `SelectAccountTeam`,
`CreateAccountTeam`, `AcceptAccountInvitation` and `UnlinkAccountIdentity`.
The account routes use the concrete adapter by default. Each transaction
reloads the direct session by its committed hash, current ancestry, membership
and account. Changed identity, selected team, issuance time, expired authority
or capability ancestry refuses the write. Account-only sessions can select a
team only after proving current membership.

Selection commits the session team and account's active team together. Team
creation and invitation acceptance share the existing transaction helpers and
commit their session selection in the same transaction. Validation, limits,
verified-email checks, one-use invitation state and business audit facts remain
in those helpers. A failed session update rolls back membership, admission and
team creation. Existing teams lock in lexical order before account and authority
rows. Invitation lookup discovers its team before that lock sequence and returns
no result until account authority has been rechecked.

Account, membership and identity timestamps use nanoseconds. Account and
identity readers, the waitlist projection and signup window counts use the same
unit. Identity unlinking requires a direct session issued within ten minutes on
the store clock. It retains the selected hashed session, ends the account's other
sessions and preserves the last sign-in method and App installation bindings.
`Store.CreateTeam`, `AcceptInvitation`, `UnlinkIdentity`, `SetActiveTeam` and
`SwitchSessionTeam` are removed. Account fixtures authenticate through
`storetest.AccountAuthority` or `SessionAuthority` before calling the final
writers. Roleless fixtures use their linked provider identity. Invitation expiry
tests advance the store clock and keep the accepting session live.

Team creation and invitation helpers leave account selection to
`selectAccountTeamTx`, which commits the account and session once. Identity
unlinking selects only the provider subject needed for the deletion and unlink
record. It returns the ended-session count.

The unused `AccountIdentity`, `GitHubRepo.CanonicalURL`, `MetricSample.OneShot`,
`IsBusyErr` and `Tenant.CacheExcludedCounts` APIs are removed. The latter's
unused SQL and its fixture-only test are deleted. Profile folding retains its
existing memoized-job exclusion tests.

## Raw run writer and unused liveness gauge removed

`Tenant.CreateRun` is removed. Executable intake uses `AdmitRun` and its sealed
request transaction. Reporting fixtures use `teststore.SeedRun`, which writes the
current sealed schema and creates no owner unit, job or capability. Existing
controller and web reporting fixtures now use that helper.

`Operator.CountActiveRunners` is removed. Its lease-window gauge had no production
caller. Tenant runner inventory uses `ReadRunners` and committed observations.
The unused `runTeamSQL` default-team fallback, `CompileLogNode`,
`ErrPlanAccepted`, `Token.IsValid`, `Token.HasScope`, `TokenEnded`,
`ErrTokenRevoked` and `ErrTokenExpired` are removed too. Capability authentication
and rotation continue through C2 and the store's live authority transactions.
Profile folding recognizes `memoized` jobs and excludes their execution samples.

## Explicit tenant reporting readers

`Store.GetRun`, `GetNode`, `ListNodes`, `ListEventsAfter` and
`GetActiveDebugPause` are removed. Select the tenant with `ForTeam` and use its
reporting readers. Cross-team `ListRuns` and `GetLatestRun` move from `Store` to
`Operator`. The dashboard backend takes one tenant at construction and uses it
for run, job and event reads. It has no default-team fallback or separate run
scope.

`Tenant.GetSecretForPipeline` is removed. Secret releases use `ReadSecret`
with current authority, declared names, pipeline/shared precedence and pool
restrictions. Trusted exact-row inspection still uses `GetSecretRow`.

## Bucket measurement and executable activity

Bucket measurement runs on the controller's ordinary timer. The cross-controller
`bucket.measure` lease and success stamp are removed (decision 50).

`Tenant.ExecutionActivity` reads executable run, pending owner and live holder
counts in one tenant-scoped snapshot. Lease expiration uses the store clock.
The local controller consumes that projection and no longer reads concurrency
tables directly. Run trends recognize the final `memoized` verdict. Pipeline
inventory joins owner rows to runs for their sealed pipeline metadata.

## Sealed reporting and raw job-finish retirement

Reporting run and trigger readers use C4's display projections, including
fail-closed argument classification before plan acceptance. Trigger metadata
comes from the sealed run and queue state from `triggers`. Repository filtering
runs in SQL over `repo_display`. Pending run cursors coalesce absent starts to
zero. `Run.Number` reports the allocated tenant number.

Job reporting reads the final `generation` and `attempt` columns. Typed state
readers retain output/manifest record selectors. The raw `FinishNode`,
`FinishNodeWithReason`, `FinishNodeWithOutputRef` APIs and their duplicate terminal
writer are removed. Tests use real claims and `TransitionNode` for metadata,
replay, refusal and tenant boundaries. Unrelated presentation fixtures use
`teststore.SeedNodeFinish` without granting production authority.

Unused Node offer-priority, artifact-digest and raw bearer fields are removed.
Unused `ClaimToken`, `ClaimTokenKind` and its kind constants are removed.


## Owner completion and retired store helpers

`Tenant.FinishRun` and its unfenced terminal writer are removed. Use
`Tenant.FinishRunAndUnit` with the current owner authority. Terminal time comes
from the store clock. Exact replay preserves it. The first successful run
records its durable team marker and audit fact in the same transaction as the
verdict, owner completion and pipeline pin. Audit expiry preserves the marker.
An event failure rolls the transaction back.

The unused `ConcurrencyHeartbeatInterval`, `ConcurrencyHeartbeatTimeout` and
both `DefaultConcurrencyHeartbeat*` constants are removed. The shared lease
keeper owns refresh cadence and attempt deadlines.

The unused `JoinProfileKey`, `SplitProfileKey`, `DisplayProfileKey`,
`RepoIdentityFromURL` and `RepoIdentityFromPath` helpers are removed. Profile
views display the stored pipeline key. They no longer decode historical keys.


## Reporting transactions and schedule lease

`Tenant.WriteMetric(ctx, Authorized, MetricSample)` and
`WriteUsage(ctx, Authorized, NodeUsage)` implement C2's `metricWriter`. Both
recheck the tenant, full live ancestry, selected route rule and current claim
fence in the committing transaction. Job reports carry the captured attempt.
A retry advances that attempt, so a delayed report cannot move to it. A holder
can report during cancellation while its lease remains live. The run host can
report a finished job while its own owner lease remains live.

`Store.AddNodeMetricSample`, `Tenant.AddNodeMetricSample`, `Store.AddNodeUsage`
and `Tenant.AddNodeUsage` are removed. Identical metric retries succeed at the
sample limit. Conflicting readings and overflowing usage totals leave stored
values unchanged. The controller selects the concrete tenant writer by default.
Its optional test resolver can still supply a fake.

`Operator.RunCronTickLeased` replaces the Store method and uses the declared
`schedules.tick` singleton lease. The existing tick window uses the store clock.
The last successful tick is a nanosecond timestamp in `settings`. Success is
stamped only while the captured lease is live. `Operator.ReleaseSingletonLease`
checks the exact name, holder and generation, so a stale callback cannot release
a replacement. Failure, cancellation and panic release the captured lease.

The unused claim-liveness SQL helpers and token scope decoder are removed.

## Tenant writes and typed claim fences

`Store.CreateRun`, `CreateTrigger`, `CreateTriggerWithRun` and `FinishRun` are
removed. Select the registered tenant with `ForTeam(ctx, team)` for tenant
operations. Executable lifecycle writes use `Tenant.AdmitRun`, `AcceptPlan`,
`TransitionNode` and `FinishRunAndUnit` with current authority.

`Store.SetNodeArtifactManifest` and `Tenant.SetNodeArtifactManifest` are
removed. Commit the authorized manifest record with the terminal
`TransitionNode` result through `NodeResult.ManifestRecord`. The separate
raw manifest-column write is removed.

`Store.StartNode` and `Tenant.StartNode` are removed. Start a claimed job with
`Tenant.TransitionNode`, `NodeRunning`, `CauseStart` and the source attempt.
The transaction checks the current holder and establishes the attempt ordinal.

`Tenant.CreateSourceTriggerWithRun` and the unused source-upload admission
branch are removed. Source acquisition uses the accepted request and source
contracts. `NodeClaimFence`, `TriggerClaimFence`, their context constructors,
readers and `WithoutClaimFences` are removed. The unused holder, membership,
reservation, generation, attempt and trigger-generation header constants are
removed too. Supply the typed actor and claim
fence on each lifecycle request. Tenant checks remain in metric and metadata
writes. Metric insertion uses `(team, run_id, node_id, ts)`, accepts identical
retries and refuses conflicting readings without changing the stored sample.

## Authorized team mutations

C2's `teamWriter` is implemented on `Tenant`. Rename, invitations and build trust
hold the team lock and recheck current administration. Invitations derive their
identity from the tenant, committed account and request key. The existing row
stores the immutable normalized email, role, actor and timestamps. Exact replay
returns that row, including after withdrawal. Changed input conflicts.

Owner deletion rechecks complete ancestry, current membership and deletion
guards under the store clock before closing the team. Run deletion checks current
`runs.control`, requires drained claims, removes a terminal run and its success pin, and preserves billing
facts. A repeated direct store call still requires live authority. The HTTP
run-target resolver still needs a deletion-cleanup retry contract because it
cannot resolve a run after its row has gone.

The six raw tenant mutation APIs are removed. Test-only fixture helpers invoke
the new transactions with current account sessions. The obsolete concurrency
memo deletion test is removed with its retired table. Native run completion and
removal cover ownership, active refusal, financial retention and cleanup replay.

## Fenced metadata and legacy settlement retirement

The raw Store/Tenant plan, step, annotation, summary, activity and approval
writers are removed. `AcceptPlan` and `AcceptExpansion` own declarations.
`RecordStep` and step metadata require the live holder and
current attempt. `TransitionNode` closes running steps when their attempt ends,
scoped by tenant, job, attempt and generation. Retry steps remain separate.
Approval opening, decisions and timeouts use `TransitionNode`. The old approval
resolver, settlement engine and its default-team getter are removed.

The unused `StateStoreOld`, `ResolveClaimInput` family, memo-key codec,
`UpdatePlanSnapshot`, `OwnsRun`, `ListPendingTriggersForParent` and
`RunExceedsWallClock` are removed. There is no compatibility wrapper. Current
record/read-set authorization replaces the legacy input reader. The controller's
native declared-reference lookup remains W2's integration responsibility.


## Placement billing and execution-attempt retirement

Cloud placement is metered regardless of the launcher's root metering flag.
Award issuance and queue eligibility use the same placement rule. Customer
runner placement retains the root's metering policy. Persisted award billing
does not change when that root's policy changes.

`AcknowledgeNodeExecutionStart`, `FinishNodeExecutionAttempt`, `ExecutionStart`
and `ExecutionAttemptFinish` are removed with their local and trigger writers.
`Tenant.TransitionNode` owns attempt ordinals, attribution, retry and completion.
`Store.ListNodeExecutionAttempts` is removed. The private tenant method
`listNodeExecutionAttempts` reads the final `node_attempts` table under a snapshot
and refuses a foreign run or job.
The unused coordinator ID reader and transaction helper are removed too.

The unused `GitHubRunnerCredentialPush`, `GitHubRunnerPush`,
`GitHubRunnerScope`, scope context helpers, `GitHubRunnerAdmits`,
`TriggerNamesGitHubRepo` and principal-prefix constant are removed. Current
push binding, enrollment and the shared matcher retain repository authority.

## Sole claim queue and console runner metering

`Store.ClaimNextReadyNode`, `ClaimSpecificTrigger`, `ClaimSpecificTriggerFor`,
`PrincipalHoldsTriggerClaim` and `Tenant.TriggerClaimant` are removed.
`Tenant.HeartbeatTrigger` and `FinishTrigger` are removed too. `ClaimUnit`
owns named and queued job and owner awards. `RenewUnit`, `ReleaseUnit` and
`FinishRunAndUnit` own the awarded lifecycle. The old token-table scope fallback,
second matcher, queue scanner, placement hold and claim-token minting helpers are
deleted. `WithClaimProfile` and the store's `RepoFilter` alias are removed.
Claimant identity and repository authority come from current enrollment.

`Operator.SetRunnerMetered(ctx, Authorized, prefix, bool)` implements C2's
console-only writer. The public selector names an enrolled machine root by its
capability prefix. The transaction rechecks the direct account session, selected
console route rule, controller account/lifetime policy and the root's current
ancestry. It updates that root's metering projection and authority envelope
together. Existing awards and their immutable billing facts do not change.

The C1-local `Store.SetConsolePolicy` accepts the controller's current console
predicate before concurrent use. `Server.New` supplies its existing direct-session,
configured-account, license and absolute-lifetime checks. No new credential class
or operator list is stored. Missing policy refuses the mutation. Roleless direct
sessions can be operators when the same controller policy permits them.

`ListRegisteredAgents` keeps a rotated credential until its revocation deadline.
Its snapshot still resolves full ancestry, membership and claimant grants.
The generated grace test covers both sides of the exact deadline.

## Capability readers and configured job placement

`Store.LookupToken`, `LookupTokenByPrefix`, `ListTokens` and `RevokeToken` are
removed. Authentication uses `ReadAuthentication` or `LookupCapability` with a
complete digest. Inventory and mutation use the tenant or operator capability
transactions. Authentication reads never extend credential expiry. The unused
`NodeClaimOrigin`, `TriggerClaimOrigin`, `Tenant.ListAgentClaims`, `AgentClaim`,
`ClaimedRunFor` and `ClaimedRun` are removed. Secret selection uses `ReadSecret`
with current authority. The raw `CreateHereTriggerWithRun` issuer, `HereRunner`
and principal-prefix helpers are removed. Here admission and award use
`AdmitRun` and `Issue`, with `limits.ClaimWindow` bounding first acquisition.

`RecordRunnerPoll(ctx, UnitDemand)` replaces `RecordAgentLabels`. It checks live
machine, Actions, launcher or here authority, strips reserved label keys and
stores observed labels, platform and the store timestamp on the capability row.
`capabilities.claim_observation_json` holds the observations. It carries no bearer.
The observation never supplies granted identity, pools or repository authority.
A here trigger award copies its host observation onto the owner capability,
without the trigger request's target or kind filter. The owner can then take
eligible nodes through the same query as every other claimant.

`ClaimPlacement.Preferences` is an ordered list of `PlacementPreference` values.
Each step has matcher `Prefers` terms and a `Hold`. An empty term list matches
every eligible runner. Holds accumulate from the job's persisted `ready_at` on
the store clock. A later claimant waits only if a live, eligible earlier runner
has time left in its cumulative hold. Earlier preferences remain eligible after
their hold expires. An explicit job preference replaces the configured list
with that preference and everyone else, using the first configured hold.
The existing controller flags configure this two-step case.

Named, queued and reconciled job claims use the same policy and matcher.
The store rechecks the earlier runner's ancestry, tenant, job-claim rights,
repository, selectors and pools. Poll JSON records its own
store-clock `observed_at`. Other credential activity cannot refresh that poll.
Held claims leave the job, awards and event sequence unchanged. The removed
`RunnerPresence` and caller-supplied `Live` list have no consumers. Liveness
comes from accepted persisted polls.

### Runner composition amendment: owner self-execution

`ReadOwnerSnapshot` adds `NodeRow.OwnerExecution` only to an authenticated private
trigger snapshot. The decision binds owner authority, team and generation to the
node reference, generation and declaration hash. `OwnerCanExecute(principal)`
checks those bindings before the host selects inline execution. Public snapshots
and plan/eval snapshots carry no self-execution decision.

The projection and transactional reconciliation use the shared SQL eligibility
predicate and configured preference holds. Reconciliation returns Dispatch
when current eligibility declines self-execution. It still performs
the normal transactional claim checks before issuing any node award.

Local hosts use their configured profile resolver. Remote trigger owners use
the persisted poll of a current registered ancestor with the same machine
subject. The live owner lease supplies self-claim liveness because its occupied
slot can prevent another root poll. Owner execution offers are constructed from
captured labels and platform, with one slot for the owner's own run, independently
of the poll's remaining queue slots and target or kind filters. Zero or future
observation times are refused.
Other runners still need fresh root polls to hold work through preferences.

Plan private snapshots require `data.read`, `plan_owner` and the live plan lease.
Eval private snapshots require `data.read` and `source_report`, whose source and
expansion lease fences remain independent of the terminal source's node lease.

An unaccepted plan owner can finish its own run as failed or cancelled under its
current planning lease. The store refuses success, start, owner-policy verdicts
and any run with a positive `plan_accepted_generation`. A retry may carry its
source spec while that generation remains zero; only accepting the current
invocation's plan transfers ownership to the controller. Pre-handoff failure
cancels the carried plan's unreleased jobs atomically and does not derive the
preparation-failure verdict from those unexecuted jobs. A plan capability cannot
release, execute, materialize or decide copied jobs before that handoff; its
node authority is limited to cancellation. Run-level concurrency slots remain
available under the planning lease; copied-job slots require executable owner
authority. Caller-selected actor kinds cannot turn a planning lease into that
authority. Internal job cancellations retain
the owner fence without reinterpreting the validated run HTTP proof as a node
HTTP proof. It replaces caller error text with the fixed
preparation-failure or cancellation stage, then closes the planning unit and run
in the same transaction. Exact failure replay remains valid after closure.

## Seeded bootstrap and legacy execution retirement

`ProvisionSpec.Seed` is validated by `Store.issue` before any origin can issue.
Only the bootstrap origin accepts it. `Store.EnsureBootstrapCapability` supplies
C2's startup adapter. The existing settings table retains the consumed
`bootstrap_admin` gate independently of capability retention. Gate locking,
first-administrator checking, digest persistence and gate consumption share the
issuing transaction. A live repeated seed is a no-op. A consumed seed cannot
revive after revocation, expiry, rotation or retention. Readiness uses
`Store.HasDeploymentAdministrator` with full live ancestry and the store clock.
The legacy `Store.CreateTokenIfNoneExist` issuer is removed.

`Store.AuthorizeClaimToken`, `AuthorizeRunnerClaim`, `ReplayClaimResult`,
`ReportAttempt`, `EnqueueChildRun`, `IsChildRunOf`, `CheckClaimSlot`,
`CheckClaimKey`, `PrincipalHoldsRunClaim`, `TriggerClaimFenceIsLive` and
`NodeClaimFenceIsLive` are removed. Their legacy route-class, runner-claim,
result-commit and attempt-report types are removed too. Current capability
snapshots, binding checks, node transitions and child admission own the retained
behavior. The old attempt writer, child writer and retry-backoff helper have no
remaining callers. Current child admission remains `Tenant.AdmitRun`.

## Named job awards and renewal

`Store.ClaimNamedNode`, `NamedClaimOptions`, `HeartbeatNodeClaim` and
`HeartbeatNodeClaimWithCredits` are removed. Named job awards use `ClaimUnit`
with a `UnitNode` target and live capability authority. The stored enrollment
selects the holder and placement. Naming a job requires the same readiness,
repository, grant, label and funding checks as the queue. Owners self-claim through
`ReconcileNode`. Renewal uses `RenewUnit` with the awarded capability prefix.

The legacy node claim and node heartbeat HTTP routes are removed. The current
engine routes own claim delivery, exchange and renewal. No caller-selected
holder, lease or sizes-to-class flag survives on those routes.

`TestNamedJobClaimCannotBypassReadinessOrAuthorityProperty` drives real intake,
acceptance, enrollment, start, report and owner completion with generated job
identifiers and labels. Refused requests preserve the full snapshot,
award count and event count. `TestRenewUnitCannotMutateAStaleJobProperty` checks
root, revoked, expired, superseded, wrong-holder and forged authority with both
execution observations. Refusal preserves the job and creates no interval or
attempt.

## Unified trigger queue

`Store.ClaimNextTrigger` and `Store.ClaimNextTriggerFor` are removed with their
separate scan, cursor and candidate decoder. Queue claims use `Operator.ClaimUnit`
with `UnitDemand.Kinds` containing `capability.UnitTrigger`. The live capability
supplies tenant and repository authority. Named and queued claims share matching,
issuance and billing in one transaction.

`TestQueuedTriggerRepositoryScopeProperty` checks FIFO behind more than 64
excluded runs and unchanged skipped owner rows and snapshots. It provisions and delegates real
capabilities and admits every run through the store. The concurrent tenant
property uses eight workers and two teams to check unique, tenant-bound awards.

## Explicit tenant readers and owner completion

`FinishRunAtGeneration` is removed from Store and Tenant. Current completion
uses `Tenant.FinishRunAndUnit`. The generated owner race check awards, releases
and reclaims real units before racing stale failure against current success.
Stale completion, same-verdict replay and release cannot change the successor's
terminal snapshot. Existing completion properties cover foreign tenants,
forged fences, rollback and concurrent exact replay.
`FinishTriggerAtGeneration`, `ReleaseClaimAtGeneration` and
`TriggerClaimGeneration` are also removed from both handles. `ReleaseUnit`
derives current authority and atomically releases only unstarted work. Its
generated owner property checks 192 forged trigger, plan and eval fences
without changing their rows, run snapshot or billing. Failure completion
retains the run verdict and masked error while closing its owner queue unit.

Nineteen default-team Store wrappers are removed. Their callers select an
explicit tenant for trigger reads and legacy mutations, run counts and deletion,
step and metric reads, approval and debug lists, output-reference completion
and settlement reads. The three retired generation APIs are included in this
wrapper count. Three unused run-filter version probes are removed too. The
remaining tenant implementations and the advertised filter version remain.

## Raw secret and launcher issuance cuts

`Tenant.CreateOrReplaceSecret`, `DeleteSecret`, `GetSecret` and
`GetSecretForRun` are removed. Mutations use the current administration
transactions `WriteSecret` and `RemoveSecret`. Claim reads use `ReadSecret`.
Metadata fixtures select an exact `GetSecretRow` scope. The store clock,
actor, count limit, size limit and default-team exemption remain unchanged.

`Store.MintClaimToken`, its private launcher issuer and `MaxClaimTokenLifetime`
are removed. The only production claim-token mint left is the legacy runner
path pending its caller cutover. Current awards use `Store.issue`. Legacy
transport tests seed presentation state without a production launcher issuer.
The unused `Operator.TeamRunIDs` report is removed with its legacy per-run
object-purge contract. Tenant purge keeps its scoped inventory checks.

<!-- design:begin -->

Machine registration adds `teams.members_can_add_machines`, a checked 0/1
column defaulting to 1. Baseline version 7 refuses older stores without migration.
The native Tenant.TeamSettings and SetAuthorizedTeamSettings methods use
[`store.TeamSettings`](../../../internal/store/controller_requests.go). HTTP and CLI
use the controller wire projection. Setting writes use the existing
team administration transaction and atomically record `team.settings_changed`.
Root issuance checks the setting under the same team lock. Current administration
is always required for shared-runner enrollment, rotation and revocation.
Personal enrollment remains bound to its independent account-session owner.

## Raw cancellation and unused readers removed

`Store.RequestCancel`, `Tenant.RequestCancel`, `Store.CancelPendingTrigger`
and `Tenant.CancelPendingTrigger` are removed. Cancellation uses the tenant's
`TransitionRun` with current authority and `RunCancel`. It records the first
intent without taking result rights from live holders. The elected unit reaper
finishes cancelled unclaimed owner units. Terminal run outcomes never change.
The separate controller-plan cancellation writer is also removed.

The unused `FilterRuns` in-memory run selector is removed. SQL listing and
counting keep their filters and cursors. The unused transaction helpers
`fenceSensitiveClaimTx` and `fenceSensitiveTriggerClaimTx` are removed.

## Legacy issuance and report retirement

`Store.RotateToken` and `Tenant.MintGitHubRunnerCredential` are removed.
Capability rotation uses `Tenant.RotateCapability`. Actions exchange uses
`Store.ProvisionCapability` with verified `ProvisionActions` evidence. The
capability quota counts live roots. Expiry frees quota without deleting the
retained authority rows.

`Tenant.ReleaseConcurrencySlot`, `ResolveCoalesceFollowers` and
`ActiveConcurrencyHolder` are removed. Terminal node and run transitions release
complete reservations. Hosts retry `MayStart` for grouped promotion. Raw holder
release, follower resolution and per-group promotion exist only in ledger tests.

`Operator.UsageMetrics`, its weekly-report types and `UsageWeekStart` are
removed with the retired usage-metrics route. The unused
`Tenant.ListSpawnedChildrenByRun` trigger reader and its unused `SpawnedChild`
decoration input type are removed.

Attempt starts derive executor class and location from claim ancestry and
billing provenance. Machine names come from the registered subject. Actions
attempts retain their push ancestor's numeric repository and workflow run,
with the enrollment's repository display name when available. A trigger-host
award retains that ancestry when it awards a job. Host observations can supply
only a local host or cloud pod display name, subject to the existing name
bound and control-character filtering. Caller metadata cannot replace machine
or Actions identity. The generated SQLite property covers direct and hosted
claims with forged metadata. Native host-observation transport remains separate.

## Membership and schema inventory

`Tenant.SetMemberRole`, `RemoveMember`, `MaxCLITokensPerMember` and
`ErrCLITokenLimit` are removed. The unused default-team
`Store.ListNodeMetricsPage`, `TriggerClaimant` and `RunExceedsWallClock` twins
are removed too. Their callers select a registered tenant.
`Tenant.UpdateMembership` resolves current
account authority and owns role changes and removal. It withdraws invitations
above the remaining role, moves account sessions after removal, and revokes
capabilities created by that member in that team after removal or demotion to
reader. Account sessions move only to another current membership. With no
remaining membership, the selected session stops authenticating. A
capability-backed session retains its parent's team and ends with that parent.
Revocation updates the envelope and authority generation. Descendants
retain their ancestry check. Every effect rolls back after a failed write.

`schemaTables` is the single table inventory in `schema.go`. Financial tenant
tables set `RetainOnTeamDeletion` so both purge passes preserve them. Operational
purge, tenant SQL guards and shape tests use the inventory. Operator-owned `audit_events`,
`capabilities` and `sessions` may carry a nullable team. Audit rows are retained
after team deletion and expire only by age. Human actors and account subjects use
account IDs. Non-account claimants use capability and holder digests, with no
caller-selected actor name. System actors use fixed enums permitted by each
payload kind. Invitation subjects use invitation IDs, and audit rows contain no
email addresses or free-form personal details. Readers render erased accounts
as "deleted account". One audit writer accepts a closed, typed payload for each
kind. It retains identifiers, enums, digests and generated receipt IDs. Retained
caller names and grant keys use digests. Free-form reasons and messages are
excluded.

Audit rows are append-only against application code. Both dialects reject every
UPDATE and permit DELETE only for rows older than `operator.audit_retention_days`
(default 400 days, set through the operator storage settings API). The leased
controller maintenance pass prunes expired rows in bounded batches using the same
database-clock cutoff as the triggers. The database stamps `recorded_at` at
insertion and callers cannot set it. Retention uses that recording time. The
separate `ts` occurrence time remains available for display, so backdating an
occurrence cannot shorten retention. PostgreSQL rejects TRUNCATE. One PostgreSQL
role owns the tables and runs the app, so credentials can disable the triggers.
Separate roles are post-launch hardening. Local SQLite append-only holds only
against SQL the app issues. Bound capabilities and sessions leave with the team.

`team_first_success` records the team's first successful run in the
completion transaction and has no run foreign key. Run deletion and audit
expiry cannot make a later success the first.

Purge removes quota reservations before aggregates and removes
runs after their operational rows and restrictive pins. Generated SQLite checks
compare all surviving rows across repeated purges with adversarial tenant keys.
They exercise the purge primitive, not deletion-request authorization or billing.
The legacy deletion request and account deletion still need their capability
cutover. Production writers for `pool_access_rules` and `repos` remain open.

The unused name-based `Tenant.GitHubAppSubscribedRepository` reader is removed.
Source release continues through the authorized sealed repository transaction.
Retained GitHub subscription and automation writers persist `gh:<repository_id>`
from their validated numeric IDs. Subscription timestamps preserve nanoseconds.
Generated rename and cross-team checks verify the persisted keys. The legacy
GitHub automation admission writer still uses removed trigger columns.
Membership types and methods live in `membership.go`, separate from authority
serialization. No foreign contract changes.

## Team token API retirement

`Tenant.CreateMemberToken`, `CreateMachineToken`, `TeamTokens`, `TeamToken`
and `RevokeTeamToken` are removed with `TokenFamily`, its constants,
`TeamTokenFamily`, `Token.Family` and `MachinePrincipal`. Production callers
already use capability provisioning and management. Member capabilities use
`Store.ProvisionCapability` under current session authority. Machine roots use
the registered `ProvisionMachine` origin. Inventory, rotation and revocation
use the authority-bearing capability methods.

The current-membership scope property checks generated grant subsets against
the committing role and refuses removed members without persisting a credential.
The membership-drop lock test now calls `ProvisionCapability` for member and
machine roots. Its PostgreSQL execution remains pending. Legacy identity and
deletion fixtures still using raw tokens seed through `CreateTokenWith`; their
remaining raw-token behavior is separate retirement work.

## Explicit secret tenants and unused fence readers

`Store.CreateOrReplaceSecret`, `GetSecret`, `GetSecretRow`,
`GetSecretForPipeline`, `GetSecretForRun`, `ListSecrets` and `DeleteSecret`
are removed. Their tenant methods remain. Local store readers select
`DefaultTeam` through `ForTeam(ctx, team)` and handle selection errors.
Controller requests continue to use their authenticated tenant.

The unused `Store.NodeClaimFenceNodeForRun`, `NodeExecutionAttemptIsLive`,
`TriggerExecutionAttemptIsLive` and `TriggerExecutionAttemptBelongsToLiveClaim`
readers are removed. Current authentication snapshots and tagged log persistence
retain the attempt, owner, ancestry and tail-grace checks.
`NodeExecutionAttemptBelongsToLiveClaim` is removed after its execution-reporting
caller migrated.

## Claim lifecycle and credential mutation cuts

`Store.StartClaimExecution`, `HeartbeatClaim` and `ClaimBeat` are removed.
Their legacy HTTP claim-token branches and client helpers are removed.
`Operator.RenewUnit` owns capability-derived unit renewal and owner compute.
Job execution starts use `Tenant.TransitionNode` with `CauseStart` and the
source attempt ordinal. The separate raw runner heartbeat/start paths still
need retirement with their consumers.

`Tenant.PutGitCredential`, `ConfirmGitCredential` and `DeleteGitCredential`
are removed. Callers use `WriteGitCredential`, `ConfirmGitCredentialFor` and
`RemoveGitCredential`, which check current authority in the committing
transaction. The remaining HTTP fakes seed their own persisted administration
session and retain the authenticated actor only as their recorded test input.
Their authority substitution is test-only. Store authority and release
properties exercise real current membership, ancestry and holder fences.

`Store.ResealSecrets`, `SecretResealCounts`, its batch reader and the controller's
startup plaintext scan are removed under the pre-launch no-backfill rule.
`SampleSealedSecrets` remains for missing-key detection and reads `TeamSecret`
directly without the retired private rotation tuple. Keyless credential access
still returns `ErrKeyRefused`. Credential HTTP encryption-failure properties
continue to require no stored row after refusal.

## Source credential hard cut and recovery proofs

The unused `Tenant.ReleaseGitCredential`, `GitCredentialReleases`,
`SetGitCredentialMachine` and `GitCredentialMachine` APIs are removed with
`GitCredentialRelease`, `SourceSpend`, `MaxSourceMints`,
`ErrSourceCredentialSpent` and the legacy source-counter helpers.
`ReleaseSourceCredential` and `SourceReleaseAudits` retain transactional
authority, host confirmation and audit ownership. **Superseded by decision 6c (2026-10-18):** `SetSourceMachineConsent`
and the `git_credential_machines` table are removed.
The unused capabilities.source_mints column is removed. No token-table release
or caller-clock fallback remains.

All eight store recovery harness cells now run as ordinary `internal/store` tests.
They open through storetest and seed sessions, machine registrations, admission,
plans, owner claims and job claims through store APIs. Retry policy is accepted
in the plan. Successor owners and competing runs use real issuance and admission.
Tests retain reopen, cancellation, retry replay, free unstarted lapses, disruption
caps, reconciliation races, local orphan recovery and concurrency adoption.
Lease-expiry observations still use scoped projection updates. The shared
recovery fixture remains for host-event and billing consumers. Native cells
remain pending.

## Planning units and terminal run completion

The unused legacy `Store.CreatePlanNode` writer is removed. Admission creates
an operational planning or trigger unit, separate from the job graph.
`FinishRunIfActive` and `FinishRunsIfActive` are removed from Store and Tenant.
`Tenant.FinishRunAndUnit` owns terminal verdicts, owner closure, events and
completion intents in one transaction. Exact replay retains the finish time.
Conflicting terminal reports fail. Completion checks include concurrent real
SQLite connections, generated owner and tenant fences, and event-write rollback.

Queue ordering and matching checks now live in `internal/store` tests. They seed
accounts, enrolled machines, Actions registration, intake, plans and readiness
through store APIs. Named and queued claims retain the same matcher refusals.
The queue check preserves the blocked job's complete row across unsuccessful
scans and proves that current push authority excludes another repository.
The native scenario catalog is unchanged.

## Shared claim eligibility and delivery effects

Every named, queued, personal, burst, self-hosted, pool and Cloud claim uses one
SQL eligibility predicate. It includes readiness, current grants and bindings,
repository access, trust, all selector terms, exclusions, placement and funding.
Host plan acceptance and settings resolution share one label normalizer: trim
outer whitespace, remove empty labels and keep the first occurrence. SDKs carry
raw declarations. Selector terms and alternatives are indexed when work is sealed.
Required-label eligibility and route selection share one reader of that index;
runner selection keeps its label-term alternatives.
`claim_contexts` assigns immutable policy contexts to source rows at intake,
plan acceptance and expansion insertion. A context hash includes the request,
provenance, resolved options and guards, job execution settings, repository
scope, allowed pools and creator. Seal identities, source revisions, timestamps and unrelated job declarations
do not split otherwise equal contexts. The context row stores its identity,
requirement set and unit kind, and copies the overflow account and minute limit
for indexed budget checks. Finished sources clear their context reference in the
finishing transaction. A context is deleted when its last source leaves. History
reads the sealed run and job settings, not this search index.

Push-bound Actions claims seek runs by team, repository key and sealed provenance
SHA/ref and the shared verified-trust push predicate, then use the shared eligibility
predicate. Run-bound and here claims seek
the bound run directly. Intake writes the provenance index from the sealed identity.
Named rechecks seek the complete team and unit key.

Broad claimants examine teams by their last committed award ordinal. Exclusions
filter the team list. Context routes apply the shared overflow budget predicate
before node lookups. Pool trust, funding and standing use the canonical per-unit
eligibility predicate, including its unpriced exception.
Overflow usage is loaded only for the team being examined and accounts with ready
work. Label lookups select matching requirement sets, then immutable context routes
with ready work. Indexed heads prove shared-claimant ranking holds. Each remaining route selects its first eligible
source through a correlated application of the canonical predicate. There is no
rejected-head probe cap, cursor advancement or fallback batch.
When neither the claimant nor its placement observers use Cloud, selection skips
overflow funding preparation entirely.

Candidate search uses a read snapshot without arbitration or row locks. It may
visit multiple policies or ready sources. After search closes its snapshot, the
award transaction takes queue arbitration and applies the same eligibility
predicate to only the selected unit, with fresh authority, placement observers
and funding. A broad-search candidate keeps the team's award ordinal from the
snapshot. Recheck requires that ordinal to remain unchanged because another
award can move the team behind other eligible work. A stale candidate returns
not-found for this poll. It does not restart the search under the lock. Backlog
and retained contexts cannot increase the number of candidate rows checked while
arbitration is held.

Local foreground intake seals `Request.LocalForeground`. An omitted dispatch
is resolved from the request before checking foreground authority. The shared predicate
reserves the original intake's generation-zero trigger for a named claim by the
submitting OS account. Queued polls retain their original unnamed intent when
an indexed head recheck names a candidate. Detached intake, retry destinations
and subsequent lease recovery use normal queue eligibility. No process-local
queue gate participates in this reservation.
There is no persisted scan state. Ordering uses each team's last committed award
ordinal, then arrival and stable identity. Only an awarded claim advances that
ordinal. Queue arbitration serializes holds and awards. Claims take the controller
lease, queue arbitration, authority and unit rows, then the ledger. Taking queue
arbitration or a new team lock after a ledger lock poisons the transaction before
acquisition. Billing pages, standing decisions, and team and account deletion
take team rows and authority before ledger locks. Financial writers take team rows
with `FOR UPDATE`. Award rechecks SQL eligibility and reserves the unit and funding atomically.
For a ready job naming no runner, the existing placement-hold predicate holds
work for the best-ranked tier with a live claimant on the same team, matching
labels and a free slot in its latest poll. The hold ends when that claimant takes
the job or the 5-second `RankingWindow` expires. Busy or offline higher tiers
cause no wait. Any eligible claimant may take the job after the window; there is
no post-window grace. Both ranking and `Prefers` holds use one admissible
claimant/work relation: complete eligibility, ranking permission and a free slot.
Owner self-execution offers its own slot instead of reusing the last poll.
Preference scoring and durable asks
are removed. Polls carry optional `free_slots`: absent means one available slot;
zero means busy. Each award consumes one advertised slot. Each loop reports
its configured concurrency minus active awards.
Context and requirement references prevent retention from removing policy still
used by work. Run retention removes unused contexts before unreferenced
requirement sets and their terms. Tenant purge follows the same dependency order. Work without ranking,
burst placement or preferences omits those relations from its SQL plan. Ranked
work without preferences keeps ranking and omits preference relations.
Named work without placement relations hydrates only its current claimant.
Scoped polls hydrate machines from their source team and keep service observers.
The predicate joins work to claimant scope by team before evaluating pairs.
Cancelled, finished or expired named work reports its state refusal before
funding diagnostics. Named claims resolve the run within the claimant's team
before kind or trust diagnostics. Only the Cloud launcher may resolve across teams.
Repository routing identities are lowercased by `sourceurl.RoutingIdentity` at intake.
Private owner placement projections use that same SQL predicate before readiness.
They write no awards, reservations or fair-share ordinals. Readiness is
rechecked when the owner asks to execute.
Runner matching has no CPU or memory fit. Cloud size labels map to credit classes
in one catalog. Existing AND-between-terms and OR-within-terms semantics remain.

A per-team fault ledger owns attribution, deduplication, claim blocking and
quarantine permission. Counted faults and nodes permitted for quarantine are
separate sets. Every contained release fault is counted before claiming again.

Intake funding is a placement and billing policy decision. Current occupancy and
launcher registration cannot turn permitted unmetered work into a paid-only
credit failure.

A canonical delivery and targeted effect identify one transactional receipt.
Targets include `admit:<pipeline>`, `publish:<repository>`,
`withdraw:<repository>` and `lifecycle:<kind>`. Each receipt stores the committed
result. Reprocessing returns it without repeating the effect. Empty effect keys
are refused. Repository publication generations and consent are captured before
source fetch, checked and advanced in the publishing transaction. Withdrawals
and consent changes advance that generation. Installation connection generation
fences remain in force. Losing that connection during source preparation is a
revoked admission or publication, with no receipt for the refused effect.
Failed deliveries never retry automatically.

`Store.ClaimLaunch`, `LaunchClaim`, `LaunchClaimRequest`, `Client.ClaimLaunch`,
launcher sweep frontiers and probe savepoints are removed. Bootstrap exchange
preserves the award fence, and pool claims still launch a fresh pod per claim.

The unused `Tenant.SetRepoDispatch`, `Store.SeedToken`,
`ErrSeededTokenRevoked`, `Store.CheckClaimSensitive`,
`Store.PrincipalHoldsProfileClaim`, `Tenant.SpendSourceCredential`,
`Tenant.SourceClaimant` and `Tenant.GitCredentialMachines` are removed.
The capability issuer, binding transactions, sealed intake and source release
own their retained responsibilities. Seeded startup bootstrap uses the gated
`Store.issue` transaction described above.

Installation withdrawal and listing reconciliation share one targeted receipt.
It commits the complete original repository set and aggregate result with the
withdrawals, before a replay can enumerate current repositories. Individual
repository effects retain their own receipts. A changed listing or processing
branch cannot expand an already applied installation effect.

## Launcher award snapshots

`Operator.ReconcileLauncherAwards(ctx, authority, observed, reservations,
preparing, rejected)` correlates observations through unit rows before validating
capability envelopes and their lookup projections. Root ancestry and service
subject determine ownership across teams. Exchange replaces the stored credential
without changing the observed award fence. Current seals, job settings, committed
resources and parent expiry are read together. Eval liveness checks its source
generation and the current controller lease.

One reconciliation owner isolates typed `integrity_failure` errors during
preparation renewal, rejection, reservation validation and snapshot decoding.
Each damaged award is deferred under its observed identity while healthy siblings
proceed. JSON decoding, semantic job state and persisted billing validators use
this category. Database and launcher-authority failures discard the response.

Preparation renews an exact bootstrap fence. Rejection fails only an exact owned
award. Reservations must match the original bootstrap's ancestry and subject even
after expiry or exchange. An unknown or foreign reservation refuses the request.

The default `LauncherSyncReader` validates unique Job and award observations. It
returns cleanup only after confirming an ended lease, cancelled or terminal unit,
replaced fence or different launcher owner. Missing snapshot entries are deferred.
Cleanup names only the observed namespace/name/UID or reservation identity. The
response carries no bearer material.

`Store.SyncLaunchJobs`, its types and helpers, and `Client.LauncherSync` are
removed. Unstarted release uses `ReleaseUnit`. The owner redispatches pending
jobs through its normal readiness transition. The elected reaper recovers lost
or cancelled holders.

The `placement-run-host-to-launcher/store-sqlite` cell checks real claim,
exchange, release, owner redispatch, replacement and synchronization across three
reopenings. Native Kubernetes adoption and UID-fenced teardown remain separate
integration evidence.

## Capability inventory and rotation

Tenant and Operator implement C2's CapabilityManager. Collection and detail reads
rerun the selected route alternative against current ancestry and membership in
one snapshot. A tenant member sees owned account and machine credentials. A
current team administrator sees the team's inventory. Operator reads expose only
deployment service and operator roots. Execution awards, push credentials,
node-host credentials and private authority fields stay outside this inventory.

Rotation locks the predecessor, rechecks current ownership and issues its
replacement through Store.issue. Delegated credentials keep their original
parent. Provisioned roots keep their origin and registration in CapabilityRecord.Origin.
The committing transaction preserves scopes, owner, metering and source consent,
checks current origin or parent ceilings, schedules the predecessor's grace
and advances its authority generation. Expiry during origin verification rolls
back the replacement. No bearer leaves a failed transaction.

A transaction-scoped replacement identifies the locked root to the existing
provisioner. It permits replacing an owned member root under current account or
team-admin authority, preserves existing machine metering, and requires current
deployment token authority to replace a bootstrap operator root. It cannot
change the locked root's scope. Ordinary member-root creation still requires a
session. Machine and service replacement rechecks the configured origin policy.
Missing provisioning origin is an integrity failure. No legacy-token fallback
or new issuance variant exists.

The SQLite credentials-delegation-subset/store-sqlite cell verifies inventory,
root and descendant behavior, rotation and revocation through two reopenings.
The real HTTP component uses the default Tenant manager with session proof and
CSRF. Machine/service enrollment uses persisted capability registrations by default.
Console sessions, host observations and native PostgreSQL remain
integration obligations.

## Committed result copies

`NodeRow.OutputRecord` and `ManifestRecord` persist the exact selectors committed
with terminal state. Materialized copies use `blob.MaterializedRecordKey` with
the destination result sequence. The destination has no synthetic attempt.
`Policies.ResultBlobs` configures both attachment and materialization. Its reader
also supplies `VerifyBlob(context.Context, blob.Blob)` for copies. The filesystem
and S3 backends verify every selected object and manifest member before mutation.

`CopyMaterializedRecords` retains source provenance and reassigns record lifetime
to the destination run. It uses the shared record mutation owner, leaving commit
and rollback to reconciliation. A materialization savepoint discards all copies
on a missing, expired or corrupt source. A materialize-only miss returns Dispatch.
An ordinary miss continues to execution admission. Authority errors stay errors.
Memo sources name their exact record and need no source attempt or surviving run.

The private execution wire carries both selectors unchanged. Native host storage
reads select them directly. Trigger and plan owners can read selected terminal
records in their run. Dependencies and pinned references require the exact selected
record even when the producing result has no attempt. Digest equality grants no
record metadata authority.

`Tenant.ResolveBlobAuthority` exposes the transaction-local scope and uploader
resolver to the data controller. It accepts only a data read or write grant and
refuses caller hints that differ from persisted authority. Failed resolution
returns no authority. `DataController` uses this resolver for capability records
when no override is supplied. The materialization proof needs no custom data
authority callback and checks identity substitution and revocation after binding.

## Payment mutation transactions

Card setup, collection, payment reversal, dispute hold/release, warning and
refund-queue mutations have transaction-local owners. Their existing Tenant and
Operator entry points call those owners in a store transaction. Payment receipt
settlement can use the same owners without nesting transactions or committing
financial state before its receipt. No new unverified payment entry point exists.

Stripe holds all card data (decision 65). No table column or persisted event
JSON stores a card brand, last four digits, fingerprint, payment method ID or
payer email. Per team the store keeps only `card_customer` and `card_added_at`;
the ledger keeps checkout, payment and refund IDs, amounts, modes and statuses.
Refills charge the Stripe customer's default payment method off-session, and the
billing page shows and changes the card through Stripe's customer portal. Stripe
Radar handles card fraud. The store never links one card across teams, so abuse
of one card across many teams is a Radar rule or a manual matter. An actionable
early fraud warning holds only the team its payment funded.
`TestNoCardDetailsAreStoredFromSetupThroughPurge` scans every column of every
table after a card setup, payment, refill, warning, refund and purge.

Card setup ordering uses the supplied completion timestamp at nanosecond
precision, or receipt time when completion time is absent. Older timestamps
cannot replace the recorded card setup. The subsecond migration marker is removed.
Collection amounts must fit the credit ledger's integer representation.
Refund queue state is read and updated under one transaction and ledger lock. A
refund Stripe fails or cancels is terminal and listed for the operator, never retried.
Payment settlement, refund reconciliation and purge first lock team rows FOR UPDATE.
Deletion never waits for financial finality. Purge removes operational data,
refill settings and the team's Stripe customer ID, keeping the team row as a
tombstone. Checkouts, receipts, refunds, grants, charges, holds and payer
associations survive both purge passes, so late settlement, refund callbacks and
fraud warnings still reconcile. No card details exist to scrub.
Purge final-settles every award from its retained intervals before deleting work.
Warnings and dispute holds write financial facts and a drain deadline only. Work
renewal applies that deadline. Financial writers acquire no owner or work locks
after the ledger. Refund binding uses the payer tombstone without an eligibility
check. Financial JSON never holds card details, so purge scrubs nothing.

`TestPaymentMutationsRollBackWithReceipt` exercises eight mutations against a
failing receipt boundary and checks their financial state after reopening.
Generated properties cover refund conservation, payer isolation, immutable
replay, setup ordering and unrepresentable collection amounts. Verified event
dispatch now consumes C2's expanded proof in `payment_provider_settlement.go`.
The final schema includes the receipt and correlation tables below. The setup
association still needs its trusted billing-page writer. Collection binding uses
`Operator.RecordAttemptIntent`, which takes the customer used by the trusted
worker's provider request. Provider tests use the final schema. Production
provider composition remains separate evidence.

### Non-checkout payment persistence contract

`SettlePayment` dispatches verified setup, collection, refund, warning and dispute
facts through the existing transaction-local owners. One locked transaction
checks configured mode, immutable provider-object association and event replay,
then commits the financial result and receipt. A receipt failure rolls back the
financial mutation. C2 owns provider payload/status verification. C1 owns payer
association, supported ledger currency, integer arithmetic and settlement.

The final `schema.sql` implements these local C1 requirements. Payment receipts
are operator-owned because an unbound warning has no team. A team purge retains
bound receipts and financial associations as well as unbound warnings. Card-setup
correlations and hosted-page delivery details are removed.

- `payment_events` keeps its existing columns and `(payment_mode,event_id)` key.
  Its kind check admits all `capability.PaymentEventKind` values. `checkout_id`
  becomes nullable for non-checkout events, and `team` becomes nullable only for
  a warning with no committed payer yet. Checkout events retain their composite
  checkout foreign key and required team/checkout association.
- Add `object_id TEXT NOT NULL DEFAULT ''`, `event_json TEXT` and
  `settlement_json TEXT` to that receipt. Non-checkout events require all three.
  The JSON stores the verified facts and `PaymentSettlement`, respectively.
  Checkout writes retain their existing shape. Index `(payment_mode,object_id)`.
  Several event IDs can refer to one provider object. They cannot change its
  payer association or a settled object's payment, amount, currency and charge.
  A payment-intent failure is an observation before settlement. Its amount and
  latest charge can change before success while the persisted payer
  and attempt remain fixed. Closing a dispute preserves its
  terminal outcome and prevents a late open from restoring its hold.
- Add `payment_correlations` with `payment_mode`, `provider_id`, `kind`, `team`,
  `customer_id`, `currency`, `attempt_id` and `charge_id`, all non-null TEXT.
  The primary key is `(payment_mode,provider_id)`. Mode is test/live, kind is
  setup/collection, and team references `teams(name)`. Customer is nonempty.
  Currency, attempt and charge default to empty for setup. Collection requires
  its currency and exact persisted attempt/charge. These associations are
  immutable and survive run retention.
- The trusted billing-page completion transaction records a setup correlation
  from its committed reservation and confirmed provider setup ID. The trusted
  collection adapter records a collection correlation from its exact attempt,
  internal charge, customer and confirmed payment-intent ID. It also binds
  `card_attempts.payment_intent` before settlement. Provider metadata, callback
  parameters and caller-selected teams cannot create these associations.

`RecordAttemptIntent` persists the collection correlation and the exact attempt's
payment intent in one ledger-locked transaction. It derives team and charge from
the existing attempt and checks their same-team relation. Its customer argument
comes from the trusted worker's provider request, retaining that identity across
a concurrent saved-card replacement. A checkout payment cannot fund collection.
Exact replay adds no write. Conflicting payer, customer, attempt, charge, kind,
currency, mode or payment fails without changing the attempt or finances.
The provider collection fixture uses this writer instead of inserting an
association. Generated binding tests and receipt-failure tests exercise these
owners against the declared schema fake.

`providerPaymentFixture` supplies a schema fake for this contract. It drives all
seven non-checkout outcomes, event/object replay, rollback, reopening, setup
ordering, partial refunds, warning escalation and payer isolation through the
real transaction owners. A signed refund also enters through C2's authenticator
and the public opaque-proof `SettlePayment` entry point. No provider network or
billing-page persistence integration is claimed.

Refund and dispute owners resolve the mode-scoped checkout or collection payer
before touching legacy financial rows. Unknown warnings may persist before a
payment is associated, so later settlement can refuse the warned payment.
Warnings escalate monotonically. Returning to non-actionable never removes
recorded protection. Warnings select the configured mode. The test-to-live
startup switch forgets test-provider customers while retaining payment history.

## Persisted log readers

`Store.LatestNodeLog`, `RunNodeLogs`, `NodeLogSettled` and the legacy `NodeLog`
read model are removed. Controller log reads select tagged subjects through
`Tenant.ReadRunView`, then use `logseg.CommittedReader` and `Tenant.ReadLogSegments`.
Object listings and physical manifests grant no read authority. The reader binds
its actor to the authenticated capability prefix or session digest. Each segment
read and live-ring delivery rechecks current persisted authority. Log routes keep
the existing response-byte egress meter and log download-cap exemption.

Job reads select the newest attempt by ordinal and generation. Streams resume in
that order. Whole-run reads include committed job, trigger, plan and eval subjects.
Missing or corrupt committed bytes fail the read. Persisted incomplete seals add
a loss line or a `log_incomplete` stream event. Completeness verifies bytes before
reporting the persisted seal, and never changes the job result.

`Segment`, `SegmentCommit`, `LogSegments`, `RecordSegments` and `ReadLogSegments`
live in `internal/store/result_logs.go`. Log subject validation uses
`blob.LogSubject.Validate`. Step, annotation and summary types and transactions
live in `result_metadata.go`. The completed result contract scaffold is removed.

The controller tests drive the final SQLite claim, start, segment commit and
terminal transition through the reader. Revoking its capability refuses the next
read before physical I/O. Generated fake-boundary tests cover arbitrary payloads,
foreign actors and authority ending between inventory selection and segment read.
Authenticated `LogTransport` signing/verification and host subject construction
remain separate integrations.

Remote log follow opens the stream without probing legacy segment capabilities.
Stream refusals remain errors. The old segment-unavailable classification is removed.

## Host log uploads

The pod log sink writes through the `logseg.Uploader` the host binds; the
controller's uploader signs uploads and commits segments transactionally. Its
subject comes from the authenticated binding. It never infers a team or attempt from an object prefix.
An absent binding leaves the pod's stdout log intact and reports the missing
binding. Job sinks keep live watch delivery under that exact attempt fence.

`Client.LogSegment`, `SealLog`, `LogSegmentUploader`, `store.LogSegmentRequest`
and `LogSegmentGrant` are removed with the unused `EngineNodeLog` wrapper.
`logseg.SubjectUploader`, `logseg.Data` and `SegmentRecorder` had no
production caller and are removed with their tests. The watch test uses an
in-process transport.

## Log byte accounting

`Segment.Offset` and `Segment.Size` describe decoded log bytes. `StoredBytes`
and `Digest` describe the immutable compressed payload. Both sizes are positive
and participate in replay identity. `log_segments.stored_bytes` and
`log_streams.stored_bytes` count retained physical bytes. Segment start/end
offsets and `uploaded_through` count decoded bytes. Reads return both sizes.
The schema needs no new column because decoded size is `end_offset-start_offset`.
The subject uploader supplies both sizes on append and final sealing. The
committed reader verifies stored length and digest before emitting decoded bytes.

## Controller result verification

`Server.StorePolicies` captures `storage.BlobManifests{Objects: objects}` from
`WithDataObjects`. The composition root supplies that snapshot through
`store.WithPolicies` before `Open` returns. Terminal
output and manifest attachments use that backend inside the live tenant
transaction. `Tenant.CommitRecord`, `LookupRecord`, `LookupMemo`, `LookupDepcache`
and `CopyMemo` use the configured verified manifest reader too. An explicit
`BlobRecords` reader overrides it. Without either reader, manifest operations
fail. The controller property test drives real claim/start, upload
reservation/settlement, record commit and terminal transitions. Corrupt physical
manifest bytes refuse record reads and completion. Restoring them allows
completion and exact replay. Authenticated data binding remains C2's separate
contract.

The legacy `AttemptReport.OutputFrom`, `Store.FinishNodeCopyingOutput` and
their `node_outputs` copy branches are removed. Host memo materialization
uses the existing transactional `blob.RecordStore.CopyMemo` contract. The
unused backend output-read forwarding methods are removed too.

`RecordStore.CopyMemo` is implemented, but the state machine's private
`CopyMaterializedRecords` seam remains assigned only by tests. Removing that
fixture assignment makes manual-retry, memo and coalesced reconciliation fail
with `transactional materialization records are not configured`. W1b must
implement the existing transaction-local copier so W1 can bind it inside its
state transaction. Calling the public copy method before
reconciliation would commit the records separately from the terminal state.

`Policies.ResultBlobs` also binds the real `CopyMaterializedRecords`
implementation to reconciliation's transaction. Retry and memo copies retain
exact selectors, verify physical bytes and commit with terminal state. Coalesced
copies still require a declared persisted read edge. A source ref and matching
spec hash grant no authority. The final waiter schema excludes the legacy
coalesce policy, so that table cannot establish a coalesced read. This path
refuses until its owner declares the missing authority.

`Tenant.StorageStanding` reports the selected team's policy, retained event,
blob and tagged-log bytes, and live upload reservation bytes from one snapshot.
The controller and retained-storage billing share the same blob/log inventory
owner. Historical usage counters never determine retained bytes.
The raw `Store.StorageStandingFor` and `Store.TeamStorage` reads, their
`TeamStorageUsage` type, and `Store.ReconcileFreeEventBytes` are removed.
`free_slots` records only slot ownership and `taken_at`. Event bytes come from
retained `runs.event_bytes`, including during event storage admission.
`Store.ReleaseExpiredStorage` is removed. Upload expiry uses quota admission
and the leased blob-maintenance pass.

## Amendment 1: authority and execution seams

`Tenant.RegisterGitHubRunner` and `RevokeGitHubRunner` implement C2's
`GitHubRunnerRegistry`. Registration checks current administration authority,
derives the actor, writes the canonical numeric repository key and uses the
store clock. Duplicate registration is refused without changing consent.
Enrollment and Actions issuance share the team lock. Issuance checks the
current numeric repository and owner, trusted registration and workflow identity,
fixed claim grants and the existing live-root cap. Removal revokes every
matching Actions root. Its descendants end through their retained ancestry.
The removal and authority changes commit together. Enrollment reads decode
nanosecond timestamps. The raw add/remove binding writers are removed.

SQLite tests cover failed authority, caps, concurrent issuance/removal,
transaction rollback and reopening. The controller test uses default tenant
transactions and a signed test OIDC token. Its provider transport and trusted
origin policy are fakes. Production origin-policy composition, enrolled claim
profiles and native Actions execution remain separate integrations.

`Tenant.WriteSecret`, `RemoveSecret`, `WriteGitCredential`,
`ConfirmGitCredentialFor` and `RemoveGitCredential` implement C2's credential
mutation adapter. Each transaction rechecks the selected team-admin alternative,
current ancestry and membership, derives the actor from persisted authority,
and uses the store clock. Existing enrollment limits are serialized under the
team lock. Confirmation checks the current row's fingerprint. A replacement
retains confirmation only for the same SSH fingerprint. Failed writes return
no credential and roll back the transaction.

`Tenant.ReadSecret` implements C2's authority-bearing read adapter. Members
may read unmasked rows within their tenant. Claims derive the pipeline from
their bound run even when the request omits its run selector. They require
the sealed declaration, current lease and generation, no cancellation and the
selected row's pool authorization. Pipeline rows precede shared team rows.
Each release commits `secret_released` before returning the stored envelope.
An audit or final authority failure returns no row. Claim-wide authority has
attempt zero and remains valid across execution starts. A nonzero attempt
binding is limited to that attempt. `Tenant.ReleaseClaimSecret` and its legacy
undeclared-secret error are removed. Verified local peer transport integration
remains with C2.


`Tenant.RotateCredentials` implements the credential-maintenance transaction.
It reloads persisted account authority and current team-admin membership, selects
only the authenticated tenant, visits each secret and git credential serially,
and commits both value-column changes together. The secret transform returns
`(value, skip, error)`. A skipped row retains its envelope. `CredentialRotation`
returns `Rotated`, `GitCredentials` and safe `Skipped` row identities together
from the committed attempt. Every retry resets that result. Callback, authority
and commit errors return an empty result and roll back both tables. Row metadata
stays unchanged.
The raw `Store.RotateSecretValues` and `Store.RotateGitCredentialSecrets` APIs
and their deployment-wide readers are removed. Verified local peer maintenance
still requires the C2 transport-to-authority integration.

Team-deletion requests, purge completion, recheck deadlines and deletion leases
store Unix nanoseconds. Their readers and deadline predicates use the same unit.
Purge's account `updated_at` write also uses nanoseconds. Final-schema stores
have no seconds-based compatibility reader or data conversion.

`Tenant.SourceReleaseAudits` implements the final tenant-scoped audit projection.
It reads event, run and account IDs, generation, digests of caller-supplied
identifiers and nanosecond occurrence timestamps, newest first, with the
existing 200-row bound. Secret and git-credential row timestamps also use Unix
nanoseconds.

`ReadAuthentication` projects `BindingState.CancelReason`. A job's committed
intent takes precedence over the run's intent, including an empty reason.
Owner snapshots retain the run's intent. Session lookups classify infrastructure
and integrity failures with `ErrSessionBackend`, preserving typed authentication
refusals for ended credentials.

Provider-linked account sessions may have no selected team. `sessions.team` is
nullable, with a CHECK requiring an account and no capability parent when absent.
Sessions are operator-owned identity rows. Tenant statements still carry their
team predicate. A roleless session authenticates the account with no team or
grants, including after renewal and membership creation. An explicit team switch
requires a current membership. Roleless creation still requires the linked
provider identity in the creating transaction.

Identity-link and GitHub-connect consumption use `flow_nonces` with separate
kinds, store-clock expiry checks and nanosecond timestamps. The existing unique
key arbitrates concurrent consumption. Replay survives reopening. Expiration
cleanup belongs to the leased retention pass, rather than a request-side delete.
No separate identity-link or GitHub-connect state table exists.

Future revocation timestamps remain live until their exact deadline. Transactional
authority resolution applies this rule to every ancestor and repeats the expiry
and revocation checks after locking the chain.


Deployment queue metrics use `Operator.CountPendingNodes`,
`Operator.CountNodesByQueueState`.
Their raw Store methods are removed. Queue counts partition outstanding jobs
by persisted state; a held job with no readiness timestamp is claimed once.
Expired claims remain claimed until recovery commits. Runner liveness uses
the tenant's committed observation inventory.
`Store.CountPendingTriggers` is also removed. Local queue readers select
DefaultTeam explicitly; request admission keeps its authenticated Tenant.
The tenant trigger count never includes another team's backlog.

The lifecycle billing adapter uses the shared accounting owner's unmetered
bypass without supplying a second calculator override. Renewal, release and
started/unstarted recovery of trigger, plan, eval and job units produce zero
charges and no billable intervals without consulting C2's calculator.

`UpdateNodeDeps` on Store, Tenant and Client and its HTTP endpoint are removed.
Generated dependencies come from accepted expansion membership in `Scheduling`.
Expansion never rewrites the initial plan or a dependent's declaration. Acceptance
passes the accepted plan's concurrency declarations through `ExistingConcurrency`
on initial submission and replay. The SQLite `lifecycle-generator-barrier/store-sqlite`
proof checks two members, an empty group, reopening, terminal replay and dependent
release. Delegation issuance is seeded because C2's validator remains a stub.


`Store.ReserveStorage`, `Store.CommitStorage`, `StorageReserve`,
`StorageReservation`, `StorageCommit` and `DefaultStorageReservationTTL` are
removed. Public uploads use tenant blob tickets with exact identity, size,
digest, expiry and verified settlement. Generic byte deltas cannot mint a blob
or receipt. The controller's retention probe reads `Tenant.StorageStanding`
and slot availability without writing reservations. The legacy `Store.ReserveUpload`, `Store.CommitUpload`,
`Store.UploadForTeam`, `Store.ReleaseStorage` and `UploadRequest` are removed.
Their output-upload and output-commit HTTP writers are removed too. Generic
`storage_reservations` and `storage_commit_receipts` no longer have writers.
The legacy `Store.OpenNodeLog`, `SignNodeLogSegment`, `NodeLogSegments`,
`MarkNodeLogSealed`, `GetNodeLog` and `CurrentNodeAttempt` APIs are removed.
Their node `log-segments` and `log-manifest` HTTP writers and S3 signing code
are removed too. No Go writer references `node_logs` or `node_log_segments`,
and reads no longer seal abandoned logs. `Tenant.RecordSegments` and leased
`Operator.SealAbandonedLogStreams` own tagged persistence and sealing.
Legacy capabilities no longer advertise segment upload support. Old output
reads and host/client interfaces still need their final record cutover.
The exported `Store.NodeIsAncestor` and `Tenant.NodeIsAncestor` methods are
removed. The private `Tenant.nodeIsAncestor` shares the blob read-set dependency
resolver over sealed job specs, including optional, artifact and generated-group
dependencies.
The duplicate `MaxOutputBytes` and `MaxRunOutputBytes` constants are removed.
Remaining output callers use `limits.NodeOutputBytes` (16 MiB) and
`limits.RunOutputBytes` (1 GiB). File and signed-URL reads refuse bytes above
the shared per-job bound even when their recorded size and digest match.
`SetNodeStatus` and `RevokeNodeReady` on Store, Tenant and Client are removed
with their legacy HTTP handlers. Debug pauses use `status_detail` through
activity writes and leave lifecycle state intact. Queue withdrawal uses
`Tenant.TransitionNode` with `CauseRevoke` under the current owner fence.
Claimed, running and terminal jobs cannot be withdrawn. Refused transitions
change neither state nor events. The SQLite
`lifecycle-transition-replay-and-fencing/store-sqlite` proof covers withdrawal,
reopening, holder exclusion, cleared terminal claims and exact replay.
The raw readiness publisher remains pending its caller migrations.

`Store.TouchRunHeartbeat`, its Tenant twin, `Client.TouchRunHeartbeat` and
`Run.LastHeartbeatAt` are removed with the independent scheduler ticker,
legacy HTTP handler and Doctor's heartbeat-age run finalizer. `TouchNodeHeartbeat`
on Store, Tenant and Client is also removed with the node-touch route and
Kubernetes dispatcher's advisory heartbeat loop. Job claim renewal supplies
the job's liveness timestamp. Run liveness is
the current owner lease. `RenewUnit` and `RenewOwner` renew that exact fence.
Only the elected `ReapUnits` pass recovers an expired owner. Job renewal never
extends its run owner's lease. The final C2 owner-heartbeat route remains a
declaration for W2 to bind to the implemented `EngineController.Renew` adapter.

`Tenant.AcceptPlan` is the sole plan-acceptance API. The raw `Store.AcceptPlan`,
its separate decoder/validator/inserter and `Client.SubmitPlan` are removed.
The controller's claim-token acceptance branch and old launched planner are
removed with them. Legacy plan-node bootstrap, snapshots, claims and lifecycle
writers remain pending their caller migrations. They are not v2 acceptance paths.

Required-secret preflight returns canonical `plan_invalid`. After acceptance
rolls back, its diagnostic transaction rechecks the current owner, serializes
on the tenant's run and records the detail once using the store clock. It
rechecks the owner before commit. A failed or stale diagnostic write leaves
no diagnostic. The original refusal is retained if recording fails.

The SQLite `lifecycle-request-and-plan-seals/store-sqlite` cell exercises real
admission, owner claim, strict validation, settings resolution and sealing. It
checks invalid-input refusal, rollback on the second job insertion, immutable
replay and drift after reopening. Intake policy and enrollment use contract
fakes. Native execution and HTTP transport remain separate evidence.

Schedule reads, edits, deletion and fire history belong to `Tenant`. The thirteen
implicit-default-team `Store` schedule methods are removed. Local CLI and
dashboard constructors select the registered DefaultTeam. Controller requests
retain their authenticated tenant. A deployment tick enumerates teams through
`Operator.ListCronSchedulesAcrossTeams`, then selects each tenant before resolving
its schedules. `Service.Schedules` is required for tenant operations. A nil value
selects deployment enumeration only for Tick.

`Operator.RecordCronTick` and `Operator.GetCronTick` persist deployment tick
metadata atomically in `settings`. The old Store methods and their
`sparkwing_meta` writes are removed. The cadence guard reads the same `settings`
rows its existing writer commits. `Tenant.ArmCronSchedule` writes the required
`repo_key` through `runspec.CanonicalRepository`. GitHub identities require the
resolved numeric repository ID. A non-run history entry stores SQL NULL for
run_id. A fired history entry retains the final same-team run foreign key.

The SQLite `surface-schedules-and-managed-timers/store-sqlite` proof checks two
teams sharing a repository path, foreign read/edit/history refusal, atomic rollback
when history insertion fails, nanosecond timestamps, reopening, tick metadata and
tenant deletion. The generated package property varies 32 pairs of schedules
and mutation order. Native timers, schedule launch admission and HTTP remain
integration obligations.

[Amendments 1](amendments-1.md) records the worker requests and assignments.
These declarations leave transaction implementation with W1.

- `plan_contracts.go` declares `PlanSecretOpener`. The controller installs
  the trusted decryptor before serving. Acceptance selects tenant/pipeline secrets
  in its transaction and passes plaintext only to validation. Neither intake facts
  nor secret values arrive as trusted planner JSON. `Resolved.SecretArgs` retains
  every classification sealed at intake. Describe may add classifications.
- `enrollment_contract.go` declares `ClaimProfileResolver`. It resolves persisted
  enrollment from authenticated root/subject identity. Granted runner name/type,
  pools, repository authority and metering never come from labels, environment or
  a caller-selected runner name. Enrollment configuration and claim observation
  remain separate. No new registration table is required by this seam.
  `Store.ReadClaimProfile` gives controller adapters that same enrollment owner.
  Without an injected resolver it reads the registered machine, service or Actions
  root in the authentication snapshot. Machine acceptance comes from its persisted
  canonical repository patterns. Capacity remains unknown until supplied as a host
  observation. Self-claims still require their configured host observer.
  Transactional matching re-reads registered capacity before awarding work, so a
  stale controller observation cannot widen it. Unknown capacity retains the
  matcher's existing meaning.
- `authentication_contract.go` implements the declaration of C2's
  `AuthenticationReader` on Store. A single committed snapshot includes store time,
  ancestry, current membership and route-specific unit/result/log facts. Binding,
  data-edge and source-release checks also run on the transaction owning the write.
  A source credential is released only after its authorization audit commits.
- `ProvisionSpec.Authority` is the authorizer whose current rights constrain
  provisioning. Machine, service and Actions roots have no revocation parent.
  Member capabilities retain the issuing session as their parent, so logout
  ends their authority. Membership also governs account authority at use time.
  Here awards retain the requester's session/token ancestry and current runs.write.
  `memberIssuancePolicy` owns the expiry for issuance and rotation. Member tokens
  default to and expire within 90 days. Explicit `team.admin` tokens retain the
  tighter 30-day ceiling. Their minting session is a revocation parent only.
  Idle or absolute session timeout cannot shorten the token's stored or reported
  expiry. Ancestry projects that session without an expiry only when its child
  has a validated member-token origin. Logout, admin revocation and membership
  removal or demotion still end authority through the same ancestry and epoch
  checks. Ordinary delegation retains its parent-expiry bounds.
- `provision_contract.go` declares the transaction-owned checks for trusted
  bootstrap, Actions origin and enrollment. Machine/service enrollment
  uses the existing capability rows and their persisted `CapabilityRecord.Origin`.
  Current team administration, canonical registration selectors, claim-only machine
  grants, deployment-only service grants, repository patterns, unique pools, active
  names and the existing machine cap are checked inside the issuing transaction.
  Team locks serialize machine enrollment. One advisory lock serializes service names.
  Rotation preserves registration and counts overlapping grace roots as one machine.
  The store rechecks authorizer scope, expiry, root shape and registered team.
  Only deployment token authority can set
  metering or authorize service issuance. Provisioning never grants console
  authority. Machine enrollment records the account authorizer in `created_by`,
  which also decides whether a claim is the requester's own machine.
- Claim selection checks every requested kind's grant before one ordered scan.
  Runner selection accepts trigger/node, launcher selection plan/eval/node. The
  named trigger path retains the exact here reservation's trigger.host exception. Unknown,
  duplicate, empty or unauthorized kinds are refused. A new award is delivered once.
  A lost claim reply recovers through the held lease, not a second automatic award.
- `ReconcileRequest.MaterializeOnly` preserves terminal/holder decisions and may
  materialize an authorized source. A source miss returns `Dispatch` without
  self-claim or bearer. `ReconciledNode.Bearer` is private delivery for a fresh
  execution award, never persisted or placed in public status.
- `ReadFailure` returns a recorded `nodeproto.JobFailure` under a declared recovery
  edge and the immutable source generation. It preserves stage, code, masked
  message and details. It cannot manufacture a failure from a terminal status.
  `NodeResult.Failure` supplies that already masked structure in the result
  transaction. It must name the reported job and a failed outcome. A recovery
  holder can read only the source declared by its own accepted `OnFailureOf`.
- Run-host cancellation is scoped to its own live owner lease. Pending work can
  end immediately. Held work receives only cancel_requested_at and one request
  event, then its holder drains. It grants no right to overwrite holder results.
- Attempt starts at zero. First execution start establishes one. A started
  disruption's successor starts the next ordinal. An unstarted release consumes
  none. Application retry advances under the same generation. Start/retry return
  the authoritative ordinal in Applied. Owner/eval logs always use unit subjects.

`StartNode`, `RetryNode`, `ReportNode` and `ReleaseNode` carry a
`SourceAttempt` field. It is a required nonnegative ordinal pointer for
holder start, retry, report and unstarted release. It names the persisted ordinal
before the edge. Initial start supplies an explicit zero. A resumed claim supplies
the last ordinal it retained through disruption. Start and retry return the new
ordinal in `Applied.Attempt`. An unstarted report or release allocates none.
Other actors and causes must omit the pointer.

The source ordinal participates in the transition digest. An exact replay of the
last committed transition returns its recorded result, including after retry has
advanced the ordinal. Every other holder execution transition must match the
locked row's current ordinal. Missing or negative ordinals return bad_request.
A mismatched ordinal loses holder rights. Delayed reports cannot close a newer
attempt under the same claim generation. W9 must add the corresponding optional
wire field and preserve its presence through the adapter. The store never derives
an externally reported source ordinal from current authentication state.

`Operator.ReapUnits` is the sole unit-expiry entry point. Every pass requires the
current `controller.reaper` lease and rechecks it in each unit transaction.
`Store.ReapExpiredNodeClaims`, `Maintenance.RecoverExpiredNodeClaims`,
`Maintenance.ReapExpiredTriggers` and `AgentLossRecovery` are removed. Recovery
returns tagged `ReapedUnit` results and never creates a retry run. A local owner
that has started fails in place after lease loss. An unstarted local owner
requeues without consuming a disruption. Terminal owner timestamps stay recorded.
The old dispatcher-expiry path and its application-retry accounting are removed.
Metered recovery uses the lifecycle settlement join. C2's production billing
calculator remains an integration point.
`Store.RequeueUnstartedClaim` and its Tenant twin are removed. Only the leased
pass recovers expired owners. Unstarted owner recovery preserves intake identity,
clears holder authority, consumes no disruption and fences every previous award.
C2's billing start rule still determines whether compute began at award or awaits
a qualifying pod renewal/start.

`Operator.MaintainConcurrency(ctx, lease)` runs under that same
`controller.reaper` fence. It locks capacity entries in tenant/key order and
removes dead holders and waiters in one transaction. Hosts retry `MayStart` to
promote complete group sets through the sole admission owner. Maintenance never
grants an individual group. The fence is checked before work and before commit. Any failure
returns zero counters and rolls back the whole pass. Expired waiter leases never
retain arrival order. Waiters bound to terminal or missing jobs cannot promote.
Live blocked waiters retain their arrival and lease across owner generations.
Queue precedence, grouped grants and renewal share claim-or-owner liveness. A job
generation still fences replacement executions. A reservation identifies durable
work, so supersession cancels its current job award rather than comparing the
reservation's original claim generation. No separate promotion lease or waiter-age
policy remains.

`Store.MaintainConcurrency`, the `Maintenance` function registry and its six
unfenced operations are removed. `OperatorStore` includes the leased pass. The
obsolete concurrency memo sweep, cache-count API and cache-cap fields are removed
because the final schema stores memo records through C1's blob-record contract.
Record expiry remains in `Operator.MaintainRetention`.
`Store.PurgeDeadLocalConcurrency` and `Store.CountDeadLocalConcurrency` are removed.
The elected pass cleans terminal work in every scope. Doctor no longer runs an
independent local-key repair or exposes its two repair counters.

Cancellation intent persists in `runs.cancel_reason` and `nodes.cancel_reason`,
separately from terminal result fields. The first request owns that intent.
`RunRow.CancelReason` and `NodeRow.CancelReason` retain it after draining and
reopening. Renewal carries it in `LeaseBeat.CancelReason` for job, owner and eval
units. A request-only edge never writes a holder's error, failure reason or exit
code. Run cancellation passes its reason to affected jobs without replacing an
earlier job request. `AuthenticationReader` transports the same intent through C2's
`BindingState.CancelReason`.

Session creation and lookup use the store clock and Unix nanoseconds. The session
row holds a SHA-256 digest. Every authentication reads current membership and
complete capability ancestry. Account sessions follow current role grants.
Capability-backed sessions also retain their parent's team, account and grant
ceiling. The controller preserves those verified grants instead of replacing them
with the full role. The removed `triggers.read` permission is absent from the role
map because C2's final grant registry excludes it.

`SessionKeyCipher` and the constructor's `Policies.SessionKeys` configure row-bound encryption
for `settings.session_csrf_key`. The row must be sealed and open under that cipher.
A missing key is minted once, atomically invalidating sessions signed by no
available key. Replicas open the same committed envelope. Wrong keys, malformed
envelopes and missing cipher configuration refuse session operations. The
controller's `StorePolicies` captures the bound cipher from `WithSecretsCipher`
during construction. Changing server configuration after `Open` leaves the
store's policy dependencies fixed.
No new key file, environment variable or encryption implementation is introduced.

`CreateCapabilitySession(ctx, prefix, ttl)` replaces `CreateTokenSession`.
It derives identity from an unbound account capability and retains that capability
as its revocation parent. `Session.CapabilityPrefix` replaces `TokenPrefix`.
`CreateAccountSession`, `CreateIdentityAccountSession`, `LookupSession` and
`LookupSessionAndRenew` no longer accept caller clocks. Their callers select
session identity and duration. The store supplies timestamps, digest persistence,
current grants and the committing transaction.

The SQLite session proof `credentials-session-digest-csrf/store-sqlite` checks
creation, digest authentication, encrypted key storage, identical CSRF after store
reopening, membership demotion and session revocation. It does not exercise OAuth,
a native browser, production C2 middleware or a network listener. The controller
component property separately exercises the registered session handler with
sixteen generated grant subsets and a fake origin verifier.

`IntakePolicyFacts.SourceLocation` is the server-verified acquisition location for
the selected repository. Intake replaces the proposed location with that fact.
A nonempty proposed location without this fact is refused before either run or
owner insertion. The policy checks credential-free remote identity, local-store
authority and the checkout-relative pipeline directory before supplying it.
Retry carries the original location with the original SHA.

`Store.DelegateCapability` and `Store.ProvisionCapability` wrap `Store.issue` in
the committing transaction. Ordinary delegation invokes C2's `ValidateDelegation`
on current parent authority and retains that parent. All issuance adapters return
the one-time bearer after commit and discard it on validation or storage errors.
`ExchangeCapability` uses that same commit boundary.

Controller claim adapters use the installed enrollment resolver and a
`RunnerAdmissionPolicy`. `SignedRunnerAdmission` delegates index verification and
runner eligibility to `internal/engine`. Request bodies supply requested kinds,
labels and observed platform. Persisted authority supplies identity, repositories and pools. Cancellation and renewal repeat their store
fences. Expansion replay returns the persisted `ExecutionJob` in each `Current`.

`Store.CheckBinding` and the transaction's `CheckBinding` reload current authority
before checking resource relations. Caller-supplied principal fields cannot widen
the persisted binding or grants. The checker covers run, job, owner, plan,
pipeline, child-generation, own-unit, declared dependency/recovery, source and
secret relations. C2 still owns route grants and liveness classes. Named claim
checks share the matching-input reader without taking write locks. The awarding
transaction evaluates observed labels and observed platform through the matcher.
Data selectors call the transaction's `DataAccessChecker`, supplied by the blob
owner. Receipt and read-edge changes in that transaction govern its verdict.
Concurrency binding resolves the accepted group and exact holder generation
from C2's target selectors. Node-host process cleanup remains the host's binding check.

Snapshot transactions omit row locks because PostgreSQL refuses them in a
read-only transaction. Authorization and owner snapshots use the same repeatable
snapshot for ancestry and resource relations. Write transactions retain ancestry,
run and resource locks through commit. The public binding check grants no right
to skip the committing transaction's authority and fence checks.

`Tenant.ListRegisteredAgents` reads only its team's enrolled claimants.
`Operator.ListWaitingNodes`, `Operator.SetNodeAttention` and
`Operator.CountConcurrencyCache` own deployment inspection and advisory writes.
Their raw `Store` methods are removed. The unused raw `SetRetriedAs`,
`ActiveConcurrencyHolder` and `ListSpawnedChildrenByRun` wrappers are removed.
Their tenant methods remain while legacy callers migrate.

`Tenant.ResetPipelineProfile`, `Tenant.ResetAllProfiles` and
`Tenant.ProfileSamples` own capacity resets and sample reads. Their raw Store
twins are removed. Local stats reset selects DefaultTeam explicitly. The local
dashboard selects the request's tenant before reading profiles or samples.
Query parameters cannot select another team's profiles.

The remaining raw Store profile methods are removed: RecordProfileObservation,
RecordWaitObservation, RecordContention, SetProfilePin, UpsertProfilePin,
GetPipelineProfile, ListPipelineProfiles and CacheExcludedCounts. Tenant owns
the complete profile API. Local orchestration, capacity stats and profile
diagnostics select the registered DefaultTeam explicitly.

Tenant admission owns complete-set grants and award permits. State and terminal
transitions own renewal and release. Tenant exposes scoped group and queue reads.
The raw Store twins and unused deployment-wide ListConcurrencyStates are removed.
The local adapter selects the registered DefaultTeam and propagates handle errors.
Permits capture the current award generation. Reservation identity and queue
arrival survive handoff. Queued arrivals persist their lease. Expired waiters
cannot keep arrival order or promote. Admission, renewal and maintenance use the
store clock and one claim-or-owner liveness horizon. Run queries carry the selected tenant key.

The SQLite recovery proof `recovery-concurrency-continuous-adoption/store-sqlite`
holds node and plan capacity through owner loss, remote claim renewal and store
reopening. It reclaims the owner through ClaimUnit and retains both reservations.
Job completion releases node capacity. Run completion releases plan capacity.
Each release allows one queued competitor through `MayStart`, and retries never
grant partial group sets.
Enrollment is a contract fake. Native process recovery remains separate evidence.

`Tenant.ReadOwnerSnapshot` reads the current owner authority, lease and scheduling
state in one snapshot. A replaced, expired or revoked owner receives no snapshot.
Cancellation remains readable while a held owner drains. The controller transports
the shared scheduling projection, exact expansion bodies and empty generators.
An uncancelled sealed guard refusal returns unavailable until the wire contract
can carry it. Dropping that refusal would permit refused work to execute.

Ordinary delegation retains its parent's immutable unit, purpose, delivery and
billing identity while narrowing authority. Persisting the child creates no new
billing award.

`Tenant.ReadRunView` returns a `RunView` containing the run snapshot, node-wide
attempt history and committed tagged log subjects from one read transaction.
Log subjects expose identity only, without object prefixes, segment keys or
signing authority. This reader does not write log persistence. The controller's
`RunDetail` adapter projects seals through `runspec.DisplayRequest` or
`DisplaySpec`, uses the destination row's retry lineage and omits private hashes,
bearers and execution declarations. `EngineController.VersionPolicy` supplies
read-time policy through the engine owner. `SignedEngineVersionPolicy` loads the
verified index, preserves fallback warnings, and evaluates the accepted engine
version. Active revocation/minimum errors become enforcement notices on stored
runs. Other policy failures discard the response instead of returning partial
status. A run without an accepted engine needs no version lookup.

`EngineController.Compatibility` looks up the exact release in verified metadata
and delegates calendar arithmetic to `controllerwire.CompatibilityFor`. Execution
eligibility does not change that release's API guarantee. Missing configuration,
failed index reads and unknown releases return errors. The engine index/evaluation
and wire calendar helper remain their owners' runtime integrations.

The controller calls `Operator.ReconcileControllerRuns` on startup and each
maintenance tick under `controller.owner`. It pages across all teams and renews
the exact fence between full pages. Election, renewal or reconciliation failure
stops the pass. Every Server has a random instance lease identity, separate from
the hostname displayed in schedule history. Another process on the same machine
cannot reuse that holder's live fence.

Final run pruning uses `Operator.MaintainRetention`. The unleased
`Store.PruneRunsOlderThan` API is removed. Finish time controls eligibility, and
a failed pass rolls back every removal and discards its result counters.

Identity expiry also uses `Operator.MaintainRetention`. The unleased
`Store.PruneSpentIdentity` and `Store.ExpireSessions` methods, `IdentityPrune` and
`SpentIdentityRetention` are removed. Invitations expire thirty days after
acceptance, withdrawal or expiry. Invitation and email-delivery timestamps use
nanoseconds. Each identity table observes the pass limit. Session expiry preserves
any row named by a capability parent. `RetentionResult.Invitations` and
`RetentionResult.Sessions` count only committed deletions. Controller maintenance
calls this fenced pass once instead of running a second identity sweep.

`Tenant.ReleaseSourceCredential` accepts C2's authorized authority reference and
route target. In one write transaction it reloads ancestry, locks the owner, run
and job, checks the live unstarted source binding and run deadline, and derives
the credential host from the sealed source location. It resolves executor
enrollment through `ClaimProfileResolver`. Repository permission requires the
award's repository scope and the current enrollment filter. A cloud executor
requires both the immutable cloud award class and enrolled cloud location.
Other executors require the same team's shared machine root (enrollment origin
not personal). **Superseded by decision 6c (2026-10-18):** owner consent no longer releases to a personal machine,
which is refused with `personal_machine`. Caller-supplied principal, binding and
executor fields grant nothing.

The transaction calls C2's `AuthorizeSourceRelease` through a private contract
dependency, reads the confirmed team credential, and commits the complete
`SourceReleaseAudit` before returning the sealed credential. The controller owns
decryption. Audit or commit failure returns neither credential nor audit. A final
clock check refuses a lease or authority that expires during policy work.
`audit_events` retains the run ID and generation with digests of the source
release's repository, node, machine and capability identifiers. The audit survives
without consulting mutable claims. Occurrence timestamps use store-clock
nanoseconds. The database assigns recording time for retention.

**Superseded by decision 6c (2026-10-18):** `Tenant.SetSourceMachineConsent` is removed. `Tenant.OwnerMachineClaimant`
reports whether an award's root is the run requester itself or a personal machine
the requester enrolled; the controller returns it as the claim's `owner_machine`
mark, and that host fetches with its owner's own Git access.
The production C2 policy, route/decryption adapter, trusted machine-owner
enrollment and GitHub App issuance path remain separate integrations.

Offer-round finalization and the warm-dispatch fallback are removed.
`Store.FinalizeClaimRound`, `ClaimRoundResult` and `WithQueueRunners` no longer
exist. The controller endpoint and client finalizer are removed with their warm
dispatcher caller. `ClaimUnit` uses shared matching and persisted queue order.
A soft preference never requires an offer window. A mismatch changes no ready
row and emits no event. The final ordered-queue SQLite acceptance proof covers
push scope, eligible ordering, soft preferences and a later matching claimant.

Legacy node readiness writers remain until their host callers migrate. They no
longer open or emit offer rounds. The old offer columns remain because legacy
claim and credit code still references them. Their removal is part of that cut.

Actions claims and awards check every push binding in the capability ancestry.
The sealed repository, ref and SHA must match. The workflow run identifies the
verified credential and cannot be replaced by a claim request. Named and queued
claims use the same check, and direct issuance repeats it in the award transaction.
Push authority never creates verified-branch trust. Production Actions evidence
and enrollment remain C2's integration.

### Upload and record persistence

`Tenant.ReadBlob(ctx, authority, digest)` supplies C2's live metadata read hook.
It rechecks tenant authority and the digest's read edge in one transaction,
then returns only untombstoned committed existence and size. An unreadable,
missing or tombstoned digest returns no metadata. Reading creates no receipt
or record edge. C2 still owns selecting the authenticated authority and wiring
this tenant method into DataController.ReadBlob.

`Tenant.TrustLocalBuilds` and `Tenant.SetTrustLocalBuilds` own the team's
local binary execution opt-in. Their raw Store methods are removed. An absent
row means false. Read errors or invalid stored flags cannot enable trust.
Enabling trust does not change upload or record provenance. The controller's
membership checks own who may read or change this policy. W7's
`blob.CheckBinaryExecution` and W3's execution adapter must enforce the policy
against the committed binary and digest.

`blob.Authority.Upload` contains C2's resolved tagged uploader. W1 rechecks it
against the exact job/attempt/generation, owner lease or imported terminal
published attempt. Attempt-scoped publication requires a persisted finish time
through the shared authority resolver for upload admission, ticket lookup,
commit, abort, data access and log commits.
`RecordLookup.VerifiedDefaultOnly` selects only the authorized repository's
verified default-branch entries before required bytes or member metadata are read.
An excluded record is a miss. The evidence retains the App event key, SHA and
matching verified ref/default branch. A bound reader's sealed default branch
must match when present. The default ref comes from persisted verified
facts. Setting the flag false cannot weaken DataRemoteCache authority. Returned
`RecordLookupResult.Members` includes committed path/size/mode metadata after
read-edge checks. Internal member reads confer no client read authority.

`blob_upload.go` defines upload tickets and verified evidence.
`upload_runtime.go` implements ReserveBlobUpload, LookupBlobUpload, CommitBlobUpload
and AbortBlobUpload using the existing uploads table. Reservation binds tenant,
uploader, digest, size, expiry, provenance and ref facts before signing. The
physical backend verifies size/digest before W1 commits existence and the exact
receipt with quota settlement. Commit repeats live authority and fence checks.
`VerifiedUploadCommit.ReplicaID` is trusted controller configuration, never a
client-selected field. The ticket ID also binds the persisted quota reservation.
It is one ASCII letter/digit/underscore/hyphen path segment selected by the
trusted adapter within its tenant/principal idempotency scope.
Existing CAS bytes grant no receipt. Failed settlement grants no edge. Abort or
leased retention releases uncommitted reservations without deleting a live blob.

`blob.ObjectBackend` owns signed physical transfers, verified immutable publication
and deletion. Both filesystem and S3 implementations consume stored bytes through
verified EOF and successful close. S3 fences its copy to the verified source ETag,
creates the final key only if absent, and verifies final bytes before returning.
Storage-root amendment: `NewS3(client, bucket, prefix)` requires the parsed URL
prefix. `S3.PhysicalKey` maps validated relative keys to that root once for every
signed transfer, read, copy source/destination, deletion, log listing and
lifecycle check. CloudFront downloads use the same mapping. Filesystem keys stay
relative to `FilesystemOptions.Directory` through its `os.Root` handle. Logical
record keys and persisted log references exclude the deployment prefix; records
remain database metadata. A configured root cannot be bypassed by an object class.

A physical commit grants no record, existence row or receipt. Those remain W1's
transactional responsibility after the controller repeats authorization.

`ObjectBackend.VerifyManifest(ctx, team, digest, members)` derives the tenant CAS
key and streams the manifest against the commit request's submitted members.
Decoder memory is bounded by the submitted path/digest metadata's worst-case JSON
representation; object byte length never sets an allocation size. Whitespace runs
outside strings consume constant memory. Unknown fields, surplus entries and
metadata disagreements fail integrity checks. No manifest byte-size cap is added.
Every original byte contributes to length and digest verification through EOF,
and successful close is required before admission.

`storage.BlobManifests` implements W1's `BlobManifestReader.VerifyBlobManifest`
with that physical verifier. The record transaction authorizes the manifest
before verification and independently authorizes each submitted member before
mutation. `ReadBlobManifest` remains the separate admitted-record lookup path;
it streams entries instead of buffering the raw object. These methods open no
store transaction or authenticated data request, so they run inside W1's existing
transaction. Controller binding supplies the adapter to `Tenant.BlobRecords`.

`ObjectBackend.DeleteBlob` implements W1's existing `BlobDeleter` interface for
filesystem and S3 collection. It derives the tenant CAS key from the selected
blob. Missing bytes are an idempotent success. The leased collector owns the
reachability recheck and tombstone grace, including references added after
marking. The controller's age-based output pruner and the bucket measurement
expiry callback are removed. The maintenance coordinator must bind the final
physical backend before collecting objects.

`blob.LogDeleter.DeleteLogs(ctx, team, run)` removes physical logs in the selected
tenant across stored retention classes. An empty run selects that tenant's logs.
Both common backends implement it and return the removed byte count. Invalid
selectors fail before I/O. Filesystem traversal follows no symlink. S3 deletion
checks each listed key against the selected prefix and rejects stalled pages.
Neither backend removes CAS objects through this operation. Run deletion and
the team's leased purge/recheck call it through the configured data backend
before removing rows. Listings establish no record or read authority.
This is a W7-local contract addition. Authenticated log bindings remain with W2.

`internal/teamblob` retains scoped key construction and team validation for
legacy S3 output reads. Its cache byte/multipart APIs, prefix deletion,
directory listing, measurement and write breaker are removed. Deployment
measurement remains separate from authoritative tenant accounting.

The standalone filesystem backend uses the controller URL as its signing base.
It signs the C2 data upload, object download and log-segment paths. Method,
exact object path, declared byte checks and expiry remain bound to the signed
token. An external URL prefix belongs to the proxy, while verification uses
the registered controller route.
`Filesystem.VerifyTransfer` supplies C2's external endpoint proof callback.
The common controller mounts these signed transfers without bearer middleware.
Authenticated binding and new local-controller construction remain separate.
Legacy filesystem output and raw log paths have been removed.
The sharded filesystem ArtifactStore, generic raw storage.ArtifactStore contract
and portable conformance package are also removed. Filesystem deployment
measurement uses fs.NewUsageReporter. It exposes totals without an object-read
or write API. S3's remaining raw log reader still needs its authenticated
cutover. Physical CAS reads and writes stay with the common object backend.

`CommitObject` requires verified bytes at the exact ticket's staging key even
when the final CAS object already exists. An absent staging key returns
`ErrNotFound`; a digest and another uploader's final object cannot establish an
own-upload receipt. Retries before settlement retain staging. After settlement,
the authenticated adapter replays the persisted receipt under current authority
without repeating physical publication. Staging cleanup follows settlement.

The `storage-cas-integrity/store-data-sqlite` proof refuses mismatched verified
size, digest and tenant evidence. It reopens an expired interrupted upload,
reserves a replacement under the same capacity, rolls back a receipt failure,
then commits and replays the replacement. After reopening again, only the new
uploader can read the digest and usage counts one blob. Authority and physical
verification are contract fixtures. Native transfer integrity and EOF checks
remain separate integration proofs.

`TestBlobOwnerReceiptCannotTransferToAJob` and
`TestBlobUploadOwnerTicketsRetainUnitIdentity` check tagged trigger/plan uploads
before any job exists, reopening and refusal to transfer an owner's receipt or
ticket to a job.
`TestBlobUploadCommitRechecksTicketAndAuthority` checks failed verification.
Generated eval tests check both source and unit fences under explicit fake write
authority. C2's issued eval grant table remains read-only. The native cell still
requires signed transfer and stream EOF verification.

Upload tickets reserve monthly bytes, lifetime run bytes and lifetime run objects
in the existing quota tables. Each dimension has its own durable reservation.
The aggregate keys are `storage`, `storage.run.bytes:<run>` and
`storage.run.objects:<run>`. All paths acquire them in that order. Run dimensions
have no calendar reset. Commit charges only newly inserted tenant blobs, with
one object even when its size is zero. Authorized existing bytes reserve zero
in every dimension. Abort and leased expiry release all three holds, including
holds whose upload row was removed by a run cascade. Effective default-tier
policy is read for every new upload without creating an override row.

Logical record pointers add no storage usage. Output and memo records may point
to the same authorized blob at the run's object ceiling. A new blob still needs
an object reservation. The legacy `SetNodeArtifactManifestCharged` methods are
removed with the legacy manifest-pointer endpoint. W7/W9 wire final publication
and member validation through the record and result contracts. No manifest
column or mutable pointer route is restored to the final schema.

CommitRecord and CopyMemo use one transaction-local record mutation owner.
They prepare every namespace and verified manifest before taking the sorted
blob locks. After checking existence, reference edges and immutable destinations,
the owner rechecks writer ancestry and holder authority against the store clock.
That check is the publication boundary. The owner writes the complete destination
batch and tombstone changes under the same transaction. Its caller owns commit
and rollback. An expired writer cannot publish even when it was live before
namespace or manifest preparation. Immutable replay and atomic output/manifest
copies retain their existing contracts.

Record commits normalize expiry instants to UTC and clear values in unset optional
expiry and manifest fields. Returned metadata matches persisted metadata across
replay and reopening. A different expiry instant still conflicts with an immutable
output or manifest record.

`TestBlobUploadCrashReplay` exercises admission, commit, abort and deduplication
through the store's shared upload fixture. Child processes exit without closing
the database before and after commit and abort. The parent reopens the database
and checks persisted tickets, quota holds, usage and replay capacity. Physical
verification evidence is a contract fake. Native controller checkpoints, signing
and transfer integrity remain integration obligations.

The `TestReplica` tests use `internal/store/internal/storetest` targets to check usage
sums, quota races, reservation release, singleton election and download caps
across independent clients and reopening. Quota setup uses ReserveQuota and
SettleQuota. The suite's dialect selection applies to these tests.

LookupRecord, LookupMemo, LookupDepcache, CommitRecord and CopyMemo remain the
only record owners. W1 integrates receipt/read-set checks, members, unique-byte
accounting, newest-success pins and reachability retention. Artifacts require
read authority. The cloud-provenance execution restriction applies to binaries.
Claim/start/reap/finish transactions invoke the shared billing owner with immutable
award facts. Plan/eval have no minimum, job and host minima remain separate, and
customer-runner waiting is excluded. Verified payment events settle committed
checkouts idempotently. No caller-selected team or credit amount reaches settlement.

The unit lifecycle joins W1b's billing helpers in the claim, start, renewal,
release, plan acceptance, expansion acceptance, finish and reaper transactions.
Pricing comes from the selected fixed Cloud size label and its exact credit class. The existing
minute reservation is refundable. C2's calculator applies the separate final
minimums, including zero for plan/eval. A here intake capability authorizes a
future claim and opens no compute reservation at generation zero.

Queued matching checks funding before ranking eligible work and repeats the
reservation under the ledger lock when awarding. An unfunded team cannot block
funded matches. Renewal starts Cloud job, plan and eval billing regardless of
reported activity, without consuming a job execution attempt. Customer-runner
owner Executing reports current compute: false closes an interval and true
resumes it with a fresh key. Application retries keep their reservation
and open another interval. Reaping settles at the persisted lease lapse.
The eval lease and billing generation come from persisted expansion authority.
C2's production calculator and W3's current-activity reporting remain integration
dependencies. The SQLite billing-start proof covers issuance, exchange, starts,
immutable pricing and reopening. Lifecycle tests substitute C2's calculator with
a contract fake. Awarding repeats the claim and eval-owner deadline checks after
accounting. Expiry rolls back the reservation and unit binding together.



This document specifies the rewrite target. Functions marked `unimplemented`
are signatures for the implementation swarm, not working runtime behavior.
The authority is the frozen architecture v4, especially sections 3, 4, 7.5 and
8, with decisions 87-104. Earlier design chapters supply evidence only where
they agree with that target.

## Ownership and package boundaries

C1 owns the final neutral schema, persisted run and job state, transition
rules, both run-input seals, attempt and repository identity, blob records and
reference authority, concurrency continuity, completion revisions and retention.
Internal Go and SQL identifiers use node; customer JSON uses job.

- `internal/store/schema.sql` is the only baseline. The private `translateSchema`
  maps neutral types for each dialect. Neutral types are TEXT, BIGINT, DOUBLE PRECISION and BLOB. PostgreSQL maps
  BLOB to BYTEA without changing checks, keys or predicates.
- `internal/store` owns transactional enforcement. `Tenant` carries an explicit,
  registered team and cannot widen itself to the raw store. `OperatorStore`
  exposes named deployment operations. `Store` keeps opening, handle creation,
  capability-derived claims and cross-team reaping.
- `internal/nodehost` defines the host-facing ports. `OwnerStore` and
  `ClaimStore` are implemented by `*store.Tenant`; `NativeHostStore` combines
  them. Hosts borrow these tenant handles and never close the shared store.
- `internal/runspec` owns immutable inputs and identity. It resolves no settings,
  authenticates no credential and validates no plan.
- `internal/blob` owns byte integrity and reference-edge types. The controller
  supplies its signed data transport; `Tenant` supplies transactional records.
- C2 owns capability issuance, route authentication, binding and API errors.
  C3 owns `ValidatePlan` and host interfaces. C1 owns `RunSnapshot.Scheduling`
  and the shared pure `Decide` in `internal/store`. Run hosts and controller
  settlement use that same projection and decision before transition writes. C3's host expansion interface
  calls `Tenant.AcceptExpansion`; it is not a second persistence owner.
- C4 owns setting resolution and numeric limits. C1's retention table records
  the frozen policy, and implementations obtain timing and size values from
  C4's limits package. T1 owns cross-placement acceptance execution.

## State and transaction invariants

1. Intake creates the run and its executable owner unit together, except for
   publish intake, which has no executable owner. Run numbers allocate under
   the team lock and are unique per team; IDs remain globally unique.
   Verified App redelivery returns the original run for that delivery and
   pipeline, even without a client idempotency key. The unique
   `(team, webhook_replay_key)` index enforces this identity. The provider's
   unsigned delivery header is audit data and has no uniqueness constraint.
2. Intake seals the requested inputs. Acceptance validates the C3 plan,
   resolves C4 settings and ceilings, performs server-side secret preflight,
   seals the resolved spec and inserts nodes in one transaction. Different
   accepted hashes fail as `spec_drift`. Projections are written only in
   `run_spec.go`. Guard refusal may end a run before a plan is accepted.
3. Every node insertion uses `insertNodeTx` through plan or expansion acceptance
   and starts with `state_seq=0`. Only `node_state.go` and `run_state.go` own
   status changes. An absent edge is illegal; a zero-row write is not success.
4. `nodeState` derives ready and claimed from persisted status and claim fields.
   Lease expiry does not itself turn a row pending. The reaper commits that
   edge using the store clock. Paused is status detail, not a state.
5. Holder rights require the exact live capability binding, holder and generation.
   A trigger owner cannot overwrite another live holder. Generation never resets.
   Identical actor, generation and payload digest is a replay with no new event.
   Terminal is otherwise absorbing. State actor and digest survive claim clearing.
6. Attempt identity is `(team,run,job,attempt)`. Attempt is a positive node-wide
   ordinal and never resets across claims; generation is an attribute.
   Application retry closes the failed attempt and advances the ordinal under
   the same claim. Requeue consumes no application retry. Unstarted lapse is
   free; started lapse records `agent_lost`, increments disruptions and fails
   when the increment reaches three. OOM is an ordinary job failure.
7. Every pending or terminal transition clears claim columns. A cancellation
   of claimed/running work sets `cancel_requested_at` and emits
   `job_cancel_requested`; it does not emit a terminal event. `nodeEvents`
   selects by requested target plus `RequestOnly`. Cancellation wins when a
   lapsed claim is reaped. Claimed/running work drains before the run finishes.
8. State, attempt closure, output references, manifest authority, lifecycle
   event, concurrency release, expansion barrier and controller settlement
   commit together. The store owns lifecycle events and the per-run event
   sequence. Hosts never emit a second lifecycle event.
9. Reconciliation decides and self-claims atomically. `WaitHolder` follows
   events until terminal or requeued, then reconciles again. Resume, retry,
   memo and coalesced source policies keep distinct read authority. The only API
   is `ReconcileNode(ReconcileRequest) (ReconciledNode, error)`. Materialization
   reads the source outcome and result edges from persisted state. A request
   cannot supply an outcome.
10. Successful and memoized generator sources remain `expected` until expansion
    submission. Failed, skipped, cancelled or superseded sources resolve to
    `none` in the terminal transaction. Run cancellation resolves all expected
    expansions. Generator errors resolve to `failed`. Expected expansions
    prevent settlement and release of `needs_generated` dependents.
11. Expansion uses a live owner delegation or separately leased eval award,
    never the ended source claim. It checks source generation, expected state,
    cancellation and both applicable leases in the transaction. Exactly one success
    body or structured failure is required. An empty success contains an explicit
    empty jobs array. Both forms persist a canonical hash and replay unchanged.
    A changed body or failure conflicts. Persisted generated specs and hashes control
    topology. Generator replay reconstructs code only and must match those hashes.
    `PlanAcceptance.Seal.Declarations` carries restricted pipeline/default inputs.
    `settings.ResolvePlan` and `settings.ResolveExpansion` seal execution settings
    separately in `NodeRow.Execution` and `nodes.execution_json`. Hosts execute
    those winners and compare declarative hashes only for reconstruction.
12. Owner loss preserves remote work in the same run. Controller-owned runs
    use `controller.owner` after plan acceptance. Local started orphans fail.
    Controller ownership lapse does not fail its runs; the next elected replica
    resumes them, subject to their existing limits.
13. A concurrency holder's own lease proves continuously counted capacity.
    Node holders renew while their node is nonterminal and its claim or owner
    remains live; plan holders renew while their run is nonterminal and its
    owner or any node claim remains live. Expired capacity cannot revive.
    A still-live waiter keeps arrival order; an expired waiter is a new arrival.
14. Acquire transaction locks in the frozen class order and lexical order within a class.
    Implementers must review claim, cancellation and financial paths together
    before adding a lock; they must not silently acquire an earlier class late.
    Authority mutations use `acquireLockSet` to discover every team, membership,
    authority and installation row before acquiring any of them. The set includes
    teams referenced by authority and installation rows. The owner acquires
    each row once in class order, then repeats discovery under those locks. A
    changed set restarts the transaction and releases its locks before retrying.
    Nested authority checks reuse held locks. A late earlier-class lock or lock
    upgrade fails rather than extending an incomplete set.

## Scheduling, placement and log contracts

`ReadSnapshot` reads the run, ordered nodes and accepted expansion bytes in one
transaction. `RunSnapshot.Scheduling` uses the persisted plan and job specs for
edges, `ClosureSpec.Generate` for generator identities, and accepted generated
`JobSpec.Groups` for membership. It keeps an entry for an empty accepted group.
States and outcomes come from persisted rows. Invalid declaration/state mixtures
fail projection. Both owners pass this result to the same pure `Decide`.

C1 owns placement input construction. `matchingInputsTx` derives `match.Profile`
identity and `ReservedPools` from the live capability and its enrollment facts.
It derives `match.Demand.Selector` and `Prefers` from resolved execution
settings, and `AllowedPools` from the shared reservation predicate, which reads
the current access rules against the run's sealed provenance and the unit's
resolved requirements. Every named, queued and self claim
checks the shared SQL predicate with those inputs. `UnitDemand.Target` selects a named unit;
absence selects the ordered eligible queue. Reservations require an explicitly
required granted pool that a current access rule allows. A self-asserted `pool=` label grants
nothing. Preference holds apply only between eligible claimants. The shared SQL
query orders jobs by team award ordinal, arrival and identity. It does not score
queued jobs by preferences or match CPU and memory observations.

`ClaimProfileResolver` supplies enrolled repository acceptance and location,
plus the host observations needed by a self-claim. The store replaces its name,
class and reserved pools with authenticated capability facts. Self-claims
require this resolver because `ReconcileRequest` carries no machine observations.
Named and queued claims carry their observed labels and platform in
`UnitDemand`.

Repository matching checks every capability ancestor. Canonical repository keys
match exactly. Machine host/path patterns match the sealed repository display
through the shared repository parser. Omitting a delegated repository list cannot
remove an ancestor's restriction. Named and queued claims use the same check.

Advisory runner discovery reads live unbound machine capabilities and rechecks
their ancestry and current claim grants. `RecordAgentLabels` persists only
self-asserted labels and a nanosecond last-use time. `RegisteredAgent.CapabilityPrefix`
identifies that capability. Its profile combines enrollment with the same granted
identity and pools used for claims. Online presence replaces observed labels and
platform only. `ListWaitingNodes` pages ready host-owned jobs from their sealed
execution demand and request runner selection. `WaitingNode.AllowedPools` carries
the pools current access rules grant into the advisory matcher. Advisory results grant no
authority to claim or mutate a job.

`ReconciledNode.Bearer` carries the new self-claim credential after commit and
is excluded from JSON. `nodes.owner_capability_prefix` and the matching
`NodeRow.OwnerCapabilityPrefix` identify which live trigger award may renew
a self-claimed job. Dispatched claims leave this field empty. Claim clearing
also clears this field. This fence is separate from capability ancestry because
C2 awards parent to the claimant root.

`MaterializeSource.Result` names a committed terminal result by job and state
sequence. It is exclusive with the positive execution-attempt identity in
`Source`. This lets a later retry reuse a previously materialized result without
inventing an execution attempt. Both variants read the outcome from persisted
state and require the same policy-specific record authority.

`RunRow.CreatedAt` is the new invocation's creation time. Queue deadlines use
it because retries preserve the original request seal's admission time.
`nodes.queue_started_at` and `NodeRow.QueueStartedAt` retain a job's first
release time across unstarted claim losses and releases. Execution start or
terminal completion clears it. A started disruption therefore receives a new
queue interval on its next release. An unclaimed pending job with an elapsed
queue interval can fail as `queue_timeout` before its owner releases it again.
The accepted spec supplies claim wait after seal point two. Before acceptance,
the request seal supplies it. Named, queued and self claims enforce that same
deadline before awarding work, even when reaping has not run yet.
`ScheduleSnapshot.GuardRefusal` carries a sealed guard refusal to the shared
decider. Refused jobs become skipped and the run fails with the recorded reason.
Cancellation still takes precedence. A refused seal cannot release or claim work.

`Operator.RenewUnit` is the controller adapter for a unit capability and the wire's
`executing` flag. It derives team, unit, holder and generations from the live
capability. Trigger and plan renewal share `Tenant.RenewOwner`'s transaction
path. Job renewal shares `Tenant.RenewNodeClaim`'s path. Eval renewal checks its
own claim, terminal source generation and elected controller generation.
Bootstrap credentials cannot renew. The first executing renewal records an
owner unit's start, which distinguishes free loss from a started disruption.
Job starts still use `TransitionNode`. Cancelled units renew for draining,
return cancellation, and record no new execution start. Billing settlement
must join the renewal transaction through the accounting implementation.

Owner requeue clears the claim and appends one `run_requeued` event in the
same transaction. It advances the run state sequence without changing its
pending or running status. Repeated sweeps cannot duplicate that event.

`Operator.ReconcileControllerRuns` resumes accepted controller-owned runs
under the exact live `controller.owner` lease. It visits a bounded page across
teams, ordered by global run ID. Callers pass the last visited ID to the next
page and start the next sweep with an empty cursor. Each run settles in its
own transaction. Errors return the committed prefix. The sweep renews only
continuously live concurrency holders and waiters to the persisted owner
lease expiry. It cannot revive expired capacity.

Controller ownership begins at a positive `plan_accepted_generation`, after
the current invocation's planner accepts its plan. A retry carries its source
spec before that handoff, so a spec alone cannot establish controller ownership.
Read projections, owner authorization, settlement and owner reaping use the
accepted generation. A lost retry planner still requeues or fails at its
disruption limit. The carried request, spec and plan remain unchanged.

Published plans, job results and completion share one publisher check:
controller audience, `data.write`, a run binding, publish data scope, matching
repository and live ancestry. Completion requires an accepted plan and a
verdict that agrees with persisted jobs. Published history has no executable
owner, including when its dispatch field names the controller. Result-record
attachment remains transactional and must supply its own blob authority proof.

Materialization uses the store-local `materializationRecords` interface inside
the state transaction. Its implementation must authorize the policy-specific
source and copy verified output and manifest edges atomically. Memo authority
comes from the retained record, so pruning its source run does not remove a hit.
Retry and coalesced outcomes come from persisted terminal job and attempt rows.
The unconfigured path refuses materialization. Tests may fake the record boundary,
but those tests do not establish blob persistence or byte-integrity coverage.
The matching `nodeResultRecords` interface attaches reported or published output
and manifest records in the terminal transition transaction. It must authorize
the exact persisted attempt or publication binding. A refused attachment rolls
back state, attempt closure and events. An exact result replay does not reattach
records.

`NodeRow.OutputRecord` and `NodeRow.ManifestRecord` expose the selectors
committed with the terminal result. `nodes.result_records` stores that pair in
the same transaction as state, attempts and events. Reported, published and
materialized results share this projection. Jobs materialized without execution
retain attempt zero. Readers never infer result keys from an execution attempt
or depend on retained event payloads. Selectors grant no data access. W7 readers
still authorize the selected record and verify its bytes. W3/W9 must carry these
fields through their execution projections.

`Policies.ResultBlobs` configures the blob implementation of
`nodeResultRecords` before the store is shared. Attachment verifies exact
destination result keys and all required bytes in the state transaction through
the existing record reader. It requires `data.write`, rechecks live authority
after manifest I/O, and preserves caller-owned commit and rollback. Missing,
expired or corrupt records prevent the result and its event from committing.
No synthetic attempt represents an unexecuted job or a published import.

`IntakePolicy` resolves request options through C4 and supplies server ceilings,
repository authority, verified App delivery facts and log retention. The store
requires it before admission and rechecks the caller in the intake transaction.
It does not accept client origin labels as repository or branch authority.

`blob.LogSubject` is the shared identity for signing, commits, live pushes and
reads. It contains either a positive job attempt identity or a trigger/plan/eval
unit. Trigger and plan use the run and unit generation. Eval also names its
source job and immutable source generation, separate from its own lease fence.
`Validate` rejects mixed or missing variants. No synthetic jobs or zero attempts
represent pre-job logs.

`log_streams` persists that tagged identity, its generation, retention class and
final commit digest. The transaction that starts an attempt or claims an owner
unit also creates its stream. Publication imports create their streams in the same transaction.
`RecordSegments` appends and seals existing streams, refusing a missing registration.
`log_segments` references the stream with the same generation. The writer rechecks
its exact capability binding and persisted generation.
An unsealed final job attempt may flush after its result commits until the
persisted finish time plus `limits.LogSealGrace`. C1 rechecks the recorded writer,
attempt, generation, current credential ancestry/expiry/membership and absence
of a newer claim. After sealing, only an identical final commit may replay.
Owner-unit liveness stays separate; eval gains no job-attempt grace exception.
Published job logs use live run-bound `data.write` publisher authority and the
exact imported terminal attempt in a publish-mode run. Publication has no
execution lease, so execution tail grace does not govern its uploads. Normal
segment integrity, retention and seal rules still apply. Owner-unit publication
needs an imported unit identity contract before it can bypass a live unit lease.
`LogTransport.SignSegments` derives object keys and retention from the authorized
subject and run. Each segment URL includes its required `Headers`; the final
manifest uses `ManifestURL` and `ManifestHeaders`. Storage transports must send
those headers, including S3's signed create-only condition. HTTP 412 proves only
that the key exists and cannot authorize a commit or an identical replay.
`LogTransport.VerifyUpload` recovers an uncertain PUT by reauthorizing the exact
writer and verifying the stored bytes. `LogUploadCheck` carries the subject,
index, stored size and SHA-256. Index zero selects the final manifest. Positive
indexes select segments. The controller derives the key from the subject and
the run's persisted retention class. `logseg.UploadVerifier.Verify` consumes and
closes that object before confirming its exact size and digest. Verification
returns no bytes or read URL and creates no record edge. The subsequent
`RecordSegments` transaction repeats writer authorization. Cancellation remains
an error. A changed payload, missing object or failed close cannot recover.
`ReadLogSegments` and `LogTransport.ReadLog` reauthorize the same
subject, proxy bytes through the controller, meter egress and apply the log
exception to download caps. Persisted segments fill gaps or stalls in the ring.

`blob.LogObjectKey(subject, class, index)` owns the physical key for every signed
log slot. Index zero names the final manifest. Physical segment slots start at
one in signing and verification.
`LogSubjectPrefix`, `LogSegmentKey` and `LogManifestKey` are its shared key builders.
The backend-relative layout is `logs/<class>/teams/<team>/runs/<run>/`, followed
by the tagged job or unit fence. Persisted references use this relative key; the
backend applies the configured storage root when locating the physical object.

The `storage-hasmany-read-set/store-data-sqlite` proof uses exact imported job
attempts under a preseeded publication capability. It checks hidden unrelated and
foreign existence, budget expiry, reservation and failed-commit refusal, independent
verified receipts, deduplicated replica charging and persistence after reopening.
Both `BindData` and `BindReadSet` dispatch through `Store.CheckBinding` to the
transactional data checker in this SQLite proof. Generated store tests also
check uncommitted receipt changes and caller-supplied principal forgeries.
Presence metadata consumes neither download nor egress bytes. Physical verification,
job-holder transports and native placements remain integration obligations.

The `storage-hasmany-large-budget/store-data-sqlite` proof checks 1,025 distinct
digests and their duplicates across reopening. All 257 readable digests appear
once in request order. Unrelated and foreign bytes remain hidden. Presence reads
leave upload, download and egress accounting unchanged. Generated store tests
advance the injected clock across the final read and require an empty budget
error once the deadline is reached. Trigger/plan owner receipt properties also
check forged identities and refusal to transfer the receipt to a job in the same
run. Physical stream verification and native placements remain separate proofs.

Record commits and lookups recheck live authority after verified manifest I/O.
Generated tests cross the job lease, leaf capability and ancestor expiry during
that read through CommitRecord, LookupRecord, LookupMemo, LookupDepcache and
CopyMemo. Refusals return no read set and persist no output or manifest record.

The `TestLogPersistence` tests own immutable segments, gapped-batch rollback,
offset reads, incomplete seals, replay, retained-byte accounting, subject
isolation, revocation and abandoned-stream maintenance. Job, publication and
maintenance-seal metadata survive reopening. Publication adds no operational or
financial units. A later node finish cannot extend an attempt's read grace.
Generated tests check reads, segment commits, final seals and maintenance before,
at and after the grace boundary. Historical reads and stored bytes survive
sealing. Log loss leaves the successful job result unchanged. Claim authority
and segment digests are contract fixtures. Physical bytes, owner-unit transport,
ring fallback, signing, lifecycle expiry and proxy egress remain integration
obligations.

`Operator.SealAbandonedLogStreams(ctx, maintenanceLease, limit)` closes existing
unsealed execution streams with `writer_lost`. Job attempts wait until strictly
after their persisted finish plus `limits.LogSealGrace`. Trigger and plan streams
need no matching live unit generation. Eval checks its own generation and its
immutable source generation separately. The operation locks each run, rechecks
eligibility and the maintenance fence, and commits a bounded batch atomically.
It preserves segments, stored-byte accounting, final seals and execution results.
Publish-mode streams retain their publisher's final-commit contract because
publication has no executable lease. The controller invokes this operation from
its leased retention pass. The old `Store.AbandonedNodeLogs` query and unfenced
controller sweep are removed. Physical manifests and ring fallback remain with
the log transport.

Persisted reads recheck a trigger's current owner lease even when its award has
`runs.read`. Job-scoped readers retain their exact writer, attempt and generation
fence and cannot reuse sealed-write replay to read past `limits.LogSealGrace`.
Unbound run/log readers can still read historical subjects. Identical final
write replay keeps its existing ancestry checks and separate replay exception.

The `storage-reference-edge-laundering/store-data-sqlite` proof reopens a seeded
output/manifest/member graph and checks presence plus download/reference
authorization. Direct, optional, transitive, artifact-input, generated-group and
failure-handler dependencies grant edges. Unrelated, foreign-team and missing
digests stay hidden. These metadata checks consume no egress or download budget.
Accepted-plan intake, manifest byte verification, record commits and native
routes remain separate integration obligations.

The `storage-publish-one-way/store-data-sqlite` proof commits published job logs
after local completion, reopens their segments and incomplete seal, and checks
retained bytes, immutable replay, credential revocation and unchanged job results.
It creates no executable unit or billable interval. Publisher authority and
terminal imported attempts are preseeded. Run/result import, owner-unit logs,
local publication transport and physical bytes remain integration obligations.

| Export | Purpose |
|---|---|
| `AcceptedExpansionRow` | Keeps accepted expansion bytes, private secret registration, source fence, body hash and combined acceptance digest in a consistent snapshot. |
| `RunSnapshot.Scheduling` | Projects declarations and persisted state into the scheduling graph. |
| `ScheduleNode`, `ScheduleExpansion`, `ScheduleSnapshot` | Carry the accepted graph, including empty generator groups. |
| `DecisionKind`, `NodeDecision`, `ScheduleDecision`, `Decide` | Define one pure graph decision consumed by both owners. |
| `UnitRef`, `UnitDemand` | Select named or queued work with observed platform and labels. |
| `match.Profile`, `match.Demand`, `match.Evaluate` | Match granted reservations against sealed requirements and authorized pools. |
| `blob.LogSubjectKind`, `blob.LogUnitKind` | Select attempt versus unit logs and the persisted owner-unit kind. |
| `blob.LogSubject`, `blob.LogUnit`, `blob.LogSource`, `blob.LogSubject.Validate` | Carry and validate one fenced log identity. |
| `blob.LogSigningRequest`, `blob.LogUploadURL`, `blob.LogUploadBatch` | Request and return bounded segment PUT slots for that identity. |
| `blob.LogUploadCheck`, `blob.LogTransport.VerifyUpload` | Verify the exact stored payload after an uncertain PUT under current writer authority. |
| `logseg.UploadVerifier` | Derive one authorized object's key and check its stored size and digest after successful close. |
| `blob.LogReadRequest`, `blob.LogTransport` | Sign uploads, verify uncertain writes and proxy authenticated reads through the controller. |
| `SegmentCommit`, `runstate.LogSegments`, `Tenant.ReadLogSegments` | Commit and read persisted segments under the same subject fence. |
| `Operator.SealAbandonedLogStreams` | Seal abandoned execution streams under the maintenance lease without changing execution results. |

## Capability persistence, authority and payment settlement

`Actor.Authority`, `IntakeRequest.Authority` and the sealed intake authority use
C2's tagged `AuthorityRef`. A session remains a session through transaction
rechecks. `CapabilityPrefix` remains a separate claim fence. `storeTx` implements
C2's `AuthorityResolver`, so current ancestry, membership and grants are checked
inside the write transaction.

`Store.ResolveAuthority` and `Store.LookupCapability` expose consistent ancestry
and membership reads to controller adapters. Lookup accepts a bearer digest.
These methods do not authorize a route or establish a unit's live binding.
`Store.ExchangeCapability` owns the bootstrap exchange transaction and returns
the replacement bearer only after commit.

`CapabilityRecord` stores version 1 and the full C2 capability in `authority_json`.
The bearer digest stays in its indexed column and no raw bearer is stored. The
envelope preserves attempt, deadlines, reservation, Actions workflow identity,
expansion lease, UnitID, delivery purpose, data scope and repository scopes.
Indexed columns are exact projections, validated on load and written with the
envelope. They cannot reconstruct authority from newer mutable rows. Tagged
`parent_kind` and `parent_id` support capability and session ancestry. C2 validates
the referenced store and parent liveness transactionally. Retention and revocation
must preserve that ancestry check, including descendants of here sessions.

`capability.UnitKind` owns operational values `trigger`, `plan`, `eval`, `node`.
Grant purpose maps work to node and here to trigger. Bootstrap is a delivery
purpose that retains the underlying plan/eval/node unit. The schema stores purpose,
delivery and unit separately. `billing_unit` stores `host`, `plan`, `eval`, `job`
with checks for trigger-to-host and node-to-job. Both ledger tables preserve the
operational kind and financial classification after run or capability pruning.

Authentication reads the complete capability/session ancestry and current
membership in one snapshot on every request. Revocation, role changes and
membership epochs take effect at use time. Authentication facts are not cached.

The first authentication of each `(session, team)` records an immutable membership
epoch in `session_membership_epochs`. Selecting a team cannot replace that epoch.
Session-derived credentials, including delegated data credentials, retain their
minting team's context through every ancestor. When ancestry reaches a session,
it resolves that session in the child's team and checks that team's recorded
epoch against current membership. The session's active team still controls direct
session use. Switching it cannot change an existing descendant's team or grants.
Removal and demotion advance the team's membership epoch in the membership
transaction. Rejoining or promotion cannot revive descendants of an old session.
Session timeout validation preserves the session row because live member tokens
still depend on its explicit revocation state. Retention keeps referenced sessions.

`Store.SettlePayment` accepts C2's opaque verified event and looks up the committed
checkout by provider session ID and configured test/live mode. The checkout supplies
the team and credit amount. Verified amount, currency and payment identity must
match. An unset payment ID binds once from the signed event for that checkout.
`payment_events` deduplicates mode/event ID and its canonical payload. The locked
checkout records at most one credit grant, even when multiple paid event IDs arrive.
Failures and expiry cannot undo paid credit. Financial rows have no run cascade.
Human grants, refunds and freezes remain console-only operations.

`payment_settlement.go` owns settlement and receipt deduplication. The
`settings.payment_mode` value is a required JSON string, `"test"` or `"live"`.

Payment lookups and console refunds follow the checkout's persisted `grant_id`.
The grant's ledger reference is not a provider payment ID. Refunds retain that
ledger reference in `reverses`, count prior reversals and cap the total at the
paid grant. Checkout lookups select the configured test/live mode. Distinct card
charge grants retain their provider payment reference. Ambiguous payment/grant
associations fail instead of choosing a team. Caller-directed checkout closure
is removed. Verified failure and expiry events own those outcomes.
`CreditGrantRequest` carries no checkout session. Only verified payment
settlement writes the checkout's paid timestamp and grant association.

`Operator` owns deployment card collection and refund workers: `DueCardCharges`,
`RecordAttemptIntent`, `SettleCardPayment`,
`DueCardRefunds`, `MarkCardRefundMade` and `FailCardAttempt`.
Their raw Store methods are removed. Provider workers
select Operator before operating on persisted attempt and payment identities.
Tenant spend policy and recovery checkout keep their tenant handles.
Raw card setup, warning and provider refund callback APIs are removed. Their
transaction-local owners remain for verified payment-event settlement.
The collection worker and webhook settlement retain their ledger lock,
persisted payer attribution, replay rules and refund queue. This handle cut
adds no provider verification contract. C2 still owns verified event delivery.

Warning persistence and its payer hold share one transaction-local mutation owner.
A warning ID retains its first payment. A conflicting replay fails before
applying a hold. Payer lookup compares every matching paid grant and card
attempt. Conflicting tenant evidence rolls back the warning and hold. A warning
holds only the team its payment funded.
Holds use the tenant-scoped warning identity. The legacy provider-only hold
identity is removed. Replaying an exact warning after release cannot restore
its hold. A failed audit rolls back both warning persistence and the hold.

`Operator.ReversePayment`, `Operator.PaymentTeam`, `Tenant.SaveCard`,
`Tenant.HoldForDispute`, `Operator.RecordPaymentWarning` and
`Operator.ReportCardRefund` are removed. Verified event settlement owns provider
mutations. Private transaction owners retain payment/grant resolution, refund
conservation, setup ordering and immutable dispute attribution. A dispute stays
bound to its first team and payment after release. Replay cannot restore a hold.
`Tenant.ReleaseCreditFreezes` still requires a named hold, actor and reason.
`Tenant.HoldByOperator` and `Tenant.ReleaseOperatorHolds` use those same owners.
Console HTTP and break-glass callers retain their authenticated tenant selection.

The raw `paid` grant path and `POST /api/v1/credits/grants` are removed.
`Tenant.GrantCredits` is removed. `Tenant.RecordCreditGrant` owns free grant requests
and requires an explicit idempotency key. A correction reverses a named free grant,
up to its remaining amount. Paid credit and payment reversals use verified provider
settlement or the authorized console refund transaction. Checkout
settlement checks actionable fraud warnings in the same transaction as the
grant and event receipt. A refusal leaves the checkout and ledger unchanged.
The verified-event implementation and HTTP adapter remain W2 integrations.

`Operator.ConfigureDeployment` permits one test-to-live switch, including after
financial activity. The persisted `settings.payment_mode` records live on the
first live start; later live starts preserve it. Live-to-test startup is refused.
Balances, standing history, holds, refill windows and payment decisions select
the configured mode. Startup refuses the switch while any test award or reservation
is unsettled or a test refill is open. All test work settles before live accounting
starts, so later charges need no reservation-mode carry-over. The switch restarts
storage watermarks at the cutover instant and forgets test-provider customers.
Test financial rows remain for history, excluded from live reports and decisions.

`Operator.CreditLedgerTotals` owns deployment-wide ledger reports. Its raw Store
method is removed. Reports use the same billing calculator as unit renewal and
settlement and never advance financial checkpoints. The real claim/exchange/release
regression checks an unstarted reservation before and after its refund.

Credit pricing and storage-rate settings use unsealed JSON values in `settings`.
Only missing keys select defaults. Malformed values, invalid prices and sealed
rows return errors. A settings batch and its returned snapshot commit together.
Rate-table reads use one snapshot so a concurrent update cannot mix its scalar
price with another table. Operator table authority is checked under the shared
advisory lock before any settings write.
`Store.CreditRateMicroPerSecond` and `Store.CreditGraceSeconds` are removed.
Their callers read the existing `CreditSettings` snapshot fields.

`CreditSettings` supplies max-charge seconds, the storage price, the free storage
allowance and rate-table presence. Their unused Store getters are removed.

| Export | Purpose |
|---|---|
| `CapabilityRecord`, `CapabilityRecord.Validate` | Preserve and validate complete versioned authority alongside indexed projections. |
| `Tenant.RevokeAuthority`, `Operator.RevokeAuthority` | Revoke authorized tenant or deployment authority atomically. |
| `MembershipUpdate`, `Tenant.UpdateMembership` | Change or remove membership with current account authority and session movement. Authority use checks the member's current role and the membership epoch. |
| `PaymentSettlement`, `Store.SettlePayment` | Deduplicate verified checkout events and commit their derived-team financial result. |

`TestRewriteSchemaCapabilitiesPreserveAuthority` persists full capability envelopes
and indexed projections, including session parents, here purpose and bootstrap
delivery. It checks invalid parent, operational and financial classifications.
The tests use JSON and the final SQLite schema because transaction methods are stubs.

## Completion, retention and accounting

Run transitions update one desired-state row per effect in their transaction.
Desired rank advances pending, running, terminal and never regresses. Each
advance increments `desired_rev`. Delivery acknowledges only the sent revision,
so a pending acknowledgement cannot consume a concurrently written terminal
revision. GitHub deliveries retain their report lock and stale-phase check.
Metrics and profile folding act once per terminal run. Parent notification is
also terminal-only in this contract.

The leased retention pass keeps each pipeline's newest successful run and its
output records. Runs otherwise expire after the configured window (Cloud 90
days; self-hosted disabled). Outputs expire after 30 days except pinned outputs.
Metrics use their own setting. Log classes retain funded logs for 90 days and
free logs for 30, or an operator-defined class. Standing changes affect new runs
only; signing refuses a class without its backend lifecycle rule. Unpinned blob
records expire by their own time; reachability GC uses a 24-hour tombstone grace.
Capabilities expire seven days after expiry or revocation. Fully delivered
terminal effects expire seven days after delivery; undelivered effects remain due.

Credit grants, charges, checkouts, freezes, cards, refunds, Stripe evidence,
storage usage, immutable billing awards and billable intervals never cascade
from a run. Removing a run must not restore spent credit. Award snapshots survive
capability pruning. Unmetered checkpoint and final settlement need no billing
calculator and write no charge or spend row. They retain award closure and
exact replay. Plan/eval compute has no minimum; job compute retains its
60-second minimum; customer-runner waits produce no billable interval. Billing
keys are configuration, so test-key and live-key deployments use the same schema.

The cache service's egress-total endpoint, its high-water-mark store methods,
client bridge and fake route are removed. Tenant usage has no service-selected
counter owner.

The legacy `PruneExpiredCacheObjects` row sweep is removed with its controller
call. Record expiry and reachability GC govern final blob lifetime. Old creation
time alone cannot delete a live record's bytes, including the default tenant's.
`ClaimExpiredSourceBundles`, `DeleteSourceBundleRows`, `PruneExpiredUploads`,
`DirectCacheMaxAge` and the controller's `CacheObjectMaxAge` are removed.
The controller's cache listing has no age-expiry callback. Source records retain
bytes independently of their originating run. Upload-ticket expiry releases
quota through the leased final retention pass. Physical GC wiring remains with
the storage adapter.

`Store.ExpiredOutputRuns`, `Store.ReleaseRunOutputs`, `Store.ForgetRunOutputs`,
`ExpiredOutputRun` and the store's duplicate `OutputRetention` constant are removed
with the controller's output-key sweep. Leased record expiry and reachability GC
own output lifetime. A copied output and its transitive manifest members survive
source-run deletion and reopening. Their bytes become collectible after the last
copy disappears. Copies add no upload usage. The SQLite retention proof checks
both deletion stages, and generated store tests race memo copying with collection.

Usage rows are additive per team, period and replica. Reporting sums rows.
Admission takes a shared team/quota lock and records each reservation durably;
settlement is idempotent by reservation ID. Log reads meter egress exactly and
are exempt from download quota. Deferred plan counters add no enforcement here.
The unused service-wide `egress_day` table is removed. Tenant egress remains in
`egress_usage` with period and replica identity.

`Tenant.AddUsage` owns egress writes under tenant, UTC day and trusted replica
identity. The controller records delivered successful-response bytes once at
request completion, including partial writes and cancelled request contexts.
An absent in-memory meter still records durable tenant usage. Logs count as
egress without consuming download quota. Accounting errors are logged. A process
termination before request completion can lose that request's egress sample.
`Tenant.EgressUsage` sums replicas for a UTC day or month.
`Operator.EgressUsageAcrossTeams` supplies team totals for startup alarm reload.
`Operator.PruneEgressUsage` retains thirteen months under the maintenance lease
and store clock. `Store.RecordEgressUsage`, `Store.ListEgressUsage` and the raw
`Store.PruneEgressUsage` are removed with the controller's principal flush timer.
Account deletion leaves the tenant counters unchanged because they have no user
identity. No schema or policy limit changes.

`Tenant.AppendEventCharged` replaces the raw Store event writer. Its trusted
replica identity comes from the controller. The method accepts no principal,
team or clock. It locks the tenant's run and commits the event, retained run
counters, shared storage admission and additive replica usage together. Event
bytes include the kind and payload. Events consume monthly and lifetime run
bytes alongside pending uploads, without consuming object capacity. Monthly
admission resets with the store clock. Run capacity survives month changes.
The existing event caps, free share, funding and claim fences remain in force.
Usage is recorded even when no quota is configured. Failed writes and overflow
return no sequence and roll back every accounting row.

`Tenant.ChargeDownload` replaces `Store.ChargeDownload`. `DownloadCharge`
carries the controller's trusted `ReplicaID`, byte count, delivered-byte flag
and existing free/funded caps. It accepts no team or caller clock. The tenant
handle supplies the team. Store time after the shared `download` quota lock
selects the UTC day. Admission totals and additive `team_download_day` replica
rows commit together. Unexpired download reservations consume shared capacity.
Expiry releases their holds before another charge can use that capacity.
Already delivered bytes remain countable after the cap, and the download that
crosses the cap finishes. Zero-byte checks add no usage row. Midnight resets
admission while preserving earlier report rows. Overflow or a failed counter
write rolls back both sides. Controller adapters reuse their existing lease
holder identity as the replica ID. Logs retain their download-cap exemption.

`Operator.ChargeRetainedStorage(ctx, maintenanceLease)` bills unique `blobs`
and committed `log_streams.stored_bytes`. Historical replica usage and reference
counts cannot multiply the charge. The team's cache and log rows share their
existing `storage_charged_through` watermark because the free allowance applies
once to the combined bytes. The tenant's retained allowance caps that total
before the free allowance comes off. An absent quota row adds no ceiling.
Each watermark and ledger debit commit under the current maintenance fence and
credit lock. Initial samples establish billing time. Idle gaps are clamped to
the stored bytes' creation time. A zero price resets existing watermarks so
resuming billing starts a new interval. Storage charges remain runless.

The `billing-ledger-survives-retention/store-data-sqlite` acceptance proof
checks a real storage debit and preserves every financial row across leased
retention and reopening. Tenant billing state, grant history and charge history
exclude a neighboring team's ledger and survive the same retention and restart.
Both history methods retain tenant attribution. Charge history reads the final
capability column into CreditCharge.TokenPrefix and preserves the original
run, job and awarded capability after those operational rows expire.
`Tenant.CreditUsageByRun` nets reservations, usage and refunds, excluding storage
and runless charges. Its per-run rows survive operational run retention and
reopening alongside the tenant balance and history.
`Store.CreditLedgerTotals` reads deployment-wide financial totals in one snapshot.
For each open metered award it uses C2's billing calculator and the immutable
intervals to subtract refundable seconds from the ledger's net compute seconds.
Consumption beyond already charged seconds waits for a billing checkpoint.
Open intervals stop at their persisted charged checkpoint for reporting. Closed
intervals use their finish time. A delayed reaper cannot add unconfirmed time.
Reporting cannot advance charged checkpoints or close intervals. Operational
run, node, trigger and credential rows do not control these totals. Settled and
unmetered awards need no calculator call. Generated store tests can use a contract
fake. The SQLite billing-start boundary uses C2's production calculator through
final settlement and replay after reopening. The retention proof
also checks exact deployment totals across pruning and reopening, with storage
seconds excluded. The unused `Store.RunFinished` pruning query is removed.
`Operator.CreditSettings`, `Operator.CreditRateTable` and
`Operator.WarmCPUClassCores` own deployment price and policy reads.
`Operator.SetCreditSettings` owns every billing-policy update. A stored rate
table refuses a scalar price update before any setting changes. The separate
`SetOperatorCreditSettings` writer and the reader override for older binaries
are removed. Explicit tables supply their own prices. An absent table derives
the default ladder from the scalar rate. Validation, ledger locking, settings
serialization and rollback stay in their existing transaction owners.
The raw Store methods are removed with every caller.
`Store.GrantCredits`, `Store.ListCreditCharges` and `Store.AppendEventOnce` are
also removed. Callers select a Tenant explicitly before grants and charge history.
The unused `Tenant.AppendEventOnce` writer is removed. State events commit with
their owning transition. External event ingestion uses `Tenant.AppendEventCharged`.

`Tenant.CreditState` and `Tenant.ListCreditGrants` own these reads. Their raw
Store twins are removed. Burn windows use the store clock. A credit-state read
uses one snapshot for grants, charges, storage spend, burn, exhaustion and prices.
The read-only SQLite property commits a concurrent ledger and price update
between the aggregate and burn reads. The first report retains its original
snapshot, and the next report sees the complete update.
Balances use `Tenant.CreditBalanceMicro`. Its raw Store twin is removed.
`Tenant.RecordCreditGrant` owns grant requests. Its raw Store twin is removed.
The proof does not exercise provider verification or native placements.
The controller's billing caller uses this operator method. W1's
maintenance coordinator owns the shared retention, quota and GC ordering.

The `billing-start-award-versus-start/store-sqlite` proof now lives in
`internal/store/store_unit_billing_boundary_test.go` as
`TestStoreBillingStartAwardVersusStart`. It uses the store's existing metered
claim fixtures with the production C2 calculator and reopens SQLite after execution
starts. Finish and replay retain the award's original price after settings change.
The ordinary-package
`internal/acceptance/store_unit_billing.go` harness and its acceptance wrapper
are removed. No native placement proof is implied.

### Storage policy and reports

The service-token `/internal/storage/reserve`, `/internal/storage/commit`,
`/internal/storage/release` and `/internal/downloads/charge` routes are removed.
`Store.RenewStorage`, `storageRenewRequest`, `recordStorageRenewTx` and
`replayStorageRenewTx` are removed with that transport. Final upload tickets own
reservation, verified commit, abort and immutable replay. Old cache RPC clients
still need W7's hard cut. Legacy direct-upload and node-log writers and their
internal reservation code are removed. Their old host/client and read surfaces
remain pending final data and tagged-log bindings.

`Operator.ConfigureDeployment` writes the final settings table through the existing checked
writer. Negative or sealed updates fail without replacing the prior value.
The persisted setting survives reopening. The controller reads slot availability
and passes full or unreadable conditions to its signup gate without a counter RPC.

`Tenant.SetStorageQuota`, `StorageQuota`, `SetStorageAllowance`,
`StorageUsageFor` and `StorageRetainedBytes` replace raw principal-selected
storage methods. Quota, usage and ranking rows carry `Team`. Write limits and
retained allowances have independent writers. Allowance creation preserves the
effective default tier. Usage reports sum replica rows and reject corrupt or
overflowing totals. Retained-byte reports share the billing inventory.

`Operator.StorageSettings`, `SetStorageSettings`, `SeedRunRetention`,
`DatabaseSize`, `ListStorageQuotas` and `TopStorageTeams` own deployment policy
and reports. Run and metric windows use `operator.run_retention_days` and
`operator.node_metric_retention_days`. Alarm and default-tier keys retain their
existing names in the final settings table. Policy writes commit as one batch.
Malformed, sealed, negative and overflowing values fail loudly.

The controller's storage maintenance calls leased retention and billing with the same
maintenance fence. The independent bucket-listing reconciliation pass and its
StorageMarks, ReconcileStorage, TeamsOverFreeLogShare and
PrunableNodeLogs APIs are removed. Retained-byte reports and billing read unique
blobs and committed tagged log streams. Bucket listings cannot replace replica
usage counters. PruneStorageCommitReceipts and PruneDownloadDays are removed with
the pass. Final upload tickets and quota reservations retain their atomic replay
state through leased maintenance. Cloud seeds 90-day run retention only when absent. Metric
retention stays independent. The old event-only and principal allowance sweeps
are removed. Allowance-driven eviction, physical GC and full elected-worker
coordination remain integration obligations.

The local C1 correction removes the raw methods and principal quota types.
Controller callers use team quota paths and `run_retention_days`. No C2, C3 or
C4 contract declaration changes here.

## Input and blob invariants

Environment snapshots persist names and a digest, not values. Raw argument
storage retains the execution/retry contract; secret argument classification
controls display. Before describe has classified arguments, display redacts all
requested values. The second seal must retain every secret name classified at
intake. Display and reproduction fail closed if those classifications disagree.
Reproduction and display omit secret values and public hashes
of secret-bearing input. App trust comes only from verified, deduplicated webhook
facts. Intake copies an available App default branch into the request and refuses
a conflicting supplied branch. Origin HEAD can fill a missing default branch
without manufacturing trust.

Repository identity is distinct from access. GitHub uses its numeric ID;
other hosts lowercase the host and preserve ports and path case, except
GitLab.com and Bitbucket.org path folding. Credentials and trailing .git are
stripped. Filesystem identities hash the cleaned absolute path and remain local
to that machine. Controller access rules and memo writes reject local keys.

A verified upload produces a receipt only after size and digest verification
and immutable-key commit. Object existence, integrity and reference authority
are separate checks. Every record/member/copy edge checks the current read set
or an exact permitted uploader receipt in the transaction. HasMany answers only
inside the read set; absent and unauthorized are indistinguishable. A download
must reach a verified EOF; early Close does not establish success.

Repo-bound user cache readers have no job attempt or upload identity. Their
server-derived `memo_scope` read edges use an empty reader attempt and carry the
authorized repository and verified record ref. `AuthorizeEdge` accepts these
only for output, manifest, memo and depcache. The transaction checks the user's
live repo grant and verified-default evidence before constructing those edges.

`blob.OutputData` applies the shared output cap to writes and reads using
`Store` and `RecordStore`. `blob.ResultRecordKey` supplies W1's persisted
output and manifest layout. `Name` is `result`. `Scope.Key` is
`<run>/<attempt>/<generation>`, alongside the authorized repo/ref/pipeline/job.
The exact uploader also stays in `Record.Source`. `ResolveWriteKey` derives this
layout from the bound attempt and refuses another run/job or a supplied scope
key. Empty output has an explicit blob and record. Required reads verify EOF and
successful close. Memo copies use the same destination layout.

`blob.MemoRecordName` is `output` for memo writers and exact-key readers.
A manifest-only memo retains W1's separate `manifest` name.
Read-only remote memo hits verify bounded output, manifest/member agreement
and member bytes before granting artifact access. Failed lookups or verification
produce a miss. Caller cancellation remains an error.

Job receipts retain team, run, job, attempt and generation. Owner-unit and
non-job publication receipts authorize only binary/source bundles. Imported
publication attempts may attach output/manifests only for the bound published
job. Owning another receipt in the same run grants no access. Source provenance
persists separately from operational run ownership, so memo, depcache and binary
records may survive the source run without losing uploader/ref facts.

Memo namespace contains repo, ref, pipeline, job and hash. Submitter-vouched
refs use manual scope and never overwrite a verified ref. Depcache restores own
ref, verified PR base, then verified default branch. A memo hit adds authorized
edges and copies output/manifest records atomically. Missing or corrupt memo is
a miss; depcache runs cold; staging and required output reads fail. Logs use a
separate signed upload path and controller-proxied reads. Cloud binary execution
checks committed digest and cloud provenance unless trust_local_builds is set.
Local remote-cache opt-in uses the user's repo-bound read grant and returns only
verified default-branch memo, artifacts and depcache, with no writes or binaries.

`depcacheblob.NewRemoteReader` adapts that `blob.RemoteCache` to `depcache.Reader`
with an explicit repository and lookup budget. `depcache.WithRemoteReader`
keeps local archives first and saves locally. The remote interface has no write
method. Both dependency-archive backends use the same verified-download and staged
extraction path. A copied memo without an explicit output digest is a miss,
including for followers. Explicit empty output remains a valid hit.

`blob.RemoteCache` accepts a zero optional lookup deadline for unbounded local
hosts. Such reads retain caller cancellation and any context deadline. An
explicit Budget deadline still bounds the entire lookup and byte verification.
No cache package supplies a default timeout.

Memo and depcache default-branch fallback apply `VerifiedDefaultOnly` before
reading bytes or member metadata. A verified ref without default-branch evidence
does not qualify. Memo uses own scope before default fallback and has no PR-base
fallback. Depcache preserves its own-ref, PR-base, default order.

The same fallback restrictions govern persisted read edges, including HasMany,
download authorization and new references. A PR-base grant permits depcache
reads, not memo reads. A default fallback needs recorded default-branch evidence
matching the reader's sealed default branch. The
`storage-memo-and-depcache-scope/store-data-sqlite` proof checks those read-set
decisions after reopening. It seeds records and publication authority; native
App intake, namespace rules and byte verification remain integration work.

`CheckDataAccess` carries a complete target run/job/attempt/generation tuple into
the authority recheck. Published job receipts require that exact imported
attempt; omitting the selectors cannot borrow a receipt from another job.

Both `Tenant` and the owning store transaction implement C2's `DataAccessChecker`.
They share the persisted authority, receipt and read-edge checks. The transaction
implementation opens no second transaction and leaves commit or rollback to its
caller. Uncommitted receipt removal, reference changes, revocation and holder
fences therefore govern authorization in that write. Caller-supplied principal
fields cannot widen the persisted capability. Final upload and record writes
still repeat their locking checks before persisting a receipt or reference.

The `storage-reachability-and-retention-tails/store-data-sqlite` proof exercises
leased record expiry, newest-success output pins, receipt roots, transitive
members, persisted tombstones, grace and failed physical deletion. Collection
requires the persisted maintenance fence even when no tombstones are ready.
The proof refuses forged fences before and after reopening, then refuses an
expired or superseded generation on an empty inventory. A same-holder takeover
must acquire a new generation before collection resumes. Collection
rechecks references added after marking, then removes tails after their last
root disappears. An old source bundle survives its originating run and becomes
collectible only after its record expires and the tombstone grace passes.
Graphs and the deletion backend are contract fixtures. Native
placement, manifest verification and physical object deletion remain integration
obligations.

The 16 MiB output cap applies to memo records named `output` at commit and lookup.
An oversized persisted memo is corrupt cache data: memo lookup and copying miss,
while a required record lookup fails. Manifest-only memo records retain their
separate artifact contract.

## Publication and retry interpretation

Publication uses the same validators and transitions under a live publisher
capability bound to the imported run. Its accepted plan creates importable jobs
without awarding an executable claim. A publish-only node edge materializes
terminal results and exact imported attempts under local provenance. That edge
is refused for executable runs. Publication never changes the local outcome.

Retry intake reads the source named by `Request.Lineage.RetryOf` from the same
Tenant. It carries source request/spec/plan bytes and hashes unchanged. The new
run ID and retrier lineage live on the destination row, outside the immutable
source input envelope; the envelope's embedded RunID remains the source identity.
`RunRow.ID` is the execution identity. `RunRow.Lineage` reads the destination
row's versioned `lineage_json`, populated by intake; `retry_of` and
`created_principal` are indexed/source-attribution projections. Inputs are never reconstructed from a new
branch head, current project settings or caller-supplied copies of App facts.

## Acceptance scenarios

These are implementation requirements for T1, not claims that stubbed methods
already pass. Each should run on SQLite and PostgreSQL where a store is involved.

| ID | Scenario and required result | Frozen section |
|---|---|---|
| C1-01 | Cross-team child/receipt/event writes fail; omitted team fails; no silent default team. | 4.1 |
| C1-02 | Concurrent intake allocates distinct team numbers and atomically creates the executable owner. | 3.1, 4.2 |
| C1-03 | Every placement seals equal inputs equally; explicit zero survives; over-ceiling input is refused. | 3.1, 7.5 |
| C1-04 | Different second seal is spec_drift; identical accept replays without inserting duplicate jobs. | 3.1 |
| C1-05 | Foreign/stale/lapsed holder cannot write; exact terminal replay writes no event. | 3.3 |
| C1-06 | Application retry retains generation/lease and advances ordinal; disruption requeues pending in same run. | 3.3-3.4 |
| C1-07 | Third started disruption fails; unstarted lapse is free; cancel wins; OOM uses ordinary retry. | 3.4, 7.6 |
| C1-08 | Claimed cancellation emits request only, then one terminal event after drain; output refusal rolls back all writes. | 3.3 |
| C1-09 | Reconciliation waits for foreign live work, then reclaims only after committed requeue. | 3.3 |
| C1-10 | Success/memo source waits for expansion; failed/skipped/cancelled source clears barrier atomically. | 3.6 |
| C1-11 | Expansion accepts only live owner/eval and terminal source generation; replay matches body; code reconstruction matches persisted hash. | 3.6 |
| C1-12 | Owner loss while remote work runs retains capacity; expired holder cannot steal capacity back; expired waiter loses arrival priority. | 3.7 |
| C1-13 | Controller owner moves after lapse without failing runs; local started orphan still fails. | 3.8 |
| C1-14 | Pending delivery in flight cannot acknowledge a later terminal revision; metrics/profile fold occur once. | 3.9 |
| C1-15 | Run retention cascades operational rows but preserves all charges and newest-success pins. | 4.4 |
| C1-16 | Two replicas reserve against one quota row; crash/retry settlement releases exactly one persisted reservation. | 2.7 |
| C1-17 | Uploaded foreign-team, foreign-run and unrelated same-run digests cannot be attached by forged manifests. | 8.1 |
| C1-18 | Missing/corrupt required bytes fail; memo misses and depcache runs cold; HasMany reveals readable bytes only. | 8.1 |
| C1-19 | Manual refs cannot overwrite verified refs; memo cannot share across pipelines without a permitted edge. | 8.1 |
| C1-20 | Local binary provenance is refused by Cloud until trust_local_builds; exact committed digest is executed. | 8.1 |
| C1-21 | Opted-in local cache reads user-authorized verified-default data only, never binary fetch or shared writes. | 8.5 |
| C1-22 | Publish accepts plan/results/logs without owner claims; failed publication leaves local success intact. | 3.1, 9.3 |
| C1-23 | Retrying older default-branch SHA retains original App trust, actor, PR and seals, with distinct retrier lineage. | 3.1, 3.5 |
| C1-24 | Repository rename, case, host ports and filesystem paths preserve the canonical identity rules. | 4.1 |
| C1-25 | Final neutral schema and translated schema have identical shapes and classified tables. | 4.1, 4.5 |
| C1-26 | Pre-plan binary/source receipt works under owner authority but cannot authorize unrelated job outputs. | 3.1, 8.1 |
| C1-27 | Log gaps/stalls read flushed segments; incomplete tail cannot change job outcome; retention class is snapshotted. | 2.7, 8.1 |

## Cross-area assumptions

| C1 type or surface | Other area must match |
|---|---|
| `runstate.Actor`, `runstate.OwnerLeaseRef`, `capability.UnitKind`, `UnitClaim.Bearer`, `AdmissionResult.HereBearer`, schema capabilities | C2 resolves references in the transaction, matches actor/fence and fixed award kinds trigger/plan/eval/node; here is a trigger Award case. Bearers return in memory once through C2's transport, never in persisted snapshots or generic JSON. |
| `ErrIllegalTransition`, blob errors | C2 maps holder_rights/stale_generation to lock_held; illegal_edge/terminal to illegal_transition; unreadable digest to forbidden with digest_not_readable. |
| `PlanAcceptance`, `ExpansionSubmission`, `RunSnapshot` | C3 validates plan declarations. C1 projects a consistent snapshot and shares Decide between both owners. |
| `AcceptedExpansion`, `NodeRow.SpecHash`, `AttemptIdentity` | C3 preserves source vs eval generations, exact node-wide attempt ordinal and persisted generated topology. |
| `runspec.Setting`, `RunnerSelection`, `Ceilings`, `SealInput` | C4 applies the frozen precedence and refuses over-ceiling settings at the correct seal. Authority checks use the store transaction clock. Delivery inbox operations accept controller timestamps; deadlines, due times and query cursors remain caller inputs. |
| `RetentionRequest`, `SegmentCommit` | C4 owns numeric limits and log class settings; C3 emits immutable attributed segments and terminal completeness. |
| `blob.Authority`, upload identity, publication edge | C2 derives run/repo/ref/publisher scope; job receipts never transfer through possession of a digest or same-run membership. |
| `billing_awards`, `billable_intervals` | C2 billing provenance and settlement use immutable award facts, exclude customer-runner wait and survive run/capability pruning. |
| `nodehost.OwnerStore`, `nodehost.ClaimStore`, `OperatorStore`, `blob.RecordStore` | Hosts use tenant-scoped ports; deployment maintenance uses the operator handle and blob records use their own port. |
| C1-01 through C1-27 | T1 supplies executable cross-placement scenarios and aligns names with its independent catalog. |

## Intake replay

`IntakeRequest.IdempotencyKey` survives `IntakeForStore` and retry intake.
`AdmitRun` derives the principal/team/pipeline scope and canonical input digest
and commits them on the run, including published runs with no trigger row.
A matching key and payload returns the original result; changed input returns
`conflict`. An absent key requests a separate invocation. Trigger queue rows
hold no competing idempotency fields.

## Interpretations and implementation obligations

1. Scheduling and expansion ownership is settled: C1 owns `RunSnapshot.Scheduling`,
   `Decide`, `ReconcileNode` and `AcceptExpansion`. C3 consumes those APIs through
   the host interfaces. The controller's wire adapter combines a declaration
   body or structured failure with C2-derived `Actor` authority.
2. Capabilities are operator-owned with nullable team only for unbound service
   and operator roots. Awards and exchanges require the derived team. Tagged
   parents preserve session and cross-team launcher ancestry.
3. Eval uses `Binding.Generation` for its immutable source fence and
   `Binding.Expansion.Generation` for its own lease. The complete binding persists
   in the versioned capability envelope, with indexed source and eval projections.
4. The frozen state table omits publication despite requiring one-way published
   jobs. The added publication-only acceptance and materialization edge uses
   live run-bound publisher authority, local provenance and no executable claim.
   Likewise, pre-plan binary/source uploads need a tagged non-job uploader. That
   authority must never satisfy an unrelated job's output edge.
5. Retry keeps the original input-envelope identity, with new execution identity
   outside it, as specified above. This preserves literal sealed bytes; C3 must
   use RunRow.ID for execution and the carried envelope only for inputs.
6. Canonical JSON and scope serialization need shared golden vectors before
   implementation. Hashes exclude their own fields and seal timestamps; object
   keys sort, list order and explicit presence remain stable. Typed namespace
   components must encode without collisions, including manual ref scope.
7. Account audit and account sessions may precede any team. `audit_events` and
   `sessions` are operator-owned with nullable team. Tenant queries still filter it. Intake idempotency belongs to `runs`, scoped by the server-derived principal
   and pipeline, so published runs receive the same replay guarantee.
8. Newest-success pins use a restrictive run FK because a cascading pin would
   permit deletion to erase the retention exception. Explicit deletion must
   resolve the pin first. The general operational-cascade rule has this exception.
9. Terminal replay uses C2 LiveResult with the persisted actor, generation and
   digest. Its narrow exception still checks token expiry, ancestry and membership.
   C1 rechecks the same identity transactionally; replay grants no sensitive reads.
10. The lock order is a target derived from the existing ledger order with new
    capability, ownership, capacity and blob locks. Implementation must exercise
    competing awards, revocation, cancellation, quota and billing before it is
    treated as proven deadlock-free.

## Schema inventory

The final baseline contains 83 tables and 101 explicit indexes. `schemaTables`
classifies 66 tenant and 17 operator tables. It retains accounts, identity,
GitHub App/Actions, schedules, sizing, admission, approvals, logs, metrics,
credits, cards, git credential audits and build trust. Obsolete token,
output, memo, agent-loss, dispatch, bounce and duplicate-plan tables are removed.
`quota_admission` is the locked aggregate; `quota_reservations` persists each
hold and its settlement. `settings.session_csrf_key` requires an envelope and
its sealed flag. All persisted timestamps use Unix nanoseconds.

## Verification boundary

This branch is a contract artifact. The neutral schema replaces the old dialect
files, so the old runtime SQL is not a functioning implementation of this
baseline. No Sparkwing binary was run. Runtime startup, PostgreSQL translation,
state-machine execution and end-to-end acceptance remain implementation work.
Completed checks:

- `GOWORK=off GOFLAGS=-buildvcs=false go build ./...` passed.
- `go vet` passed for `internal/store`, `internal/storage/...`, `internal/controller/client`,
  `internal/nodehost`, `internal/orchestrator`, `internal/blob` and `internal/runspec`,
  with the same workspace and VCS flags.
- `go test ./internal/store -run '^TestRewriteSchema' -count=1` passed all four tests.
  They exercise raw in-memory SQLite against the final embedded schema.
- `commentcheck -base origin/main` passed for the changed Go files;
  `git diff --check` passed.
- An independent Astra review found publication, pre-plan uploads and durable
  quota identity gaps. Follow-up inspection confirmed those blockers closed.
  The remaining retry read-model field was added before the final checks.

Build caches and temporary files stayed inside this worktree. No migration, compatibility shim or intermediate
schema version is added.

## File manifest

| Change | Path |
|---|---|
| Change | `internal/nodehost/approvals_test.go` |
| Change | `internal/nodehost/events_test.go` |
| Change | `internal/nodehost/expand.go` |
| Change | `internal/nodehost/rehydrate_test.go` |
| Change | `internal/nodehost/retry_eval_unix_test.go` |
| Change | `internal/nodehost/retry_test.go` |
| Change | `internal/nodehost/scheduler.go` |
| Change | `internal/nodehost/scheduler_test.go` |
| Change | `internal/orchestrator/backends.go` |
| Change | `internal/orchestrator/orchestrator.go` |
| Change | `internal/orchestrator/pod_node_host.go` |
| Change | `internal/controller/client/client.go` |
| Change | `internal/controller/client/state_assertion.go` |
| Change | `internal/storage/doc.go` |
| Change | `internal/storage/state.go` |
| Change | `internal/storage/state_test.go` |
| Change | `internal/storage/storeurl/spec.go` |
| Change | `internal/store/doc.go` |
| Delete | `internal/store/schema_postgres.sql` |
| Delete | `internal/store/schema_sqlite.sql` |
| Change | `internal/store/schema_test.go` |
| Change | `internal/store/store.go` |
| Change | `internal/store/tenant.go` |
| Create | `internal/blob/contracts.go` |
| Create | `internal/runspec/contracts.go` |
| Change | `internal/nodehost/owner.go`, `claimed_node.go`, `native_host.go` |
| Create | `internal/store/claim_contract.go` |
| Create | `internal/store/completion.go` |
| Delete | `internal/store/capacity_adoption.go` |
| Create | `internal/store/maintenance_contract.go` |
| Create | `internal/store/node_state.go` |
| Implement | `internal/store/result_metadata.go` |
| Create | `internal/store/rewrite_schema_test.go` |
| Create | `internal/store/run_spec.go` |
| Create | `internal/store/run_state.go` |
| Create | `internal/store/schema.sql` |
| Create | `internal/store/schema.go` |
| Create | `internal/store/source_release.go` |
| Create | `internal/store/state_contract.go` |

The host ports live in `internal/nodehost`; the replaced storage interfaces
are removed. Store startup uses the neutral schema and dialect translator.
The dialect-shape test compares translated schemas. `c1-data.md` is this
contract document.

## Export inventory

Each row names a contract declaration and its purpose. Enum constants belong
to their adjacent type; registry functions and variables contain complete sets.
The existing `Store`, `Tenant`, `Operator`, `Open`, `ForTeam`, `AsOperator` and
`Team` handles retain their names. Their final authority boundaries are above.

### internal/runspec/contracts.go

| Export | Purpose |
|---|---|
| `Optional` | Optional preserves an explicitly selected zero separately from absence. |
| `Hash` | Hash is a lowercase SHA-256 of canonical, versioned JSON. |
| `RepoKey` | RepoKey identifies a repository without conferring authority. |
| `AttemptIdentity` | AttemptIdentity uses a node-wide positive Attempt that never resets. Generation is a fence attribute, not the attempt ordinal or its identity. |
| `RepoInput` | RepoInput supplies independently resolved identity facts, never authorization. |
| `Repository` | Repository retains a human spelling beside the canonical key. |
| `CanonicalRepository` | CanonicalRepository strips URL credentials and trailing .git, preserves ports, and hashes cleaned absolute filesystem paths. Resolving a GitHub ID grants no access. |
| `RepoUse` | RepoUse separates machine-local storage from controller authorization scopes. |
| `ValidateRepoUse` | ValidateRepoUse refuses local keys in controller access rules and memo writes. |
| `IntakeMode` | IntakeMode prevents published history from obtaining executable owner claims. |
| `Dispatch` | Dispatch selects the owner of scheduling after acceptance. |
| `Origin` | Origin identifies the intake path without making it a trust assertion. |
| `runstate.Actor` | Actor preserves the original actor when a different person retries a run. |
| `PullRequest` | PullRequest keeps base and head facts so intake can refuse forks before award. |
| `AppFacts` | AppFacts is server-populated only after HMAC verification and replay-key dedup. No current-head lookup changes these facts, including on a retry of an older SHA. |
| `Git` | Git pins checkout to the sealed SHA, never the later head of Ref. |
| `EnvSnapshot` | EnvSnapshot persists only names and a digest; values remain in private host storage. |
| `Source` | Source records acquisition intent without embedding source credentials. |
| `Lineage` | Lineage records retriers separately from immutable original actor and git facts. |
| `Layer` | Layer names the source to report when an explicit setting is invalid. |
| `SettingOrigin` | SettingOrigin allows C4 errors to name source, file and key. |
| `Setting` | Setting retains presence and origin without resolving precedence in this package. |
| `RunnerSelection` | Preserve the requested any/name/label/here route without granting identity or access. |
| `RunnerSelectionKind` | Select the closed route vocabulary; here requests C2's run-bound Award. |
| `Options` | Options carries request winners and resolved values without zero-as-default rules. |
| `Ceilings` | Ceilings snapshots operator bounds for C4 to enforce without silent clamping. |
| `Request` | Request is submitted to the one intake transaction on every placement. App facts and authority references are derived by intake, not trusted from JSON; pool access is decided at each use. Raw requested argument values are restricted execution inputs, not a display document. SecretArgs is unset before describe. Display must then redact every requested value. |
| `IntakeFacts` | IntakeFacts are server-derived, with opaque authority IDs resolved only by C2. |
| `SealedRequest` | SealedRequest is immutable at intake, even when later settings resolve from code. |
| `SealRequest` | SealRequest canonicalizes immutable intake data after the intake owner authorizes it. |
| `DefaultBranchSource` | DefaultBranchSource limits discovery to App facts or the acquired origin HEAD. |
| `Guards` | Guards preserves refusal evidence so a rejected run seals the same inputs as a plan. |
| `Resolved` | Resolved contains checkout-derived facts and C4-resolved settings for seal point two. SecretArgs must be present (an empty, non-nil list means none) after describe. Raw Args remain available for execution, retries and masking, never direct display. |
| `Spec` | Spec binds both seals and the accepted plan hash as the one host input. |
| `SealInput` | SealInput accepts raw plan bytes already validated by C3's ValidatePlan. The store performs that validation and C4 ceiling checks in its acceptance transaction. |
| `SealSpec` | SealSpec binds the canonical spec and plan without owning plan validation. |
| `CheckSeal` | CheckSeal permits an identical replay and refuses a different hash as spec_drift. |
| `RetryInput` | RetryInput carries sealed state unchanged and records the new requester as lineage. |
| `RetrySnapshot` | RetrySnapshot preserves source trust and seals while adding separate retry lineage. |
| `CarryRetry` | CarryRetry does not consult branch head or reconstruct inputs from current settings. |
| `Context` | Context provides trusted execution inputs on every placement. It may contain secret argument values and must not be rendered or logged. |
| `Spec.GuardContext` | GuardContext retains trusted default-branch provenance for fail-closed guards. |
| `CanonicalHash` | CanonicalHash excludes the hash field itself and nonsemantic seal timestamps. Sorted object keys, stable list ordering and explicit Optional presence prevent hash drift. |
| `EnvironmentDigest` | EnvironmentDigest returns only sorted names and a digest of their captured values. Callers retain values in private host storage and never persist them in the spec. |
| `Display` | Display is a separate safe projection, because serializing Spec exposes execution inputs. Hashes and env digests are omitted because deterministic hashes can reveal guessed secret values. |
| `DisplayRequest` | DisplayRequest redacts all requested values until trusted describe supplies classification. A malformed classification fails closed. This copy never changes inputs retained for execution. |
| `DisplaySpec` | DisplaySpec applies resolved secret classification to both requested and resolved args. Every dashboard, event, public API and CLI renderer uses this projection, never raw Spec JSON. |

### internal/blob/types.go

| Export | Purpose |
|---|---|
| `Digest` | Digest names immutable bytes by lowercase SHA-256. |
| `Kind` | Kind excludes logs because log reads must be controller-proxied. |
| `Kinds` | Kinds is the complete record-kind registry for schema and wire drift checks. |
| `Blob` | Blob is existence truth; object storage alone cannot establish a live blob. |
| `Provenance` | Provenance is assigned by the server, never copied from upload metadata. |
| `Principal` | Principal is an opaque server-resolved C2 reference, never a bearer token. |
| `RefScope` | RefScope distinguishes submitter-vouched refs from verified refs. |
| `RefFacts` | RefFacts carries server-sealed evidence rather than accepting client trust flags. |
| `Scope` | Scope bounds records by authorized repository, ref and pipeline. |
| `RecordKey` | RecordKey keeps the kind, server-derived namespace and logical name distinct. |
| `Record` | Record binds source facts and provenance to immutable committed bytes. |
| `Member` | Member makes every manifest edge independently subject to authority checks. |
| `Receipt` | Receipt retains its tagged uploader and cannot become run-wide job authority. Job and imported-publication receipts keep the exact job, attempt and generation. |
| `EdgeKind` | EdgeKind enumerates the only admitted ways to make bytes reachable. |
| `ReadEdge` | ReadEdge records a server-derived ancestry or scoped-cache entitlement. |
| `ReadSet` | ReadSet is a transaction snapshot and must never be decoded from caller JSON. |
| `Authority` | Authority carries opaque C2 references for a transaction-time authorization recheck. Possession of this struct is not proof of authority; all values must be resolved server-side. |
| `EdgeRequest` | EdgeRequest checks authority independently from integrity and existence. |
| `AuthorizeEdge` | AuthorizeEdge rejects unrelated same-run receipts as digest_not_readable. Own-upload job edges require the exact Attempt, including generation. Owner and non-job publication receipts authorize only binary/source_bundle, never another job's outputs. Imported publication output edges require the matching imported attempt in a publish run. |
| `CommitRecordRequest` | CommitRecordRequest leaves provenance to server derivation and checks every member. |
| `RecordStore` | RecordStore methods execute with live binding, existence and edge checks in one transaction. |
| `Budget` | Budget bounds a batched existence query by an explicit caller deadline. No package default silently adds a customer-facing limit; limits owns policy values. |
| `PutRequest` | PutRequest signs both expected size and digest before any transfer. |
| `GetRequest` | GetRequest names bytes only through a live server-computed read set. |
| `HasManyRequest` | HasManyRequest must never expose existence beyond the caller's read set. |
| `Presence` | Presence reports only readable, live blobs; absent and unauthorized are indistinguishable. |
| `VerifiedReader` | VerifiedReader reports digest mismatch at EOF; a caller must consume EOF for success. Close before EOF does not claim verification. |
| `Store` | Store is the sole byte backend: signed /data upload plus commit, and verified downloads. Put yields a receipt only after size/digest verification and immutable-key commit. |
| `SignedTransfer` | SignedTransfer carries a one-object external URL, not a Sparkwing credential. |
| `UploadIntent` | UploadIntent binds staging bytes to the uploader before issuing a PUT URL. |
| `UploadTicket` | UploadTicket is opaque and is checked again by the commit endpoint. |
| `CommitUpload` | CommitUpload cannot substitute a digest or uploader after signing. |
| `DataSigner` | DataSigner is the controller's signed-data backend contract for S3 or filesystem. Download expiry comes from limits (60 seconds), and logs use a separate signer. |
| `SignedStoreOptions` | SignedStoreOptions injects signed-data transport without adding another cache protocol. |
| `HTTPClient` | HTTPClient constrains transport injection to one signed transfer operation. |
| `NewSignedStore` | NewSignedStore verifies transferred bytes and never falls back to cache-service routes. |
| `ObjectKey` | ObjectKey produces teams/<team>/cas/<sha256> after validating both components. |
| `MemoKey` | MemoKey cannot share an unclaimed result across pipelines. |
| `MemoRecordName` | Names the persisted memo output consistently for writers and exact-key readers. |
| `ResultRecordKey` | Names output and manifest records for an exact persisted attempt. |
| `MemoNamespace` | MemoNamespace reserves manual:<ref> for submitter-vouched writes. |
| `MemoLookup` | MemoLookup uses the authority's server-derived scope and verified default fallback. |
| `CopyMemoRequest` | CopyMemoRequest creates output and manifest edges atomically with the hit. |
| `MemoHit` | MemoHit carries digests and authorized edges rather than relying on the source run's survival. |
| `DepcacheKey` | DepcacheKey shares repo/ref isolation with memo while naming a file cache. |
| `RestoreSource` | RestoreSource orders own-ref, PR-base and verified-default cache lookups. |
| `DepcacheLookup` | DepcacheLookup never accepts a caller-selected repo outside its data grant. |
| `BuildTrust` | BuildTrust preserves the team-owned opt-in for local binary provenance. |
| `BinaryExecution` | BinaryExecution binds cloud execution to the committed digest and provenance. |
| `CheckBinaryExecution` | CheckBinaryExecution permits local provenance only with trust_local_builds. |
| `RemoteCacheOptions` | RemoteCacheOptions uses the user's existing repo-bound read authority. |
| `RemoteCache` | RemoteCache exposes reads only, with no binary fetch or shared-cache write method. Every returned record must originate in a verified default-branch run. |
| `OpenRemoteCache` | OpenRemoteCache is opt-in by profile and never borrows a runner credential. |
| `ErrBudget` | ErrBudget distinguishes exhausted reads from absent bytes. |
| `ErrNotFound` | ErrNotFound makes missing committed bytes loud at required-read boundaries. |
| `ErrIntegrity` | ErrIntegrity prevents corrupt bytes from being accepted as a successful read. |
| `ErrDigestNotReadable` | ErrDigestNotReadable maps through C2 to forbidden with reason digest_not_readable. |
| `RecordLookup` | RecordLookup combines a logical key with authority instead of accepting a bare CAS digest. Local stores may use local repository keys. Controller lookups require repo-bound data authority. |
| `RecordLookupResult` | RecordLookupResult distinguishes a cache miss from a record with required missing bytes. All returned members have passed existence and edge checks before their digests enter ReadSet. |
| `DepcacheHit` | DepcacheHit records the winning fallback so provenance remains visible after a restore. No hit, corrupt bytes and exhausted budget mean a cold cache. |
| `WriteScopeIntent` | WriteScopeIntent omits repo, ref and trust because the authority supplies those facts. Pipeline/job choices still require membership checks against the sealed run at commit. |
| `ResolveWriteKey` | ResolveWriteKey derives repository and vouched/manual ref scope from live authority. It rejects client-selected real-ref writes without server-verified facts. |
| `RecordNamespace` | RecordNamespace encodes typed components without delimiter collisions. Memo namespaces preserve memo/<repo_key>/<ref_scope>/<pipeline>/<job>/<hash> semantics. |
| `DepcacheNamespace` | DepcacheNamespace shares the memo write-ref rule while preserving the cache name and key. |
| `UploadIdentityKind` | UploadIdentityKind covers executable jobs, preplan owners and non-executable publication. |
| `UploadUnitKind` | UploadUnitKind keeps owner uploads bound to the live awarded unit's fence. |
| `UploadUnit` | UploadUnit identifies the owner lease without inventing a job before AcceptPlan. Eval source identity binds the evaluation to its terminal generator source. |
| `UploadIdentity` | UploadIdentity is resolved by C2 from live authority, never accepted as caller proof. Job requires Attempt and forbids Unit. Owner requires Unit and forbids Attempt. Publication forbids Unit and requires a publish-mode run without an executable claim. Imported publication outputs require Attempt matching that run's imported job result. Nested attempt Team/RunID must equal this identity's Team/RunID. |
| `UploadRule` | UploadRule distinguishes non-job byte intake from job-owned output edges. |
| `UploadRules` | UploadRules is the complete identity/record-kind policy, rechecked at commit. Publication with an imported attempt follows the final row rather than non-job publication. |
| `AuthorizeUpload` | AuthorizeUpload checks the C2-resolved identity and target kind in the write transaction. It requires a live owner fence or publication authority, verifies imported job identity, and rejects identity/principal/capability mismatches with the authenticated authority. |

### internal/blob/output.go

| Export | Purpose |
|---|---|
| `OutputData` | Binds output bytes and records to reader authority and an exact uploader. |
| `OutputData.Put` | Checks the output cap and uploader binding, obtains a verified receipt, then commits the output record. |
| `OutputData.Read` | Looks up the authorized output record and consumes bounded bytes through verified EOF and successful close. |

### internal/store/claim_contract.go

| Export | Purpose |
|---|---|
| `UnitDemand` | UnitDemand carries self-asserted labels only; granted identity, reservations and team come from C2's capability. |
| `UnitClaim` | UnitClaim records the store-derived team and award; source generation remains distinct from eval claim generation. |
| `Operator.ClaimUnit` | ClaimUnit applies the shared matcher and C2 Award atomically; only launcher service scope can cross teams. |
| `Store.ReleaseUnit` | ReleaseUnit returns an unstarted unit without consuming an attempt; cancellation and started leases use transitions. |
| `Operator.RenewUnit` | Renews the capability-derived unit, records owner execution starts and delivers cancellation without accepting caller-selected fences. |
| `OperatorStore` | OperatorStore restricts deployment work to explicit methods without embedding a tenant or raw Store. |

### internal/store/controller_recovery.go

| Export | Purpose |
|---|---|
| `ControllerRun` | Carries the team and global run ID of a visited controller-owned run. |
| `Operator.ReconcileControllerRuns` | Resumes settlement and live capacity renewal in bounded pages under the elected controller fence. |

### internal/store/completion.go

| Export | Purpose |
|---|---|
| `Effect` | Effect identifies one revisioned desired-state row per team and run. |
| `EffectPhase` | EffectPhase is ranked independently of the terminal outcome so a late pending write cannot regress it. |
| `DesiredRunEffect` | DesiredRunEffect is a committed snapshot for delivery; Payload comes from the run, never an executor. |
| `EffectDelivery` | EffectDelivery acknowledges only the revision actually sent, leaving a newer desired revision due. |
| `Operator.DueRunEffects` | DueRunEffects reads revisions under the completion singleton lease; expiry stops further delivery acknowledgements. |
| `Tenant.AcknowledgeRunEffect` | AcknowledgeRunEffect advances delivered_rev only to the delivered revision, never to a newly observed desired_rev. |
| `Tenant.RetryRunEffect` | RetryRunEffect records a failed delivery without overwriting a later desired state or its due work. |

### internal/store/capacity_admission.go

| Export | Purpose |
|---|---|
| `Tenant.MayStart` | Derives the complete accepted job group set and award host, then commits a grant and start permit, wait or terminal policy outcome. |

### internal/store/maintenance_contract.go

| Export | Purpose |
|---|---|
| `SingletonLease` | SingletonLease is fenced so a former replica cannot continue maintenance after another replica takes over. |
| `Operator.AcquireSingletonLease` | AcquireSingletonLease refuses a live foreign holder and advances the fence after a lapse. |
| `Operator.RenewSingletonLease` | RenewSingletonLease refuses an expired fence; owner recovery acquires a new lease instead of reviving one. |
| `ReapedUnit` | ReapedUnit reports recovery in the same run; application retry budgets are never consumed by a lapse. |
| `Operator.ReapUnits` | ReapUnits uses the store clock and cancellation precedence, settling billing and effects with each transition. |
| `RetentionRequest` | RetentionRequest is executed only while the maintenance lease is live; settings are resolved inside the store. |
| `RetentionResult` | RetentionResult reports committed removals and preserved pins; financial rows must always remain untouched. |
| `Operator.MaintainRetention` | MaintainRetention is the single deployment pass for run, metric, object and credential expiration. |
| `UsageDelta` | UsageDelta is additive per replica; summed counters are reports and never admission locks. |
| `QuotaReservation` | QuotaReservation is backed by the shared team/quota row, preventing two replicas from spending the same capacity. |
| `Tenant.AddUsage` | AddUsage records accounting under the Tenant with a replica identity, including exact log egress. |
| `Tenant.ReserveQuota` | ReserveQuota locks one shared team/quota row; log reads are exempt from download quota. |
| `Tenant.SettleQuota` | SettleQuota commits actual usage or releases the reservation in one transaction. |

### internal/store/result_metadata.go

| Export | Purpose |
|---|---|
| `StepReport` | StepReport fences step state to the attempt that produced it. |
| `AnnotationWrite` | AnnotationWrite carries already masked text; the store rechecks the shared annotation byte limit without truncation. |
| `SummaryWrite` | SummaryWrite carries masked job or step markdown under the writer's live binding. |
| `Tenant.RecordStep` | RecordStep refuses stale attempts instead of letting delayed reports replace a later attempt's steps. |
| `Tenant.WriteAnnotation` | WriteAnnotation refuses an oversized masked annotation so the store cannot split UTF-8 or reveal a raw suffix. |
| `Tenant.WriteSummary` | WriteSummary stores job or step markdown only after the claim binding is rechecked. |
| `Tenant.CommitRecord` | CommitRecord checks all member existence and reference authority before making any digest reachable. |
| `Tenant.CopyMemo` | CopyMemo creates destination output and manifest edges atomically from a server-scoped cache hit. |
| `Tenant.LookupRecord` | LookupRecord adds no authority merely because a key exists; all returned bytes must be in the caller's read set. |
| `Tenant.LookupMemo` | LookupMemo applies the claim's repo/ref/pipeline scope and the verified default-branch fallback. |
| `Tenant.LookupDepcache` | LookupDepcache follows the single restore-order table and runs cold on absent or invalid bytes. |

### internal/store/result_logs.go

| Export | Purpose |
|---|---|
| `runstate.Segment` | Separates decoded ranges from stored bytes and digest. |
| `SegmentCommit` | Binds a contiguous batch and optional seal to its writer and tagged subject. |
| `runstate.LogSegments` | Returns authorized persisted ranges and seal state. |
| `Tenant.RecordSegments` | Commits immutable segments and seals under the current writer fence. |
| `Tenant.ReadLogSegments` | Rechecks authority for the exact subject before selecting byte ranges. |

### internal/store/source_release.go

| Export | Purpose |
|---|---|
| `Tenant.ReleaseSourceCredential` | Rechecks live source authority, sealed repository, enrollment, host confirmation and a Cloud or shared-runner executor (never a personal machine, decision 6c), then commits C2's audit before returning the sealed team credential. |
| `Tenant.OwnerMachineClaimant` | Reports whether an award's root is the run requester or a personal machine the requester enrolled; `SetSourceMachineConsent` is superseded by decision 6c. |

### internal/store/state_contract.go

| Export | Purpose |
|---|---|
| `RunRow` | RunRow is the immutable request/spec plus the mutable lifecycle read model used by hosts. |
| `RunSnapshot` | Reads persisted state and accepted expansion declarations for the shared scheduling projection. |
| `runstate.SourcePolicy` | SourcePolicy keeps resume, retry, memo and coalescing authority distinct at reconciliation. |
| `runstate.ReconcileKind` | ReconcileKind tells a host whether it may execute; only Execute carries a newly fenced claim. |
| `ReconcileRequest` | ReconcileRequest names a possible source; the store checks it rather than trusting the host's cache hit. |
| `runstate.ReconciledNode` | ReconciledNode is decided with any self-claim in the same transaction. |
| `runstate.LeaseBeat` | LeaseBeat delivers cancellation and watched jobs without changing the owner heartbeat cadence. |
| `StateEvent` | StateEvent uses a single per-run sequence and a unique node state sequence for lifecycle writes. |
| `Tenant.ReadRun` | ReadRun returns only the Tenant's run, including both seals and the stable team number. |
| `Tenant.ReadNode` | ReadNode returns persisted state without interpreting an elapsed lease as an implicit requeue. |
| `Tenant.ReadSnapshot` | ReadSnapshot prevents scheduling from a mix of states observed in different transactions. |
| `Tenant.ReadStateEvents` | ReadStateEvents resumes strictly after the caller's sequence; state transitions own lifecycle event writes. |
| `Tenant.ReconcileNode` | ReconcileNode decides and self-claims atomically; WaitHolder callers follow events and reconcile again after requeue. |
| `Tenant.RenewOwner` | RenewOwner also renews self-claimed jobs and continuously live plan and job concurrency holders atomically. |
| `Tenant.RenewNodeClaim` | RenewNodeClaim renews the exact live claim and its bound concurrency holders using the store clock. |

### internal/store/node_state.go

| Export | Purpose |
|---|---|
| `runstate.NodeState` | NodeState separates offerability and claim ownership from the persisted status. |
| `runstate.NodeStatus` | NodeStatus is the persisted status; ready and claimed are derived by nodeState. |
| `runstate.ExpansionState` | ExpansionState prevents settlement before generated jobs have been accepted. |
| `runstate.ActorKind` | ActorKind identifies which store-verified authority may take an edge. |
| `runstate.OwnerLeaseKind` | OwnerLeaseKind selects the persisted liveness row; local hosts use a trigger lease too. |
| `runstate.OwnerLeaseRef` | OwnerLeaseRef identifies a fence to re-read under the store clock, never a caller's liveness assertion. |
| `runstate.Actor` | Actor carries references to authenticated authority; C2 resolves the capability inside each write transaction. |
| `runstate.NodeRow` | NodeRow contains the state facts consumed by the shared decision and reconciliation functions. |
| `runstate.NodeAttempt` | NodeAttempt preserves the node-wide ordinal across claims; generation is an attribute of that ordinal. |
| `runstate.TransitionCause` | TransitionCause selects edge-specific validation rather than accepting arbitrary column updates. |
| `runstate.NodeResult` | NodeResult references authorized records; raw digests alone never authorize output attachment. |
| `runstate.MaterializeSource` | MaterializeSource preserves the read edge that permits copying another result. |
| `runstate.ApprovalResolution` | ApprovalResolution records the resolver and policy result in the same transaction as the node edge. |
| `runstate.ClaimAward` | ClaimAward identifies an award already derived by the store; callers cannot choose its team or grants. |
| `NodeCommand` | A closed set of named operations fixes each edge and its payload before normalization. |
| `runstate.Applied` | Applied distinguishes an identical replay from a state change or a cooperative cancellation request. |
| `IllegalTransitionReason` | IllegalTransitionReason maps stale authority to lock_held and invalid edges to illegal_transition. |
| `ErrIllegalTransition` | ErrIllegalTransition refuses a failed write loudly; a zero-row mutation is never success. |
| `ErrIllegalTransition.Error` | Error supplies a stable reason without exposing a capability or result payload. |
| `Tenant.TransitionNode` | TransitionNode changes state, attempts, events, expansion barriers, holder release and settlement atomically. |

### internal/store/run_state.go

| Export | Purpose |
|---|---|
| `runstate.RunState` | RunState is both the persisted status and the logical state; terminal states never reopen. |
| `runstate.RunCause` | RunCause distinguishes replay-safe lifecycle writes from trigger recovery that keeps the same run alive. |
| `runstate.RunTransition` | RunTransition never accepts a client clock or effect payload; those derive from committed state. |
| `runstate.FinishRunRequest` | FinishRunRequest binds the terminal verdict to the owner unit that must end in the same transaction. |
| `Tenant.TransitionRun` | TransitionRun applies the run edge, its event and revisioned completion intents together. |
| `Tenant.FinishRunAndUnit` | FinishRunAndUnit prevents a terminal run from leaving an executable owner claim behind. |

### internal/store/run_spec.go

| Export | Purpose |
|---|---|
| `IntakeRequest` | IntakeRequest carries client input and the authenticated caller reference, never self-certified App facts. |
| `AdmissionResult` | AdmissionResult includes the atomically allocated team number and the first sealed request. |
| `PlanAcceptance` | PlanAcceptance binds the second seal and C3 plan to a live owner, or a live run-bound publisher for publish intake. |
| `AcceptedPlan` | AcceptedPlan is immutable after its first commit; an identical resubmission is replayed. |
| `ExpansionSubmission` | ExpansionSubmission uses owner or eval authority after the source job's claim has ended. |
| `ExpansionFailure` | ExpansionFailure resolves the barrier without inventing generated topology on a generator error. |
| `AcceptedExpansion` | AcceptedExpansion reports persisted topology; deterministic code reconstruction must reproduce each SpecHash. |
| `Tenant.AdmitRun` | AdmitRun is the only intake for local, remote, retry, child and published runs. It derives authority in the transaction; published runs receive no executable owner unit. Retry intake loads Lineage.RetryOf from this Tenant, carries its seals unchanged and records the new ID outside them. |
| `Tenant.AcceptPlan` | AcceptPlan validates the C3 plan, ceilings and declared secrets, then seals spec and inserts nodes atomically. Neither a request field nor an existing seal may change; a differing sealed hash returns spec_drift. Published plans require live publisher authority and create importable jobs without awarding an executable claim. |
| `Tenant.AcceptExpansion` | Atomically accepts exactly one success body or structured failure, rechecking authority and persisting its canonical hash. |

`Store.SetTokenMetered` is removed. Root capability metering uses C2's operator
route. Legacy claim selection shares one private metering query until those
claim APIs are retired.

## Operator refund and output read integration

Tenant.ReadJobOutput and ReadChildOutput share one current-authority snapshot
and committed output-record selector. The controller verifies the selected
physical bytes through its configured blob store. The obsolete claim-input
policy is removed with the retired C1 reader.

Console.ConsoleRefund accepts capability.Authorized, [`store.OperatorRefund`](../../../internal/store/controller_requests.go)
and the request key, returning CreditReversal. It runs through consoleWrite,
resolves the persisted paid grant before any reversal and checks its team
against the path. Its reference matches verified refund settlement. A receipt
in console_refund_receipts retains the normalized request digest and original
result independently of audit retention. The audit row projects identifiers,
digests and financial facts. The existing ledger lock serializes reads, replay,
reversal and receipt commit.

<!-- design:end -->

## Job insertion and reporting hard cuts

`Store.CreateNode`, both `MarkNodeReady` methods and the unused run-tenant
fallback are removed. `insertNodeTx` remains the sole production insertion
owner. Plan and expansion acceptance hold the run lock and apply configured
job-count guards there. The metered guard reads committed owner/plan award
billing facts from capabilities. It reads no legacy token marker. A failed
batch rolls back the seal, all inserted jobs and the expansion barrier.
Replay of an accepted plan or expansion does not spend the limit again.
Configured job-count refusal preserves `ErrComputeLimit` and carries canonical
`plan_invalid` for HTTP and engine clients.

The unused `child_invocations` table is removed from the baseline and schema
inventory. Child admission already stores lineage and the invocation replay
key on runs. Storage reservation and receipt table retirement belongs to W1b.

Run reports already use C4's sealed display projection. The legacy invocation
redactor, its public wrappers/constants and the unused classification getter are
removed. HTTP and dashboard
readers consume the reporting view directly. Receipts consume that same view
and omit input hashes for unknown classification and supplied secret args.
GitHub commit reporting shares the sealed trigger scanner and filters by the
tenant, numeric repository key and exact SHA. It no longer reads removed
trigger repository columns or issues one read per result.


## GitHub App administration transactions

Tenant also implements C2 gitHubConnectWriter. Identity reads, nonce consumption
and installation binding recheck the same live direct owner session, current
team membership and exact account/team in a store-clock transaction. Binding
rechecks the linked provider subject, derives the connector and preserves a
foreign installation's ownership. Nonces commit once before provider redemption.
Installation creation and update timestamps use nanoseconds. Raw installation
binding and the unscoped connection nonce API are removed. Test fixtures use
TestOnlySeedGitHubInstallation, which shares the binding statement.

Tenant implements C2 gitHubAppWriter. All six mutations serialize on the team
and recheck live authority, ancestry, membership and the selected team.admin
access before commit. Subscription and automation writes lock and require the
tenant's unsuspended installation. Actor and nanosecond timestamps derive from
the transaction. Automation retries retain the existing consent generation.
Revocation followed by a new grant receives a new store-clock generation.

Extra repository slugs and positive provider-verified numeric IDs commit in one
transaction. Nonempty lists require an active installation for that repository
owner. Missing IDs, foreign owners and failed inserts roll back replacement.
Clearing a list remains possible after unbinding or suspension.

The raw Tenant UnbindGitHubAppInstallation, PutGitHubAppTrigger,
DeleteGitHubAppTrigger, PutGitHubAppAutomation, DeleteGitHubAppAutomation,
SetGitHubAppExtraRepos and SetGitHubAppExtraRepoIDs are removed. Trusted provider
uninstall still uses Operator and its existing transaction helper. Automatic
schedule run admission still needs the committing origin contract described
in the earlier intake handoff. App admission uses the trusted transaction below.


## Run-control transactions and sealed intake rate guards

Tenant implements C2's runControlWriter. Debug-pause creation and approval
resolution recheck the canonical route's current member/local authority in the
committing transaction. Targets stay in one tenant and run. Pause creation
requires a live job and derives its timestamp and default expiry from the store
clock. Approval resolution uses the existing terminal transition, persists the
account subject as approver and never accepts a public timeout decision.

The C1-local `run_control_requests` table stores private replay receipts keyed by
`(team,run_id,subject_kind,subject_id,operation,request_key)`. Its request hash
covers the exact submitted body, including the selected job. A repeated key
returns the committed result after current authority checks. A changed body
conflicts even when masking would make both public strings equal. The receipt,
state, public events and masked approval result commit together. The table
references its run with cascading deletion. Tenant purge removes it too. Account
deletion drops that account's private receipts after ending its authority, while
other subjects' replay records remain.

Public control text uses the shared secrets masker over the store-selected
masked pipeline/shared secrets and classified sealed arguments. Before describe,
unknown argument classification masks every requested value. The existing
PlanSecretOpener supplies decryption. A decryption or final authority failure
rolls back the receipt and all state. This adds no runtime-mask propagation.

AdmitRun applies the configured hourly guards after idempotent and App replay
selection, before inserting either the run or its owner. The single per-principal
guard uses the authenticated subject kind and ID within its tenant, including
unmetered members. Retry admissions use the committed retrier lineage for the
subject kind because the source request retains its original actor. An account
and machine sharing an ID have separate budgets. The global guard covers every team and uses only the configured cap. Pending
execution backlog never derives a per-team hourly share. Both read committed run admissions
under the shared store serialization, never summed usage counters. Refusals preserve ErrComputeLimit and expose canonical rate_limited with the
existing five-second retry delay. Replay spends no second slot. Raw pending-run attribution and token metering are not intake
inputs. The unused OldestWaitingReadyNodeForPrincipal diagnostic is removed.

## Runner inventory and accepted liveness

Runner observation cursors use one database time captured before their query.
Inventory refuses future observations. Placement compares poll time and the
stored last-use time against the same liveness cutoff.

`Tenant.ReadRunners(context.Context) (capability.RunnerSnapshot, error)` implements
C2's `runnerInventoryReader`. It resolves live machine ancestry, accepted polls
and current owner, job and eval awards in one store-clock snapshot. Roots remain
distinct by their persisted authority. Cloud awards use their immutable billing
reservation across exchange. Here jobs group under their original here award,
separately from a requesting machine's registration. Names and holders never join
unrelated authority.

The single issuance transaction records accepted award liveness. Node and owner
renewal transactions and eval renewal record liveness after validating the current
fence and accounting. An observation failure rolls the transaction back. Renewal
updates `capabilities.last_used_at` without changing `claim_observation_json` or
advertised labels. Placement still requires a fresh accepted poll timestamp.

Only current same-team awards contribute activity. Revoked ancestry, expired
leases, finished work and replaced generations do not contribute. Cancellation
keeps a live draining holder visible. Eval activity also requires its current
source generation, expected expansion and controller lease. Corrupt observation
facts refuse the whole snapshot.

No additional table or foreign signature is added. Maximum concurrency has no
enrollment field in the current provisioning contract. Queue depth has no claim
observation field in the wire contract. Both remain zero until their owners
supply those inputs. Missing capacity measurements stay absent. W1 does not
derive concurrency or queue depth from CPU counts or leases.


## Upload abandonment and billing ledger ownership

`uploads.abandoned_at BIGINT` is nullable. Abort and leased upload expiry set it
once under the upload and digest locks, register a tombstoned blob for possible
copied bytes, and release quota in the same transaction. Pending tickets with no
abandonment timestamp are roots in the tenant's reachable blob graph. Reservation
holds the digest lock and clears its tombstone before the object copy can begin.
Abandoned rows create no receipt. The object deleter treats missing bytes as
success. This prevents repeat expiry from recreating already collected blobs.

The node and trigger `credit_*` columns and the legacy claim-credit writers are
removed. `billing_awards` and `billable_intervals` own compute charges. Unique
indexes bind `(team,capability_prefix)` and
`(team,run_id,node_id,unit_kind,generation)` to one award each. Node price and
active compute usage read that ledger.

Non-final metered checkpoints check headroom after charging and stamp the team's
existing exhaustion time. Renewal delivers cancellation at the grace boundary.
`DefaultCreditGraceSeconds` is the single ten-minute placeholder pending Korey's
policy decision. Restored headroom clears exhaustion. Cloud plan and eval
intervals open on the first renewal and remain open regardless of the holder's
executing flag, as Cloud job intervals do.

Upload tickets retain their run and job identifiers without cascading foreign
keys. They survive run deletion because leased expiry still needs the digest to
collect possible copied bytes and release every quota dimension.

## Decision 112 billing cut

The billing implementation uses `billing_policy.go` as its ladder and standing
table. `BillingStanding` carries `Tier`, `CloudVCPU`, `MaxJobDuration` and the
operator decision. `SpendStanding` keeps the owner budget, balance, current rung
and collection state. Purchase-window and dollar-cap APIs and override fields
are removed. The final schema replaces `billing_rung_cents` with `billing_rung_index` and
removes shared-card spend buckets. Standing is enforced inside Cloud issuance
under the ledger lock. The award captures the hard deadline. See
`docs/billing.md` for the decision's policy table.

Queue eligibility reads the same standing capacity rule without taking the
ledger lock ahead of claim locks. Issuance rechecks it under the ledger lock.
Full teams and oversized jobs do not block other eligible teams. A lost dispute
reverses its payment while retaining the hold. Won and warning-closed disputes
release their hold. The existing named operator release remains available.

Each declined collection attempt owns a separate hold. Replayed declines retain
operator releases. A later declined retry creates a new hold. Successful
collection releases every decline hold on that team.


## Decision 112 production hosted pages

`Tenant` implements C2's `billingWriter` directly. Billing writes lock the team
and member authority before the ledger, then recheck authority at commit. `billing_pages`
is a tenant table with the reservation ID, caller account ID, request key,
payment mode, immutable page facts and failed-delivery time. Its unique key is
`(team, account_id, request_key)`. Changing kind or requested amount conflicts.
Account removal and tenant purge preserve financial replay facts. Purge removes
the hosted-page delivery details, retaining its checkout and payment association.

Prepare commits a prepaid checkout or recovery attempt with the page reservation.
Card setup reserves a new setup identity. Delivery failure retains the reservation
and provider key. Completion binds the hosted session and its customer and saves
the URL in the same transaction. It cannot extend expiry
or change reserved amount, payer or attempt facts. A completed replay uses its
saved URL after rechecking authority and payment mode. The existing 35-minute
checkout/recovery hold bounds page expiry. An expired key conflicts rather than
creating another payment operation.

`BillingPage.ProviderID` is the hosted Checkout Session and `BillingPage.Customer`
its Stripe customer; no intent exists when the page opens. Completion records a
card setup page as a `setup_session` correlation and a recovery page as a
`collection_session` correlation, both keyed by the session ID. The signed
`checkout.session.completed` event of that session then binds the setup or
payment intent it names to the correlated team, customer and attempt, and that
intent's own events settle against the binding without trusting webhook
metadata. The checkout service response supplies `id`, `url`, `customer` and
optional `expires_at` for card setup and recovery, and `id`, `url` and optional
`expires_at` for credit pages. Completion refuses absent required correlation
facts.

Decision 112 replaces the prior purchase-cap/trust gate wording. Adding funds
and saving a card require current team administration, valid payment amounts
and a team not pending deletion. Cloud standing and owner budgets are enforced
at Cloud admission. They do not prevent funding or debt recovery.

The production tenant surface has no raw `OpenCreditCheckout`,
`AttachCreditCheckout`, `DropCreditCheckout`, `SetSpendBudget` or
`StartRecoveryAttempt`. Authorized page and budget writers own those mutations.
Store fixtures reach transaction helpers through test-only methods. The unused
unattached-checkout abort path and its legacy-only test are deleted. Ambiguous
provider failures retain their reservation and replay identity.

`Operator.ConfigureDeployment` persists validated `test`/`live` configuration
in the startup transaction, before requests and workers start. Controller startup
uses the same `--payment-mode` for the store and verifier. An unconfigured ledger
uses test mode. The first live start refuses unsettled test awards and reservations
or open test refills. It atomically records the switch, restarts storage watermarks
at the cutover instant, and forgets test Stripe customers and refill opt-ins.
Publication failure rolls back the whole transaction. Subsequent live starts
preserve live cards, refills and accounting watermarks. Payment mode stays fixed
for that startup, so financial readers need no payment-mode row lock. Purging teams never clears
the persisted mode. Live-to-test startup is refused. Reservations and verified
settlement select the configured mode, and test rows never fund live work.

## Runner integration amendment: source reporting and declared references

Source annotations and summaries retain their owner or eval lease independently
of a work attempt. `WriteAnnotation` and `WriteSummary` use that authority only
when `StepID` is empty. `RecordStep` and step metadata keep the live holder,
positive attempt and attempt-writer checks.

`BindSourceReport` binds an expansion evaluator to its source job and immutable
source generation. Its committing check repeats expansion authority, including
the trigger-owner lease or the eval lease and elected controller-owner fence.
It refuses a cancelled or finished run and a source that is no longer successful
at the captured generation. No synthetic attempt is stored. A trigger or plan
owner can write job metadata under its current owner lease before work is awarded.

Declared pipeline-reference reads accept a live trigger or plan owner as well as
a live work claim. The source process narrows the owner's declarations to the
job being evaluated. Reading a result from another run additionally requires a
declared pipeline and job, a terminal producer run and a successful or memoized
producer job. A pipeline selector carries no run ID; a concrete producer lookup supplies
its run and job separately and verifies their terminal state before checking
the declaration. Every lookup remains inside the credential's team. Eval's fixed
grants still allow only its own source-output read.

The security invariants are: source metadata cannot outlive or escape its source
lease, and a pipeline-reference read cannot escape its declared pipeline and job.
`FuzzSourceMetadataCannotEscapeItsLease` and
`FuzzDeclaredPipelineReferenceDoesNotEscapeItsJob` exercise those boundaries.

A remote trigger owner submits expansion through the controller, which derives
the same source-bound delegation used by an in-process owner. `AcceptExpansion`
keeps its existing source, cancellation, owner-lease and replay checks.

## Runner composition amendment: materialized eval source zero

An eval source generation is nonnegative because memoized and materialized
terminal jobs can finish before any work claim. The eval lease generation stays
positive. Claim encoding includes an explicit `SourceGeneration`, including
zero. Decoding refuses an omitted or null generation instead of treating it as
materialized source zero.

Claims, exchange, renewal, expansion authorization and tagged eval logs preserve
these independent fences. Log subjects require a source object and use its
generation in the physical namespace. A source at zero with eval lease one
uses `s0-g1`, distinct from other source or lease generations. The final schema
permits nonnegative source generations in eval units and log streams. No
migration or backfill is introduced.

`TestMemoizedSourceZeroClaimsRenewsAndPublishesEvalLog` drives the persisted
boundaries. `FuzzZeroSourceEvalCannotBorrowAnotherFence` refuses changed source
and lease identities without appending or sealing the awarded log stream.

## W9 launcher preparation renewal

`Store.RenewLauncherPreparation` is a writer transaction authenticated by launcher
root authority. It returns only the bootstrap fences it renewed. The original
award must belong to that root and service subject. The current row must retain
the requested unit, holder and generation, a live lease and unexchanged bootstrap.
Root ancestry and unit deadlines remain bounds. Forged or foreign original awards
refuse the request. Finished, cancelled, expired, replaced and exchanged own
awards receive no renewal acknowledgement and do not block cleanup of other Jobs.

Preparation uses the shared capacity renewal and Cloud financial functions.
It never opens a billable interval (A59): metering for a launcher award starts
at the work pod's first renewal or start report. It never synthesizes a running
transition. Accounting and existing credit grace continue during preparation.
Cancelling a run, or a claimed node, settles every metered award under it that
has no billable interval in the same transaction, refunding its reservation.
At grace expiry the transaction commits accounting and expires only the original
bootstrap lease, so reconciliation can retire its Job and exchange cannot start
work. No acknowledgement is returned. No new rate or grace policy is added.

Invariant: launcher preparation cannot renew another launcher's award, revive an
expired lease or renew the SDK's exchanged claim. Property coverage is
`FuzzLauncherPreparationKeepsExactUnexchangedFence` and
`FuzzLauncherPreparationBillingAndGrace`. Fake-clock tests cover plan/eval/node
renewal through exchange and mixed live/ended preparation requests.

## Credit holds on unclaimed Cloud work

`UnclaimedUnit.CreditHold` is set, for a `dispatch=controller` run, when no
live unmetered team claimant could take the waiting unit (claim grants,
`meteredPlacement` false, and the claim's matching inputs and requirements
evaluate OK) and the unit's first slice fails the same hold and balance checks a
launcher claim applies. It reads that refusal's own message, which names the
balance shortfall and its remedy (add funds or turn on automatic refill) or the
hold and its remedy.
The controller reports it as notice kind `credit_hold` in place of
`unclaimed_work`, after the same 30-second claim attention grace. Intake does not refuse: work waits and is claimed as soon as
prepaid credits cover it (decision 26). An unpriced CPU class reports no hold.

## Break-glass credit grants

Break-glass in-pod grants extend frozen §9's break-glass scope from freeze to
grants. They are capped at $100 without the explicit flag. `sparkwing-controller admin
grant` refuses more than 10,000 cents per grant unless `--allow-large` is supplied
for that invocation. `--operator` names the operator's own account ID. It grants
once per key with reference `admin:<key>` and records `credit.granted` with that
account as the actor and a digest of the reference. Audit rows exclude free-form
reasons. No remote credential reaches it.

## Integrated working-tree source uploads

`source_uploads` is a tenant table. Its primary key is `(team,id)`; it stores
`authority_kind`, `authority_id`, `repo_key`, `repo_display`, `execution_sha`,
`pipeline_sha`, `digest`, `size`, `expires_at`, `committed_at`, `bound_run_id`,
`period`, `replica_id` and `charged_objects`. `bound_run_id` references the
retained run with cascading deletion. An expiry index selects
tickets. Pending tickets are not blob receipts and create no synthetic run.

`Tenant.ReserveSourceUpload`, `ReadSourceUpload`, `CommitSourceUpload` and
`AbortSourceUpload` resolve current submission authority in their transactions.
An unbound controller member/local credential needs `runs.write` and repository
authority. Scope carries the server-verified display as well as the stable key.
Reserve uses shared monthly quota holds; commit requires backend-verified
bytes and uses the shared blob insert and unique-byte accounting. Committed
charges retain their period, replica and unique-object count. Replays spend
nothing. Signing failure aborts its hold.

Intake binds one committed, unconsumed upload owned by its exact tagged
requester to the same repository, execution revision, pipeline revision and
digest. It atomically enforces run quota, records the actual committed charge
in run usage, and inserts a local `source_bundle` row in `blob_records` with
`source_kind=submission`, no verified ref and the originating run's provenance.
Retries copy that retained source edge while preserving original provenance.
A source digest in caller JSON without this proof is forbidden.

Unbound committed uploads retain the existing 24-hour source window, measured
from commit. `Operator.ExpireSourceUploads` holds the maintenance lease, deletes
staging bytes before releasing pending quota and removing expired unbound
tickets. Failed deletion retains the ticket for retry. Bound source records
follow run retention after their temporary upload tickets expire.

The source-upload schema advances the hard-cut rewrite baseline to version 4.
Version 3 stores are refused by the existing baseline check; no migration or
compatibility path is added. Native QA must use a fresh version-4 development
store. This is a schema change within the assigned integration slice.

## Burst requester secrets

`runspec.Request.LocalSecretNames` seals only the requester-selected names.
`IntakeRequest.LocalSecrets` carries their resolved values outside persisted
JSON. Names and values must match exactly. Admission copies values into the
Store process's run-and-team registry before its transaction commits. Failed
admission removes only the entry it created. Identical active intake replays
retain the first values. Changed values refuse with `spec_drift`.
`Tenant.ReplayRun` and `Tenant.AdmitRun` share private-input validation, including
local name/value equality, registry availability and submitted-environment
verification. Replay reads the registry without inserting values. Only a missing
idempotency record proceeds to source resolution. Replay conflicts and validation
errors return before source access, including when the repository is unavailable.

Plan acceptance requires every selected local name in the pipeline's declared
secret names. Local values participate in identifier validation and satisfy
secret preflight. The selected local value takes precedence over a stored team
secret. Local values have no pool restriction inherited from that stored row.

`Tenant.ReadSecret` returns local values only after the existing live authority,
declaration and claim fences pass. It commits the same `secret_released` audit,
then rechecks authority. Returned local rows have `Masked=true` and
`Ephemeral=true`. The ephemeral marker is absent from JSON. Values never enter
secret rows, run seals, events or source-bundle storage. Terminal run commit
removes the registry entry. Team deletion and owner lease expiry also purge it.
A rolled-back completion retains it. Store close clears the registry.

Losing the registry fails with `unavailable`, even if a stored secret has the
same name. Intake replay cannot restore lost values. Retry of a run with local
secrets requires a new requester submission because retained seals have only
names. The refusal directs the requester to submit a new run.

The supported Cloud chart uses one controller replica and Recreate rollouts.
That topology sends intake, plan validation and releases to the same in-memory
registry. Multi-replica deployment needs a verified run-affinity or ephemeral
relay contract before it can support local secrets. This amendment adds no
relay or stored value. ResupplyHereSecrets accepts matching sealed names and values
only from the run's live, fenced here owner. It restores a lost registry on its
authenticated heartbeat. Existing values cannot be changed. After cancellation,
resupply from the live, fenced here owner is a no-op so reporting renewal and
drain can finish. It never restores values or permits a secret read. The owner
and run locks serialize this check with cancellation. Burst job claims and
secret reads independently restrict eligibility to the sealed here/personal/Cloud
set. The here owner keeps planning authority. Publish-mode success never updates
pipeline_success_pins.

## Burst placement and per-user overflow

`Request.Burst` seals ordered `PlacementStep{Target, Hold}` preferences and an optional narrowing `OverflowMinutes` cap. Targets resolve to authenticated here capacity, the requesting account's registered machines, Cloud or granted runner/pool selectors. Existing `ClaimPlacement` controls hold times for the same owner projections, reconciliation and claim paths. Burst nodes can overflow a here-hosted run without transferring its owner.

The baseline is version 7. `runs.created_subject_kind` accompanies the authenticated creator ID so personal-machine ownership cannot confuse account and machine IDs. `overflow_controls(team, account_id, monthly_minutes)` stores optional monthly allowances. No row means no per-user cap. Account authority changes its own allowance through `SetOverflowLimit` and reads usage through `ReadOverflowControl`. Cloud admission observes the narrower stored/request cap as well as team billing admission. Existing work finishes and unmetered capacity remains eligible. Full field and verification details are in `design/rewrite/W9-BURST-PLACEMENT.md`.

`billing_awards.requester_account` captures the authenticated account creator
from the run at award time. Non-account creators store an empty value.
Overflow usage reads that retained attribution with its retained charges and
compute intervals, without joining run history. Deletion or reuse of a run ID
cannot erase usage or transfer it to another account. No foreign key makes
financial attribution depend on retained runs or accounts. Version-5 stores
are refused and need a fresh baseline; no migration or backfill is added.

## Cloud allocation projection for burst execution

`Store.CloudAwardResources(ctx, authority)` reads live ancestry and the award's
committed billing reservation in one snapshot. It returns no allocation for
customer machines or here ownership. A Cloud award returns the CPU class
recorded in its reservation charge and `CPUClassMemoryBytes` for that class.
The current price table and later labels cannot resize that award.
Bootstrap exchange retains the reservation and its allocation. A consumed
bootstrap cannot read it again. No new price, tier, limit or schema field is added.

The allocation property starts with the smallest Cloud size, replaces the
price table, and exchanges the bootstrap. Generated table changes retain the
original class and memory. Customer machine awards retain no Cloud allocation.

## Label requirements

All claims use sealed label requirements and observed platform facts. There is
no implicit CPU request, CPU/memory fit or current free-resource observation.
Cloud sizes use the catalog in `pkg/match/cloud_sizes.go`; an unspecified size
selects its existing smallest two-core allocation for billing and delivery.

## Burst self-claim eligibility

Burst self-claims use the canonical label predicate and live owner fence.
They carry no resource reservation or free-capacity observation. The atomic
award rechecks repository access, selectors, placement and funding policy.

## Local dependency-cache scope

A live `SubjectLocal` execution with `DataClaim` authority may save and restore
dependency caches under its sealed `local:` repository key in the local
controller. Foreign repositories, account/machine subjects, publication and
cross-profile cache authority cannot use that local namespace. Shared memo
restrictions remain. Existing attempt, receipt, ref and read-edge checks apply.

An absent unverified Git ref has the distinct namespace component `manual:`.
Named refs escape their colons, so a literal `manual:` ref cannot alias it.
An empty verified ref is invalid. The dependency-cache client preserves the
absent unverified ref on lookup instead of rejecting a valid local hit.
No table, credential grant or public API changes.

## Imported local history

CausePublishAttempt (`publish_attempt`) is a pending-to-pending operator edge
under the private `guardPublication` rule. TransitionExtra.Publication carries PublishedAttempt:
positive ordinal and generation, completed timestamps, terminal outcome and
executor name. Import creates one terminal local node_attempts row, without an
award, live holder, lease, trigger or billing reservation. It rejects a second import
with changed bytes. CausePublish must agree with that imported outcome. Imported
results retain the normal receipt, exact-attempt record and member checks.
Owner and publication result proofs use the issuing capability's attempt,
separate from the job's execution attempt. Node-holder proofs use the captured
source attempt and retain the committing transaction's ordinal check.
Secret availability preflight applies to
executable intake, while publish-mode plans require no execution secrets.

Tenant.ReadLocalPublication reads terminal state and its selected result records
and verified members. The controller exposes it only to the verified local peer
on the existing private snapshot route. Local CAS readers use ObjectKey and an
os.Root, and signed uploads verify the digest and size again.

Local publication reuses the persisted result reader used by materialization,
retaining failure reasons, masked errors, exit codes and structured failures.

Publication attempts always store local for executor kind, name and location.
The caller-supplied executor name cannot alter that provenance. Billing awards
index (team, requester_account) for account-scoped overflow lookup.

## Profile completion delivery

`Operator.DueProfileFolds(ctx, completionLease, limit)` selects the profile lane
through the same leased completion reader as `DueRunEffects`. Other due effect
types cannot starve this lane. It leaves their revisions unchanged.

`Tenant.ApplyProfileFold(ctx, EffectDelivery, map[string]ProfileObservation)`
returns whether it committed a new fold. It loads the pipeline from the same
team's terminal run, locks the exact outbox row, applies the complete observation
batch in stable job order and acknowledges that revision in one transaction.
Invalid observations, write failures and lease expiry roll everything back.
Already delivered revisions apply no observations. The lease is rechecked after
the writes before commit. The profile observation transaction helper is shared
with `RecordProfileObservation`; no second folding algorithm exists.

The completion implementation lives in `internal/store/completion.go`. Its former
contract scaffold is removed. `Server.ReconcileRunEffects(ctx)` consumes the
profile lane with current tenant handles, retrying failed reads or commits
through the existing outbox. Corrupt metric reads fail the entire observation
batch. The HTTP controller calls the reconciler from its existing maintenance loop.
The elected local runtime calls the same reconciler through
`localcontroller.Services.Complete func(context.Context) error` on its existing
maintenance cadence, after readiness and while owning the store. The CLI binds
that callback to the concrete server. Local completion failures join the
existing worker shutdown path. GitHub delivery shares the same worker as described below. Metrics and parent
notification delivery remain unfinished lanes of the same completion outbox.

## GitHub completion delivery

`Operator.DueGitHubChecks` selects the GitHub lane under the existing completion
lease. Every completion reader captures its due cutoff and `ObservedAt` once
before opening the result cursor, so reading a batch requires no nested clock
queries. `DesiredRunEffect.ObservedAt` supplies the store time used for retry
deadlines. `Reason` is read from the exact run state event, including
queue_timeout and timeout. The payload carries that committed reason. A late
acknowledgement still advances only its delivered revision.

`triggers.github_status_fallback` records the per-run commit-status fallback as
0 or 1. `Tenant.GitHubCommitStatuses` reads it and
`Tenant.UseGitHubCommitStatuses` commits it before any fallback status is sent.
It is independent of the positive GitHub check ID and scoped to the same tenant
and run. A controller replacement continues that mode even if the installation
has since accepted the checks permission. `ReportGitHubCheck` keeps its existing
report lock and ID recording transaction. Failed ID persistence is retried by
adopting the check with this run's external ID.

This adds one column to the final schema. Combined with burst attribution and
overflow controls, the disposable baseline is version 7. Earlier stores are
refused. There is no migration or backfill. Native QA needs a fresh version-6 store.

`Server.ReconcileRunEffects` replaces `ReconcileProfileFolds` in both controller
compositions. It delivers the profile and GitHub lanes under the same lease,
with each lane receiving its own bounded batch. The in-memory GitHub queue,
capacity drops, retry sleeps and shutdown drain are removed. Pending, running
and terminal intents all come from run transitions. Metrics and parent
notification remain due for their unfinished consumers.

Invalid sealed App completion facts call `Tenant.RejectRunEffect`: the existing
revision-fenced transaction increments attempts once and consumes only that
revision. The controller logs that failure once. Transient provider and storage
errors retain the existing retry backoff. No completion SQL dialect change is made.


## Verified GitHub App admission integration

`Tenant.AdmitGitHubAppRun(ctx, GitHubAppAdmission)` is a trusted webhook adapter,
not a customer route. C2 calls it only after HMAC verification, event binding,
repository coverage and event/subscription selection. It supplies the exact
installation snapshot and either the selected subscription or automation grant.

The transaction attaches that proof privately to `storeTx` and calls the existing
`admitRun`. Its `github_app` authority reference identifies the installation and
numeric repository for accountability. It is neither a capability nor a session.
Only that transaction resolves it. Ordinary intake and authentication cannot
manufacture it, and no bearer is issued for it. `VerifiedAppIntake` exposes the
proof to the existing C4 settings policy through its transaction resolver.

Every authority check locks and rechecks the installation's team, suspension and
connection generation, the bound signed event, and the exact subscription or
consent generation. Admission retains the shared number allocation, rate guards,
source location, owner unit, first seal and completion intent. Failed validation
or effects roll all of them back. A retry of the signed body and pipeline returns
the existing run without spending another number or admission slot.

A fresh installation connection receives a random generation. Ingress records
that generation and installation ID with the signed body under the installation
lock. One transaction fence compares the receipt with the live connection before
manual processing, every delivery mutation, and admission. Moving away and back,
or disconnecting and reconnecting within one team, cannot revive an old delivery.
The receipt expires with the existing replay retention.

Replay keys use the signed body digest plus pipeline and are selected inside the
tenant. The connection fence prevents replays across replacement installation
bindings. `GitHubCommitRuns` replaces `GitHubCommitTriggers` and reads
sealed requests in one query by tenant, numeric repository and exact SHA.
Re-request selection uses those facts rather than removed trigger environment
columns. OIDC classification uses the event in the seal, so expiry of the replay
ledger does not erase the identity of a retained or retried run.

`CronSchedule.RepositoryKey` is a read projection of derived identity, separate
from App installation authority. `ArmCronSchedule` ignores the input key and
canonicalizes the source using the verified App IDs or `scheduleRepositoryResolver`
on its existing intake policy. The controller implements that resolver through
its shared run-intake identity lookup. Manual GitHub schedules acquire numeric
identity without acquiring App authority. Manual/App conflicts compare those keys.
Unresolved GitHub URLs fail before arming, including former names that cannot be
resolved. The old alias comparison and its GitHub token mint are removed.

Data authority maps a full GitHub branch ref and the App's short default-branch
name into the same storage ref namespace. Only the exact verified pushed branch
can receive default-branch trust. Tags, mismatched SHAs and non-default events
remain untrusted. Generated tests cover these cases through live data authority.

The raw automation-specific and generic trigger insertion APIs are removed.
Controller schedules reach the same sealed intake through the adapter below.


## Integration amendment: audited GitHub source release

[`store.Tenant.PrepareGitHubSourceRelease`](../../../internal/store/github_source_release.go)
resolves source authority and consent in a short transaction. The controller
mints through [`controller.GitHubSourceProvider`](../../../internal/controller/source_controller.go)
with no transaction open. [`store.Tenant.CommitGitHubSourceRelease`](../../../internal/store/github_source_release.go)
rechecks authority, enrollment, consent and fences, requires the same installation,
approved IDs and names, and commits the audit before the controller returns
the token. The grant carries approved extra repository names, excluding the
primary repository.
GitHub verifies coverage while minting that ID set. Source release never lists
installation repositories, so the listing's 1,000-repository bound cannot turn
an authorized source into not_found. Refused coverage returns conflict; other
mint failures return unavailable. The store derives the repository, ref, SHA, installation and executor from
its authority chain and request seal. Verified App requests use their recorded
installation. Typed requests require a current team subscription for their
numeric repository ID. A live installation belongs to the same team. Every
requested ID must be covered, and only `contents: read` is minted.

`github_app_extra_repos.repository_id` is a positive source repository ID in the
disposable baseline schema. Owner writes resolve it from the installation along
with each extra ID. Release and replacement use this source ID, so neither a
source nor an extra can transfer its approval to a replacement at the old name.
Display slugs remain reporting fields. No migration or name-based fallback is
added. The unused `GitHubAppExtraRepoRefs` reader and legacy `SourceCredential`
and `SourceSpec` response declarations are removed. Their numeric-ID assertions
now run through the release transaction and generated rename tests.

The App and stored-credential paths share persisted source authority, fences and
the audit writer. **Superseded by decision 6c (2026-10-18):** App tokens and stored team credentials
both require verified Cloud placement or a shared runner, never a personal
machine. Both paths check the claim profile's Accept rule and require the root
machine in the same team, except for verified Cloud placement. The App path
checks authority and lease again after provider I/O. Audit
failure returns no token. An upstream token minted before a refused audit stays
unreturned, within the existing external-token revocation residual.


## Integration amendment: scheduled origin and final trigger-writer cut

`Tenant.AdmitScheduledRun(ctx, CronSchedule, due)` is the trusted controller-tick
adapter. The persisted schedule is durable consent for unattended execution.
Its private transaction authority is `schedule:<id>`, not a capability or session.
`ArmedBy` remains attribution for the arming operation. It never fabricates a
live account or bearer. The admission subject is a service attributed to `ArmedBy`, so all of that
principal's schedules share the per-principal admission counter. The sealed
authority reference still identifies the exact schedule.

The transaction locks and rechecks the tenant, repository key and location,
pipeline, effective declaration, arming identity and timestamp, pause and declared
state. Mutable cursor and fire history do not change execution consent. A pending
team deletion refuses admission. App-managed schedules also require the current
same-team, unsuspended installation and repository automation consent. Admission
rechecks the origin after shared intake effects and number allocation, before
commit. A changed or removed origin returns no run and rolls back all effects.

`VerifiedScheduleIntake` supplies the persisted source through the existing
`IntakePolicy` resolver. Ordinary requests cannot manufacture that transaction
proof. Source identity does not establish a verified App event, default-branch
trust or automatic pool grants. The shared C4 policy still resolves settings,
ceilings and retention. Run-now may use a paused schedule, as before.

The shared intake seals the current source, effective arguments and schedule
origin, allocates the number, writes the owner queue and completion intents,
and applies run-rate guards. Its exact due key uses the schedule ID and UTC
RFC3339Nano instant. Repetition returns the original execution. A changed
execution declaration under the same key conflicts. The sealed authority ID
traces the run to its schedule instead of a legacy trigger environment column.

`Tenant.CreateTrigger`, `CreateTriggerWithRun`, `FindTriggerByIdempotencyKey`,
`createTriggerTx`, `Store.createRunTx`, both duplicate-trigger sentinel errors,
and controller `triggerIntake`, `triggerRows`, `triggerRunInvocation`,
`triggerReqGit` and `CronIdempotencyKey` are removed. The context-selected
`WithCreatingPrincipal` API is removed from production. Shared intake derives
actors from its committing authority. Reporting fixtures seed current-schema
rows through `teststore`, without acquiring executable authority.


Scheduled source release uses the sealed schedule authority ID without inventing
App event facts. If there is no ordinary subscription, the source transaction
selects the same tenant, numeric repository, pipeline and live schedule's
repository automation consent. It locks the installation before locking and
rechecking that relation. Contents-only minting, exact repository and extra IDs,
claim fencing and committing audit remain the existing source-release path.

## Detached request trace

`runspec.Request.RequestID` carries optional detached submission trace metadata.
The ordinary request seal, intake persistence and retry clone retain it. It
carries no authority and does not replace the intake idempotency key. No schema
column is added because it lives in the existing request JSON.

## Launcher Pod rejection amendment (2026-10-17, W8 integration)

`Store.RejectLauncherAwards` transactionally validates claims.launch ancestry
and the original award LeaseRef, then fails only its current plan/eval/work.
The launcher actor and launch_rejected edges are restricted to this exact live
award. Node failures use the common transition/billing/settlement path. Plan
failure uses the common run transition and owner settlement. Eval failure uses
the shared expansion-loss and eval billing paths. Ended original awards replay
without touching replacement claims. Forged original fences refuse.
## W1b child admission reconciliation

`Tenant.AdmitRun` checks child ancestry before sealing or inserting the new run.
A `(repo_key, pipeline)` already present in the same team's ancestry returns
`conflict`. A repeated ancestor ID also returns `conflict`, so malformed lineage
cannot loop. The walk uses committed `lineage_json` and tenant-scoped run reads.
A missing ancestor returns the normal not-found error and exposes no foreign
pipeline. Retry intake keeps its committed source seals and bypasses this new
invocation check.

Current child requests carry the source snapshot explicitly. The native process
client inherits it from its authenticated parent snapshot. Explicit repository
and ref inputs retain their own source facts. This does not implement the
client's still-missing cross-repository source resolution.


## W1b scheduled intake and concurrency transactions

Tenant implements C2's six `scheduleWriter` mutations. Automatic and manual
launches use W1's `AdmitScheduledRun(ctx, CronSchedule, due)` adapter and its
private schedule authority proof. There is one call to the shared `admitRun`.
The duplicate admission implementation, private intake selector and separate
schedule source-verification API are removed.

A stored arming consent or a current App installation and automation grant is
required for unattended execution. A raw declaration alone grants no execution.
App schedules also
recheck live same-team installation and repository automation consent. API/local
registration retains its request template and arming authority in
`cron_schedules.consent_json`; those registrations recheck that authority and its
membership, including replay. `ArmedBy` supplies attribution through the schedule
service principal. It never creates a credential or a session.

Replacement locks the tenant and commits declarations, captured inputs,
withdrawals and caps together. It preserves IDs, pauses, cursors, history and
overrides. `CronOverride.TZSet` preserves an explicit empty zone. Local
registration prepares pins and environment snapshots before the transaction.
Its request retains the captured environment, checkout and configured entrypoint.

`ScheduleSourcePolicy` resolves source intent before admission. The final
transaction locks and rechecks the declaration and saved inputs, then uses W1's
source proof through the existing intake policy. API/local due instants must
match the effective cadence and store clock. Manual runs can launch a paused
schedule without moving its cursor. Run, sealed request, queue unit and fire
history commit together.

Shared intake scopes schedule replay by tenant and schedule authority ID while
preserving `ArmedBy` for attribution and rate guards. Automatic keys include the
exact UTC due instant. Manual keys occupy a separate namespace. Replay retains
the first revision after a followed branch moves and still checks current
consent. Changed pinned revisions or execution inputs conflict under one key.
Replacing the armer cannot admit a second run for an already committed instant.
The tick reloads the tenant's stored row before passing it to the writer, so
caller-supplied schedule fields cannot replace persisted intent.

`Tenant.MayStart` owns logical admission for every execution placement.
It reads sealed declarations and actual job awards, grants complete job sets,
and preserves reservation identity and queue arrival independently of owners.
Store release transitions commit plan admission with release permission.
The public queue API retains group reads and member priority control. Single-group
mutation and resolution routes are deleted. `ConcurrencyStore` is a tenant read
port. Claim group reads preserve contention accounting while removing memo keys
and leader references outside the declared read set.

Proofs use real SQLite, controller routing, source resolution and sealed intake.
They cover concurrent due replay, consent revocation, membership loss, source
resolution races, atomic cap refusal, empty override values and both local and
Cloud scheduling. The real api.sock test is
`TestNativeLocalControllerScheduleFiresOneSealedRunAtDueInstant`. Its in-memory
transport counterpart runs the same assertions. Native evidence belongs to the
lead harness.

Open integration points: following a private remote branch needs audited
controller credentials during revision lookup. Pinned App schedules use the
existing post-admission source release. Box groups use only `ExecutionHost` sealed
on the authenticated award. Launcher awards wait for activation to seal that
identity. Other awards without it are refused. Memo requests need accepted
namespace metadata. These gaps remain visible for the lead's launch integration
work.


## W1b review integration, 2026-10-17

Slot mutations take the admission key, controller lease, current authority chain,
the bound unit's owner row, sorted runs and nodes, then the group. Owner locks
use the current capability's UnitKind, including plan for controller dispatch.
Group and holder reads use a read-only snapshot without admission or row locks.
Release ignores the caller's outcome for inheritance. The unreachable legacy
coalesce waiter deletion is removed. Real-store cross-team leader resolution
returns 403 without disclosing outcomes.

Scheduled intake locks its schedule FOR UPDATE before taking the intake lock.
An aborted transaction does not write fire history or consume its due instant.
The source and authority snapshots are rechecked at the committing boundary.
Both schedule caps derive from internal/limits. Local principals retain their
cap exemption.

Unpriced job refusal now uses the existing node state owner. C1 adds the
claimant actor and unpriced edge for an unclaimed pending or ready job. The edge
also requires the private transaction proof carrying the failed poller's matching
inputs. A public TransitionNode caller cannot manufacture it. Its guard rechecks live claimant ancestry, repository and unit scope, metered placement,
matching policy and the current price table. Only a still-unpriced job can fail
with unpriced_cpu_class. Claim issuance rolls back before that guarded edge,
so no award, attempt or charge survives. Named and queued claims check the exact selected size price before award. Pricing refusal does not limit unmetered self-hosted work.
The guard is exercised by generated foreign, missing, revoked and repriced cases.

IntakeQueueDepth reads the tenant queue from the same admission transaction.
The controller's existing flood policy runs after C1 replay lookup. This removes
nested store reads while holding the intake transaction and preserves replay
and retry semantics.


## W1b sealed inheritance and capacity legacy cut, 2026-10-17

`runspec.Lineage.RepoInherited` retains whether the child caller omitted its
repository override. The native client supplies that intent beside the parent's
captured Git and Source. `AdmitRun` requires a parent when the bit is set and
checks that its repository key matches that parent's committed request. The real
intake policy still checks current source scope. The bit is sealed, participates
in idempotency, survives retries and feeds run/trigger reporting. No SQL column
or execution authority is added.

The capacity core no longer implements inherited-holder IDs, synthetic zero-cost
holders, recursive supersession or cost transfer. Both production entrypoints
already refused inherited-holder IDs. The HTTP decoder now rejects the removed
selector. Accepted declarations, bound leases and their state transitions own
capacity continuity.

The schema already refuses coalesce waiter rows. Its unused admission and
promotion paths, follower draining, cached acquisition outcomes and constants
are deleted. Memo result selection remains in the blob/result transactions.
Coalesced source requests are refused. The remaining cache/leader fields and
leader-read redaction path are removed with durable admission.

`TestConcurrencyHTTPRejectsRetiredInheritedHolderSelector` replaces the obsolete
inheritance surface tests. Weighted admission, backfill, cancellation, promotion,
owner-loss continuity and real result materialization retain their tests. The
native harness still owns PostgreSQL and socket verification.

`internal/store/capacity_adoption.go` and reservation-generation adoption are
removed. `capacity_admission.go` owns work identity, durable arrivals and permits.


## W1b committed output reporting and retired readers, 2026-10-17

`Node.OutputDigest` replaces `Node.OutputRef`. GetNode and ListNodes select the
exact committed output record under that tenant, run and job. A missing record
has no digest. A malformed or foreign selector fails closed. Receipts hash these
digests for executed and materialized results. No physical object key is exposed.
The obsolete OutputRef type, output-prefix helpers and writers for the deleted
node_outputs/data_objects tables are removed, along with direct-upload key and
SHA validators with no callers. Current record and upload authority is unchanged.

CreditRateTable.ClassForLabels uses the fixed catalog label's exact credit class. The unused
ExecutorResource alias and nodeChargeTx/profile-fallback reader are removed.
Billing prices the selected Cloud size label for the awarded unit. Later profile
pins cannot reprice it. Storage inventory's unused lockTeamStorageTx and the
unused legacy NodeLogSealGrace constant are also removed. The current blob,
retained-storage and log transactions remain their respective writers.


## W1b free grants and unused writer retirement, 2026-10-17

The free-grant transaction cannot create paid credit or a payment-linked reversal.
The duplicate raw reversal validator and writer, unused grant-kind helper and
reversal overflow sentinel are removed. Provider settlement and ConsoleRefund
retain payment identity, payer selection, replay and conservation.

The unused Tenant.AppendEventOnce and Operator.DropCardAttempt writers are
removed. The obsolete reportCardRefundTx callback adapter and its fixture API are deleted.
Production refund callbacks use the verified-event transaction.
Its exact refund mutation owner, applyCardRefundTx, remains shared with the
collection worker. No persistence schema, protocol or runtime setting changes.

MarkCardRefundMade binds the provider response and consumes any matching
committed refund receipt in the same ledger transaction. The receipt is selected
by configured provider mode, refund object, tenant and payment identity through
the existing payment_events object index. A successful receipt completes the
queue and wins over a stale failed worker response; failed or canceled receipts
are final and listed for the operator, never retried. Repeated binds cannot
repeat that transition. The verified callback owns the financial reversal.
No second callback authority or refund ledger is introduced.

TestARefundOutcomeBeforeItsBindIsKept now uses provider settlement with a real
committed collection correlation. TestRecordedRefundCannotBindAnotherAssociationProperty
generates foreign tenant, mode, payment and refund associations.
TestRecordedRefundBindingRollsBackWithItsOutcome proves atomicity when applying
the stored outcome fails. The native harness retains PostgreSQL execution.

## Environment provenance integration

`runspec.Request.EnvironmentSource` seals `captured` or `submitted` independently
of invocation origin. Captured snapshots stay in private local host storage.
Submitted values use `runs.submitted_env` and its existing bound cipher. Intake
rejects submitted values attached to captured provenance and refuses nonempty
environments without known provenance. Child intake copies its parent's
environment and provenance in the ancestry transaction. A supplied snapshot or
provenance that differs from the parent fails with `spec_drift`. Retry retains
the original seal. Invocation origin never selects environment storage.


## Durable admission

`Tenant.MayStart` owns job admission in the controller or local store. It accepts
only the work binding and authenticated job award. Owners propose releases through
ready transitions and `ReleaseEvaluation`. The store commits plan admission with
permission to release. Each complete group set grants atomically with one arrival.
A wait holds none of that set, and a policy refusal settles through the existing
node and run verdict owner. A job never reacquires its parent plan. The ledger
creates and locks each group entry before locking its holders. A first arrival
that found no entry follows the same order after inserting or losing an insert
race. Lease bounds use the transaction clock after that lock is held.

Concurrent admission tests enter through accepted declarations and authenticated
awards. Per-key accounting fixtures cannot model this boundary: reaping discards
the work's whole group set under the admission owner. Barrier snapshots check
complete sets, capacity, live-waiter FIFO and settlement while admission, execution
and maintenance race on both store dialects.

Schema baseline 20 includes `admission_work`. Run/job identity and arrival survive
owner handoff. Its generation and holder bind a start permit to the actual award.
Every start requires a permit, including jobs without groups. Process delegation
reads the same permit with its running attempt in one authentication snapshot.
Logical reservation generations do not need adoption or rewriting. Claim or
owner liveness preserves continuously retained arrival. Expired leases never revive.
Queue timeouts apply only to ungranted work. Replaying a live complete grant
retains its permit and arrival after the queue deadline.

`StartGate` is a decision client followed by optional machine compute. It derives
no groups or policies. The store alone derives accepted keys and the award's box
identity. Plan/job namespaces and the prohibition on plan box scope remain.
Burst awards are provisional until compute starts. Unavailable compute relinquishes
the award and job reservations without resetting readiness ranking.

Approval-only predicates and expansion callbacks receive store-issued evaluation
permission. They hold no job groups. Approval callbacks hold no compute. Their
typed outcomes commit under the exact source and live owner/evaluation fence.
A predicate skip remains skipped. Controller-owned approval predicates use the
same evaluation-unit lifecycle as generated work. A shared accepted-job predicate
distinguishes approval evaluation from generator expansion when deriving output,
secret and pipeline-reference read permissions. Approval evaluation retains its
declared `Needs` and data grants on both placements, with the exact evaluation
fence still required. `PipelineReferenceTarget` carries only the referenced
pipeline and job, independently of the caller's run binding. Process routing and
controller reads use that canonical target. Expansion cannot borrow those approval
permissions.

The simulation models claim, owner and per-level capacity deadlines independently.
Claim loss alone cannot reset arrival or waive FIFO. It asserts each expected
complete reservation set even when no rows are returned. Production `MayStart`
fixtures replace the retired single-group test allocator. The source-writer check
is architectural lint; behavioral permit and complete-set tests prove execution
enforcement. Retired coalesce cache/leader fields and their redaction path are
removed from requests, queue responses and the schema.

## Launcher execution host sealing

Launcher awards have no `ExecutionHost` when claimed, while waiting for capacity,
or when a node is merely reserved. Only the launcher's authenticated activation
of that execution reservation seals `node:<Node UID>` on the live bootstrap
award. The transaction checks the award's ancestry, unit fence, lease and existing
host. Replays preserve the host. Another node is refused. Bootstrap exchange
copies this sealed identity to the execution credential.

Box admission reads only the persisted award identity. A launcher award awaiting
activation queues without creating a provisional concurrency key or holder.
An award without launcher ancestry and without a host refuses admission.
Capacity Pod UIDs and provisional Pod placement never supply a box identity.
`TestLauncherCapacityWaitDoesNotSealExecutionHost` covers capacity waiting,
reservation, activation, exchange and subsequent reconciliation through the
launcher, controller and store. The Actions, account-backed here and unbound
machine regressions retain their placement identity coverage.

## Plan continuity after release

The admission module derives plan reacquisition policy from committed release
events. Once work has been released, plan groups queue without a timeout under
every original on-limit policy. Initial admission still applies the accepted
policy. Controller settlement processes terminal work while plan capacity is
unavailable and withholds further release. Read-only group and queue views show
`waiting to reacquire <group>` for that run. The controller continuity-lapse
regression covers running-job completion, contention and subsequent release for
queue, skip, fail and cancel-others. The native-owner regression covers the same
release boundary with compute enabled and disabled.

## Fenced completion and transaction clocks

Successful transaction construction captures database time. Failure to read that
clock aborts construction before any transaction body runs. Read transactions
keep that instant and one database snapshot. Mutation authority and lease checks
read fresh database time after lock waits or policy work through `storeTx.now`.
Claim funding and final lease bounds read time after issuance, including any
billable intervals it opened.
A failed clock read poisons the transaction: later SQL returns that error,
commit rolls back, and transaction callbacks cannot hide it behind success
or a different refusal. Mutation transactions expose no stored snapshot
timestamp, and lifecycle guards accept no caller-supplied time.
Transaction cursors must close or reach exhaustion before another statement or
clock read. The transaction owner enforces this for both database dialects;
a violation poisons the transaction and rolls back its writes. Pending single-row
queries also retain their cursor until scanned. Retained-storage sampling reads
its database clock before opening the inventory cursor.
Lifecycle, identity, billing and schedule mutations
use store time instead of a caller clock. Schedule due times and provider event
timestamps remain domain facts.
Capacity decisions read time after waiting for locks, before counting holders
or promoting waiters.
Blob lookup budgets read elapsed database time through one budget check. They
do not reuse the authorization snapshot. Cache lookup exhaustion remains a miss.
Browser flow expiry and recent sign-in checks use the same snapshot as the
session authority read.

Runs, nodes and triggers store a generated `drain_deadline_at` from their first
`cancel_requested_at` plus five minutes. The database derives it in the same
write, including concurrency supersession. Cancellation clamps existing work and
slot leases. Renewal uses `leaseBound` for the authority, cancellation and funded
bounds. The schema baseline is version 22.
The shared capacity writer applies the bound to each holder and waiter,
including controller reconciliation.
Node capacity follows the later live claim or owner lease; plan capacity follows
the latest owner or claimed-job lease because jobs can outlive their owner during
recovery. Cancellation still bounds the whole run, and a live owner cannot extend
a metered node's funded horizon. The holder's own lease must remain continuous;
renewal never revives expired capacity.

`Tenant.TransitionNode` accepts the closed `NodeCommand` interface. `StartNode`,
`RetryNode`, `ReportNode` and `ReleaseNode` carry the captured attempt. Start,
retry, ready, release and cancel commands fix their destination. Approval timeout
reads its verdict from the persisted policy. Each command exposes only its own
payload. `BindNodeCommand` supplies authenticated transport authority without
changing the operation. The private normalizer retains the transition table as
the single state owner. Controller and local transports decode through
[`store.NodeCommandFromPayload`](../../../internal/store/node_commands.go), which rejects fields that the selected command cannot
carry. Materialization and publication command types remain private to the store.
Node rows and execution inputs keep claim generations and
attempt ordinals distinct. Actor generations and command source attempts use
those same types. Lifecycle commands retain typed run and node identifiers.
Holder actors carry complete `NodeFence` values, and owner references carry
`EvalFence` values. Guards compare the captured target and generations together.
Run completion carries no requested outcome. Admission refusal first persists
its job outcomes and resolves expansions, then derives the run verdict through
the shared scheduler. Existing required-job failures retain their effect.
Completion returns the committed outcome and reason, which the host reports
and the foreground CLI uses for its exit status.


## Intake funding and launcher faults

Funding admission uses the sealed placement and team billing policy. It never
consults runner registration, polls, observed labels or current occupancy. A
policy that permits unmetered execution leaves an unfunded push pending;
Cloud-only placement or an exclusive Cloud ranking checks credit standing,
including prepaid policy and holds, before admitting executable work. A
refused push remains terminal history and replay retains that result.

Launcher faults isolate suspect nodes and report their identities to the operator.
Healthy nodes keep serving the team. Reservation and binding metadata use one
validation predicate for reconciliation and placement, including resumption.
Damaged metadata keeps a node excluded even if quarantine cannot persist.
Quarantine writes require the recorded team to match the protected team label in
the launcher's namespace. Every retry retains and checks the original node UID
and protected team. A replacement or ownership change refuses the write.
Operators can stop a team's new claims and waiting placements with `--stop-team`
on every launcher.

## Sealed masking projection (decision 43)

The accepted run seals `Resolved.SecretRegistration`. It retains secret defaults
from every discovered pipeline, selected list argument kinds, and the complete
raw/default/effective/item registration set. `BuildSecretRegistration` is the
single derivation owner. Seal validation recomputes the projection and refuses
drift. Accepted expansions extend this projection privately for generated args.
Controllers and execution hosts use it rather than infer list semantics from
joined values. Display receipts continue to use the accepted spec's latest secret
classification.

Stored secrets, supplied secret arguments and nonempty defaults of the selected
pipeline require at least four characters, including each nonempty normalized
list item. Empty and unselected defaults do not refuse a run. Short derived
values are skipped. Secrets containing `***` are accepted. Plain config variables
remain available for shorter values.

## Shared execution and reporting projections

[`store.RunRow`](../../../internal/store/state_contract.go) embeds
[`runstate.Execution`](../../../internal/runstate/transport.go) beside private
admission metadata. Host snapshots use that execution value directly.
[`store.Run`](../../../internal/store/store.go) embeds
[`runstate.RunSummary`](../../../internal/runstate/reporting.go) beside its
private team and plan snapshot. Handlers serialize the summary, so persistence
metadata cannot enter the response through a missing field mapping.

## Asserted retry provenance

Every retry preserves its source seal, including the writer, in both full and
failed modes. This includes asserted account, run and armer writers. The retrier
is recorded only in execution lineage as the actor who requested the retry.
Personal placement, owner-machine checks and cache and memo authority read the
same sealed writer because that principal chose the code.

Dependency terminal transitions require the outcome that `Decide(graph)` returns
for that node in the committing transaction. An owner cannot bypass an approval
by asserting that its dependencies require a skip.
