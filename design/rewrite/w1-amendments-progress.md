# W1 amendment handoff

Continuation of b96641913. W1 is incomplete. The index is clean. The lead owns
commits, independent review and delivery pipelines. W1b implementation files
were left alone.

## Latest continuation

Continues b96641913 with a clean index. Added the C1 team source-credential release
transaction and current-owner consent mutation. Release reloads authority and
enrollment, derives the host from the sealed source location, repeats the claim
and deadline checks, and commits C2's complete audit before returning the sealed
credential. The final clock check covers authority or lease expiry during policy
work. The controller still owns decryption and HTTP authorization.

Security invariants: a release returns a credential only for the live source
award and its sealed repository after the matching audit commits. Only the
machine's current owner can change consent. Two property tests generate forged
principal and target fields, account identities and consent choices. Component
tests cover node, trigger and cloud plan awards, unconfirmed SSH, revoked parent
authority, changed holders/generations, execution start, cancellation, deadline,
lost consent, enrollment changes, fake cloud placement, policy refusal, and an
audit-write or commit failure. They use the real store and a fake for C2's declared
AuthorizeSourceRelease policy, which remains a stub on this branch.

Local C1 additions are Tenant.ReleaseSourceCredential and
Tenant.SetSourceMachineConsent. The five added source-audit columns implement
fields already required by SourceReleaseAudit. No other area's contract changed.
The new private source-policy dependency delegates to the existing C2 owner.
No setting, limit, environment variable, service or compatibility alias was added.

Deleted the unused Operator.ListRunsAcrossTeams and CountRunsAcrossTeams APIs
(10 production lines) and their obsolete assertions (10 test lines). Neither had
a runtime caller. No package outside pkg/store was changed. The larger legacy
runtime APIs still have callers and remain open. W1b files are untouched.

Real integration still needs W2's policy, authenticated source route and decryptor,
and enrollment that records the account owner in capabilities.created_by. The
separate GitHub App mint/audit path remains open. The existing contract gaps below
remain open. No native acceptance cell or inventory test port was claimed in this
continuation.

Verification uses the scratch/cache environment recorded below. Whole-repository
go build and scoped go vet pass. The final W1 selection passes 238 top-level
tests / 685 cases without skips or failures. Add source_release_test.go and
run_view_test.go to the two prior handoffs' file lists to reproduce it. All
TestEngine controller tests pass, 21 top-level tests / 31 cases. The final focused
source selection passes eight top-level tests / 29 cases. Schema and SQL-scope
checks pass against the stricter audit columns. Repository key, machine identity
and a positive claim generation are required on new audits.

Full match, controllerwire and acceptance package commands pass. Acceptance still
contains 2,995 native skeleton skips. The OpenAPI drift check and diff whitespace
check pass. No local user.name, user.email or core.worktree override is present.
The changed source and schema have no new dependency outside the worktree.

Shared files touched are pkg/store/store.go and schema.sql, the C1 contract
document and this handoff. Other source changes stay in pkg/store. Native HTTP,
PostgreSQL interleavings, operating-system placements, races and delivery pipelines
remain lead checks. The only fixture correction was to pair terminal run status
with finished_at as the final schema requires. Aggregate API snapshots and the
rewrite changelog remain integration work. No Git writes or Sparkwing binaries
were run. Scratch is removed before the final report.

## Previous status-reader continuation

Implemented the persisted status portion of EngineController.RunDetail. The new
local C1 API Tenant.ReadRunView returns status, ordered node-wide attempts and
committed tagged log identities from one read snapshot. Read errors discard the
whole result. It reads log identities from the agreed schema without changing
W1b's log persistence files or granting log access. The public adapter uses W6's
DisplayRequest/DisplaySpec and the destination run's lineage. It omits private
seals, hashes, capabilities, job declarations and storage prefixes. Engine version
comes from the accepted spec. Version-policy notices remain an engine integration
point. The method currently returns an empty Notices list.

