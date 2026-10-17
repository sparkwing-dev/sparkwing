# Controller API contract

<!-- design:begin -->

## Operator team creation (decision 114)

OperatorManager.CreateOperatorTeam(ctx, OperatorTeamCreate) returns
OperatorTeamCreated through POST `/api/v1/operator/teams`. Input contains
`slug` and optional `display_name`. Output contains `team`, `account_id`,
`capability`, `prefix` and `grants`. The response carries the first admin's
one-time credential. C2's RetryNever policy prevents transport and HTTP retries.
A lost response cannot be recovered by repeating creation. Existing or retired
team slugs return conflict. C2 and C1 specify operator checks and the atomic
creation transaction.

`ExecutionJob.output_record` and `manifest_record` carry C1's exact committed
record keys on private reconciliation and expansion responses. Terminal results
may carry selectors with attempt zero. `ReuseSource.result` carries their run,
job and state sequence independently of an execution attempt. Hosts preserve
both identities and use the committed keys for output reads.

## Amendment 1: private execution and claim wire

The CLI host-state adapter implements `nodehost.NativeHostStore` using the
declared C3 plan, step, completion, reconciliation, expansion and renewal
operations. Renewal reads current compute activity on each call. Actor authority
and owner fences never enter these HTTP bodies. The accepted-plan response does
not supply owner authority, so the adapter leaves that optional field absent.
Private `ReadSnapshot`, `ReadNode`, `TransitionRun` and `TransitionNode` remain
separate C1-shaped dependencies. The scheduling snapshot cannot replace them.
Their authenticated remote transport and the elected local factory still need
W2/W3 integration. Fake dependencies establish only the adapter boundary.

The selected-engine CLI describe handoff reads the acquired SDK's protocol
response for the configured entrypoint. The invoked alias, required pipeline/job
argument metadata, risks and secret classifications survive that handoff.
Source or protocol failure returns no successful description. Catalog caches
cannot supply this selected-engine describe handoff.

Local, standalone and here hosting share one session lifecycle. The award supplies
the run-host kind and trigger owner fence. The trigger fence retains the run ID,
holder and generation with an empty name, matching C1's persisted projection.
Binding supplies the resolved authority reference, capability prefix, storage,
engine resolver and plan cache. It cannot replace the owner fence, request,
acceptance or granted runner. Cancellation preserves its cause through cleanup.

Owner adapters serialize `runspec.Request` into `RunInput.SealedRequest`, as
required by C3's host boundary. `RequestHash` remains the enclosing request seal's
canonical hash. `RunInput.RunID` and `AcceptedRun.RunID` identify the awarded
execution, including a retry whose original request/spec retain their source
run ID. Local and here handoff share the award projection and reject invalid
seals or mismatched claim bindings before native binding. Initial intake receipt
validation separately requires the new seal's run ID to match its receipt.

`ClaimedUnit.HostContext` bounds source/host binding and execution by the earliest
caller, binding, owner or parent-capability deadline. Local, here and runner
hosting share it. An already expired award stops before binding. Lease expiry
still belongs to renewal and the store clock. Host binding receives a separate
copy of the granted runner labels.

`JobResult.Failure` carries the complete persisted `nodeproto.JobFailure` as
`{job, stage, error}`. It replaces the error-only `JobResult.Error` field.
The adapter passes the failure to `NodeResult.Failure` and derives the masked
display message from its canonical inner error. C1 validates job identity,
stage, error and size before committing it with the result. Report replay
includes that complete failure. Recovery reads preserve its code, message,
hint and details, including unknown additive codes. No stage is inferred from
an outcome, reason or error string. Native hosts still need to report through
this boundary.

`JobTransitionRequest.SourceAttempt` carries C1's optional source ordinal without
collapsing omitted and explicit zero. Holder start, retry, report and unstarted
release require it. Initial start uses zero. Later reports use the captured
ordinal returned by `Applied.Attempt`. Other actors and causes omit it. The
adapter forwards it unchanged to `NodeTransition.SourceAttempt`; C1 checks the
locked row and includes it in immutable replay. Native hosts must send the
captured ordinal rather than read a newer attempt before reporting.

`Controller.RecordStep` accepts `StepReportRequest` on the existing C2 routes.
State `running` selects `steps/start`, `skipped` selects `steps/skip`, and `passed`,
`failed` or `cancelled` selects `steps/finish`. A positive `source_attempt` names
the observed attempt. The body contains no actor, holder, team or generation.
W2 derives those fields from authorization and current state, compares the source
ordinal with that state, and calls C1 `RecordStep` with the derived
`AttemptIdentity`. C1 rechecks that identity in the committing transaction.
`error` is already masked text. Optional start and finish timestamps retain
nanosecond precision. Success is `StepRecorded{recorded:true}` after persistence.
False, absent or malformed acknowledgements fail. Exact-body retries retain the
original source ordinal and timestamps. W2's native route/store binding and W3's
remote executor adapter remain open.

`ReuseSource.Result` transports C1's materialized-result source as
`{run_id, job_id, state_seq}`. It identifies the exact persisted terminal state
when no execution attempt produced that result. It is exclusive with a nonzero
`Attempt`; C1 validates the source, lineage, read edges and state sequence in its
transaction. No source outcome is accepted from the caller. Existing attempt
sources keep their team/run/job/generation/ordinal identity.

`ScheduleSnapshot.GuardRefusal` preserves the accepted guard's refusal alongside
`CancelRequested`. The controller projects both from C1's consistent scheduling
read instead of refusing an otherwise valid snapshot. C1's decider retains
cancellation precedence. Remote owners must consume both fields before dispatch.
These W9 wire additions carry existing C1 facts and change no foreign contract.

Claim with a single trigger kind and RunID uses `/runs/{run}/claim`, including
the here Award under LiveHereClaim. Other Claim requests select one ordered scan
through `/runners/claim`, or `/launcher/claim` when
EngineOptions.Launcher is true. Selection cannot infer launcher identity from unit
kinds. The server checks every kind's grant and derives granted runner identity,
repository authorization, pools and billing from enrollment. New claims are not
automatically retried after response loss. Store lease recovery resolves uncertainty. Named trigger/job claims and
reconciliation also disable automatic retries because they can award a new bearer.
Here intake/retry never transparently retries. Idempotent replay retains the run
but cannot return its raw HereBearer. A lost first response is a failed handoff,
with a fresh trigger request as the recovery and the abandoned reservation expiring
at its original claim_by. Resume uses the retained private bearer, bound to the controller URL and run.
Local transport also binds the absolute API socket path. Local clients use a raw
socket transport under the same C2 route retry policy as HTTP clients, without
an additional transport retry loop.

