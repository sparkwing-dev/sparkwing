# T1 acceptance contract

<!-- design:begin -->

## Amendment 1 integration evidence

`TestSurface/surface-schedules-and-managed-timers/store-sqlite` checks schedule
persistence below CLI, timers and HTTP. Two teams share one repository path.
Foreign reads, edits and fire history stay inaccessible. A failed history insert
rolls back the cursor. Reopening preserves both schedule history and operator tick
metadata. Tenant deletion preserves the other team's schedule. The native
managed-timer cells remain pending.

`TestPlacement/placement-ordered-matching/store-sqlite` checks that an Actions
push cannot claim another repository's earlier trigger. Two jobs prove that soft
preferences rank eligible choices and remain optional. An ineligible oldest job
keeps its queue position through failed polls and can be claimed as soon as its
requirement is satisfied. The final store transactions, matcher and capability
ancestry are real. Enrollment is a contract fake. Native placement cells remain
required.

`TestRecovery/recovery-concurrency-no-revival/store-sqlite` runs the elected
capacity maintenance pass. A forged fence cannot promote work. After expiry, the
queued competitor takes capacity exactly once. Reopening preserves that holder,
and the former owner queues as a new arrival. Native host and platform cells
remain required.

`TestRecovery/recovery-local-orphans/store-sqlite` proves the local store boundary.
It runs the elected reaper, repeats the pass and reopens the database. Unstarted
owners requeue without a disruption. Started owners fail in place. Each case has
one lifecycle event and retains its original run and owner rows. Native local
controller/process cells remain required.

[Amendments 1](amendments-1.md) assigns each cross-area dependency. Shared decoder
and text-bounding tests are component evidence. Native placement cells require the
real host, controller/auth, store and storage adapters. Keep cells blocked until the
actual HTTP/api.sock, backend and OS path runs. W4/W5 share the v2 conformance corpus
and canonical hash/presence fixtures. W5/W9 package and test native Windows handle
bridging. W8 supplies node/network/teardown evidence. T1 and the lead own the full
real-repo QA required by decision 99. No fake driver or parser-only pass replaces it.


This contract defines the evidence required to accept the rewrite. It is based on
`ARCHITECTURE-v4-frozen.md` and decisions 87-104. Sections below refer to that
frozen architecture. Historical requirements in its sections 14-15 do not override
the target. Decisions 102-103 replace its version policy, and decision 104 adds
native TypeScript execution on Windows.

T1 owns the acceptance harness boundary, synthetic fixtures, scenario catalog,
execution matrix, parked-test port plan and skipped test skeletons. C1-C4 own
product behavior and its implementation. T1 does not define another transition
engine, authorization policy, plan validator, environment resolver or schema.

The Go import path is `github.com/sparkwing-dev/sparkwing/internal/acceptance`.
Scenario skeletons live in `tests/acceptance`. [The catalog](t1-scenarios.md) maps
every case to its architecture sections. [The port inventory](t1-port-inventory.md) accounts
for the parked tests individually, including assertions that must be reversed.
Its [credential-route appendix](t1-credential-routes.md) accounts for every old
matrix route and pins the complete final route declarations and permission
alternatives. [The invariant map](t1-invariants.md) binds required behavior to
specific assertion excerpts and independently required execution dimensions.

## Delivery state

`New` and `Run` are signatures with `panic("unimplemented: t1-acceptance")`
bodies. The catalogs contain contract data. Every product scenario is skipped
with its required C1-C4 areas named. A passing skeleton suite proves compilation
and catalog consistency only. It provides no product, security, billing, Windows,
Kubernetes or launch-acceptance evidence.

When an area lands, implement the scenario's adapter, stimuli and independent
assertions, then remove that case's explicit skip. Never replace a skip with an
empty passing body or detect a known bug and skip it. A selected QA lane fails
as unable to run when a required dependency is unavailable. The launch gate
rejects any remaining required skip, unexecuted cell or cleanup failure.

## Exported Go surface

