# Controller HTTP API

Reporting reads use the shared capability policy and the authorized resource's team. This includes run lists, receipts, attempts, steps, events, approvals, debug pauses, pipeline lists and latest runs, trends, runner views, sizing and logs. Unbound machine roots and deployment credentials cannot substitute for team membership. Work and trigger claims receive only the read alternatives declared for their bindings.

Latest pipeline reads select verified default-branch runs by default. Pass
`from_any_run=true` to include other runs. A job or owner capability can use that
opt-out only when its plan declares `from_any_run: true` on the pipeline reference.
Direct cross-run job-output reads enforce the same requirement.

Run receipts use the same response on HTTP and the local API socket. Each step
identifies its job with `job_id`. Receipts omit raw invocation metadata and local
store locations.

Job reports expose `output_digest` from the job's committed output record.
Receipts include that digest as `sha256:<digest>` in `identity.outputs_hash`,
including outputs copied from a memo. Collected records omit the digest.

Job logs use `/api/v1/runs/{run}/jobs/{job}/logs`, with `/stream` and `/completeness` beneath it. The old `/runs/{run}/logs/{job}` paths are removed. Use `/api/v1/runners` instead of `/api/v1/agents`, `/api/v1/approvals` instead of `/api/v1/approvals/pending`, and `/api/v1/sizing` or `/api/v1/sizing/explain` instead of `/api/v1/capacity/profiles`. Read debug pauses at `/runs/{run}/debug-pauses`. The `paused` alias is removed.

Schedule reads and writes use `/api/v1/schedules`. Repository replacement uses
`PUT /schedules/repos`; repository deletion uses `DELETE /schedules/repos`.
Delete one schedule with `DELETE /schedules/{schedule}`. The `/api/v1/crons`
family and `POST .../disarm` are removed. Reads require member `runs.read`;
writes require member `runs.control`. List queries accept `all` and `next`;
detail queries accept `fires` and `next`, including zero. Overrides preserve
explicit empty argument sets, empty zones and zero catch-up.

Approval decisions use `POST /runs/{run}/approvals/{job}` with `resolution`
(`approved` or `denied`) and optional `comment`. The authenticated identity
supplies the approver. Success returns `{"approval":{...},"replayed":false}`.
Approval reads and decisions use `job_id`. The approval-request HTTP route is
removed; the run owner creates gates through its execution lifecycle.

Schedule run-now, approval decisions and debug-pause creation require one
`Idempotency-Key` header. Debug-pause creation accepts `job_id`, `reason` and
optional `expires_at`, with the run selected by the route. Caller-supplied
approvers, release fields, run selectors and legacy `node_id` fields are refused.
Embedded controllers supply `WithScheduleWriter` and `WithRunControlWriter`;
these adapters recheck authority and commit state, events and replay together.
Missing transaction adapters return `policy_missing`.

Team invitation, build-trust, billing and storage reads use the same capability
boundary, as do GitHub App installation, repository, trigger and extra-repository
reads. Billing availability is checked after authentication.
The API's `/metrics` route requires deployment `operator.limits` or a verified
console session. A dedicated metrics listener keeps its existing network boundary.

The CLI, runners, fetched engines and dashboard use `/api/v1`. The local
controller exposes the same messages and authorization policy over its private API
socket. The [HTTP reference](api-reference.md) is generated from the shared
capability table. It includes node-host and external-proof routes with their
separate audiences.

Inventory lists and run detail collections accept `limit` and an opaque
`cursor`, default to 50 rows and accept at most 200 per page. Responses retain
their named collection field and include `next_cursor` and `truncated`. Pass a
nonempty `next_cursor` back as `cursor`, preserving the other filters, until
`truncated` is false. Raw array responses now use a named collection envelope:
`members`, `invitations`, `deliveries`, `pauses`, `deletions` or `jobs`, as
appropriate. The CLI and dashboard follow these cursors to display complete
inventories. Existing run, log, metric, sizing and other bounded-query
pagination retain their own limits.

