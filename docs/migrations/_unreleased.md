# Migrating to the next release

## Data signing and authentication metrics

Embedded controllers use `blob.NewFilesystem` or `blob.NewS3` as their physical
backend and pass it through `WithDataController` or `WithDataObjects`. The duplicate
controller filesystem and S3 signer types and `WithSignedDownloads` are removed.
Wrap an S3 backend with `controller.NewCloudFrontDataBackend` to sign public-ingress
downloads through CloudFront. Direct requests retain regional S3 URLs. Uploads keep
the backend's size and checksum constraints. Retained log readers use that same
backend. `WithS3Logs` and the raw S3 log object adapter are removed. Replace
`s3.NewArtifactStore` with `s3.NewMeasurementStore` for bucket measurements.
Measurement handles expose `Usage` only. Local dashboard logs require committed
tagged segments because run-directory log fallback is removed.

Replace the `user` and `runner` credential labels on
`sparkwing_requests_by_principal_total` with `account` and `machine`. The other
labels are `service`, `operator`, `local`, `anonymous` and `other`. Requests count
once authentication succeeds, including later authorization refusals. Remove
queries for `sparkwing_auth_token_cache_total`, because the token-table cache is
removed.

## Runner observation snapshots

Embedded controllers supply `Server.WithRunnerInventory` with a tenant-scoped
`RunnerInventoryReader`. Runner lists and queue headroom use the same committed
snapshot. The old process-local presence, heartbeat and headroom registries and
holder-name parser are removed. A missing snapshot adapter returns `policy_missing`.
The `/api/v1/runners` response retains `agents`. Equal display names can appear in
separate rows because they do not merge credentials. Last-seen and headroom
observation times remain separate. Runner metrics read the same snapshots.

`RunnerInventoryReader.ReadRunners` returns `capability.RunnerSnapshot`.
The snapshot, observation and headroom types live in `internal/capability`,
so the store adapter can implement the reader without a controller import.

## Reporting credential routes

Reporting reads use the shared capability policy and the authorized resource's team. This includes run lists, receipts, attempts, steps, events, approvals, debug pauses, pipeline lists and latest runs, trends, runner views, sizing and logs. Unbound machine roots and deployment credentials cannot substitute for team membership. Work and trigger claims receive only the read alternatives declared for their bindings.

Job logs use `/api/v1/runs/{run}/jobs/{job}/logs`, with `/stream` and `/completeness` beneath it. The old `/runs/{run}/logs/{job}` paths are removed. Use `/api/v1/runners` instead of `/api/v1/agents`, `/api/v1/approvals` instead of `/api/v1/approvals/pending`, and `/api/v1/sizing` or `/api/v1/sizing/explain` instead of `/api/v1/capacity/profiles`. Read debug pauses at `/runs/{run}/debug-pauses`. The `paused` alias is removed.

## Legacy execution credential routes

The `/api/v1/triggers` and `/api/v1/runs/{run}/nodes` route families are
removed, including heartbeat, execution-start, execution-finish, claim input,
attempt results, node logs and direct node output upload. Raw
`POST /api/v1/runs/{run}/events` is also removed. Events come from the
committing state operation.

Use capability-backed `POST /api/v1/runs` for intake, `/runs/{run}/plan`
for plan acceptance, `/runs/{run}/jobs/{job}/transition` for job changes,
and the run or job `heartbeat` route for renewal. Use `/api/v1/data` for
authorized output uploads and records. Retired claim tokens and execution
headers are no longer an execution credential. Rebuild runner and controller
consumers together. No alias accepts the previous request shapes.

Embedded controllers remove `Server.WithLocalExecution`. Local execution now
uses the same capability and self-claim rules. The elected local runtime obtains
verified peer identity through `Server.HTTP()`.

Absent paths return canonical `not_found` before credential lookup. A
removed method on a path that still has another method returns canonical
`method_not_allowed` with `Allow`. Neither refusal renews a browser session.
The unused `WriteUnsupportedRoute` helper and `UnsupportedRouteError` constant
are removed. Embedded callers use the shared handler and canonical error envelope.

