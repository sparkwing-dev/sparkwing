# Controller API contract

<!-- design:begin -->

`internal/controllerwire` owns the HTTP request and response types shared by
fetched engines, the controller, api.sock, local status and remote status.
`capability.Routes` owns authentication, bindings, liveness and retry policy.
`pkg/controller.EngineController` adapts these types to C1 transactions.
`client.NewEngineClient` supplies the same interface to every fetched engine.
The client executes this contract over HTTP. The server adapter retains stub bodies.

From launch freeze, the controller preserves the API emitted by every engine
for twelve calendar months after that engine's release. Matching engines own
their SDK node protocols. Older engines continue to work until an explicit
signed revocation or minimum-version policy takes effect. The end of a support
window alone does not refuse a run. Additive response fields and unknown error
codes remain readable. Required fields and types still validate. Errors unwrap
one envelope and use `apierr.DecodeInner`.

| Export | Contract |
| --- | --- |
| `Compatibility`, `CompatibilityFor`, `APIMajor` | API identity and per-engine compatibility window, served by `/api/v1/capabilities?engine_version=` from verified release metadata. |
| `CreateRunRequest`, `CreateRunResponse` | Shared intake, allocated team number, first seal, replay and one-time here bearer. |
| `PlanSubmission`, `PlanAccepted` | Second-seal proposal and committed plan/spec hashes. Restricted `CheckoutDeclarations` retain pipeline/default layers and origins; the store resolves them against its own request overrides and ceilings. |
| `ClaimRequest`, `ClaimResponse`, `ClaimedUnit` | Queue or named selection and a server-derived unit award. Granted identity, pools and billing never come from the request. Runner software version is checked by `engine.EvaluateRunner` before new awards; draining renewals stay available. |
| `RenewalRequest`, `RenewalResponse` | Shared keeper activity, expiry, cancellation and watched jobs. |
| `JobResult`, `JobTransitionRequest`, `Applied` | One transition/report boundary, authorized output records and replay result. |
| `ExpansionSubmission`, `ExpansionAccepted`, `ExpandedJob` | Exactly one success body or structured failure and the persisted topology/settings. |
| `ReuseSource`, `ReconcileRequest`, `ReconcileResponse` | C1 reconciliation over a proposed source, with persisted source outcome and committed claim. |
| `FinishRequest`, `CancelRequest`, `RetryRequest` | Owner finish, cooperative cancellation and retry lineage. |
| `RunDetail`, `JobDetail`, `AttemptDetail` | Shared safe status response. Inputs come from `runspec.DisplayRequest` or `DisplaySpec`. |
| `ScheduleSnapshot`, `ScheduleJob`, `ScheduleGenerator` | One consistent graph read, preserving declared empty generators. |
| `Controller` | Shared client operations; the route table supplies retry behavior. |
| `controller.ExpansionForStore` | Combines the wire body with C2-derived authority into C1 `ExpansionSubmission`. |
| `controller.IntakeForStore` | Carries tagged session/capability authority into C1 `IntakeRequest`. |
| `controller.EngineController` | Shared server adapter for lifecycle, claims, renewals and reads. |
| `client.EngineOptions`, `NewEngineClient` | Authenticated HTTP transport, including api.sock dialing. `Launcher` explicitly selects the deployment launcher claim route. |
| `nodehost.RemoteOwnerStore` | Consumes the shared wire through C1 reconciler and expansion interfaces. |

The expansion adapter derives the source and actor from the authenticated
route/binding. It compares the immutable source generation and preserves
failure code and masked message. Both success and failure call
`Tenant.AcceptExpansion`; there is no separate failure writer. A successful
empty expansion has an explicit empty jobs array. Replays hash the same
canonical submission, including structured failure.

Snapshot reads use `Tenant.ReadSnapshot` and `RunSnapshot.Scheduling`.
Controller settlement and remote owners consume that projection and the same
`store.Decide`. A client cannot assemble a schedule from separately timed
job reads. Reconciliation uses `ReconcileRequest` and `ReconciledNode`.
An offered memo or retry source cannot supply its own outcome.

`PlanSubmission.Declarations` reaches `PlanAcceptance.Seal.Declarations` and
`settings.ResolvePlan` unchanged as author declarations. The store validates its
restricted fields/origins and computes winners against the persisted intake and
ceilings. `Spec.Declarations` retains both layers for expansion and retry.

`CreateRunRequest.IdempotencyKey` reaches `IntakeRequest`, including retry
intake. The store derives the scope and digest and stores them on the run, so
executable and published intake both replay without creating another run.

HTTP bodies cannot select an actor, granted identity, billing provenance or
claim fence. C2 middleware authorizes the complete route alternative and C1
rechecks it in the mutation transaction. A session reference survives intake
and approval writes. Capability prefixes remain separate claim-fence facts.

`RunDetail` is a display boundary. It exposes the redacted input projection,
status, attempts, timings, engine version, version-policy notices and tagged log subjects.
Run output and the dashboard render the same `engine.Notice` data. Notices are
read-time policy facts, so a later signed warning does not rewrite the run seal. Claim responses are a
private execution boundary and may carry sealed execution inputs and a bearer.
Neither response may be substituted for the other.

Runtime conformance must drive the same lifecycle through HTTP and api.sock,
exercise exact result replay, stale-fence refusal and expansion failure, and
check old-engine request fixtures across the guaranteed window. Stub bodies do
not establish these behaviors.

### Public run observations

`controllerwire.RunReader` uses the same authenticated transport as `Controller`
for CLI status views. `ReadRun` retains one `RunDetail` for local and remote
connections. Its optional `steps` field carries `StepDetail` values when a client
requests step history. No execution seal or raw store row enters these views.

