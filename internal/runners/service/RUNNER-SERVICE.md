# Runner services

`Install` writes a systemd user service on Linux or a launchd agent on macOS.
Native command inspection and file publication share `service/unitfile` with
local controller units and timers. Files are private. Registration checks
refuse another loaded file before install, refresh or uninstall can act on its
unit name or label. Literal command arguments preserve dollar signs, spaces
and percent signs without a shell.
Both execute `sparkwing-runner start --config PATH` in the foreground. They
start at login and restart on failure. Windows installation returns
`ErrUnsupported` so the caller can use its existing service manager.

The registration names the existing config path. It does not read or copy
credentials. Files without the managed marker remain untouched. `Uninstall`
also preserves a registration naming another config.

Enrollment writes `runner.credentials_dir` and `runner.repos`. Machine startup
reads the private `agent-token` file once and retains it in a transport callback.
Warm service startup uses the shared credential reader so Kubernetes projected
files keep working. A failed enrollment revokes its new authority, removes its
new private files and service file, and restores the previous runner
section. Existing service files and unrelated settings survive cleanup.

`Inspect` reads the executable and arguments from the actual registration.
It uses the runner's shared parser to identify the effective config, including
repeated flags and values that resemble flags.
`Refresh` preserves the config path while updating the executable and command.
An absent registration stays absent. Linux reloads systemd and uses
`try-restart`, which leaves stopped services stopped. macOS reloads only an
agent whose launchd state is running. An inactive agent's updated file takes
effect when launchd next loads it. Errors preserve the managed file for a
retry and reach the caller. All service-manager commands receive its context.

Refresh must run outside the target service because launchd stops that process
before loading the replacement. A runner self-updater must hand native service
work to an external owner through its installation hooks. Refresh alone does
not complete that process handoff, refresh controller services or migrate
schedule timers.

Component tests use real registration files and fake service managers on both
platforms. The acceptance component sends the parsed installed argv through
the unified runner parser. Native login, restart and self-update proofs need
platform fixtures, final credential enrollment and the shared host composition.
