# C3 protocol and SDK contract

[Shared steps and alias callers](w3-ingress-lease-continuation.md#shared-steps-and-alias-caller-continuation)
replaces the retained step scheduler with the common graph driver and carries
alias selection through CLI parsing and the retained placement callers.
Native owner-policy and placement cutovers remain open.

[Runtime ingress and lease continuation](w3-ingress-lease-continuation.md)
connects the retained scheduler to raw-byte validation and common environment
projection and removes another claim-heartbeat loop. Native caller migration
and legacy runtime deletion remain open.

[Native assembly and router cutover](w3-router-native-continuation.md) joins the
owner and claimed-node host around one executor, transport, engine resolver and
environment builder. The registered service router uses the v2 routes and errors.
Native placements and the remaining legacy runtime cut stay open.

[Native grant handoff](w3-native-grant-continuation.md) passes the validated
revoker into native attempt and owner-evaluation startup. It ports real Go SDK
closure failures, carries persisted cancellation reasons and removes the separate
host-trigger heartbeat loop.

[Process authority](w3-authority-continuation.md) adds digest-based in-memory
delegation, fresh parent snapshots, bound service resolution and process
revocation. C2 router and native placement assembly remain open.

[Execution diagnostics](w3-diagnostics-continuation.md) adds committed step/retry
logs and the native Go step-policy acceptance component. Step windows and native
placement assembly remain open.

[Step persistence](w3-step-continuation.md) connects the common executor to C1's
fenced step writer and ports native step-state and dry-run assertions.

[Settlement and expansion continuation](w3-settlement-continuation.md) consumes
the amended step snapshots and concurrency context, resolves the working merge
contents and adds real Go SDK process proofs. Native runtime migration remains open.

[Services and process environments](w3-services-continuation.md) completes the
shared service registrar, separates pipeline aliases from registered entries,
and supplies complete native cleanup environments. Native runtime migration
and legacy deletion remain open.

[Checkout planning](w3-checkout-continuation.md) connects the common owner to
acquired project files, shared argument and guard resolution, and plan-dependent
settings for the second seal. Native entry-point migration remains open.

[W3 amendment implementation and remaining integration](w3-amendment-handoff.md)
records the current host boundary. Native placement assembly remains incomplete.
[Required artifact staging](w3-artifact-continuation.md) adds the verified read
boundary and its executor acceptance checks.
[Artifact publication](w3-publication-continuation.md) adds capture and the shared
artifact layer. Native placement and cache assembly remain open.
[Directory caching](w3-depcache-continuation.md) adds the executor depcache layer,
package-manager resolution and opt-in remote directory-cache reads.
[Memo verification](w3-memo-continuation.md) adds verified lookup/copy results
and remote memo reads. Owner-side source selection and atomic materialization
still need the C1 contract completions listed there.

## Amendment 1: shared host inputs

[Amendments 1](amendments-1.md) defines the required worker integrations.
RunInput and NodeInput now carry StorageBinding: verified byte storage,
transactional records, current authority and optional read-only remote cache.
Refresh the binding for each attempt/generation. The shared host layers use these
clients for artifacts, memo and depcache. No cache-service or ArtifactStore adapter
is restored. The SDK's declared Cache helpers remain author declarations, and
pipeline tooling uses their public workdir-safe keys.

Source acquisition consumes Source.Location from the request seal, separately
from repo identity and checkout policy. RemoteURL contains no credential and
remote acquisition uses the sealed SHA. LocalCheckout is authorized on the
machine whose store admitted it. PipelineDir stays inside that checkout.
Intake verifies that the location belongs to the sealed repository before any
credential release. Repo display text is never a clone location. Source.Options.Base feeds procenv
for Git and Install. Resolve the pin after acquisition, then call the selected
engine's InstallSDK. Accepted executions Ensure the sealed engine identity before
reconstruction. Host-version binaries and ambient runner identity are not substitutes.

W8's `runners.NodePreparation` constructs accepted executor input and acquires
source under `Hosting`'s keeper. Its default acquisition calls `source.ForExecution`
and `source.Acquire`. It preserves the cleanup returned with partial acquisition.
Bound host services supply the job hash, delegated routes, storage and replay.
The node-input constructor captures accepted seals, job settings, engine,
runner and claim identity before renewal or preparation. It owns their projection
for every preparation path and copies nested mutable settings. The preparer
acquires the accepted source and uses that constructor. `NewManagedRunner` composes one claim
client and award clients over the same HTTP transport with separate bearers.
Concrete shared host constructors and run/plan/eval execution identities remain
with W3.

For trigger and here awards, Hosting constructs RunInput's run ID, first seal
and accepted second seal before renewal or preparation. Prepared host services
supply storage, authority, engine resolution and plan caching. They cannot
replace the awarded seals or add an accepted plan to a new run. Trigger and node
preparation share the accepted-plan projection.

Reconciliation supports MaterializeOnly: an authorized cache/retry hit is committed,
while a miss returns Dispatch without stealing remote execution. Private execution
responses preserve declaration bytes/hash, settings, retries, fences, cancellation
and expansion state. Expansion replay returns current persisted jobs, which may
already be running or terminal. Accepted expansion bodies preserve empty groups.
ReadFailure supplies persisted recovery context through the declared read edge.
DecisionCancelHolder uses the owner's scoped cancellation request and lets the
holder finish. Both owners continue to use the same scheduling projection/Decide.

Protocol cancel accepts job with an optional step, or target with the active request
ID. Job-only cancels that job's active run_step/eval operations. Target handles eval
without synthetic job/step values. Selectors are mutually exclusive and never
cross the process binding. Acknowledgement follows cancellation and completion
of the selected operation. Cleanup waits for the original request to finish.

Both SDKs use nodeproto's MaskRequest, AnnotationRequest and SummaryRequest on
C2's declared node-host routes. Mask waits for acknowledgement, and annotation/
summary errors propagate. Body env is applied after plan, startup env before spawn,
and neither SDK reads credentials from env. Approval SkipIf remains an admission
condition on every placement. Executable approval before/after hooks are removed.

Concurrency declaration options preserve omission with pointers. The host resolves
scope=global, capacity=1, cost=1 and on_limit=queue only when absent, persists winners
in ExecutionSettings/JobExecutionSettings, and leaves declaration hashes unchanged.
Explicit zero capacity/cost fails. Both SDKs share canonical v2 vectors for these
presence cases, error objects, generated hashes and secret-containing identifiers.


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
| `nodehost.OwnerDelegator`, `OwnerHost.Delegator` | Resolve live owner ceilings and delegate expansion submission to the exact source generation under that owner lease. |
| `nodehost.NewNativeHost`, `NativeHostConfig` | Assemble owner and claimed execution with one native executor and register their delegated process services. |
| `nodehost.LocalAdmission`, `LocalAdmission.Run`, `LocalAdmission.Node` | Hold local run resources and run/box semaphores through joined scheduling, and job admission through execution and cleanup, using sealed settings. Nonlocal concurrency requires its store adapter. |
| `nodehost.StoreAdmission`, `CapacityStore`, `NativeHostConfig.Capacity` | Admit sealed plan/job groups through tenant capacity and continuous adoption. Native assembly discovers the store and binds the current run owner on each invocation. Unit renewal keeps holders and waiters live. Terminal transitions release capacity. |
| `nodehost.AdmissionFailure` | Preserve admission outcome and reason through cancellation and fenced executor reports. |
| `nodehost.OwnerHost.Around`, `OwnerHost.Admit` | Admit scheduling and job release before readiness, retaining local leases through execution and cleanup. |
| `nodehost.NativeProcesses` | Bind native startup, attributed masked logs, child-wait activity and cleanup to one live process delegation. |
| `nodehost.OwnerPlanning`, `OwnerLayerResult` | Supply checkout planning inputs and owner policy results without replacing intake or claim authority. |
| `nodehost.GeneratorReplay` | Supplies recorded source output for code-only reconstruction. |
| `nodehost.OwnerEvaluator`, `NewOwnerEvaluator`, `EvaluationInput`, `EvaluationDependencies`, `EvaluationStore` | Reconstruct accepted source code and evaluate every source generator through the common protocol and hash checks. |
| `nodehost.OwnerEvaluator.SkipIf`, `SkipEvaluation`, `OwnerEvaluator.MemoKey` | Evaluate pre-dispatch conditions and memo keys without starting job attempts or writing lifecycle state. |
| `nodehost.ProcessGrant` | Transfers a process binding and its idempotent revoker to the executor or owner evaluator. |
| `nodehost.ProtocolSessionConfig.StartEvaluation` | Launches the owner evaluator with private pipes, run-log attribution and route revocation. |
| `nodehost.SDKPlanner`, `PlanningProcessInput`, `PreparedPlanning` | Describe acquired code, resolve SDK argument defaults and supply lazy planning through the common cache boundary. |
| `nodehost.CheckoutPlanningInput`, `SDKPlanner.PrepareCheckout`, `OwnerPlanning.Resolve` | Resolve acquired project declarations and guards, then compute the settings proposal from the validated fresh or cached plan before acceptance. |
| `nodehost.ProtocolSessionConfig.StartPlanning` | Launches unbound describe/plan processes and routes every output stream through the planning secret gate. |
| `nodehost.ClaimAuthority` | Refers to authenticated executor authority without granting it from a request body. |
| `nodehost.NodeInput` | Binds an attempt to accepted spec, node, generation, runner and route bearer. |
| `nodehost.NewNodeInput` | Constructs execution from a persisted awarded row, its accepted seal, the selected engine and the granted runner. |
| `nodehost.ProcessAuthorities`, `NewProcessAuthorities`, `ProcessAuthorityDependencies` | Own narrowed node-host bearers in memory, provide fresh authentication snapshots and process-bound resource checks, resolve live services, and revoke authority on process exit or registry shutdown. |
| `nodehost.ConcurrencyKey`, `nodehost.ParseConcurrencyKey` | Encode and decode scoped group identity without separator collisions. |
| `nodehost.NodeHost.Execute` | Replans, checks drift, runs executor layers and reports through store transitions. |
| `nodehost.NewNodeExecutor`, `ExecutorDependencies`, `ExecutorProcess` | Connect the common executor to the fenced store, matched engine, SDK process and storage adapters. |
| `nodehost.ExecutorDependencies.Log` | Delivers masked host diagnostics under the committed attempt after step/retry writes. Delivery errors stop further author code, prevent retries and propagate after joining work. |
| `nodehost.OutputRecords`, `nodehost.JobUpload` | Publish typed output through verified uploads and record commits; read authorized output only after verified EOF. JobUpload supplies the common attempt/scope binding for output and artifact writes. |
| `nodehost.ArtifactRecords`, `ArtifactRecords.Stage`, `ArtifactRecords.Publish`, `ArtifactRecords.Prepare` | Stage authorized manifest members and capture workspace files through verified uploads and a transactional manifest commit. The artifact layer stages once and publishes after retries with the last attempt's binding. |
| `nodehost.DepcacheRecords`, `DepcacheRecords.Prepare` | Restore declared directories once and save cold successful caches after retries through the current storage binding. |
| `nodehost.MemoRecords`, `MemoRecords.Lookup`, `MemoRecords.Copy` | Verify scoped lookup and copy results, including explicit remote reads, before returning reusable references. They grant no claim and write no lifecycle state. |
| `nodehost.SDKEnvironment.CacheDirectory` | Resolve Go/npm cache directories with the captured environment, persisted job values and platform tool invocation. |
| `nodehost.ClaimedNodeHost`, `ClaimStore` | Own source acquisition and renewal around the common executor for an awarded remote job. |
| `nodehost.ExecutorActivity` | Reports progress and brackets authorized child awaits without extending the execution deadline. |
| `nodehost.ProtocolSessionConfig`, `ProtocolSession`, `StartProtocolSession` | Launch protocol-v2 SDK processes with private pipes, request correlation, cancellation, log attribution and joined cleanup. |
| `nodehost.SDKEnvironment`, `ProtocolSessionConfig.Environments` | Project captured host inputs, sealed run facts and persisted job environment through the shared process policy before native SDK startup. |
| `nodehost.FileEnvironmentSnapshots`, `NewFileEnvironmentSnapshots`, `CapturedEnvironmentLoader` | Store private local values and verify the request's environment identity before source acquisition or SDK startup. |
| `nodehost.LayerSide` | Distinguishes owner decisions from executor work. |
| `nodehost.OwnerSide`, `nodehost.ExecutorSide` | Name the two layer owners. |
| `nodehost.LayerSpec`, `nodehost.LayerTable` | Declare the complete outer-to-inner envelope and retry scope. |
| `nodehost.Reconciler.ReconcileNode` | Follows a holder and repeats atomic reconciliation after terminal or requeue events. |
| `nodehost.StateFollower`, `StateReader`, `StateSubscription` | Share persisted catch-up and joined subscriptions between owner graph waits and node reconciliation. |
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
| `source.EngineResolver`, `source.Options.InstallSDK` | Resolve the pinned engine and install the SDK entry with that engine and an explicit install environment. |
| `source.Entry` | Returns acquired checkout, exact commit, resolved default branch and SDK argv. |
| `source.ForRequest`, `source.ForExecution` | Project intake acquisition inputs or the accepted execution checkout policy without rereading mutable settings. |
| `source.Acquire` | Acquires source and builds or resolves the SDK entry through one placement-independent path. |
| `source.Entry.Close` | Releases owned resources without removing the user's checkout. |
| `source.Entry.CheckoutDirectory` | Exposes the acquired root that confines checkout-derived configuration reads. |

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
   NodeTransitionStore includes C1's RecordStep. Running step records commit before
   SDK invocation. Terminal and blocked-step records carry the same holder,
   capability, generation and attempt as the job. A refused write stops further
   dispatch, joins active steps and fails loudly without an application retry.
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

## Memo candidate verification

`MemoRecords.Lookup` calls the supplied transactional record client with the
bound repository/ref and declared job key. Local hits require authoritative
`HasMany` presence for output, manifest and member digests. Downloads must verify
through EOF and close. Output must also satisfy the shared JSON/size rule.
Declared output and artifacts require their references. Explicit JSON null and
an empty artifact manifest remain valid. Missing, refused, corrupt, malformed or
over-budget results return an empty miss. Parent cancellation remains an error.
Diagnostics contain only a constant message.

`MemoRecords.Copy` sends the supplied CopyMemoRequest unchanged after checking
its authority and destination against the binding. It verifies the returned hit
by the same rules. The record store owns the atomic copy and repeats live
authorization. The host cannot make a successful verdict from a failed copy.
Copy requires the positive destination attempt declared by C1. It cannot serve
owner hits before execution until C1 resolves the destination-identity gap.

Explicit RemoteCache enables a remote lookup only after a clean local miss.
Every remote download uses RemoteCache.Artifact. No remote digest reaches the
local byte client, and these reads never call CopyMemo or CommitRecord. W7 owns
the remote user's live authorization and verified-default provenance.

These APIs verify candidates. ReconcileNode still owns terminal materialization
and the sole lifecycle event. LookupMemo does not yet identify a definitive
MaterializeSource, and the record contract does not specify memo publication
encoding for optional outputs. Native owner composition, publication, coalesced
followers and remote-to-local import remain open. The continuation lists the
required contracts without changing another area's declarations.

## Executor directory caches

`DepcacheRecords.Prepare` implements the caches row of `LayerTable`. `Bind`
resolves a write destination for the current attempt and the derived
`blob.DepcacheKey`. It must return a `JobUpload` whose authority, repository,
ref, pipeline, job, cache name and key match those inputs. The controller still
reauthorizes every write. `Resolve` handles ecosystem directories and can use
`SDKEnvironment.CacheDirectory`. Explicit paths retain relative and absolute
directory support.

Lockfiles are opened inside the acquired workspace. The first readable regular
candidate supplies the key through `depcache.Key`. Key derivation uses the same
platform, scope and bytes for all cache callers. An escaping lockfile symlink
cannot contribute bytes. The key stays fixed through application retries.

The layer submits own repo/ref authority to `LookupDepcache`. The record store
owns own-ref, PR-base and verified-default ordering and provenance checks.
An explicit `StorageBinding.RemoteCache` permits a remote lookup after a local
miss. Remote reads use that interface alone. The layer has no remote writer.
Its profile authority may belong to a different team from the local run. A
remote record never becomes a local byte-read grant.
Cache records hold gzip tar archives. `tarsafe.WriteGzip` captures files through
rooted opens and rejects escaping links. Restoration verifies gzip, tar, blob
EOF and successful reader Close before installing the staged directory.

Warm directories are left in place. Missing keys disable the declaration.
Empty directories are not saved.
Missing, corrupt, refused or over-budget reads start cold. Save uses the last
attempt's clients and binding after a successful job. Save failures produce a
safe warning and preserve the job verdict. `Budget` bounds cache reads only
when its caller supplies a deadline. No cache timeout, size cap or expiry is
added here.

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

`source.Options.BundleTransport` supplies the HTTP transport for signed source
bundle downloads. Nil uses the standard transport. The source client owns
redirect refusal independently of that transport. It verifies the complete
bundle digest before unbundling, then fetches and checks out the sealed commit.
Read and close failures return stage-specific messages because transport errors
can contain the signed URL. Cancellation retains its cause. Every exit closes
the reader and removes owned seed, credential and checkout files.

Source acquisition checks cancellation after reading the SDK pin and after
engine resolution. Authority lost at either boundary prevents installation.
An acquired local checkout remains owned by the user.

`placement-direct-source/hermetic-native-git` acquires the sealed commit twice
through direct fetch and bundle-plus-delta fetch after origin advances and its
default branch changes. Native git supplies the checkout. Credential release,
engine resolution, installation and signed storage use contract fakes.
`hermetic-bundle-failures` refuses corrupt bytes, an invalid bundle, missing
objects, redirects, transfer/read/close errors and cancellation before engine
startup. Both cases check source credential and workspace cleanup. Native
placement drivers, real source-release authority, storage signing and Windows
remain separate integration proofs.

`ClaimedNodeHost.Execute` checks its award against persisted acceptance and job
settings, then waits for a successful claim renewal before acquiring source.
`source.ForExecution` supplies the sealed checkout policy and engine. Acquisition
uses explicit source options and passes its acquired entry to `NodeHost.Execute`.
The adapter joins execution, closes its entry and stops its keeper before returning.
Self-claimed jobs execute directly under the owner's keeper, which already renews
them atomically with the owner.

Executor errors are masked before they return to the caller as well as before
their terminal request is sent. Error matching still reaches the original cause,
so lease-loss handling and refused-transaction detection retain their meaning.
Structured error rendering receives the same masked message and hint with its
original code. Untrusted SDK details are withheld from that rendered error.

`OwnerHost.Mask` is required before renewal. Owner and claimed-node hosts use
the executor's error wrapper for every returned error, including persistence
reads and cleanup. Error matching retains the original cause. Rendered canonical
errors retain their code and masked hint but omit untrusted details. The mask
must share the source and SDK secret registry because these errors may contain
credentials before ordinary job logging starts.

An acquisition adapter that returns success after cancellation cannot continue
to acceptance or scheduling. Requested cancellation before acceptance finishes
the run and owner unit atomically. Lease loss and failed resume setup leave the
work to recovery. Initial planning retains the cancellation cause across cache
lookup, hit validation, invalidation, fresh planning, hashing and publication.
The owner seal acceptance check covers masked setup/acceptance errors and late
acquisition success after cancellation with fake store and acquisition adapters.

Setup failures report through `TransitionNode` with the awarded holder, capability
prefix and generation. Requested cancellation reports a cancelled job. Lost
authority or shutdown leaves reporting to recovery. Returned source errors are
masked and structured. A refused report remains an error. Acquisition never runs
under a rejected or silent claim. An acquisition that returns after lease loss
cannot start SDK code.

The unfenced `RunNodeCommand` entry point and its environment-derived claim
reader are removed. Runners and launcher bootstrap must supply the authenticated
award and `ClaimStore`, source-access and executor adapters to `ClaimedNodeHost`.
The older run-host caller still uses its dispatched-heartbeat helper until its
own lifecycle cut. The old Job builders in `internal/runners/k8s` and
`internal/runners/launcher` still emit `run-node` and need the authenticated
bootstrap cut owned by the runner area.

The unused launcher execution function, its buffered attempt reporter and cache
grant renewal loop are removed. `HostNode` and the claimed-mode pod branch are
removed with them. The remaining legacy pod path executes dispatched work only.
Its owner already applied skip, memo and admission. Claimed work uses
`ClaimedNodeHost` and the common executor. Launcher bootstrap must provide its
authenticated award and adapters through that boundary.

`SDKPlanner.Prepare` runs describe with the acquired engine and granted runner
identity. Describe and plan hello omit job bindings. All declared secret defaults,
including defaults from other pipelines in the entry, enter the run's masker
before describe output is released. Explicit argument values, including empty
strings, override defaults. `PreparedPlanning.Args` and `SecretArgs` supply the
resolved inputs for `AcceptPlan`. The owner uses the returned description for
secret declarations, risks and guard evaluation. It retains both YAML layers.

Prepare closes and cleans up the describe process. Its lazy `Functions.Plan`
starts a separate process only on a cache miss. Both starts recheck engine policy.
The cache identity includes the describe reply, resolved args, SDK-visible run
context, granted runner, target platform and the host's runner-input digest.
Other source, environment and project digests still come from the host. Cached
plans pass the current secret matcher and shared validator before acceptance.

Describe retains the thirty-second budget and eight-MiB output staging bound.
Overflow is counted and reported. A failed describe withholds unknown-secret
output and error text. Successful describe releases masked frames, stdout and
stderr into run logs. Plan, validation and cleanup errors are masked before they
leave the host. A failed close or cleanup discards the proposed plan.

`OwnerHost` waits for its first successful renewal before acquiring source.
`OwnerHost.Renew` is required and carries the current `Executing` value to the
authenticated unit renewal transaction. It replaces the liveness-only
`OwnerStore.RenewOwner` requirement. Trigger and plan adapters use
`Store.RenewUnit` or the corresponding controller request. Controller-owner
adapters retain their singleton fence. Preparation reports compute until
acceptance. Native assembly then uses live SDK process counts per run, through
startup, close and cleanup. Waiting without an SDK process reports false.
Overlapping processes keep compute active until the last cleanup returns,
including failed cleanup. Failed startup without a process releases its count.
Acquisition receives the persisted acceptance on resume, even when the caller
omitted it. Checkout resolution cannot replace the stored intake request.
Without a custom acquisition adapter, `OwnerHost.Source` supplies the shared
`source.Acquire` path. New owners project `source.ForRequest` from persisted
intake. Resumed owners project `source.ForExecution` from persisted acceptance,
including its checkout policy and sealed engine. The explicit source options
supply the captured process base. `RunInput.Engines` fills an absent resolver.
Owner policy tasks run independently so a waiting approval or admission does
not stop ready siblings. Completed executor errors are checked against persisted
terminal state because a committed failure must still release recovery jobs.
Policy and persistence errors remain errors even when a competing write makes
the job terminal. Acquisition and planning failures before acceptance finish the
run and owner unit atomically. Setup failures on resumed runs leave accepted work
to recovery. Loss of authority never writes a terminal verdict.

Approval release stops after the approval layer. It never enters memo, compute
admission or executor layers while waiting for a human decision.

`ExecutorActivity.Await` brackets an authenticated child wait and returns an
idempotent resume function. Nested waits suspend only the no-progress deadline.
The native child-await route adapter must bracket the whole wait. The execution
deadline and owner cancellation remain active. Timed-out attempts run finally
steps and last-attempt after_run under the existing cleanup budget while the
owner remains live. Lost authority suppresses further SDK calls. Process cleanup
joins after route revocation and cannot turn a failed attempt into success.

`ProtocolSessionConfig.Start` supplies the native executor process adapter.
It takes a captured environment policy, a masking log sink, route revocation
and registered cleanups. It uses the shared request validator and frame decoder.
Tests inject those functions while their owning area is unimplemented. Wire
request IDs are private and opaque. A cancelled request joins its SDK work before
returning so finally steps cannot overlap a timed-out body. Windows protocol
handles travel in argv. The session does not own controller credentials.
Protocol cancellation now targets the active private request ID for evals,
including evals with no job. Cancellation joins the original operation before
cleanup and preserves the SDK process after successful acknowledgement.

Process startup owns route revocation even when no process starts. Failed
validation, cancelled setup and exec errors revoke the delegated route once and
retain revocation errors. A returned process transfers that duty to Close.

The request retains its cancellation cause. Close treats this requested eval
termination as an expected exit, but still reports revocation and reaping errors.

The native Start, StartEvaluation and StartPlanning methods build their own
environment through `ProtocolSessionConfig.Environments`. `SDKEnvironment`
defaults to `procenv.Build` and `procenv.SplitJobEnv`. Planning uses the Plan
kind and has no job startup assignments. Attempts and owner evaluations read
the persisted job declaration, including generated jobs absent from the initial
plan. They check its run, job, generation and spec hash against their input.
The sealed spec supplies run identity, git facts, origin and DryRun. Granted
runner identity comes from the award. Only SplitJobEnv's Startup assignments
reach process startup. The SDK applies Body after a successful replan.

Environment construction errors discard partial output and revoke the delegated
route before returning. The low-level StartProtocolSession transport still takes
an explicit Environment. Native adapters always replace that transport input with
the built result. Native tests launch real SDK fixture processes and verify that
exact environment. The environment acceptance cell uses store and procenv fakes
and proves projection and startup refusal. W6 filtering, SDK body application,
snapshot capture at intake and native placement assembly remain integration work.

Local source and SDK startup verify the captured environment against the request's
names and digest. `FileEnvironmentSnapshots.Capture` writes host-selected values
to an owner-only file before intake. Its returned `EnvSnapshot` belongs in the
first seal. Files preserve native value bytes and explicit empty values. Loading
does not consume a snapshot because replacement owners, retries and children
need the same inputs. `OwnerHost.Snapshots`, `ClaimedNodeHost.Snapshots` and
`SDKEnvironment.Snapshots` load that identity. Without a loader, a local
`Base.Captured` must still match the seal. Changed, missing, malformed or
nonprivate snapshots fail before acquisition or process startup. A read failure
cannot select the daemon's environment.

`PlanningProcessInput.Environment` carries the intake snapshot metadata through
describe and plan. `SDKPlanner` includes its digest beside the host environment
digest in the planning-cache inputs. Snapshot identity cannot replace missing
host inputs or hide changes to a remote base.
Attempt and eval startup read the identity from the accepted request, including
retry seals that retain the source run ID. Native child-process tests use the
real procenv policy to check credential filtering and runtime identity after
snapshot loading. The new `environment-captured-local-base/hermetic-private-snapshots`
cell checks the file and startup boundaries. Intake, schedule installation,
child submission and snapshot retention still need placement wiring. C4's
`EnvironmentDigest` also needs byte-preserving hashing for non-UTF-8 values.
Distinct native values currently collapse through JSON replacement characters.

Retry intake preserves the source run's sealed request, spec and plan. SDK
environment and replan requests use the destination row's run ID while git,
arguments and execution settings keep their accepted values. Attempt and eval
environment construction cannot require the envelope's embedded source run ID
to equal the execution run ID. The environment acceptance cell and native SDK
startup tests cover both identities with fake persistence and process policy.

`ExecutorDependencies.Bind` and `EvaluationDependencies.Bind` delegate a new
nodehost route bearer before each process startup. The factory uses authenticated
owner or claim authority. The returned binding must keep the input's run and job
identity and must not reuse that input bearer. Start receives the delegated
binding. The factory returns `ProcessGrant`, including an idempotent revoker.
The host retains that revoker before checking the binding or starting a process.
It revokes before joining the process and running cleanup on every exit,
including a factory that returns both a grant and an error. Revocation errors
prevent successful execution or policy acknowledgement. SDK hello receives only
the process binding. Unbound transports may omit the factory.

Owner self-claims use `NewNodeInput` with the persisted awarded row, acquired
engine and `RunInput.GrantedRunner`. `OwnerHost.AdoptClaim` receives the private
award for per-attempt delegation. It cannot replace the constructed input.
An award containing a bearer requires this adopter, and a refused adoption stops
execution. The former `OwnerHost.Bind` input-construction callback is removed.

Native `ProtocolSessionConfig.Start` and `StartEvaluation` receive that validated
revoker through the host's private input. It replaces any revoker in reusable
session configuration before environment construction or spawn. Callers do not
mutate shared configuration to connect a new grant. Direct session users still
supply their own revoker.

`lease.Beat.CancelReason` carries the cancellation reason from C1's renewal.
The owner and claimed-node adapters preserve it. A successful cancellation reply
uses that reason in `lease.CancelError`. An absent reason keeps `cancel_requested`.
Failed renewal replies cannot request cancellation or replace the first cause.

`OwnerEvaluator.SkipIf` evaluates predicates in declaration order and stops at
the first true result. Each predicate reconstructs and checks the accepted code
in its own process. An expired eval is cancelled by private request ID and joined
before that process closes. The
per-predicate budget is 30 seconds unless declared otherwise. Timeout means
don't skip and records the predicate index in `SkipEvaluation.TimedOut` for host
diagnostics. The next predicate starts after close and cleanup join. Null and
non-boolean results, drift, SDK failures and cleanup errors prevent dispatch.

`NewNativeHost` installs this evaluator for the `skip_if` owner layer. Approval
opening uses the store transition and state follower. Its caller-supplied
`Owner.Layer` handles active debug, memo and admission policies. Jobs without
those policies need no callback. Local execution still requires admission,
even without a concurrency group. The
native layer logs timeout indices and proposes a skipped transition only after
evaluation, revocation and cleanup join. Approval SkipIf success still requires
the missing C1 owner approval-success edge. The native layer refuses that case
instead of recording the approval as a skipped job.

Both session implementations use one platform process launcher. The duplicate
native launch functions and their forwarding file are removed. Native owner
assembly is exercised through `OwnerHost.Run`, self-claim, execution and atomic
finish with explicit store and C2 validation fakes. See the
[owner assembly continuation](w3-owner-assembly-continuation.md) for the remaining
placement and legacy-runtime cuts.

Holder start, retry and result transitions include the captured
`NodeTransition.SourceAttempt`. Setup failures before execution report the
award's retained ordinal. Start and retry consume `Applied.Attempt` before
later SDK calls or reports. Neither path rereads an ordinal to refresh an old
report.

Request cancellation joins an outstanding request-pipe write within the existing
process termination grace before deciding that the write was interrupted. The
SDK may already have consumed the frame while the writer has not yet published
completion. A completed write proceeds to targeted cancellation and leaves the
session usable. A blocked write still fails the session and joins the child.

`OwnerEvaluator.MemoKey` evaluates the declared closure with its existing
30-second budget. A nonempty string supplies the key; null disables memoization
for the job. NoMemo bypasses reads while preserving key evaluation for later
writes. The store and storage adapters still own scope, result authorization and
materialization. Neither evaluator creates an attempt. Cancellation retains its
cause, and other policy error text passes through masking.

`OwnerEvaluator.Expand` checks the persisted source generation and declaration
hash, then reads its authorized recorded output. A successful or memoized source
with expected expansion evaluates all declared generators in one SDK process.
Generated sources first reconstruct their ancestor chain with the executor's
shared replay code. A source materialized before any claim may have generation
zero. Its persisted generation still fences expansion and replay.

Each returned job must belong to the generator that emitted it. The evaluator
preserves raw job JSON until the shared validator checks the complete expansion,
so unknown fields and dependencies between generator groups cannot be hidden by
a host decoder. Empty expansion is an explicit jobs array. Evaluation, drift,
validation and cleanup failures discard the entire proposed body and return one
masked `ExpansionFailure`. Owner cancellation or lease loss returns an error and
leaves settlement to live authority. `OwnerHost` submits the body or failure once
through `AcceptExpansion`. The evaluator writes no topology or lifecycle state.

`EvaluationDependencies.Start` supplies the environment and delegated route
binding. `ProtocolSessionConfig.StartEvaluation` provides the native process
transport. Its log callback attributes owner eval output to run logs. The output
reader must verify the source generation and authorize the recorded bytes. These
adapters still need placement wiring.

Code replay evaluates every declared generator of a recorded source together,
including empty groups. It uses the same raw-body collector as initial expansion
and validates the combined body because jobs can depend on another generator's
members. Persisted jobs from other sources supply the remaining validation
topology even when their code has not been reconstructed yet. Every reconstructed
member must retain its persisted hash. Repeated replay entries for one source
evaluate it once and must agree on its generation and recorded output.

`lifecycle-generator-reconstruct-code/hermetic-replay` checks nested generation,
dependencies within and between source expansions, recovery ancestry, empty
groups, duplicate replay, changed or missing siblings, raw unknown fields and
conflicting source inputs. Changed declarations fail with plan_drift before the body.
Its persistence, validator and SDK transport are fakes. Native placement and
transactional insertion coverage remain open.

The host implementation accepts the declared store, validator, hash and engine
interfaces so their independent implementations can be tested with fakes.
This does not complete the native placement adapters. Those adapters must supply
source installation, environment construction, owner policy
evaluation, blob storage, route delegation and a state follower that catches up
from persistence before waiting. The store owns the scheduling projection and
decider. No host adapter may substitute its own readiness policy.

`OutputRecords.PublishOutput` supplies the executor's output publisher. Its
binding adapter returns live server-derived upload authority and a record key.
The host checks the destination job attempt, scope, byte limit and JSON before
upload. It hashes the exact JSON bytes, checks the verified receipt, commits
the output record and checks the returned key, digest and source identity.
Only then may the executor propose a terminal result containing that key.
The result transaction still rechecks its reference edge. A refused upload,
record commit or result transaction cannot produce a successful result.

`OutputRecords.ReadOutput` looks up an authorized output record, consumes its
download to verified EOF and closes the reader on every path. Missing records,
missing bytes, invalid JSON, oversized output, integrity failures and close
errors fail the read. Explicit JSON null remains a valid recorded value.
The adapter does not grant access from a digest or infer a missing value as null.

The output acceptance cells use fake blob and record stores. Native wiring
still needs C2 upload binding and W7 byte/record implementations. Persisted
result reads also need C1/W9 to expose the output record key with its terminal
generation and distinguish an absent declared result from a missing required
record. `NodeRow` exposes no result reference, so the host cannot construct that
lookup from a node read. Owner evaluation and generated-code replay must use
the persisted reference through live read authority.

`ArtifactRecords.Publish` captures declared file globs beneath the acquired
workspace. It uploads every selected file under the exact current attempt,
checks each receipt, uploads a deterministic manifest and commits the authorized
member rows. The manifest encodes a sorted `files` array whose entries contain
`path`, `digest`, `size` and `mode`. Empty captures encode an empty array and
still commit a manifest. A failure returns no successful record key.

Rooted opens prevent symlink changes from reading outside the workspace. A
wildcard match outside the workspace is skipped through the supplied warning
sink. A literal outside file fails. Internal symlinks retain their declared
paths. The byte backend verifies the expected size and hash on upload, so a file
changed after hashing cannot produce a successful receipt for stale bytes.
Neither file capture nor publication uses a team-wide existence probe.

`ArtifactRecords.Prepare` supplies the artifact portion of the common executor
layer. Producer resolves a declared producer's persisted manifest record key.
Staging runs in consumes order before SDK startup. Publication runs once after
successful execution and uses the last committed attempt's StorageBinding.
Failed work publishes no manifest. Upload and record errors fail the job.
Only a successful record commit supplies `NodeResult.ManifestRecord`, and the
terminal transaction still rechecks attachment authority. The caller composes
the separate cache layer and routes warnings through the shared masker.

The artifact publication acceptance cells use real workspace files and fake
byte/record transactions. Generated tests cover outside symlink chains and
attempt/scope mismatches. Real upload binding, source record reads, edge checks
and native placement assembly remain integration points.

Owner reuse now probes MaterializeOnly before admission. A hit materializes;
a miss returns Dispatch without awarding execution. DecisionCancelHolder uses
the run owner's scoped cancellation transition. Native store/HTTP assembly must
still supply those transactions.

Owner policy also needs a fenced store edge for a skip predicate that errors
before dispatch. `NodeTransitions` permits owner dependency skips and cancels,
but no pending-to-failed policy result. Debug pause status detail has no typed
mutation in `TransitionExtra`. Neither path may fall back to legacy raw writers.
Approval conditions need a pending-to-success edge when SkipIf returns true,
because that bypass must release dependents. A skipped dependency would suppress
them. Approval waiting also needs the persisted opening time and resolution ID
in the read contract so a replacement owner retains the original deadline.
Eval log identity must allow a materialized source at generation zero because
`nodes.generation` starts at zero and unclaimed materialization needs no award.
The `log_streams.source_generation > 0` constraint does not cover that source.

The amended ReconcileResponse and ExpandedJob.Current carry the persisted
execution state, and the remote adapters preserve it. ScheduleSnapshot remains
insufficient for full execution reads. Owner expansion and replay also need
terminal record references. ExecutorDependencies.ReadFailure carries the source
generation and requires the recorded masked failure. Its native authenticated
adapter remains an integration point.

Executor retry and terminal transitions now populate `NodeResult.Failure` with
the bound job ID, action/verify stage and the same masked, bounded message as
the result. Source setup failures use action. Verify timeouts retain verify.
Hints and details are discarded before persistence. Unknown error codes survive
unless masking changes them, in which case the error uses `sdk_error`.
Successful and cancelled results carry no failure. Holder renewal and reporting
also carry the awarded capability's `Actor.Authority` reference, not just its fence.

The remote wire still omits `JobResult.Failure` and
`MaterializeSource.Result`. W3 refuses the latter before calling the controller
because dropping its terminal state sequence would change the proposed source.
Both fields need W9 contract declarations before remote adapters can carry them.

`lifecycle-dependency-verdicts/hermetic-recorded-failure` checks retry and final
failure records through the common claim host and executor with contract fakes.
`hermetic-recovery-graph` checks the remote host projection and real shared
decider against ordinary and generated prerequisites, failed/nonfailed parents
and optional recovery verdicts. These do not prove native placement or store
transactions. The remaining work is listed in [the merge continuation](w3-merge-continuation.md).

`StateFollower.Wait` and `WaitNode` supply the shared state follower for
`OwnerHost.Wait` and `WaitNodeChange`. Each wait arms its run subscription before
reading persistence because a holder can finish between reconciliation and
subscription. A notification prompts another read. A committed state sequence,
initial acceptance, added job or expansion resolution ends the wait. An expired lease and a
changed store clock grant no execution authority. Reordering rows is not progress.
Regressed sequences, missing jobs and replaced accepted expansions fail loudly.
Every exit cancels and joins the subscription, including failed setup. Owner waits
retain subscription failures when another job completes or the owner is cancelled.

The placement supplies `StateFollower.Subscribe`. It must deliver wake-ups for
commits from other processes and return only after the subscription is armed.
Its Close function joins subscription work after cancellation. The shared follower
owns catch-up and progress checks. It adds no polling interval or lease inference.
The recovery acceptance cell uses the follower with fake persistence and
subscriptions. Native controller and local-controller subscriptions remain open.

The legacy envelope's retry-auto dispatch branch, duplicate start-event condition
and dedicated tests are removed. Automatic retry-cause traversal and the host's
terminal-agent-loss reset hook are also removed. A terminal row stays terminal;
only a committed requeue permits recovery. Application retries keep their existing budget
and backoff. The common executor's fenced retry acceptance remains the target
proof. The legacy scheduler, dispatched-pod executor and run-host entry points
still need their lifecycle cut.

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

Amendment A1-54 resolves approval SkipIf: retain the condition and cut only
executable before/after hooks. Remaining W3 contract gaps are listed in the
[amendment handoff](w3-amendment-handoff.md).

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
