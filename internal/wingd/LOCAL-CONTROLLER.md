# Local controller lifecycle

`NewLocalLifecycle` coordinates a home-bound `LocalRuntime`. The runtime is bound to the same home before lifecycle construction, so its
status queries address that owner. `Run` rejects options naming another home
before opening a store. Concurrent callers share startup. A successful start waits for `AwaitReady` and a running status.
Ending an on-demand startup request does not cancel a controller that has become
ready. Standalone mode follows its caller's context because that process owns
the controller lifetime.
A failed startup cancels and joins both the runtime and its readiness waiter
before returning. The runtime port must honor cancellation after cleanup.
`StartDashboard` can promote an existing controller to stay mode and start its
dashboard serving loop. `Stop` waits for the runtime's admission drain and
shutdown.

`NewLocalRuntime` implements the process lifetime behind `LocalRuntime`. It
acquires the admission election before invoking `LocalRuntimeConfig.Open`.
That factory opens one store and returns the shared controller, including its
run-store adapter, local queue consumer and leased maintenance. `Reconcile`
finishes orphan recovery and supplies the initial activity before any listener
starts. A losing election opens no store and returns `ErrNotElected`.

`localcontroller.Config.Open` supplies that factory's store ownership and
maintenance. It opens `<home>/state.db`, selects `DefaultTeam`, and gives the
shared service binder that store, tenant and `EngineController`. The binder
returns the shared policy handlers, state projection and `StoreRunnerConfig`.
`Config.Open` constructs the shared queue over its owned store before returning.
`runners.NewStoreRunner` constructs that queue from the shared controller and
`runners.StoreRunnerConfig`. `Config.Open` rejects another controller store.
The runner supplies `StoreHosting` over the owned handle. The controller needs its signed runner
admission policy and the store needs its enrollment resolver. The bearer provider
supplies a provisioned claimant credential.
The native service binder supplies W3's source, process, logging and storage
services. Cleanup must accompany partially constructed services even when
binding fails. It closes those services before the store. Missing runner services
or signed policy fail startup through the same cleanup path. Replacing the
controller's store also fails startup.

Before readiness, this composition acquires the store's `controller.reaper`
lease, runs unit recovery and concurrency maintenance, then asks the state
projection to reconcile local activity. It renews the same fence after that
projection returns. Expiry or replacement during reconciliation refuses startup.
The elected home process uses the stable `wingd` maintenance holder because its
workers join before the election is released. Periodic maintenance renews the
fence and uses the same store operations. It is not an activity hold.

Queue execution, activity observation and maintenance share cancellation.
Failure of one stops and joins all three. Store close refuses while those
workers are running. The runtime continues to own HTTP shutdown and closes the
composition only after its handlers and admission finalizers join.

Admission reads run state through the composition's `DefaultTeam` handle.
Cancellation calls `Tenant.TransitionRun` with the elected reaper fence. It
records the request while live holders drain, and maintenance finishes the run
after their leases end. Repeated requests preserve the first reason and leave
terminal results intact. Missing and foreign runs fail. Stale fences cannot mutate state.
Disconnect finalization logs failures and remains usable during runtime drain
because admission finalizers join before store close. Readiness requires the
open, reconciled owner and the shared state binding's readiness check.

Readiness waits for reconciliation, admission startup and the API serving loop.
When `DashboardAddress` is set, it also waits for the dashboard serving loop.
An empty address serves only `api.sock`. `AwaitReady` waits for an active start
and cannot reuse a completed start's readiness or failure. `LocalLifecycle`
observes the matching `Run` result, so startup failures and lost elections
return instead of waiting forever.

`EnsureDashboard` starts the dashboard on an existing ready controller. Requests
for the same address share its listener and readiness. A bind failure leaves
the controller running. A different address requires a stop and restart because
the runtime owns one dashboard listener. Shutdown joins that serving loop before
closing the store, including when the dashboard started after the controller.

Any serving or worker failure cancels the runtime. Drain uses admission's
draining state, stops listeners and joins workers, API requests and admission
finalizers before closing the store. Election ownership lasts through store
close. The daemon also joins its sampling, idle and diagnostic workers.
Transient startup failures can be retried. An incompatible store remains a
doctor finding. A dashboard startup failure reaches its caller after that
cleanup, with the original service error preserved.

