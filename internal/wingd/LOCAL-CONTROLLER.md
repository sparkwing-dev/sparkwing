# Local controller lifecycle

`NewLocalLifecycle` coordinates a home-bound `LocalRuntime`. The runtime is bound to the same home before lifecycle construction, so its
status queries address that owner. `Run` rejects options naming another home
before opening a store. Concurrent callers share startup. A successful start waits for `AwaitReady` and a running status.
Ending an on-demand startup request does not cancel a controller that has become
ready. Standalone mode follows its caller's context because that process owns
the controller lifetime.
A failed startup cancels and joins both the runtime and its readiness waiter
before returning. The runtime port must honor cancellation after cleanup.
`StartDashboard` starts the dashboard serving loop on an existing controller.
`Stop` waits for the runtime's admission drain and shutdown.
`InstallController` holds the same lifecycle gate through drain, managed-unit
activation and successor readiness. It joins the previous owner before starting
the service. The client holds the existing home startup claim through this
transition, so another CLI cannot start an on-demand owner during the handoff.
Installation requires an installed unit and a new running PID with the
requested version. Activation or readiness failure reports where
the handoff stopped. It starts no fallback controller because the managed unit
may already be active. These checks establish activation and successor identity,
not the service manager's process ancestry.

`NewLocalRuntime` implements the process lifetime behind `LocalRuntime`. It
acquires the admission election before invoking `LocalRuntimeConfig.Open`.
That factory opens one store and returns the shared controller, including its
run-store adapter, local queue consumer and leased maintenance. `Reconcile`
finishes orphan recovery and supplies the initial activity before any listener
starts. A losing election opens no store and returns `ErrNotElected`.

`localcontroller.Config.Open` supplies that factory's store ownership and
maintenance. It opens `<home>/state.db`, selects `DefaultTeam`, and gives the
shared service binder that store, tenant and `EngineController`. The binder
returns the shared policy handlers, state projection and `Services.Runner`.
After the API starts listening, that function supervises the local runner on
Linux and macOS. It discovers the persisted active instance, checks process birth,
version and executable digest, and reuses a matching live process. A different
build or dead process starts a successor from a digest-pinned executable under
`<home>/runners/builds`. The successor obtains a claim-only credential through
verified socket HTTP and constructs `runners.Loop` with native HTTP hosting.
It records its process instance and activates it before claiming. Windows still
uses an embedded loop until same-account SCM supervision is available. The
production store claimant and hosting adapters are removed.
The controller needs its signed runner admission policy and the store needs its
enrollment resolver. Queue startup and refresh use the local signed policy.
Claim admission authenticates the persisted credential through
`EngineController.CheckRunner`, which selects local policy only for a persisted
local subject. Local checks use cached policy without network refresh, including
when a new home has no cached policy. Machine and service claimants retain strict
runner policy. Queue startup therefore cannot end a fresh local controller by
applying online runner policy to its local claimant.
The native service binder supplies W3's source, process, logging and storage
services. Cleanup must accompany partially constructed services even when
binding fails. It closes those services before the store. Missing runner services
or signed policy fail startup through the same cleanup path. Replacing the
controller's store also fails startup.

The CLI factory now binds `NewState`, the shared `Server.HTTP()` handlers,
signed engine and runner policies, and the native service constructor.
`NewState` reads activity in one tenant-scoped snapshot and reports changed
counts. Published history and terminal nodes do not contribute to activity.
The production factory test reconciles a fresh home, checks
both HTTP handlers and verifies closure without a socket.

Both controllers construct their server with `controller.New`, which installs
the transactional intake resolver before handlers are built. It resolves captured
preferences with deployment ceilings and seals log retention. Local execution
keeps its unbounded owner duration. The store installs its native provision
policy when opened. Capability inventory uses the server's tenant/operator
adapters when no replacement resolver is supplied.

`TestProductionLocalControllerAdmitsRun` opens the real local controller and uses
`ServeAPI` over in-memory connections with peer identity. It exercises admission,
replay, signed owner admission, plan acceptance, node claims and result reporting,
output upload/readback, durable log upload/readback, and atomic run completion.
The test supplies signed release metadata and reports execution results through
the API. Pipeline spawning and machine admission remain native-host checks.

