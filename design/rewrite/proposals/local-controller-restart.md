# Local controller restart without handover

Design proposal.

This design uses one local controller and durable state so jobs can outlive a
controller process. It supersedes earlier proposals where they differ.
The controller is today's wingd without job execution. It alone writes
`state.db`. It serves the existing controller API and machine admission through HTTP on the local API socket.
CLI and runner clients use that address. Long polls wake queued tickets and
watch leases. A poll waits about 20 seconds before the client asks again.
The browser uses a loopback HTTP dashboard on the same controller. A socket
connection owns no queue position, grant or job.

Upgrade means stop the controller, start the new installed binary, back up the
store, migrate on open, then serve. The runner loop keeps polling across
transport errors. There is no handover protocol, transferred listener, retired
controller or proxy chain. Runner processes keep executing while the controller restarts.

D15 remains fixed: preserve FIFO under the configured admission policy, count
job process trees as Sparkwing load, and honor SDK `Cores()`. The owners and
guards are the [queue ordering row](../ORIENTATION.md#L132) and the
[declared cores and process-tree load row](../ORIENTATION.md#L133) in
`design/rewrite/ORIENTATION.md`, read at commit `effbeb87d05b`.
The reported November incident motivates the change. This note does not claim
to reproduce it.

The store holds authority, scheduling facts and acknowledged progress. Its
existing managed blob storage holds log and artifact bytes. Engine caches,
OS process handles and disposable measurements do not become SQLite rows.
A runner still supervises its processes and pipes. Its upgrade lets that
process finish. A runner crash can lose execution and unacknowledged output.
A controller restart must not.

SDKs declare and relay only. Policy and security behavior live in the host
behind the versioned protocol. This project makes no Go or TypeScript SDK
change and requires no SDK release. SDK thinning and compatibility audits are
separate work.

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
| Dashboard lifetime | An ordinary start serves a remembered dashboard or only the API socket. Dashboard binding is conditional. The controller remains running while idle until explicitly stopped. A remembered remote address restores as loopback, the behavior called D8 here. See [internal/wingd/local_runtime.go:139](../../../internal/wingd/local_runtime.go#L139), [internal/wingd/local_runtime.go:251](../../../internal/wingd/local_runtime.go#L251), [internal/wingd/local_runtime_test.go:312](../../../internal/wingd/local_runtime_test.go#L312), and [internal/wingd/local_dashboard_memory.go:42](../../../internal/wingd/local_dashboard_memory.go#L42). |
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
| Nodehost machine admission | [internal/nodehost/machine_admission.go:21](../../../internal/nodehost/machine_admission.go#L21), [internal/nodehost/local_admission.go:55](../../../internal/nodehost/local_admission.go#L55), [internal/nodehost/local_admission.go:91](../../../internal/nodehost/local_admission.go#L91) | Replace Acquire/Watch/Release on a held connection with HTTP request, ticket long poll, lease watch and explicit release. Preserve skip, fail, supersede and cancellation outcomes. |
| Local CLI execution hosts | [cmd/sparkwing/local_run_host.go:98](../../../cmd/sparkwing/local_run_host.go#L98), [cmd/sparkwing/local_host.go:55](../../../cmd/sparkwing/local_host.go#L55) | Move OwnerHost and job hosting into the local runner. CLI submits and observes through HTTP, including foreground cancellation. No direct store fallback. |
| Native runner executing a remote controller's work here | [internal/cluster/production_runtime.go:157](../../../internal/cluster/production_runtime.go#L157) | Use the same local HTTP admission client when machine admission is enabled. Keep remote-controller participant namespacing. Remote execution still talks to its configured controller. |
| Runner capacity preflight | [internal/cluster/native_claim_capacity.go:18](../../../internal/cluster/native_claim_capacity.go#L18), [internal/cluster/production_runtime.go:54](../../../internal/cluster/production_runtime.go#L54) | Read the local HTTP capacity view. It remains advisory. Only the admission transaction grants capacity. |
| Queue priority CLI | [cmd/sparkwing/queue_priority.go:64](../../../cmd/sparkwing/queue_priority.go#L64) | Replace wingd Query and SetPriority with controller HTTP calls. Persist priority overrides. |
| Queue CLI and dashboard bridge | [cmd/sparkwing/queue.go:40](../../../cmd/sparkwing/queue.go#L40), [internal/controller/queue_view.go:34](../../../internal/controller/queue_view.go#L34), [cmd/sparkwing/local_services.go:177](../../../cmd/sparkwing/local_services.go#L177) | CLI already uses controller HTTP. Remove the controller's socket call back into itself. Query stored machine admission alongside logical admission. |
| Local status/control clients | [internal/wingd/client/local_control.go:39](../../../internal/wingd/client/local_control.go#L39) | Move status/control onto the API socket. Installed CLI owns restart. Browser requests cannot replace the executable. |

Bitwing's gate slots are separate. The source at
Bitwing `.sparkwing/jobs/pre_push.go:331` uses
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
Xwing `internal/toolruntime/runtime.go:651` and
`internal/toolruntime/dashboard.go:423`. Update that
private lifecycle contract for a controller plus runner. Overwing invokes the
normal pipeline CLI at
Overwing `internal/textlint/runner.go:60`.

### Stored ledger, requests and leases

Keep the existing ledger engine and its policy algorithms in
[internal/admission/ledger.go:1438](../../../internal/admission/ledger.go#L1438).
Persist its full state in the store on every change, including waiters.
Restore that state before serving admission. A grant or queue change becomes
visible only after persistence. Replace the sidecar writer that drops waiters.
Do not split the ledger into normalized request, ticket and lease rows.

Logical reservations, permits and execution awards keep their existing owners.
The logical contract remains in
[design/rewrite/ADMISSION.md:3](../ADMISSION.md#L3). Machine admission uses the
stored ledger. These are the two admission layers.

A request carries participant/run/job identity, resources, class, priority,
semaphores, queue policy and deadline, and the local holder PID plus process
start time. Keep arrival counters, tokens, priorities, members, parent
relationships and semaphore reservations in the full persisted ledger state.
A queued response identifies the ticket and its original arrival sequence.
Long-poll ticket status for about 20 seconds. Wake the poll when the ticket can
proceed or becomes terminal. Polling and reconnecting keep the same arrival.
Preserve existing absolute queue deadlines.

A grant identifies the lease and its authority. Holder liveness is PID plus
process start time. Machine leases have no renewal, expiry or ticket liveness
window. HTTP disconnect does not release capacity. Explicit release and ticket
cancel remain idempotent. A lease watch long poll delivers eviction or cancel
immediately. Its roughly 20-second timeout is a transport wait, not a lease
lifetime.

Verify PID ownership from the socket peer. Record `procgroup.ProcessBirth`
with the PID and validate it against the OS. Register execution roots only
after checking their identity and ownership against the authenticated peer.
Shared runner transports must keep each job's roots distinct. Recheck holders
on restart and during admission scans. Confirmed exit or PID reuse frees the
holder's reservation on observation. Inspection errors leave liveness unknown
and retain the reservation until identity can be checked.

Preserve D15's ordering within priority and class. Preserve the guarded
backfill rule that cannot delay the head. Re-asking never moves a ticket back.
Attribute CPU above a job's requested charge to its process tree. Deduplicate
shared roots. Current ownership logic is at
[internal/wingd/headroom.go:255](../../../internal/wingd/headroom.go#L255) and
[internal/wingd/headroom.go:293](../../../internal/wingd/headroom.go#L293).
Rebuild sampling from durable holder roots. Retain reservations while CPU
measurements warm up. Missing samples must not cause a burst of grants.
The controller's sampler also supplies live per-job CPU and memory to the
dashboard. This belongs in implementation step 1.

Keep roots registered and measured until confirmed exit, including while a
cancelled tree stops. Execution awards retain their existing fences and lease
rules. They are separate from PID-based machine admission.

## State that must survive

| State today | Current owner and evidence | New home |
| --- | --- | --- |
| Machine leases, tokens, arrivals, priorities, waiters and semaphore reservations | [internal/admission/snapshot.go:8](../../../internal/admission/snapshot.go#L8), [internal/wingd/state.go:25](../../../internal/wingd/state.go#L25) | Persist the existing ledger's full state, including waiters, on every change. Restore it before admission. Delete sidecar persistence. |
| Connection role, participant/run/member/parent mappings, requested and charged resources, queue start/deadline, PID, liveness nonce and sampling counters | [internal/wingd/conn.go:38](../../../internal/wingd/conn.go#L38), [internal/wingd/server.go:60](../../../internal/wingd/server.go#L60) | Persist authority, queue deadlines and holder PID/start identity with the full ledger state. Persist useful history. Derive peer identity for each request. Drop connection roles, nonces and pushed-delivery bookkeeping. |
| Pending cancellations, cancelled-run cache, disconnected and reattach maps | [internal/wingd/server.go:65](../../../internal/wingd/server.go#L65) | Store cancellation and terminal outcomes. Ticket and lease watch long polls return them. Delete reattach state. Closing HTTP never releases capacity or finalizes a run, unlike [internal/wingd/server.go:1520](../../../internal/wingd/server.go#L1520). |
| Headroom samples, process CPU baselines, smoothing, attribution, measured PID set and host/container totals | [internal/wingd/server.go:82](../../../internal/wingd/server.go#L82), [internal/wingd/sampler.go:98](../../../internal/wingd/sampler.go#L98) | Rebuild from OS measurements plus stored holder roots and effective policy. Cache samples only. Keep grants conservative during warmup. |
| Recent admission events and journal buffers | [internal/wingd/events.go:35](../../../internal/wingd/events.go#L35), [internal/wingd/journal/journal.go:38](../../../internal/wingd/journal/journal.go#L38) | Store user-visible admission history with transitions. Diagnostic journal buffers remain best effort. They grant no authority. |
| Election lock, listeners, accepted sockets, timers, channels, shutdown flags, cgroup handles and readiness/activity counts | [internal/wingd/server.go:33](../../../internal/wingd/server.go#L33), [internal/wingd/local_runtime.go:34](../../../internal/wingd/local_runtime.go#L34) | Recreate handles. Derive activity from store. Reapply effective policy and limits. Delete idle shutdown and stay bookkeeping. |
| Requester local secret values in `sync.Map` | [internal/store/local_secrets.go:37](../../../internal/store/local_secrets.go#L37), [internal/store/local_secrets.go:70](../../../internal/store/local_secrets.go#L70), [internal/store/store.go:305](../../../internal/store/store.go#L305) | Encrypt run-scoped values in store before intake acknowledgement. Delete at run end. The existing keyring supplies encryption so secrets survive restart without plaintext storage. |
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
| Machine labels and local features | [internal/controller/engine_claim.go:82](../../../internal/controller/engine_claim.go#L82) applies the local claim profile. [cmd/sparkwing/local_host.go:71](../../../cmd/sparkwing/local_host.go#L71) and [cmd/sparkwing/local_host.go:92](../../../cmd/sparkwing/local_host.go#L92) add local cache/publication behavior. | Extract labels from the claimant being deleted. Port local workspace/source, cache, publication and foreground cancellation behavior into the common runner path with tests. Preserve engine pin selection. |
| Logs and artifacts | [internal/controller/client/native_logs.go:117](../../../internal/controller/client/native_logs.go#L117) uploads signed segments. [internal/controller/client/native_logs.go:179](../../../internal/controller/client/native_logs.go#L179) commits replayable manifests. PUT uses the injected HTTP transport at line 102. | Persist the local signing key. Retry interrupted PUTs and commits. Batch output using this shared segmented protocol. It is not currently a raw byte-append endpoint. Cut a second local log protocol from scope. |
| Reconnect | Ordinary claim transport errors end the loop at [internal/runners/loop.go:317](../../../internal/runners/loop.go#L317). | The runner loop keeps polling across transport errors. |
| Renew and result | [internal/lease/keeper.go:81](../../../internal/lease/keeper.go#L81) tolerates transient renew errors. [internal/limits/claim.go:7](../../../internal/limits/claim.go#L7) gives existing awards a three-minute lease. Terminal routes have exact-body retry metadata at [internal/capability/routes.go:17](../../../internal/capability/routes.go#L17). | Keep existing execution-award lease and result semantics. Machine admission uses PID liveness and the lease watch for cancel. |

Keep the existing claim and intake contracts. Claim receipts, intake replay
and changes to the one-time hosting bearer are outside this project.

## Ownership audit

- The runner reports the exit time it observed. A delayed report after a
  controller restart must not replace that time with controller receipt time.
- Cancel and eviction reach the runner immediately through the lease watch
  long poll. Add a measured test from cancellation to process stop. Do not wait
  for the next periodic poll or award renewal.
- The controller kills orphaned job process groups when their runner is gone.
  Store runner and job-root PID/start identities. Verify those identities
  before cleanup so PID reuse cannot target another process group. Test runner
  death, orphan cleanup and controller restart during cleanup.
- Port local-only host features to the common runner path with tests. Cover
  workspace/source handling, cache, publication, labels, engine selection and
  foreground cancellation before removing the old local host bindings.

## Restart, runner roll and interrupted requests

| Event | Queue and grants | Running jobs and progress |
| --- | --- | --- |
| Controller stops and restarts | The full ledger state keeps arrivals, priorities, queue deadlines, waiters and leases. Clients reconnect and resume their watches. Live PID/start identities retain machine admission. | Runner processes keep SDK pipes and children alive. They use the existing award, log and result protocols when service returns. No re-launch or attempt increment. |
| Controller dies before an admission change commits | The stored ledger retains its previous state. No uncommitted grant becomes visible. | The runner loop keeps polling across transport errors. |
| Controller commits an admission change before the response is lost | The restored ledger includes that change. Ticket and lease status come from that state. | Existing log/result retry semantics stay in place. No new claim or intake replay is added. |
| Controller outage outlasts an execution award's existing lease | Machine admission has no expiry. Holder PID/start identity still decides its liveness. | The existing award keeper stops work when award authority expires. This project adds no machine lease timer. |
| Requester crashes while queued or holding | Confirmed PID exit or reuse reclaims the reservation. A lost release reply can be repeated. | When a runner dies, the controller cleans up its orphaned job process groups. Persisted results and logs remain. |
| Runner upgrades | The controller starts a successor and drains the old instance. Both use the same machine ledger. Old reservations remain charged until release or confirmed holder exit. | The old instance finishes hosts, uploads and terminal acknowledgements, then exits. The successor takes new work. Process handles and active awards stay with their original runner. |
| Old runner crashes during a roll | The successor does not steal live old awards. The controller confirms death by PID/start identity and cleans up orphaned job groups. | Existing lost-award recovery, fences and retry policy apply. Arbitrary job side effects are not exactly once. |

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
replacement at [internal/runners/managed.go:40](../../../internal/runners/managed.go#L40). The controller starts and rolls the local runner.
It launches the runner detached and records it in the store. The successor
becomes ready before the old process exits. On controller restart, check the
recorded runner identity and reuse a live runner.

Record runner instance PID/start identity and accepting/draining state in the
store. Activate the new instance and disable old claims in one transaction.
Already committed old claims must finish or release. Keep a stable machine
identity for placement and labels. Use distinct process instances for claims.
New instances must be able to run dependencies of old waiting hosts. Do not create a second resource budget.
Do not impose a new drain timeout that kills healthy jobs. A roll sends the
application drain request. It stops old claims and waits for hosts, uploads and
terminal acknowledgements before the old runner exits. Keep its service
registration active until then, including on Windows. Today the Linux runner
unit uses SIGTERM and a 30-second stop timeout at
[internal/runners/service/service.go:298](../../../internal/runners/service/service.go#L298).
A normal service restart cannot implement this roll unchanged.

Detached startup must also escape the controller's service lifetime:

- **Linux:** a detached child stays in the controller unit's cgroup. The default
  [`KillMode=control-group`](https://www.freedesktop.org/software/systemd/man/latest/systemd.kill.html)
  kills it on stop. Start each runner instance as its own transient user service
  with [`systemd-run --user`](https://www.freedesktop.org/software/systemd/man/latest/systemd-run.html).
  This keeps service cleanup without sharing the controller's cgroup. Do not
  add stop dependencies from runner units to the controller. Detect systemd
  boot with [`sd_booted`](https://www.freedesktop.org/software/systemd/man/latest/sd_booted.html) and probe the user manager. A missing user manager on a
  systemd host is a setup error. Only a confirmed no-systemd host uses `setsid`,
  independent stdio and no parent-death signal. Its supervisor must stop only
  the controller, leaving the runner's session alive.
- **macOS:** launch each runner instance as a separate
  [launchd agent](https://github.com/apple-oss-distributions/launchd/blob/main/man/launchd.plist.5).
  It has its own process group because launchd cleans up a stopped agent's
  group. Stopping the controller agent must not unload runner agents.
- **Windows:** start each runner instance through SCM as a separate service,
  outside any controller-owned Job Object. Controller stop must not stop runner
  services or close their handles. The runner owns its job-tree handles because
  [`KILL_ON_JOB_CLOSE`](https://learn.microsoft.com/en-us/windows/win32/procthread/job-objects)
  can kill every process in the object. A roll drains through the application
  API before stopping the old service. An explicit runner service stop drains
  on a worker thread, reports `STOP_PENDING` with
  [checkpoints and wait hints](https://learn.microsoft.com/en-us/windows/win32/api/winsvc/ns-winsvc-service_status),
  then reports `STOPPED` after acknowledgements. Its
  [control handler](https://learn.microsoft.com/en-us/windows/win32/services/service-control-handler-function)
  returns within 30 seconds. Controller restart never sends that stop request.

## Dashboard, CLI ownership and landing

The dashboard always binds `127.0.0.1:4343`, including idle startup. Retain an
explicitly configured loopback port. A bind failure must be visible without
stopping admission. Remove idle exit and remembered activation. Do not silently
pick another port because bookmarks and Serve need a stable address.

Cut `sparkwing dashboard start` and `sparkwing dashboard stop`. Keep dashboard
open/status/logs. Controller lifecycle belongs under `sparkwing daemon`.

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

Use plain `127.0.0.1` because it keeps a stable local address without local TLS
setup. Cookies are shared by host, not isolated by port.
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
and direct non-loopback binds because Serve supplies the HTTPS entry point.

Only the installed CLI starts or restarts the controller using the installed
binary. Compatible pinned CLIs use the running controller. Old incompatible pins
are a hard cut. Bump all pins and mark the change Breaking. There is no adapter
for engines that still embed the old admission host. A separate installed-CLI
ownership change must remove pinned-engine takeover.
Its source boundaries are [internal/wingd/client/client.go:457](../../../internal/wingd/client/client.go#L457),
[internal/wingd/client/client.go:573](../../../internal/wingd/client/client.go#L573) and [internal/wingd/client/spawn.go:20](../../../internal/wingd/client/spawn.go#L20).
This proposal depends on that change. It adds no second version election.
Keep the matching engine for a repo's SDK pin. Missing API capabilities get an actionable error, never a
fallback to an old controller or direct store access.

After landing, refresh the global CLI and runner from the exact pushed commit,
including any merge. Reuse the installer contract. Build in a clean snapshot,
verify the dashboard bundle, stage binaries and replace install targets safely.
The current land job ends after push and record at [.sparkwing/jobs/land.go:227](../../../.sparkwing/jobs/land.go#L227).
The installer builds CLI and runner at [bin/install.sh:80](../../../bin/install.sh#L80) and
[bin/install.sh:88](../../../bin/install.sh#L88). Add refresh after successful push. Invoke the newly
installed CLI by absolute path, outside repository engine selection.
It stops/starts the controller. The controller rolls the local runner.
The land job itself continues in its old runner.

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
reuse. Treat access-denied inspection as unknown and retain the reservation
until identity can be checked. Unix birth support exists at
[internal/procgroup/birth_unix.go:7](../../../internal/procgroup/birth_unix.go#L7).
Keep normal process-tree termination in runner-owned Windows Job Objects.
Controller restart must not close those handles. The controller also needs
a tested orphan cleanup path after confirmed runner death. Test native pipe
reconnect, SID rejection, process exit, PID reuse and rolling service
registration on Windows before claiming parity.

## Deletions, additions and wire changes

Counts are rough production lines before replacement, excluding tests. They
are not additive across overlapping edits. Keep policy and sampling code that
still has a job.

| Cut or replace | Rough lines | Keep or add |
| --- | ---: | --- |
| Framed connection handler `internal/wingd/conn.go` and admission sidecar `state.go` | 187 + 89 | HTTP routes and full ledger-state persistence in the store. |
| Connection routing, disconnect finalization, drain/reattach and delivery machinery in `internal/wingd/server.go` | 800-1,200 of 1,785 | Election, policy, sampling and status projection. |
| Framed client handshake, takeover, reattach and pushed lease watching | 600-1,000 across client files | Shared HTTP admission client with ticket long poll and lease watch. |
| Embedded store runner/host adapters | 95 + 95 | Existing runner Loop and remote HTTP host composition. |
| Embedded claimant credential logic and local direct store host | About 80 + 116 | Extract labels and port local cache/publication behavior. |
| Embedded queue composition and lifecycle | About 30 | Controller-managed detached runner startup and roll. Controller maintenance stays. |
| Idle lifetime and remembered dashboard activation | 104 + 65 | Always-on binding and configured address. |
| In-memory browser bearer/session gate | About 150-200 of 293 | Stored one-time code, cookie session and request guards. |

Add storage for the full ledger state, runner instance control, local sessions,
encrypted requester inputs and signing metadata. Keep the ledger engine.
Remove normalized admission rows, claim receipts and intake replay from the
estimate. Allow roughly 600-1,000 production lines for ledger persistence,
HTTP admission, lease watch and live metrics. Allow 300-600 for runner lifecycle
wiring and 200-400 for sessions. Secrets and ownership tests also need work.
These are planning estimates, not a code budget to fill. Reuse existing crypto,
blob protocol and policy. Revisit the design if it needs a generic store RPC
layer, a second scheduler, a launcher supervisor hierarchy or a journal engine.

Proposed local HTTP surface, subject to the existing route policy checks:

| Operation | Route and behavior |
| --- | --- |
| Request admission | `POST /api/v1/local/admission/requests`. Carries authenticated holder identity and the ledger request identity. |
| Read ticket or lease | `GET /api/v1/local/admission/requests/{id}`. Long-polls for about 20 seconds. Returns queued, granted or terminal state without changing arrival. |
| Watch lease | `GET /api/v1/local/admission/requests/{id}/watch`. Long-polls for eviction or cancel and wakes immediately on change. |
| Cancel or release | `DELETE /api/v1/local/admission/requests/{id}`. Repeat returns the stored terminal outcome. |
| Queue and priority | Reuse the controller queue view. Add a socket-authenticated priority mutation through the persisted ledger. |
| Runner registration and roll | Socket-authenticated instance registration and activation/drain calls. The controller starts and rolls the detached local runner. The installed CLI owns controller replacement. |

Machine admission and lifecycle mutations require verified local peer authority.
They are not browser cookie routes. Execution reporting still requires the
award bearer. Share error envelopes and retry rules with the existing API.

Hard-cut the admission frame protocol once callers move together. Replace its
request/grant, queued pushes, liveness, release, cancellation, priority and
query messages with HTTP operations on the controller API socket. Retain
resource/queue data types where useful. Delete unused public wire types only
after checking their callers. Keep the existing claim, intake, one-time hosting
bearer and award/log/result wire semantics. No legacy socket proxy or
compatibility relay.

Customer-visible cuts are the old admission wire and caller compatibility,
old incompatible pins, removed dashboard start/stop commands and the
already chosen remote-bind removal. Bump all pins and mark the cuts Breaking.
Regenerate API snapshots, generated CLI/config docs and source-doc drift checks
when implementation lands. Update `ADMISSION.md` and
ORIENTATION's owners/guards in the implementing change. This design commit
changes neither product behavior nor those source contracts.

Cut from scope: descriptor transfer, handover generations, store promotion,
retired controllers, process reparenting, queue export/import, reattach tokens,
a second local log API, automatic Tailscale identity sign-in, local TLS setup,
new scheduler limits and a cross-tool resource broker. Keep one store, one
controller API and the existing runner loop.

## Implementation order

S means half a day. M means one to two days. L means three to five days.
Sizes include focused tests, not release coordination. The six steps total
roughly 10-18 working days. Installed-CLI ownership remains a separate change.

1. **Durable machine admission over HTTP, L.** Persist the existing ledger's
   full state on every change, including waiters. Add ticket long poll and PID
   plus start-time liveness. Migrate nodehost, queue and capacity callers.
   Preserve D15. Add live per-job CPU/memory from the controller's sampler to
   the dashboard. Deliver instant cancel through the lease watch and measure it.
2. **Encrypted secrets and stored signing key, M.** Use the existing keyring.
   Delete run-scoped secrets at run end. Preserve object URLs across restart.
   Test restart recovery and document encrypted backup retention.
3. **Local jobs through the runner path, L.** The controller starts and rolls
   the detached runner and records its identity in the store. Move local owner
   and job execution to the common host composition. Port local-only features
   with tests. Keep the runner loop polling across transport errors. Report
   observed exit time and test controller cleanup of orphaned process groups.
   Remove direct execution-host store bindings and the embedded runner.
4. **Backup before migration and daemon restart, M.** Back up before opening
   for migration. Test restart recovery with live runners and persisted waiters.
   Verify graceful runner roll and native Windows process/pipe behavior.
5. **Always-on dashboard, cookies, Serve and cuts, M.** Store 30-day sessions.
   Keep exact Host/Origin checks and allowed hosts. Cut dashboard start/stop and
   remote binds. Lifecycle lives under `sparkwing daemon`.
6. **Land refresh, pin bumps and release, M.** Refresh from the pushed revision.
   Restart the controller and let it roll the runner without waiting for the
   land host to exit. Hard-cut incompatible old pins and bump all pins. Mark
   Breaking. Remove dead frame code and update generated docs and drift checks.
   Follow the separate release policy and its approval gate.

Enable automatic controller restart only after steps 1-4 are proven together.
The first upgrade from embedded execution cannot preserve a job that still
belongs to the old controller. Bootstrap at a naturally idle boundary and
explain this one-time limit. Do not drain the machine to manufacture idleness.

Acceptance needs real processes and interrupted responses. Cover FIFO waiters,
declared cores, shared process roots, live per-job CPU/memory, a logging job,
local secrets, a schedule, release with a lost reply, immediate cancellation,
runner exit-time reporting, orphan cleanup and migration failure. Measure
cancel-to-stop latency through the lease watch. Test polling through transport
errors without adding claim or intake replay. Restart the controller twice
while old jobs run. Stop and restart the controller through systemd, launchd
and SCM with a logging child job alive. Verify distinct cgroups, process groups
or Job Objects and unchanged runner/job identities. Cover the no-systemd
session path. On Windows, test application drain and explicit service stop
through terminal acknowledgement. Roll a runner while its owner awaits a child.
Land from the old runner and prove refresh returns before it exits. Assert one store writer,
unchanged live attempts, preserved order, no duplicate machine grants, correct
log offsets and no controller-caused job termination. Test cookie persistence,
expiry, logout, hostile Host/Origin and real Serve routing. Retain native
Windows pipe/process/service tests. Use repository delivery gates for the
implementation.

Validation here covers source references and design consistency. Runtime
acceptance checks belong to the implementing changes.

## Decisions

These decisions close the earlier open questions. The dashboard uses a 30-day
cookie to preserve sign-in across restarts. Its stable loopback address and
exact Serve allowlist keep routing explicit. The installed CLI owns controller
replacement so repository pins cannot replace the shared service.

| Decision | Date | Agreed behavior |
| --- | --- | --- |
| Q1: admission owners | 2026-11-05 | Keep two admission layers because logical placement and machine capacity have different owners. Machine admission lives in the local store. |
| Q2: waiting | 2026-11-05 | Keep the roughly 20-second long poll because it bounds transport waits. Wake queued tickets when they can proceed. |
| Q3: durability | 2026-11-05 | Keep the existing ledger engine. Persist its full state, including waiters, in the store on every change. Keep it whole because the ledger already owns policy and ordering. |
| Q4: reconnect scope | 2026-11-05 | The runner loop keeps polling across transport errors. Keep existing claim and result semantics because restart needs transport recovery, not a new replay protocol. |
| Q5: local secrets | 2026-11-05 | Encrypt run-scoped secrets in the store with the existing keyring. Delete them at run end. Document encrypted backup retention. Encryption preserves restart recovery without plaintext storage. |
| Q6: machine lease liveness | 2026-11-05 | Use holder PID plus start time. No renewal, expiry or ticket liveness window. Eviction and cancel use the lease watch long poll. Process identity survives transport loss without freeing live capacity. |
| Q7: local runner | 2026-11-05 | The controller starts and rolls the detached local runner. Record its identity and lifecycle state in the store so a successor controller can reuse it. |
| Q8: compatibility | 2026-11-05 | Hard-cut old incompatible pins because they still embed the old admission host. Bump all pins. Mark Breaking. |
| Q9: dashboard lifecycle | 2026-11-05 | Cut dashboard start/stop because the dashboard shares the always-on controller. Lifecycle belongs under `sparkwing daemon`. |
| Ownership audit | 2026-11-05 | The runner reports observed exit time. Measure instant cancel via the lease watch. The controller cleans up orphaned job process groups. Port local-only host features with tests so moving execution preserves their behavior. |
| Live job metrics | 2026-11-05 | Show per-job CPU and memory from the controller's sampler on the dashboard in step 1 so users can see the load admission measures. |
| SDK scope | 2026-11-05 | SDKs declare and relay only. Policy and security behavior live in the host behind the versioned protocol. This project changes neither Go nor TypeScript SDK. Host ownership avoids SDK releases for policy changes. SDK thinning and compatibility audits are separate work. |
| Delivery order | 2026-11-05 | Follow the six implementation steps above. Release remains separate from engineering completion because it has its own checks and approval gate. |