## Browser identity linking

Link start and completion use the shared browser proof policy. Use the account's
direct provider session and the matching session-bound CSRF form. Account-token
sessions and Authorization headers cannot link identities. Failures return the
canonical JSON error envelope instead of a settings-page refusal redirect.
Embedded controllers supply `WithIdentityLinkWriter` to recheck the session and
commit one-use nonce consumption and identity attachment.

Nonlocal dashboard pages always require a live session. `Dashboard.Local`
selects local pages. Elected local controllers obtain the API and dashboard
handlers from `Server.HTTP()` and supply them through `wingd.LocalHTTP`.

## CLI client retirement

The legacy `Client`, `New` and `NewWithToken` are removed. Construct the shared
clients with `EngineOptions`. Its `HTTP` field supports the private api.sock
transport, and `Bearer` supplies the credential without exposing it in a URL.

| Operation | Shared client |
| --- | --- |
| Intake, claim, renewal, plan acceptance, transitions, step reports and completion | `NewEngineClient` |
| Public run history, steps and receipts | `NewRunReader` |
| Persisted logs and lifecycle events | `NewLogReader` |
| Job location and active pause | `NewDebugReader` |
| Approvals and annotations | `NewRunReview` |
| Secret values, metadata, writes, deletion and rotation | `NewSecretManager` |
| Authorized object transfers and records | `NewDataClient` |
| Run deletion with a persistence receipt | `NewRunPruner` |

Intake uses the sealed `CreateRunRequest`. Hosts accept plans and expansions
atomically and report fenced job transitions and typed step observations. The old
trigger requests, node setters, claim-token headers, direct output uploads,
concurrency-slot client and log-segment uploader are removed. There is no raw
execution reader or capability probe that falls back to those APIs. The shared
transport decodes the common error envelope and follows the route table's replay
policy. Controller redirects are refused.

`repos info` reads each pipeline's latest admitted run through the local
controller's public projection. It follows pagination and reports read failures.
A queued run has no start time until execution starts. Follow persisted logs with
the returned cursor until the page is complete.

The old orchestrator readers, SQLite/profile factories, `HostTriggerRun`,
`TriggerHost`, `RunNodeOnce`, `RunHosted`, `HostClaimedTrigger`,
`RunLocalTriggerConsumer` and `ServeConsumer` are removed. Their state, logging,
admission, submission-environment and child-dispatch backends and the old
`pkg/localws` server are also removed. Captured environments use the shared host
snapshot contract. W3's retained `StateStoreOld` scheduler still needs retirement.

Local lifecycle uses `wingd.LocalRuntime`. Foreground construction receives the
parsed `--home` before election. Lifecycle, managed units and logs share that
home. The elected controller factory, detached startup and native execution
composition remain unfinished. Typed step reports also require the controller's
matching request decoder and persistence acknowledgement.

## Sealed step selections

`--sw-start-at`, `--sw-stop-at` and `--sw-only` enter the request seal as
`options.start_at`, `options.stop_at` and `options.only`. Each carries presence,
value and origin. Plan acceptance rejects a changed selection, and retry keeps
the original values. Hosts must read these fields from the seal. The native
host integration remains unfinished.

## Internal launcher arguments

Launcher invocations require `--docker-image` with a digest-pinned DinD image
beside the runner `--image`. Remove `--deadline`. The Job deadline derives from
the awarded execution inputs. Plan awards omit `--job` in `fetch-source` and
`run-node`; work and eval retain the job from their private award metadata.

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

Use `sparkwing cache warm` from the checkout to prepare the local cache.
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
`Append` and `DeleteRun` methods are removed. Controller readers use
`client.NewLogReader`; the old `client.NodeLogs` adapter is removed. Writers publish tagged segments with `logseg.Sink`.
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