`NewClaimProfile` captures the process's kernel account. Its enrollment resolver
accepts only that authenticated local account in `DefaultTeam`, and supplies
the machine's CPU count, OS and architecture, plus the `runner.labels` the
machine configuration listed when the controller started. Ambient runner names and tokens
cannot select the account. It holds no store handle or credential. The local
queue delegates exactly `triggers.claim` and `nodes.claim` through
`POST /api/v1/tokens`. The store retains the peer root across controller starts,
so process-specific delegates keep the same machine placement identity.
After the loop joins its hosts, the queue records its stopped instance and
revokes its delegate after terminal acknowledgements. The runner has an
independent lifetime; controller replacement does not join or cancel its hosts.

`NativeServices` takes `NativeConfig` with W3's `NativeHostStore`, C2's
`AuthenticationReader` and `BindingChecker`, the authenticated principal, refresh
and renewal callbacks, and source options. The local queue supplies authenticated
HTTP adapters. Those interfaces require no execution-host store opener.
Refreshed authority, persisted run identity and both tenant projections
must agree before process transport opens. The constructor binds W3's host
configuration, source, captured environments, lease keeper, state following and
process router. Owner
renewals reauthenticate the award, check its owner fence, and call the shared
unit-renewal transaction with current compute activity. The shared host owns
the keeper and joins it before service cleanup. It
checks current authority before allocating resources. Process HTTP uses the
same joined serving lifecycle as the API and dashboard. Closing the host
cancels requests, closes HTTP and hijacked connections, and joins handlers
before closing process authority. Repeated close calls return the same cleanup
error. Logs use the shared segment sink, signed object uploads and transactional
segment commits. Public log readers and the dashboard consume those same records.
Node logs seal before terminal reports or retries; owner logs seal before atomic
run completion. The private JSONL writer is removed.

`WithClient` supplies process services and fresh byte/record bindings through the
authenticated controller transport. Every node process retains its independent
work award. Owner evaluation uses a separate binder and cannot fall back to a
node claim. C2's owner/eval nodehost process-authority contract remains required
before owner closures can execute. Cache lookup responses also need canonical
read-set proof; those operations fail explicitly while that contract is missing.
The native HTTP roundtrip test verifies output bytes and normal public log reads
without opening sockets.

The native local host binds W3's machine-admission port to the elected home
with takeover and spawning disabled. W6 resolves the configured box identity
and its hostname default. The same identity reaches local and store capacity
admission. Every locally scheduled job uses machine admission before execution.
Each admission request creates a durable HTTP ticket on the controller API
socket. Retrying its client request key returns the same result. Queued tickets
retain arrival order through dropped long polls and controller restarts. The
host watches a granted ticket for committed eviction or cancellation and releases
it with an idempotent request. The production CLI supplies its version to this
binding. The adapter tests preserve the captured home and refuse malformed
machine settings. Native socket identity still requires platform evidence.

`internal/admission/ledger.go` retains machine policy ownership. Every mutation
commits the complete ledger snapshot, waiters and request receipts to
`local_machine_admission` in the elected controller's `state.db`. Startup restores
that payload before serving admission. The former `wingd/state.json` sidecar
supplies no authority. Commit failures restore the previous committed state.
In-memory wake channels only prompt pollers to reread their durable tickets.

Tickets and leases bind the holder root PID to its `procgroup.ProcessBirth`
start identity. The controller verifies its OS account against the socket peer.
Each admission decision and a periodic check release dead or reused holders.
Process inspection errors retain capacity. There is no machine renewal, expiry
or timeout setting, and a connection closing releases nothing. Hosts still own
job execution and the existing logical claim renewals.

Runner instances persist in `local_runner_instances` with a claim credential
prefix, PID, process birth, version, executable SHA-256 and lifecycle state.
Registration verifies the socket account and live process identity. An instance
starts registered and cannot claim. Activation holds the same store arbitration
lock as `claimUnitTx`, changes the previous active instance to draining and
enables the successor in one transaction. The peer-root machine identity remains
separate from process instance IDs. Draining leaves held award ancestry valid
through cleanup, upload and terminal acknowledgement. New awards from registered
local runners retain the nearest registered runner credential in their ancestry.
Fresh queue claims and owner self-claims check that instance under the same
claim-arbitration lock. A draining owner leaves new dependencies ready for its
successor while continuing held renewals, reports, logs and child intake.
Placement still follows the peer root; here-award parenting is unchanged.

