<!-- GENERATED from the CLI command registry by `sparkwing commands --format markdown --output plain`. Do not edit by hand; regenerate with `bash bin/gen-cli-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# CLI reference: sparkwing runners

Every `sparkwing runners` command, flag, and argument, generated from the CLI's own command registry. All command groups are indexed in [cli-reference.md](cli-reference.md).

## `sparkwing runners`

Manage runners

### Subcommands

- `add` -- Register a runner
- `install-service` -- Install a runner service under a dedicated OS user (root)
- `uninstall-service` -- Remove a dedicated-user runner service (root)
- `list` -- List runners and their capacity
- `pause` -- Stop this machine's runner from claiming new work
- `resume` -- Let this machine's paused runner claim work again
- `remove` -- Remove a runner

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |

## `sparkwing runners add`

Register a runner

Without --session-stdin a personal runner can be approved in the browser: the command needs stdin and stderr on a terminal, prints a code and the controller's /device page, and a signed-in account types the code there, checks the runner name, team and grants, and approves; the runner is enrolled under that account's authority and the command prints the team before writing config. --team makes the controller refuse an approver from another team. Shared runners, reserved pools, --trust, --platform and --launcher always need --session-stdin. Browser approval rejects allowed_labels. Set runner labels through --session-stdin enrollment: a team administrator sets them on shared runners, and a member may set them on a personal runner, where they route only that member's own manual runs. For headless setup, pipe sparkwing-controller admin session from the controller pod, run by an existing team administrator, to --session-stdin. Shared runner registration requires team administration. --personal registers capacity for only your runs. Members may enroll personal machines when the team's members_can_add_machines setting is true (the default). When false, enrollment requires team administration. Turning it off ends personal machines that members without administration enrolled, and turning it back on does not revive them. The session and its CSRF proof stay in memory. After fetching the session CSRF proof, the CLI attempts logout on enrollment success or failure. Use a dedicated session because logout ends it for every client. A token minted by sparkwing login ends when its approving browser session signs out, so supplying that same session invalidates the CLI token when logout succeeds. Copy the enrollment cookie from a separate private browser window. --reserved-for requires team-admin runner registration support. --trust sets which runs a shared runner takes (verified-default or any); unset, a launcher holding a pool with a reserved secret takes only verified default-branch work, including unmodified admin-armed default-branch schedules. --launcher enrolls only claims.launch for a shared reserved pool and skips the persistent runner service. Verified-default pools and pools holding reserved secrets require launcher credentials, including with --trust=any. --repos accepts repeatable, comma-separated patterns and is required for shared runners; a runner scoped by it takes only repository-proven work, so it is a trusted host whose jobs can produce a pass that sw.FromProvenRunAtCommit counts. On a developer's machine the trust boundary is the OS account the runner service runs as: any process running as that user can read the runner's credential and report a pass directly, and shares its HOME, Go configuration and build cache, Sparkwing home and PATH, so enroll a workstation this way only for people the team would let bypass the required gate check, or enroll it with --service-user so the service runs under a dedicated OS user with its own HOME and SPARKWING_HOME. Such a runner also runs any pushed branch of its repositories as that user. A shared runner enrolled into an account whose home holds SSH keys, cloud credentials or browser profiles prints a warning naming them, and --repos patterns spanning several repositories print a warning that those repositories can claim each other's jobs and secrets. --service-user asks for sudo before minting, creates the OS user when it is absent (no password, no login shell, home under /var/lib/sparkwing on Linux or /private/var/sparkwing on macOS), writes the runner section and credential into that user's own config, and installs a systemd system service or launchd daemon running as it. It refuses root, your own account, members of root-equivalent groups, accounts whose home sits under /home, /Users or /root, and a new name an existing group already holds. Its runner does not share this machine's local admission, so --max-concurrent is the only cap on its load. Omit it for personal runners: they claim only their owner's asserted work and never repository-proven runs. A runner serving several repositories lets a job from one act as the runner and claim another repository's jobs in the team, with their secrets; scope --repos to one repository, or to repositories that trust each other, to isolate them. JSON reports personal scope, credential metadata and service state without the bearer. Plain emits the runner name.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--runner RUNNER` | Runner name or selection. (required) |
| `--personal` | Offer this machine only to your runs, including Cloud overflow. |
| `--session-stdin` | Read an independent account session from stdin without saving it. After fetching its CSRF proof, attempt logout on enrollment success or failure. Copy its cookie from a separate private browser window so logout does not invalidate your CLI login token. |
| `--repos PATTERN` | Repository patterns this runner takes jobs for; a job on a runner serving several repositories can claim the others' jobs and their secrets, so scope it to repositories that trust each other. Its passes count for CI, and every process running as the service's OS user can forge one, so scope a workstation only for people the team would let bypass the required gate check. Repeatable; comma-separated values are accepted. |
| `--label LABEL` | Configure and authorize this label at enrollment. Claims cannot add labels. A team administrator can revoke and re-enroll the runner to change the allowed set. Self-hosted operators can also use sparkwing-controller admin runner-labels. Repeatable; comma-separated values are accepted. |
| `--max-concurrent N` | Most jobs this runner works on at once, written to its config. A job waiting in `RunAndAwait` does not count until its child finishes, so a one-slot runner can run the child. Must be at least 1. |
| `--config PATH` | Machine config file to write the runner section into, the standard Sparkwing config file by default. |
| `--force` | Replace an existing object. |
| `--no-service` | Write the runner config and credential without installing or starting the runner service. |
| `--profile NAME` | Controller connection profile from your config. Without it, or with --profile=, the command uses the local controller. |
| `--reserved-for POOL` | Reserved pools this shared runner holds, which lets a --launcher runner take work and secrets restricted to those pools; a persistent runner holding a pool that a reserved secret names takes no work, and it requires team administration. Repeatable; comma-separated values are accepted. |
| `--trust CLASS` | Which runs this shared runner takes within its --repos scope, which already admits only repository-proven work: verified-default takes only verified default-branch work (signed pushes to the default branch and unmodified fires of admin-armed default-branch schedules), and any takes every proven run; a runner holding a pool with a reserved secret, or a verified-default runner with any pool, takes nothing unless enrolled with --launcher, and unset such a launcher takes only verified default-branch work. Values: verified-default, any. |
| `--platform PLATFORM` | Record a shared Kubernetes runner at admin enrollment; a runner in a pod stops at its first poll without it. Values: kubernetes. |
| `--launcher` | Enroll only claims.launch for a shared reserved pool and skip the persistent runner service. |
| `--team TEAM` | With browser approval, accept an approval only from this team. |
| `--service-user USER` | Run the runner under this dedicated, unprivileged OS user instead of your own account, so its jobs cannot read your home. Creates the user when absent and installs a system service running as it; asks for sudo before minting the credential. |