The prepared-request initial-trigger component calls `Controller.CreateRun` once,
preserving a supplied idempotency key or generating one before submission. Its
receipt exposes run ID, team number, replay, observed terminal outcome and a
masked error. The component validates the returned first seal and initial run
identity before handing the complete private response to here hosting. A fresh
here receipt requires its bearer; a replay cannot contain one. Received here
authority reaches the retaining handoff even if cancellation arrived during
intake. Detached submission does not observe the run. An unfinished or failed
observation exits 3 independently of any recorded outcome.

Committed-source preparation reads the execution commit's project configuration
without changing the checkout. `Git.SHA` retains that execution commit.
`Source.Ref` retains the pipeline commit selected by the configured source or
explicit pipeline revision. Both selections resolve to exact commits. A configured
source pin and explicit pipeline revision must agree. `Source.Entrypoint` retains
the execution configuration's Go type identity, while
the registered name supplies `Request.Pipeline`. GitHub repository identity requires
a resolved positive numeric ID. Typed repository identity and origin HEAD supply
no App trust or authority. Clone URLs cannot carry credentials.

Request preparation selects the registered pipeline name's argument schema,
preserves its secret classification, uses C4 request resolution and hashes the caller's captured
environment through `EnvironmentDigest`. Environment values stay with the private
capture owner. Validation diagnostics mask supplied argument tokens through the
shared CLI sanitizer. Supplied idempotency keys survive preparation and intake.

Committed-source preflight checks that origin's remote-tracking refs contain both
selected commits. Another remote cannot satisfy it. Inspection acquires the exact
pipeline commit through `source.Acquire`, carrying the captured process base,
repository identity and acquisition location. The selected engine runs its
`pipeline describe` command in the acquired directory. The returned alias and
entrypoint must match the captured pair before its argument metadata is accepted.
Acquisition resources close after success, failure or cancellation. Policy notices
pass through the existing masked writer after environment registration, and a
failed write fails preparation. The shared
process builder and sanitizer own credential filtering and diagnostic masking.

These components have native Git fixtures, a selected-engine shell fixture,
fake acquisition/controller and HTTP/api.sock transport evidence. The Git tests
inject the declared process-environment boundary.
Native numeric-ID lookup, W3 source acquisition and W8 engine resolution are
wired. Private environment retention remains a binding. Working-tree upload
is integrated below. Native selected-engine process-tree cancellation remains unverified.
The public command now uses this prepared request and sealed intake path.
GitHub numeric-ID lookup and working-tree source binding use the integrated paths below.
Captured environment delivery still needs a native binding. Initial here intake requires a contract for persisting
the required reason and refuses before submission until that contract exists. Neither `CreateRunRequest` nor C1 `IntakeRequest` carries
that reason. It must survive intake and replay before the CLI can replace the old
path without dropping it. No foreign contract is changed by this component.

Sealed here resume and retry follow public logs with the caller's display
credential. The retained bearer selects the named owner claim, and the fresh
award capability binds the host. The first interrupt requests cancellation with
the caller's credential while hosting continues. A second interrupt cancels
hosting and observation and joins both, including any pending cancel request.
Host completion drains final persisted log segments. A log failure does not
abandon a live host. An incomplete observation returns exit 3 independently of
the recorded run outcome. These CLI adapters still require native shared-host
construction and persisted-log completeness from their owning areas.

NewDataClient uses the same HTTP/api.sock transport to provide blob.DataSigner
and blob.RecordStore. Each call reauthorizes server-derived scope/uploader facts.
The signed-object client never receives the reusable controller credential.

### Shared data messages

`internal/controllerwire/data.go` mirrors the registered C2 HTTP handlers. The
controller derives authority and uploader identity; no client authority, read-set
or ref evidence is accepted in a request.

| Route | Request | Response |
| --- | --- | --- |
| POST `/api/v1/data/upload` | `digest`, `size`; stable Idempotency-Key per call | `upload_id`, `url`, `headers`, `expires_at`, `sha256`, `size` |
| POST `/api/v1/data/commit` | `upload_id` | `digest`, `created_at` |
| POST `/api/v1/data/download` | `digest` | `url`, `headers`, `sha256`, `size`, `expires` |
| POST `/api/v1/data/has-many` | `digests`, optional `deadline` | `present` |
| GET `/api/v1/data/records` | flattened `kind`, `name`, `ref`, `pipeline`, `job`, `key`, `deadline`, `verified_default_only` query selectors | `found`, optional flat `record`, `members` |
| POST `/api/v1/data/records` | flat logical record selectors, digest, optional manifest/expiry, members | flat canonical record |

Record handlers and the client use the shared DataRecordWriteRequest,
DataRecordLookupResult, DataRecord and DataMember DTOs. A verified-default lookup
accepts the server-selected nonempty verified ref while checking every other
record selector against the request. An ordinary lookup requires an exact ref
match. Invalid query escaping returns `bad_request` before storage access.

The private local-host `data-authority` endpoints return C1's canonical live
storage projection under the independently authenticated owner or node award.
Native clients compare every byte/record authority argument against that captured
projection and refresh it after each committed attempt start. Signed transfers
carry object headers only. Output upload, record commit, verified download and
shared log publication are exercised through the real controller HTTP handlers
with an in-process transport.

Record, memo and depcache HTTP responses do not carry C1's canonical read-set
proof or complete source metadata. Cache adapters fail with `policy_missing`
until that foreign schema contract is completed; they do not reconstruct proof
from caller selectors. Output reads use persisted record keys and verified bytes.
Upload and commit retries retain their original key/body. Explicit request
budgets remain context deadlines and exhaust as `blob.ErrBudget`.

