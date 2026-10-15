# Controller API contract

<!-- design:begin -->

## Amendment 1: private execution and claim wire

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
at its original claim_by. Resume uses the retained private bearer.

NewDataClient uses the same HTTP/api.sock transport to provide blob.DataSigner
and blob.RecordStore. Each call reauthorizes server-derived scope/uploader facts.
The signed-object client never receives the reusable controller credential.

ClaimedUnit includes accepted engine, source generation and owner/attempt/parent
expiry deadlines. Its sealed request carries Source.Location. CloudJobAward consumes
that immutable source projection. SyncLauncher returns held awards and stale Job
identities without raw bearers. Exchange presents the bootstrap in the authenticated
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
uses the source-generation query and returns persisted nodeproto.JobFailure under
a declared recovery edge. Actor/grants never enter request bodies. Adapters map
private wire state directly to persisted state rather than inventing pending rows,
retry counters or failures. Applied.Attempt from start/retry supplies the execution
ordinal. The initial zero attempt is not a synthetic attempt for owner-unit logs.



`internal/controllerwire` owns the HTTP request and response types shared by
fetched engines, the controller, api.sock, local status and remote status.
`capability.Routes` owns authentication, bindings, liveness and retry policy.
`pkg/controller.EngineController` adapts these types to C1 transactions.
`client.NewEngineClient` supplies the same interface to every fetched engine.
These are declarations with stub bodies.

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
| `client.EngineOptions`, `NewEngineClient` | Authenticated HTTP transport, including api.sock dialing. |
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

<!-- design:end -->