Jev admission reads its optional `TYPESAFE_API_KEY` through the elected
controller's secrets store. An explicit advisor takes precedence. A missing or
unreadable key is logged and retains the existing admission fallback. Startup
does not open a second secrets store.

The daemon accepts live controller counts through `SetLocalActivity`. Runs,
queued triggers, held leases and dashboard connections prevent idle exit.
The runtime counts its dashboard HTTP connections until they close, including
idle keep-alive sockets and hijacked connections. Closing the last connection
starts a fresh idle interval. Controller activity updates cannot erase that hold.
Admission holders and waiting runs also remain active. `SetStay` pins the
daemon. Health probes, repeated unchanged counts and idle API connections do
not extend its idle deadline. The timeout comes from
`limits.LocalControllerIdleTimeout` unless the existing admission configuration
overrides it.

Idle expiry and the stopping transition share the stay-policy lock. Once idle
shutdown is committed, the controller refuses stay promotion and new admission.
It cannot acknowledge a running owner and then exit from an earlier idle
decision.

`LocalSubject` accepts matching kernel UIDs on Linux and macOS, or matching
client SIDs on Windows. Missing identity refuses access. The socket transport
also refuses peers when its platform implementation cannot supply credentials.
`APIConn.PeerIdentity` exposes that verified identity to the shared controller.
The connection owns its identity snapshot. Changing an input UID or a returned
UID cannot change the account attached to the connection. The API listener
checks `LocalSubject` before yielding a connection. Unix uses kernel UIDs.
Windows uses owner-only named pipes and the client's identification-token SID.
The [transport contract](LOCAL-TRANSPORT.md) lists the native tests still required.
Admission state and election files use `fssecure`. Admission state accepts only
the current schema.

`ResolveAdmissionPolicy` reads the complete machine configuration through
`userconfig.ReadConfig`. The shared configuration package owns decoding and
validation. The runtime converts its typed admission settings to scheduling and
Jev policy, preserving explicit zero values and the defaults for omitted fields.

<!-- design:begin -->

## Integration requirements

Detached and scheduled startup callers use the shared lifecycle, preserve its
startup error and require a running result. They refuse a service binding for
another home before starting it. The separate trigger-consumer command, process,
election, store opener, dispatcher and renewal loop are removed. `localws.Run`,
which started another consumer beside a second store owner, is removed too.
The retained local HTTP handler fixtures use their own test server.
The CLI service resolver and sealed local intake remain production bindings.


`localcontroller.Config.Bind` still needs the production shared services.
Its `State` interface supplies readiness, the owned secrets read, startup
reconciliation and live activity observation. The binder
also supplies the authenticated API and dashboard handlers, provisioned claimant
and native host services. `Open` constructs the shared queue from those inputs.
The SQLite composition tests substitute these foreign boundaries. They prove
store ownership, admission cancellation and leased maintenance. Shared policy
routes and the production activity projection remain unbound.
`TestNativeStoreOwnerW8/local_controller` exercises `Config.Open`, leased
reconciliation, the shared queue, native host, persisted completion and joined
shutdown. It supplies fixture state/HTTP, enrollment, version policy and source
services. It does not establish production activity or peer authorization.

The controller composition supplies `LocalController` from the shared
`pkg/controller` implementation. Its `Run` method reports all four activity
counts for activity outside the runtime's own HTTP connections and joins its
consumer and maintenance children before returning. The runtime adds its
dashboard connection count. Its API server consumes the kernel identity on
`APIConn`. Its dashboard server retains browser authentication. Local runs
continue to execute in the CLI.

The final state projection, peer authorization and native service binder remain
unbound here. `runners.NewStoreRunner` supplies the shared consumer over the elected
store. The runtime tests still substitute its surrounding services.
The final store also needs an activity projection and a typed incompatible-store error. No
compatibility router or second store opener supplies those gaps.

`client.QueryLocalController` discovers the owner through the authenticated
admission socket. It reports readiness, PID, version, stay policy and the four
activity counts. The query uses a health-probe connection and leaves the idle
deadline unchanged. `client.SetLocalStay` changes the owner's policy and checks
its acknowledgement. Neither operation starts a process or replaces another
version. Missing sockets, unreachable owners, unsupported operations and stalled
replies remain distinct errors. The existing `client.Stop` uses admission drain
and waits for the socket and election lock to close.

