# Local controller lifecycle

`NewLocalLifecycle` coordinates a home-bound `LocalRuntime`. The runtime is bound to the same home before lifecycle construction, so its
status queries address that owner. `Run` rejects options naming another home
before opening a store. Concurrent callers share startup. A successful start waits for `AwaitReady` and a running status.
Ending an on-demand startup request does not cancel a controller that has become
ready. Standalone mode follows its caller's context because that process owns
the controller lifetime.
A failed startup cancels and joins both the runtime and its readiness waiter
before returning. The runtime port must honor cancellation after cleanup.
`StartDashboard` can promote an existing controller to stay mode. `Stop` waits
for the runtime's admission drain and shutdown.

`NewLocalRuntime` implements the process lifetime behind `LocalRuntime`. It
acquires the admission election before invoking `LocalRuntimeConfig.Open`.
That factory opens one store and returns the shared controller, including its
run-store adapter, local queue consumer and leased maintenance. `Reconcile`
finishes orphan recovery and supplies the initial activity before any listener
starts. A losing election opens no store and returns `ErrNotElected`.

Readiness waits for reconciliation, admission startup and the API serving loop.
When `DashboardAddress` is set, it also waits for the dashboard serving loop.
An empty address serves only `api.sock`. `AwaitReady` waits for an active start
and cannot reuse a completed start's readiness or failure. `LocalLifecycle`
observes the matching `Run` result, so startup failures and lost elections
return instead of waiting forever.

Any serving or worker failure cancels the runtime. Drain uses admission's
draining state, stops listeners and joins workers, API requests and admission
finalizers before closing the store. Election ownership lasts through store
close. The daemon also joins its sampling, idle and diagnostic workers.
Transient startup failures can be retried. An incompatible store remains a
doctor finding.

Jev admission reads its optional `TYPESAFE_API_KEY` through the elected
controller's secrets store. An explicit advisor takes precedence. A missing or
unreadable key is logged and retains the existing admission fallback. Startup
does not open a second secrets store.

The daemon accepts live controller counts through `SetLocalActivity`. Runs,
queued triggers, held leases and dashboard connections prevent idle exit.
Admission holders and waiting runs also remain active. `SetStay` pins the
daemon. Health probes, repeated unchanged counts and idle API connections do
not extend its idle deadline. The timeout comes from
`limits.LocalControllerIdleTimeout` unless the existing admission configuration
overrides it.

`LocalSubject` accepts matching kernel UIDs on Linux and macOS, or matching
client SIDs on Windows. Missing identity refuses access. The socket transport
also refuses peers when its platform implementation cannot supply credentials.
Admission state and election files use `fssecure`. Admission state accepts only
the current schema.

<!-- design:begin -->

## Integration requirements

The controller composition supplies `LocalController` from the shared
`pkg/controller` implementation. Its `Run` method reports all four activity
counts, including dashboard connections, and joins its consumer and maintenance
children before returning. Its API server consumes the kernel identity on
`APIConn`. Its dashboard server retains browser authentication. Local runs
continue to execute in the CLI.

The final controller routes, capability authorization, store reads, leased
maintenance and local consumer are still unbound here. Their implementations
must replace the controller fixture used by the runtime tests. The final store
also needs an activity projection and a typed incompatible-store error. No
compatibility router or second store opener supplies those gaps.

Cross-process owner discovery, status, stay promotion and drain still need the
final owner client. `NewLocalRuntime` controls its own process. It refuses a
foreign election rather than opening that process's store. Windows transport
still refuses API access when it cannot supply a client SID.

`ManagedUnitManager` remains the service-management port. Its policies specify
`dashboard start --stay` and `__tick`. The CLI contract must settle how installed
units select `LocalStartOptions.Home` through arguments and how a managed
controller stays attached to systemd or launchd. The declared commands do not
provide those semantics. Installation, refresh and OS unit tests require that
contract before a renderer can be connected.

`TestLocalRuntime*` exercises election-before-open, readiness, failure cleanup,
restart after failed startup, all four activity holds and idle probes. It uses
in-memory listeners and fake shared-controller services. The process/store
scenarios remain open: local controller
idle lifecycle, health-probe process exit, dashboard install/update, Windows
transport identity and local orphan recovery. These require the real controller
and platform fixtures.

<!-- design:end -->
