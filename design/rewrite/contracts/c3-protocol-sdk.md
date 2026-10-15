# C3 protocol and SDK contract

This is the implementation contract for node protocol 2, both author SDKs,
the fake host, and the shared host library. It follows the frozen architecture
and decisions 87-104. Declarations compile, but bodies marked
`panic("unimplemented: c3-protocol-sdk")` do not provide runtime behavior.
The capability and layer tables contain the complete target data.

## Ownership

| Concern | Owner and source |
|---|---|
| Hello, operations, frames, describe, flat plan and shared validation | `internal/nodeproto/*_contract.go` |
| Windows inherited-handle argv spelling | `internal/protofd/transport_contract.go` |
| Run and node host roles, layer order, event-following reconciliation | `internal/nodehost/host_contract.go` |
| Pure graph decisions and consistent snapshot projection | `pkg/store/scheduling_contract.go` |
| Every unit's renewal loop | `internal/lease/keeper_contract.go` |
| Checkout and entry acquisition | `internal/source/acquire_contract.go` |
| Plan cache identity, eligibility and validated hits | `internal/nodehost/plan_cache_contract.go` |
| Exact engines, verified releases and signed version policy | `internal/engine` |
| Go declarations and services | `sparkwing/sdk_contract.go`, `sparkwing/inputs/memo_contract.go`, retained API inventory below |
| Author test host | `sparkwing/sparkwingtest/host_contract.go` |
| TypeScript author API | `sdk/typescript/index.d.ts` |

The detailed inventories are part of this contract:

- [Protocol inventory, wire rules and validation](c3-protocol-notes.md).
- [Shared controller API and engine compatibility](c3-controller-wire.md).
- [Engine resolution, signed version policy and upgrades](c3-engine-version.md).
- [Go SDK inventory, including retained and removed exports](c3-go-sdk-notes.md).
- [TypeScript export and member inventory](c3-typescript-notes.md).

C1 owns state transitions, schema, accepted spec storage and blob authority.
C1 owns `ReconcileNode` and `AcceptExpansion`. C3 hosts consume those exact
signatures. Both run owners project `RunSnapshot.Scheduling()` and call
`store.Decide`; the store remains the only state writer.
C2 owns the canonical error object, code registry and credential authority.
`nodeproto.ErrorUses()` inventories typed canonical codes and their reasons.
`apierr.DecodeInner` validates every structured protocol error. C4 owns environment policy, settings,
numeric limits and deadline calculations.

Existing colliding identifiers have an `Old` suffix and existing callers use
them so the old runtime still compiles. They are scaffolding to delete during
implementation, not aliases or a supported compatibility path. The target
wire has only version 2. No migrations, dual registration or old-wire adapter
are part of this contract.

## Host and store export inventory

