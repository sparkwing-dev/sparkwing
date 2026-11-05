# Daemon adopt and an always-on dashboard

Design proposal, 2026-11-05. No implementation in this change.

Korey's decisions are fixed: a newer daemon adopts live work, the dashboard
always serves on loopback, browsers keep a 30-day local session, and Tailscale
Serve provides remote access. D15 rules out draining the machine to upgrade
its admission controller. The reported 2026-11-04/05 incident is the motivation,
not a reproduction performed for this note.

## What the code does today

References describe the checkout at `70795d142`. Paths start at the repository
root. Everything after this section is proposed behavior unless labelled otherwise.

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

## Adopt, with one active owner

Keep the old process supervising its existing executions. Move admission and
the controller to the successor. Do not reparent jobs or reconstruct running
Go goroutines from PIDs. A retired process has no store handle, claim loop,
scheduler, reaper, or admission ledger. It keeps only execution hosts and the
connections it must relay until their clients finish.

This needs one prerequisite. All local execution hosts must use the existing
award-bound controller API for store reads, renewal, delegation, logs, and
settlement. Remove their direct store bindings before advertising adopt support.
Their API endpoint stays stable through upgrades. The predecessor can then
close its store without closing hosts or cancelling execution contexts.
This is the largest change. A general proxy for every store method would cost
more and preserve the wrong boundary.

Keep each log writer's sequence and last acknowledged offset in its original
host. Retry an unacknowledged tail or terminal report against the successor
with the same identity. The supervisor stays alive until both are acknowledged.

Add one versioned adopt capability and one private handover channel. The running
daemon spawns the candidate from the resolved executable. Use an inherited Unix
socketpair for the transfer and authenticate the initiating control-socket peer
as the daemon's OS account. Include the expected predecessor build and a unique
handover generation. Only one attempt may prepare at a time. The candidate
reports its actual build, supported wire operations, and schema range. A
caller-supplied version string alone cannot establish compatibility.

Released versions advance by semantic version. Equal builds reuse the running
daemon. An older binary must refuse replacement and use the running one through
its negotiated protocol. An unsupported operation fails with upgrade guidance.
It must never downgrade the daemon, start another admission domain, or open the
store directly. Equal-version source builds need the ordering decision below.
Reject legacy automatic drain requests that name an older successor too. An
older client must not bypass this rule through the previous takeover protocol.

### Ownership across the boundary

| State | Before and while preparing | After commit |
| --- | --- | --- |
| Job processes, protocol pipes, masks, cleanup, exit status | Their original host keeps supervising them. Candidate gets an inventory of run, award, attempt, process group, and host identity. | The same hosts keep reading output and waiting for exits. They send logs and terminal reports to the successor through the stable API. The successor owns durable results. No job is relaunched. |
| Machine admission | Old daemon alone mutates the ledger and sequences requests. The final transfer includes waiters, holders, lease tokens, member and parent mappings, ranks, deadlines, cancellations, pending deliveries, and counters. | Successor alone grants, releases, and persists. Existing connections relay to it. The old process cannot grant or free capacity. |
| Logical admission and execution fences | Old store owner commits reservations, permits, claims, and renewals. | The migrated store keeps the same reservation identities, arrivals, holders, generations, and expiry times. Adoption is not a release, retry, or new award. |
| SQLite and controller maintenance | Old daemon is the only live-store writer. Candidate may inspect a private backup only after the writer barrier. | Successor is the only writer and runs maintenance and scheduling. Predecessor holds no database handle. |
| Volatile inputs and credentials | Old owner retains requester secrets, claimant credential, pending sign-in links, and object-signing key. Copy them over the private channel. | Successor owns them. Do not revoke the claimant or rotate the credential epoch during retirement. Keep already issued object URLs usable. Wipe transferred secrets from retired memory when no host needs them. Never put requester secrets in the backup or handover log. |
| Controller clients and runner session | Hosts retain their client objects, authority, and renewal loops. The separate runner service keeps its controller URL, bearer, active awards, and process. | HTTP connections can reconnect to the same endpoint. Preserve runner identity and award fences. No re-enrollment or runner-service restart is part of daemon adopt. |
| Control socket, API socket, dashboard | Old process owns the listening descriptors and existing accepted connections. Candidate receives duplicate listening descriptors but does not accept yet. | Successor accepts on the same descriptors and addresses. Old accepted streams finish or relay. The old process never unlinks the socket paths. |
| Browser sessions | Old owner serves them. Session records also persist for ordinary restarts. | Successor uses the same session digests, peer bindings, origin bindings, expiry, and revocations. Open tabs stay signed in. |

