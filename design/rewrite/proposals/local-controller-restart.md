# Local controller restart without handover

Design proposal, 2026-11-05. Design only.

Korey chose one local controller and durable state. The controller is today's
wingd without job execution. It alone writes `state.db`. It serves the existing
controller API and machine admission through HTTP on the local API socket.
CLI and runner clients make short, retryable requests to that address.
The browser uses a loopback HTTP dashboard on the same controller. A socket
connection owns no queue position, grant or job.

Upgrade means stop the controller, start the new installed binary, back up the
store, migrate on open, then serve. Clients retry connection failures for a few
seconds. There is no handover protocol, transferred listener, retired controller
or proxy chain. Runner processes keep executing while the controller restarts.

D15 remains fixed: preserve FIFO under the configured admission policy, count
job process trees as Sparkwing load, and honor SDK `Cores()`. The owners and
guards are at [design/rewrite/ORIENTATION.md:124](../../../design/rewrite/ORIENTATION.md#L124) through line 127.
The reported November incident motivates the change. This note does not claim
to reproduce it.

The store holds authority, scheduling facts and acknowledged progress. Its
existing managed blob storage holds log and artifact bytes. Engine caches,
OS process handles and disposable measurements do not become SQLite rows.
A runner still supervises its processes and pipes. Its upgrade lets that
process finish. A runner crash can lose execution and unacknowledged output.
A controller restart must not.

## What the code does today

References describe the checkout at `7e749ce72`. Paths start at the repository
root. The table is retained from the adopt proposal. Its citations were rechecked.
Later sections separate current-code findings from proposed changes.

| Area | Current behavior and evidence |
| --- | --- |
| Engine selection | The CLI resolves the repository SDK pin to a verified Sparkwing executable and replays the command through it. See [cmd/sparkwing/toolchain_select.go:98](../../../cmd/sparkwing/toolchain_select.go#L98) and [cmd/sparkwing/toolchain_select.go:132](../../../cmd/sparkwing/toolchain_select.go#L132). |
| Replacement | A newer protocol, newer binary version, or differing build at the same version can trigger takeover. Takeover sends `DrainRequest`, waits for the election lock, then spawns the replacement. Default spawning uses the caller's executable. See [internal/wingd/client/client.go:457](../../../internal/wingd/client/client.go#L457), [internal/wingd/client/client.go:573](../../../internal/wingd/client/client.go#L573), and [internal/wingd/client/spawn.go:20](../../../internal/wingd/client/spawn.go#L20). |
| Drain | Drain marks admission as draining, closes the API listener, and requests shutdown. New admissions are refused while draining. Final shutdown closes every admission connection. The local runtime cancels its controller workers when the daemon exits. See [internal/wingd/server.go:1302](../../../internal/wingd/server.go#L1302), [internal/wingd/server.go:818](../../../internal/wingd/server.go#L818), [internal/wingd/server.go:307](../../../internal/wingd/server.go#L307), and [internal/wingd/local_runtime.go:272](../../../internal/wingd/local_runtime.go#L272). |
| Lost execution | The SDK session kills its process tree when its execution context ends. Owner reaping can fail a started local run after owner loss and records `agent_lost`. This explains a possible failure chain, not proof of the incident's exact sequence. See [internal/nodehost/protocol_session.go:249](../../../internal/nodehost/protocol_session.go#L249) and [internal/store/owner_reaper.go:69](../../../internal/store/owner_reaper.go#L69). |
| Execution ownership | The embedded runner hosts awards in goroutines. Local hosts hold direct tenant, operator, renewal, admission, and delegation bindings. SDK sessions own protocol and output pipes, masking, and process cleanup. See [internal/runners/loop.go:187](../../../internal/runners/loop.go#L187), [cmd/sparkwing/local_host.go:55](../../../cmd/sparkwing/local_host.go#L55), [cmd/sparkwing/local_host.go:68](../../../cmd/sparkwing/local_host.go#L68), and [internal/nodehost/protocol_session.go:202](../../../internal/nodehost/protocol_session.go#L202). |
| Two admission layers | Logical reservations, arrivals, fences, and permits belong to the store. The machine ledger separately snapshots leases, tokens, arrival counters, priorities, and waiters. Its disk writer explicitly drops waiters. Connection metadata also holds queue deadlines and peer identities. See [design/rewrite/ADMISSION.md:3](../ADMISSION.md#L3), [internal/admission/snapshot.go:8](../../../internal/admission/snapshot.go#L8), [internal/wingd/state.go:25](../../../internal/wingd/state.go#L25), and [internal/wingd/conn.go:49](../../../internal/wingd/conn.go#L49). |
| Store ownership | The runtime elects before opening the local controller. That controller opens `state.db`; SQLite uses one connection. Open runs schema setup and forward migrations. Newer schemas are refused. See [internal/wingd/local_runtime.go:106](../../../internal/wingd/local_runtime.go#L106), [internal/wingd/localcontroller/controller.go:72](../../../internal/wingd/localcontroller/controller.go#L72), [internal/store/store.go:265](../../../internal/store/store.go#L265), [internal/store/store.go:1355](../../../internal/store/store.go#L1355), and [internal/store/schema_error.go:6](../../../internal/store/schema_error.go#L6). |
| Volatile authority | Requester secrets live in a store-owned memory map. The local claimant caches a delegated credential and revokes it on close. Object serving gets a random signing key at controller construction. See [internal/store/local_secrets.go:37](../../../internal/store/local_secrets.go#L37), [internal/wingd/localcontroller/claimant.go:47](../../../internal/wingd/localcontroller/claimant.go#L47), [internal/wingd/localcontroller/claimant.go:82](../../../internal/wingd/localcontroller/claimant.go#L82), and [cmd/sparkwing/local_services.go:132](../../../cmd/sparkwing/local_services.go#L132). |
| Dashboard lifetime | An ordinary start serves a remembered dashboard or only the API socket. Dashboard binding is conditional. Idle detection can stop the daemon. A remembered remote address restores as loopback, the behavior called D8 here. See [internal/wingd/local_runtime.go:139](../../../internal/wingd/local_runtime.go#L139), [internal/wingd/local_runtime.go:251](../../../internal/wingd/local_runtime.go#L251), [internal/wingd/idle.go:35](../../../internal/wingd/idle.go#L35), and [internal/wingd/local_dashboard_memory.go:42](../../../internal/wingd/local_dashboard_memory.go#L42). |
| Browser authority | Login links come from a verified API-socket peer and expire after 60 seconds. Sessions live in memory, with 12-hour idle and 24-hour absolute expiry. Login returns a URL-fragment bearer, and API requests authenticate with that bearer. Host, Origin, and fetch checks precede the session gate. See [internal/wingd/local_login.go:23](../../../internal/wingd/local_login.go#L23), [internal/wingd/local_login.go:98](../../../internal/wingd/local_login.go#L98), [internal/wingd/local_login.go:137](../../../internal/wingd/local_login.go#L137), [internal/wingd/local_login.go:161](../../../internal/wingd/local_login.go#L161), [internal/wingd/local_login.go:253](../../../internal/wingd/local_login.go#L253), and [internal/wingd/local_runtime.go:499](../../../internal/wingd/local_runtime.go#L499). |
| Dashboard commands | `open` prints a login link and can start the dashboard. `start` permits a remote address with `--allow-remote`; `stop` stops the whole controller. The default address is `127.0.0.1:4343`. See [cmd/sparkwing/dashboard_lifecycle.go:149](../../../cmd/sparkwing/dashboard_lifecycle.go#L149), [cmd/sparkwing/dashboard.go:97](../../../cmd/sparkwing/dashboard.go#L97), [cmd/sparkwing/dashboard.go:114](../../../cmd/sparkwing/dashboard.go#L114), and [cmd/sparkwing/dashboard.go:137](../../../cmd/sparkwing/dashboard.go#L137). |
| Runner service | The native runner service is a separate `sparkwing-runner start` process. It retains an agent bearer, shares an HTTP controller transport, and keeps award bearers separate. Its software update drains its own claims before replacement and restart. Registration refresh alone does not restart it. See [internal/runners/service/service.go:283](../../../internal/runners/service/service.go#L283), [internal/runners/credentials.go:11](../../../internal/runners/credentials.go#L11), [internal/runners/runtime.go:29](../../../internal/runners/runtime.go#L29), [internal/runners/installation.go:46](../../../internal/runners/installation.go#L46), and [internal/runners/service/refresh.go:49](../../../internal/runners/service/refresh.go#L49). |
| Landing | The land job verifies, pushes, and records its result. It has no install step. The installer builds the CLI and runner into the selected bin directory. See [.sparkwing/jobs/land.go:227](../../../.sparkwing/jobs/land.go#L227), [bin/install.sh:24](../../../bin/install.sh#L24), and [bin/install.sh:77](../../../bin/install.sh#L77). |

## Admission clients and the cut

This inventory covers production callers in this checkout and source searches
of the local Bitwing, sparks-core, Xwing, Overwing, Flockwing and Stormwing
repositories. It cannot rule out an external consumer of public `pkg/wingwire`.

| Caller today | Evidence | Change |
| --- | --- | --- |
| Go SDK `Cores()` and TypeScript `cores()` | [sparkwing/plan.go:354](../../../sparkwing/plan.go#L354), [sparkwing/protocol_plan.go:104](../../../sparkwing/protocol_plan.go#L104), [sdk/typescript/src/plan.ts:419](../../../sdk/typescript/src/plan.ts#L419) | Keep declarations, defaults and `job_cores` host negotiation. Neither SDK directly speaks wingd admission. Nodehost translates the declaration. |
| Nodehost machine admission | [internal/nodehost/machine_admission.go:21](../../../internal/nodehost/machine_admission.go#L21), [internal/nodehost/local_admission.go:55](../../../internal/nodehost/local_admission.go#L55), [internal/nodehost/local_admission.go:91](../../../internal/nodehost/local_admission.go#L91) | Replace Acquire/Watch/Release on a held connection with HTTP request, ticket status, lease renewal and explicit release. Preserve skip, fail, supersede and cancellation outcomes. |
| Local CLI execution hosts | [cmd/sparkwing/local_run_host.go:98](../../../cmd/sparkwing/local_run_host.go#L98), [cmd/sparkwing/local_host.go:55](../../../cmd/sparkwing/local_host.go#L55) | Move OwnerHost and job hosting into the local runner. CLI submits and observes through HTTP, including foreground cancellation. No direct store fallback. |
| Native runner executing a remote controller's work here | [internal/cluster/production_runtime.go:157](../../../internal/cluster/production_runtime.go#L157) | Use the same local HTTP admission client when machine admission is enabled. Keep remote-controller participant namespacing. Remote execution still talks to its configured controller. |
| Runner capacity preflight | [internal/cluster/native_claim_capacity.go:18](../../../internal/cluster/native_claim_capacity.go#L18), [internal/cluster/production_runtime.go:54](../../../internal/cluster/production_runtime.go#L54) | Read the local HTTP capacity view. It remains advisory. Only the admission transaction grants capacity. |
| Queue priority CLI | [cmd/sparkwing/queue_priority.go:64](../../../cmd/sparkwing/queue_priority.go#L64) | Replace wingd Query and SetPriority with controller HTTP calls. Persist priority overrides. |
| Queue CLI and dashboard bridge | [cmd/sparkwing/queue.go:40](../../../cmd/sparkwing/queue.go#L40), [internal/controller/queue_view.go:34](../../../internal/controller/queue_view.go#L34), [cmd/sparkwing/local_services.go:177](../../../cmd/sparkwing/local_services.go#L177) | CLI already uses controller HTTP. Remove the controller's socket call back into itself. Query stored machine admission alongside logical admission. |
| Local status/control clients | [internal/wingd/client/local_control.go:39](../../../internal/wingd/client/local_control.go#L39) | Move status/control onto the API socket. Installed CLI owns restart. Browser requests cannot replace the executable. |

Bitwing's gate slots are separate. The source at
`/home/bombuntu/code/bitwing/.sparkwing/jobs/pre_push.go:331` uses
`BITWING_GATE_SLOTS` and `syscall.Flock`, with per-step lock files. It is not a
wingd client. The ordinary pipeline job is at the same file's line 46.
Recommendation: replace the private CPU gate with truthful SDK `Cores()`
declarations in a separate Bitwing change. Keep any distinct per-step policy
only if it has a reason beyond machine capacity. Do not grow this proposal into
a cross-tool scheduler.

No direct admission client was found in sparks-core or the other searched
tools, including subprocess calls to admission/queue commands. Sparks-core
needs no transport migration on that evidence. Xwing does launch `wingd run`
for private candidates at
`/home/bombuntu/code/xwing/internal/toolruntime/runtime.go:651` and
`/home/bombuntu/code/xwing/internal/toolruntime/dashboard.go:423`. Update that
private lifecycle contract for a controller plus runner. Overwing invokes the
normal pipeline CLI at
`/home/bombuntu/code/overwing/internal/textlint/runner.go:60`.

### Stored requests and leases

Add machine-admission rows to the same store. Keep logical reservations,
permits and execution awards as their existing distinct concepts. Preserve the
policy algorithms in [internal/admission/ledger.go:1438](../../../internal/admission/ledger.go#L1438) and the logical
contract in [design/rewrite/ADMISSION.md:3](../../../design/rewrite/ADMISSION.md#L3). Store transitions are the source of
truth. An in-memory projection may speed reads, but it cannot grant ahead of a
commit or recover from a sidecar snapshot.

A request includes a client-generated request key, participant/run/job identity,
resources, class, priority, semaphores, existing queue policy and deadline, and
local holder PID plus process start token. Scope keys to the authenticated
subject and machine. Store a request fingerprint. Reusing a key with different
inputs returns a conflict. Retrying the same request returns its existing
queued ticket, lease or terminal outcome.

A queued response includes a stable ticket, stored arrival sequence and
suggested re-ask interval. Status polling keeps the ticket alive without
changing its arrival. Cut long-poll from the first implementation. It can later reduce polling
without owning queue state. Preserve original absolute queue
deadlines. Persist priority changes, semaphore ownership, members and parent
relationships. A disconnected requester is reclaimed by process identity or
its ticket's liveness deadline, never by HTTP disconnect. The ticket liveness
window needs the same approval as the new lease expiry below.

A granted response includes a never-reused lease identity, authority, expiry and
renewal interval. Renew about every 10 seconds. Use the existing award lifetime
where suitable. A new machine-lease default needs Korey's OK. Proposed starting
point: 60 seconds, not a job duration limit. Renewal carries a monotonic
sequence. Replaying the same sequence returns its first committed expiry,
rather than extending authority forever. Explicit release and ticket cancel
are idempotent. Keep terminal rows long enough to answer outstanding retries.
Do not let an old acquire retry recreate a released lease.

For local holders, verify PID ownership from the socket peer and record
`procgroup.ProcessBirth` with the PID. Validate the birth token against the OS. Register execution roots only after
verifying their identity and ownership against the authenticated peer. Shared
runner transports must not mix jobs' root identities. Recheck live identities on restart and during the
normal admission scan. Confirmed exit or PID reuse frees capacity immediately
on observation. Inspection errors leave the lease until expiry. Renewals do
not rescue a confirmed dead holder. Do not trust a caller's arbitrary PID.

Preserve D15's current ordering within priority and class. Preserve the guarded
backfill rule that cannot delay the head. Re-asking never moves a ticket back.
Keep measured CPU above a job's requested charge attributed to its process
tree. Deduplicate shared roots. Current ownership logic is at
[internal/wingd/headroom.go:255](../../../internal/wingd/headroom.go#L255) and [internal/wingd/headroom.go:293](../../../internal/wingd/headroom.go#L293).
Rebuild sampling from durable holder roots. On restart, retain reservations
while warming CPU measurements. Avoid a burst of grants based on missing data.

Lease expiry removes authority. The holder must stop work if it cannot renew
before expiry. Keep owned roots in the registry until confirmed exit, even
after lease expiry. A still-running tree remains measured load while it exits.
Do not turn expiry into unmeasured free capacity. Keep execution award fencing
and machine admission aligned, while retaining their separate identities.

## State that must survive

| State today | Current owner and evidence | New home |
| --- | --- | --- |
| Machine leases, tokens, arrivals, priorities, waiters and semaphore reservations | [internal/admission/snapshot.go:8](../../../internal/admission/snapshot.go#L8), [internal/wingd/state.go:25](../../../internal/wingd/state.go#L25) | Store request/ticket/lease rows and counters. Delete sidecar persistence, which currently drops waiters. |
| Connection role, participant/run/member/parent mappings, requested and charged resources, queue start/deadline, PID, liveness nonce and sampling counters | [internal/wingd/conn.go:38](../../../internal/wingd/conn.go#L38), [internal/wingd/server.go:60](../../../internal/wingd/server.go#L60) | Persist authority and deadlines on ticket/lease/holder rows. Persist useful history. Derive peer identity for each request. Drop connection roles, nonces and pushed-delivery bookkeeping. |
| Pending cancellations, cancelled-run cache, disconnected and reattach maps | [internal/wingd/server.go:65](../../../internal/wingd/server.go#L65) | Store cancellation and terminal outcomes. Renewal/status returns them. Delete reattach state. Closing HTTP never releases capacity or finalizes a run, unlike [internal/wingd/server.go:1520](../../../internal/wingd/server.go#L1520). |
| Headroom samples, process CPU baselines, smoothing, attribution, measured PID set and host/container totals | [internal/wingd/server.go:82](../../../internal/wingd/server.go#L82), [internal/wingd/sampler.go:98](../../../internal/wingd/sampler.go#L98) | Rebuild from OS measurements plus stored holder roots and effective policy. Cache samples only. Keep grants conservative during warmup. |
| Recent admission events and journal buffers | [internal/wingd/events.go:35](../../../internal/wingd/events.go#L35), [internal/wingd/journal/journal.go:38](../../../internal/wingd/journal/journal.go#L38) | Store user-visible admission history with transitions. Diagnostic journal buffers remain best effort. They grant no authority. |
| Election lock, listeners, accepted sockets, timers, channels, shutdown flags, cgroup handles and readiness/activity counts | [internal/wingd/server.go:33](../../../internal/wingd/server.go#L33), [internal/wingd/local_runtime.go:34](../../../internal/wingd/local_runtime.go#L34) | Recreate handles. Derive activity from store. Reapply effective policy and limits. Delete idle shutdown and stay bookkeeping. |
| Requester local secret values in `sync.Map` | [internal/store/local_secrets.go:37](../../../internal/store/local_secrets.go#L37), [internal/store/local_secrets.go:70](../../../internal/store/local_secrets.go#L70), [internal/store/store.go:305](../../../internal/store/store.go#L305) | Encrypt run-scoped values in store before intake acknowledgement. Delete after terminal cleanup. Existing keyring supplies encryption. This changes the at-rest secret contract and needs Korey's OK. |
| Embedded claimant's cached bearer and close-time revocation | [internal/wingd/localcontroller/claimant.go:47](../../../internal/wingd/localcontroller/claimant.go#L47), [internal/wingd/localcontroller/claimant.go:82](../../../internal/wingd/localcontroller/claimant.go#L82) | Delete the credential cache and embedded claimant. Local runner uses peer auth. Keep persistent award credentials and credential epoch across controller restart. Extract its labels policy. |
| Filesystem object URL signing key | [cmd/sparkwing/local_services.go:132](../../../cmd/sparkwing/local_services.go#L132) | Persist encrypted signing material in store metadata. Preserve outstanding upload/download URLs. Existing blob bytes stay in managed `data/` storage. |
| Pending sign-in codes, sessions, last seen, expiry and revocation channel | [internal/wingd/local_login.go:35](../../../internal/wingd/local_login.go#L35), [internal/wingd/local_login.go:50](../../../internal/wingd/local_login.go#L50) | Store code/session digests, subject, exact origin, expiry and revocation. Replace revocation channels with request-time reads. |
| Remembered dashboard address/read-only state | [internal/wingd/local_dashboard_memory.go:17](../../../internal/wingd/local_dashboard_memory.go#L17) | Read effective settings, including allowed hosts. Store durable policy where needed. Delete activation memory and `dashboard.json`. |
| Embedded runner active claims, job hosts, SDK pipes, masks, exit waits and log offsets | [internal/wingd/localcontroller/controller.go:115](../../../internal/wingd/localcontroller/controller.go#L115), [internal/nodehost/protocol_session.go:202](../../../internal/nodehost/protocol_session.go#L202) | Move execution into independent runner processes. Store claims, award fences, acknowledged logs and results. The original runner retains its OS handles and pending batches until it finishes. |
| Live log rings, watchers and HTTP controller caches | [internal/controller/livelogs.go:41](../../../internal/controller/livelogs.go#L41), [internal/controller/server.go:45](../../../internal/controller/server.go#L45) | Rebuild disposable views. Replay durable logs by attempt and offset. A reconnect may lose an uncommitted live preview, never committed segments. Keep durable security keys, following [internal/store/session_key.go:21](../../../internal/store/session_key.go#L21). |

Keep configuration and the existing OS keyring as installation inputs. Persist
runtime overrides and effective admission facts needed to interpret live
leases. Do not introduce a second authority file. Persisted local secrets also
enter encrypted backups. Deleting a live row does not erase an older backup.
Document that retention instead of promising immediate erasure.

## Use the existing runner path

The loop itself can run locally. It depends on a `ClaimController` at
[internal/runners/loop.go:21](../../../internal/runners/loop.go#L21). Hosting builds award-bound clients at
[internal/runners/hosting.go:129](../../../internal/runners/hosting.go#L129). The remote host composition already uses
HTTP store access at [internal/cluster/production_runtime.go:95](../../../internal/cluster/production_runtime.go#L95).
Use that composition for local owner planning and jobs. Remove every direct
store binding from local execution hosts, including the foreground owner path.
Controller scheduling, reconciliation and reaping remain in the controller.

| Concern | What works today | Missing work |
| --- | --- | --- |
| Transport and URL | [internal/controller/client/engine_client.go:14](../../../internal/controller/client/engine_client.go#L14) accepts an injected HTTP client. [internal/wingd/client/api_http.go:13](../../../internal/wingd/client/api_http.go#L13) uses `http://sparkwing-api` with a socket dialer. | Add a local runner selection that injects this transport into claimant, private host, logs and objects. Keep the synthetic HTTP URL. A literal Unix socket path is not an accepted controller URL. |
| Socket credentials | [internal/wingd/apisock.go:98](../../../internal/wingd/apisock.go#L98) verifies the peer. [internal/controller/local_http.go:15](../../../internal/controller/local_http.go#L15) maps UID/SID. [internal/store/authentication_contract.go:89](../../../internal/store/authentication_contract.go#L89) grants local claim authority. | Stock runner CLI uses URL plus `FileBearer` at [internal/cluster/runner_cli.go:90](../../../internal/cluster/runner_cli.go#L90). [internal/runners/runtime.go:34](../../../internal/runners/runtime.go#L34) requires a bearer callback. Permit verified local peer auth without an `agent-token` file. No enrollment or new long-lived credential is needed locally. |
| Award authority | [internal/runners/hosting.go:134](../../../internal/runners/hosting.go#L134) uses the returned award bearer. Explicit Authorization wins over peer authority at [internal/routepolicy/router.go:245](../../../internal/routepolicy/router.go#L245). | Preserve narrow award bearers and stored generations. Never substitute the socket peer's broad authority for an execution award. Controller shutdown must not revoke awards. |
| Machine labels and local features | [internal/controller/engine_claim.go:82](../../../internal/controller/engine_claim.go#L82) applies the local claim profile. [cmd/sparkwing/local_host.go:71](../../../cmd/sparkwing/local_host.go#L71) and [cmd/sparkwing/local_host.go:92](../../../cmd/sparkwing/local_host.go#L92) add local cache/publication behavior. | Extract labels from the claimant being deleted. Port local workspace/source, cache, publication and foreground cancellation behavior into the common runner path. Preserve engine pin selection. |
| Logs and artifacts | [internal/controller/client/native_logs.go:117](../../../internal/controller/client/native_logs.go#L117) uploads signed segments. [internal/controller/client/native_logs.go:179](../../../internal/controller/client/native_logs.go#L179) commits replayable manifests. PUT uses the injected HTTP transport at line 102. | Persist the local signing key. Retry interrupted PUTs and commits. Batch output using this shared segmented protocol. It is not currently a raw byte-append endpoint. Cut a second local log protocol from scope. |
| Reconnect | [internal/controller/client/engine.go:79](../../../internal/controller/client/engine.go#L79) makes three retries, with 700 ms total backoff. [internal/controller/client/retry.go:16](../../../internal/controller/client/retry.go#L16) omits missing socket errors. | Give safe and keyed requests a few-second local reconnect budget. Include missing socket and interrupted response reads. Ordinary claim transport errors currently end the loop at [internal/runners/loop.go:317](../../../internal/runners/loop.go#L317). Keep it polling across restart. |
| Renew and result | [internal/lease/keeper.go:81](../../../internal/lease/keeper.go#L81) tolerates transient renew errors. [internal/limits/claim.go:7](../../../internal/limits/claim.go#L7) gives existing awards a three-minute lease. Terminal routes have exact-body retry metadata at [internal/capability/routes.go:17](../../../internal/capability/routes.go#L17). | Preserve hard expiry and cancellation semantics. Add restart retry without canceling job contexts on the first socket failure. Verify terminal replay after a lost acknowledgement. |

Claims are the remaining unsafe mutation. They are `RetryNever` at
[internal/capability/routes.go:175](../../../internal/capability/routes.go#L175). The loop request at
[internal/runners/loop.go:380](../../../internal/runners/loop.go#L380) has no idempotency key. A committed claim whose
reply disappears leaves an unknown award until expiry. That is recoverable,
but it is not transparent restart.

Add a claim request key scoped to the runner instance. Commit the claim and
its replay receipt together. Retrying returns the same award, generation and
usable bearer. Capabilities store a digest at [internal/store/schema.sql:1283](../../../internal/store/schema.sql#L1283),
so an award ID alone cannot reconstruct the returned bearer. Encrypt the
replay response in the store through award terminal state or expiry. Retain a
tombstone through the bounded retry window. Cut a new start-acknowledgement
handshake. Replays never resurrect expired or completed awards. A claim retry
must not accidentally acquire another unit.

Intake also needs review. It already has persisted idempotency at
[internal/store/intake_runtime.go:562](../../../internal/store/intake_runtime.go#L562). The HTTP client disables automatic
retry for a here intake because its response contains a one-time bearer at
[internal/controller/client/engine.go:67](../../../internal/controller/client/engine.go#L67). Local intake currently issues that direct here authority at
[internal/store/intake_runtime.go:222](../../../internal/store/intake_runtime.go#L222).
For local submission, replace direct CLI hosting with a queued owner eligible
for this machine's runner. Preserve here placement and foreground observation.
The runner receives its normal award bearer through the claim API. Local intake
then returns replayable run/observer data without a one-time hosting bearer.
The caller can query its committed run after a lost response. Keep this on the
existing intake API. Cut a second local submission protocol.

## Restart, runner roll and interrupted requests

| Event | Queue and grants | Running jobs and progress |
| --- | --- | --- |
| Controller stops and restarts within lease budgets | Arrival, priority, deadlines, tickets and lease identities remain in store. Connections vanish. Clients reconnect and ask with the same keys. No new admission domain forms. | Runner processes keep SDK pipes and children alive. They renew the same awards, append the same log batches and report the same results when service returns. No re-launch or attempt increment. |
| Controller dies before a request commits | Transaction rolls back. Retry may create the request once. | A claim is not permission to execute until its committed response is known. Retry the same claim key. |
| Controller commits then dies before replying | Retry reads the existing ticket, grant, claim, cancellation or result. The original arrival and charge survive. | Replay the exact terminal body or log segment identity. Existing segment matching checks index, offset and digest at [internal/store/result_logs.go:224](../../../internal/store/result_logs.go#L224). No duplicate bytes or result transition. |
| Controller outage outlasts expiry | Expired tickets/leases become terminal. A caller must observe loss. Old renewals cannot recreate them. | Award keeper stops execution when authority expires. Existing owner reaping can fail a started local run with `agent_lost` at [internal/store/owner_reaper.go:69](../../../internal/store/owner_reaper.go#L69) and [internal/store/owner_reaper.go:99](../../../internal/store/owner_reaper.go#L99). Restart is not a promise to survive an unbounded outage. |
| Requester crashes while queued or holding | Stored PID/start identity allows prompt reclamation. Ticket/lease expiry is the fallback. A lost release reply is safely repeatable. | Runner death still uses normal lost-award recovery. Store records survive, but the system cannot promise exactly-once external side effects from arbitrary jobs. |
| Runner upgrades | New runner instance claims through the same controller and machine admission. Old instance stops claiming. Old leases remain charged until explicit release or expiry. | Old instance finishes hosts, uploads and terminal acknowledgements, then exits. New instance executes new awards. No transfer of process handles or active awards. |
| Old runner crashes during a roll | New runner does not steal live old awards. Reaping uses existing expiry/fences and retry policy. | Persisted results/logs remain. In-flight execution follows normal crash recovery. Controller restart does not cause that crash. |

Keep the election lock around the whole store-owner lifetime. Stop and join
controller work before releasing it. The successor acquires it, takes a
consistent backup, then opens and migrates `state.db`. Today election precedes
open at [internal/wingd/local_runtime.go:106](../../../internal/wingd/local_runtime.go#L106), and migrations are transactional
at [internal/store/store.go:1316](../../../internal/store/store.go#L1316). Add a backup-before-migrate step. It does not
exist on this open path. Use a SQLite-aware backup, or checkpoint with no writer
before copying, as required by [internal/store/backup_restore_drill_test.go:244](../../../internal/store/backup_restore_drill_test.go#L244).
Keep the existing forward-migration digest checks and newer-schema refusal.
On failure, report the exact migration and backup. Never restore over new writes
automatically. No staged-store promotion or rollback coordinator is needed.

Runner drain already separates claim cancellation from host execution and
joins hosts at [internal/runners/loop.go:226](../../../internal/runners/loop.go#L226). A pending claim racing drain is
accepted and joined there. Expose a graceful drain request instead of sending
SIGTERM to the root context, which currently cancels hosts at
[internal/cluster/main.go:39](../../../internal/cluster/main.go#L39). The existing updater waits for drain before
replacement at [internal/runners/managed.go:40](../../../internal/runners/managed.go#L40). Change local rolling
orchestration so the successor becomes ready before the old process exits.

Record runner instance PID/start identity and accepting/draining state in the
store. Activate the new instance and disable old claims in one transaction.
Already committed old claims must finish or release. Claim replay remains
allowed while draining. Keep a stable machine identity for placement and labels,
with distinct process instances for claims. New instances must be able to run
dependencies of old waiting hosts. Do not create a second resource budget.
Do not impose a new drain timeout that kills healthy jobs. Service registration
must let the old process finish, including on Windows. Today the Linux runner
unit uses SIGTERM and a 30-second stop timeout at
[internal/runners/service/service.go:298](../../../internal/runners/service/service.go#L298).
A normal service restart cannot implement this roll unchanged.

## Dashboard, CLI ownership and landing

The dashboard always binds `127.0.0.1:4343`, including idle startup. Retain an
explicitly configured loopback port. A bind failure must be visible without
stopping admission. Remove idle exit and remembered activation. Do not silently
pick another port because bookmarks and Serve need a stable address.

A verified socket peer mints a short-lived, single-use sign-in code. Store its
digest and exact target origin. Redemption creates a stored 30-day session.
Set a host-only cookie with `HttpOnly`, `SameSite=Strict`, `Path=/`, no Domain,
and a 30-day absolute expiry. Remove the shorter idle expiry. The plain
`127.0.0.1` HTTP cookie is non-Secure. Set Secure for the configured HTTPS Serve
origin. Logout revokes the row and expires the cookie. Restarts keep it valid.
The cookie never becomes a controller award credential.

Check exact Host, scheme and Origin before handlers. Cookie-authenticated
mutations require Origin, including logout. Keep fetch protections, JSON-only
writes and no permissive CORS. Refuse null or cross-site origins. Reconnecting
watches recheck session expiry and revocation. A navigation may serve the
signed-out shell. Keep only one redemption path. Remove browser fragment
bearers and in-memory session authority.

Korey chose plain `127.0.0.1`. Cookies are shared by host, not isolated by port.
Another service on that host can receive the cookie when visited. HttpOnly,
SameSite and Origin checks do not fix that limitation. The existing rationale
is at [internal/wingd/local_login.go:219](../../../internal/wingd/local_login.go#L219). Document the accepted port-sharing
limit. Do not add a random `.localhost` name or local certificate project.

Use Tailscale Serve to proxy HTTPS to loopback. Add exact machine authorities
in `dashboard.allowed_hosts`, for example `workstation.example.ts.net`.
Derive the corresponding allowed HTTPS origin. No wildcard tailnet allowlist.
The allowlist admits routing, not user authority. Keep the same sign-in code
and cookie flow at the Serve origin. Ignore forwarded identity headers for
authentication. Tailscale documents both its identity headers and the risk
from services on the same backend machine in its
[Serve guide](https://tailscale.com/docs/features/tailscale-serve#identity-headers).
Cut automatic tailnet identity login from this proposal. Cut `--allow-remote`
and direct non-loopback binds, as Korey chose.

Only the installed CLI starts or restarts the controller using the installed
binary. Pinned and older CLIs use the running controller. The separate
**installed-CLI daemon ownership lane**, already in progress, owns that change.
Its source boundaries are [internal/wingd/client/client.go:457](../../../internal/wingd/client/client.go#L457),
[internal/wingd/client/client.go:573](../../../internal/wingd/client/client.go#L573) and [internal/wingd/client/spawn.go:20](../../../internal/wingd/client/spawn.go#L20).
This proposal depends on that lane. It does not invent a second version election
or claim the lane is already in this checkout. Keep the matching engine for a
repo's SDK pin. Missing API capabilities get an actionable error, never a
fallback to an old controller or direct store access.

After landing, refresh the global CLI and runner from the exact pushed commit,
including any merge. Reuse the installer contract. Build in a clean snapshot,
verify the dashboard bundle, stage binaries and replace install targets safely.
The current land job ends after push and record at [.sparkwing/jobs/land.go:227](../../../.sparkwing/jobs/land.go#L227).
The installer builds CLI and runner at [bin/install.sh:80](../../../bin/install.sh#L80) and
[bin/install.sh:88](../../../bin/install.sh#L88). Add refresh after successful push. Invoke the newly
installed CLI by absolute path, outside repository engine selection.
It stops/starts the controller, then rolls the runner. The land job itself
continues in its old runner.

Refresh must return after the successor is ready and drain is requested.
Waiting for the old runner to exit would deadlock the land job that it is still
hosting. Old runner exits after that job's result and logs are acknowledged.
Registration refresh alone is not a roll. Serialize local installs and reject
a stale refresh that would overwrite a later installed revision. Record push,
install, controller readiness and runner roll outcomes separately. On refresh
failure, say “landed, local refresh failed” and print a retry command. Do not
push again. Dry runs, stop-at-verify and hosted CI do not install into a user's
global bin. This refresh is development delivery, separate from a public release.

## Windows

Keep the named-pipe transport already used behind logical socket paths.
[internal/wingd/socket_transport_windows.go:63](../../../internal/wingd/socket_transport_windows.go#L63) derives a private pipe name,
restricts its DACL to the owner SID and rejects remote clients. The client
verifies the server owner at line 272. Peer authentication uses impersonation at
[internal/wingd/peeridentity_windows.go:48](../../../internal/wingd/peeridentity_windows.go#L48). Client PID comes from
[internal/wingd/peerpid_windows.go:9](../../../internal/wingd/peerpid_windows.go#L9). HTTP requests can use that transport.
No loopback TCP control mode is needed. Adding one would require a new local
credential and distribution problem.

Persist PID plus `procgroup.ProcessBirth`. Windows already reads process
creation time with OpenProcess/GetProcessTimes at
[internal/procgroup/job_windows.go:21](../../../internal/procgroup/job_windows.go#L21). Recheck it to distinguish exit and PID
reuse. Treat access-denied inspection as unknown until expiry. Unix birth
support exists at [internal/procgroup/birth_unix.go:7](../../../internal/procgroup/birth_unix.go#L7). Keep process-tree
termination in runner-owned Windows Job Objects. Controller restart must not
close those handles. Test native pipe reconnect, SID rejection, process exit,
PID reuse and rolling service registration on Windows before claiming parity.

## Deletions, additions and wire changes

Counts are rough production lines before replacement, excluding tests. They
are not additive across overlapping edits. Keep policy and sampling code that
still has a job.

| Cut or replace | Rough lines | Keep or add |
| --- | ---: | --- |
| Framed connection handler `internal/wingd/conn.go` and admission sidecar `state.go` | 187 + 89 | HTTP routes and transactional store rows. |
| Connection routing, disconnect finalization, drain/reattach and delivery machinery in `internal/wingd/server.go` | 800-1,200 of 1,785 | Election, policy, sampling and status projection. |
| Framed client handshake, takeover, reattach and pushed lease watching | 600-1,000 across client files | Shared HTTP admission client with request keys and renewal. |
| Embedded store runner/host adapters | 95 + 95 | Existing runner Loop and remote HTTP host composition. |
| Embedded claimant credential logic and local direct store host | About 80 + 116 | Extract labels and port local cache/publication behavior. |
| Embedded queue composition and lifecycle | About 30 | Separate runner startup and roll. Controller maintenance stays. |
| Idle lifetime and remembered dashboard activation | 104 + 65 | Always-on binding and configured address. |
| In-memory browser bearer/session gate | About 150-200 of 293 | Stored one-time code, cookie session and request guards. |

Add one schema migration for machine tickets/leases, runner instance control,
claim replay receipts, local session rows, encrypted requester inputs and signing
metadata. Add focused store methods and shared HTTP routes. Expect roughly
1,000-1,800 production lines for durable admission and replay, 300-600 for
transport/runner lifecycle wiring and 200-400 for session changes. These are
planning estimates, not a code budget to fill. Reuse existing crypto, leases,
blob protocol and policy. Revisit the design if it needs a generic store RPC
layer, a second scheduler, a launcher supervisor hierarchy or a journal engine.

Proposed local HTTP surface, subject to the existing route policy checks:

| Operation | Route and retry identity |
| --- | --- |
| Request admission | `POST /api/v1/local/admission/requests`, keyed by authenticated subject plus request key. |
| Read ticket or lease | `GET /api/v1/local/admission/requests/{id}`. Returns queued, granted or terminal state and re-ask advice. |
| Renew | `POST /api/v1/local/admission/requests/{id}/renew`, with renewal sequence. |
| Cancel or release | `DELETE /api/v1/local/admission/requests/{id}`. Repeat returns the stored terminal outcome. |
| Queue and priority | Reuse the controller queue view. Add a socket-authenticated priority mutation over stored run admission rows. |
| Runner registration and roll | Socket-authenticated instance registration and activation/drain calls. Only installed CLI initiates replacement. |

Machine admission and lifecycle mutations require verified local peer authority.
They are not browser cookie routes. Execution reporting still requires the
award bearer. Share error envelopes and retry rules with the existing API.

Hard-cut the admission frame protocol once callers move together. Replace its
request/grant, queued pushes, liveness, release, cancellation, priority and
query messages with HTTP operations on the controller API socket. Retain
resource/queue data types where useful. Delete unused public wire types only
after checking their callers. Add keyed claim replay to the existing claim
wire and route retry metadata. Cut the one-time hosting-bearer response from
local intake when moving that owner to normal runner claims. Keep shared
award/log/result wire semantics. No legacy socket proxy or compatibility relay.

Customer-visible cuts are the old admission wire and caller compatibility,
the local intake response cut, removed dashboard lifecycle commands if approved
below, and the already chosen remote-bind removal. Regenerate API snapshots, generated CLI/config docs and
source-doc drift checks when implementation lands. Update `ADMISSION.md` and
ORIENTATION's owners/guards in the implementing change. This design commit
changes neither product behavior nor those source contracts.

Cut from scope: descriptor transfer, handover generations, store promotion,
retired controllers, process reparenting, queue export/import, reattach tokens,
a second local log API, automatic Tailscale identity sign-in, local TLS setup,
new scheduler limits and a cross-tool resource broker. Keep one store, one
controller API and the existing runner loop.

## Implementation order

S means half a day. M means one to two days. L means three to five days.
Sizes include focused tests, not release coordination. Several L items mean
this is a multi-week change, even after deleting handover.

1. **Installed-CLI ownership, M, separate lane.** Remove caller-driven controller
   replacement first. Preserve SDK engine pinning. Establish the source of the
   installed executable and actionable capability failures.
2. **Durable admission and authority, L.** Store ticket/lease transitions,
   ordering, PID/start identity, cancellations, replay and local secret/signing
   material. Add HTTP APIs and migrate nodehost, queue and capacity callers.
   Preserve D15 guard behavior. Decide the customer cuts before deleting wire.
3. **Retryable claim and intake, M.** Add claim receipts and recoverable award
   authority. Move local here intake to queued runner ownership and replayable
   observer data. Extend local reconnect for body reads, uploads and safe/keyed
   operations. Verify commit-before-reply failures.
4. **Common local runner execution, L.** Move local owner and job execution
   through the existing remote composition. Port local source/cache/publication
   behavior. Remove all direct execution-host store bindings and embedded runner.
   Keep controller maintenance and existing engine selection.
5. **Restart and runner roll, M.** Backup before migration, store runner-instance
   claim eligibility, expose drain without root cancellation, and handle service
   manager lifetime. Exercise Unix and native Windows paths. Add another M if
   Windows service registration needs a separate rolling implementation.
6. **Always-on dashboard and Serve, M.** Stored 30-day cookies, exact request
   guards, allowed hosts and surface cuts. Keep the chosen loopback cookie limit.
7. **Land refresh and removal sweep, M.** Install from pushed revision, restart,
   roll without waiting for the land host, and record partial failures. Remove
   dead frame code and update generated docs and drift checks together.

Enable automatic controller restart only after steps 2-5 are proven together.
The first upgrade from today's embedded execution cannot preserve a job that
still belongs to the old controller. Bootstrap at a naturally idle boundary
and explain this one-time limit. Do not manufacture idleness by draining the
machine or advertise seamless restart for an unconverted predecessor.

Acceptance needs real processes and interrupted responses: FIFO waiters,
declared cores, shared process roots, a logging job, local secrets, a schedule,
a committed claim with a lost reply, a release with a lost reply, cancellation,
terminal replay and migration failure. Restart the controller twice while old
jobs run. Roll a runner while its owner awaits a child. Land from the old
runner and prove refresh returns before that runner exits. Assert one store
writer, unchanged live attempts, preserved order, no duplicate grants, correct
log offsets and no controller-caused job termination. Test cookie persistence,
expiry, logout, hostile Host/Origin and real Serve routing. Retain native
Windows pipe/process/service tests. Use the repository delivery gates for the
implementation.

This proposal was checked by source reading and citation/path checks only.
No Sparkwing binary was built or run. No store, service or pipeline was opened.
The lead runs the full gate after this session.

## Open questions for Korey

The dashboard lifetime, 30-day cookie, plain loopback hostname, Serve allowlist,
remote-bind cut and installed-CLI ownership are already chosen. They need no
second approval. These remaining customer-visible changes need OK before
implementation ships.

1. **Old admission clients and first conversion.** Hard cuts simplify the API,
   but public wingwire consumers and a pinned engine containing the old host
   can break. Recommendation: cut the frame wire in the coordinated pre-launch
   conversion, update first-party callers together and document the one idle
   bootstrap. Include removal of local intake's one-time hosting bearer. Keep SDK
   `Cores()` and matching-engine behavior. If supported pins still embed the old
   admission host, delay wire removal until compatible hosting is available
   or Korey approves an explicit pre-launch cutoff. Never silently drop admission. This is an explicit exception to the
   normal upgrade-compatibility promise, not permission to break it after launch.
2. **Requester secrets on disk.** Restart-safe secrets need encrypted run-scoped
   storage, including encrypted backups. Memory-only secrets cannot satisfy the
   chosen model after controller death. Recommendation: use the existing local
   keyring, restrict reads to award authority and delete active rows on terminal
   cleanup. Document backup retention. Do not add resupply or handover machinery.
3. **Expiry and command cuts.** Renewing every 10 seconds needs a hard failure
   boundary. Recommendation: start with a 60-second machine lease and ticket
   liveness window, keep existing three-minute award leases, and impose no new
   job-duration or runner-drain cap. Measure restart/migration time before
   fixing the machine window. Replace independent dashboard start/stop with
   controller lifecycle commands, retaining open/status/logs. This command cut
   goes beyond always-on binding and needs OK. For comparison,
   [GitHub Actions](https://docs.github.com/en/actions/reference/runners/self-hosted-runners#routing-precedence-for-self-hosted-runners)
   requeues unaccepted assignments after 60 seconds and expires queued jobs
   after 24 hours.
   [CircleCI](https://circleci.com/docs/runner/machine-runner-3-configuration-reference/)
   exposes `max_run_time` and TERM-based drain. Those are different boundaries,
   not defaults to copy into local admission. No new queue-duration cap is
   proposed here.
