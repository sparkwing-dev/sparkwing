# Migrating to the next release

## GitHub runner enrollment uses current administration authority

`Tenant.AddGitHubRunnerBinding` and `RemoveGitHubRunnerBinding` are removed.
Use `RegisterGitHubRunner(ctx, authorized, binding)` and
`RevokeGitHubRunner(ctx, authorized, repositoryID)`. Each write rechecks the
current team administrator in its transaction and uses the store clock.
Removing an enrollment ends its Actions roots and their descendants atomically.
An Actions root needs the current numeric repository and owner registration.

## Secret release uses current authority

`Tenant.ReleaseClaimSecret` and `ErrSecretUndeclared` are removed. Use
`Tenant.ReadSecret(ctx, authorized)` through the controller's `SecretReader`
interface. It checks the current claim and accepted declaration and commits
the release audit with selection. Undeclared reads return the canonical
`forbidden` error with reason `secret_undeclared`. The caller supplies no clock.
The default tenant also implements `CredentialWriter` for secret and
git-credential mutations. Verified local peer presentations still need the
transport's authority adapter.

## Queue reads use explicit store owners

`CountPendingNodes`, `CountNodesByQueueState` and `CountActiveRunners` move
from Store to Operator. Select it with `store.AsOperator()` for deployment
metrics. These reads continue to aggregate across teams.

`Store.CountPendingTriggers` is removed. Select the intended Tenant with
`store.ForTeam(ctx, team)` before reading its pending trigger count. Local
consumers use DefaultTeam; controller requests use the authenticated team.

## Generated dependencies use accepted expansions

`UpdateNodeDeps` on Store, Tenant and Client and
`POST /api/v1/runs/{id}/nodes/{nodeID}/deps` are removed.
Declare `needs_generated` in the accepted plan and submit generated jobs with
`Tenant.AcceptExpansion`. Read `RunSnapshot.Scheduling` to resolve group members
and decide when dependents can run. Accepted declarations stay unchanged.

## Daemon event cursors

Replace `daemon events --offset N` with the returned `--cursor`. Repeat the same
run, kind, incarnation and since filters on continuation. `--limit` must be
positive. The default remains 50. Follow cursors until the final page to read
all matching retained events. Newer arrivals do not shift older pages. Removed or
edited unread history fails with a hint to restart without the cursor.

JSON emits event rows followed by a `kind: page` object containing `next_cursor`
and `truncated`, including on empty and final pages. Consumers must distinguish
that object from event rows. `--explain --run ID` still returns the complete
related timeline without pagination or page metadata.

## Daemon lifecycle reports

Daemon status, stop and restart share the local controller lifecycle used by
dashboard and doctor. JSON uses `kind: service`, `service: controller`, `action`
and `state: running|stopped`. It also carries `version`, `pid` when present,
`stay`, `installed` and `activity`. Mutation reports include `restarted`,
`stopped` and the preceding `previous_version` when a controller was replaced
or stopped. Plain output is the state.

Consumers of `running`, `healthy`, `binary_version`, schema versions or socket
paths must switch to this projection. Use doctor for controller diagnostics.
No status command opens the runs database. These adapters need the native
local-services binding before they can inspect a running controller.

Restart leaves a stopped controller stopped. The same exact version stays
running unless `--force` is selected. Replacement preserves the home, dashboard
address and stay policy, waits for shutdown, and refuses an unready or
wrong-version replacement.

## Job failure reports

Replace `JobResult.error` with `failure: {job, stage, error}`. The nested error
uses the canonical code, message, hint and details shape. The job must match the
report's job selector. Stage must be present. Recovery reads the persisted
failure without inferring its stage from an outcome or reason.

## Reconciliation sources and sealed guards

Private reconciliation can identify a previously materialized result with
`source.result` containing `run_id`, `job_id` and `state_seq`. This is separate
from an execution attempt. The controller rechecks its persisted state and read
authority before reuse. Do not invent an attempt for a memoized result.

Private scheduling snapshots retain `guard_refusal` and `cancel_requested`.
Owners must preserve both when reconstructing the scheduling decision.
Cancellation retains precedence over the guard refusal.

## Spark library inflation

Use `sparkwing pipeline sparks inflate --library MODULE`. The module can be a
sparks-core module name or a full Go module path. `--module` is removed.
`pipeline sparks catalog` prints the selector for each available module.
Plain output is the destination directory. JSON retains module, version,
destination and replacement metadata.

## Machine connection profiles

Profiles contain only `controller.url` and an optional `controller.token`.
Remove profile `state`, `secrets`, `rerun_image`, cache and log settings, and
remove project-level profile definitions. Use `sparkwing profiles add`,
`show`, `list`, `set` and `remove` to manage machine connections.

Every profile command validates the whole machine file. An invalid setting
in another section blocks reads and writes without exposing credential values.
An explicitly empty `SPARKWING_CONFIG` fails. Unset it to use the default path.
No reader imports old per-file settings.

Adding and removing profiles preserves concurrent edits. If a connection
changes during credential revocation, removal fails and retains that changed
connection. Reload it before retrying. The removed `configure init` handler
and its permission-repair behavior are gone.

## Runner enrollment files

Runner settings use the `runner:` section, `repos` and `credentials_dir`.
The reusable credential is stored in a private file inside that directory.
Remove inline runner tokens and the old `agent:` and `allow_repos` keys.
Re-enroll with `sparkwing runners add --runner NAME --profile PROFILE --force`.
Its JSON receipt contains credential metadata rather than the bearer.

Enrollment previously wrote `max_concurrent: 2` when the flag was absent.
It now leaves that preference unset, using the shared default of one.
Pass `--max-concurrent 2` to retain two concurrent jobs. Existing explicit
values remain unchanged.

If writing the configuration or installing its service fails, enrollment
revokes the new credential and removes the new credential files. A failed
replacement restores the previous runner section. Concurrent configuration
changes are preserved. A controller revocation failure prints the exact
manual revocation command and returns nonzero.

## Queue and concurrency views

Replace bare `sparkwing queue` listings with `sparkwing queue list`.
Replace `sparkwing cluster concurrency --namespace GROUP --profile P` with
`sparkwing queue list --group GROUP --profile P`. Bare `queue` shows help.
The client uses `/api/v1/queue` and `/api/v1/queue/groups/{group}`.
Connection failures return nonzero without an empty queue record.