The admission wire's `local_control` request carries an optional `stay` value.
Only an elected `LocalRuntime` serves it. A health probe may read status but
cannot change policy. `local_control_status` is the process snapshot, not an
installed-service claim. Native service inspection supplies that separate fact.

`client.StartLocalDashboard` requests dashboard startup from an existing owner.
Its `local_dashboard` request waits for `local_dashboard_ack` after the serving
loop reaches readiness. An identical address reuses the listener. A bind failure
returns the owner's error and leaves the controller available. Probe connections
and admission-only daemons refuse the operation.

The dashboard request uses one connection. Peer disconnect cancels the readiness
wait, and the handler joins its reader before returning. Shutdown closes the
connection before joining handlers because a readiness waiter may be waiting
for that same runtime to finish. Service failure therefore reaches a foreign
caller as a transport error. The runtime retains the service error through
shutdown. Client cancellation closes its connection and returns its context
error, including cancellation during the switch from handshake to readiness.
The handshake has the existing probe budget. Readiness follows the caller's
context because dashboard startup can take longer than a health probe.

`client.NewLocalLifecycle` reuses a ready owner or starts the captured absolute
executable through `wingd supervise --home HOME --version VERSION`. Its
`wingd run` child owns the controller. Startup uses the admission client's shared
start claim and election checks, with takeover disabled. Readiness requires the
local controller status to report running within the existing startup budget.
The request can finish while that detached process continues hosting other runs.

The adapter binds one home before any owner query. An empty start Home selects
that bound home. Another home is refused before mutation. It promotes stay,
attaches the dashboard and drains the elected process through the authenticated
socket. Status retains installed-unit information. Unreachable, stalled or
unsupported owners remain errors. Version skew does not replace a ready owner.

Standalone uses the supplied in-process runtime and refuses an existing owner.
If another process wins its election, the lifecycle joins local cleanup before
querying the winner. A joined cleanup failure remains an error. Starting or
draining local processes retain lifecycle operations until cleanup finishes.

The foreground `wingd run` child still needs the shared state, HTTP and native
host services supplied through `localcontroller.Config.Bind`.
`NewLocalRuntime` refuses a foreign election before opening its store. Windows
transport still refuses API access when it cannot supply a client SID.

Drain preserves cancellation and unreachable-owner errors and requires a
`drain_ack` on the current admission protocol. A successful drain then waits for
the socket and election lock to close. It never starts a replacement process.

`NewManagedUnitManager` implements `ManagedUnitManager` for one captured state
home. Linux uses systemd user units. macOS uses LaunchAgents. Controller units
run foreground `wingd run --stay --home <absolute-home>`. Timers run
`__tick --home <absolute-home>`. Windows refuses native installation with
`--stay` guidance. Inspect reads the installed argv and release without opening
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

The W9 dashboard, schedule, update and doctor adapters still need to supply this
manager. The foreground process still needs the shared-controller factory.
The foreground CLI uses `runWingdRunCommand` and the shared flag registry. Its
former second runtime constructor is removed. Election and factory-failure
coverage now use that same command path.
File and service-manager fixtures exercise these ports. They do not establish
native login, crash, shutdown or Windows behavior.

`TestLocalRuntime*` exercises election-before-open, readiness, failure cleanup,
restart after failed startup, all four activity holds and idle probes. It uses
in-memory listeners and fake shared-controller services. `TestLocalControl*`
exercises the new wire exchange with `net.Pipe`, including probe restrictions,
response validation, cancellation and stalled peers. `TestLocalDashboard*`
covers starting the UI on an existing owner, failed binds, readiness and joined
shutdown. Its wire tests also cover probe refusal, peer cancellation and service
failure during a remote start. The wire shape and round-trip tests cover the
status/stay and dashboard messages.
`TestLocalLifecycle*` in the client package exercises foreign-owner composition,
election races, home binding, concurrent reuse and refusal paths through the
lifecycle interface with process-boundary fakes. `TestLocalDrain*` uses
`net.Pipe` to cover acknowledgement validation and cancelled handshakes.
The process/store
scenarios remain open: local controller
idle lifecycle, health-probe process exit, dashboard install/update, Windows
transport identity and local orphan recovery. These require the real controller
and platform fixtures.

<!-- design:end -->
