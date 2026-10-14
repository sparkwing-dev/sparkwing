# C3 protocol and SDK contract

This is the implementation contract for node protocol 2, both author SDKs,
the fake host, and the shared host library. It follows the frozen architecture
and decisions 87-100. Declarations compile, but bodies marked
`panic("unimplemented: c3-protocol-sdk")` do not provide runtime behavior.
The capability and layer tables contain the complete target data.

## Ownership

| Concern | Owner and source |
|---|---|
| Hello, operations, frames, describe, flat plan and shared validation | `internal/nodeproto/*_contract.go` |
| Windows inherited-handle argv spelling | `internal/protofd/transport_contract.go` |
| Run and node host roles, layer order, event-following reconciliation | `internal/nodehost/host_contract.go` |
| Pure graph decisions and atomic reconciliation/expansion signatures | `pkg/store/scheduling_contract.go` |
| Every unit's renewal loop | `internal/lease/keeper_contract.go` |
| Checkout and entry acquisition | `internal/source/acquire_contract.go` |
| Go declarations and services | `sparkwing/sdk_contract.go`, `sparkwing/inputs/memo_contract.go`, retained API inventory below |
| Author test host | `sparkwing/sparkwingtest/host_contract.go` |
| TypeScript author API | `sdk/typescript/index.d.ts` |

The detailed inventories are part of this contract:

- [Protocol inventory, wire rules and validation](c3-protocol-notes.md).
- [Go SDK inventory, including retained and removed exports](c3-go-sdk-notes.md).
- [TypeScript export and member inventory](c3-typescript-notes.md).

C1 owns state transitions, schema, accepted spec storage and blob authority.
C3's store file declares the scheduling and host integration surface. It adds
no status writer and no second transition table. C1's Tenant interface must
include these methods rather than declaring another expansion or reconciler.
C2 owns the canonical error object, code registry and credential authority.
`nodeproto.ErrorUses()` is the complete list of protocol code dependencies,
not another definition registry. C4 owns environment policy, settings,
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
| `nodehost.GeneratorReplay` | Supplies recorded source output for code-only reconstruction. |
| `nodehost.ClaimAuthority` | Refers to authenticated executor authority without granting it from a request body. |
| `nodehost.NodeInput` | Binds an attempt to accepted spec, node, generation, runner and route bearer. |
| `nodehost.NodeHost.Execute` | Replans, checks drift, runs executor layers and reports through store transitions. |
| `nodehost.LayerSide` | Distinguishes owner decisions from executor work. |
| `nodehost.OwnerSide`, `nodehost.ExecutorSide` | Name the two layer owners. |
| `nodehost.LayerSpec`, `nodehost.LayerTable` | Declare the complete outer-to-inner envelope and retry scope. |
| `nodehost.Reconciler.Reconcile` | Follows a holder and repeats atomic reconciliation after terminal or requeue events. |
| `nodehost.ExpansionStore` | Gives owner/eval hosts the Tenant's expansion persistence boundary. |
| `store.ScheduleNode` | Projects accepted graph and lifecycle facts into the pure decider. |
| `store.ScheduleExpansion` | Preserves generator-to-source/member mapping, including empty expansions. |
| `store.ScheduleSnapshot` | Supplies a consistent graph, expansions and cancellation state. |
| `store.DecisionKind` | Names graph intent without granting permission to write state. |
| `store.DecisionReady`, `DecisionApproval`, `DecisionTerminal`, `DecisionCancelHolder`, `DecisionExpand` | Enumerate every graph action. |
| `store.NodeDecision` | Carries one job action with its generation, outcome and reason. |
| `store.ScheduleDecision` | Returns ordered graph actions and an optional final run verdict. |
| `store.Decide` | Exposes the single pure `decide()` to run hosts. |
| `store.ReconcileRef` | Identifies a run/node within the Tenant's team. |
| `store.ReconcileActor` | Refers to owner capability and fence for transactional rechecks. |
| `store.MaterializationSource` | Names persisted memo/retry result authority and its spec hash. |
| `store.SourcePolicyC3` | Offers a reuse source without overriding a live holder. |
| `store.ReconcileKindC3` | Names the complete atomic reconciliation result set. |
| `store.KeepTerminalC3`, `WaitHolderC3`, `ExecuteC3`, `MaterializeC3` | Distinguish terminal reuse, event following, committed self-claim and committed result copy. |
| `store.ReconcileResult` | Returns committed generation, attempt, event sequence and any materialization source. |
| `store.Tenant.ReconcileNodeC3` | Chooses, self-claims or materializes in one transaction. |
| `store.ExpansionSubmissionC3` | Names the source's immutable generation and generated job declarations. |
| `store.ExpansionAuthority` | Refers to the live owner delegation or awarded eval capability. |
| `store.ExpansionResult` | Returns exact accepted jobs, persisted spec hashes and replay status. |
| `store.Tenant.AcceptExpansionC3` | Validates and inserts generated jobs while resolving the barrier atomically. |
| `store.Tenant.FailExpansion` | Resolves a generator error under the same authority and fence. |

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
| `source.Entry` | Returns acquired checkout, exact commit, resolved default branch and SDK argv. |
| `source.Acquire` | Acquires source and builds or resolves the SDK entry through one placement-independent path. |
| `source.Entry.Close` | Releases owned resources without removing the user's checkout. |