The security invariants are that public status exposes no classified argument
value or private seal, and that a tenant cannot read another tenant's status,
attempts or log identities. New property tests generate argument values,
classification states, a declassified second seal and foreign tenant keys.
Runtime component tests cover multi-generation attempts, plan/eval log identities,
timestamps, retry lineage, missing runs and failed snapshot reads.

The display tests port the redaction behavior of
TestSecretArgs_ControllerGetRunRedactsByDefault and
TestSecretArgs_ControllerRedactsEveryArgOfAnUnclassifiedRun from
5ba4cb0ec1ec222defc58f5eaee6ca332a058303. They exercise the final adapter and
shared display helpers. HTTP registration and route authorization remain W2's
integration work. No native acceptance cell was activated.

Deleted pkg/store/secrets_legacy_import.go, including ImportSecrets and ImportMark.
It contained 55 legacy lines and had no callers after the merged migration cut.
No caller outside pkg/store needed migration for that deletion. The legacy
StateStoreOld, plan/claim/trigger writers and offer-round path still have callers.
They remain open. No W1b file was edited.

The three conflicted files still contain resolved content with no conflict
markers. Their unmerged index entries remain for the lead to stage because the
swarm brief prohibits worker Git writes. No merge was aborted or Git state written.

Verification in this continuation uses the scratch/cache settings recorded below.
Whole-repository go build and scoped go vet pass. The W1 store selection now
passes 230 top-level tests / 656 cases with no skips or failures. All TestEngine
tests pass, 21 top-level tests / 31 cases. The match, controllerwire and acceptance
package commands pass. Acceptance skeleton success still includes blocked native
cells and establishes no native execution. The OpenAPI drift check and diff
whitespace check pass.

To reproduce the new selection, add run_view_test.go to the store file list below
and run all controller tests matching ^TestEngine. The new controller files are
engine_detail.go and engine_detail_test.go. engine_contract.go calls the new
reader. Local C1 additions are RunView and Tenant.ReadRunView, documented in
c1-data.md. No external contract, setting, limit, environment variable, service,
schema table or compatibility path was added. Source release, SyncLauncher,
Compatibility, signed status notices, the contract gaps below, legacy retirement
and real runtime integration remain open.

## Previous continuation

Added Store.CheckBinding and the transaction implementation for current
authority and tenant, run, job, owner, plan, pipeline, child-generation,
own-unit, token-owner, machine-owner, expansion, declared read, source and secret
relations. The supplied Principal is a lookup reference. Persisted fields decide
the result. Named selection shares the existing matching-input reader with
locking disabled for the PostgreSQL read snapshot. Awarding still uses its
locking path and the full matcher with observed capacity and labels.

This adapter is incomplete in two places. BindSlotOfBoundNode returns unavailable
because Target cannot name the declared concurrency key. Data selectors call
capability.DataAccessChecker on the same store transaction when W1b supplies that
implementation. This branch returns unavailable instead of granting an edge.
Host process cleanup stays with C3. Machine ownership uses capabilities.created_by.
The trusted machine provisioning path still has to persist the enrolling account
there. Current generic capability persistence writes the subject ID.

Added Tenant.ReadOwnerSnapshot and implemented EngineController.Snapshot. The
authority, owner lease, jobs and expansions come from one snapshot. Replaced,
expired and revoked owners receive no state. The response retains accepted
declarations, current state/attempts, exact expansion bytes/hash/source fence,
and empty generators. An uncancelled guard refusal returns unavailable because
the wire cannot carry it yet. Cancellation takes precedence and remains readable
while the owner drains.

Ordinary delegation now retains immutable unit, purpose, delivery and billing
identity. Its child still has the requested narrowed authority and a parent.
It creates no second billing_awards row. Without that identity, controller
own-unit authorization could not recognize narrowed award descendants.

Ported the concurrent-classification behavior from the inventory's
TestCreateRun_ConcurrentWritersNeverShrinkTheSecretArgs into
TestConcurrentClassificationNeverShrinksTheIntakeSeal. Two overlapping final
AcceptPlan calls use the real W6 settings and seals, converge on one accepted
spec and retain/redact the intake classification. W4 validation alone remains
a contract fake. PostgreSQL interleaving is still required for the native cell.