ClaimedUnit includes accepted engine, source generation and owner/attempt/parent
expiry deadlines. Its sealed request carries Source.Location. CloudJobAward consumes
that immutable source projection. SyncLauncher returns held awards and stale Job
identities without raw bearers. `LauncherObservation.Award` is the existing
`capability.LeaseRef` tuple of kind, ID, holder and generation. This is the
awarded unit's generation; an eval uses its own lease generation, separately
from the immutable source generation. The observed Job carries namespace, name
and UID, and `node_uid` identifies its reserved node instance. Observations
cannot replace the stored fence or authorize a cleanup by themselves.
A reservation-only observation carries the same award LeaseRef, `node_uid` and
`reservation_id`, and omits `job`. It cannot also carry a Job reference.
`LauncherSyncResponse.RemoveReservations` returns `LauncherReservationRef` values
with that exact award, NodeUID and ReservationID. The store proves the original
bootstrap award belongs to the authenticated launcher's ancestry, even after
expiry, cancellation, exchange or a later generation. Unknown, forged and foreign
award fences refuse the whole response. A live award remains in `Awards`.
The launcher persists the award and reservation on the node. Sync observes
reservations even when no Job was created. It releases only the matching retired
reservation after proving the named Job absent and every non-DaemonSet pod gone.
Parent credential expiry alone never authorizes release. An unexpiring service
root is valid when the award's execution deadline is bounded.
`LauncherSyncResponse.Remove` is a list of `LauncherJobRef`, carrying the exact
namespace, name and UID required as a Kubernetes deletion precondition. A name
alone cannot identify an instance after replacement. Every transport, decoding
or validation error returns an empty sync result, so a partial decode cannot
expose private bearer material. The old unfenced
unit-ID/Job-name observation and string cleanup list are removed. W8 must retain
award annotations and the actual Kubernetes Job UID, submit observations and
delete with the UID precondition before releasing its reservation. W1/W2 must
compare the reported award with stored ownership and reconcile only the
authenticated launcher's awards. Store and launcher component tests exercise these paths. Native Kubernetes enforcement remains an acceptance obligation.
Exchange presents the bootstrap in the authenticated
transport and returns the exchanged ClaimedUnit once. The controller never mints
replacement bootstraps from an observed Kubernetes Job. Lost bootstrap delivery
recovers through lease expiry and re-award.

ReconcileRequest.MaterializeOnly is sent unchanged to C1. ReconcileResponse.Job is
ExecutionJob, which preserves raw declaration/hash, execution settings, retries,
fences, lease/cancel timestamps and expansion state. It is restricted to execution
authority. RunDetail and JobDetail remain safe public display projections.
ExpandedJob.Current carries this same current state on first acceptance and replay.
ScheduleSnapshot.Expansions carries accepted body/hash and source fence, including
empty expansions, for deterministic code reconstruction without adding topology.

Controller.CancelJob reaches the scoped cancellation route. Controller.ReadFailure
uses the source-generation query and returns a persisted nodeproto.JobFailure or
JSON null when that source has no recorded failure, under a declared recovery
edge. Null is not a synthetic failure and is not a valid successful response for
other controller operations. A present failure requires its structured inner error. Actor/grants never enter request bodies. Adapters map
private wire state directly to persisted state rather than inventing pending rows,
retry counters or failures. Applied.Attempt from start/retry supplies the execution
ordinal. The initial zero attempt is not a synthetic attempt for owner-unit logs.



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
| `StepReportRequest`, `StepRecorded` | Attempt-specific step state and acknowledged persistence on the C2 start/finish/skip routes. |
| `LauncherObservation`, `LauncherJobRef`, `LauncherSyncRequest`, `LauncherSyncResponse` | Award-fenced observations and UID-preconditioned Kubernetes cleanup identities. |
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

`RetryRequest.Mode` is required: `failed` reuses authorized successful jobs,
while `all` executes the complete sealed plan. `RetryRequest.Validate` refuses
an unknown/absent mode or absent idempotency key before intake. The mode joins
the exact-body digest, so the same key with a different mode conflicts. Neither
selection changes the original request, plan, source authority, actor or captured
environment. The controller records the retrier as separate lineage.
`CreateRunResponse.RunID` identifies the newly allocated retry. Its `Request`
retains the original seal's run identity and hash, including across retry chains.
Consumers must not rewrite or equate those identities.

The CLI submits both local and profile retries through `Controller.Retry`.
It resolves and deduplicates all flag/stdin selectors before submitting any run.
Each selected source gets a separate idempotency key. JSON receipts expose only
source/new IDs, allocated number, replay and structured failure. Sealed requests
and one-time here bearers never enter output. A here retry retains its one-time bearer in an owner-only controller/run-bound
file before hosting. A missing native host returns incomplete exit 3 after
intake, retains the returned run ID in its JSON failure receipt and makes no
claim that hosting started. `pipeline trigger --resume` resolves a stable ID
or team number through the public reader, loads that binding and makes one
named trigger claim. The award supplies the request and accepted seals unchanged.
The runtime binder receives the complete award, including runner identity,
fences and deadlines, and owns source, storage and lease composition.
Native host construction remains W3/W8/W9 integration work. W1/W2 must
implement selected retry intake/materialization and consume the shared validator.
No other area's contract or authority rule changed with this W9 wire amendment.

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

The W9 observation types complete these existing C2 GET routes:

| Route | Request | Response |
| --- | --- | --- |
| `/api/v1/runs` | positive `limit`, opaque `cursor`, optional exact `parent_run_id` | `RunPage` with `runs`, `next_cursor`, `truncated` |
| `/api/v1/runs/{run}/steps` | stable ID or team number | `RunSteps` with `steps` |
| `/api/v1/runs/{run}/receipt` | stable ID or team number | `Receipt` |
| `/api/v1/runs/{run}/attempts` | stable ID or team number | complete `RunPage` of public retry history, oldest first |

Retry history uses the same `RunDetail` projection as run status. It includes
the root, siblings and descendants within the authenticated team. Every returned
run must pass the caller's binding. A denied or invalid member fails the complete
response. The page has no cursor and is not truncated. Current retry lineage
comes from each execution row, while its inputs retain the original request and
spec seals. Raw reporting rows and invocation metadata never enter this response.
Equal creation timestamps sort by run number. Traversal detects cycles by
visited identity and imposes no retry-chain depth limit.

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

`RunPruner.DeleteRun` uses `DELETE /api/v1/runs/{run}` and returns only an error.
Success requires the server's empty HTTP 204 acknowledgement after deletion and
log cleanup. The client reports the selected run without guessing whether a row
was removed or already absent. The server owns deletion authorization, retention
and financial records. Age-based CLI prune
collects all terminal runs strictly before its cutoff before deleting any row,
so deletion cannot invalidate selection cursors. A failed selection deletes none.
Cancel, retry and prune preserve the supplied stable IDs and numeric selectors
through flags and stdin. The CLI never prepends a run ID prefix or resolves a
number outside the authenticated controller.

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
selects a step and posts to the existing job or job-step annotation route.
Annotation and summary routes resolve numeric run selectors within the current
credential's team before checking the awarded job binding. Reads
contain masked text. `RunReview.AppendAnnotation` returns only an error and
requires an empty HTTP 204 acknowledgement. The CLI emits the selected run, job
and optional step after success. It has no transition or replay receipt to print.
Native process reports use the same annotation transport and retain every source
location field. C1/C2 must persist column and end line before those fields can
complete the controller path.
The existing C2 table has no run-scoped annotation write or
ordinary-member annotation grant. Those are W2 contract gaps, not approved
feature restrictions. W9 adds no route or grant to bypass them.