| Type or function | Purpose |
| --- | --- |
| `Area` and `C1`, `C2`, `C3`, `C4` | Name the production-contract prerequisites for a scenario. |
| `Placement` and `Local`, `LocalTriggered`, `RunnerHosted`, `Launcher`, `Here`, `GitHubActions` | Identify the six execution paths in section 3.2. |
| `Lane` and `Hermetic`, `Native`, `Cluster`, `Hosted` | Separate in-process proofs from native processes and external integration evidence. |
| `Target` | Pin placement, SDK, native OS, state backend, data backend and evidence lane. |
| `Fixture` | Name a synthetic input set and its independent oracle. |
| `Scenario` | Specify architecture references, prerequisites, placements, fixtures, stimuli and required observations. |
| `Selection` | Select an existing scenario and target by ID. |
| `Options` | Supply an owned root, exact binary paths, target, fixtures and external resources. |
| `ExternalResources` | Supply explicit test context, namespace, cleanup allowlist, repository and credential-file paths. |
| `Command` | Pass argv, directory and environment without shell interpolation. |
| `Result` | Preserve exit status and separate stdout/stderr. |
| `Principal` | Select a fixture identity without placing a bearer in argv or environment. |
| `Checkpoint` | Address a committed state or process signal by run, job, generation and attempt. |
| `Fault` | Inject a named fault at an acknowledged checkpoint. |
| `Artifact` | Identify a redacted evidence file by kind, sandbox path and digest. |
| `Evidence` | Record selection, source/binary identity, runs, checked assertions, artifacts, skips and cleanup failures. |
| `Harness` | Expose the test adapter without duplicating a product decision. |
| `Harness.CLI` | Execute a candidate command using argv and return its exit/output evidence. |
| `Harness.HTTP` | Send a request with the selected fixture credential through the real route stack. |
| `Harness.Reach` | Wait for a named state/process acknowledgement rather than elapsed wall time. |
| `Harness.Inject` | Apply one fault to an owned fixture resource. |
| `Harness.AdvanceStoreClock` | Advance an injected store clock for deterministic deadline and lease tests. |
| `Harness.Collect` | Gather redacted evidence for the selected scenario. |
| `Harness.Close` | Stop, reap and remove owned resources and report cleanup failures. |
| `New` | Provision the selected target and fixtures under `Options.Root`. |
| `Run` | Execute a selected scenario and check every declared assertion. |
| `Fixtures` | Return the complete fixture registry. |
| `Targets` | Return the required native execution cells. |
| `Scenarios` | Return the scenario catalog with independent behavioral oracles. |
| `Applies` | Select declared placements and explicit OS/SDK/mode restrictions. Empty restrictions require all supported variants. |
| `Selections` | Expand scenarios across their declared placements and all corresponding targets. |
| `CredentialRouteExpectation` and `CredentialRoutes` | Pin final route audience, owner, authentication and each complete permission alternative independently of the product table. |

Unknown fixture, scenario, target, principal, checkpoint or fault names are
errors. `Run` verifies that its selection matches the provisioned harness.
Commands and HTTP bodies may contain synthetic test secrets in memory where the
product API requires them. Saved output and evidence must be redacted. The HTTP
caller closes response bodies. `New` cleans up partial provisioning on failure.

## Harness and execution matrix

The native harness runs candidate CLI, controller, runner and SDK processes.
It builds both SDK fixtures against the same source revision and records binary
and image digests. HTTP tests use the real middleware and handler stack.
Read-only store inspection supplies an additional oracle for atomicity, attempts,
leases, blob edges, billing and the completion outbox. Test setup may seed a fresh
database, but a scenario must exercise the product entry point for the behavior
it claims to prove.

Expected outcomes come from these scenarios and fixture manifests. Reading
`TransitionTable`, the route policy table, a production capability table or a
settings resolver to generate the expected answer would test a table against
itself. Those tables may enumerate the surface that needs coverage. Independently
written assertions determine whether that surface is correct.

| Placement | Driver and proof |
| --- | --- |
| Local | `sparkwing run <pipeline>` hosts the run in the CLI, using the local controller for admission and state. `standalone-*` targets select the supported in-process CI mode. Both build and reuse a local pipeline binary. |
| Local triggered | Submit through the local controller's queue, observe its `__host-trigger` child and keep the same admission and local-orphan semantics. |
| Runner-hosted | `pipeline trigger` submits to a private controller. A registered runner claims the trigger, self-claims local work and dispatches other ready jobs to a pool runner or launcher. Test both executor paths. |
| Launcher | Submit to the controller, run the real launcher and observe plan, eval and work Jobs. The controller owns scheduling after `AcceptPlan`. |
| Here | `pipeline trigger <pipeline> --profile P --runner here` creates the run-bound Award. Kill the CLI, lapse its claim, then use `pipeline trigger <pipeline> --profile P --resume <run>` to resume that same run. |
| GitHub Actions | A dedicated Actions workflow exchanges a real push-scoped OIDC identity and runs the registered runner path. Hermetic issuer tests cover adversarial claims separately. |