The socket-only routes are `POST /api/v1/local/runners`,
`GET /api/v1/local/runners/{id}`, and `POST` actions at that instance's
`/activate`, `/drain` and `/stop` paths. A read with `wait_state=active` waits up
to 20 seconds for a state change. Mutations wake these watchers after commit.
Browser sessions and remote credentials cannot use these lifecycle routes.
The runner watches independently of claim capacity and calls `Loop.Drain` when
its instance leaves active state. Watch reads reconnect through transport errors.
After joining its hosts, the queue records stopped state before revoking its
credential. Ordinary ended-credential retention removes its instance record.

On Linux, systemd-booted machines require a working user manager. Each instance
runs in a separate `systemd-run --user` service with `KillMode=mixed` and
`TimeoutStopSec=infinity`; controller service shutdown cannot reach its cgroup.
A confirmed no-systemd host uses a separate session, separate stdio and no
parent-death signal. macOS uses a separate per-instance launchd agent with no
forced drain timeout. Captured environment overrides the manager environment,
and each instance has its own log under `<home>/runners`. Services do not
restart a retired instance after a crash; the controller starts a new identity.
Accepted Go replacement identities retain their exact executable in
`runners/builds/<sha256>`. New resolutions select the installed CLI. A successor
can ensure a previously accepted identity only from that digest's retained file,
checking native target, embedded build version and the complete SHA-256 before
execution or SDK installation. Missing or changed bytes fail the job.

No healthy drain has a kill deadline. Runner stop signals call `Loop.Drain`.
Whenever the claim loop exits, an independent mutation records draining while
held hosts continue. It reconnects through API outages and is joined after the
hosts finish. A controller returning after the claim recovery window can start
a successor instead of reusing a process that stopped polling. Startup waits use
the existing private-request timeout and never kill a launched runner. Private
launcher environment files are removed after the manager loads them.

Foreground local runs without publication submit to the queue and observe
persisted logs and terminal status through HTTP. Their SDK processes and pipes
belong to the runner, with the submitter's captured environment. Preparation
retains its selected replacement-engine bytes once, so an older compatible
runner can execute the accepted build. Read observation reconnects through
controller outages without resetting its log cursor. Permission, protocol and
integrity refusals remain errors. First interrupt requests durable cancellation;
a second stops observation, including when the first arrived during intake.

Standalone runs and foreground publication keep their CLI host. Standalone owns
its in-process controller; publication keeps its existing cleanup ordering,
warning and deadline before return. Remote here execution also remains in the
CLI. Windows still shares the controller queue lifetime. Native launchd and
Windows service survival remain unverified.

The existing process-tree sampler supplies current, peak and average CPU and
memory usage through the controller API for the dashboard. Its attribution and
the ledger's guarded backfill retain their existing scheduling rules.

The local schedule factory constructs the same home-bound lifecycle and native
timer operations without opening the store. Registration, pinning and ticks
remain unbound because the current controller contracts have no local-checkout
registration with a captured environment, pin operation or tick operation.
The CLI factory tests verify home preservation and native timer cancellation.
`localcontroller.NewScheduleService` constructs the store-backed cron service
inside the elected owner. It uses the owned default-team tenant, captures the
OS account and hostname, and binds the tick lock and pin directory to that home.
Its launcher must implement the shared `crons.Launcher` contract. The factory
adds no intake path. SQLite tests cover registration, pin/unpin, tick history and
pin deletion through this service with a fake prover and launcher. The HTTP
operations and captured-environment association above still need contracts.

Before readiness, this composition acquires the store's `controller.reaper`
lease, runs unit recovery and concurrency maintenance, then asks the state
projection to reconcile local activity. It renews the same fence after that
projection returns. Expiry or replacement during reconciliation refuses startup.
The elected home process uses the stable `wingd` maintenance holder because its
workers join before the election is released. Periodic maintenance renews the
fence and uses the same store operations. It does not contribute to activity.