`EngineOptions.Launcher` selects `/api/v1/launcher/claim` for every launcher
selection, including node-only and named jobs. Unit kinds cannot distinguish
a runner's `nodes.claim` credential from a launcher's `claims.launch`
credential. This transport option grants no authority. C2 authenticates the
selected route. Other clients use the named run/job routes or the separate
runner run/job queues. A mixed trigger/job queue request needs a shared C2 route
before that combination can be transported in one claim.

### Sizing, pool rules and credential management

These W9 wire projections complete existing C2 routes. W2 must implement the
server projections and apply C1/C2 authority in its transactions. No route,
authorization grant or root-provisioning rule changed here.

`SizingManager.ListSizing` reads `GET /api/v1/sizing` with optional `pipeline`,
positive `limit` and opaque `cursor`. The controller applies the pipeline filter
before pagination. `SizingPage` contains `sizing`, `next_cursor` and `truncated`.
Each `SizingDetail` has separate repository and pipeline identities, the
controller-selected source and drift, memo exclusions, rollup measurements and
job-named rows. CPU/memory distributions, sustained charges, waits, pins, floors,
plan identity and prior measurements survive JSON. Clients do not choose the
admission source or reconstruct legacy profile-key encodings.

The CLI uses `RunReader` to calculate ordinary run statistics from every matching
root-run page. Counts distinguish success, failure, cancellation and active work.
Success percentage includes all terminal runs. Durations use both timestamps,
nearest-rank P95 and an overflow-safe integer mean. Selection must finish before
any totals are emitted. No fixed history-row cap remains.

`SizingReset` and `SizingResetSummary` allow the CLI to consume a controller
reset without opening its database. The summary retains deleted/cleared row,
sample and floor counts. Explicit resource pins survive reset. C2 has no reset
route. The HTTP adapter returns `policy_missing` before any request. W2 must
specify the route and authority, and W1 must implement the reset, before this
feature is integrated. Fakes establish selection and rendering only.

`PoolManager` uses `GET /api/v1/pools`,
`POST /api/v1/pools/{pool}/rules` and
`DELETE /api/v1/pools/{pool}/rules/{rule}`. Lists retain server-granted runner
reservations and rule IDs, repository selectors, branch selectors, optional
`github` source and the route bit. An allow request sends the complete selectors
and an idempotency key in the header. The result returns the same rule plus its
server ID and replay marker. Deletion returns pool, rule ID and whether it was
removed or already absent. Repository strings are proposals, never proof of
repository authority. The controller verifies that authority independently.

`TokenManager` binds all operations to one namespace. Team commands use
`/api/v1/tokens` and member/runner types. Operator commands use
`/api/v1/operator/tokens` and service/deployment types. List reads accept `kind`
and `include_revoked=true`. Single-prefix reads and revocation use `/{token}`.
Creation and `/{token}/rotate` carry idempotency keys in the header. Retried
requests preserve their bodies and keys.

`TokenMetadata` has prefix, kind, principal and exact scopes, with optional team,
subject, repository/pool grants, metering and Unix-second lifetime fields. Raw
bearers appear only in creation and rotation results. Those results include
metadata, and rotation also returns the old prefix and its revocation time.
Revocation returns the prefix and confirmed revoked state. Metadata reads never
include a raw bearer. The issuer derives grants, membership and billing facts.
The client does not expand any scope to a wildcard or grant console authority.

Creation TTL omission selects the server default. Explicit zero requests no
expiry, subject to issuer policy. Rotation zero or omitted TTL preserves the old
remaining lifetime. Grace defaults to 24 hours in the CLI. The wire stores whole
seconds, so fractional seconds are refused rather than rounded. Negative TTL or
grace fails before connection resolution. No maximum lifetime was introduced.

Lists use NDJSON and plain prefixes. Creation and rotation JSON retain the raw
bearer and metadata in one document. Plain emits the new bearer once. Revocation
JSON retains its result and plain emits the prefix. Structured controller errors
pass through without replacement envelopes.

### Secret management and runner observations

W9 adds `SecretManager` and `RunnerReader` projections on existing C2 routes.
W2 must serve them, while W1/W2 own persistence, encryption and authorization.
No route, permission alternative or credential rule changes. Fake-backed CLI
and transport tests do not establish encryption, pool preflight or release.

`SecretManager.ListSecrets` reads `GET /api/v1/secrets` as `Secrets`. Its
`SecretMetadata` rows retain name, principal, pipeline, masked/shared/bound flags,
`only_on` pool, and creation/update seconds. Metadata types contain no value.
Unknown additive fields cannot copy raw values into metadata output.

`ReadSecret` uses `GET /api/v1/secrets/{name}?pipeline=` and returns a
`SecretValue`, with required `value` even when empty. A scoped read may return
the exact pipeline row or a shared unscoped fallback. Release to an executing
job remains a separate bound-claim authorization decision. Only an explicit
authorized management read returns the value to the CLI.

`WriteSecret` posts `SecretWrite` to `/api/v1/secrets`. It carries name, value,
pipeline, explicit masked/shared flags and optional `only_on`. The request
cannot select its owner or encryption result. `SecretWritten` returns the
encrypted/bound metadata and replay marker. The client refuses an unconfirmed
restriction, visibility change or unbound result. W1/W2 must persist `only_on`
and enforce it at plan acceptance and release, including here refusal.

`DeleteSecret` uses `DELETE /api/v1/secrets/{name}?pipeline=` and returns
`SecretDeletion` with the exact row selector and a deleted/absent bit. Deletion
has no pipeline fallback. Rotation posts to `/api/v1/secrets/rotate` and returns
`SecretRotation` with a nonnegative rotated count, skipped name/pipeline pairs
and replay marker. Any skipped row means the previous key is still needed.
Writes and rotations carry stable header idempotency keys and exact retry bodies.

CLI value input preserves stdin/file bytes, trailing newlines and explicit empty
`--value`. Local unscoped writes remain shared with local pipelines. Remote
visibility comes from explicit `--pipeline`/`--shared`. List JSON is metadata
NDJSON. Get JSON includes metadata and value. Pretty/plain get writes raw bytes
without a newline. Mutation JSON preserves confirmed results without echoing
write inputs. Name search is case-insensitive. Final server projections remain
integration requirements.