The repository's `api/openapi.yaml` is generated from the same policy and the
shared wire types. Each operation retains its complete authorization alternatives
under `x-sparkwing-policy`. JSON schemas preserve omitted fields, nulls and byte
encoding. Shared validators enforce constraints beyond those serialization shapes.
Operations without a documented success body carry `x-sparkwing-wire: undocumented`.
They do not inherit a body from a removed endpoint.

The shared wire contract and controller policy use the same route declarations.
Native deployment and storage composition require acceptance evidence beyond
client transport fixtures.

<!-- design:begin -->

## Authentication

A route requires one complete access alternative: credential class, every listed
grant, resource binding and liveness. Alternatives cannot supply one another's
missing checks. Ownership comes from a server lookup. A request cannot choose its
actor, granted runner identity, billing provenance or claim fence.

Capabilities, browser sessions and local peers pass the same applicable resource
checks. Operator-console sessions and deployment-admin capabilities have separate
powers. See [Authentication](auth.md) and [Security](security.md).

External routes name their own proof: GitHub webhook HMAC, payment signature,
GitHub Actions OIDC, browser CSRF/nonce or exact signed-object URL. A missing or
invalid credential returns 401. A live credential without the route's authority
returns 403. Resource conflicts use their canonical code, including `lock_held`.

Machine enrollment at `POST /api/v1/runners` requires a direct account session
with `runs.write` for a personal runner or `team.admin` for a shared runner.
The issuing transaction rechecks that authority, repository patterns, reserved
pools, the existing runner limit and active name uniqueness. Service issuance at
`POST /api/v1/operator/tokens` rechecks deployment authority and active service
names. Registration facts persist with the capability root. Machine claims retain
every ancestor's repository restriction, including after delegation and rotation.

## Errors and retries

HTTP errors use one envelope:

```json
{"error":{"code":"lock_held","message":"another holder owns this claim","hint":"wait for its lease"}}
```

The inner object requires a nonempty code and a string message. Optional `hint`
and `details` retain their declared string and object types. Clients accept
unknown additive fields and future codes. They reject duplicate keys, null
required fields, malformed bodies and nested envelopes without echoing the body.

The route's retry policy governs the shared HTTP/socket transport. Exact-body
retries preserve the body. Keyed intake keeps its idempotency key and body.
Claims, named claims and reconciliation can return a fresh bearer once, so a lost
response is never automatically replayed. Here intake and retry also suppress
replay. Lease recovery resolves an uncertain claim. A lost initial here bearer
requires a fresh trigger request. See [Here runners](hotfix-runner.md).

## Intake, execution and display

| Operation | Request | Response |
| --- | --- | --- |
| `POST /api/v1/runs` | `CreateRunRequest` | `CreateRunResponse` |
| `POST /api/v1/runners/claim` | `ClaimRequest` | `ClaimResponse` |
| `POST /api/v1/launcher/claim` | `ClaimRequest` | `ClaimResponse` |
| `POST /api/v1/runs/{run}/claim` | Named trigger `ClaimRequest` | `ClaimResponse` |
| `POST /api/v1/runs/{run}/plan` | `PlanSubmission` | `PlanAccepted` |
| `GET /api/v1/runs/{run}/snapshot` | Run selector | `ScheduleSnapshot` |
| `POST /api/v1/runs/{run}/jobs/{job}/reconcile` | `ReconcileRequest` | `ReconcileResponse` |
| `POST /api/v1/runs/{run}/jobs/{job}/transition` | `JobTransitionRequest` | `Applied` |
| `POST /api/v1/runs/{run}/jobs/{job}/steps/start` | `StepReportRequest` with state `running` | `StepRecorded` |
| `POST /api/v1/runs/{run}/jobs/{job}/steps/finish` | `StepReportRequest` with state `passed`, `failed` or `cancelled` | `StepRecorded` |
| `POST /api/v1/runs/{run}/jobs/{job}/steps/skip` | `StepReportRequest` with state `skipped` | `StepRecorded` |
| `POST /api/v1/runs/{run}/jobs/{job}/expansion` | `ExpansionSubmission` | `ExpansionAccepted` |
| `POST /api/v1/runs/{run}/finish` | `FinishRequest` | `Applied` |
| `POST /api/v1/runs/{run}/retry` | `RetryRequest` | `CreateRunResponse` |
| `GET /api/v1/runs/{run}` | Run ID or team number | `RunDetail` |