The W9 observation types complete three existing C2 GET routes:

| Route | Request | Response |
| --- | --- | --- |
| `/api/v1/runs` | positive `limit`, opaque `cursor`, optional exact `parent_run_id` | `RunPage` with `runs`, `next_cursor`, `truncated` |
| `/api/v1/runs/{run}/steps` | stable ID or team number | `RunSteps` with `steps` |
| `/api/v1/runs/{run}/receipt` | stable ID or team number | `Receipt` |

Pages contain public run details. A truncated page supplies a new nonempty
cursor. A final page has an empty cursor. The parent filter selects direct
children. CLI tree traversal consumes every page and refuses repeated run IDs
or cursors. The server still authenticates every read through C2's route policy.

Steps identify `job_id`, step `id`, label, attempt, state, masked reason and
timestamps. Receipts retain the controller's identity hashes, job-named steps,
cost and settlement facts. Input hashes exclude secret arguments. The CLI does
not calculate replacement costs or receipt hashes. These W9 wire additions need
W2's final read adapters. Transport fakes establish client behavior only.

Status follow compares the observed detail, including read-time policy notices,
and refreshes descendant trees independently of the parent's state sequence.
An interrupted, timed-out or otherwise incomplete observation exits 3. Invalid
flags exit 2. Terminal failure exits 1 unless `--exit-zero` is selected.

### Listing, deletion, logs and reviews

These W9-owned wire additions use existing C2 routes. W2 must implement the
response projections and W7 must supply persisted log completeness. Transport
and CLI tests use fakes while those owners remain stubs.

`RunQuery` carries `pipeline`, `status`, repeated `branch` and `repo`, `sha`,
`root_only`, `error`, `search`, and RFC3339 `started_after`, `started_before`,
`finished_after` and `finished_before` filters. Filters apply before the limit.
Pipeline, status, branch and SHA accept a leading `!` exclusion. Search terms
match case-insensitively across public identity, pipeline, ref, SHA and masked
failure text. A leading `-` excludes a term. Repeated positive values are ORed,
exclusions subtract, and different filter categories are ANDed. Time bounds are
inclusive. Unfinished runs do not match a finished-time bound.

Pages are ordered by descending allocated team number. `after_number` is an
exclusive lower bound. Cursors retain filters and the page snapshot. The CLI
keeps its initial follow boundary fixed and deduplicates emitted IDs because
a newer run can fail before an earlier run. Moving that boundary after every
poll would lose the earlier run when it later matches a mutable filter.

CLI list JSON projects compact selection metadata from `RunDetail`. It does
not repeat inputs, jobs, attempts or log subjects. A `kind: page` record carries
`next_cursor` and `truncated`. Quiet JSON emits ID objects and sends continuation
metadata to stderr. Pipeline totals consume every matching page. Failure groups
use job IDs or job/step IDs and count distinct runs in the selected page.

`RunPruner.DeleteRun` uses `DELETE /api/v1/runs/{run}` and returns `RunDeletion`
with canonical `run_id` and `deleted`. False means already absent. Retention pins
must fail with a structured error rather than report a successful no-op. The
server owns deletion authorization and financial retention. Age-based CLI prune
collects all terminal runs strictly before its cutoff before deleting any row,
so deletion cannot invalidate selection cursors. A failed selection deletes none.

`LogReader` reads `/runs/{run}/logs`, `/runs/{run}/jobs/{job}/logs`,
`/runs/{run}/logs/search` and `/runs/grep` under `/api/v1`. `LogQuery` carries
explicit head/tail presence, an inclusive line range, grep text, since, tree,
event selection and an opaque resume cursor. `LogRecord` contains either a tagged
`blob.LogSubject` or a lifecycle event. Events may carry job/step identity.
Output subjects retain attempt and generation or trigger/plan/eval identity.
No synthetic job names replace owner logs.

`LogPage` carries records, next cursor, truncation and completeness. Truncated
pages require a progressing cursor. An idle live page retains a nonempty cursor.
Complete means terminal state and final persisted segments have both been
observed. CLI follow drains available pages before polling and cannot treat
missing final segments as completion. `LogSearchQuery` reuses `RunQuery` and adds
job, pattern and per-subject maximum matches. Zero means uncapped. Search can
return no matches with a continuation because the cursor advances through
candidate runs. No implicit tail limit was added.

`RunReview` shares `ReadRun`/`ReadSteps` and adds pending or selected-run approval
reads, approval decisions and job/step annotations. `ApprovalDecision` carries
resolution and optional comment. Its idempotency key travels in the header, and
the controller derives the approver from authentication. `ApprovalResult`
preserves the resolved gate and replay marker. Pending gates use
`GET /api/v1/approvals?pending=true`; run history and decisions use the existing
run approval routes.

`Annotation` retains level, message, file, line, column and end line. Public job
and step details carry optional annotation arrays. `AnnotationWrite` optionally
selects a step and posts to the existing job or job-step annotation route. Reads
contain masked text. The existing C2 table has no run-scoped annotation write or
ordinary-member annotation grant. Those are W2 contract gaps, not approved
feature restrictions. W9 adds no route or grant to bypass them.

`EngineOptions.Launcher` selects `/api/v1/launcher/claim` for every launcher
selection, including node-only and named jobs. Unit kinds cannot distinguish
a runner's `nodes.claim` credential from a launcher's `claims.launch`
credential. This transport option grants no authority. C2 authenticates the
selected route. Other clients use the named run/job routes or the separate
runner run/job queues. A mixed trigger/job queue request needs a shared C2 route
before that combination can be transported in one claim.

<!-- design:end -->