Embedded controllers supply `WithCredentialRotator` and `WithSourceAuditReader`.
Missing adapters return `policy_missing`. The old global rotation and legacy
audit reader cannot provide these tenant and authority guarantees.

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
Launcher sync carries each immutable award fence as `{kind,id,holder,generation}`,
the Kubernetes Job as `{namespace,name,uid}`, and `node_uid`. It returns awards
with empty capabilities and full Job references to remove. Name-only cleanup
is removed. Embedded controllers must
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
  Read tagged log pages through `client.NewLogReader` from
  `pkg/controller/client`. Each page reports whether the run and its logs
  are complete.
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
- **Go:** select the registered team with `Store.ForTeam`. Use
  `Tenant.ReleaseCreditFreezes` to release a named hold. A release requires the
  actor, reason and one hold ID. An empty ID is refused. Provider disputes and
  refunds use verified payment-event settlement. Raw callback APIs and
  `Store.PaidGrantTeam` are removed.
  A caller of the removed `Store.DisputeTeam` reads the team from its hold
  or from `Tenant.CreditFreeze`.

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
  `storeurl.OpenStateStoreFromSpec` is removed.
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
`CreditSettings`, `SetCreditSettings`,
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

Go callers use `client.NewRunnerTokenManager(...).EnrollRunner` for enrollment
and `client.NewTokenManager(..., true).CreateToken` with kind `service` for
deployment issuance. The legacy `Client.EnrollRunner` and `Client.CreateServiceToken`
methods are removed. `runners add --reserved-for POOL` forwards the pool proposal
to the team-admin enrollment route. Repeated and comma-separated values are
preserved. Enrollment sends one request and never replays a lost bearer response.
The legacy `Client.CreateToken`, `ListTokens`, `LookupToken`, `RevokeToken`,
`RotateToken` and `ListSecrets` methods are removed. Use `NewTokenManager` and
`NewSecretManager` for typed management operations. The legacy enqueue API is removed. Submit a sealed `CreateRunRequest` through
`Controller.CreateRun`; the request retains its captured environment digest and
resolved intake options. Controller integrations retain the
existing `WithCredentialIssuer` and C1 `ProvisionPolicy` transaction boundary.
Missing origin policy refuses with `unavailable`. Production enrollment and
console-origin persistence must be provided before enabling those flows.

## Account discovery authorization

The three account discovery reads under `/api/v1/me` require a live account
session and use the common error object. They no longer enter the legacy bearer
or renewing session middleware. GET requests do not switch the session's team.
Use the active-team action when changing membership. Cookie and Session-header
presentations use the same account policy.

Credit-settings reads use the member `runs.read` policy. Configure shared
route authorization before constructing an embedded controller's handler.

## Account deletion is console-only

`DELETE /api/v1/me` is removed with its `confirm_email` request and recent-sign-in
check. Use `DELETE /api/v1/operator/accounts/{account}` from an operator-console
session with CSRF proof. A member session or deployment bearer cannot delete an
account. The existing last-owner and financial refusals still apply.

## Account mutations use session policy

Active-team selection, team creation, invitation acceptance and identity
unlinking use the shared account-session policy. Cookie and Session-header
mutations require session-bound CSRF and same-origin proof. These calls no
longer renew the session. Bearer and local-peer authority cannot use them.
Errors use the common object, and closed invitations return `conflict`.

Embedded controllers supply `WithAccountWriter` with authority-bearing account
transactions. Each operation rechecks the selected session at commit. Team
changes commit account and session selection together. Unlinking retains the
recent-sign-in and last-method checks. Missing transactions return
`policy_missing`; raw account-store methods are not fallback adapters.

## OIDC claim authorization

OIDC minting uses `POST /api/v1/runs/{run}/oidc-token` with a live run-bound
`oidc.mint` capability. Passing the runner’s machine root or an account token
no longer works. Hosts use the awarded job capability. The audience request
and signed-token response retain their shapes. Errors use the common envelope.
Embedded controllers supply `WithOIDCReader` for authority-rechecked sealed
identity and an effective ancestry expiry. The legacy claimed-run lookup and
dispatched-node refusal are removed.

## Concurrency credential routes