`Targets()` is the executable target inventory. Go runs natively on Linux,
macOS and Windows for local, local-triggered, runner-hosted and here placements.
TypeScript covers those paths on Linux, macOS and Windows. Launcher cells cover both SDKs
on Linux with PostgreSQL/S3 and single-controller SQLite/filesystem signing.
Actions cells cover both SDKs on Linux and Windows. No Windows
result is inferred from cross-compilation or a Unix permission test.

Each scenario executes on every target selected by its placements and any explicit
`OS`/`SDKs`/`Modes` restriction. Modes are `controller` and `standalone`, selected
by `Target.Standalone`. The catalog documents those restrictions. Subcases
whose behavior is platform-specific assert that platform's contract. For example,
Linux/macOS install a login service, while Windows refuses unsupported service
installation and supports `--stay`. Windows schedules run through the daemon.
Standalone CI asserts its in-process mode without pretending to install a login
service. An inapplicable subcase records the reason and its positive/refusal
counterpart; it cannot silently remove an entire target from the launch matrix.

In-process SQLite/PostgreSQL transition and race tests remain focused package
tests in addition to this matrix. Run every store invariant under both dialects.
Hermetic tests may replace provider responses, clocks and object storage with
deterministic fixtures. They never replace a real SDK, Windows, Actions, DinD or
Stripe test-mode proof in a selected integration cell.

## Fixture contracts

`Fixtures()` specifies each fixture's setup and oracle. The
adapter materializes them under its owned root. The `repo` fixture has a default
branch that advances after a verified webhook, two teams, same-team unrelated
jobs and foreign repositories. These distinctions are needed because team and
repo membership alone do not authorize a blob edge or a branch trust claim.

The `sdk` fixture has matching Go and TypeScript pipeline behavior. Persist the
expected graph, output values, artifact bytes and side-effect counts as reviewed
fixture data. It covers `Needs`, `NeedsIfPresent`, `Generate`, `OnFailure`, retries,
memo, file caches, artifacts/consumes, concurrency, placement, inline work,
secrets, risk, cleanup, approval, children, refs, OIDC, masking, annotations and
summary. Malformed wire inputs are handcrafted separately because a valid SDK
cannot exercise all validator refusals. Use boundary values at, below and above
each published limit.

The materialized fixture files and process drivers are implementation work. This
contract specifies their inputs and oracles; the catalogs do not create them.

Every fixture starts from the final schema. An incompatible schema is a refusal
fixture, not a migration fixture. Store inspections must preserve the distinction
between operational rows that cascade and financial records that survive run
retention. A fresh PostgreSQL fixture is required for transaction interleavings
that a serialized SQLite run cannot establish.

The `identities` fixture expands each supported route across live grant/binding
combinations and expired, revoked, ended, foreign-team and stale-generation
variants. Positive requests have valid minimal bodies and reach the operation.
An unrelated 400 response does not prove a route was properly authorized. The
old credential matrix is evidence about coverage gaps, not the expected-answer
table for the new routes.

## Deterministic faults and evidence

`Reach` consumes acknowledged process signals, committed events or transaction
barriers. Checkpoint names are `intake_committed`, `owner_claimed`, `plan_accepted`,
`job_claimed`, `job_started`, `attempt_closed`, `expansion_expected`,
`expansion_accepted`, `result_body_open`, `before_result_commit`,
`effect_delivery_started`, `effect_ack_pending`, `segment_committed`,
`reservation_created`, `billing_started` and `process_exited`. Identity fields
select the exact run/job/attempt where relevant. Adapters may add a checkpoint
when a scenario needs it, with a concrete acknowledgement and a documented owner.