Queue execution, activity observation and maintenance share cancellation.
Failure of one stops and joins all three. Store close refuses while those
workers are running. The runtime continues to own HTTP shutdown and closes the
composition only after its handlers and admission finalizers join.

Admission reads run state through the composition's `DefaultTeam` handle.
Cancellation calls `Tenant.TransitionRun` with the elected reaper fence. It
records the request while live holders drain, and maintenance finishes the run
after their leases end. Repeated requests preserve the first reason and leave
terminal results intact. Missing and foreign runs fail. Stale fences cannot mutate state.
Process-death finalization logs failures and remains usable during runtime drain
because admission workers join before store close. Readiness requires the
open, reconciled owner and the shared state binding's readiness check.

Readiness waits for reconciliation, admission startup and the API serving loop.
When `DashboardAddress` is set, it also waits for the dashboard serving loop.
An empty address serves only `api.sock`. `AwaitReady` waits for an active start
and cannot reuse a completed start's readiness or failure. `LocalLifecycle`
observes the matching `Run` result, so startup failures and lost elections
return instead of waiting forever.

`EnsureDashboard` starts the dashboard on an existing ready controller. Requests
for the same address and read-only policy share its listener and readiness. A bind failure leaves
the controller running. A different address or read-only policy requires a stop and restart because
the runtime owns one dashboard listener. Shutdown joins that serving loop before
closing the store, including when the dashboard started after the controller.

Any serving or worker failure cancels the runtime. Drain uses admission's
draining state, stops listeners and joins workers, API requests and admission
finalizers before closing the store. Election ownership lasts through store
close. The daemon also joins its sampling and diagnostic workers.
Transient startup failures can be retried. An incompatible store remains a
doctor finding. A dashboard startup failure reaches its caller after that
cleanup, with the original service error preserved.

The daemon accepts live controller counts through `setLocalActivity` for status.
The runtime counts its dashboard HTTP connections until they close, including
idle keep-alive sockets and hijacked connections. Controller activity updates
cannot erase that count. The controller remains running while idle until an
explicit stop, context cancellation or runtime failure.

The runtime records the address and read-only policy of the dashboard it last
served in `wingd/dashboard.json` under its home. A controller started without a
dashboard address, other than a standalone one, serves that dashboard again, so
an on-demand start from a run or schedule restores the dashboard. The restore
binds loopback only: a remembered non-loopback address comes back as `127.0.0.1` on the same port, because `--allow-remote` consents to
one explicit start. A failed bind on that restore is logged and the controller
starts without it. `sparkwing dashboard stop` and `sparkwing daemon stop` clear the
record with `ForgetDashboard`; `daemon restart` keeps it.
The client `Probe` API reads controller HTTP status without requesting queue work.

`LocalSubject` accepts matching kernel UIDs on Linux and macOS, or matching
client SIDs on Windows. Missing identity refuses access. The socket transport
also refuses peers when its platform implementation cannot supply credentials.
`authenticatedAPIConn.PeerIdentity` exposes that verified identity to the shared controller.
The connection owns its identity snapshot. Changing an input UID or a returned
UID cannot change the account attached to the connection. The API listener
checks `LocalSubject` before yielding a connection. Unix uses kernel UIDs.
Windows uses owner-only named pipes and the client's identification-token SID.
The [transport contract](LOCAL-TRANSPORT.md) lists the native tests still required.
Election files use `fssecure`. Machine admission state uses the store's schema
migration and baseline checks.

`ResolveAdmissionPolicy` reads the complete machine configuration through
`userconfig.ReadConfig`. The shared configuration package owns decoding and
validation. The runtime converts its typed admission settings to scheduling
policy, preserving explicit zero values and the defaults for omitted fields.

<!-- design:begin -->

## Integration requirements

Detached and scheduled startup callers use the shared lifecycle, preserve its
startup error and require a running result. They refuse a service binding for
another home before starting it. The separate trigger-consumer command, process,
election, store opener, dispatcher and renewal loop are removed. `localws.Run`,
which started another consumer beside a second store owner, is removed too.
The retained local HTTP handler fixtures use their own test server.
The CLI service resolver and sealed local intake remain production bindings.

