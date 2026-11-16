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
start at login and restart on failure. Signal-only cancellation of `start` or
`warm` exits 0 after shutdown; a runtime or cleanup failure still exits 1.
Windows installation returns
`ErrUnsupported` so the caller can use its existing service manager.

Enrollment captures PATH for both service managers and prepends the installed
Go toolchain's `GOROOT/bin` when `GOTOOLCHAIN=local go env GOROOT` resolves it.
This avoids version-manager shim selection after the service changes directory.
The probe cannot download a toolchain. Native assignment and XML escaping keep
paths literal. `TestRunnerServiceRetainsEnrolledGoToolchain` covers enrollment
through registration on both platforms with a Go shim and a fake service manager.

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
Plist inspection decodes one dictionary and a string-only `ProgramArguments`
array. Duplicate commands, incomplete XML and extra documents are refused.
`FuzzPlistArguments` covers literal argument recovery and those refusals.
Inspection, registration refresh and uninstall share one registration reader
and config check. `RefreshRegistration` reloads a matching registration without
restarting its process, so a runner replacing itself keeps running. Nothing
outside the runner's own update restarts the service: `sparkwing update`
replaces only the CLI, and the runner updates itself from its controller.

`RunnerInstallation.Apply` serializes the complete update. Its stages are private,
so replacement, refresh and restart cannot run out of order.
`NativeInstallationConfig` keeps the registration's configuration and credential
paths in its callbacks. Drain stops claims and joins renewing hosts. The refresh hook
receives the installed runner version and also calls the controller/timer refresh
boundary with the CLI's own executable and version. Restart uses the same runner
registration. Replacement preserves symlinks and executable permissions. Windows
retains the running image as `.old` until the next update.

Component tests use real registration files and fake service managers on both
platforms. The acceptance component sends the parsed installed argv through
the unified runner parser. Native login, restart and self-update proofs need
platform fixtures, final credential enrollment and the shared host composition.

## Dedicated-account services

`InstallSystem` writes a systemd system unit (`sparkwing-runner-NAME.service`
with `User=`, `ProtectHome=yes`, `NoNewPrivileges=yes`, `PrivateTmp=yes`,
`ProtectSystem=full` and `UMask=0077`) or a launchd daemon
(`com.sparkwing.runner.NAME` with `UserName`) for one account. Unit names carry
the account, so a dedicated-account runner and a login runner can coexist. A
same-named unit loaded from another file, or a file without the managed marker,
is refused. The macOS daemon logs under `/Library/Logs`, because launchd opens
log files as root.

`EnsureAccount` reuses an account or creates one with no password, no login
shell and a 0700 home under `/var/lib/sparkwing` (Linux, `useradd --system`) or
`/private/var/sparkwing` (macOS, `dscl` with a hidden id from 400 to 499). It
refuses root, the invoking account, members of root-equivalent groups and
homes under `/home`, `/Users` or `/root`, because `ProtectHome=yes` would hide
them and they suggest a person's account. It refuses to create an account whose
name an existing group holds, before any `useradd` or `dscl` write. `ServicePath` drops PATH entries
under those roots.

The CLI writes the account's config, credential and runner binary in a child
process running as the account, so root never writes through paths the account
controls. Root records the runner name, controller and token prefix in
`.enrollments/NAME.json`, a root-only directory beside the account homes.
Removal stops the service before reading anything the account can change and
takes the token from that record. `system_test.go` covers rendering, command order, refusals and
foreign units with fake managers; a native proof needs root on each platform.