| Fault kind | Required effect |
| --- | --- |
| `kill_owner`, `kill_executor`, `kill_controller` | Terminate the selected owned process and acknowledge its exit. |
| `signal_interrupt`, `signal_terminate` | Exercise native graceful shutdown and child cleanup. |
| `drop_renewal`, `reject_renewal`, `shed_renewal` | Model silence, 401/403 or 409 `lock_held` lease loss, other 409-code controls, or 429/503 Retry-After without treating a refusal as renewal success. |
| `hold_report`, `release_report` | Cross a lease/generation or cancellation boundary while a report is in flight. |
| `hold_effect_ack`, `release_effect_ack` | Let terminal desired state overtake a pending/running delivery. |
| `fail_record_commit` | Fail the transaction before commit and prove no successful partial state/event/edge exists. |
| `drop_blob`, `corrupt_blob` | Remove or damage owned fixture bytes while preserving the independently recorded expected digest. |
| `fail_manifest_commit`, `drop_log_tail` | Exercise memo/staging failures and incomplete log reporting separately from the job result. |
| `oom_executor` | Produce a real or fixture-reported OOM failure that uses ordinary retry policy, not disruption accounting. |
| `duplicate_webhook`, `delay_webhook` | Replay or reorder a signed provider event without inventing another payment or run. |

Store-clock tests check just before, at and after lease/deadline boundaries.
Native process tests use process handles and bounded caller contexts. They do
not add sleeps or package timing variables. External provider expiry is checked
against that provider or the controlled signer; advancing the store clock alone
cannot prove expiry of an already issued external token or URL.

Capture run/job status, transition sequence, attempts and generations, event
counts, expansion/spec hashes, claim lineage, blob reference edges, log completeness,
outbox revisions and ledger amounts where the scenario requires them. Preserve
both negative HTTP status/code and the absence of forbidden side effects.
Normalize only nondeterministic IDs, timestamps and endpoints. Never normalize
away outcomes, event counts, actor/binding, retry/disruption counts or money.

Evidence files are JSON receipts and redacted logs inside the sandbox. Each
receipt identifies the candidate source and binary/image digests, SDK version,
target, scenario, fixture versions, actual assertions checked and cleanup result.
Unknown or missing measurements are missing evidence. A stopped or failed run
does not overwrite its original receipt with a later retry's pass.

## Isolation and cleanup

Provision private homes, configuration, secrets files, cache roots, database,
ports and sockets. Clear inherited Sparkwing service bindings, runner credentials
and unrelated provider credentials. Pass only fixture credentials by memory,
protocol or owner-only file. Never discover endpoints from `~/.sparkwing`, a
developer's kube context, default cloud profile or ambient Stripe account.

Register cleanup before starting each process, service, namespace resource,
private file or fixture database. Track handles and resource UIDs. On success,
failure or cancellation, stop children, wait for exits, remove owned resources
and confirm there are no leaked processes, pods, credentials or reservations.
Kubernetes cleanup requires the explicit context/namespace/allowlist tuple and
removes only resources created by that test. A suite never provisions or deletes
cluster infrastructure as incidental cleanup.

## Cloud, security and billing evidence

Every Cloud plan, eval and work Job must run as root with privileged DinD and
20 GiB aggregate scratch, including Docker storage, on a team-dedicated node
holding exactly one Job. The host waits for Docker readiness. Pipeline containers
have no launcher/controller service-account authority, host mounts or host
namespaces. Teardown stops every container before releasing the node reservation.
Test admission and scheduling refusal when node or network boundary evidence is
unavailable. Run Docker inside
the actual Job, test permitted and denied egress, inventory reachable DaemonSet
ports and record the disposable escape-probe canary. A rendered Pod spec cannot
prove network enforcement. Tests do not claim that the sidecar, Yama or bootstrap
exchange isolates credentials from root code inside that Job.

Bootstrap exchange must revoke the file credential while retaining ancestry to
the claimant root. Secret release, team git credential opt-in and audit, capability
revocation and node-host/controller audience separation are checked through real
routes. Here machines cannot receive restricted secrets. External source/OIDC
credentials and already signed URLs retain their documented residual lifetimes.
Fresh signing or controller log reads must honor current authorization.

A59 tests read the immutable award's payer, metered flag, executor class,
reservation, start rule and unit kind. Change the root credential after award
and prove settlement still uses the award. Hold a metered run host while work
waits for a customer-owned runner and prove the wait is excluded. Plan/eval
compute is per second without the job minimum, plan-cache hits are free, and
executing work retains its 60-second minimum. Test retries, unstarted failure,
disruption, cancellation, duplicate results and controller restart for exactly
one settlement or refund and no negative/lost ledger delta.