| Export | Purpose |
|---|---|
| `nodehost.AcceptedRun` | Carries canonical accepted spec/plan bytes and their sealed hashes. |
| `nodehost.RunInput` | Gives a new or resumed owner the sealed request and existing acceptance. |
| `nodehost.RunCompletion` | Reports the verdict after atomic run/unit finish. |
| `nodehost.RunHost.Run` | Runs acquire through completion for every run-host placement. |
| `nodehost.OwnerHost`, `OwnerStore` | Coordinate renewal, planning, acceptance, scheduling and atomic finish through the Tenant contracts. |
| `nodehost.OwnerPlanning`, `OwnerLayerResult` | Supply checkout planning inputs and owner policy results without replacing intake or claim authority. |
| `nodehost.GeneratorReplay` | Supplies recorded source output for code-only reconstruction. |
| `nodehost.ClaimAuthority` | Refers to authenticated executor authority without granting it from a request body. |
| `nodehost.NodeInput` | Binds an attempt to accepted spec, node, generation, runner and route bearer. |
| `nodehost.NodeHost.Execute` | Replans, checks drift, runs executor layers and reports through store transitions. |
| `nodehost.NewNodeExecutor`, `ExecutorDependencies`, `ExecutorProcess` | Connect the common executor to the fenced store, matched engine, SDK process and storage adapters. |
| `nodehost.LayerSide` | Distinguishes owner decisions from executor work. |
| `nodehost.OwnerSide`, `nodehost.ExecutorSide` | Name the two layer owners. |
| `nodehost.LayerSpec`, `nodehost.LayerTable` | Declare the complete outer-to-inner envelope and retry scope. |
| `nodehost.Reconciler.ReconcileNode` | Follows a holder and repeats atomic reconciliation after terminal or requeue events. |
| `nodehost.ExpansionStore` | Gives owner/eval hosts the Tenant's expansion persistence boundary. |
| `store.ScheduleNode` | Projects accepted graph and lifecycle facts into the pure decider. |
| `store.ScheduleExpansion` | Preserves generator-to-source/member mapping, including empty expansions. |
| `store.ScheduleSnapshot` | Supplies a consistent graph, expansions and cancellation state. |
| `store.DecisionKind` | Names graph intent without granting permission to write state. |
| `store.DecisionReady`, `DecisionApproval`, `DecisionTerminal`, `DecisionCancelHolder`, `DecisionExpand` | Enumerate every graph action. |
| `store.NodeDecision` | Carries one job action with its generation, outcome and reason. |
| `store.ScheduleDecision` | Returns ordered graph actions and an optional final run verdict. |
| `store.Decide` | Exposes the single pure `decide()` to run hosts. |
| `store.RunSnapshot.Scheduling` | Projects accepted declarations and persisted state, including empty generator groups, into `ScheduleSnapshot`. |
| `store.ReconcileRequest`, `store.SourcePolicy` | Carry typed resume, retry, memo or coalescing source authority. |
| `store.ReconciledNode` | Returns the persisted node and any committed claim or authorized source. |
| `store.Tenant.ReconcileNode` | Chooses, self-claims or materializes in one transaction. |
| `store.ExpansionSubmission` | Carries source generation, server-derived actor and exactly one success body or structured failure. |
| `store.AcceptedExpansion` | Returns persisted nodes, body hash, barrier state and replay status. |
| `store.Tenant.AcceptExpansion` | Validates and inserts generated jobs or records structured failure in the same transaction. |

## Lease and source export inventory

| Export | Purpose |
|---|---|
| `lease.Beat` | Delivers cancel and watch demand on the normal renewal reply. |
| `protofd.HandlesArg` | Names the Windows-only argv value containing decimal request/frame handles. |
| `lease.Keeper`, `lease.Keeper.Run` | Renew any unit and return a cause-aware context plus a joining stop function. |
| `lease.CancelError`, `lease.CancelError.Error` | Distinguish requested cancellation from loss of authority. |
| `lease.ErrLeaseLost` | Stops execution after an authenticated renewal rejection. |
| `lease.ErrSilent` | Stops execution after a full lease without a successful renewal. |
| `source.Spec` | Projects sealed repository identity, exact SHA and checkout policy for acquisition. |
| `source.Access.GitCredential` | Obtains live, audited git authority from the credential owner. |
| `source.Access.BundleURL` | Obtains a signed URL for an authorized source-bundle seed. |
| `source.Credential` | Carries host-bound App, HTTPS or SSH material in memory. |
| `source.Options` | Supplies host workspace and source access separately from sealed source data. |
| `source.EngineResolver`, `source.EntryInstaller` | Resolve the pinned engine and install the SDK entry with that engine and an explicit install environment. |
| `source.Entry` | Returns acquired checkout, exact commit, resolved default branch and SDK argv. |
| `source.ForRequest`, `source.ForExecution` | Project intake acquisition inputs or the accepted execution checkout policy without rereading mutable settings. |
| `source.Acquire` | Acquires source and builds or resolves the SDK entry through one placement-independent path. |
| `source.Entry.Close` | Releases owned resources without removing the user's checkout. |

## Host invariants

1. The same lifecycle handles new and resumed runs. The request seal is immutable.
   First planning resolves checkout facts, invokes describe, merges settings and
   submits the second spec half, restricted checkout declarations and plan together.
   `Spec.Declarations` preserves both project layers for store-side resolution,
   expansion and retry. No job runs before AcceptPlan.
   Resume uses accepted bytes and hashes. It does not reread mutable settings.
2. The store owns every lifecycle event. Hosts request transitions and emit
   ordinary logs only. A refused output, memo-copy write or terminal transition
   is an error, never a successful result accompanied by a warning.
3. `decide()` performs no I/O, clock reads or mutations. Both owners call it.
   Terminal upstreams determine readiness. Optional dependencies may be absent.
   Continue-on-error permits downstream release. Optional jobs do not fail the
   run. Recovery edges may consume their failed source's recorded output.
   Nonmatching recovery jobs skip. Generated recovery jobs follow these rules too.