Replace `/api/v1/concurrency/{key}/state` with
`/api/v1/queue/groups/{group}`. Move `acquire`, `heartbeat`, `release`,
`cancel-waiter`, `holder`, `resolve` and `force-release` under the same group
path. Replace `node_id`, `leader_node_id` and `origin_node_id` with their
`job_id`, `leader_job_id` and `origin_job_id` spellings. Group waiter positions
are one-based. `cancel_timeout_ns` is a numeric duration in nanoseconds.

Executable callers need a capability bound to the declared group and current
work generation. Root runner tokens cannot acquire slots directly. Force
release uses member `runs.control`. Embedded controllers supply
`WithConcurrencyStore` for authority-bearing transactions. The old unfenced
concurrency methods do not provide that adapter.

## Typed step reports

Send `StepReportRequest` to the job's `steps/start`, `steps/finish` or
`steps/skip` route. Include `step_id`, `state` and the positive `source_attempt`
captured when execution began. State `running` selects start, `skipped` selects
skip, and `passed`, `failed` or `cancelled` selects finish. The old step-finish
`status` field and structured `error` object are removed. `error` is masked
text. Optional `started_at` and `finished_at` preserve nanoseconds.

A successful write returns HTTP 200 with `{"recorded":true}`. Wait for that
acknowledgement before emitting the step diagnostic. A source ordinal that no
longer matches returns `lock_held`. Do not read the latest attempt to replace a
captured ordinal when retrying the report.


## Verified billing events and deployment bootstrap

Automated billing callbacks use `POST /webhooks/stripe`. The signature verifier
accepts checkout outcomes, `setup_intent.succeeded`, payment-intent success and
failure, individual refund creation/update/failure, early fraud warning
creation/update, and dispute creation/closure. It passes provider identities,
minor-unit amounts and statuses to settlement. Provider metadata never selects
a team, credit grant or card attempt. Card details are read from Stripe with the
configured key and checked against the signed object. Test and live use the same
path with different keys.

The store must connect non-checkout outcomes to its committed billing records.
That settlement integration is still incomplete. The removed service routes
have no forwarding aliases. Keep provider retries enabled until settlement can
commit their outcomes.

Replace `swu_` bootstrap credentials with a `swk_` controller capability
presentation. `EnsureBootstrapAdminToken` now takes a context, transactional
issuer and raw presentation. It requires `BootstrapAdminIssuer` and forwards a
trusted `ProvisionSpec.Seed` to its once-only transaction. It no longer calls
`CreateTokenIfNoneExist` or accepts a caller clock. The transaction must use
`Store.Issue`, keep only the digest, close the gate after the first admin
credential and preserve revocation across restarts. Until that store adapter
is installed, a configured bootstrap seed refuses startup. Bootstrap authority
is operational only. Sign in with an account capability or OAuth for dashboard
access.


`EnableAuthFromStore` always requires credentials. It no longer disables
authentication when the legacy token table is empty or unreadable. The
single-team anonymous bootstrap mode is removed. Local access uses the
controller's verified peer path. `--require-auth` retains a startup-readiness
check for a live deployment administrator capability. The store must implement
`HasDeploymentAdministrator(context.Context) (bool, error)` for that check.
A missing reader refuses startup when the flag is set. It never falls back to
`ListTokens`. Health reports authentication enabled before bootstrap completes.


## Schedule and review credential routes

Move `/api/v1/crons` and its descendants to `/api/v1/schedules`. Replace
`POST /crons/{id}/disarm` with `DELETE /schedules/{schedule}`. Repository
replacement and deletion remain `PUT` and `DELETE` beneath `/schedules/repos`.
The retired routes have no aliases. Use member `runs.read` for schedule reads
and `runs.control` for mutations. Deployment admin tokens and unbound runner
roots do not substitute for member authority.

Send one `Idempotency-Key` header when running a schedule, resolving an approval
or creating a debug pause. Schedule run-now returns its `replayed` result.
Approval resolution accepts only `approved` or `denied`, with optional `comment`;
the server derives the approver. Its response is an `approval` envelope with
`replayed`. Approval objects use `job_id`. Replace debug-pause `node_id` with
`job_id` and omit lifecycle/release fields. The raw approval-request route is
removed; execution owns gate creation.