The wire types live in `internal/controllerwire`. Intake seals requested inputs
once. Acceptance seals checkout declarations, resolved settings and the validated
plan together. Retry retains the original request, source authority, SHA and
accepted seals, with the retrier recorded separately. It never rewrites the
original seal's identity to match the new run ID.

Retry requires `mode=failed|all` and a nonempty `idempotency_key` in the request
body. The server rejects a missing or unknown selection before creating a run.

Direct store consumers use `Store.ClaimUnit` for launcher and runner awards.
`Store.ClaimLaunch`, `LaunchClaim`, `LaunchClaimRequest`, `MinLaunchLifetime`
and `LaunchDeadlineSlack` are removed. HTTP consumers call `Claim` on the controller returned by
`client.NewEngineClient` with `EngineOptions.Launcher`. `Client.ClaimLaunch` is removed.
The granted service root supplies identity and metering. Claims persist their
fences, billing reservations and bootstrap credentials in one transaction.

Owner handoff keeps the awarded execution run ID separate from that original
seal ID. The host receives the inner request document and its canonical seal
hash, plus the accepted spec and plan when resuming. Local, here and runner
adapters preserve those inputs before host binding. Initial intake receipts still
require the returned run ID to match their newly created seal. Local, standalone
and here hosting derive the owner fence from the award and preserve the reason
when authority cancellation ends hosting.

Private claim and reconciliation responses carry execution settings, fences,
accepted inputs and fresh capabilities. Public `RunDetail` and `JobDetail` carry
safe display projections. Status clients never substitute a raw execution seal
for a display record.

A snapshot is one consistent scheduling read. Generator replay uses the exact
accepted body and source fence, including empty expansions. The persisted topology
stays authoritative. Held jobs receive a cancellation request while their current
holder retains result rights. A result replay compares the actor, operation,
fence and body digest. Holder start, retry, report and unstarted release include
`source_attempt`. Initial start uses zero. Later reports use the captured ordinal
returned by `Applied.attempt`. Omission and explicit zero remain distinct.
Other actors and causes omit the field. The store refuses a mismatched ordinal
and replays an identical transition without consuming another attempt.

Run completion returns the committed verdict in `Applied.outcome` and its reason
in `Applied.reason`. Local and remote hosts use that result for completion output and exit status
because cancellation can commit after their last scheduling snapshot.

Step reports carry a positive `source_attempt`, step ID, state, optional masked
error and optional start/finish timestamps. They carry no actor or claim fence.
The controller derives those from authorization and current state, compares the
source ordinal, and commits through the store's attempt-fenced step writer.
The host waits for `{"recorded":true}` before emitting its step diagnostic.
The controller commits these reports through the shared attempt-fenced writer.

A reported job result carries `failure: {job, stage, error}` when it has a
structured failure. The inner error retains its code, message, hint and details.
Recovery reads that same persisted failure. An outcome or reason cannot replace
its stage. The error-only result field is removed.

## Run detail