4. Every declared generator has a `ScheduleExpansion`, even if it accepts zero
   members. `needs_generated` names generator IDs. Its source and complete member
   set come from this projection, never inference from whichever child rows exist.
   Any expected expansion prevents final run settlement. A successful or memoized
   source schedules expansion from its recorded output. Failed, skipped,
   cancelled or superseded sources resolve to none in the source transition.
   A failed generator settles dependents as upstream_failed. Run cancellation
   resolves all expected expansions and requests cancellation from live holders.
5. Reconciliation commits its choice before execution. It keeps terminal rows,
   waits for a current holder, self-claims eligible work or copies an authorized
   source. Event following starts after the returned state sequence and reconciles
   again after requeue. A still-running holder's old result is never terminal proof.
6. A node-wide attempt ordinal never resets when generation changes. Application
   retries advance the ordinal under the same claim and generation. Only a lapsed
   lease releases work. Started lapses consume the separate disruption allowance.
   OOM is an ordinary job failure governed by application retry policy.
7. The layer table is authoritative: debug, skip_if, approval, memo and admission
   belong to the owner. Artifacts, caches, retry and timeout belong to the executor.
   A local in-process execution applies both sides once. A dispatched executor
   applies only its side. A launcher pod may evaluate owner closures, but the
   controller records their decisions through the same transitions. Bounce,
   worker-cap and retry-auto layers do not exist.
8. Executor attempts replan only to reconstruct code and verify accepted hashes.
   Generated jobs replay their generator chain from recorded outputs for the same
   purpose. Replay cannot add, delete or change persisted topology. A mismatch
   fails as plan_drift before the body starts.
9. AcceptExpansion accepts all generator groups of a source in one body containing
   jobs only, or a structured failure with no body. Each job names exactly one current source generator in its groups.
   Enforce the 256-job limit per generator, not over the combined source body.
   Empty groups remain derivable
   from the accepted source's generate declarations. The transaction rechecks
   live owner/eval authority, terminal source generation, cancellation, the
   expected state and all graph/secret rules. An identical accepted submission
   replays, while a different body conflicts. Retry replay still needs live
   authority. Specs and hashes commit with insertion and accepted state.
10. A lease rejection cancels run and node execution alike. 401, 403 and
    lock_held mean lost authority. A generic 409 is not automatically cancellation.
    429/503 honor Retry-After without renewing liveness. Watches never shorten the
    owner heartbeat. Stopping a keeper joins its loop. Renewals keep bound
    concurrency holders live. Capacity whose lease lapsed cannot be revived.
11. Source acquisition trusts sealed identity and exact SHA, not today's branch
    head. It discovers origin HEAD only when DefaultBranch was absent. A bundle
    only seeds a verified checkout. Credentials are bound to their host.
    SSH uses the confirmed KnownHosts value. ExtraRepositories limits submodule
    reads. Kind is github_app, https or ssh; empty means owner credentials, whose
    use is fenced by the runner repository allow-list. Team release requires the
    credential owner's cloud/opt-in and audit checks. Install tools get base env.
12. Raw canonical spec/error JSON is a transport boundary, not another schema.
    C1's spec decoder and `apierr.DecodeInner` validate it. Source projections and
    scheduling snapshots come from trusted host/store adapters. Capabilities,
    holders and generations supplied by an SDK or arbitrary HTTP body grant no
    authority. The store derives binding from authenticated capability records.

## Planning cache

C3 owns the default-enabled `nodehost.PlanCache`. `PlanCacheInputs` includes the
team/repository scope, exact engine/SDK, entry and source-tree digests, project
settings, merged args, environment, checkout policy, describe result, secret
registry version, granted runner and complete SDK-visible run context.
`PlanCacheIdentity` canonicalizes that input with explicit presence. A changed
input changes the key. An input whose dependency set cannot be established
bypasses the cache and plans normally.

The complete run context includes run IDs and timestamps because the SDK can
read them during planning. This conservative key permits reuse only when those
inputs also match. Cross-run reuse needs an explicit SDK guarantee before any
of those fields can be omitted. Key digests stay private, because hashes of
secret-bearing inputs can reveal guesses.