The removed consumer's snapshot reader, consume-at-start rule and cleanup cursor
are removed too. Final local hosts use `FileEnvironmentSnapshots`, whose sealed
identity survives replacement owners, retries and children. Missing or altered
values fail before acquisition. The remaining legacy CLI capture writer still
needs migration to that intake contract. It has no consumer after the hard cut.

The `localcontroller.State` interface supplies readiness, the owned secrets read, startup
reconciliation and live activity observation. The CLI binder supplies the
production state projection, shared API/dashboard handlers, peer claimant and
native host configuration. `Open` constructs the shared queue from those inputs.
`Run` joins the queue, activity observer and maintenance before returning.
The maintenance loop runs the shared server's storage pass after readiness,
on its first renewal tick and then at `StorageMaintenanceInterval`. It joins
before the elected store and object backend close.
The runtime adds its dashboard connection count. Its API server consumes the
kernel identity on `authenticatedAPIConn`. Its dashboard server retains browser authentication.

The native binder adopts committed self-claims through `OwnerHost.AdoptClaim`.
It checks each private node award through the shared authentication and relation
policy, registers its bearer with the shared masker, and captures it separately
from the run owner's authority. Every process delegation rechecks the current
owner, node award and persisted generation and attempt. The parent claim stays
at attempt zero across retries. SDK process authority and its data scope narrow
to the running attempt. Its process revoker ends the child authority when
execution joins.

`TestNativeStoreOwnerW8/local_controller` exercises `Config.Open`, leased
reconciliation, the shared queue, native host, persisted completion and joined
shutdown. It substitutes state/HTTP, enrollment, version policy and source
services. The local node-adoption properties use C2 snapshot and relation fakes
with the shared authenticator. The persisted node-start tests
verify that an attempt-zero award survives the transition to its current attempt.
The delegation test uses the shared `NativeProcesses` and `ProcessAuthorities`.
These tests do not establish persisted peer authentication, remote native state,
owner policy composition, byte/record bindings or native process execution.
The local CLI host and schedule operation transport still require those shared
contracts. The store also needs a typed incompatible-store error.

`client.QueryLocalController` discovers the owner through the authenticated
controller API socket. It reports readiness, PID, version and the four
activity counts. The query starts no process and replaces no other version.
Missing sockets, unreachable owners, unsupported operations and stalled replies remain distinct errors. The existing `client.Stop` requests controller drain
and waits for the socket and election lock to close.

The controller HTTP routes under `/api/v1/local/controller/` expose probe,
status, dashboard and drain operations. Each requires the verified local
socket peer. Only an elected `LocalRuntime` serves lifecycle mutations. Status
describes the running process. Native service inspection supplies installed-unit
facts separately.

`client.startLocalDashboard` requests dashboard startup from an existing owner.
Its HTTP request waits for the serving loop to reach readiness. An identical
address reuses the listener. A bind failure returns the owner's error and leaves
the controller available. Admission-only daemons refuse the operation.

The dashboard HTTP request follows the caller's context while waiting for
readiness. Shutdown cancels requests and joins handlers before closing the
store. A service failure remains observable after cleanup. Dashboard startup
can take longer than a health probe, so readiness follows the caller's context.

`client.NewLocalLifecycle` reuses a ready owner or starts the captured absolute
executable through `wingd run --home HOME --version VERSION`. That process
owns the controller. Startup uses the admission client's shared
start claim and election checks, with takeover disabled. Readiness requires the
local controller status to report running within the existing startup budget.
The request can finish while that detached process continues hosting other runs.

The adapter binds one home before any owner query. An empty start Home selects
that bound home. Another home is refused before mutation. It attaches the
dashboard and drains the elected process through the authenticated socket. Status retains installed-unit information. Unreachable, stalled or
unsupported owners remain errors. Version skew does not replace a ready owner.

Standalone uses the supplied in-process runtime and refuses an existing owner.
If another process wins its election, the lifecycle joins local cleanup before
querying the winner. A joined cleanup failure remains an error. Starting or
draining local processes retain lifecycle operations until cleanup finishes.