`GET /api/v1/runs/{run}/attempts` returns retry history as a cursor-paginated
`RunPage`, oldest first. Stable IDs and team-local numbers select the same
history. Each entry uses the shared public `RunDetail` projection, including
redacted inputs and current `inputs.lineage`. Invocation
metadata, execution seals, environment digests and checkout paths are omitted.
Every returned run must pass the caller's binding. A denied or invalid member
fails the response before any history is sent.
Equal creation timestamps sort by run number. Following cursors reads long
chains. Cycles cannot repeat entries or prevent traversal from finishing.

`RunPruner.DeleteRun` uses the authorized `DELETE /api/v1/runs/{run}` route.
It succeeds after an empty HTTP 204 acknowledgement, including log cleanup.
The client returns only an error. Deleting another member's run requires the
credential to carry `team.admin`. A credential with `runs.control` may delete
its own account's runs. Prune output identifies the selected run and
any structured failure. It does not report a removed-row count or replay state.
Cancel, retry and prune preserve numeric run selectors and stable IDs from flags
or stdin. The authenticated controller resolves them within the current team.

`GET /api/v1/runs/{run}` returns the shared run status object with `inputs`,
`jobs`, `attempts`, `logs` and `notices`. Job and attempt status excludes raw
declarations, holder identities and capability prefixes. Log subjects preserve
the distinction between job attempts and trigger, plan or eval work.

Inputs always use the display projection. Secret argument values are redacted.
Missing or invalid classification redacts every value, including on finished
runs. The `include` query cannot reveal secrets or select an execution view.
Members need `runs.read`. Bound claims need `source.read` or `trigger.host`
for that run. Missing or dead credentials receive `401`; live credentials
without the required authority receive `403`.

The controller requires its shared verifier and a complete status-read adapter.
An absent adapter returns `policy_missing`. It never fills missing history with
invented attempts or returns a partial status response after a read error.

## Launcher reconciliation

`POST /api/v1/launcher/sync` accepts `LauncherSyncRequest`. Each observation
contains `award: {kind, id, holder, generation}`, `job: {namespace, name, uid}`,
and the reserved `node_uid`. The award generation identifies the unit award,
including an eval's own lease. It does not replace that eval's source generation.
The server compares observations with this launcher's stored awards.

The default store adapter reads ancestry, current claims and execution inputs in
one snapshot. It checks the root and service identity, holder, generation,
expiry and cancellation. Eval awards also require unresolved source expansion,
and for a controller-dispatched run, that the controller still owns the run. Exchange preserves the original award shown
by synchronization. Sync never changes a claim or returns its bearer.

The response contains held `awards` without bearer material and a `remove` list
of exact Job instances. Each removal carries namespace, name and UID. The
launcher must use that UID as a Kubernetes deletion precondition because a new
Job can reuse the name. A failed transport, decode or validation returns no
partial sync result. Native Kubernetes adoption and teardown still require
execution evidence.

## Logs and stored data

Run logs use `GET /api/v1/runs/{run}/logs`. Job reads, streams and completeness
use `/api/v1/runs/{run}/jobs/{job}/logs`, `/logs/stream` and
`/logs/completeness`. Job segment and manifest writes use the matching
`/log-segments` and `/log-manifest` routes. A commit verifies the stored objects it
records; the final commit's `manifest` names the manifest's stored `size` and
`digest`, and a mismatch answers `integrity_failure`. Log subjects distinguish job attempts
from trigger, plan and eval units. A final log-tail exception cannot reopen work
or write another attempt's logs.

An interrupted or incomplete observation remains visible independently of the
recorded run verdict. The CLI returns exit 3 when it cannot establish a complete
observation, and exit 4 when the controller refuses the read (`not_found`,
`unauthenticated`, `forbidden`), which retrying cannot change. See
[Observability](observability.md).

Blob transfers and logical memo/depcache records use the authenticated `/data`
routes. Storage authorization rechecks the live capability, bound uploader and
persisted read edges. Object requests receive only signed transfer headers.
See [Direct data uploads](data-uploads.md).

## Data downloads

