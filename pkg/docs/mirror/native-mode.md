# Local dashboard

The public dashboard commands use the local controller lifecycle. See the
[dashboard reference](cli-dashboard.md) for their grammar and output formats.

This build still needs native runtime, managed-service and log-source bindings.
Dashboard commands report `unavailable` when those bindings are absent. They do
not fall back to a separate dashboard server or open another state store.

Doctor reports an incomplete controller inspection when its lifecycle binding is
absent. Its project toolchain, hook survey and competing-install checks still
run. An incomplete inspection is an error, even when those machine checks pass.

<!-- design:begin -->
## Controller lifecycle contract

The local controller owns the state store, admission, queue and dashboard in one
home. Local pipeline execution stays in the CLI process. Standalone CI uses the
same runtime in process and must not open a home already owned by a controller.

Runs, queued work, held leases and open dashboard connections keep the controller
alive. With none of those holds, it may exit after the shared idle interval.
Dashboard start with `--stay` keeps it up until stopped. Health probes alone do
not keep it alive.

Login services on Linux and macOS use a foreground controller process with an
absolute home. The CLI adapter forwards home, version, stay and standalone mode
to that runtime and waits for it to return. Signal cancellation reaches the same
runtime context. Startup reconciliation and shutdown draining belong to the
runtime. Windows supports stay mode and daemon schedules, without a login-service
installer.

Managed schedule timers use one stable hidden entrypoint with the captured home.
Machine updates refresh both timer and controller units. The controller runtime,
service manager, timer and log bindings still need native integration evidence.

The foreground contract has no fields for explicit budget, headroom or alternate
state-db flags. The CLI reports `unavailable` for those explicit flags instead of
dropping them. Machine admission settings remain the runtime's responsibility.
<!-- design:end -->

See [machine settings](machine-config.md), [local admission](admission.md) and
[machine updates](updates.md) for the related configuration and contracts.