New tests cover forged Principal fields, current revocation, tenancy, named
selection, complete claim-kind validation, declared dependency reads, child
generation, source-start/lapse/cancellation fences, secret declarations and pools,
token and machine ownership, narrowed unit identity, snapshot corruption and
owner replacement. No native acceptance skip was removed.

No legacy runtime API was deleted in this continuation: 0 legacy lines removed.
The earlier 20-line duplicate-declaration deletion remains part of this worktree.
StateStoreOld and the plan/claim/trigger/offer writers still need removal. Their
runtime consumers remain, and W1b's billing tests still call old recovery APIs.
W1b files were not edited.

Additional contract work needed:

- W2 Target needs the resolved concurrency key for the slot binding check.
- W1b needs CheckDataAccess on storeTx so data authorization stays in the
  committing transaction. Cross-run/memo/record read edges belong to that owner.
- W2's trusted machine provisioning must persist its account authorizer in
  created_by before owner consent can succeed for a provisioned machine root.
- A generation-zero materialized generator needs a consistent source-fence
  contract. Materialization retains generation zero before any award, but
  eval_units.source_generation, eval claim validation, owner log source checks
  and ExpansionForStore currently require positive source generations. This was
  found by reading the paths, not established by a native runtime test. Do not
  invent an execution attempt or self-claim to hide this gap.

New shared files in this continuation are pkg/controller/engine_contract.go,
engine_snapshot.go and engine_snapshot_test.go. No other package's caller was
migrated or deleted. The C1 document records CheckBinding, ReadOwnerSnapshot and
delegated award identity. No new interface, setting, environment variable, table,
service or compatibility path was introduced. CheckBinding implements C2's
existing interface. ReadOwnerSnapshot is the one local C1 API addition.

The three conflicted files still have resolved contents and unmerged index
entries. The lead must stage them because the brief prohibits worker Git writes.

## Merge resolution

The three conflicted files have resolved contents:

- `internal/nodehost/scheduler.go` retains W1's removal of the legacy automatic
  reset of lost jobs. Recovery remains with the reaper and reconciler.
- `pkg/controller/engine_contract.go` combines W1's implemented adapters with
  the amended controller declarations and private execution projection.
- `pkg/store/state_contract.go` keeps the amended materialization probe contract
  and the private bearer field.

There are no conflict markers in these files. Their index entries still show
unmerged because the swarm brief prohibits Git writes. The lead must stage the
resolutions and finish the merge.

## Implemented

Materialization-only misses return Dispatch without releasing a pending job,
claiming it or minting a bearer. Terminal, held and authorized source-hit behavior
is preserved. Controller responses carry persisted execution declarations,
settings, attempts, generations and expansion state. Expansion replay fills
ExpandedJob.Current from the persisted row.

Run hosts can cancel their own jobs. Held jobs receive one cancellation request
and keep their holder's result rights. The controller preserves the supplied
reason in the event. An identical run-host transition replay rechecks the live
owner fence, so a replaced owner cannot replay cancellation successfully.

NodeResult.Failure persists the masked JobFailure, including stage and structured
error details. ReadFailure checks the exact source generation and the accepted
recovery edge. A recovery holder can read only its own OnFailureOf source. A
terminal status alone supplies no synthetic SDK error. Failed writes roll back
state, attempt closure and events. Materialization retains a recorded failure.

ReadAuthentication reads bearer/session authority, complete ancestry, membership,
store time and bound unit facts in one snapshot. It retains the final job's
attempt fence after claim clearing and reads log sealing from the tagged subject.
Eval source generation, eval lease and elected controller fence remain separate.
Plan bindings read their owner lease. Here claim history survives requeue.
The unimplemented authentication cases are listed below.

