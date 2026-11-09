# Local dashboard

The local controller serves the dashboard whenever it runs.
`sparkwing dashboard open` starts it when needed and prints a one-time sign-in
link. Visit that link in your browser. `sparkwing daemon status` reports the
controller and dashboard address; `sparkwing daemon stop` stops both.

On Linux and macOS, install a login service with:

```sh
sparkwing daemon install
```

Linux uses systemd user services, so run this from a login session with a working
`systemctl --user status`. macOS uses launchd. Windows starts the controller on
demand and has no login-service installer. See the
[dashboard reference](cli-dashboard.md) for command grammar and output formats.

Dashboard commands report `unavailable`, naming the missing piece, when this
platform cannot supply the local controller lifecycle, managed units or log
source. They do not fall back to a separate dashboard server or open another
state store.

Doctor reports an incomplete controller inspection when its lifecycle binding is
absent. Its project toolchain and hook survey checks still run. An incomplete inspection is an error, even when those machine checks pass.

<!-- design:begin -->
## Controller lifecycle contract

The local controller owns the state store, admission, queue and dashboard in one
home. Local pipeline execution stays in the CLI process. Standalone CI uses the
same runtime in process and must not open a home already owned by a controller.

The local controller remains running while idle. Use `sparkwing daemon stop` to
stop it.

Login services on Linux and macOS use a foreground controller process with an
absolute home. The CLI adapter forwards home, version and standalone mode
to that runtime and waits for it to return. Signal cancellation reaches the same
runtime context. Startup reconciliation and shutdown draining belong to the
runtime. Windows supports the foreground controller and daemon schedules, without a
login-service installer.

Managed schedule timers use one stable hidden entrypoint with the captured home.
Machine updates refresh both timer and controller units. The controller runtime,
service manager, timer and log bindings are described in
[local execution](local-execution.md).

The foreground contract has no fields for explicit budget, headroom or alternate
state-db flags. The CLI reports `unavailable` for those explicit flags instead of
dropping them. Machine admission settings remain the runtime's responsibility.
<!-- design:end -->

See [machine settings](machine-config.md), [local admission](admission.md) and
[machine updates](updates.md) for the related configuration and contracts.