Billing has two proofs. Deterministic provider fixtures cover ordering and race
cases. A separate Stripe test-mode lane runs checkout, verifies actual signed
webhooks, grants credit, consumes it, handles payment failure/refund and checks
the ledger. Require test-mode keys and test-mode provider objects before sending
requests. Retain provider IDs, never keys or sensitive payment data. Switching
the provider adapter's supplied configuration must not select different product
code or schema. No live charge is part of this acceptance suite. Decision 100's
later live-key switch remains a deployment configuration action.

## Version-policy evidence

Both SDK pins select the exact engine on every placement. The `versions` fixture
records independent signed-release assets, platform identities and index contents.
Nine-month-old and twelve-month-boundary engines exercise the controller API
through the full lifecycle. Their node protocols match their SDKs by construction.
Engine downloads come from GitHub Releases, pass `verify-release` trust roots and
closed-asset verification before execution, and are cached per machine. This is
separate from pipeline binaries, which local runs still build locally.

The signed index from `sparkwing.dev` carries latest, minimum, revocations with
reasons and support-window ends. Invalid or unavailable index responses cannot
replace the last verified copy. The first-use case with no verified copy is
tracked as `version-index-first-use` below. Its unresolved policy is not a pass.
Support-window expiry alone does not revoke a release. Deprecation notices appear
in run output and the dashboard before enforcement. Revocation reports the reason
and `sparkwing upgrade`; that command bumps and verifies both SDK pins. Runner
self-update verifies release bytes, and a stale runner cannot bypass its minimum.

## Activation and launch gate

`TestCredentials/credentials-session-digest-csrf/store-sqlite` exercises account
session creation, digest lookup, sealed CSRF-key persistence, reopening, demotion
and revocation against a real SQLite file. It also checks maintenance-fence
refusal, expired-session cleanup, preservation of a live session and persistence
after another reopen. It is store-boundary evidence. Native
browser, OAuth and production C2 route-matrix cells remain blocked. Eight
TestOAuthHTTP* component tests exercise registered OAuth start/callback routes,
generated proof and PKCE refusals, replay, private diagnostics, link continuation
and real SQLite session authentication through a fake GitHub provider transport.
They do not establish native browser/provider integration or roleless sessions.
The existing
`recovery-cancel-beats-loss/store-sqlite` proof also checks that the first reason
survives cooperative draining, reaping and reopening.

`TestRecovery` also runs two SQLite persistence cells through
`CheckStoreRecoveryBoundary`: cancellation after holder loss and reconciliation
before and after reaping. They check late-report refusal, one self-award under
concurrent reconciliation, unchanged run identity and attempt history, memo-miss
dispatch, unique lifecycle events, and persistence across reopening. Authority is
preseeded in the final schema and enrollment uses a contract fake. These cells
do not replace the native placement cells below.

`TestRecovery/recovery-application-retry-ordinal/store-sqlite` checks two identical
failures followed by success under one holder, generation and lease. Explicit
source ordinals separate their replay identities. Delayed failure and success
reports cannot affect a newer attempt. Retry replay after reopening creates no
attempt or event. All three completed attempts and the four exact lifecycle
events survive reopening. Terminal replay emits no extra event and a conflicting
result fails. W9's source-ordinal wire amendment remains open in `handoffs.md`.
Native process, backoff and launcher-Job continuity still require their placement
cells.

The nine skeleton groups are `TestLifecycle`, `TestRecovery`, `TestPlacement`,
`TestCredentials`, `TestStorage`, `TestProtocol`, `TestEnvironment`, `TestSurface`
and `TestBilling`. Each enumerates its catalog scenarios and target cells. The
catalog conformance test rejects unknown fixtures/areas, duplicate IDs, empty
steps/assertions and scenarios with no target. The invariant check binds each
required decision to a specific assertion and tests its independently required
placement, OS, SDK and mode cells against the 34-target baseline. Removing a
Windows or TypeScript target or narrowing a scenario cannot leave that check green.
The documentation drift check compares IDs, references and applicability.
`TestPortDestinations` validates each source identity and the exact Go test
function, signature and scenario group at its acceptance destination.
`TestFinalCredentialRouteDeclarations` compares every final route and complete
permission alternative against committed expectations. Actual router registration
and handler authorization remain scenario proofs when C2 lands.
Activating a scenario requires its mapped port subcases as well as its primary
assertions. The separate focused package proofs in the inventory remain required
for launch.