### Prepare, commit, retire

1. **Prepare without changing service.** Verify the candidate executable and
   transfer format. Load its dashboard bundle and policies. Duplicate the three
   listeners and the election-lock descriptor. Keep old admission and HTTP
   service running. Candidate failure closes only its copies.
2. **Take a state barrier, not a drain.** Finish already executing mutations and
   settle each grant's delivery state. Park subsequent mutations in arrival
   order. Keep listening and receiving requests. Do not set `draining`, reject
   work, release a permit, or cancel a host. Capture the full ledger and volatile
   state at this boundary. Include grants committed before the barrier whose
   replies are still pending. Retry must return that same grant.
3. **Back up, then migrate.** With store writes parked, checkpoint and make a
   consistent SQLite backup. Close all old database handles. Preserve an
   immutable backup, its schema/digest, and the matching executable identity.
   Migrate a separate copy forward with the normal migration chain. Validate
   it and construct the successor controller without running reconciliation,
   maintenance, claims, or public handlers. Checkpoint and close the staged
   database before promotion. No old and new process may write concurrently,
   even to the staged database during this phase.
4. **Commit once.** Put the handover generation in the staged database. After
   the candidate reports ready and the old owner is fenced, atomically promote
   that database to `state.db` under the continuously held election lock.
   Handle the closed databases' WAL/SHM files before promotion. Sync the
   directory. This rename selects both the new schema and the new generation.
   The successor opens it, takes the active role, starts accepting on the
   inherited listeners, and processes parked mutations in order. The old owner
   closes its listener and lock copies without unlocking the inherited lock.
   Start maintenance only after installing the transferred live-host inventory.
   Adopted hosts must not pass through cold-start orphan recovery.
5. **Retire execution only when finished.** Old hosts keep renewing, logging,
   and reporting through the successor. Exit a retired supervisor after its
   hosts, final log acknowledgements, and required relays finish. On another
   upgrade, retarget existing supervisors to the newest owner. Avoid a chain of
   daemon proxies.

The store barrier delays mutations briefly. It must not wait for job completion.
Measure the backup and migration interval and abort preparation before it can
consume the remaining claim-renewal slack. Keep accepting requests with normal
backpressure. Do not hide a long schema rewrite inside this interval. Such a
migration needs a separately reviewed online migration, while the old daemon
continues serving. Full online database replication is outside this proposal.

FIFO means preserving original arrival order within the configured priority and
scheduling policy. Copy arrival and admit counters unchanged. New requests join
after the transferred queue. Never resubmit waiters as fresh requests. Carry
request IDs and pending replies across the barrier so a grant delivered twice
is still one grant and cannot start a second execution.

Pass listening descriptors with Unix descriptor passing. Retain the election
lock's open file description across the transfer. Listener duplication must
disable automatic Unix-socket unlink on predecessor close. Accepted framed
connections stay with their parser and relay complete messages to the active
owner, carrying their verified identity and connection ID. This avoids losing
partly read frames or queue positions.

Accepted HTTP requests finish their mutation before the barrier or park before
dispatch. After commit, the old HTTP front end forwards subsequent requests to
the successor. Preserve the verified socket peer in a private handover envelope,
never in a publicly trusted header. Keep dashboard Host, Origin, and session
checks. Long polls and streams may reconnect with their cursor. The listening
socket and URL never disappear. Moving Go HTTP parser state is unnecessary.