ReadClaimProfile exposes the same enrollment owner used in transactional matching.
Matching re-reads enrolled total capacity instead of accepting a wider controller
observation. Kinds are checked as a complete set before selection: empty queues,
unknown/duplicate kinds and unauthorized mixed selections are refused. Invalid
resource observations fail even when the queue is empty. Named internal targets
still identify their one unit without a redundant Kinds list.

EngineController.Claim uses server enrollment, available-resource observations,
the shared store matcher and RunnerAdmissionPolicy. SignedRunnerAdmission calls
the engine owner's verified index and EvaluateRunner. A queue miss returns no
claim. A held award is never returned as a second bearer. The exchange adapter
returns the committed original plan/job award once and ignores caller-selected
team/run fields. Claim payloads include purpose, delivery, accepted engine,
source generation and known expiry bounds.

Member provisioning now creates a root without a session revocation parent.
Current membership still governs it. Its optional expiry is independent of the
issuing session. Ordinary delegation invokes C2's validation contract and keeps
its parent. ProvisionCapability, DelegateCapability and ExchangeCapability share
the same commit boundary and discard raw bearer output on errors.

Local and here awards without an execution deadline remain renewable through
their leases. They carry local/here executor identity instead of gaining a new
deadline or being labelled as an ordinary runner.

IntakePolicyFacts.SourceLocation supplies a server-verified acquisition location.
Intake replaces a proposed location with this fact and refuses a nonempty proposal
without it. Retry retains the original location and SHA. This prevents a typed
repository ID from authorizing a different checkout URL or local path.

Real W6 settings/seals now have a store integration test for resources, timeout,
requirements, classification retention, replay and explicit-zero refusal. Only
W4's still-declared plan validator is faked in that test. A separate contract fake
proves that resolved concurrency winners survive store persistence.

## Local C1 contract additions

The C1 source document describes these additions:

- IntakePolicyFacts.SourceLocation carries verified acquisition facts. W2's
  production intake policy must fill it after checking repository identity,
  credentials, local-store authority and the relative pipeline directory.
- NodeResult.Failure carries the full masked JobFailure. W9's result adapter
  still needs a wire field for its stage.
- Store.ReadClaimProfile shares enrollment between controller observations and
  transactional matching.
- Store.ProvisionCapability and Store.DelegateCapability return committed
  one-time issuance results for trusted adapters.

RunnerAdmissionPolicy is the controller's injection boundary for the existing
engine policy owner. The production adapter calls that owner. Component tests use
a fake because the engine functions remain stubs in this merged tree. Delegation
uses the same pattern for C2's ValidateDelegation. Neither seam creates a second
policy implementation. No table, environment variable, service, compatibility
alias or customer limit was added.

Deleted 20 lines of duplicate enrollment/secret-opener declarations. The amended
files now own those declarations and their implementations. No legacy runtime
API or archived test was retired in this continuation. The previous five test
ports remain in the W1 selection. Legacy API deletion is still required.

## Amendment audit