## Host invariants

1. The same lifecycle handles new and resumed runs. The request seal is immutable.
   First planning resolves checkout facts, invokes describe, merges settings and
   submits the second spec half and plan together. No job runs before AcceptPlan.
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
9. AcceptExpansionC3 accepts all generator groups of a source in one body containing
   jobs only. Each job names exactly one current source generator in its groups.
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
    C1's spec decoder and C2's error decoder validate it. Source projections and
    scheduling snapshots come from trusted host/store adapters. Capabilities,
    holders and generations supplied by an SDK or arbitrary HTTP body grant no
    authority. The store derives binding from authenticated capability records.

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
| C1 | `store.Tenant` must implement `ReconcileNodeC3`, `AcceptExpansionC3` and `FailExpansion` with these signatures. Reuse `ScheduleSnapshot`/`ScheduleExpansion` and `decide()` for controller settlement. |
| C1 | `runspec.Request` serializes into `RunInput.SealedRequest`; the accepted spec serializes into `AcceptedRun.Spec`. Hashes are canonical C1 hashes. `source.Spec` is a derived projection, never a second stored spec. |
| C1 | `ReconcileResult.Attempt` and `NodeInput.Attempt` are node-wide ordinals. `Generation` is the claim fence. Expansion source generation is immutable at terminal. |
| C1 | `MaterializationSource` must pass blob existence, integrity and read-set authorization. C3 does not mint a receipt or expand a read set. |
| C2 | `apierr.Error`/`apierr.Code` own the object and registry. `Reply.Error`, `JobFailure.Error`, `RunResult.Error` and `EvalResult.Error` carry its inner JSON object. All `ErrorUses()` strings must exist in that registry. |
| C2 | `ProcessBinding.Routes` contains a nodehost-audience bearer. `ClaimAuthority`, `ReconcileActor` and `ExpansionAuthority` refer to authenticated capabilities, whose binding/liveness C2 and the store recheck. |
| C2 | `source.Access` calls the live source-release policy. `lease.Keeper.Renew` errors expose canonical codes and Retry-After metadata. `Beat.WatchedNodes` encodes as watched_jobs to keep customer vocabulary. |
| C4 | `ValidationContext.Policy` implements the one seal-time setting/ceiling resolver. `lease.Keeper.Interval` comes from `limits.Renew`; renewal configuration passes `limits.CheckRenewal`. |
| C4 | `EnvValue.Startup`, SDK `Startup()` and TypeScript `startup()` agree with `procenv`'s fixed startup names. Attempt and cleanup env agree, and credentials never enter Build output. |
| C4 | `protofd.Arg` remains --sw-node-protocol; `protofd.HandlesArg` is --sw-protocol-handles followed by decimal request,frame on Windows. Both SDK transports parse that form and clear inheritance during initialization. Unix keeps fds 3 and 4. |
| T1 | Conformance drives final `nodeproto.Request`, `PlanDoc`, `ValidatePlan`, `RunHost`, `NodeHost`, `sparkwingtest.Host` and TypeScript's root declarations. Old-suffixed symbols test scaffolding only. |

## Open questions for the lead

- The brief assigns a codes registry to C3 and the one canonical registry to C2.
  This contract stops at `ErrorUses()` and keeps C2 as sole registry owner, as §6.5
  requires. Confirm this ownership when integrating the parallel areas.
- C1 owns the target store API while C3 names decide, reconciliation and expansion.
  This contract owns their declarations. C1 owns their transactions and state
  writers. Include these methods in C1's interface instead of adding competing
  types or another AcceptExpansionC3 implementation.
- §6.8 refuses TypeScript on Windows, but decision 99 requires both at launch.
  This contract chooses full Windows parity and records the conflict for the lead.
- A28 removes approval hook methods while §3.2/A27 require approval-condition
  parity. Both SDKs retain SkipIf as a condition and omit executable before/after
  hooks. Confirm that reading before implementation.
- Decision 90 cannot print an offending ID containing a secret while hiding that
  secret. Diagnostics name its field path instead. The complete identifier and
  matching value must not appear in error details, logs or receipts.
- The architecture names source.Acquire but not its concrete input type. C3 uses
  a source-only projection so it compiles independently of C1's new package.
  C1 must supply this projection from sealed facts without re-resolving identity.

- A source can declare several generators, but the schema stores one expansion
  state per source. This contract accepts their combined body atomically, counts
  the 256-job fan-out cap separately for each generator, and records each group
  even when empty. Confirm that source-level settlement is the intended unit.

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