For an installed native daemon service, keep a small service-owned parent that
starts and watches the active generation. It owns no store or admission state.
Adopt changes the child, not the service-manager PID. This avoids tying an
upgrade to systemd or launchd stopping the predecessor's process group. Keep
this parent inside `wingd run`; add no executable or separately installed
service. Test this alongside the detached startup path. Refresh registration
for future starts without stopping active generations. The separate runner
service keeps its own update lifecycle.

### Failure and the first upgrade

Before commit, every failed handover leaves the old daemon in charge. Candidate
startup, descriptor validation, backup, migration, or readiness failure discards
the staged copy. Reopen the unchanged original database if needed, replay parked
mutations, and resume the old acceptors. The old process keeps its jobs, queue,
listeners, credentials, and browser sessions. Report the failure. Never fall
back to drain-and-restart.

Keep the exported volatile state until commit, so reopening the old store also
restores requester secrets. Store close clears its memory map at
[internal/store/store.go:304](../../../internal/store/store.go#L304).

If an acknowledgement is lost, inspect the committed database generation before
taking action. A promoted generation means adoption committed. The predecessor
stays fenced. A successor crash after commit is a daemon crash, not permission
to restore the old backup over new writes. Restart the successor build against
the new schema. An old binary encountering that schema refuses it unchanged
and directs the user to the matching newer binary. Do not attempt a down migration.

A daemon without adopt support cannot be made to export its goroutines or
listeners after the fact. New clients must leave it running. Reuse it when the
needed protocol works; otherwise explain that the first adopt-capable daemon
must be installed at a naturally idle boundary. Do not drain its queue to create
that boundary. Ship the capability and execution boundary before enabling
automatic replacement. Existing old clients also need a compatible hello so
they do not choose their old drain takeover path.

## Dashboard and browser sign-in

Every daemon startup binds `127.0.0.1:4343` and serves the dashboard. Bind a
configured loopback port when the default is occupied by another Sparkwing
home. Never silently choose another port, because bookmarks and Serve depend
on a stable URL. A UI bind failure must be visible without stopping machine
admission. The daemon stays up while idle. Explicit daemon stop and OS logout
can still stop it. A browser or Serve cannot wake a process that no longer owns
its listener, so retaining idle exit would contradict always-on service.

This supersedes D8's idle-exit trigger. Preserve its loopback rule on every
start and recovery. Keep a remembered port only as an installation migration
input, then remove the remembered-dashboard activation state.

`sparkwing dashboard open` opens the browser at the serving URL. A browser with
a session needs no CLI step. On first sign-in, the page accepts a short-lived,
single-use code minted through the authenticated local API socket. The CLI can
open that sign-in URL or print it for a headless machine. Bind redemption to the
chosen allowed origin. This also lets a remote browser redeem a code for the
configured Serve URL in phase 1. Never redirect a loopback login secret to a
Host supplied by the request.

Issue an opaque 30-day cookie with `HttpOnly`, `SameSite=Strict`, `Path=/`, and
no `Domain`. Its absolute expiry is 30 days; remove the shorter idle expiry.
Use `Secure` for the HTTPS Serve origin. The local HTTP origin needs a
non-Secure cookie. Scope sessions to this local daemon and the exact origin
used at sign-in. Store only the token digest, verified local subject, origin,
expiry, and revocation in the local store. Logout revokes the row and expires
the cookie. Restart and adopt preserve it. Never use the hosted-controller
session cookie or forward this cookie as a controller capability.

Any web page can attempt requests to localhost. Host checks stop a hostile DNS
name resolving to loopback. Exact scheme/host/port Origin checks stop another
site from making authenticated writes. Require Origin on cookie-authenticated
mutations, including logout, and check it on WebSocket upgrades. Refuse null
origins and cross-site subresources. Keep JSON-only API writes and no permissive
CORS. Navigating to the signed-out shell may remain public. Apply these checks
before authentication and all handlers.

Other local users can also connect to loopback and forge HTTP headers. Host and
Origin checks do not authenticate those users. Only the same-account API socket
can mint local sign-in codes. Cookie auth also has a real limit: browsers scope
cookies by host, not port. Another service on the same loopback host can receive
one if the user visits it. The existing bearer gate explicitly calls out this
constraint at [internal/wingd/local_login.go:219](../../../internal/wingd/local_login.go#L219).
Do not claim that `HttpOnly` or Host checks solve it. The cookie decision stands;
the isolation recommendation is an open question below.

## Tailscale Serve

Configure Serve to proxy HTTPS to `http://127.0.0.1:4343`, for example
`tailscale serve --bg http://127.0.0.1:4343`. Serve supports proxying a local port
and persistent background configuration. See the official
[Serve guide](https://tailscale.com/docs/features/tailscale-serve#run-tailscale-serve)
and [CLI reference](https://tailscale.com/docs/reference/tailscale-cli/serve).
Sparkwing never binds the tailnet IP, a LAN IP, or a wildcard address for this.

Add `dashboard.allowed_hosts`, for example `["workstation.example.ts.net"]`.
Entries are exact machine names, never a wildcard for all of `*.ts.net`. Each
entry admits that Host and its HTTPS origin on port 443. Local defaults admit
only the configured loopback origins. Host admission grants no user authority.
Ignore `Forwarded` and `X-Forwarded-*` for authentication. Test the Host and
Origin seen through real Serve before shipping. Keep the external origin bound
to its session even though the backend connection is HTTP.

Phase 1 uses the same one-time sign-in and 30-day cookie at the Serve hostname.
Ignore `Tailscale-User-Login` as authority. Tailscale documents that Serve adds
identity headers and removes client-supplied versions, but also warns that local
services can forge a direct backend request. See
[Serve identity headers](https://tailscale.com/docs/features/tailscale-serve#identity-headers).

**Phase 2, later and design only:** allow an exact configured login identity to
sign in as the configured local subject, then issue the same origin-bound
cookie. A loopback source address, Host, forwarded address, or identity header
cannot prove that Serve sent the request. On the ordinary TCP backend there is
no cheap proof. Therefore leave automatic identity sign-in disabled there.
If implemented later, require an authenticated Serve-to-backend path, such as
a separate Unix socket whose kernel peer is the dedicated tailscaled account,
with no same-user impersonation path. Merely hiding a socket from other users
is insufficient if ordinary processes of its owner can forge the identity.
Confirm support and deployment cost before choosing that transport. Otherwise
keep phase 1 sign-in. Do not add a header-trusting switch.

Cut `--allow-remote` and non-loopback dashboard binds. Replace their docs with
Serve setup and the explicit Host allowlist. No deprecated alias or forwarding
shim. Cut independent dashboard start/stop and move installation ownership to
daemon install/uninstall. Keep dashboard open, status, and logs as UI conveniences.
Make daemon restart use adopt when replacement is needed. Keep destructive stop
explicit and separate from upgrades.

## Refresh the global CLI after land

Add a local install-and-adopt step after a successful push. Build from the exact
landed commit, including any merge, in a clean snapshot. Build and verify the
dashboard bundle too. Stage the CLI and runner next to their install targets,
then replace each executable atomically. Reuse the installer contract rather
than adding a second build recipe. Do not reinstall or restart the runner service.

Invoke the freshly installed CLI's daemon adopt operation by absolute path,
outside repository engine selection. It offers that executable and build
identity to the running daemon. If the daemon is already newer, reuse it. If
none is running, start the installed one. The land run itself may still have
active hosts when this step executes; it must survive its own adopt.

Serialize local installation and recheck the installed build before replacing
it, so two landings cannot install an earlier build last. Record the pushed
commit separately from installation and adoption outcomes. An install or adopt
failure reports "landed, local refresh failed" with an exact retry command.
Do not repeat the push or imply that main rolled back. Dry runs, stop-at-verify,
and hosted CI never write a developer's global bin directory. This is a local
development refresh, not a public release.

## Cuts and implementation order

Cut drain-based automatic takeover, same-version build ping-pong, direct store
bindings in execution hosts, dashboard activation memory, short-lived bearer
browser sessions, independent dashboard lifecycle, and remote binds. Keep the
store migration chain, admission policy, award protocol, and runner updater.
Add no process reparenting, replicated database, generic store RPC layer, new
service executable, or parallel admission daemon.

Sizes are engineering estimates, including focused tests. S is about half a
day, M is one to two days, L is three to five days. Native-process fixtures and
cross-platform failures can extend them.

1. **Execution boundary and rollout guard, L.** Route every local host operation
   through the award API. Split claim/maintenance lifetime from active execution.
   Stop automatic drain replacement. Negotiate adopt and old-client reuse.
2. **Handover transaction, L.** Add listener and lock transfer, full machine
   queue transfer, writer barrier, backup, staged migration, commit generation,
   rollback, and retired connection relays. Preserve volatile authority.
3. **Native lifetime and repeated upgrades, M.** Cover detached startup and the
   managed-service parent. Retarget surviving supervisors without proxy chains.
4. **Always-on dashboard and cookies, M.** Persist sessions, remove idle exit,
   add sign-in code redemption and strict cookie-request checks, cut dashboard
   lifecycle commands, and settle loopback cookie isolation.
5. **Serve and surface cleanup, S.** Add the exact Host allowlist, phase 1
   sign-in, and remove `--allow-remote`. Keep identity sign-in for later.
6. **Land refresh, M.** Install the landed build atomically and trigger adopt.
   Update delivery instructions and generated CLI/config references together.

Before enabling automatic adopt, exercise real processes with two CLI versions:
a long-running job that keeps logging and exits with a known status, multiple
FIFO waiters, a grant whose reply is interrupted, concurrent cancellation, local
secrets, a scheduled run, and an active runner-service award. Adopt twice while
the first supervisor still has work. Submit another run during each transfer.
Require unchanged attempts, preserved order, one start per award, continuous log
offsets, one store writer, and the same dashboard listener and browser session.
Inject failure before every commit boundary, including migration and descriptor
transfer. Test a lost commit acknowledgement and a successor crash separately.
Reject older candidates without touching the store or jobs. Test through real
systemd/launchd registrations, not just process fakes.

Dashboard coverage must include a signed-in browser across adoption and an
ordinary restart, an expired and revoked cookie, hostile Host/Origin requests,
a second local OS account, and a forged Serve identity header. Exercise the
Serve URL while loopback binding remains unchanged. Update ORIENTATION's owners
and guards, ADMISSION's handoff contract, source docs and drift checks when the
behavior lands. The repository requires those source-doc updates at
[AGENTS.md:14](../../../AGENTS.md#L14), and names migration guards at
[design/rewrite/ORIENTATION.md:21](../ORIENTATION.md#L21).

This note is documentation only. No Sparkwing binary was built or run, and no
store, service, or pipeline was exercised.
The lead owns the full gate after this session. Delivery's focused-evidence and
landing decisions are at [DELIVERY.md:3](../../../DELIVERY.md#L3) and
[DELIVERY.md:1473](../../../DELIVERY.md#L1473).

## Open questions for Korey

1. **First release and platform coverage.** A predecessor that lacks export
   support cannot hand over safely. Unix descriptor transfer also needs a
   Windows handle-transfer counterpart. Recommendation: ship the prerequisite
   release first, reuse unsupported predecessors, and activate automatic adopt
   per platform only after its native handover tests pass. Never use a drain
   fallback. This leaves an explicit first-upgrade limitation.
2. **Ordering source builds after land.** Semantic versions do not order two
   commits built from the same release base. Recommendation: let the local
   installer prove Git ancestry from the recorded running revision to the
   landed revision and offer a compare-and-swap against that predecessor build.
   Incomparable or dirty builds reuse the running daemon and report why they
   cannot adopt. Do not equate a differing build identity with "newer".
3. **Local cookie isolation.** A plain HTTP cookie on a shared loopback hostname
   cannot isolate hostile local services by port. Recommendation: use a stable,
   random hostname under `.localhost` per daemon and a host-only cookie, with
   exact Host/Origin checks. This reduces accidental cross-service exposure but
   does not prove isolation against an active local adversary. Keep that limit
   explicit. Strong isolation needs a trusted HTTPS origin and certificate
   setup, which is larger than this upgrade change. The 30-day cookie decision
   remains fixed either way.
