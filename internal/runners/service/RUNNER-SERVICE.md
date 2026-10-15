# Runner services

`Install` writes a systemd user service on Linux or a launchd agent on macOS.
Native command inspection and file publication share `service/unitfile` with
local controller units and timers. Files are private. Registration checks
refuse another loaded file before install, refresh or uninstall can act on its
unit name or label. Install, refresh and uninstall share that registration check.
Install and refresh also share the private file writer. Preflight and mutation
require absolute native registration paths. Literal command arguments preserve dollar signs, spaces
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

The unified loop requests triggers and jobs by default. A here loop with RunID
and omitted Kinds requests only its trigger, selecting the shared client's named
`/runs/{run}/claim` route. It refuses foreign awards and returns an uncertain claim
error without retrying for another one-time bearer. Claim windows and reclaim
authority remain server decisions.

Successful version-policy refreshes keep a pending claim on its original request.
Policy refusal, drain and lifecycle deadlines cancel and join that request.
A committed award returned during cancellation still drains through its host.
The stop result survives award delivery, and the loop takes no more claims.
Managed updates consume the update policy result and retain other failures,
including the caller's cancellation cause during cleanup or replacement.

Hosting accepts direct trigger, here and work awards with matching unit purposes
and claim fences. Bootstrap credentials require the Cloud exchange path. Renewal,
source preparation and execution share the earliest binding, owner or parent
credential deadline, capped by the caller's deadline. Absent deadlines remain
unbounded. The keeper owns renewable lease expiry. The host owns per-attempt
deadlines, so application retries can continue under the same unit claim.

`Hosting` creates one shared controller client for each award. Its bearer callback
retains that award's capability and refuses a cancelled lookup. The same client
confirms the award, renews through the keeper and reaches run or job preparation.
The runner's root client stays in the claim loop. Preparation receives the full
server award, including granted runner identity, engine and storage inputs.
Keeper renewals leave `Executing` false because the host owns execution start.
A job renewal cannot report execution before its start transition commits.

`Inspect` reads the executable and arguments from the actual registration.
It uses the runner's shared parser to identify the effective config, including
repeated flags and values that resemble flags.
Systemd inspection requires one complete `ExecStart` command. Leading empty
reset directives remain valid. A malformed nonempty command cannot be hidden
by a later valid command.
Refresh preserves empty reset directives when replacing the executable.
Plist inspection decodes one dictionary and a string-only `ProgramArguments`
array. Duplicate commands, incomplete XML and extra documents are refused.
`FuzzPlistArguments` covers literal argument recovery and those refusals.
`Refresh` replaces the executable in the installed command. It preserves all
arguments, including repository overrides and repeated config flags, and keeps
the unit's environment, log paths and native policy. Inspection, refresh and
uninstall share one registration reader and config check.
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

`RunnerInstallation.Apply` serializes the complete update. Its stages are private,
so replacement, refresh and restart cannot run out of order.
`NativeInstallationConfig` keeps the registration's configuration and credential
paths in its callbacks. Drain stops claims and joins renewing hosts. Refresh
receives the installed runner version and also calls the controller/timer refresh
boundary with the CLI's own executable and version. Restart uses the same runner
registration. Replacement preserves symlinks and executable permissions. Windows
retains the running image as `.old` until the next update.

Component tests use real registration files and fake service managers on both
platforms. The acceptance component sends the parsed installed argv through
the unified runner parser. Native login, restart and self-update proofs need
platform fixtures, final credential enrollment and the shared host composition.