Embedded controllers must supply authority-bearing `ScheduleWriter` and
`RunControlWriter` adapters. Legacy schedule, approval and debug-pause writes
are not substitutes. The default refuses a missing adapter. Team invitation,
trust, GitHub App, billing and storage reads now require the shared capability
policy; API metrics requires `operator.limits` or console authority.

## Remaining team credential routes

Team rename/deletion, invitation writes, build trust, run deletion, GitHub App
configuration and billing writes use the shared capability policy. The legacy
token-table verifier, positive and negative caches, here/Actions route fences,
legacy tenant-route table and browser API middleware are removed. The Go
`Authenticator`, `NewAuthenticator`, `WithAuthenticator`, `EnableAuthFromStore`,
`AuthEnabled` and controller `ScopesForRole` APIs are removed. Constructing a
controller installs its policy boundary. Use `WithAuthorization` for an
embedded authority reader and binding checker. Use `store.ScopesForRole` for
role projection.

Embedded transaction adapters use `WithTeamWriter`, `WithGitHubAppWriter` and
`WithBillingWriter`. Missing adapters return `policy_missing`. A write must
recheck current authority and membership inside its committing transaction.
No former raw write is a fallback.

Invitation creation and billing checkout, card setup and pay-now require an
`Idempotency-Key` header. Their JSON failures use the canonical error envelope.
Billing refusal fields live under `error.details`, with their business code in
`reason`. A completed billing-page replay returns the saved URL. The checkout
service must preserve its `key` across retries. Card setup also receives the
committed `setup_id`.

The account dashboard no longer calls the removed self-service account-deletion
route. Account deletion belongs to the deployment operator console.

## Browser connection and console transactions

The GitHub App browser flow uses the shared policy router and current account
session authority. Embedded controllers supply `WithGitHubConnectWriter` for
linked GitHub identity reads, one-use flow consumption and installation binding.
The old browser Principal lookup and standalone form middleware are removed.

Console writes require `WithConsoleWriter`. Raw credit, billing, hold, deletion,
waitlist, settings and installation methods are no longer HTTP fallbacks.
Missing transaction adapters return `policy_missing`. Waitlist approval requires
an `Idempotency-Key` header. Keep the same key when retrying an approval request.
Console clients read failures from `error.message` in the canonical envelope.

The unused token-table metering and HTTP billing-finalization helpers are removed.
Claim billing and settlement belong to the store's immutable award lifecycle.


## Final credential helpers and reporting writes

The controller `Principal`, `PrincipalFromContext`, `HasScope` and `Scope*`
exports are removed. Embedded handlers receive `capability.Authorized` from the
shared policy router. Team selection comes from that value. An absent credential
no longer selects the default team through a legacy helper.

Replace `/api/v1/runs/{id}/nodes/{nodeID}/metrics` and `/usage` with
`/api/v1/runs/{run}/jobs/{job}/metrics` and `/usage`. The Go client's metric and
usage methods use the final paths. Embedded controllers supply `WithMetricWriter`
for committing writes. A missing adapter returns `policy_missing`.

Deployment settings writes require `WithOperatorSettingsWriter`. Its transactions
recheck deployment authority, and a signup request commits its mode and limits
in one transaction. Raw settings methods are no longer HTTP write fallbacks.

Runner budgets use the verified team and holder after shared policy authorization.
Empty claims spend the polling budget. Awarded work does not. Empty polls are not
audited. Job path identifiers appear as `run_id` and `job_id` in audit records.
The unused live-log HTTP handlers and their `LiveLogRead` type are removed.
Dashboard log reads retain the shared policy routes and committed segment reader.
That reader joins its authorized segments to the in-memory ring.

Egress concurrency refusals use `error.code=rate_limited`, with
`egress_download_limit` or `egress_stream_limit` in `error.details.reason`.
The exported `EgressDownloadLimitCode` and `EgressStreamLimitCode` constants are
removed. Read the canonical error object instead of a top-level code.


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