Use `queue priority --group GROUP --profile P --run ID --set VALUE` for a
group rank change. Local admission priority keeps its existing admission
operation. See [Queue views](../diagnosing-admission.md#queue-views) for the
unfinished native controller integrations.

## Local cache warming

Before, `sparkwing pipeline sparks warmup` compiled a pipeline and uploaded its
binary to gitcache. Local execution could also download that binary.

Use `sparkwing cache warm --checkout /path/to/repo` to prepare the local cache.
Go pipelines build on this machine. TypeScript checkouts install dependencies.
Compiled pipeline binaries stay local. Remove remote binary-upload steps from
warming scripts. Explicit result publication and memo/artifact reads use their
separate run options.

JSON warming output is one preparation receipt. Plain output is the prepared
path. Scripts that consumed progress from stdout must read stderr instead.
See [Warmup](../sparks.md#warmup) for native process-policy integration status.

## Controller diagnostics

Before, `sparkwing doctor` opened the local store and repaired abandoned state.
It returned repair-oriented fields such as `orphaned_runs` and
`permission_repairs`.

Doctor now gets controller findings through the lifecycle interface. Scripts
consume `findings`, `controller_checked` and `controller_error`. A failed
inspection returns nonzero and can still include partial findings. Treat an
empty array as complete only when `controller_checked` is true.

Doctor no longer repairs private-home permissions or reaps processes from the
CLI. Remove automation that expects those side effects. See
[Doctor reports](../diagnosing-admission.md#doctor-reports) for the unfinished
native runtime and machine-reader integration.
## Retry requests and receipts

Use `runs retry --run RUN --failed` to reuse successful jobs or `--all-jobs`
to execute the complete sealed plan again. Repeated `--run`, comma lists and
`--run -` select multiple runs. The old `full=1` query and legacy retry client
method are removed. Each source run gets an independent idempotency key.

Local and remote retries use the controller API. JSON emits one safe receipt
or structured failure per source run. Plain emits new run IDs. The CLI no longer
opens local or standalone state files to retry runs. Native retry intake, reuse
and here handoff still require integration. An incomplete here handoff exits 3
and its JSON failure receipt identifies the created run without its credential.

## Plan inspection

Use `sparkwing pipeline plan --pipeline NAME -- <pipeline arguments>` to supply
required inputs. The registered SDK entrypoint selects the code; the invoked
project name remains in the run context. Secret inputs reach planning but are
redacted in previews and structured diagnostics. The preview JSON uses `jobs`
in place of `nodes`.
## Memo copies use transactional blob records

`AttemptReport.OutputFrom`, its `output_from` payload and
`Store.FinishNodeCopyingOutput` are removed. Use `blob.RecordStore.CopyMemo`
for authorized output and manifest materialization. The host memo layer
already calls it. Requests carrying `output_from` now fail strict decoding.
No alias or conversion of stored output pointers is provided.

The unused `backend.StoreBackend.GetNodeOutput` and
`backend.ClientBackend.GetNodeOutput` methods are also removed.

## Filesystem object writes use signed transfers

`fs.ArtifactStore`, `fs.NewArtifactStore` and the sharded filesystem object
layout are removed. Use the shared signed-data backend for byte transfers and
the controller for log deletion. Use `fs.NewUsageReporter` for deployment usage
totals. It exposes no object reads or writes. No object conversion is provided.

Run deletion and team purge/recheck use the configured common backend to remove
logs across stored retention classes. They leave CAS objects to reachability
collection. Authenticated log and host bindings remain integration work.

## Filesystem transfers use the shared data backend

The standalone controller opens its filesystem data backend under its home
when no S3 bucket is configured. Signed transfers use
`/api/v1/data/uploads/{upload}`, `/api/v1/data/objects/{key...}` and
`/api/v1/log-segments/{key...}`. They use the same verified physical objects
as the record-backed maintenance pass.

Removed `Server.WithOutputSignKey`, the older `/api/v1/outputs` handlers,
filesystem output moves and the raw filesystem log adapter. No data conversion
or fallback is provided. The legacy S3 output/log adapters still require their
final authenticated cutover. Local-controller construction and authenticated
data/host bindings remain separate integration work.

## Log readers expose only reads

`storage.LogStore` provides `Read`, `ReadRun` and `Stream`. Its unused
`Append` and `DeleteRun` methods and the corresponding `client.NodeLogs`
methods are removed. Writers publish tagged segments with `logseg.Sink`.
Delete a run through the controller's run API to remove its logs.

```go
// Before
logs.Append(ctx, runID, jobID, data)
logs.DeleteRun(ctx, runID)

// After
sink.Append(data, "", 0, 0)
controllerClient.DeleteRun(ctx, runID)
```

`storage.SafeLogIDs`, `SafeRelPath` and `SafeArtifactKey` are removed with
unused raw storage surfaces. Use `blob.LogObjectKey` for tagged log slots and
`blob.RecordNamespace` for record namespaces. Those helpers validate their
structured inputs.

## Filesystem logs use tagged segments

`fs.LogStore` and `fs.NewLogStore` are removed. Log writers use a tagged
`logseg.Sink` with the host's bound `logseg.SubjectUploader`. Filesystem and S3
storage use the same immutable segment format.

`conformance.TestLogStore` is removed.
Tagged-log coverage lives in `pkg/storage/logseg` and the storage acceptance
cells, including verified offset reads and physical retention.

Daemon handshake and status responses drop `artifact_store_error`, which
described a cache backend the daemon no longer opens. Remove that field from
status consumers. API binding faults still use `api_ready` and `api_error`.

## Log identities use tagged subjects

- **Before:** log manifests carried separate run, node, attempt and generation
  fields. `logseg.AttemptPrefix` built an attempt-only key.
- **After:** manifests require `subject`, which identifies a job attempt or a
  trigger, plan or eval unit. Job keys include `/jobs/`. Owner keys include
  `/units/`. Readers reject a manifest whose subject differs from its key.
- **Upgrade steps:** pass `Options.Subject` when creating a log sink and use
  `blob.LogObjectKey` for signed slots and `blob.LogSubjectPrefix` for prefixes.
  The former `logseg` key helpers are removed. Update readers and writers together. Existing
  untagged manifests require a fresh run. There is no old-format reader.

## Direct source fetches own their Git objects

- **Before:** direct fetches reused a bare mirror per remote and checked out
  linked worktrees. Runner cleanup swept idle mirrors.
- **After:** each checkout fetches from upstream and owns its Git objects.
  Fetches need upstream access even for a commit an earlier run fetched.
  Failed fetches remove their checkout. Existing destinations remain untouched.
- **Upgrade steps:** stop older runners and their active runs before removing
  unused `source-direct` directories under the Sparkwing home. The new runner
  leaves these directories untouched.

## Local binary caches use one layout

- **Before:** cache status counted binaries in older directories, and pruning
  quarantined those entries before deleting them after a grace period.
- **After:** status and pruning use `cache/pipelines/v1/entries` in the Sparkwing
  home. Older directories are left untouched. The CLI's JSON cache report drops
  `legacy_entries` and `legacy_bytes`.
- **Upgrade steps:** update report consumers to use `entries` and `total_bytes`.
  Pipelines without a current cache entry compile on their next run. Stop older
  Sparkwing processes before removing their unused cache directories.

## Node state writes use transitions

`SetNodeStatus` and `RevokeNodeReady` on Store, Tenant and Client are removed.
The per-node `status` and `revoke-ready` HTTP endpoints are removed with them.
Use `Tenant.TransitionNode` for lifecycle changes. Queue withdrawal supplies
`To: NodePending`, `Cause: CauseRevoke` and the current run owner's authority.
Only ready, unclaimed jobs can be withdrawn. A competing claim or stale owner
returns an error without changing state or events. An identical committed
transition replays.

Debug pauses write `paused` to activity detail and clear it on resume.
They preserve the job's lifecycle status, start timestamp and claim.

## Owner leases replace run heartbeats

`TouchRunHeartbeat` and `TouchNodeHeartbeat` on Store, Tenant and Client, plus
`Run.LastHeartbeatAt`, are removed. Hosts use `Store.RenewUnit` or
`Tenant.RenewOwner` with their current owner authority. Owner renewal also
renews self-claimed jobs. A remotely renewed job
cannot extend the owner's lease.

Doctor no longer finalizes runs from heartbeat age or reports `orphaned_runs`.
The controller's elected `ReapUnits` pass recovers expired owners under the
store clock. Started local orphans fail in place. Unstarted owners requeue.
The legacy run-heartbeat and node-touch HTTP handlers are removed. The old
Kubernetes dispatcher no longer sends advisory heartbeat writes for its pods.
The pod's own claim renewal supplies its liveness timestamp.

## Plan acceptance uses tenant seals

`Store.AcceptPlan`, its legacy plan validator and `Client.SubmitPlan` are removed.
Use `Tenant.AcceptPlan` with `PlanAcceptance`, including the admitted request,
checkout declarations, resolved settings and the v2 `jobs` document. The store
reruns the shared validator and settings resolver before committing both the
second seal and jobs. Identical submissions replay. Changed sealed input returns
`spec_drift`. The controller's legacy claim-token plan-acceptance branch and the
old launched planner are removed.

Missing required secrets return canonical `plan_invalid`. The detailed refusal
is saved once for run status, after the failed acceptance rolls back, under the
current owner fence and store clock. No seal or job is committed by that refusal.

## Unit claims reserve compute credit

Metered `Store.ClaimUnit` awards commit a refundable compute reservation with
the claim. The award retains its CPU class, rate and payer after settings change
or bootstrap exchange. Unfunded queued work leaves funded matches eligible.
The shared billing calculator is required for renewal and final settlement.

## Offer-round fallback is removed

The warm dispatcher, its factory and the `finalize-ready` endpoint are removed.
`Store.FinalizeClaimRound`, `ClaimRoundResult`, `WithQueueRunners` and
`Client.FinalizeNodeReady` are removed with them. Use `Store.ClaimUnit` for queued
or named claims and `Tenant.ReconcileNode` for owner execution decisions.
Eligible work can be claimed immediately. Soft preferences rank eligible work.
A mismatch leaves the ready row and its queue position unchanged.

## Concurrency maintenance requires the operator fence

`Store.MaintainConcurrency` and the `store.Maintenance` registry are removed.
Acquire `controller.reaper` through `Operator.AcquireSingletonLease` and pass that
fence to `Operator.MaintainConcurrency(ctx, lease, options)`. The pass commits
holder cleanup, waiter cleanup and promotion together. An error returns no
committed counts. The old concurrency-cache sweep and count APIs are removed.
Memo-record retention uses `Operator.MaintainRetention` under its maintenance
lease. `Store.PurgeDeadLocalConcurrency` and `Store.CountDeadLocalConcurrency`
are removed. The elected pass cleans terminal capacity in every scope. Doctor's
`dead_concurrency_holders` and `dead_concurrency_waiters` fields are removed with
its independent repair path.

## Egress accounting uses tenant and replica counters

`Store.RecordEgressUsage`, `Store.ListEgressUsage` and `Store.PruneEgressUsage`
are removed. Write additive bytes with `Tenant.AddUsage` using `egress_bytes`,
a UTC day and the controller's trusted replica ID. Read day or month totals with
`Tenant.EgressUsage`. Deployment reports use `Operator.EgressUsageAcrossTeams`.
Pruning uses `Operator.PruneEgressUsage` with the maintenance lease.

The controller records delivered successful-response bytes at request completion,
including partial transfers and reads served without an in-memory meter. Logs
consume egress while remaining exempt from download caps. Alarm reload uses team
totals. A failed counter write emits an error. A controller crash before request
completion can lose that request's egress sample. Thirteen months of reports stay
available. There is no principal-based persistence or periodic flush.

## Event charges use tenant handles and replica identity

`Store.AppendEventCharged` is removed. Obtain a tenant with `Store.ForTeam`,
then call `Tenant.AppendEventCharged(ctx, replicaID, runID, nodeID, kind, payload)`.
The replica ID is the controller's trusted identity. The caller no longer
selects a charged principal. The tenant supplies the team and the store owns time.

Event writes, retained run counters, shared storage quotas and replica usage
commit together. Pending uploads and events share monthly and run byte capacity.
Events consume no object capacity. Usage is recorded without a configured quota.
The existing event limits and free-tier policy keep their meaning. The HTTP
request and response shapes stay the same.

## Download charges use tenant handles and replica identity

`Store.ChargeDownload` is removed. Obtain a tenant with `Store.ForTeam`, then
call `Tenant.ChargeDownload`. `DownloadCharge.ReplicaID` identifies the trusted
controller replica. The request no longer accepts `Team` or `Now`. The store
selects the current UTC day under its shared quota lock.

Admission and replica usage commit together. Free and funded caps keep their
existing behavior, including finishing the download that crosses the cap and
recording already delivered bytes. Logs stay exempt. Controller integrations
use their existing lease-holder identity and require no new configuration.

## Paid credit requires verified checkout settlement

`POST /api/v1/credits/grants` is removed. `Tenant.GrantCredits` and
`Tenant.RecordCreditGrant` refuse `paid` grants.
Free operator grants keep their console path. Payment reversals require a
verified provider event.

Checkout integrations must supply C2's verified provider event to
`Store.SettlePayment`. The committed checkout supplies the team and credit
amount. The event must match its session, payment, amount, currency and test/live
mode. Actionable fraud warnings refuse settlement before any credit or event
receipt commits. Replayed events grant once. Provider verification and its
controller adapter remain integration work.

## Storage counters use verified upload tickets

The `/internal/storage/reserve`, `/internal/storage/commit`,
`/internal/storage/release` and `/internal/downloads/charge` endpoints are removed.
Remove cache `controller-token` credentials and service-counter integrations.
`Store.RenewStorage` and its rolling block receipts are removed.

The final storage adapter reserves quota with `Tenant.ReserveBlobUpload`, verifies
physical bytes, then commits with `Tenant.CommitBlobUpload`. Signing or transfer
failure uses `Tenant.AbortBlobUpload`. The ticket binds the exact uploader and
retains immutable replay state. The signed-data controller adapter remains an
integration dependency. There is no service-token fallback.

`SetFreeTeamSlots` writes the final settings table and refuses sealed values.
Its slot count and signup behavior keep their existing meaning.

## Storage maintenance uses committed inventory

Remove `Server.WithStoragePass` and `Server.RunStoragePass` calls. The controller's
existing maintenance timer runs leased retention, tagged-log sealing and storage
billing. An embedding that serves `Handler` directly calls `Server.MaintainStorage`.
`StoragePassEvery` is removed. `StorageMaintenanceInterval` names the timer period.

`Store.StorageMarks`, `ReconcileStorage`, `TeamsOverFreeLogShare`,
`PrunableNodeLogs`, `PruneStorageCommitReceipts` and `PruneDownloadDays` are removed.
Use `Tenant.StorageRetainedBytes` for retained inventory and `Tenant.StorageUsageFor`
for summed replica usage. `Operator.ChargeRetainedStorage` and `MaintainRetention`
require the current maintenance lease. Bucket listings no longer replace usage
counters. Log retention follows the run's persisted class. Allowance-driven
physical eviction still requires the final storage adapter.


## OAuth browser callbacks

OAuth start and callback use the shared route policy and browser-flow verifier.
Callback requires one flow cookie, one matching state and one code. A declined
callback carries one error and no code. Remove duplicate query selectors.
Refused proofs preserve the flow cookie. HEAD and unsupported methods return
the common `405 method_not_allowed` error.

Refusals return the common error envelope instead of a sign-in HTML page.
An invalid or replayed code returns `401 unauthenticated`. An unverified email
returns `403 forbidden`. Provider and session backend failures return
`503 unavailable`. The existing login budgets retain `429` and `Retry-After`.
Successful navigation and same-origin return paths retain their browser pages.

## Capability inventory and rotation

Use `GET /api/v1/tokens` instead of `GET /api/v1/team/tokens`. The collection
returns `{tokens:[...]}`. Read `subject.kind` and `subject.id` instead of
`family`, `kind`, `name` or `principal`, and `grants` instead of `scopes`.
`issued_at`, `expires_at`, `last_used_at` and `revoked_at` use RFC3339 timestamps.
Members see owned credentials. Team admins can see the team's credentials.
Deployment service/operator metadata uses `/api/v1/operator/tokens`.

Rotation uses `POST` on the selected token resource's `/rotate` suffix.
Send `grace_secs` and optional absolute `expires_at`, replacing `ttl_secs`.
Explicit expiry must pass the original parent's ceilings or current root
origin policy. Grace defaults to 24
hours, accepts zero for immediate replacement and has the existing seven-day
ceiling. Read the one-time bearer from `capability`, its metadata from `new`,
and the old credential's deadline from the RFC3339 `old_revoked_at`.
`old_prefix` replaces `old_replaced_by`. Never automatically retry rotation.

Embedded controllers provide `WithCapabilityManager`. Its reads and rotation
must recheck authority inside their store transaction. The retired token-table
APIs cannot supply this contract. Missing adapters return `policy_missing`.

## Session lifecycle

Session renewal and logout use the shared policy router with session proof.
Present one session cookie or one `Authorization: Session <id>` header.
Bearer credentials cannot substitute for a session. Renewal refuses HEAD and
ambiguous presentations and retains current membership and parent grant ceilings.

`POST /api/v1/auth/logout` takes no body. Remove `session_id` and supply the
session-bound CSRF header and cookie plus a matching Origin or Referer. Logout
ends only the verified session and clears its browser cookies. Both endpoints
return the common error envelope and no-store. Storage and key failures return
`unavailable`. Configure the bound secrets cipher before serving sessions.

Ordinary policy routes also accept the Session header and enforce the configured
absolute lifetime without renewing. Browser logout posts a form to `/logout`
with the session-bound `csrf_token`. It uses the shared proof boundary, returns
canonical failures and redirects to `/login` only after successful deletion.

## Browser capability requests

Shared policy routes authenticate browser sessions through their committed
hashed identity. Writes use the controller's session-bound CSRF verifier.
Configure the bound secrets cipher before serving sessions. The same cipher
protects the persisted session signing key. Forged, cross-session and
foreign-origin proofs return `forbidden`. Key access failures return
`unavailable`.

## Secret-by-name reads

GET `/api/v1/secrets/{name}` uses the shared capability policy. Member or
verified local authority with `secrets.read` or `team.admin` reads unmasked
config in its own team. Masked values require a live `secrets.release` claim
whose sealed run declares the name and permits its pipeline/shared scope and
reserved pool. Machine roots and deployment-admin tokens gain no read access.

Embedded controllers supply `WithSecretReader` with the authority-bearing tenant
transaction. Missing adapters return `policy_missing`. Bound encryption is
required, errors use the shared API envelope, and all responses use no-store.
Duplicate selectors and malformed queries fail after authentication.

`Server.ResealStoredSecrets` and `LegacyCipher` are removed with the startup
migration. Row-secret helpers require BoundCipher and never fall back to
unbound Seal/Open methods. Plaintext
and pre-team envelopes remain unreadable. Rewrite them through the authenticated
credential API. Explicit key rotation remains available for bound envelopes.

## Credential maintenance

Secret-key rotation requires live member or verified local `team.admin`
authority and selects only the authenticated team. Browser sessions require
CSRF. Rotation takes no body and returns the shared API error envelope.
Missing bound encryption returns `unavailable`. Plaintext and unbound rows
stay unreadable. Rewrite them through the authenticated credential API.
An unreadable git credential or any sealing failure rolls back secret and
git changes together. Unreadable secrets remain unchanged in `skipped`.

Source-release audit lists use the same team policy. Replace `token_prefix`
with `capability_prefix` in their responses. Each row also carries
`repository`, `job_id` and `generation`. Times remain Unix seconds.

The default store supplies `Tenant.RotateCredentials` and
`Tenant.SourceReleaseAudits`. Embedded controllers may replace them through
`WithCredentialRotator` and `WithSourceAuditReader`. Missing replacements return
`policy_missing`. `Store.RotateSecretValues` and `Store.RotateGitCredentialSecrets`
are removed. Select the authenticated tenant and pass its verified authority to
the combined rotation transaction.

## Credential mutations

Secret create/delete and git-credential create/confirm/delete require live
member or verified local authority with `team.admin`. Session mutations require
CSRF. The authenticated team selects the rows, and errors use the shared API
envelope. A deployment-admin bearer gains no team credential authority.

Embedded controllers supply `WithCredentialWriter` with tenant transactions
that recheck authority before commit. The default refuses writes with
`policy_missing` until that adapter is installed. Existing unfenced Tenant
writers cannot stand in for it. Secret names must pass the shared grammar on
replacement too. Omit an SSH port to use 22. An explicit zero is invalid.

## Credential inventories

Secret and git-credential lists use the shared capability policy. Use a live
member or verified local credential with `runs.read`. Secret inventory also
accepts `team.admin`. The authenticated team selects the rows. Machine roots,
claims and bootstraps cannot list them. Masked secret values and stored git
credentials remain private. An unreadable public config value fails the whole
secret list instead of disappearing from a successful response.

Embedded controllers may supply `WithCredentialInventoryReader` using Tenant's
existing read methods. Its reader must belong to the authenticated team.

## Source credentials

`POST /api/v1/runs/{run}/source-credential` takes no body and requires a live
`source.read` capability bound to the run. The response carries `kind`, `host`,
`secret`, optional `username` and optional `known_hosts`. Checkout identity and
policy come from the sealed execution input. The store commits authorization
and its audit before the controller decrypts the credential.

Move machine consent to `PUT /api/v1/tokens/{token}/git-credentials` with
`{"enabled":true}` or `{"enabled":false}`. The machine owner's account session
and CSRF proof are required. Team administration cannot substitute for ownership.

Embedded controllers may supply `WithSourceController` with the declared
transaction and bound-cipher interfaces. `WithSecretsCipher` also configures
C1's plan-secret opener. Missing keys refuse decryption and plan validation.

## Work claims

Use `POST /api/v1/runners/claim` with the shared claim request instead of the
separate trigger and job queue endpoints. Every requested kind needs its own
grant. The response contains a private `claim`, or an empty object when no work
matches. Enrollment supplies the runner identity, repository scope and pools.
The body supplies labels, current availability and the runner version.

`POST /api/v1/runs/{run}/claim` acquires the named trigger, including a reserved
here run. It requires the single `trigger` kind. `POST /api/v1/launcher/claim`
requires launcher authority and accepts plan, eval and job kinds. Configure the
engine controller's verified runner policy and the store's enrollment resolver
before accepting claims. Existing holders may renew while software policy
blocks new awards.

Send a pod bootstrap to `POST /api/v1/claims/exchange` with an empty JSON object.
It yields the original award's full in-memory capability once and consumes the
bootstrap. Renew through `POST /api/v1/claims/{claim}/heartbeat`, where `claim`
is the award's unit ID. Renewal cannot start a job without its start transition.

Claim, exchange and reconciliation responses carry bearer material once. Do not
retry a lost response automatically. The unused award recovers through its lease.
Launcher sync uses `{observed:[{unit_id,job_name,node_uid}]}` and returns awards
with empty capabilities plus job names to remove. Embedded controllers must
supply `EngineController.Launcher`. Missing snapshot wiring returns
`policy_missing` and exposes no award.

The standalone artifact-manifest write is removed. Publish manifests through
record and result commits. Legacy remote state clients cannot publish a manifest
separately. Local state writers retain their existing direct persistence while
the host moves to result transactions.

## Data writes and records

Upload and commit use live controller capabilities through the shared policy
router. Replace `{kind,key,size,sha256,run_id}` with `{digest,size}` on
`POST /api/v1/data/upload`. PUT the bytes with the returned signed headers,
then commit with `{upload_id}`. Commit returns `{digest,created_at}` with
HTTP 200. Cache grants and the pretrigger user-token upload path are removed.

Create references with `POST /api/v1/data/records` and look them up with
`GET /api/v1/data/records`. The controller derives the team, repository, write
ref and uploader. Record responses omit private authority and receipt facts.
See [Direct data uploads](../data-uploads.md) for the request fields.

Embedded controllers must configure `WithDataController`. Upload discovery
uses those adapters. The older direct S3 output/log configuration does not
advertise authenticated data writes.

## Data reads

`POST /api/v1/data/download` accepts `{"digest":"<sha256>"}` through the shared
capability policy. Replace cache grants and `{kind,key}` requests with a live
data capability and an authorized digest. Responses include signed `headers`
alongside `url`, `sha256`, `size` and `expires`. Send those headers to storage.

`POST /api/v1/data/has-many` returns only readable live digests from its
`digests` array. Missing and unreadable objects are indistinguishable. This
query does not consume the team's download allowance. Both routes return
the common error envelope.

## Run detail

`GET /api/v1/runs/{run}` returns `RunDetail` with display-safe `inputs`,
`jobs`, `attempts`, `logs`, `engine` and `notices`. Replace readers of raw
run fields and the former `{run, nodes}` wrapper with this shared shape.
Fetch approval, step and child details through their own resources.

`include=secret_values` is removed. Secret argument values stay redacted for
members, claims and local peers. Missing or invalid classification hides every
value, including on finished runs. Execution inputs travel in the private
claim wire. Members require `runs.read`; bound claims require `source.read`
or `trigger.host` for their run. Bare machine roots cannot use this read.

Embedded controllers must configure the shared authorization adapters and
`EngineController.ReadRunStatus` when overriding the default
`Tenant.ReadRunView` adapter. The reader supplies one committed snapshot,
complete attempt history and validated log subjects for the selected team and
run. Missing wiring refuses the request with `policy_missing`.

## Queue reads

Read `/api/v1/queue` instead of `/api/v1/queue/state`. The CLI's remote queue
reader and the dashboard use the new path. The old path is removed. The
response retains the concurrency groups, holders, waiters and runner headroom
for the authorized team.

A member or verified local peer needs `runs.read`. Machine roots and work
claims cannot read the team queue. Configure the controller's shared verifier
and binding adapters before serving the route. Errors use the common envelope.
Embedded controllers may supply the existing tenant read contract through
`WithQueueReader`. Its default uses the server's store.

## Credit ledger reads

`GET /api/v1/credits` and `GET /api/v1/credits/history` use the shared
capability verifier. A member or verified local peer needs `runs.read` for
its active team. Configure the controller's shared authentication and binding
adapters before serving these routes. Machine roots and claims cannot read
the team ledger, even when they carry `runs.read`.

The paths and successful response fields stay the same. Errors use the
canonical envelope. Missing or dead credentials return `unauthenticated`.
Live credentials without member authority return `forbidden`. Authorized
reads return `not_found` when credit billing is disabled. History accepts
one nonnegative `limit`, at most 1000 rows of each kind. Omission or zero
uses the store's existing default.

## Job status

Read `/api/v1/runs/{run}/jobs` and `/api/v1/runs/{run}/jobs/{job}` instead
of the corresponding `/nodes` paths. The collection returns a JSON array of
safe job status objects. The detail returns one such object. Both use `id`
for the job identifier and omit raw declarations, environment, holder and
capability fields. Private execution inputs use the awarded execution wire.
The existing Go client's status readers use these paths and the canonical
error envelope. Missing jobs return the typed `not_found` API error.

Members need `runs.read`. The live run owner may read the collection and
details with `trigger.host`. A job's live `data.read` authority reaches its
own detail only. Controllers need the shared authentication/binding adapters
and C1's tenant snapshot/node reads. No old path or credential fallback remains.

## Pipeline sizing

Replace `/api/v1/pipelines/{name}/profile` with
`/api/v1/pipelines/{name}/sizing` for the read and its `pin`, `observations`,
`contention` and `waits` operations. Replace the `node` query with `job`.
The old paths are removed. The Go controller client uses the new paths.

Reads require member `runs.read` or an executable claim with `source.read`
and authority for the sealed repository and pipeline. A bare runner or
Actions credential no longer reads arbitrary sizing records. Bootstrap
credentials cannot read sizing. Writes require `node.report` on the bound
pipeline. Admin grants cannot bypass the claim check.

Controllers must configure the shared authentication and binding adapters.
Sizing writes also require an authority-bearing transaction adapter through
`WithSizingStore`. An absent adapter returns `policy_missing`. Implementations
must recheck revocation, membership and the claim fence under the write lock,
and avoid duplicating a measurement on an exact retry.

## Credential discovery

`GET /api/v1/auth/whoami`, `GET /api/v1/services` and
`GET /api/v1/data/capabilities` require live capability authority, a session or
a verified local peer. Service and data discovery also require an active team.
Configure the controller's shared authentication reader and transport adapters
before serving these routes. An incomplete policy returns `policy_missing`.

`whoami` returns the committed subject ID in `principal`, its subject kind in
`kind`, and effective grants in `scopes`. The kind is `account`, `machine`,
`service`, `operator`, `local` or `anonymous`. `token_prefix` is present only
for a capability. The response omits `role` and session identifiers.
Callers should handle the common `unauthenticated`, `forbidden` and `not_found`
error envelope. Bootstrap and eval credentials cannot discover services.

The OIDC discovery document and public keys stay public. Their disabled-issuer
response uses the same error envelope. All five discovery routes accept only
`GET` and return `method_not_allowed` with `Allow: GET` for other methods.

## Job reporting

Annotations, summaries and step start, finish and skip requests use
`/api/v1/runs/{run}/jobs/{job}`. Replace `/nodes/{nodeID}` in those seven
reporting paths with `/jobs/{job}`. The Go controller client uses the new
paths. The old paths are removed.

Reports require an awarded job or trigger capability with `node.report`, or
the run's local publication authority. Caller-supplied holder and generation
headers grant no authority. Step finish accepts `passed`, `failed` and
`cancelled`, plus an optional inner error with a code and masked message.
Annotations and summaries accept at most 128 KiB of masked text.
## Unit recovery requires the elected reaper lease

`Store.ReapExpiredNodeClaims`, `Maintenance.RecoverExpiredNodeClaims`,
`Maintenance.ReapExpiredTriggers` and `AgentLossRecovery` are removed. Acquire
`controller.reaper` through `Operator.AcquireSingletonLease`, then pass that fence
and a batch limit to `Store.ReapUnits`. Results identify the team, run and unit.
Lease loss requeues unfinished remote work within the same run. Started local
owners fail in place. Unstarted owners consume no execution attempt or disruption.
`Store.RequeueUnstartedClaim` and `Tenant.RequeueUnstartedClaim` are removed.
Expired owner awards use the same leased pass. An unstarted owner requeues
without a disruption, and a replacement award advances its generation.
The former holder cannot renew, finish or accept a plan for the replacement.

## Build trust uses tenant handles

`Store.TrustLocalBuilds` and `Store.SetTrustLocalBuilds` are removed. Obtain a
tenant with `Store.ForTeam`, then call `Tenant.TrustLocalBuilds` or
`Tenant.SetTrustLocalBuilds` without a team argument. Local stores use
`store.DefaultTeam`. The default is false. The HTTP build-trust routes and
their membership requirements keep their existing behavior.

## Financial totals use persisted awards

`Store.CreditLedgerTotals` subtracts refundable reservations using the immutable
billing awards and intervals. Reading a report leaves billing checkpoints and
intervals unchanged. Unstarted metered awards contribute no settled seconds.
Open compute intervals stop at the persisted charged checkpoint in reports.
Storage charges affect balances but contribute no compute seconds.

The unused `Store.RunFinished` method is removed. Leased retention and tagged
log-stream maintenance own pruning.

## Grant requests use tenant handles

`Store.RecordCreditGrant` is removed. Obtain a tenant with `Store.ForTeam`,
then call `Tenant.RecordCreditGrant`. Local stores use `store.DefaultTeam`.

## Billing reads use tenant handles

`Store.CreditBalanceMicro`, `Store.CreditState` and `Store.ListCreditGrants` are
removed. Obtain a tenant with `Store.ForTeam`, then call the matching Tenant method.
Local stores use the handle for `store.DefaultTeam`.

## Rate-table presence uses the settings snapshot

`Store.CreditRateTableSet`, `Store.CreditRateMicroPerSecond` and
`Store.CreditGraceSeconds` are removed. Read `CreditSettings.RateTableSet`,
`CreditSettings.RateMicroPerSecond` and `CreditSettings.GraceSeconds` from the
same settings snapshot.

`Store.SetCreditRateTable` is removed. Write the table through
`SetCreditSettings` with `CreditSettingsUpdate.RateTable`.

## Abandoned logs use tagged streams

`Store.AbandonedNodeLogs` and the controller's old periodic node-log sweep are
removed. `Operator.SealAbandonedLogStreams` closes existing execution streams as
incomplete under the maintenance lease. Job attempts keep their 60-second final
tail grace. Trigger, plan and eval streams use their own persisted unit fences.
Sealing preserves committed segments and job outcomes. Published logs still
require the publisher's final commit because they have no execution lease.

## Source bundles use record reachability

`Store.ClaimExpiredSourceBundles`, `Store.DeleteSourceBundleRows`,
`Store.PruneExpiredUploads`, `store.DirectCacheMaxAge` and
`controller.CacheObjectMaxAge` are removed. Cache listings no longer expire bytes
by object age. Keep source bytes through blob records. Run
`Operator.MaintainRetention` under the maintenance lease to expire records and
release upload-ticket quota holds. Use `Operator.MarkBlobTombstones` and
`Operator.CollectBlobTombstones` with the verified object backend for physical
collection after the 24-hour grace. Rebuild pre-launch stores from the final
schema. There is no old-row migration or compatibility adapter.

## Node logs reach the controller as segments

- **Before:** a Kubernetes Job, warm runner, self-hosted agent, GitHub
  Actions runner, launcher Job or the trigger runner's in-process nodes
  appended node logs to the logs service at their logs URL, as did a laptop
  run against a remote profile, and without one
  wrote them to a disk nobody could read and that went away with the pod.
  `sparkwing runs logs --profile` and `runs logs --grep --profile` read the
  logs service the profile or the controller named.
- **After:** against a controller that keeps segments, every one of those
  runners uploads each node's log as gzip segments, each through a URL the
  controller signs for its exact size and Content-MD5, plus a sealing
  manifest it posts to the controller, into the controller's cache bucket
  (`--cache-blob-store`) or, without one, its output directory, whether or
  not it has a logs URL. The dashboard and the CLI read every node through
  the controller, which serves segments first and the logs service for a
  node that wrote none, so logs the service already holds stay readable.
  Deleting a run or a team deletes its segments.
- **Operator steps:** keep the logs service running so its existing logs
  stay readable. On an S3 cache bucket, grant the controller
  `s3:GetLifecycleConfiguration` and add enabled lifecycle rules expiring
  `<cache prefix>/logs/r90/` after 90 days and `<cache prefix>/logs/r30/`
  after 30, plus `<cache prefix>/logs/r<days>/` after `<days>` for each
  team `--log-retention team=days` names; [self-hosting.md](../self-hosting.md#node-log-retention)
  has the command. Until a class has its rule, the controller issues no
  upload URLs into it and those nodes' runners keep writing to their logs
  URL, or their own disk without one. A controller that keeps segments on
  its own disk needs no rule and keeps them until their run is deleted.
  On a multi-team controller segment bytes count against the team's log
  storage.
- **Logs the service already holds:** nothing converts them; they become
  unreadable when you retire the service.
- **Mixed versions:** upgrade the controller first. A runner that predates
  segments keeps writing to its logs URL, which the controller still reads
  through. A runner of this release against an older controller finds no
  segments advertised and writes to its logs URL, or locally without one,
  as before. Rolling the runners back returns their new logs to the logs
  service.

## The logs service is removed

This release ships no `sparkwing-logs`. Its steps replace the ones in
[Node logs reach the controller as segments](#node-logs-reach-the-controller-as-segments)
that keep the logs service running.

- **Before:** `sparkwing-logs` held node logs that a runner without
  segments, or a node from before segments, wrote; the controller read
  through it and announced it as `logs` in `GET /api/v1/services`. The
  runner-bundle chart deployed it, and runners, agents and profiles named
  its URL.
- **After:** the controller is the only place node logs go. It keeps them
  as segments in its cache bucket (`--cache-blob-store`) or, without one,
  its output directory; a controller with neither keeps no node logs, so a
  runner keeps the log on its own disk. Pipeline binaries built against any SDK
  keep logging unchanged: the runner serves their appends on loopback.
- **Before you upgrade:** nothing converts the service's logs; delete its
  volume once you no longer need the files.
- **Helm:** delete `sparkwing-runner-bundle.logs` (or `logs` for a
  standalone runner bundle) and `controller.logs` from your values; a
  values file that still sets them fails to render. The upgrade removes the
  logs Deployment, Service and ServiceAccount; the logs PVC is kept by its
  resource policy, so delete it once the export is done:

  ```sh
  kubectl -n <namespace> delete pvc <release>-sparkwing-runner-bundle-logs
  ```

- **Controller:** drop `--logs-url` and `--logs-archive-store`, and delete
  the `logs-delete-token` file from `--credentials-dir` (or the
  `controller.credentialsSecret` key). Nothing reads it. Leased maintenance
  bills committed blob and tagged-log inventory. Deleting a team deletes its
  segments.
- **Runners and agents:** drop `--logs` from `sparkwing-runner runner` and
  `launch`, `--trigger-runner-logs-url` from `runner`, and the `logs:` key
  from an agent's `config.yaml`, which now fails to load with
  `field logs not found`. `sparkwing cluster runners add` has no `--logs`.
  Drop any `SPARKWING_LOGS_EGRESS_*` variables; nothing reads them.
- **Profiles:** the `logs` and `cache` surfaces are gone; remove them.
- **Credentials and API:** the `logs.write` and `logs.delete` scopes are
  gone, and minting a token with either is refused; runner tokens carry
  `nodes.claim`, `triggers.claim`, `runs.state` and `secrets.read`.
  Existing tokens that list them keep working with the rest of their
  scopes. `GET /api/v1/runs/{id}/log-access`,
  `POST /api/v1/runs/{id}/nodes/{nodeID}/claim/validate`, the `logs` field
  of `GET /api/v1/services` and signed `kind=log` downloads are removed;
  read logs through `GET /api/v1/runs/{id}/logs/{node}`. The storage
  counter routes answer only the cache's operator token.
- **Laptop:** `sparkwing serve` runs no logs service and writes only
  `SPARKWING_CONTROLLER_URL` to `dev.env`; the dashboard reads node logs
  from the run directories, and `sparkwing runs prune` removes them through
  the dashboard.
- **Go packages:** `pkg/logs` and `pkg/storage/sparkwinglogs` are removed.
  Read node logs with `client.NodeLogs` from
  `pkg/controller/client`, and their completeness with `pkg/storage/logseg`.
- **Mixed versions:** upgrade the runners and the controller together. A
  runner from before segments has nowhere to send its logs once the
  service is gone, and keeps them on its own disk.

## Operator powers need the operator console

- **Before:** an admin token reached a team's billing standing and the
  sign-up waitlist through `GET`/`POST /api/v1/teams/{team}/trust`,
  `GET /api/v1/signups/waitlist`, `POST /api/v1/signups/waitlist/approve`,
  `free` and `reversal` grants on `POST /api/v1/credits/grants`, holds
  naming a team and every release on `POST /api/v1/credits/freezes`, and
  refunds on `POST /api/v1/credits/reversals`. It also passed every other
  `credits.grant` route.
- **After:** those powers answer only a listed operator's own signed-in
  console session (`--operator-accounts`). The four admin routes above are
  removed. Paid checkout events use the signed `/webhooks/stripe` callback.
  `POST /api/v1/credits/grants` is removed.
  The card, refund, warning, dispute, unit and payment-lookup service routes
  under `/api/v1/credits/` are also removed. No service token can submit
  financial outcomes. Card and dispute provider-event integration remains
  incomplete. The console's trust action sets the daily cap, card rung and
  Cloud node cap alongside the purchase limit, and its unfreeze releases a
  payment dispute's hold named by `hold`. The new
  `sparkwing-controller admin freeze|unfreeze` holds or releases a team
  from inside the controller pod when console sign-in is down.
- **Migrate:** list the operators' account ids in `--operator-accounts`
  (Helm: `controller.operatorAccounts`) and have each sign in with Google
  or GitHub before the upgrade; an operator token's session does not reach
  the console. Move each call:

  | Before (admin token) | After |
  |---|---|
  | `GET /api/v1/teams/{team}/trust` | `GET /api/v1/operator/teams/{team}`, field `billing` |
  | `POST /api/v1/teams/{team}/trust` | `POST /api/v1/operator/teams/{team}/trust`, same body |
  | `GET /api/v1/signups/waitlist` | `GET /api/v1/operator/waitlist`, newest first, with `offset` |
  | `POST /api/v1/signups/waitlist/approve` | `POST /api/v1/operator/waitlist/approve`, same body |
  | `free` grant on `POST /api/v1/credits/grants` | `POST /api/v1/operator/teams/{team}/grants` with `amount_cents`, `reason` and `key` |
  | `reversal` grant, or a refund on `POST /api/v1/credits/reversals` | refund in the Stripe dashboard; callback settlement remains incomplete |
  | hold naming `team` on `POST /api/v1/credits/freezes` | `POST /api/v1/operator/teams/{team}/freeze` |
  | `release` on `POST /api/v1/credits/freezes` | `POST /api/v1/operator/teams/{team}/unfreeze`, with `hold` for one dispute's hold |

  A script that used the admin token for any row above now gets `404` or
  `403`.
- **The checkout service's token:** `credits-token` is removed from controller
  credentials. Delete its configuration after switching checkout settlement
  to the signed provider callback. The controller no longer seeds a service
  token on startup.
- **Runner permissions:** GitHub Actions enrollment moves from
  `/api/v1/team/github-runners` to `/api/v1/runners/github`. Removal selects
  the positive numeric repository ID in `/api/v1/runners/github/{repository}`.
  Both mutations require live team administration and session-bound CSRF
  when using a session. The dashboard uses these paths.
- **Runner metering:** `/api/v1/tokens/{prefix}/metered` is removed. Use the
  console-only `/api/v1/operator/runners/{runner}/metered` with an explicit
  boolean `metered`. The runner selector is the enrolled root's public
  capability prefix. The response contains `runner` and `metered`.
- **Go:** pass the actor, the reason and the one hold's id to
  `Store.ReleaseCreditFreezes`, which refuses an empty id; a
  caller of the removed `Store.DisputeTeam` reads the team from the hold it
  holds, or from `Tenant.CreditFreeze`.

## Password users are removed

- **Before:** the controller kept dashboard users with argon2id passwords.
  `POST /api/v1/auth/login` and the `/login` form exchanged a username and
  password for a session, `GET`, `POST` and `DELETE /api/v1/users` and
  `sparkwing cluster users add|list|delete` managed the accounts, and an open
  single-team controller offered a "Create first admin" form at
  `/login/bootstrap`.
- **After:** dashboard sign-in is Google or GitHub, where a multi-team license
  and an OAuth client offer them, or an operator token: a user token holding
  `admin`, such as the bootstrap admin token, pasted into the `/login` form.
  The routes, the form, the CLI commands and the client's `CreateUser`,
  `ListUsers` and `DeleteUser` are gone; each route answers `404`, or `401` to
  a request with no credential. Schema v96 drops the users table and ends
  every password session, so each person signs in again. A token session
  follows its token: revoking or expiring the token ends it.

Before upgrading:

1. Choose how each person signs in. On a controller with a multi-team license,
   configure Google or GitHub sign-in (`--google-client-id`,
   `--github-client-id`, their secrets and `--oauth-redirect-uris`, as
   [auth.md](../auth.md#google-and-github-sign-in) lists) and invite each
   former password user to their team. Without a license the controller offers
   no identity provider, so the operator token is the dashboard's only sign-in.
2. Make sure the operator holds an admin token. A controller provisioned with
   `--bootstrap-admin-token-file` (chart: `controller.bootstrapAdminToken.name`)
   already has one. Otherwise mint one while a password admin can still act:

   ```bash
   sparkwing cluster tokens create --type user --principal operator \
       --scope admin --profile prod
   ```

3. Remove `sparkwing cluster users` from scripts. A script that used
   `POST /api/v1/auth/login` for a session calls the API with a bearer token
   instead.

**Edge cases:**

- A password user with a narrower scope set has no direct replacement: an
  operator token carries `admin`. Give that person an account on a licensed
  controller with the matching role, or a bearer token of the narrower scopes
  for the CLI.
- An open controller (empty tokens table) serves the dashboard without
  sign-in, as before, and its `/login` redirects to the page asked for.
- Rollback: an older controller or CLI refuses the new store. Roll back by
  restoring a backup taken before the upgrade.

## Team CLI and runner tokens become team tokens

Scripts that mint team CLI or runner tokens through the API move to one route.
Tokens already minted keep working.

- **Before:** a team member minted a personal CLI token with
  `POST /api/v1/team/cli-tokens` and an editor minted a runner token with
  `POST /api/v1/team/runner-tokens`. Each kind had its own `GET` list and
  `DELETE /{prefix}` revoke, and an owner released git credentials to a
  machine with `PUT /api/v1/team/runner-tokens/{prefix}/git-credentials`.
- **After:** those routes are gone with no alias.
  `POST /api/v1/team/tokens` mints a team token (`swt_`) whose `scopes` come
  from exactly one family. Member scopes (`runs.read`, `logs.read`,
  `triggers.read`, and for an editor or owner `runs.write`, `runs.control`,
  `approvals.write`) make a CLI token: any member mints one, with no `name` or
  `repos`, and it lasts 90 days. Machine scopes (`nodes.claim`,
  `triggers.claim` and `runs.state`, including `nodes.claim` or
  `triggers.claim`) make a runner token: an editor mints one
  with `name` and `repos`, and it lasts 90 days past its last use. A request
  that mixes families, names an unknown scope or sends none answers `400`.
  `GET /api/v1/team/tokens`, `DELETE /api/v1/team/tokens/{prefix}` and
  `PUT /api/v1/team/tokens/{prefix}/git-credentials` replace the list, revoke
  and git-credential routes for both kinds. Schema v97 makes an agent name
  unique across live runner and machine tokens; it needs no operator action.

To upgrade:

1. Leave existing tokens alone. A `swu_` CLI token or `swr_` runner token
   minted from team settings keeps authenticating as before until it expires
   or is revoked, and `GET /api/v1/team/tokens` lists it with the new ones.
2. Point scripts at the new route and name the scopes. The route still needs
   a signed-in session, as the old ones did. A runner token becomes this body:

   ```json
   {"scopes": ["nodes.claim", "triggers.claim", "runs.state"],
    "name": "build-box", "repos": ["github.com/acme/*"]}
   ```

   A CLI token sends the scopes of the caller's role, for an editor
   `["runs.read", "logs.read", "triggers.read", "runs.write", "runs.control", "approvals.write"]`,
   and no `name` or `repos`.
3. Move list, revoke and git-credential calls to `/api/v1/team/tokens` and
   `/api/v1/team/tokens/{prefix}`. Read each row's `family` to tell a member
   token from a machine token.

**Edge cases:**

- Minting a machine token whose `name` a live runner or machine token already
  holds answers `409`, as it did for runner tokens. Revoke the old token
  first, or pick another name.
- The 10 live member tokens per member and 100 live machine tokens per team
  count the old CLI and runner tokens too.
- Operator tokens (`POST /api/v1/tokens`, `sparkwing cluster tokens`), the
  bootstrap token and GitHub Actions exchange credentials are unchanged. The dashboard's **Team > Machines** page uses the new routes
  and works as before.

## Assisted offers, enrollment and the foreground fleet are removed

The assisted-offer claim protocol had no shipped helper client, so nothing
but tests reached it. Machines that helped a run go back to being ordinary
runners, which claim nodes the way warm trigger runners always have.

- **Before:** an operator enrolled an executor with
  `sparkwing cluster agents enroll` (`PUT /api/v1/agents/{name}`), binding a
  runner token to a name, priority, budget and placement. An enrolled
  executor kept itself live with `POST /api/v1/agents/{name}/heartbeat`,
  previewed work with `POST /api/v1/nodes/claim/prepare` and offered for a
  node by sending `executor_name` and a reservation to
  `POST /api/v1/nodes/claim`; its token could no longer make an ordinary
  claim. `sparkwing fleet init` wrote a `fleet` section into `config.yaml`,
  and `sparkwing run PIPELINE --sw-fleet` ran a foreground coordinator that
  offered nodes to the helpers that section named.
- **After:** those routes answer `404`, and `POST /api/v1/nodes/claim`
  refuses the offer fields as unknown. `sparkwing fleet`, `--sw-fleet` and
  `sparkwing cluster agents enroll` are gone. Any runner or machine token
  with `nodes.claim` claims through `POST /api/v1/nodes/claim`, including one
  that was enrolled. `GET /api/v1/agents` and `sparkwing cluster agents list`
  show the runners that claimed in the last hour with the labels, slot limit
  and headroom they advertised; `active_slots`, `base_priority`,
  `priority_ceiling`, `budget` and the `gateway` type are gone. The store
  has no executor or offer tables, and an older controller refuses it.

To upgrade:

1. Delete the `fleet:` section from `config.yaml`. Until it is gone every
   command that reads the file stops with
   `unknown section "fleet"`.
2. For each machine that was an enrolled helper, run it as an ordinary
   runner: `sparkwing cluster runners add --profile PROFILE --name MACHINE`
   on it mints its runner token and starts the service, or deploy the runner
   bundle in Kubernetes. Give it the capabilities its enrollment listed with
   `--labels`, and its `max_concurrent` with `--max-concurrent`. Revoke the
   token the enrollment named once nothing uses it.
3. Run pipelines without `--sw-fleet` or `--sw-allow-secret-file`; the
   latter only fed the fleet's source snapshot, and
   `sparkwing pipeline trigger --working-tree --allow-secret-file` keeps it. A foreground `sparkwing run` executes
   on the machine you run it on; to spread a run across machines, trigger it
   on a controller with runners attached.
4. Code embedding `pkg/controller/client` drops `PrepareExecutorClaim`,
   `OfferExecutorClaim` and `HeartbeatExecutor`, and calls `ClaimNode` where
   it called `ClaimNodeAs`. Code embedding `pkg/store` renames
   `Tenant.ListLegacyAgentClaims` to `ListAgentClaims`, `LegacyAgentClaim` to
   `AgentClaim`, and drops the executor, offer and scheduling-summary methods.
   Claim-round finalization is removed entirely.

**Edge cases:**

- The enrolled executors' priorities, budgets and trusted locations have no
  runner equivalent. A runner advertises its own labels and capacity, and
  the controller places work by those.
- Runs record no `executor_offer_received`, `executor_offer_expired`,
  `executor_offer_declined`, `executor_offer_awarded` or `executor_selected`
  events from now on; runs already stored keep theirs.

## Runner claims answer with claim tokens

A runner's credential stays in the runner. The process that runs a node's
code, and every Kubernetes Job a runner creates, authenticates with the
node's claim token instead.

- **Before:** a self-hosted runner, a Kubernetes or warm trigger runner and
  `sparkwing cluster worker` handed their bearer to the code they ran. A
  Kubernetes or warm runner passed it to the `handle-trigger` child, and the
  Kubernetes node runner wrote it into every Job's `SPARKWING_AGENT_TOKEN` as
  a literal value, readable by anyone who can `get` Jobs in the namespace.
- **After:** the node claim routes (`POST /api/v1/nodes/claim`,
  `POST /api/v1/runs/{id}/nodes/{nodeID}/claim`) and the trigger claim routes
  (`POST /api/v1/triggers/claim`, `POST /api/v1/triggers/{id}/claim`) add a
  `claim_token` field. A node's claim token acts as the runner that made the
  claim, confined to the claim's run and to writes on its own node, and stops
  when the claim ends. Beyond its run it reaches only what `Ref.Get`,
  `TryGet` and `RunAndAwait` need in its own team: a pipeline's latest run,
  run outputs, starting and polling its own node's child runs, and the
  spawned-child lookup. A trigger's claim token reads the run's declared
  secrets. A Job's `SPARKWING_AGENT_TOKEN` holds its node's claim token, and
  every trigger child reaches the controller through a loopback broker that
  admits only its run's routes. Schema v99 adds the `trigger_claim_tokens`
  table; it needs no operator action.

To upgrade:

1. Upgrade the controller and every runner together: the runner bundle, the
   `sparkwing-runner runner` service, GitHub Actions runners and
   `sparkwing cluster worker`. A runner from this release gets no claim token
   from an older controller, and its nodes fail with `401`.
2. Leave runner tokens as they are. They keep claiming, heartbeating and
   making the runner's own calls; only the work they claim moves to claim
   tokens.

**Edge cases:**

- A runner older than this release keeps working against the upgraded
  controller, ignoring the `claim_token` field and running nodes on its
  bearer.
- A node's claim token answers `403 claim_mismatch` for a route outside its
  run and those cross-pipeline calls, or a write to another node, and
  `403 claim_ended` once its claim has ended. A pipeline that reached
  another run through its runner's bearer from inside a node, beyond those
  calls, no longer can.

## Node secret reads need the claim token

A node reads its run's secrets only through its claim, so every read is one
the run's plan declares and every release is recorded.

- **Before:** `GET /api/v1/secrets/{name}?run=<id>` answered any bearer
  holding `secrets.read` and a live claim on the run with any secret of the
  run's pipeline, with no declaration check and no event. A runner token,
  and scripts given one, read secrets this way.
- **After:** that bearer gets `403 claim_required`. A claim token reads a
  name its run's plan declares and leaves a `secret_released` event; an
  undeclared name answers `403 secret_undeclared`. The operator (`admin`)
  and a team's owner (`team.admin`) still read a row by name; a masked row
  still answers a team owner `403 write_only`. `secrets.read` is no longer a
  machine team token scope: `POST /api/v1/team/tokens` refuses it with
  `400`, and `sparkwing cluster runners add`, the dashboard and the GitHub
  Actions exchange stop granting it. Existing tokens that carry it keep
  authenticating; the scope releases nothing.

To upgrade:

1. Upgrade every runner together with the controller (see
   [Runner claims answer with claim tokens](#runner-claims-answer-with-claim-tokens)).
   A runner older than this release reads no secret from the upgraded
   controller, so its nodes fail at their first secret.
2. Declare every secret a pipeline reads in its `Secrets()` struct. A node
   that calls `sparkwing.Secret(ctx, name)` for a name the struct does not
   declare now gets `secret_undeclared` on a runner, as it already did on a
   controller-dispatched run.
3. Move scripts that read secrets with a runner or machine token to an
   operator token (`sparkwing cluster tokens create`) or a team owner's
   credential.
4. Drop `secrets.read` from scripts that mint machine tokens through
   `POST /api/v1/team/tokens`, and from code that calls
   `store.Store.ClaimedRunsFor`, which is removed; `ClaimedRunFor` stays.

**Edge cases:**

- A local run whose profile reads secrets from a controller does so with
  the profile's token, which reads by name only when it is an operator or
  team-admin credential, as before.
- A node that runs inside its trigger's process, under the in-process node
  runner, reads through the trigger's claim token; its release event names
  the run and no node.

## The cache's static operator token is removed

Remove `SPARKWING_CACHE_TOKEN`, the `cache-token` credential and
`cache.tokenSecret` from older installations. The separate cache service,
its grant-key replacement and the controller's cache proxy are now removed.
Do not provision a replacement cache token or grant key. Remove the cache
deployment and use the controller's authenticated data routes for byte storage.

## Cache-service accounting is removed

The cache service and its accounting client have been deleted. Remove the
cache deployment and its `controller-token` credential. The controller no longer
registers the cache's storage-reserve, commit, release, download-charge or
egress-total routes. The `storage.count` service scope has been removed.
Storage and download accounting stays in the controller's database.

## The source-token route is removed

- **Before:** `POST /api/v1/runs/{id}/source-token` minted a read-only GitHub
  App token for a runner holding a claim on the run, and
  `GET /api/v1/capabilities` advertised it as `github_app.source_tokens`. A
  runner asked it when the git-credential route answered a plain 404, as a
  controller older than v0.66 does.
- **After:** the route answers 404 and the capabilities answer has no
  `source_tokens` field. A runner asks only the controller's source credential
  route and reports an error when the controller lacks it. A runner or
  controller older than v0.66 fetches no source from a controller of this
  release, and a runner of this release fetches no source from a controller
  older than v0.66.

To upgrade:

1. Upgrade every runner and the controller to this release together. The
   release ships after 2026-10-15, two weeks after v0.66, so a fleet that
   kept current already runs v0.66 or later.
2. Drop `github_app.source_tokens` from code that reads
   `GET /api/v1/capabilities`; `github_app` still names the App's `slug`.

## Source credentials go to claim tokens only

- **Before:** a runner without the git cache asked
  `POST /api/v1/runs/{id}/git-credential` with its own token while it held a
  claim on the run. The controller minted a GitHub App token for whatever
  github.com repository the run named, if an installation of the team covered
  it, kept it for reuse, and otherwise released the team's stored git
  credential for the host. The answer carried `repository` and
  `extra_repositories`.
- **After:** `POST /api/v1/runs/{id}/source-credential` is the only route,
  and it answers only a claim token of the run: a node claim's or trigger
  claim's, which every runner claim returns, or a Cloud Job's. A bearer gets
  `403 claim_token_required` and `git-credential` answers 404. The App token
  reads the repository the App's webhook recorded on the run's trigger, or a
  repository the run names by hand only when one of the team's App trigger
  subscriptions names it. The team's stored host credential is unchanged in
  who receives it. A node's claim gets at most three credentials and none
  once its attempt starts; every App token issued appears in
  `GET /api/v1/team/git-credentials/releases`. The answer names the run's
  repository and the owner's extra repositories in `repositories`, the run's
  first.

To upgrade:

1. Upgrade every runner together with the controller. A runner older than
   this release asks the removed route and fails to fetch source straight
   from the controller; a runner using the operator's git cache is
   unaffected.
2. For a repository your team starts runs of by hand (`sparkwing run` against
   the controller, the API or the dashboard) rather than through the GitHub
   App, subscribe it under **Team > GitHub**, or store a git credential for
   github.com under **Team > Git credentials**. Until then those runs fail at
   the fetch with `no_source_credential` and a message naming the
   subscription. A public repository needs the same, since a runner presents
   no credential of its own.
3. If an owner listed extra repositories before their GitHub IDs were
   recorded, save the list again; such a list now answers 409 rather than
   minting by name.
4. Code that calls `store.Tenant.SpendSourceCredential` or
   `ReleaseGitCredential` passes the claim token and a
   `store.GitCredentialRelease`; code that decoded
   `controller.GitCredentialResponse` or `controller.SourceTokenResponse`
   decodes `store.SourceCredential`.

## S3 and Postgres state profiles are removed

`sparkwing run` executes on this machine and records to this machine's
store. It no longer takes `--profile`, and a profile's `state:` is
`sqlite` or `controller`. Shared run history lives on a controller:
`sparkwing pipeline trigger <pipeline> --profile P` runs a pipeline there.
This ends the temporary exception
[v0.67.0](v0.67.0.md#sparkwing-run-takes---profile-only-for-s3-or-postgres-state)
kept for S3 and Postgres state profiles.

| Before | After |
|---|---|
| `sparkwing run build --profile shared` with an S3 or Postgres `state:` | `sparkwing pipeline trigger build --profile prod` to run on a controller, or `sparkwing run build` to run here |
| `state: {type: s3, ...}` or `state: {type: postgres, ...}` in a machine profile (`~/.config/sparkwing/config.yaml`) | Delete the `state:` key, or the profile, or replace it with a controller profile (`sparkwing cloud connect`). The config fails to load with `state.type: s3 is removed` (or `postgres`) and a pointer to this section. |
| The same in a project profile (`.sparkwing/sparkwing.yaml`) | A project profile declares all four surfaces, so deleting `state:` alone fails with `state surface is required`. Remove the profile, or set `state: {type: sqlite}` for this machine or `state: {type: controller}` for the profile's controller. Both errors name these choices. |
| `url_source: env:VAR` on any surface | Delete it; it named the Postgres state DSN. The config fails to load with `field url_source not found`; a Postgres `state:` that carries it fails on that key before its type is checked. |
| `mirror_local:` on a profile | Delete it. The config fails to load with `field mirror_local not found`. |
| `cache: {binaries: {...}}` | Delete the `binaries:` block. The config fails to load with `field binaries not found`. Runs fetch prebuilt binaries from the git cache `SPARKWING_GITCACHE_URL` names. |
| `sparkwing run X --sw-artifact-digest-backfill` | Delete the flag; it applied only to binaries fetched from a profile's cache. `run` refuses it as an unknown runner flag. |
| `sparkwing serve start --profile P [--no-local-store]` | `sparkwing serve start` for this machine's runs, or the controller's dashboard for shared ones. |
| `guards: {require: [profile:name=prod]}` | `profile:local`, `profile:controller` or an `arg:` token. A run never has a named profile, so the token never matched; a config naming it now fails to load. |
| `sparkwing runs list --profile shared` against an S3 or Postgres state profile | Nothing reads that history any more. The bucket or database is yours to keep or delete. |

- **Refusal:** `sparkwing run deploy --profile prod` fails before it
  compiles anything: `run: --profile is removed; sparkwing run executes on
  this machine against its own store, so run on the profile's controller
  with sparkwing pipeline trigger deploy --profile prod`. `--profile`
  before the verb fails the same way.
- **Hooks:** a hook installed before v0.67.0 with `--profile 'P'` on its
  `sparkwing run` lines fails until you reinstall it with `sparkwing
  pipeline hooks install`.
- **Mixed versions:** a pipeline binary built against this SDK refuses a
  run that an older CLI started with `--profile`, naming `sparkwing
  pipeline trigger`, rather than recording it locally. This CLI still
  drops an inherited `SPARKWING_PROFILE` and `SPARKWING_SECRETS_PROFILE`,
  so a pipeline binary from an older SDK cannot be pointed at a remote
  store either.
- **Controllers:** a controller on PostgreSQL is unchanged; only a laptop
  or CI run opening a shared database or bucket itself is removed. The
  `install/terraform/mode3-postgres` module now documents the controller's
  database alone.
- **Output:** a run no longer writes `runs/<id>/state.ndjson` into this
  machine's cache directory. `run_start` reports no `backends` or
  `profile` block.
  `sparkwing configure profiles show -o json` drops `mirror_local`. The
  controller's health no longer reports `object_store.stalled`.
- **Go packages:** `pkg/storage/s3state` is removed. `pkg/controller`
  drops `Loopback`, `LoopbackState`, `LoopbackConcurrency` and
  `NewLoopback`; a local run's node processes reach the full controller.
  `pkg/backends` drops `TypePostgres`, `Spec.Binaries` and
  `Surfaces.BinaryCache` and `Spec.URLSource` and adds
  `MigrationGuideURL`. `stdoutlogs.CheckSpec` drops its `urlSource`
  parameter.
  `storeurl.OpenStateStoreFromSpec` opens `sqlite` and `controller` only.
  `pkg/localws` drops `Options.NoLocalStore`. `pkg/pipelines` drops
  `KindProfileName` and `GuardContext.ProfileName`.

## Pipelines run on the engine host

The engine now hosts the run. `sparkwing run` and a launcher Job start the
pipeline binary as `<binary> --sw-node-protocol`, plan in one process,
and run each node in a process of its own, as the
[node protocol](../node-protocol.md) describes. A `.sparkwing` module built
against an older SDK does not answer that protocol, so it no longer runs.
Nothing converts an old pin.

- **Steps:** move the pin to this release's SDK with
  `sparkwing repos update --in-place`, or `go get` the SDK version in
  `.sparkwing/go.mod`, then run the pipeline once. A module that replaces
  the SDK with a local checkout needs that checkout at this release.
- **Refusal:** an old binary fails before the run starts with `pipeline
  entry exited without answering describe; rebuild .sparkwing against a
  matching SDK`. A binary that speaks another protocol version names both
  versions and its SDK.
- **Plan code must be deterministic.** Every node process plans the run
  again, and a node whose plan entry differs from the one the run accepted
  fails with `plan_drift`. Read time, randomness and the environment in
  step bodies, not in `Plan`.
- **Run options are not environment variables.** `--sw-start-at`,
  `--sw-stop-at`, `--sw-only`, `--sw-no-cache` and the other run flags no
  longer reach pipeline code as `SPARKWING_*` variables. A dry run still
  sets `SPARKWING_DRY_RUN`. A node sees every other `SPARKWING_*` variable
  you set; the engine strips its own credentials and plumbing, such as
  `SPARKWING_SECRETS_KEY`, `SPARKWING_AGENT_TOKEN` and
  `SPARKWING_NODE_TOKEN`.
- **Limits:** a step's output is at most 16 MiB as JSON, a cleanup runs
  for at most 2 minutes, the SDK retries a `429` from a node route for at
  most 2 minutes, and `after_run` after a timeout runs for at most 2
  minutes. See [node protocol limits](../node-protocol.md#limits).
- **Inspection:** `sparkwing pipeline plan` evaluates no `skip_if`; a
  step with one reports `may_skip` (`user_skipif`) instead of
  `would_skip`, and plan no longer reports lint warnings or
  `--sw-dry-run` decisions. An unknown pipeline flag
  fails as invalid arguments instead of being dropped.
- **Plan document:** dependency caches are listed under `caches` instead
  of `dir_caches`, including in `sparkwing pipeline plan --static -o json`.

## The pipeline binary answers only the node protocol

The compiled pipeline binary no longer runs, plans or inspects a pipeline by
itself; it answers `--sw-node-protocol` and exits 2 on anything else. A
script that called the binary directly moves to the CLI:

| Before | After |
|---|---|
| `<binary> <pipeline> [flags]` | `sparkwing run <pipeline> [flags]` |
| `<binary> <pipeline> --plan`, `sparkwing run <pipeline> --plan` | `sparkwing pipeline plan --name <pipeline>` |
| `<binary> <pipeline> --explain`, `sparkwing run <pipeline> --explain` | `sparkwing pipeline plan --static --name <pipeline>` |
| `<binary> <pipeline> config`, `sparkwing run <pipeline> config` | `sparkwing pipeline describe --name <pipeline> --secrets` |
| `<binary> ops queue`, `ops doctor`, `ops version` | `sparkwing queue`, `sparkwing doctor`, `sparkwing version` |
| `<binary> ops stats`, `ops stats-reset` | none |
| `<binary> replay-node`, `sparkwing debug replay` | none; a hosted run records no dispatch snapshot |
| `<binary> handle-trigger`, `run-node`, `sparkwing handle-trigger` | none; the CLI and `sparkwing-runner` host runs |

`pipeline describe --secrets -o json` prints `{"pipeline": ..., "secrets":
[{"name": ..., "required": true}]}` (or `"optional": true`), read from
describe, instead of the per-field Go names and resolution notes the binary
printed.

## Cache grants are removed

The `swcg` protocol, `cache-grant-key` controller credential and
`POST /api/v1/runs/{id}/cache-grant` route are gone. Data upload, commit,
download and capability requests now use live controller capabilities.
Replace grant authentication and `{kind,key}` downloads with the requests in
[Data storage](../data-uploads.md) and [Data downloads](../api.md#data-downloads).

Byte-storage consumers use the `blob.Store`, `DataSigner` and `RecordStore`
contracts. Controller embedders configure their authenticated adapters with
`WithDataController`. Services advertise data transfers only when those adapters
are configured. There is no compatibility endpoint or cache-service fallback.
Existing output and job-log handlers keep their separate transfer setup until
their own cutover.

## Raw artifact plug-ins are removed

`storage.ArtifactStore`, `storage.ErrListNotSupported` and
`conformance.TestArtifactStore` are removed. The unused raw
`s3.ArtifactStore.Delete` method is also removed. Use the common signed-data
backend and authorized records. A digest or a raw object existence check does
not establish read access or an upload receipt.

Deployment usage reporting remains separate through `storage.UsageReporter`.
Its filesystem constructor is `fs.NewUsageReporter`.

## Storage uses record-backed maintenance

`Store.PrunableNodeLogs` and `Store.DeleteRunNodeLogs` are removed. Run and team
deletion use the controller APIs and the shared backend. Retention uses the
store's leased maintenance pass.

`Server.WithDataObjects` also installs the backend's verified manifest reader
for result attachment. Configure it before serving or sharing the store.

Remove calls to `WithStoragePass` and `RunStoragePass`, references to
`StoragePassEvery`, and readers of the `storage_pass` health field. No forwarding
API remains. Controller embedders supply the physical backend with
`WithDataObjects`. Leased retained-data maintenance marks unreachable blobs,
collects eligible tombstones and expires filesystem logs. Bucket listings no
longer replace tenant usage counts. Log lifetime follows retention classes.

## Storage standing uses retained inventory

Use `Store.ForTeam(ctx, team)` followed by `Tenant.StorageStanding(ctx)` for
the team's storage report. The snapshot returns retained event, blob and log
bytes plus live blob-upload reservation bytes. Historical usage counters remain
financial records and do not determine retained storage. Deleting a run releases
its retained event bytes without a repair call.

`Store.StorageStandingFor`, `Store.TeamStorage`, `TeamStorageUsage` and
`Store.ReconcileFreeEventBytes` are removed. The pre-launch schema also removes
the duplicate `free_slots.event_bytes` column. Rebuild the disposable store.

`Store.ReleaseExpiredStorage` is removed. Final upload reservations expire
through quota admission and leased blob maintenance. An expired upload ticket
cannot commit, including a zero-byte ticket.

## Credit persistence uses explicit handles

Deployment billing settings now use `store.AsOperator()`. This includes
`CreditSettings`, `SetCreditSettings`, `SetOperatorCreditSettings`,
`CreditRateTable` and `WarmCPUClassCores`. Their raw Store methods are removed.
The settings, validation and update rules are unchanged.

`Store.GrantCredits`, `Store.ListCreditCharges` and `Store.AppendEventOnce` are
removed. Select the intended team with `ForTeam` and call its Tenant method.
Local callers that mean the default team must select `store.DefaultTeam`.

## Console authorization

Account deletion, team deletion, GitHub App installation unbinding, orphan
reconciliation and credit-settings writes move under `/api/v1/operator`.
The former deployment-admin paths are removed. These actions require an
operator-console session and CSRF proof. Bearer tokens cannot perform them.
Console errors use the shared `error` object with `code`, `message`, and optional
`details`. Account-deletion conflicts place affected teams in `error.details.teams`.

Controller embedders must configure `Server.WithAuthorization` with the shared
verifier, binding checker, verified transport identity and session-bound CSRF
adapter before constructing the handler. Missing dependencies refuse requests.
The operator account list alone does not configure API authentication.

## Deployment authorization

Use `/api/v1/operator/limits` and `/api/v1/operator/signups` with a deployment
credential carrying `operator.limits`. Object-store administration uses
`/api/v1/operator/object-store` and its `/breaker`, `/reset-breaker`, `/settings`,
`/quotas/{team}` and `/quotas/{team}/allowance` routes, with
`operator.object_store`. An operator-console session may use both groups;
its mutations require CSRF proof. Quota selectors name the team.

`GET /api/v1/storage` returns team data. Deployment totals are on
`GET /api/v1/operator/object-store`. The former compute-limits, signups,
object-store and storage-settings paths are removed, along with
`GET /api/v1/admin/usage-metrics` and the repo-dispatch mutation route.

## Credential revocation

Use `DELETE /api/v1/tokens/{token}` for a team capability. The old
`DELETE /api/v1/team/tokens/{prefix}` path is removed. Deployment token
revocation moves to `DELETE /api/v1/operator/tokens/{token}` and requires
`operator.tokens` or an operator console session. The Go admin client's
`RevokeToken` uses the deployment path.

Member updates use `/api/v1/team/members/{account}`. PATCH accepts a JSON object
with `role` set to `reader`, `editor` or `owner`. DELETE removes the membership.
Browser mutations require CSRF proof. Last-owner refusals use the shared
`conflict` error with HTTP 409. Dead credentials use `unauthenticated` with
HTTP 401. The old token-state response fields and one-hour retry hint do not
apply to these routes.

### Member credential issuance

Replace member requests to `/api/v1/team/tokens` and the former global token
mint with `POST /api/v1/tokens`. Send `grants` and optional RFC3339
`expires_at`. Read `capability`, `prefix`, `team`, `grants` and optional
`expires_at`. The request no longer accepts principal, kind, scopes, TTL or
metering. A session provisions its own member root. A member bearer narrows
its authority through delegation. Save the returned capability privately and
do not automatically retry issuance.

## Root provisioning

Machine enrollment uses `POST /api/v1/runners` with `name`, `repos`, optional
`reserved_pools`, optional narrow `grants` and optional absolute `expires_at`.
It requires `team.admin`. `POST /api/v1/team/tokens` is removed. Deployment
service issuance uses `POST /api/v1/operator/tokens` with `name`, operational
`grants` and optional `expires_at`, under `operator.tokens` or console authority.
Both return `capability`, `prefix`, `team`, `grants` and optional RFC3339 expiry.
They return the bearer once, use no-store and require session CSRF for cookie
authentication. Do not automatically replay issuance.

Go callers use `EnrollRunner` and `CreateServiceToken`. Member `CreateToken`
accepts `Grants` and `ExpiresAt`; the old kind/principal/scopes/TTL request and
Token/Metadata response fields are removed. Controller integrations retain the
existing `WithCredentialIssuer` and C1 `ProvisionPolicy` transaction boundary.
Machine and service enrollment use persisted root registrations by default.
The store checks current authority, canonical registration selectors, repository
patterns, pools, active names and the existing machine limit before commit.
An active name returns `conflict`. The runner limit returns `quota_exceeded`.
Bootstrap and Actions provisioning still require their trusted origin verifier.
Console-origin persistence remains a separate integration.

## Launcher claims use the shared unit queue

Replace `Store.ClaimLaunch` with `Store.ClaimUnit`. A named plan selects
`UnitRef{Kind: capability.UnitPlan, RunID: runID}` without a job ID. A named
job selects `capability.UnitNode` and its job ID. Queue requests supply the
requested kinds. A deployment service capability carrying `claims.launch`
provides the launcher identity. Capacity and labels remain observations.

`Client.ClaimLaunch`, `LaunchClaim`, `LaunchClaimRequest`, `MinLaunchLifetime`
and `LaunchDeadlineSlack` are removed. HTTP consumers call `Claim` on the controller returned by
`client.NewEngineClient` with `EngineOptions.Launcher`. Awards carry the persisted expiry and bootstrap
presentation. Exchange the bootstrap before using its execution authority.
The shared queue passes over unaffordable work without an in-process cursor.

`Tenant.SetRepoDispatch` is removed. Dispatch belongs to the admitted request.
The unused `Store.SeedToken`, `ErrSeededTokenRevoked`,
`Store.CheckClaimSensitive`, `Store.PrincipalHoldsProfileClaim`,
`Tenant.SpendSourceCredential`, `Tenant.SourceClaimant` and
`Tenant.GitCredentialMachines` APIs are removed too. Use the capability issuer,
current binding checks and `Tenant.ReleaseSourceCredential` for their retained
responsibilities.

## Manual retries use sealed intake

Use `Tenant.AdmitRun` with `Request.Lineage.RetryOf` and current retry authority.
The transaction retains the original sealed request and records the retrier in
lineage. The raw `Store.CreateRetryWithRun`, `Tenant.SetRetriedAs` and
`InheritSecretArgs` APIs are removed. Hosts use the retained source and secret
classification from the seal.

`Tenant.ReadNode`, `ReadSnapshot` and reconciliation expose `NodeRow.OutputRecord`
and `NodeRow.ManifestRecord` from the committed terminal result. Use these
selectors for materialized jobs, which may have no execution attempt. Authorize
record reads through the data contract before reading bytes.