| Rows requiring W1 action | Result or remaining owner |
| --- | --- |
| A1-07 | Store persists typed concurrency winners. W6's real ResolvePlan currently omits concurrency entirely; its declaration is tested with a contract fake. |
| A1-08 | Complete kind/grant validation and mixed queue tests pass. The existing shared ranked scan remains the selection owner. |
| A1-11 | Verified source-location fact, refusal before insertion and unchanged retry source tests pass. W2 must install the real verifier. |
| A1-13 | Probe miss/holder/terminal/hit tests and controller adapter pass. |
| A1-14 | Store failure persistence/read and controller read adapter pass. Wire stage transport is still needed. |
| A1-15 | Owner cancellation, cooperative holder drain, replay fencing and controller tests pass. |
| A1-16 | Private execution projection and one-time claim delivery tests pass. |
| A1-17 | Expansion Current and the remote Snapshot use persisted state. Snapshot carries exact expansion bodies and empty groups. An uncancelled guard refusal still needs W9's missing field. |
| A1-18 | Existing zero/start/retry/disruption ordinal tests remain green, including Applied.Attempt. |
| A1-20 | Member roots have no session parent. Ordinary delegation uses C2's validator contract and retains immutable award identity. Other trusted provisioning origins still lack transaction adapters. |
| A1-21, A1-22 | Snapshot implementation covers persisted bearer/session and unit/log facts. Exact HTTP replay and the other credential classes remain open. |
| A1-23 | Store-backed engine adapters have component evidence. W2 still owns actual route registration and process authority. |
| A1-24 | AuthorityResolver and BindingChecker are implemented. Slot-key transport, transactional data edges and the final source-release adapter remain open. DataAccessChecker and record edges remain W1b work under the split. |
| A1-25 | Duplicate secret-opener declarations are removed. Existing secret preflight/identifier/transaction tests pass. Verified App delivery transport remains a W2 integration gap. |
| A1-26 | Enrollment reader, capacity recheck and controller claim adapter pass with a registered-profile fake. Actual enrollment persistence/transport is still needed. |
| A1-27, A1-28 | W1b owns accounting and payment settlement. Metered awards still fail closed. Joint lifecycle integration remains required. |
| A1-29, A1-31, A1-32, A1-33, A1-34, A1-35 | W1b owns blob/record/upload/receipt/read-edge persistence, quota settlement and its retention. W7/W2 own physical storage and signed transport. Their files were left alone. W1's transactional record interfaces still use fakes. |
| A1-38 | Claim payload and single-use exchange adapters pass. Launcher sync still needs the observation correlation contract below. |
| A1-40 | Existing owner reaping/controller reconciliation tests pass. Local/here no-deadline claims are fixed. Runtime readiness/drain integration remains W8 work. |
| A1-44 | Real W6 seal integration preserves intake classifications. Plan and expansion preflight tests remain green. |
| A1-45 | W6 owns admission-class precedence. Store sealing calls the shared resolver; no competing precedence rule was added. |
| A1-51 | No competing wire vocabulary or compatibility surface was added. W3/W8 runtime retirement, W4/W5 fixtures and W9 generation remain open. |
| A1-53 | No native acceptance cell was activated. T1/lead still owns the real placement drivers. |
| A1-55 | Existing first-window/reclaim tests pass. Here ancestry stays tied to its requester; unbounded local/here leases remain renewable. |

## Contract changes needed in other areas

1. W9 ReuseSource still cannot name MaterializeSource.Result by state sequence.
   ScheduleSnapshot still cannot carry the sealed GuardRefusal. W9 also needs to
   transport the JobFailure stage with the result error so the adapter can fill
   NodeResult.Failure without inferring it from text.
2. W2 must carry the authenticated exact request-body digest and operation into
   store result transactions. StateDigest hashes the C1 transition and cannot
   substitute for that body digest. AuthenticationSnapshot.Binding.Result is
   unfilled until that path exists, so terminal HTTP replay is not established.
3. W2 must supply the trusted identity/persistence boundary for console, peer and
   configured anonymous authentication. ReadAuthentication currently supports
   persisted bearer/session lookup and refuses the other classes. ProcessAlive
   belongs to the node host. Bootstrap, machine, service and Actions provisioning
   also need trusted origin checks inside the issuance transaction; the member
   origin is implemented, and the others return unavailable.
4. W8/W9 must specify launcher observation correlation across claim generations
   and what LauncherSyncResponse.Remove names. LauncherObservation has UnitID,
   JobName and NodeUID but no generation, and C1 has no committed Kubernetes
   identity mapping. A repeated unit ID alone cannot distinguish a stale Job
   after re-award. SyncLauncher remains a declaration.

## Remaining integration and cleanup

W1b must connect its reservation/settlement owner to award, start, renewal,
terminal result, finish and reaping. Existing persistBillingAwardTx is the
unmetered path, not a substitute for its accounting contract. materializationRecords
and nodeResultRecords still need W1b's implementation in their surrounding store
transaction. Blob, upload, log and payment declarations imported by the merge
were not represented as implemented by W1.