`POST /api/v1/data/download` takes `{"digest":"<sha256>"}`. It requires a
live data capability and a digest in that caller's read set. The controller
resolves the team, repository and ref from authenticated authority. A digest,
team query or object path grants no access by itself. Ordinary member and
machine tokens cannot download bytes without the required data authority.

The response contains `url`, `headers`, `sha256`, `size` and `expires`.
Send only the returned headers to the storage URL. The controller charges the
stored object size against the team's daily download cap before signing.
The URL names one immutable object and expires after 60 seconds. Revocation
stops new signing requests. Issued URLs remain valid until their expiry.
S3 and filesystem backends use the same authorization boundary.

`POST /api/v1/data/has-many` takes `{"digests":["<sha256>"]}` and returns
`{"present":["<sha256>"]}`. Missing and unreadable digests are omitted.
An empty array returns an empty array. Duplicate digests appear once. An
optional `deadline` is an RFC 3339 timestamp that bounds this query. Presence
checks transfer no object bytes and consume no download allowance.

Both routes use the common error envelope. Missing or dead authority returns
`401`. Live authority without the required grant or read edge returns `403`.
An authorized missing blob returns `404` with `blob_missing`. A spent download
allowance returns `429` with `Retry-After`. Private backend errors return
`internal` without object keys or diagnostic text.

`GET /api/v1/services` announces the download route only when its authority,
read-set, metadata, signing and charging adapters are configured. Signed
URLs work through the backend selected by the controller. Cache grants and
`{kind,key}` download requests are removed. There is no cache-service fallback.
Logs use controller-proxied reads, separate from byte-storage downloads.

## Concurrency groups

`GET /api/v1/queue/groups/{group}` reads one concurrency group as `QueueGroup`,
with one-based waiter positions, under a live claim. No route acquires,
heartbeats, releases or cancels a group directly: every acquisition goes
through the admission owner.

## Version policy

A repository's exact SDK pin selects its verified engine. From launch freeze,
the controller supports requests emitted by each engine for twelve calendar
months after its release. Older engines continue until a signed security
revocation or minimum-version policy takes effect. Additive fields and codes
remain readable. Each selected engine owns its matching node protocol.
See [Versioning](versioning.md).

An applicable Go SDK replacement uses the installed CLI's engine and emits an
`engine_pin_disabled` notice. The pin retains the declared SDK version and the
`replacement` module selector. The accepted identity records the installed
executable's digest in `manifest_digest`, rather than a signed release manifest.

<!-- design:end -->

## Job output

`GET /api/v1/runs/{run}/jobs/{job}/output` returns `{"output": VALUE}` from the
job's committed output record. Members need `runs.read`. Execution credentials
need a current declared read relation. The child output path uses the creating
job's child relation. Missing or corrupt bytes fail the read.

Refunds are made in the Stripe dashboard. The controller learns of them only
through Stripe's refund webhooks, which reverse the refunded credits once.

## Cloud overflow controls

`GET /api/v1/overflow` returns the authenticated account's `monthly_minutes`,
`used_seconds` and UTC `period_start`. The default limit is null. `PUT` replaces
the saved limit with `{"monthly_minutes":N}` or removes it with
`{"monthly_minutes":null}`. A sealed `request.burst.overflow_minutes` can only
narrow that account limit. Team billing admission still applies. Self-hosted and
local capacity remains eligible when Cloud claims wait for budget.

`CreateRunRequest.local_secrets` carries requester-resolved values during intake.
`RenewalRequest.local_secrets` re-supplies them only through the live here owner,
including after controller restart. Other claimants cannot supply or replace values. The sealed request retains `local_secret_names`. Responses omit the
values. Secret release follows the existing live claim and declared-secret
checks and returns `masked:true`. Values remain in the intake controller's
memory and are removed on terminal completion, team deletion and owner lease
expiry. Reads recheck the sealed burst preference set. Values remain unavailable
until the here host restores them. Retry requires a fresh requester submission.
