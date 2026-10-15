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

The runtime contract includes `AwaitReady`, `Status`, `SetStay` and `Drain`
because `Run` alone cannot establish readiness, report an existing controller,
or stop through admission. `AwaitReady` must report API readiness only after
the store opens and startup reconciliation finishes. `Run` must acquire sole
store ownership before opening state.db, including standalone mode. A failed
election may attach to an existing controller in on-demand mode. Standalone
mode must refuse it. Status reads must use the owner and never open another
writing store handle.

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

The controller composition must implement `LocalRuntime`, feed activity counts
into the daemon, mount the dashboard and API, consume local triggers and fail
started orphan runs. Local runs continue to execute in the CLI. The lifecycle
and its fake runtime tests do not establish those integrations.

`ManagedUnitManager` remains the service-management port. Its policies specify
`dashboard start --stay` and `__tick`. The CLI contract must settle how installed
units select `LocalStartOptions.Home` through arguments and how a managed
controller stays attached to systemd or launchd. The declared commands do not
provide those semantics. Installation, refresh and OS unit tests require that
contract before a renderer can be connected.

`TestLocalControllerW8Components` covers policy, identity and lifecycle calls
with a fake runtime. The process/store scenarios remain open: local controller
idle lifecycle, health-probe process exit, dashboard install/update, Windows
transport identity and local orphan recovery. These require the real controller
and platform fixtures.

<!-- design:end -->