`RunnerReader.ListRunners` reads `GET /api/v1/runners` as `Runners`. Each
`RunnerDetail` retains granted name/class/location, separate free labels and
reserved pools, repository fences, metering, maximum concurrency, total capacity,
status and last seen. Active work retains separate run and optional job IDs.
Optional headroom carries fractional cores, memory bytes, queue depth and the
controller's observation timestamp. Absence is not replaced with a zero sample.
Names are unique in a team projection. Invalid resources, timestamps or repeated
names fail as protocol errors. Future status/class values remain readable.

The CLI uses the same reader for api.sock and profiles. JSON retains all details,
plain emits names, and quiet JSON emits name objects. The removed `/agents`
reader and unreachable cluster-health report have no forwarding paths. Runner
enrollment and service installation still need their W6/W8 integration.

Profile removal uses `TokenManager.ReadToken` and confirmed `RevokeToken`.
Only `member` metadata permits automatic connection-token revocation. Principal
spelling never supplies credential kind. A lookup/revocation failure retains the
saved connection. Machine credentials require explicit `--keep-token`.

`client.NewRunnerTokenManager` binds `EnrollRunner`, metadata reads and revocation
to one authenticated HTTP/api.sock transport. Enrollment posts `EnrollRunnerRequest`
to W2's existing `/api/v1/runners` route once, without a replay key or a retry.
The request carries the name, repository patterns, optional reserved pools,
narrowed grants and absolute expiry. The server derives authority and registration.
Missing bearer, prefix, team or grants fails without returning partial issuance.
Deployment token managers refuse enrollment before transport. Removal uses
`ReadToken` and confirmed `RevokeToken`. Only explicit runner metadata permits
removal. A principal prefix never supplies kind. The CLI checks the enrolled
controller and optional name before touching its service. JSON receipts contain
metadata and service results, never the bearer. Failure after issuance names the
live prefix and final revoke command on stderr. The CLI's legacy Client wrapper
and Client.EnrollRunner/CreateServiceToken methods are deleted.

The CLI forwards repeated and comma-separated `--reserved-for` values through
that registration request. They are proposals, never self-granted pools or local
configuration authority. The existing private-file configuration and rollback
path remains. Supervised runner start/refresh and native controller/store
provisioning still need integration evidence. No C2 contract changed here.

### Legacy client retirement

The legacy raw/SSE log adapter, live-log reads and event listing are removed.
Their callers use the final tagged `LogReader` pages and cursor polling.
`Client.ListNodeSteps`, `ListDebugPauses` and `ListPendingApprovals` are removed
with the unused declarations and local wrappers in `StateStoreOld`. Typed
`RunReader`, `DebugReader` and `RunReview` own those public observations.
The unused heartbeat watch observer and global host bridge are removed.
Segment-upload watch callbacks remain with their writer. The unused
log-capability probe and bearer getter are removed.

`Client.CreateToken`, `ListTokens`, `LookupToken`, `RevokeToken`, `RotateToken`
and `ListSecrets` are removed. Credential and secret management use the typed
managers above. Their issuance and metadata messages remain shared with the
controller. `Client.EnqueueTrigger` and its environment variant are removed. All intake
uses sealed `CreateRunRequest` through `Controller.CreateRun`, retaining the
captured environment digest and resolved options. The final engine constructors live
in `engine_client.go`, and the data constructor lives beside its implementation
in `data.go`.

### Schedule management

`ScheduleManager` uses existing C2 `/api/v1/schedules` routes for both api.sock
and remote connections. `ScheduleOverview` contains `health`, `schedules` and
`upcoming`. List query fields are `all=true` and positive `next`. Detail query
fields are nonnegative `fires` (explicit zero means no history) and positive
`next`. `DetailView` contains `schedule`, `fires` and upcoming RFC3339 instants.
The controller computes upcoming times, orders history newest first, applies
undeclared-row filtering unless `all`, and refuses ambiguous selectors. It
accepts IDs, display names, pipeline/name pairs and unique pipeline names.

The schedule response types have moved from `internal/api` into this wire owner.
Their declarations, effective values, override presence, pin identity, timer
health and fire history remain separate. Callers migrated directly, without
aliases. `ScheduleOverride.Validate` is the shared cadence-edit validator.
Pointer fields preserve empty TZ and zero catch-up. A pointer to an argument
map preserves unset versus explicit empty (`--arg=` in the CLI). The edit replaces the override:
unset fields use the declaration. No clamping is permitted.

`SetSchedulePaused` posts to `/{schedule}/pause` or `/resume` and requires the
returned paused bit to match. `OverrideSchedule` puts `/override` and requires
confirmation of the supplied fields. `ResetSchedule` deletes `/override` and
requires an empty override field list. `RunSchedule` posts `/run` with a stable
header idempotency key. `ScheduleRun` preserves canonical run ID, schedule and
replay. `DeleteSchedule` deletes `/{schedule}` and returns canonical
`schedule_id` and `deleted`, retaining already-absent results.

`ScheduleRepositoryManager` puts `/schedules/repos` using `ScheduleRepoRequest`:
repository URL, branch, original SHA or follow policy, and the complete array of
`ScheduleDeclaration` entries for the selected `replace` or `upsert` mode.
`ScheduleRepoWritten` retains rows and withdrawn
display names. Delete uses `repo_url` and returns the same URL and nonnegative
removed count. These are proposals. C2 derives team/repository authority.

CLI mutations resolve every selector first and deduplicate canonical IDs before
writing. A failed selection writes nothing. Mutations after selection are
sequential, with no atomicity claim across schedules. List JSON is compact selection metadata and excludes arguments and history.
Show JSON retains the complete safe projection. Other JSON is one object per
row or result. Plain emits IDs, and immediate run emits the run ID.

W2 must serve these projections on its declared routes, including empty/zero
overrides and idempotent launch. W1/W2 must retain schedule/run identity and
apply cadence ceilings without clamping. W8 must compose native timer install,
refresh and __tick with the sole local state owner. Local pin/install paths still
use older runtime plumbing. No C2 route or authorization alternative changed.
Fake-backed tests establish transport, selection and rendering, not scheduler
persistence, timers or native execution.

### Queue observations and priority

`QueueManager` uses `GET /api/v1/queue`,
`GET /api/v1/queue/groups/{group}` and
`PUT /api/v1/queue/groups/{group}/priority` from the existing C2 route table.
`ReadQueue` retains the unchanged `wingwire.QueueState` admission snapshot,
including fractional capacity, connected holders, waiter estimates, pressure
measurements and runner headroom. Both local HTTP and profiles use this client.
An unavailable read returns an error and emits no empty success snapshot.