`PlanCache.Lookup`, `Put` and `Invalidate` own persistence. Entries hold raw
validated declarations and their canonical hash, never another run's accepted
seal or authority. Missing, corrupted, stale or unreadable entries are misses
with a reason, followed by normal planning. There is no new cache-age cutoff.
`ValidatePlanCacheHit` checks the key/digest and reruns `ValidatePlan` with the
current capability table and secret matcher. It returns `ValidatedPlanHit`.

Every hit still enters `AcceptPlan` with checkout-derived facts. The store
reruns validation, settings resolution, operator ceilings, authority and secret
preflight and commits seal point two. A hit bypasses SDK planning compute, so
it has no planning charge. Cache writes happen only for validated initial
plans; generator expansions remain under their own transactional barrier.
`PlanningInput` supplies this dependency to every placement's planning path.

`PlanInitial` connects cache lookup, validation, invalidation and fresh planning.
Its `PlanningFunctions` use the shared validator and canonical hasher by default.
`PlannedInitial.CacheHit` records whether SDK planning ran. Every result still
passes `AcceptPlan`. Cache diagnostics omit input hashes and declaration contents.

## Host adapter boundaries

`OwnerHost` waits for its first successful renewal before acquiring source.
Acquisition receives the persisted acceptance on resume, even when the caller
omitted it. Checkout resolution cannot replace the stored intake request.
Owner policy tasks run independently so a waiting approval or admission does
not stop ready siblings. Completed executor errors are checked against persisted
terminal state because a committed failure must still release recovery jobs.

The host implementation accepts the declared store, validator, hash and engine
interfaces so their independent implementations can be tested with fakes.
This does not complete the native placement adapters. Those adapters must supply
SDK process launch, source installation, environment construction, owner policy
evaluation, blob storage, route delegation and a state follower that catches up
from persistence before waiting. The store owns the scheduling projection and
decider. No host adapter may substitute its own readiness policy.

Owner operations need cross-area contract completion. Remote result reuse
needs a materialize-only reconciliation choice because a cache miss must remain
available to the selected remote executor. The run-host actor also needs an
authorized route for `DecisionCancelHolder`; the node transition table reserves
that edge for controller, operator and reaper actors. These paths return errors
until their authority and transaction contracts are supplied.

The controller wire reconciliation response must carry enough persisted state
to reconstruct `ReconciledNode`, including claim identity, retry consumption,
job spec and execution settings. Expansion responses must distinguish newly
inserted rows from replayed rows with subsequent lifecycle changes. The host
cannot infer those facts from public job details or declaration bytes.
Recovery execution also needs a store reader for the source's persisted failure
stage and masked structured error. `ExecutorDependencies.ReadFailure` is the
host adapter for that reader. It must not synthesize a failure from a status string.

Decision 104 supports TypeScript on Windows. Both SDKs use hello for the bearer
and argv for Windows protocol handles; no preload or platform refusal remains
in the target contract.

## Acceptance scenarios

T1 should drive these through its real boundaries. These are required behavior
tests for implementations, not claims that the declaration stubs execute.

| Scenario | Architecture | Evidence |
|---|---|---|
| All six placements accept and refuse the same corpus | §3.2, §6.3, A27 | Every capability cell, graph rule and limit returns the same structured verdict. |
| Go/TS hello and operation corpus | §6.1-6.5 | One negotiation, secret-free env, strict plans, additive frames, common error codes. |
| Secret-containing identifiers | Decision 90 | Initial/expanded job, step, group and approval IDs fail without echoing the secret. |
| Started holder loss and live remote work | §3.3-3.4, A60 | Ordinal survives generations, owner resumes, retained capacity stays held. |
| Terminal, live-holder and requeued reconciliation | §3.3 | No duplicate execution, result adoption or lifecycle event. |
| Success/memo/empty/nested/failed expansion | §3.6 | Stable generator mapping, barrier resolution and deterministic code-only replay. |
| Expansion replay and cancellation race | §3.6 | Same body replays, changed body conflicts, stale/cancelled authority writes nothing. |
| Lease cancellation, rejection, silence and shedding | §2.4 | Exact cause, no liveness extension on 429/503, joined cleanup, watched jobs delivered. |
| Direct source across placements and owner loss | §2.5, A15 | Exact SHA, retained default-branch trust, SSH/App/HTTPS, authorized submodules and bundle seed. |
| Run and node admission adoption | §3.7 | Only continuously counted holders/waiters retain capacity or arrival order. |
| Artifacts, memo and directory-cache parity | §8.1 | Integrity, existence and reference-edge checks; unrelated-job digest refused. |
| Author SDK and fake-host fixture | §6.6-6.8 | Every retained/new API compiles without runtime plumbing and Go/TS plans agree. |
| Windows and cleanup | §5.6, §6.2 | Handles travel in argv, child tools inherit none, private files use fssecure. |