## `sparkwing runners install-service`

Install a runner service under a dedicated OS user (root)

The privileged step of sparkwing runners add --service-user, which runs it through sudo with the enrollment as JSON on stdin. Requires root. Creates or reuses the OS user named by --service-user, writes the runner section and credential as that user into its own home, copies sparkwing-runner into its ~/.local/bin, records the runner name, controller and token prefix in a root-only directory beside the account homes, and installs a systemd system service (User=, ProtectHome=yes, NoNewPrivileges=yes, PrivateTmp=yes) or a launchd daemon (UserName) running as it. The service PATH leaves out entries under /home, /Users and /root. JSON reports the account, config path, service file and left-out PATH entries.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--service-user USER` | Dedicated OS user the runner service runs as. (required) |

## `sparkwing runners list`

List runners and their capacity

Read the same runner details from the local controller or a selected profile. The local controller lists this machine as one local runner named after its host, with the runner.labels the daemon read at start and a last_seen of the read time. JSON retains granted identity, reservations, labels, capacity, active jobs and headroom. Plain emits names; quiet JSON emits name objects.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--profile NAME` | Controller connection profile from your config. Without it, or with --profile=, the command uses the local controller. |
| `-q, --quiet` | Print only runner names. |

## `sparkwing runners pause`

Stop this machine's runner from claiming new work

Pause the runner enrolled on this machine: it finishes held jobs and claims no new work, including planning, while it keeps polling so it stays online. The pause is a file beside the runner credential, local to this machine; runners list on this machine shows the runner paused, while other machines and the console see it idle. It survives a runner restart. JSON reports the runner, paused and whether the state changed. Plain emits the runner name.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--config PATH` | Machine config file holding the enrolled runner section, the standard Sparkwing config file by default. |

## `sparkwing runners remove`

Remove a runner

Require the enrolled controller, optional runner name and explicit runner credential kind before stopping the service and revoking. With --service-user, stop the system service through sudo, then check the controller and runner name it reports before revoking, and print how to delete the OS user. JSON retains confirmed revocation. Plain emits the runner name.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--config PATH` | Machine config file holding the runner section to remove, the standard Sparkwing config file by default. |
| `--no-service` | Revoke the runner credential without stopping the installed runner service. |
| `--profile NAME` | Controller connection profile from your config. Without it, or with --profile=, the command uses the local controller. |
| `--runner RUNNER` | Runner name or selection. |
| `--service-user USER` | Remove the runner enrolled with runners add --service-user: stop its system service through sudo, then revoke its token. |

## `sparkwing runners resume`

Let this machine's paused runner claim work again

Resume the runner enrolled on this machine after runners pause; it claims work again on its next poll, without a restart. JSON reports the runner, paused and whether the state changed. Plain emits the runner name.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--config PATH` | Machine config file holding the enrolled runner section, the standard Sparkwing config file by default. |

## `sparkwing runners uninstall-service`

Remove a dedicated-user runner service (root)

The privileged step of sparkwing runners remove --service-user, which runs it through sudo and then revokes the token it reports. Requires root. Stops and removes the system service of the OS user named by --service-user first, then reports the runner name, controller and token prefix from the root-owned enrollment record runners install-service wrote, falling back to that user's config. When neither names the token, it says the service is stopped and how to find and revoke the token. The OS user and its home remain. JSON reports the account, home, runner, controller and token prefix.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--service-user USER` | Dedicated OS user the runner service runs as. (required) |