`QueueGroup` retains its group identity, declared and effective capacities,
used cost, holders and ordered waiters. Jobs have separate `job_id` fields.
Positions are one-based. Holders retain supersession and lease timestamps.
An explicit effective capacity of zero stays zero. Used cost may exceed capacity
after a capacity reduction. Empty holder and waiter arrays remain explicit.

`QueuePriorityWrite` selects a stable run ID or team number and either a
pointer to an integer priority or `front`/`back`. Explicit zero is present.
The controller resolves relative ranks against the current queue excluding the
target run's own participants. The client sends one header idempotency key and
replays the exact body. `QueuePriorityResult` retains canonical run and group,
found/missing, prior and new priority, prior and new position, participant count
and existing-holder status. Changing priority never releases a holder's lease.

The CLI validates help, output, group and priority before resolving a connection.
Bare `queue` shows help. `queue list` reads the snapshot, and `--group` selects a
single concurrency group. JSON is one coherent snapshot or mutation receipt.
Local admission priority resolves team numbers through `RunReader` before the
unchanged d.sock operation. Remote run
priority without a group has no C2 route and returns `policy_missing` before
connecting. This is an open contract seam, not an approved feature restriction.

W2 must serve the final queue/group projections, resolve team numbers and
implement priority replay. W8 must compose local controller startup and keep the
admission operation connected to its queue. Fake transports establish W9 parsing,
rendering and request behavior, not those runtime integrations. No C2 policy or
other owner's interface changed.

### Deployment operations

`OperatorManager` uses the shared authenticated transport and C2's existing
deployment routes. `ReadOperatorLimits` and `WriteOperatorLimits` use GET and PUT
`/api/v1/operator/limits`. `OperatorLimits` retains the returned guard map,
optional usage observations and request budgets. `OperatorLimitsWrite` carries
nonnegative integer guard values, including explicit zero. The controller owns
supported setting names. A write succeeds only when every supplied key and value
appears unchanged in the response. Reads render the supplied keys without
inventing missing guards or usage observations.

`ReadObjectStoreBreaker` uses GET
`/api/v1/operator/object-store/breaker`. `ResetObjectStoreBreaker` uses POST
`/api/v1/operator/object-store/reset-breaker` and requires one header idempotency
key. Transport retries retain that key and the exact mutation body. The shared
breaker projection preserves class counters, bucket ceilings, freeze state,
measurement timestamps, incomplete-measurement state and its error. Unknown
additive fields stay out of typed command output.

Both CLI command families use the registry parser and support pretty, JSON and
plain output through local api.sock or an explicit profile. Input validation and
help finish before connection resolution. Output failures are returned. The old
Client.ComputeLimits, SetComputeLimits, ObjectStoreBreakerState and
ResetObjectStoreBreaker methods and their client-owned types are deleted.

W2 must serve these messages on its declared policy routes and map persisted
settings to C4's final job and schedule names. C2 continues to own deployment
authorization and reset replay. Fake transport and CLI component tests do not
establish native store, quota, reset or console authorization behavior.

### Public debug observations

`controllerwire.DebugReader` and `client.NewDebugReader` use C2's existing GET
routes for `/api/v1/runs/{run}/jobs/{job}` and its `/debug-pause` child. W9 owns
these success messages. No route, grant or release operation is added.

The job route returns `DebugJob`: resolved `run_id`, positive team `number`, the
existing safe `JobDetail`, and nullable `execution`. An execution contains a
public runner name and `kind=local|runner|kubernetes`. Only Kubernetes execution
contains `pod`, `namespace` and `container`. All three are explicit valid
Kubernetes names. The server projects the current executor and execution
container together with the job, without returning any claim bearer, bootstrap,
sealed request, raw environment or private storage row. A claim-holder string
cannot serve as an attachment location.

The pause route returns `DebugPauseResult` with resolved `run_id`, `number`,
`job_id` and a required nullable `pause`. A present pause has masked `reason`,
nonzero `paused_at` and nullable `expires_at`. Null expiry means indefinite.
Only explicit null means no active pause. Read errors, missing fields, malformed
times and mismatched identities fail. Additive unknown fields remain readable
and are discarded when the public projection is serialized.

`debug env` reads both messages through the same HTTP/api.sock client. It resolves
a team number once and uses the stable run ID for the second read. It emits the
public job/execution/pause projection in JSON and propagates failed pause reads.
The two reads are observations rather than an atomic snapshot. They confer no
execution authority. A future environment/working-directory snapshot needs a
host-owned masked capture contract before those values can enter the view.

`debug attach` requires a running Kubernetes execution. It accepts the existing
namespace override, validates it and passes the explicit execution container to
the caller's kubectl. The CLI process environment builder filters inherited
credentials. JSON output starts with the safe execution target and streams
stdout chunks as `{kind:debug_output,text}` records. Plain/pretty stream the
child's stdout directly. Child errors, cancellation and output errors remain
failures. Fakes and a Unix kubectl fixture establish only this adapter boundary.

W2 must bind the two projections on its existing authenticated routes and derive
the current execution target. W3 owns any future masked environment/directory
capture. W9's legacy env/attach readers and claim-prefix pod guessing are deleted.
`DebugControl` extends the observation contract with `ResumeDebugPause`.
`client.NewDebugControl` binds both operations to one authenticated transport.
Pause release posts an empty object to W2's existing
`POST /api/v1/runs/{run}/jobs/{job}/resume` route and requires an empty HTTP 204
acknowledgement. The client uses that route's safe replay policy. It never sends
an actor, attribution or lifecycle transition. The CLI reads the selected pause,
validates its stable run/job identity, then releases that identity through the
same controller. The committing transaction owns current authority and repeat
release behavior. Numeric selector resolution remains with the controller.
Native controller, Kubernetes, Windows and Darwin integration remains open.

The legacy `Client`, `New` and `NewWithToken` and their raw trigger, claim,
state, secret, output, concurrency and log transports are removed. All production
callers already use the shared clients. The retained retry helpers now belong
only to the shared transport. The old local socket and direct-store secret
sources and the legacy orchestrator host/consumer/backend graph are also removed.
W3's retained scheduler still uses `StateStoreOld`.

The controller's step handler consumes `StepReportRequest` and returns
`StepRecorded`. CLI execution reports steps under the independently awarded node
bearer. Owner authority never substitutes for that node's reporting fence.

## Output and console refund wire integration

JobOutput returns an output JSON value from both the same-run/declared-ref and
child output endpoints. The native process client consumes that shared shape.