As implementations land, port the focused tests in the port inventory and activate
the affected scenario cells. Package proofs run early. Native process, cluster,
Actions and Stripe proofs run in their declared lanes with explicit resources.
The eventual product pipelines run through Sparkwing so resource admission can
account for them. Contract authoring only builds/vets the new packages and runs
the skipped skeletons; it does not invoke a Sparkwing binary.

Launch QA also requires a lead-supplied inventory of every repository using
Sparkwing, its exact pipeline entry points, source commits, credentials and
applicable target topology. Run real local and triggered pipelines through local
controllers, Kubernetes controllers, Cloud runners, moonborn and this desktop,
plus native Windows and both SDKs. Record each inventory row as an executed
receipt. The inventory may not silently omit an unavailable repository or target.
Decision 99 requires every required pipeline to be green; a synthetic fixture
suite alone is insufficient. The lead owns the external QA schedule and release.

One controller replica remains the supported deployment. Independent store
clients still test additive counters, shared quota reservations, singleton leases,
bounded auth/revocation caches and restart recovery. Segment fallback must work
for missing, partial and stalled rings without claiming that cross-replica live
fan-out or coordinated request limiting has shipped.

## Cross-area assumptions

These names identify the owning contracts. Proposed integration names below do
not specify a competing Go shape. Adapters use the reconciled type chosen by its
owner and translate observations only. The catalog remains independent of those
types while its product scenarios are skipped. The final-route declaration guard
imports C2's existing policy table only as the subject under test.

| Area | Expected type or API | Required agreement |
| --- | --- | --- |
| C1 | `runspec.Request`, proposed `runspec.Spec` | Request and accepted spec/plan seals are separately observable, immutable and digest-addressed. |
| C1 | `store.Tenant`, `store.Operator`, `store.ErrIllegalTransition`, `TransitionNode`, `FinishRunAndUnit`, `AcceptPlan`, `AcceptExpansion` | Tests can drive real writes and inspect state/event/attempt atomicity under SQLite and PostgreSQL. |
| C1 | Proposed `store.AttemptRef`, `store.BlobReceipt`, `store.RunEffect` | Attempt ordinals never reset, receipts retain job/attempt/generation, and outbox acknowledgements fence desired revisions. |
| C1 | Proposed `store.ConcurrencyHolder` and owner-liveness API | Adoption retains only continuously held capacity and live waiter arrival times. |
| C2 | `capability.Capability`, proposed `capability.RoutePolicy` and `capability.BillingProvenance` | The full route table exposes audience, tenant owner, grant, binding and liveness; awards preserve immutable A59 facts. |
| C2 | `store.Issue` with Delegate/Award/Exchange and here Award | Root ancestry, parent expiry, initial claim window, resume and eval expansion grants agree with the state contracts. |
| C2 | Proposed `apierr.Error` and the code registry | HTTP, protocol, node routes and JSON CLI errors use one envelope; 401/403/409 are distinct. |
| C3 | Proposed `nodeproto.Plan`, `nodeproto.HostCapabilities`, `nodeproto.ValidatePlan` | One strict validator covers initial plans and expansions, all required launch capabilities and decision 90. |
| C3 | `nodehost.RunHost`, `nodehost.NodeHost`, `lease.Keeper`, `source.Acquire`, proposed `source.Entry` | Adapters can drive actual host roles, exact-SHA checkout and acknowledged failure boundaries without another scheduler. |
| C3 | `sparkwing.Job` and proposed `sparkwingtest.Host` | Matching Go/TS fixtures can express the full frozen API; the fake host is a unit-test aid, not native acceptance evidence. |
| C4 | `procenv.Base`, `procenv.Runtime`, `procenv.Build` | Both SDKs receive the same identity/base policy, startup/body distinction and credential exclusion. |
| C4 | `limits.Child`, `limits.Renew`, `limits.CheckRenewal`, `limits.Table` | Boundary tests use published values; local, queue, approval and owner deadlines remain distinct. |
| C4 | Proposed command registry, `settings` per-setting resolvers, local-controller lifecycle interface and DinD Job-spec type | Tests enumerate the final grammar and lifecycle while independently asserting semantics from sections 2, 7 and 9. |

