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

`EngineOptions.Launcher` selects `/api/v1/launcher/claim` for every launcher
selection, including node-only and named jobs. Unit kinds cannot distinguish
a runner's `nodes.claim` credential from a launcher's `claims.launch`
credential. This transport option grants no authority. C2 authenticates the
selected route. Other clients use the named run/job routes or the separate
runner run/job queues. A mixed trigger/job queue request needs a shared C2 route
before that combination can be transported in one claim.

<!-- design:end -->