OperatorRefund carries payment_id, refund_id, amount_cents and reason.
POST /api/v1/operator/teams/{team}/refunds requires an Idempotency-Key and a
direct configured operator-console session with CSRF. It records an already
completed provider refund and returns OperatorRefundRecorded with team,
grant_id, paid_micro, reversed_micro, balance_micro and created. C1 commits its
receipt with the reversal and operator audit. It does not call the provider.

<!-- design:end -->

### Schedule reconciliation scope

`ScheduleRepoRequest.mode` is required: `replace` reconciles the complete
repository declaration set, and `upsert` changes only the supplied declarations.
`ScheduleRepositoryManager.WriteSchedules` uses the existing PUT repository route
for both modes. Upsert cannot withdraw a schedule or change another schedule's
source pin, overrides or pause state. An empty upsert is a no-op.
The CLI selects upsert when `--schedule` is present and skips the write when all
selected declarations run elsewhere. Local registration carries the same mode.
The client rejects missing or unknown modes and a withdrawal in an upsert receipt.
W2/W1 must apply the mode atomically; neither route authorization nor repository
authority changes. Native repository handlers remain an integration dependency.

### Local host and schedule transport

Local and hosted controllers expose private execution state under
`/api/v1/runs/{run}/local-host`. Its snapshot and node reads carry complete C1
execution state. They are separate from the public scheduling projection.
Only awarded owner or node credentials can use these routes. Reads remain
available while a current holder drains cancellation. Node credentials use
node-scoped snapshot, authentication and binding endpoints. Their snapshots
contain only the awarded node.

Private reconciliation also preserves `owner_capability_prefix` beside the node
claim prefix. The local host checks the persisted owner relation before adopting
the store-issued node award. Each job process delegates from that node award.
Owner evaluation retains its separate owner or eval authority.

Runner composition adds a private `OwnerExecution` decision to trigger snapshot
nodes. It binds the authenticated owner generation and node declaration hash.
The controller applies the same preference policy as reconciliation. The native
host uses `store.OwnerCanExecute(principal)` for its synchronous local decision.
Plan and eval snapshots use their own lease relations and omit this decision.

`client.NewLocalHostClient` implements private state reads, transitions,
authentication and relational binding checks without opening the store. The
controller derives mutation actors and fences from the authenticated bearer.
The run transition endpoint accepts the start edge. Terminal completion uses
the atomic finish endpoint, including typed admission refusal. Authentication
lookups are confined to the owner and its independently awarded current nodes.
`client.NewNodeHostClient` confines the same transport to one node award.
Its renewal uses the node bearer and unit ID separately from the owner.
Closing the host also retires its retained process, storage and log transports.
Later operations refuse before presenting the award credential.
Private authentication responses retain digest sidecars because public
capability JSON omits them. No reusable raw bearer appears in those responses.

`LocalScheduleManager` carries checkout registration, pin/unpin and dry-run tick
through the same authenticated socket transport. Registration carries explicit
replace/upsert mode, the selected project declarations, captured environment
and proof/follow choices. Local mutations require verified OS-peer provenance.
`crons.ReadLocalRegistration` loads the durable association between a schedule,
its checkout/project and its environment seal. Private environment values remain
with `FileEnvironmentSnapshots`. W1's scheduled sealed intake consumes this
association and the pin. Its launcher integration is a separate handoff.

The private `POST /api/v1/local/doctor` operation requires a verified OS peer
and sweeps the elected controller's session ledger. Dry-run leaves sessions in
place. Failed event recording produces `unrecorded_step_outcome` findings;
it does not fail a successful sweep. Partial cleanup findings accompany a
canonical failure when another cleanup fails. The operation is never replayed by the
client and is absent from the hosted controller.

## W9 launcher preparation transport

`LauncherSyncRequest.Preparing` is an optional `[]LeaseRef` serialized as
`preparing`. `LauncherSyncResponse.Prepared` is an optional `[]LeaseRef`
serialized as `prepared`. Each acknowledgement names an exact still-unexchanged
bootstrap lease renewed by the authenticated launcher root. Observed Jobs and
reservations keep their existing decision correlation. Ended own awards remain
eligible for cleanup decisions when they receive no preparation acknowledgement.

`CloudControl.Prepare` is required. `ControllerControl.Prepare` sends the fences
without inventing a Kubernetes observation and requires an exact acknowledgement
for each. Missing acknowledgements return typed `lock_held`. Existing bootstrap
and exchanged claim grants do not change. The normal sync adapter carries the
observed fences as preparation requests, keeping source init alive until exchange.

`LaunchOne` obtains its first acknowledgement immediately after claiming, before
boundary probes. A shared lease keeper then renews at lease/3 through Job and
Secret publication and final activation. Each later beat also reconciles existing
Jobs so one slow publication cannot strand another Job's source init. Its context
is passed to the blocking operations and the keeper is cancelled and joined on
every return. The normal launcher loop owns preparation after activation until
bootstrap exchange transfers lease ownership to the executing host.

## W9 native QA corrections (2026-10-16)

`GET /api/v1/runners` emits the shared `Runners` object and `RunnerDetail`
rows. The old `agents` array, label map and encoded active-job strings are
removed from this response. Active jobs retain separate run and job IDs. An empty location is an unknown
observation, like zero capacity, and does not invalidate a live enrolled runner.

`client.CheckIdentity` authenticates profile connections through
`GET /api/v1/auth/whoami`. It requires a nonempty principal and subject kind,
uses the shared transport and preserves canonical refusals. It requires no
team read grant. A successful operator probe does not authorize team commands.

Team `tokens create` accepts only `member`. Runner inventory, rotation and
revocation still accept `runner`, while creation uses `runners add` enrollment.
The production trigger identity resolver calls GitHub's repository API for a
positive numeric ID. Private lookup accepts captured `GH_TOKEN` or
`GITHUB_TOKEN`. Redirects are refused and provider bodies are not diagnostics.

## Integrated working-tree submission

`SourceUploadRequest` contains `git`, `source`, `digest` and `size`.
`POST /api/v1/data/source` verifies repository identity and returns the shared
`DataUploadTicket`. The client transfers only signed object headers, then calls
`POST /api/v1/data/source/{upload}/commit` with `{}` and receives `DataReceipt`.
The source bytes use the shared filesystem or S3 backend and SHA-256 commit
verification. Provider bodies and signed transfer URLs never enter diagnostics.

The CLI puts the committed digest in `request.source.bundle_digest`. The sealed
intake atomically consumes its submitter's matching upload and retains a
source-only record. Awarded source readers use the existing `/data/download`
transport. A bundle containing the selected commit checks out directly; missing
pinned source revisions use credentialed delta fetch. Git credentials are
released only when a remote fetch, tags, submodules or LFS needs them.