The foreground `wingd run` process constructs shared state, HTTP and native
host services through the CLI's `localcontroller.Config.Bind` adapter.
`NewLocalRuntime` refuses a foreign election before opening its store. Windows
transport still refuses API access when it cannot supply a client SID.

Drain preserves cancellation and unreachable-owner errors. Its HTTP response
acknowledges drain before shutdown starts. A successful drain then waits for
the socket and election lock to close. It starts no replacement process.

`NewManagedUnitManager` implements `ManagedUnitManager` for one captured state
home. Linux uses systemd user units. macOS uses LaunchAgents. Controller units
run foreground `wingd run --home <absolute-home>`. Timers run
`__tick --home <absolute-home>`. Windows refuses native installation and names
the foreground `wingd run` command. Inspect reads the installed argv and release
without opening
the store or contacting the service manager.

Refresh updates both installed unit kinds. It keeps their captured settings and
does not install missing units. Installed controllers restart after update.
Installed macOS timers reload even if an earlier bootstrap failed. Schedule
pause remains store state. Linux timer refresh preserves its enabled state.
Registration checks refuse files loaded from another directory before commands
can act on their names. Foreign files, different homes and symlinks are refused.
Systemd commands disable variable expansion because captured paths must remain
literal. Native files share private atomic publication and argument encoding
with runner services. Release metadata survives XML-sensitive prerelease names.

The CLI service resolver supplies this manager and the shared-controller factory.
The foreground CLI uses `runWingdRunCommand` and the shared flag registry. Its
former second runtime constructor is removed. Election and factory-failure
coverage now use that same command path.
File and service-manager fixtures exercise these ports. They do not establish
native login, crash, shutdown or Windows behavior.

`TestLocalRuntime*` exercises election-before-open, readiness, failure cleanup,
restart after failed startup, activity counts and remaining ready while idle
until explicitly stopped. It uses
in-memory listeners and fake shared-controller services. `TestLocalHTTP*`
exercises verified-peer delivery, refusal paths, cancellation and joined
shutdown. Machine-admission HTTP tests cover durable requests and poll wakeups.
The sampler tests cover process-tree attribution and current, peak and average
usage independently of admission charges.
`TestLocalLifecycle*` in the client package exercises foreign-owner composition,
election races, home binding, concurrent reuse and refusal paths through the
lifecycle interface with process-boundary fakes.
The process/store
scenarios remain open: dashboard install/update, Windows
transport identity and local orphan recovery. These require the real controller
and platform fixtures.

<!-- design:end -->

The local dashboard accepts non-loopback addresses through `dashboard start
--allow-remote`. Every data request requires the bearer session minted through
`api.sock`, including remote requests. `--read-only` rejects authenticated browser
requests other than GET, HEAD and OPTIONS before controller dispatch; sign-in and
sign-out remain available. It blocks writes but still allows reading decrypted
secrets, so a read-only session is not safe to share. The local socket retains
its normal authority.

`--allow-remote` serves cleartext HTTP. The one-time login token and 24-hour
session bearer cross the network unencrypted, and a session can read decrypted
secrets. Use it only on a trusted network or through an SSH tunnel. For a tunnel,
keep the remote dashboard bound to `127.0.0.1:4343` and run this on your machine:

```sh
ssh -N -L 4343:127.0.0.1:4343 user@host
```

Open the dashboard through `http://127.0.0.1:4343` on your machine.

The dashboard owner acknowledges the requested read-only policy so a client
refuses an owner that cannot confirm it. Browser origins must match the bound
host and port; wildcard binds need a loopback origin or a concrete bind address.
`--allow-origin` is removed because the controller's browser API requires same-origin
requests. Remote access through an alternate hostname should use a controller deployment.

Runner discovery uses `GET /api/v1/local/runners?state=active` through the verified
socket. The store resolves the account's active instance and validates its claim
credential ancestry in the same snapshot. Registered, draining, stopped and
foreign-account instances are absent. The returned PID, birth and build are
persisted identity; the caller must check the actual process before reuse.