The protocol and SDK inventories specify finer test cases. Schema generation
must read the v2 declarations, snapshot the wire, and compare both SDKs against
those schemas. Generated documentation is updated with the implementation cut.
The old generated reference is not proof of the target API.

## Cross-area assumptions

| Other area | C3 type and required agreement |
|---|---|
| C1 | `store.Tenant.ReconcileNode` and `AcceptExpansion` are the sole transactional APIs. Both owners use `RunSnapshot.Scheduling()` and `Decide`. |
| C1 | `runspec.Request` serializes into `RunInput.SealedRequest`; the accepted spec serializes into `AcceptedRun.Spec`. Hashes are canonical C1 hashes. `source.Spec` is a derived projection, never a second stored spec. |
| C1 | `ReconciledNode.Node.Attempt` and `NodeInput.Attempt` are node-wide ordinals. `Generation` is the claim fence. Expansion source generation is immutable at terminal. |
| C1 | `MaterializeSource` must pass blob existence, integrity and read-set authorization. C3 does not mint a receipt or expand a read set. |
| C2 | `apierr.Error`/`apierr.Code` own the object and registry. `Reply.Error`, `JobFailure.Error`, `RunResult.Error` and `EvalResult.Error` use `*apierr.Error`, validated by `DecodeInner`. Every typed `ErrorUses()` code belongs to that registry. |
| C2 | `ProcessBinding.Routes` contains a nodehost-audience bearer. `ClaimAuthority` and `store.Actor` refer to authenticated capabilities, whose binding/liveness C2 and the store recheck. |
| C2 | `source.Access` calls the live source-release policy. `lease.Keeper.Renew` errors expose canonical codes and Retry-After metadata. `Beat.WatchedNodes` encodes as watched_jobs to keep customer vocabulary. |
| C4 | `settings.ResolvePlan` returns sealed execution settings after `ValidatePlan`; both are mandatory inside acceptance. `lease.Keeper.Interval` comes from `limits.Renew`; renewal configuration passes `limits.CheckRenewal`. |
| C4 | `EnvValue.Startup`, SDK `Startup()` and TypeScript `startup()` agree with `procenv`'s fixed startup names. Attempt and cleanup env agree, and credentials never enter Build output. |
| C4 | `protofd.Arg` remains --sw-node-protocol; `protofd.HandlesArg` is --sw-protocol-handles followed by decimal request,frame on Windows. Both SDK transports parse that form and clear inheritance during initialization. Unix keeps fds 3 and 4. |
| T1 | Conformance drives final `nodeproto.Request`, `PlanDoc`, `ValidatePlan`, `RunHost`, `NodeHost`, `sparkwingtest.Host` and TypeScript's root declarations. Old-suffixed symbols test scaffolding only. |

## Open questions for the lead

- A28 removes approval hook methods while §3.2/A27 require approval-condition
  parity. Both SDKs retain SkipIf as a condition and omit executable before/after
  hooks. Confirm that reading before implementation.

## Verification boundary

Passed checks on the contract tree:

- Root module: `GOWORK=off GOFLAGS=-buildvcs=false go build ./...`.
- `go vet` on every new or changed package, including the separate `.sparkwing` module and explicitly named `testdata` fixture packages.
- TypeScript 5.9.3: `tsc --noEmit --strict --lib es2022,dom --skipLibCheck false` on `index.d.ts`.
- An external Go consumer compiled the renamed builders, memo inputs, conditional
  approval and fake-host Eval without importing an internal package.
- `commentcheck -base origin/main` and `git diff --check`.

The independent declaration review found four API gaps. They are corrected:
public fake-host Eval arguments, TypeScript step-group operations, generated
approvals, and enum/string-list input declarations. A subsequent helper-surface
pass covers retained Docker, git, service, command and filesystem helpers too.

Runtime acceptance, schema generation and dependency decoupling remain
implementation work. Panic stubs cannot prove those behaviors. No binary was
installed and no stateful pipeline was run for these contracts. The lead retains
commit and integration work.