Cloud source initialization downloads that bundle before exchanging its bootstrap.
The `/data/download` bootstrap alternate requires `source.read` and the
`sourceBundle` binding. It admits only the bound run's sealed submitted bundle,
without granting general `data.read`, writes or secrets. Node, Plan and Eval
preparation share this path. Exchange consumes its source authority. Retried
node awards carry no active execution attempt until NodeStart allocates the next
ordinal. Exact-body transition proof evidence carries the body's source ordinal
independently, so replay does not change the allocated ordinal.

The production GitHub lookup is shared by CLI and controller. The controller
verifies public repositories without requiring an App; private identities use
its App or confirmed team HTTPS source credential. It never trusts a typed
numeric ID or caller display name.

## Launch burst vertical (decisions 112 and 116)

`runspec.Request.Burst` seals `BurstPlacement`: ordered `PlacementStep` values
(`target`, nanosecond `hold`) and optional `overflow_minutes`. The CLI merges
user `run.routing`, repository `defaults.run` and pipeline `run` fields, then
converts YAML durations once. Targets are `here`, `machines`, `cloud`,
`name=NAME` and `pool=POOL`. Default route is local. Explicit Cloud routing sets
RunnerHere and dispatch host. Cloud remains the sole run record owner, and the
CLI hosts scheduling and public log attachment. Connection failure before intake
falls back locally with a message. No post-intake error may submit a local copy.
Source-upload network failures are marked as before intake and also fall back.
The mark never covers run submission because a lost response can hide acceptance.

`CreateRunRequest.LocalSecrets` is a private input-only map. The sealed
`Request.LocalSecretNames` contains declared names only. Intake passes values to
C1's volatile registry without serializing them into SQL, seals or responses.
The requester resolver reads only configured names from the pipeline's complete
SecretDeclaration list and captured environment. Native secret release uses the
existing live, declared, masked claim path. Registry loss and retry return
unavailable, requiring fresh requester resolution. Cross-process secret delivery
still needs a Cloud affinity or unpersisted relay contract.

`ClaimRequest` adds optional `capacity`, `os` and `arch` for the authenticated
here capability. Existing available CPU/memory fields retain current free
resources. `RenewalRequest.available` refreshes the here owner's availability.
The CLI reads the local admission daemon's actual capacity and free resources,
including explicit zero availability. `RemoteHostStore.Available` supplies
renewal observations. Canonical store placement decides owner self-execution.
Here binding now supplies the real shared native host, private HTTP state,
source access, data, logs, process services and leases.

`POST /runners` adds `personal`. It grants no extra pools or billing authority.
C1 checks personal machine ownership from account subject provenance for every
run claim. `GET /overflow` and `PUT /overflow` use OverflowControl and
OverflowControlWrite under the authenticated account. Null means uncapped.
Request caps only narrow the saved cap; team tier/ladder admission still applies.

### Burst SDK subprocess verification

Burst verification now also runs the shared SDK server in a subprocess over
native request/frame pipes for here, personal-machine and exchanged Cloud
awards. The subprocess resolves declared secrets through the real process
service router and committing controller reader. A test-only FIFO transports
HTTP messages where listeners are refused. The `rewrite_integration` variant
uses a real loopback HTTP listener. Neither changes the runtime wire. Source
acquisition and selected engine resolution remain separate from that SDK
subprocess fixture.

The CLI preparation acceptance also compiles a real Go SDK entry from tracked
working-tree edits, uploads it through authorized source storage, admits a
Cloud-owned here run, claims its owner, downloads the sealed bundle and installs
and describes that SDK again. Declared extra files survive and undeclared `.env`
does not. It uses the real development engine policy and selected installer.
The public `rewrite_integration` probe uses the configured CLI, a real controller
and the production here host to check successful execution, masked terminal
logs, an admitted handle and absence of a second local run record.

The public native probes also enroll a personal machine through
`runners add --personal --session-stdin`, start the production runner entrypoint,
and observe that machine's node award with a zero overflow cap. Separate here
and personal variants interrupt the attached CLI after a streamed SDK marker
and require the Cloud-owned run and job to finish cancelled. These probes use
real loopback and local-controller sockets and remain unverified where the
sandbox refuses listeners. They add no runtime transport or wire fields.
The personal fixture uses one test executable for both production entrypoints
because the local SDK replacement binds the installed executable's digest.
It does not prove signed engine distribution or separate release binaries.

### Overflow management client

`controllerwire.OverflowManager` exposes `ReadOverflowControl` and
`SetOverflowLimit` through `client.NewOverflowManager`. It uses the existing
account-authenticated `GET /overflow` and `PUT /overflow` routes and their retry
policies. A nil allowance sends an explicit `monthly_minutes:null`. Zero blocks
new Cloud overflow. Negative or unrepresentable allowances fail before HTTP.
Responses must include all shared fields and carry a non-negative allowance and
usage plus a first-of-month UTC period. The client rejects malformed receipts.

`NewSessionRunnerTokenManager(ctx, EngineOptions, session)` authenticates runner
enrollment and token management with an existing account session. It rejects a
simultaneous bearer callback, obtains CSRF from `GET /api/v1/auth/session`, and
keeps the session and proof in memory. Mutations send Session authorization,
the selected controller's Origin, and matching CSRF header and cookie. The
client refuses redirects and never retries one-time enrollment. Session input
cannot contain whitespace, control bytes or non-ASCII bytes. The controller
still checks independent session ancestry and current membership on each write.

`runners add --personal` requires `--session-stdin` before service preparation
or enrollment. The flag reads an existing independent account-session
presentation from stdin; it adds no login, bearer conversion or persisted
session field. Only the issued runner credential is written to the private
credential file. Failed installation revokes that credential through the same
session transport and rolls back the local configuration.

## Committed Cloud allocations and launch proof

`ClaimedUnit.cloud_resources` carries the physical CPU and memory allocation
from C1's committed billing reservation on Cloud awards, including sync and
exchange. `Execution` continues to carry the sealed author settings. An absent
CPU or memory request remains absent in those settings. The launcher requires
the allocation, builds its Job from it, and refuses an exchange that changes it.
Non-Cloud awards omit the field. The previous use of author settings as physical
Cloud allocation is removed.

The burst SDK acceptance drives real intake, claims and launcher publication
through source init, one-use bootstrap exchange, native SDK pipes, secret
masking, artifact upload/restoration and final Job/bootstrap cleanup. The
Kubernetes client and measured isolation boundary are infrastructure fakes.
The public CLI probes also declare and consume artifacts across separate jobs.
These tests do not prove cluster enforcement or release-image execution.