Unsettled dependencies are named here rather than represented by new product
types in T1. They must be resolved before the affected scenarios can activate.

| Assumption ID | Owner and contract | Required agreement |
| --- | --- | --- |
| `engine-resolution` | C3/C4 version owner and exact engine result | One resolver handles both SDK pins and exposes version, platform, digest and executable identity. All placements consume its verified cache. T1 does not choose a package or result shape. |
| `version-index-first-use` | Version owner's signed-index policy | Define the outcome when no verified index exists and fetching or verification fails. Last-verified fallback is required when a copy exists; that rule does not settle first use. |
| `controller-api-window` | C3 shared controller HTTP wire contract | Supply independently retained release fixtures spanning twelve months from launch freeze, including lifecycle, claims, expansion and run detail. T1 does not invent wire bodies. |
| `generator-membership` | C1 `AcceptExpansion` and C3 plan validator | Agree on per-generator membership, including empty groups, and one acceptance transaction. A source with 256+256 generated jobs is valid, while 257 in either group fails. |
| `owner-blob-receipts` | C1 blob receipt and record authority | Represent pre-plan owner/unit uploads without synthetic job IDs. Those receipts do not authorize unrelated job outputs. |
| `log-subject-and-grace` | C1 log identity and C2 liveness | Agree on owner/plan/eval versus job-attempt subjects and final-attempt log-seal grace. Grace does not override credential expiry, newer generations or membership loss. |
| `final-route-registration` | C2 route policies and controller/node-host registration | Expose actual registered final routes to the runtime matrix and cover every complete alternative. The declaration check is not evidence that the legacy router already uses them. |
| `route-rights-reconciliation` | C2 permissions, root authority and payment callbacks | Resolve the named assumptions in the credential appendix, including eval log authority and verified payment-event authentication. Change expected permissions only against that agreement, never by regenerating them from C2. |

The fixture constructors need explicit test seams. C1 owns an injected store
clock and transaction barriers for result/expansion/outbox commit races. C3 owns
the injected `lease.Keeper` transport/clock and process readiness/exit signals.
C4 owns local-controller idle-state and Job-admission observations. These are
constructor dependencies in tests, not customer flags, routes or environment
variables. Native process checks use real process acknowledgements; advancing
the store clock does not substitute for them. If a production package cannot
expose a required seam, its owner must agree on the smallest test constructor
change before activating the affected case. A polling sleep is not a substitute.

## Open questions for the lead

1. Decision 90 refuses registered secret values in identifiers without a minimum
   length, while a parked test accepts secret `42` in `node-42`. This contract
   refuses it. An error identifies the field and position rather than echoing the
   offending identifier, because echoing it would repeat the secret. Confirm the
   diagnostic convention and the absence of a short-value exemption.
2. The here Award has `claim_by = intake + 10 min`, while recovery allows the
   same runner to return until trigger expiry. This contract applies `claim_by`
   to initial acquisition and permits reclaim after that window while the parent,
   owner deadline and trigger expiry still allow it. C1 and C2 must agree on that
   distinction or the resume acceptance oracle needs a lead decision.
3. The real-repository QA inventory and external resources are not supplied by
   the brief. The lead must provide them for launch evidence. Their absence
   blocks external QA, not these compilable contracts. No unknown repository,
   billing account or Kubernetes namespace is guessed.
4. The integration names above need cross-area reconciliation. Expansion authority needs an
   eval-unit binding and `expansion.submit` even though the abbreviated capability
   summary does not enumerate them. Section 3.6 controls the expected behavior.
5. Frozen v4 identifies inconsistent `/services` access but does not specify the
   final permission. The credential-route port appendix identifies that and
   other unspecified route rights. C2 supplies its proposed final route table;
   the lead must settle the flagged permissions independently before those
   matrix cells can execute. Copying C2's policy into the expected-answer table
   would make that table its own oracle. Unresolved cells block launch QA.
6. The version owner must settle `version-index-first-use`. T1 covers the failure
   inputs and preserves the absence of a verified copy, but cannot choose a new
   execution restriction on the owner's behalf.

<!-- design:end -->