W2 must install the intake policy, plan secret opener and enrollment resolver,
connect the real route verifier and finish slot/data/source integration.
W8 must supply a working engine resolver to SignedRunnerAdmission. W3/W8/W9 must
finish their host/runner callers. SyncLauncher and Compatibility still have
declaration bodies. RunDetail reads persisted status but still needs signed
version-policy notices. Remote Snapshot is implemented with the guard transport
gap described above. The controller loops must invoke ReapUnits,
ReconcileControllerRuns and completion delivery under their elected leases.

StateStoreOld, legacy plan/claim/trigger writers and offer rounds still have
runtime callers. Their removal remains W1 work after migrating or deleting those
callers. W1b's billing tests still call legacy sweep/requeue APIs, so those files
were not changed here. No compatibility wrapper was introduced.

## Verification

All Go commands used GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-astra-W1/cache, GOTMPDIR=/tmp/sw-astra-W1/tmp and
TMPDIR=/tmp/sw-astra-W1/tmp. Test package parallelism was at most -p 4.

- Whole-repository `go build -p 4 ./...` passes.
- `go vet -p 4 ./pkg/store ./pkg/controller ./pkg/match ./internal/controllerwire`
  passes.
- The final W1 selection passes 228 top-level tests / 654 cases with no skips
  or failures. It includes the new binding and classification files below.
- All `^TestEngine` controller tests pass: 18 top-level tests / 28 cases.
- Full pkg/match, internal/controllerwire, internal/acceptance and tests/acceptance
  packages return success. The acceptance output contains 2,995 skipped tests.
  Scaffold parents passing do not establish native cells.
- `bin/check-api-spec.sh` and `git diff --check` pass. Edited Go files are formatted.
- No local user.name, user.email or core.worktree override is present in Git config.

To reproduce the store selection, extract top-level Test names from the files in
the previous W1 handoff plus these eleven files, join them into an anchored -run
expression and run the pkg/store test command with -count=1:

    amendment_reconcile_test.go amendment_claim_test.go failure_runtime_test.go
    authentication_runtime_test.go plan_settings_integration_test.go
    provision_runtime_test.go delegation_runtime_test.go source_intake_test.go
    unit_local_claim_test.go
    binding_runtime_test.go classification_race_test.go

These are component proofs over SQLite and contract fakes. Native HTTP/api.sock,
Postgres, Windows/Darwin, Docker, the full legacy suite, race checks, lint and
delivery pipelines remain lead checks. Aggregate API snapshots and the rewrite
changelog still need regeneration after integration. No Sparkwing binary,
provider call, daemon, Git write, deployment or release was run.

## Shared files and friction

Outside pkg/store, changed production files are internal/nodehost/scheduler.go
(merge resolution), pkg/controller/engine_contract.go, engine_store.go and the
new engine_claim.go and engine_snapshot.go. engine_amendment_test.go and
engine_snapshot_test.go add controller proofs. The C1
contract document and this handoff record the seams and verification limits.
The shared Store struct gains only the private C2 validation dependency field.

The merge duplicated interfaces already implemented in other W1 files. W9's
amended private job type also required replacing the old public projection.
The first broad selection found the session digest lookup's missing reviewed
SQL-scope exemption; its globally unique digest precedes tenant resolution.
Real W6 integration exposed missing concurrency resolution. Two recovery fixtures
needed an explicit remote-owner deadline; a separate test exposed the real local
deadline refusal, which is fixed. No failing final check was retried unchanged.

The latest continuation also required terminal-node timestamps, complete seal
columns and a secret principal in the final-schema fixtures. Those fixture errors
were corrected before the binding and snapshot checks passed. PostgreSQL's
read-only snapshot excludes locking SQL, so named authorization uses the same
matching-input reader with its locking parameter disabled. Actual PostgreSQL
execution remains untested here. The generation-zero generator issue above is
an inspection finding that still needs a runtime reproduction and amendment.

The scratch directory is removed before the final report. The merge index and
the remaining implementation work require continuation.
