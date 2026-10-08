package main

import (
	"runtime"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func helpExampleScratchDir(name string) string {
	if runtime.GOOS == "windows" {
		return `%TEMP%\` + name
	}
	return "/tmp/" + name
}

var cmdSparkwing = Command{
	Path:     "sparkwing",
	Synopsis: "sparkwing -- CI/CD pipelines written in Go",
	Description: `Sparkwing is a self-hosted pipeline runner. Pipelines are Go
programs in a repo's .sparkwing/ directory, triggered by git hooks,
webhooks, schedules, or manual invocation. Use 'sparkwing run
<pipeline>' to invoke one; 'sparkwing pipeline list' / 'describe'
for agent-facing discovery.

Three flags go before any verb:
  -C DIR          run as if started in DIR (the .sparkwing search starts there)
  --profile NAME  select a profile; the verb must accept --profile
  -o FORMAT       pretty | json | plain, for verbs that print a document

Without --profile, verbs that read runs fall back to SPARKWING_PROFILE, then
the project's defaults.profile. Verbs that change state elsewhere (secrets,
crons, runs cancel, cluster, and similar) act locally unless --profile names
the controller.`,
	SubcommandOrder: []string{"info", "pipeline", "run", "runs", "repos", "crons", "queue", "cache", "daemon", "version", "update", "serve", "doctor", "cloud", "cluster", "fleet", "secrets", "configure", "debug", "docs", "examples", "commands", "completion"},
	Examples: []Example{
		{"Run a pipeline (positional shortcut)", "sparkwing run fictional-build"},
		{"First command an agent should run", "sparkwing info --for-agent"},
		{"List every invocable (agents)", "sparkwing pipeline list -o json"},
		{"Inspect one pipeline's full metadata", "sparkwing pipeline describe --name fictional-release -o json"},
		{"Bootstrap + scaffold your first pipeline in a new repository", "sparkwing pipeline new --name release"},
		{"Start the local dashboard", "sparkwing serve start"},
		{"List another checkout's pipelines", "sparkwing -C ~/code/other pipeline list"},
	},
}

var cmdDaemon = Command{
	Path:     "sparkwing daemon",
	Synopsis: "Inspect or refresh the local admission daemon",
	Description: `The admission daemon starts on demand when a pipeline needs it. Status never
starts one. Restart replaces only an answering daemon with this installed
build, using the same drain, durable lease, and reattachment path as automatic
version takeover; a stopped daemon stays stopped. Stop drains an answering
daemon and launches no successor. The supervisor keeps a daemon whose heartbeat
counter advances during failed health probes. A whole-machine pause restarts the
stale window when the supervisor resumes. See [Diagnosing admission](diagnosing-admission.md)
for event records and dump paths.`,
	SubcommandOrder: []string{"status", "events", "restart", "stop", "recover-state"},
	Examples: []Example{
		{"Machine-readable status", "sparkwing daemon status -o json"},
		{"Refresh only if already running", "sparkwing daemon restart"},
		{"Stop it and leave it stopped", "sparkwing daemon stop"},
	},
}

var cmdDaemonEvents = Command{
	Path: "sparkwing daemon events", Synopsis: "Read retained admission events without starting the daemon",
	Description: "Reads the size-capped journal in the daemon directory. Lists the newest 50 matching records and reports how to fetch older ones. Child attach records show requested and resolved parents; cancel records show affected and blocked runs. Unreadable records are skipped and counted on stderr. Human output names the directory when no events are retained. JSON output is one record per line.\n\n--explain --run ID explains that run's admission history in sentences instead, including descendant node slots and attached children; JSON output keeps the structured records.",
	Flags:       []FlagSpec{{Name: "run", Argument: "ID", Desc: "Filter by run ID", Group: "Input"}, {Name: "since", Argument: "DURATION", Desc: "Lookback duration", Group: "Input"}, {Name: "kind", Argument: "KIND", Desc: "Record kind (repeatable)", Group: "Input"}, {Name: "incarnation", Argument: "N", Desc: "Daemon incarnation", Group: "Input"}, {Name: "limit", Argument: "N", Desc: "Maximum records (default 50; 0 for all)", Group: "Input"}, {Name: "offset", Argument: "N", Desc: "Matching records to skip from newest", Group: "Input"}, {Name: "explain", Desc: "With --run, explain the run's admission history in sentences", RequiresFlags: []string{"run"}, Group: "Input"}, {Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty|json|plain (default: pretty on TTY, json when piped)", Group: "Output"}},
	GroupOrder:  []string{"Input", "Output", "Other"},
	Examples:    []Example{{"Events for one run", "sparkwing daemon events --run abc -o json"}, {"Explain one run's admission history", "sparkwing daemon events --run abc --explain"}},
}

var cmdDaemonStop = Command{
	Path:     "sparkwing daemon stop",
	Synopsis: "Drain an answering wingd and leave it stopped",
	Description: `Drains an answering daemon through the same wire request a restart uses, then
waits for its admission socket to go quiet and its election lock to be
released. No successor is launched, and the supervisor exits with the worker it
started, so the pair stays down until the next run needs a daemon. An absent
daemon is a no-op and exits zero.

A run still holding admission finishes against the store it already opened.`,
	Flags: []FlagSpec{
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty|json|plain (default: pretty on TTY, json when piped)", Group: "Output"},
	},
	GroupOrder: []string{"Input", "Output", "Other"},
	Examples: []Example{
		{"Stop this machine's daemon", "sparkwing daemon stop"},
		{"Machine-readable result", "sparkwing daemon stop -o json"},
	},
}

var cmdDaemonRecoverState = Command{
	Path:     "sparkwing daemon recover-state",
	Synopsis: "Preserve unreadable daemon state after its holders stop",
	Description: `Fail-closed recovery for a daemon that cannot parse its durable state. The
unreadable bytes may describe leases whose runs still hold host capacity, so
first stop or verify those runs, then pass --yes. Recovery holds the
daemon election lock, moves state.json to a state.json.corrupt-<time> forensic
copy, and never discards readable state.`,
	Flags: []FlagSpec{
		{Name: "yes", Desc: "Confirm every run described by the unreadable state has stopped", Required: true, Group: "Safety"},
	},
	GroupOrder: []string{"Input", "Safety", "Other"},
	Examples: []Example{
		{"Recover only after verifying the described runs stopped", "SPARKWING_HOME=/path/to/home sparkwing daemon recover-state --yes"},
	},
}

var cmdDaemonStatus = Command{
	Path:     "sparkwing daemon status",
	Synopsis: "Report whether wingd is running and which build it serves",
	Description: `Reports daemon reachability and source build identity. An absent daemon exits
zero. An unreachable socket returns an error.

running_revision identifies the build. missing_requirements lists store
requirements the binary cannot interpret. Additive migrations permit older
binaries to keep serving. Older daemons use daemon_schema_version and
store_schema_version for comparison.

store_schema_error reports a store the CLI could not read. daemon_store_ready
and daemon_store_error describe the daemon's own store handle. The daemon
evicts runs whose terminal state it cannot check. store_path identifies the
store it tried to open.

daemon_store_skew identifies an incompatible store schema. Pipeline runs use
standalone storage for that mismatch and fail for other store errors. Older
daemons omit fields they cannot report.

api_socket names the controller API socket. api_ready reports whether it is
bound, and api_error explains a binding failure. A daemon that supports the
API but cannot bind its socket is unhealthy. Older daemons omit api_ready.

artifact_store_error reports failure to open the configured cache. The daemon
continues serving with artifact routes disabled.`,
	Flags: []FlagSpec{
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty|json|plain (default: pretty on TTY, json when piped)", Group: "Output"},
	},
	GroupOrder: []string{"Input", "Output", "Other"},
	Examples: []Example{
		{"Machine-readable status", "sparkwing daemon status -o json"},
	},
}

var cmdDaemonRestart = Command{
	Path:     "sparkwing daemon restart",
	Synopsis: "Refresh an answering wingd to this installed build",
	Description: `Refresh an answering daemon when its build differs from this installed build.
With --force, drain and replace an answering daemon even when the builds
match. Existing holders reconnect and reattach through durable leases. If no
daemon is running, nothing is started.`,
	Flags: []FlagSpec{
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty|json|plain (default: pretty on TTY, json when piped)", Group: "Output"},
		{Name: "force", Desc: "Replace the daemon even when it already serves this build", Group: "Behavior"},
	},
	GroupOrder: []string{"Input", "Behavior", "Output", "Other"},
	Examples: []Example{
		{"Refresh only if already running", "sparkwing daemon restart"},
		{"Replace an answering daemon", "sparkwing daemon restart --force"},
		{"Machine-readable result", "sparkwing daemon restart -o json"},
	},
}

var cmdInfo = Command{
	Path:     "sparkwing info",
	Synopsis: "Describe Sparkwing and the selected project",
	Description: `Reports
the CLI version, whether the current directory is inside a
sparkwing project (and how many pipelines it has), whether the Go
toolchain is on PATH, a curated list of next-step commands, and
the docs URL. When a project declares a git hook that is not
firing, repairing it is the first next step.

If the pipeline catalog cannot be read, JSON reports project.pipelines_error.
Pipeline counts are unavailable when that field is present.

Use -o json for structured output that an agent can parse, or
-o plain to emit one next-step command per line for shell
pipelines (head -n1 yields the most-likely next command).`,
	Flags: []FlagSpec{
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty | json | plain", Default: "pretty on TTY, json when piped", Group: "Output"},
		{Name: "for-agent", Desc: "Emit current discovery context for one agent wake (no ANSI, no extras)", Group: "Output"},
		{Name: "first-time", Desc: "Print the post-install onboarding card (used by install.sh; re-runnable any time)", Group: "Output"},
	},
	GroupOrder: []string{"Output", "Other"},
	Examples: []Example{
		{"Human-readable card", "sparkwing info"},
		{"Agent-readable record", "sparkwing info -o json"},
		{"Load current agent discovery", "sparkwing info --for-agent"},
		{"Reprint the post-install onboarding card", "sparkwing info --first-time"},
	},
}

var cmdCluster = Command{
	Path:     "sparkwing cluster",
	Synopsis: "Operate and inspect the sparkwing cluster",
	Description: `Inspect and operate a controller's executors, triggers, admission, users and
tokens. Select the controller with --profile NAME; connect one with
'sparkwing cloud connect' and check its health with 'sparkwing cloud status
--cluster'.

'worker' executes queued triggers on this machine. Manage secrets with
'sparkwing secrets' and the local dashboard with 'sparkwing serve'.`,
	SubcommandOrder: []string{"agents", "runners", "worker", "triggers", "users", "tokens", "limits", "concurrency", "object-store"},
	Examples: []Example{
		{"Cluster health summary", "sparkwing cloud status --profile prod --cluster"},
		{"List fleet agents", "sparkwing cluster agents list --profile prod"},
	},
}

var cmdCloud = Command{
	Path:     "sparkwing cloud",
	Synopsis: "Connect this machine to a sparkwing controller",
	Description: `One command joins a controller: 'connect' verifies the
controller answers, mints a user token when you hand it an admin credential,
and writes the profile that every other command selects with --profile.
'status' reports what that connection authenticates as. 'disconnect' removes
the profile and revokes its token.

Nothing here edits config.yaml by hand. Enroll this machine as a runner with
'sparkwing cluster runners add'.`,
	SubcommandOrder: []string{"connect", "status", "disconnect"},
	Examples: []Example{
		{"Connect with a one-time admin token", "sparkwing cloud connect --controller https://api.sparkwing.example --admin-token-stdin"},
		{"Report the connection", "sparkwing cloud status --profile api-sparkwing-example"},
	},
}

var cmdCloudConnect = Command{
	Path:     "sparkwing cloud connect",
	Synopsis: "Write the profile that reaches a controller",
	Description: `Verifies the controller answers its health route, then writes
a profile carrying the controller URL and a token.

--admin-token-stdin reads an admin credential from stdin and mints a user
token with it, carrying runs.read, runs.write, triggers.read, logs.read and
approvals.write. The admin credential is never stored; only the minted token
reaches config.yaml. --token-stdin stores a token you already hold. Neither
flag connects to a controller serving unauthenticated.

--name defaults to the controller host with every character outside a-z0-9
turned into a dash, so https://api.sparkwing.example becomes
api-sparkwing-example. An existing profile of that name is never replaced
without --force, because the token it holds stays live until it is revoked.

--set-default writes defaults.profile into this repository's
.sparkwing/sparkwing.yaml, so runs in this checkout select the connection with
no flag. The name resolves against the project's own profiles: block first and
config.yaml second, so the token stays out of the checkout.

The command closes with the dashboard URL the controller announces and the
probes 'sparkwing cloud status' runs.

--no-probe writes the profile without contacting the controller: no
reachability check, no mint, no probes. Use it to register a controller that
is not up yet, or one that serves unauthenticated (omit both token flags).`,
	Flags: []FlagSpec{
		{Name: "controller", Argument: "URL", Desc: "Controller base URL", Required: true, Group: "Identity"},
		{Name: "name", Argument: "NAME", Desc: "Profile name (default: derived from the controller host)", Group: "Identity"},
		{Name: "admin-token-stdin", Desc: "Read an admin token from stdin and mint a user token with it", ConflictsWith: []string{"token-stdin"}, Group: "Credential"},
		{Name: "token-stdin", Desc: "Read an already-minted user token from stdin", ConflictsWith: []string{"admin-token-stdin"}, Group: "Credential"},
		{Name: "scope", Argument: "CSV", Desc: "Comma-separated scopes for the minted token", Default: "runs.read,runs.write,runs.control,triggers.read,logs.read,approvals.write", Group: "Credential"},
		{Name: "set-default", Desc: "Set defaults.profile in this project's .sparkwing/sparkwing.yaml", Group: "Project"},
		{Name: "force", Desc: "Replace an existing profile of that name", Group: "Project"},
		{Name: "no-probe", Desc: "Write the profile without contacting the controller", ConflictsWith: []string{"admin-token-stdin"}, Group: "Project"},
	},
	GroupOrder: []string{"Identity", "Credential", "Project", "Other"},
	Examples: []Example{
		{"Connect with a one-time admin token", "sparkwing cloud connect --controller https://api.sparkwing.example --admin-token-stdin"},
		{"Connect and make it this repository's default", "sparkwing cloud connect --controller https://api.sparkwing.example --name prod --admin-token-stdin --set-default"},
		{"Store a token someone minted for you", "sparkwing cloud connect --controller https://api.sparkwing.example --token-stdin"},
		{"Register an unauthenticated local controller offline", "sparkwing cloud connect --controller http://127.0.0.1:4344 --name local --no-probe"},
	},
}

var cmdCloudStatus = Command{
	Path:     "sparkwing cloud status",
	Synopsis: "Report the connection, its principal, and the probes",
	Description: `Prints the selected profile, its controller, the principal
and scopes the controller reports for its token, the announced dashboard URL,
and the controller, auth, logs and gitcache probes. A profile with no
controller reports its storage probes alone. Exits non-zero when a probe
fails; a missing optional logs service warns without failing, and a controller
that announces no cache pod URL omits the gitcache probe.

--cluster answers "is this cluster alive?" for an operator. It adds the probes
that read /api/v1/agents, /api/v1/triggers (status=claimed) and
/api/v1/runs?since=24h, in three sections:

  CONNECTIVITY  controller / auth / logs / gitcache
  FLEET         agents (connected vs stale)
  QUEUE         stuck triggers + recent-run success rate

It exits 1 only when a probe fails; warnings such as a low success rate or
stale agents leave the exit code at 0.`,
	Flags: []FlagSpec{
		{Name: "profile", Argument: "NAME", Desc: "Profile naming the connection to report", Group: "Input"},
		{Name: "cluster", Desc: "Add the fleet and queue probes an operator reads", Group: "Input"},
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: json|table", Group: "Output"},
	},
	GroupOrder: []string{"Input", "Output", "Other"},
	Examples: []Example{
		{"Report the connection", "sparkwing cloud status --profile prod"},
		{"Machine-readable status", "sparkwing cloud status --profile prod -o json"},
		{"Connectivity, fleet and queue health of a cluster", "sparkwing cloud status --profile prod --cluster"},
	},
}

var cmdCloudDisconnect = Command{
	Path:     "sparkwing cloud disconnect",
	Synopsis: "Revoke the connection's token and drop the profile",
	Description: `Revokes the profile's token on its controller, then removes
the profile. A revoke this credential is not allowed to make leaves the token
live and names the prefix and the command that finishes the job, so a
connection is never dropped silently.

The profile's own token revokes only when it carries admin;
--admin-token-stdin supplies one that does. A prefix the controller reports as
anything but a user token is refused, naming what it found. --keep-token drops
the profile and touches no credential.`,
	Flags: []FlagSpec{
		{Name: "name", Argument: "NAME", Desc: "Profile name to disconnect", Required: true, Group: "Identity"},
		{Name: "admin-token-stdin", Desc: "Read an admin token from stdin and revoke with it", Group: "Credential"},
		{Name: "keep-token", Desc: "Remove the profile without revoking its token", Group: "Credential"},
	},
	GroupOrder: []string{"Identity", "Credential", "Other"},
	Examples: []Example{
		{"Disconnect and revoke with an admin token", "sparkwing cloud disconnect --name prod --admin-token-stdin"},
		{"Drop the profile and leave the token alone", "sparkwing cloud disconnect --name prod --keep-token"},
	},
}

var cmdConfigure = Command{
	Path:     "sparkwing configure",
	Synopsis: "Configure laptop-local settings",
	Description: `Configure this machine. 'init' prepares the configuration directory and
reports its contents. 'profiles' lists and edits controller connections;
'sparkwing cloud' adds, checks and removes them. 'sparkwing repos'
registers local repositories.

Manage controller users and tokens with 'sparkwing cluster'.
Manage secrets with 'sparkwing secrets'.`,
	SubcommandOrder: []string{"init", "profiles"},
	Examples: []Example{
		{"First-time laptop setup", "sparkwing configure init"},
		{"Status of laptop config", "sparkwing configure init -o json"},
		{"List profiles", "sparkwing configure profiles list"},
	},
}

var cmdConfigureInit = Command{
	Path:     "sparkwing configure init",
	Synopsis: "Set up ~/.config/sparkwing/ and report laptop-level config status",
	Description: `Idempotent setup + status command for laptop-level
sparkwing config. Creates ~/.config/sparkwing/ if it doesn't exist,
then reports which config files are present (config.yaml,
secrets.key), the running CLI + Go toolchain version,
and a curated list of next-step commands.

Pairs with the per-project flow: use this one on a fresh laptop
after install, then run 'sparkwing pipeline new --name <name>'
inside each project to scaffold .sparkwing/ + your first pipeline
in one step (no separate init needed).

Re-running on an already-set-up laptop re-applies 0700 to
~/.config/sparkwing/ and reports each config file's mode, naming any
that group or other users can read. --dry-run skips both the mkdir
and the permission fix so the command reports existing state.

Run inside a sparkwing project, it also reports whether this
checkout's declared git hooks fire, and names the command that
arms them. It installs nothing and changes no git configuration.`,
	Flags: []FlagSpec{
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty | json | plain", Default: "pretty on TTY, json when piped", Group: "Output"},
		{Name: "dry-run", Desc: "Probe + report without creating or tightening ~/.config/sparkwing/", Group: "Behavior"},
	},
	GroupOrder: []string{"Output", "Behavior", "Other"},
	Examples: []Example{
		{"First-time laptop setup", "sparkwing configure init"},
		{"Status of laptop config (agent-readable)", "sparkwing configure init -o json"},
		{"Probe without writing anything", "sparkwing configure init --dry-run"},
	},
}

var cmdVersion = Command{
	Path:     "sparkwing version",
	Synopsis: "Inspect versions (CLI, SDK, sparks)",
	Description: `Reports the installed CLI version + build provenance, the
latest published release on GitHub (with a short network
fetch -- bounded by ~3s, fail-soft when offline), and the
.sparkwing/go.mod SDK pin + any sparks-* libraries declared
alongside it.

CLI comparison uses the same read-only release metadata and provenance
checks as 'sparkwing update --check', within one ~3s network budget.
cli_status and cli_reason distinguish a verified comparison from a local
build or missing metadata. SDK pins retain their semver comparison.

--offline skips the network fetch entirely; -o json emits the
structured report; -o plain prints semver lines (CLI then
latest) for shell pipelines.`,
	SubcommandOrder:    []string{"hold"},
	SubcommandOptional: true,
	Flags: []FlagSpec{
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty | json | plain", Default: "pretty on TTY, json when piped", Group: "Output"},
		{Name: "offline", Desc: "Skip the network fetch for latest release", Group: "Behavior"},
		{Name: "changelog", Desc: "Print the changelog for the installed release", Group: "Behavior"},
	},
	GroupOrder: []string{"Output", "Behavior", "Other"},
	Examples: []Example{
		{"Human-readable card", "sparkwing version"},
		{"Agent-readable record", "sparkwing version -o json"},
		{"CLI semver only (scripts)", "sparkwing version -o plain | head -n1"},
		{"Local-only (no network)", "sparkwing version --offline"},
		{"Changelog for the installed release", "sparkwing version --changelog"},
		{"Update the CLI binary", "sparkwing update --cli"},
		{"Bump the SDK pin in this project", "sparkwing repos update --in-place"},
	},
}

var cmdVersionHold = Command{
	Path:     "sparkwing version hold",
	Synopsis: "Show, set, or clear the operator ceiling on CLI upgrades",
	Description: `A version hold is an operator-set ceiling that the tool enforces:
once set, 'sparkwing update' and 'sparkwing update --cli'
refuse to install anything beyond it, so an agent cannot perform a
major upgrade against operator instruction.

The ceiling shape controls its reach:

  vMAJOR.MINOR       caps a whole minor series -- every patch of that
                     minor is allowed, the next minor is refused
                     (v9.8 allows v9.8.7 and excludes v9.9.0, for example).
  vMAJOR.MINOR.PATCH exact ceiling -- nothing above that patch installs.

With no flags, prints the current hold and where it is set. The hold
persists in the user config (XDG_CONFIG_HOME or ~/.config/sparkwing/
version-hold). Releases beyond the hold still show in 'sparkwing version' so the operator sees what is
being deferred.

SPARKWING_HOME does not move this file; it is the state, cache and
logs root, and the hold is machine-wide even though the toolchains it
governs live under that root. A --set or --clear from a command
running under a home of its own is refused rather than applied to the
machine's hold.`,
	Flags: []FlagSpec{
		{Name: "set", Argument: "VERSION", Desc: "Set the ceiling (vMAJOR.MINOR or vMAJOR.MINOR.PATCH)", Group: "Action"},
		{Name: "clear", Desc: "Remove the hold so upgrades are unrestricted", Group: "Action"},
	},
	GroupOrder: []string{"Action", "Other"},
	Examples: []Example{
		{"Show the current hold", "sparkwing version hold"},
		{"Hold the minor series at v9.8", "sparkwing version hold --set v9.8"},
		{"Pin an exact ceiling", "sparkwing version hold --set v9.8.7"},
		{"Lift the hold", "sparkwing version hold --clear"},
	},
}

var cmdUpdate = Command{
	Path:     "sparkwing update",
	Synopsis: "Update the CLI binary",
	Description: `Updates the CLI binary; --cli names that target explicitly. It resolves the
latest published GitHub release unless --version names a specific release
tag. Bump this project's .sparkwing/go.mod SDK pin with
'sparkwing repos update --in-place'.

CLI updates verify Ed25519 signatures, the release digest and the version the
staged binary reports before atomic replacement. Verification failure is
terminal. --force permits a downgrade;
--override-hold crosses an operator CLI hold.

--check reads installed identity and release metadata without installing,
running Go, changing module files or writing caches. It honors --version. Exit 0 means current or ahead, 1 means an update is
available, and 2 means unknown, diverged or a check failure. Local SDK
replacements and unverified CLI provenance are reported as unknown.
A check does not verify downloadable assets or promise installation will work.

Output is pretty on a terminal and NDJSON otherwise. Checks emit one
update_check record; successful updates emit one update receipt. Progress
and failures go to stderr. Plain checks print the status word; plain updates
print the resulting version.`,
	Flags: []FlagSpec{
		{Name: "cli", Desc: "Update the CLI binary (the only target; optional)", Group: "Target"},
		{Name: "check", Desc: "Compare the selected target without changing it", Group: "Behavior"},
		{Name: "force", Desc: "Allow a CLI downgrade", Group: "Behavior"},
		{Name: "override-hold", Desc: "Cross an operator CLI version hold", Group: "Behavior"},
		{Name: "version", Argument: "TAG", Desc: "Canonical release tag; omit for latest published release", Group: "Input"},
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "pretty | json | plain", Group: "Output"},
	},
	GroupOrder: []string{"Target", "Input", "Behavior", "Output", "Other"},
	Examples: []Example{
		{"Check for a newer CLI release", "sparkwing update --check"},
		{"Update the CLI", "sparkwing update --cli"},
		{"Check a specific CLI release", "sparkwing update --cli --check --version v9.8.7"},
		{"Downgrade the CLI", "sparkwing update --cli --version v9.7.6 --force"},
	},
}

var cmdCommands = Command{
	Path:             "sparkwing commands",
	Synopsis:         "Index of every command: one path and synopsis per line",
	HideFromComplete: true,
	Description: `Search command paths and synopses with --query; every word must match.
--path narrows to a subtree, with or without the leading sparkwing.
Results are lexical, at most 40 by default. JSON ends with a kind:page
record reporting total, returned, truncated and next_cursor. Continue
with --cursor and the same filters, or --limit 0 for every match.

Rows carry path, synopsis and full-tree subcommand_count. Read a selected
command with <path> --help. Hidden commands require --include-hidden.
Plain prints paths only, with continuation on stderr.

--format markdown exports the full reference and rejects query/pagination
flags. --split-dir writes generated files.`,
	Flags: []FlagSpec{
		{Name: "query", Short: "q", Argument: "TEXT", Desc: "Match every word against paths and synopses", Group: "Selection"},
		{Name: "limit", Argument: "N", Desc: "Maximum records; 0 returns every remaining match", Default: "40", Group: "Selection"},
		{Name: "cursor", Argument: "CURSOR", Desc: "Continue after next_cursor with the same filters and binary version", Group: "Selection"},
		{Name: "format", Argument: "markdown", Desc: "Export the full command reference as Markdown", Group: "Output"},
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty | json | plain", Default: "pretty on TTY, json when piped", Group: "Output"},
		{Name: "split-dir", Argument: "DIR", Desc: "With --format markdown: write one page per top-level command group into DIR (plus a cli-reference.md index), pruning stale generated pages", Group: "Output"},
		{Name: "path", Argument: "PREFIX", Desc: "Only emit commands at or under PREFIX, matched by whole path components, with or without the leading 'sparkwing' (runs, sparkwing runs, runs list, and similar paths); a prefix matching nothing is an error", Group: "Filter"},
		{Name: "include-hidden", Desc: "Also emit Hidden:true commands (default: skip)", Group: "Filter"},
	},
	GroupOrder: []string{"Selection", "Filter", "Output", "Other"},
	Examples: []Example{
		{"Find status commands", "sparkwing commands --query status"},
		{"Just the pipelines subtree", "sparkwing commands --path pipeline"},
		{"The same subtree, fully qualified", "sparkwing commands --path \"sparkwing pipeline\""},
		{"All paths, one per line", "sparkwing commands --limit 0 -o plain"},
	},
}

const queueListingDescription = `Reports the local admission daemon's resource capacity, usage, and queue in
two sections: running work, then queued work in admission order.

A running row carries the repository, elapsed time, charge, and, from the
run's measured p50 profile, its expected remaining time and the clock time it
is expected to finish. A queued row carries its position, priority, cost, how
long it has waited, the resource it waits on, and, from the daemon's
admission simulation, when it is expected to start and finish. Attached child
runs appear under their parent. Connected runs that hold no resources have
separate rows.

An estimate exists only where the measurements behind it do, and a cell
without one says which measurement is missing. "unmeasured" is a row the
daemon has no profile for. "past p50" is a run that has already outlived the
profile it has, which no longer predicts it. "unknown" is a queued row the
daemon cannot place, because a run ahead of it has no estimate of its own.
None of the three is replaced by a guess. The header counts the queued runs
with no profile, because those are the ones that starve.
'sparkwing queue priority' re-ranks a queued run.

A stalled holder includes a cancellation command:
'sparkwing runs cancel --run <id>'. Inspect the holder before cancelling it.
The queue command only reports state.

Output is pretty on a terminal and JSON when piped. Select JSON explicitly
with -o json, or tab-separated records with -o plain. JSON carries each
estimate as milliseconds from the snapshot and as an RFC3339 clock time;
plain carries humanized durations and RFC3339 clock times.

An absent daemon reports an empty queue and exits 0. An unreachable daemon
reports the connection failure and exits 4; its queue state is unknown.

With --profile NAME, the view reads that profile's controller and shows each
concurrency key, its holders and waiters, and registered runner capacity.`

var cmdQueue = Command{
	Path:     "sparkwing queue",
	Synopsis: "Inspect local admission holders, connections, and waiters",
	Description: queueListingDescription + `

'sparkwing queue' and 'sparkwing queue list' print the same listing.`,
	SubcommandOrder:    []string{"list", "priority"},
	SubcommandOptional: true,
	Flags:              queueListingFlags,
	GroupOrder:         []string{"Output", "System", "Other"},
	Examples: []Example{
		{"Show the current queue", "sparkwing queue list"},
		{"Agent-readable snapshot", "sparkwing queue list -o json"},
		{"One record per line for shell pipelines", "sparkwing queue list -o plain"},
		{"Inspect a controller's admission state", "sparkwing queue list --profile prod"},
		{"Move a queued run to the front", "sparkwing queue priority --run build-123 --set front"},
	},
}

var cmdQueueList = Command{
	Path:     "sparkwing queue list",
	Synopsis: "List running and queued work with expected start and finish",
	Description: queueListingDescription + `

This is the same output as 'sparkwing queue'.`,
	Flags:      queueListingFlags,
	GroupOrder: []string{"Output", "System", "Other"},
	Examples: []Example{
		{"Show the current queue", "sparkwing queue list"},
		{"Agent-readable snapshot", "sparkwing queue list -o json"},
		{"One record per line for shell pipelines", "sparkwing queue list -o plain"},
	},
}

var queueListingFlags = []FlagSpec{
	{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty | json | plain", Group: "Output"},
	{Name: "profile", Argument: "NAME", Desc: "Inspect this profile's controller instead of the local daemon", Group: "System"},
}

var cmdQueuePriority = Command{
	Path:     "sparkwing queue priority",
	Synopsis: "Re-rank a run that is already queued for local admission",
	Description: `Changes the admission priority of a run the local daemon is
already arbitrating, without restarting it. Higher priorities admit
first and ties keep their arrival order, exactly as at launch. A raise
that frees the run to start admits it immediately.

--set takes an integer, or ` + "`front`" + ` / ` + "`back`" + `. The relative forms
resolve against the waiters that are not part of this run: front is one
above the highest other waiter's priority, back is one below the lowest,
and both fall back to a step either side of zero when nothing else is
waiting. Asking for front twice is therefore stable instead of an
escalating race with the run's own rank.

One run is several admission participants -- the run itself, and each of
its nodes admitting on its own. All of them move together, and the new
rank is remembered, so a node admitting later lands at it too instead of
at the priority its plan carried. The daemon forgets that rank once the
run has released every lease and has no participant waiting.

When the run already holds a lease there is nothing to re-order: the
command says so, and the change reaches only the node admissions the run
has yet to make.

Exits 0 whether the rank moved or was already what you asked for, 1 when
the daemon does not know the run -- a submitted run the consumer has not
claimed yet is not queued here, so it is not visible to local admission --
and 4 when the daemon's socket cannot be reached at all.`,
	Flags: []FlagSpec{
		{Name: "run", Argument: "ID", Desc: "Run id to re-rank", Required: true, Group: "Identity"},
		{Name: "set", Argument: "VALUE", Desc: "New priority: an integer, front, or back", Required: true, Group: "Identity"},
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty | json | plain", Group: "Output"},
	},
	GroupOrder: []string{"Identity", "Output", "System", "Other"},
	Examples: []Example{
		{"Send a queued run to the front", "sparkwing queue priority --run build-123 --set front"},
		{"Park a run behind everything else", "sparkwing queue priority --run nightly-42 --set back"},
		{"Set an explicit rank", "sparkwing queue priority --run build-123 --set 7"},
		{"Agent-readable answer", "sparkwing queue priority --run build-123 --set 7 -o json"},
	},
}

var cmdDocs = Command{
	Path:     "sparkwing docs",
	Synopsis: "Embedded user docs (offline)",
	Description: `The docs ship inside this binary and match its version.
Start with docs search --query <question> for a page of short snippets,
then docs read --topic <slug> --section <start_line> for one selected hit.
Docs list pages through topic metadata; --query narrows that index.
JSON indexes end with a typed page summary and continuation cursor.

Selected reads return JSON document records when piped. Explicit
--output plain prints the original Markdown. Use --web and --version
on list/read when comparing another published version.
docs list --guides names the task-sized topic sets, docs list --versions
the doc versions this CLI knows, and docs read --all is an explicit
exhaustive export. The --web fetch cache lives under ` + "`sparkwing cache info --docs`" + `.`,
	SubcommandOrder: []string{"list", "read", "search", "migrations"},
	Examples: []Example{
		{"List topic metadata", "sparkwing docs list"},
		{"List topic metadata (agent-readable)", "sparkwing docs list -o json"},
		{"Read one topic", "sparkwing docs read --topic pipelines"},
		{"Read one topic at a specific version (online)", "sparkwing docs read --topic pipelines --version v0.3.0 --web"},
		{"Find docs that mention warm pool", "sparkwing docs search --query \"warm pool\""},
		{"List migration guides this CLI knows", "sparkwing docs migrations"},
		{"Pipe every guide up to v0.4.0 into context", "sparkwing docs migrations --to v0.4.0"},
		{"List every version available online", "sparkwing docs list --versions --web"},
	},
}

var cmdDocsList = Command{
	Path:     "sparkwing docs list",
	Synopsis: "Enumerate every doc topic",
	Description: `List topic metadata in lexical slug order, at most 40 rows by default.
--query matches words in slugs, titles and summaries before pagination.
JSON ends with a kind:page record; continue with --cursor and the same
filters. --limit 0 emits every match. Bodies belong to docs read.
The embedded copy matches this binary; --web reads another version.

--guides lists the task-sized topic sets instead: each guide is a named set
of narrative topics that answer one task together, and
` + "`sparkwing docs read --guide NAME`" + ` returns the whole set in one call.
The generated references (sdk-reference, cli-reference) are lookup tables
rather than pages to read end to end; reach those with ` + "`sparkwing docs search`" + `.

--versions lists the binary's embedded documentation version and its
migration-guide versions; with --web it merges in the versions published on
sparkwing.dev. Use a returned version with ` + "`sparkwing docs read --web --version`" + `.
--guides takes only --output; --versions takes --web and --no-cache.`,
	Flags: []FlagSpec{
		{Name: "guides", Desc: "List the task-sized topic sets (`docs read --guide`) instead of topics", Group: "Selection"},
		{Name: "versions", Desc: "List the doc versions this CLI knows (and sparkwing.dev with --web) instead of topics", Group: "Selection"},
		{Name: "query", Short: "q", Argument: "TEXT", Desc: "Match words in slug, title and summary", Group: "Selection"},
		{Name: "limit", Argument: "N", Desc: "Maximum records; 0 returns every remaining match", Default: "40", Group: "Selection"},
		{Name: "cursor", Argument: "CURSOR", Desc: "Continue after next_cursor with the same filters and binary version", Group: "Selection"},
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty | json | plain", Default: "pretty on TTY, json when piped", Group: "Output"},
		{Name: "web", Desc: "Fetch from sparkwing.dev instead of the embedded corpus", Group: "Source"},
		{Name: "version", Argument: "vX.Y.Z", Desc: "Doc version (vX.Y.Z or latest). Defaults to this CLI's embedded version.", Group: "Source"},
		{Name: "no-cache", Desc: "With --web, bypass the on-disk cache for this invocation", Group: "Source"},
	},
	GroupOrder: []string{"Selection", "Source", "Output", "Other"},
	Examples: []Example{
		{"Human-readable table", "sparkwing docs list"},
		{"Agent-readable", "sparkwing docs list -o json"},
		{"Slug-per-line for shell loops", "sparkwing docs list --limit 0 -o plain"},
		{"List the v0.3.0 corpus from sparkwing.dev", "sparkwing docs list --web --version v0.3.0"},
		{"What guide sets exist", "sparkwing docs list --guides"},
		{"Doc versions embedded in this CLI", "sparkwing docs list --versions"},
		{"Every version available online", "sparkwing docs list --versions --web -o json"},
	},
}

var cmdDocsRead = Command{
	Path:     "sparkwing docs read",
	Synopsis: "Read one document",
	Description: `Reads the named topic, or one embedded section selected by its start_line
from docs search (--section). Section selection requires --topic and
cannot combine with --guide or --web. Piped output is one JSON document record;
--output plain prints raw Markdown. The slug is
the filename under /docs/ minus .md (run ` + "`sparkwing docs list`" + ` to
see them all). Nested topics use slash-separated names.

Default source is the binary's embedded corpus. Use --web to fetch
from sparkwing.dev, optionally pinned to --version vX.Y.Z or
--version latest.

--all reads every embedded document, one JSON record per page when piped;
--output plain prints the full Markdown corpus with page headers. It takes no
other selection or source flag.`,
	Flags: []FlagSpec{
		{Name: "all", Desc: "Read every embedded document (explicit exhaustive export)", Group: "Selection"},
		{Name: "section", Argument: "START_LINE", Desc: "Read one embedded section returned by search", RequiresFlags: []string{"topic"}, ConflictsWith: []string{"guide", "web"}, Group: "Selection"},
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty | json | plain (pretty on a terminal, json when piped)", Group: "Output"},
		{Name: "topic", Argument: "NAME", Desc: "Topic name from docs list", Group: "Selection"},
		{Name: "guide", Argument: "NAME", Desc: "Read a task-sized set of topics instead of one (`sparkwing docs list --guides`)", Group: "Selection"},
		{Name: "web", Desc: "Fetch from sparkwing.dev instead of the embedded corpus", Group: "Source"},
		{Name: "version", Argument: "vX.Y.Z", Desc: "Doc version (vX.Y.Z or latest). Defaults to this CLI's embedded version.", Group: "Source"},
		{Name: "no-cache", Desc: "With --web, bypass the on-disk cache for this invocation", Group: "Source"},
	},
	GroupOrder: []string{"Selection", "Source", "Other"},
	Examples: []Example{
		{"Read the getting-started page", "sparkwing docs read --topic getting-started"},
		{"Everything needed to write a pipeline, one call", "sparkwing docs read --guide authoring"},
		{"Pipe through a pager", "sparkwing docs read --topic pipelines --output plain | less"},
		{"Read v0.3.0's pipelines page online", "sparkwing docs read --topic pipelines --version v0.3.0 --web"},
		{"Always fetch the freshest version", "sparkwing docs read --topic pipelines --version latest --web"},
		{"Explicit exhaustive document export", "sparkwing docs read --all"},
	},
}

var cmdDocsSearch = Command{
	Path:     "sparkwing docs search",
	Synopsis: "Find the section that answers a question",
	Description: `Find matching sections, ranked before pagination. Every query word must
match; heading matches rank ahead of body matches. The default page has
20 hits with topic, heading, line range and a short snippet, never bodies.
JSON ends with a kind:page record; --cursor continues the same query.
--limit 0 returns all matches. Use the same binary for continuation.

Read one hit with docs read --topic <slug> --section <start_line>.
--body explicitly includes full bodies for this page. --topics lists
matching topic metadata instead of sections.`,
	Flags: []FlagSpec{
		{Name: "limit", Argument: "N", Desc: "Maximum records; 0 returns every remaining match", Default: "20", Group: "Selection"},
		{Name: "cursor", Argument: "CURSOR", Desc: "Continue after next_cursor with the same filters and binary version", Group: "Selection"},
		{Name: "query", Short: "q", Argument: "TEXT", Desc: "Search terms (every token must match)", Required: true, Group: "Selection"},
		{Name: "body", Desc: "Print each matching section in full instead of a snippet", Group: "Selection"},
		{Name: "topics", Desc: "List whole matching topics instead of sections", Group: "Selection"},
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty | json | plain", Default: "pretty on TTY, json when piped", Group: "Output"},
	},
	GroupOrder: []string{"Selection", "Output", "Other"},
	Examples: []Example{
		{"Where a PR trigger is defined", "sparkwing docs search --query pull_request"},
		{"Read the matching sections in full", "sparkwing docs search -q ApprovalConfig --body"},
		{"Compact snippets for agents", "sparkwing docs search -q approval -o json"},
		{"Matching topic metadata", "sparkwing docs search -q \"warm pool\" --topics"},
	},
}

var cmdDocsMigrations = Command{
	Path:     "sparkwing docs migrations",
	Synopsis: "Per-version migration guides (agent-friendly)",
	Description: `With no flag, lists each migration guide bundled with this binary in
descending semver order, with date, size and one-line summary parsed from
docs/migrations/README.md; --output json is an array of
{version, date, summary, slug, bytes}. When this CLI is older than the newest
embedded guide a one-line stderr note suggests updating.

--version V (or a positional vX.Y.Z) reads that one guide as a JSON document
record when piped; --output plain prints its raw Markdown. Cross-doc links are
rewritten into ` + "`sparkwing docs read --topic <slug>`" + ` form.

--from A and --to B concatenate every guide with a version greater than A and
at most B, in ascending order, into one blob: Markdown output separates guides
with horizontal rules and names the range in its heading. --from defaults to
v0.0.0 and --to to the highest embedded version, so --from v0.0.0 alone is
every guide this CLI knows.

--web reads sparkwing.dev instead of the embedded corpus, for the list, one
guide, or a range.`,
	PosArgs: []PosArg{
		{Name: "[vX.Y.Z]", Desc: "Migration guide version to read, when --version is not supplied"},
	},
	Flags: []FlagSpec{
		{Name: "version", Argument: "vX.Y.Z", Desc: "Read this one guide. Positional fallback accepted.", ConflictsWith: []string{"from", "to"}, Group: "Selection"},
		{Name: "from", Argument: "vX.Y.Z", Desc: "Concatenate the guides after this version (exclusive; default v0.0.0)", Group: "Selection"},
		{Name: "to", Argument: "vA.B.C", Desc: "Concatenate the guides up to this version (inclusive; default = latest embedded version)", Group: "Selection"},
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty | json | plain", Default: "pretty on TTY, json when piped", Group: "Output"},
		{Name: "web", Desc: "Fetch from sparkwing.dev instead of the embedded corpus", Group: "Source"},
		{Name: "no-cache", Desc: "With --web, bypass the on-disk cache for this invocation", Group: "Source"},
	},
	GroupOrder: []string{"Selection", "Source", "Output", "Other"},
	Examples: []Example{
		{"List embedded migration guides", "sparkwing docs migrations"},
		{"Version-per-line for shell loops", "sparkwing docs migrations -o plain"},
		{"Read one guide", "sparkwing docs migrations --version v0.4.0"},
		{"Positional shortcut", "sparkwing docs migrations v0.4.0"},
		{"Every guide upgrading from v0.3.0 to v0.4.0", "sparkwing docs migrations --from v0.3.0 --to v0.4.0"},
		{"Every guide this CLI knows (one-shot agent context)", "sparkwing docs migrations --from v0.0.0"},
		{"Read v0.5.0 from sparkwing.dev", "sparkwing docs migrations --version v0.5.0 --web"},
		{"Every release on sparkwing.dev", "sparkwing docs migrations --web"},
	},
}

var cmdCache = Command{
	Path:     "sparkwing cache",
	Synopsis: "Inspect or trim the compiled pipeline binary cache",
	Description: `Compiled pipeline binaries are keyed by their source fingerprint and stored
under $SPARKWING_HOME/cache/pipelines. Automatic pruning after compilation
keeps recently used entries within the configured byte and entry limits.
Use these commands to inspect entries or reclaim space.

--docs on info and prune points them at the docs --web fetch cache under
$XDG_CACHE_HOME/sparkwing/web/ (or ~/.cache/sparkwing/web/) instead.`,
	SubcommandOrder: []string{"info", "prune", "explain"},
	Examples: []Example{
		{"See what is cached", "sparkwing cache info"},
		{"Reclaim space now", "sparkwing cache prune"},
	},
}

var cmdCacheInfo = Command{
	Path:     "sparkwing cache info",
	Synopsis: "Print cache dir, size, ceilings, and recent entries",
	Description: `Lists the cache directory, its total size, the configured
ceilings, and the most recently used entries with their sizes and
last-use times. Entries are ordered by last use, which is what
pruning evicts on -- not by when they were built.

--docs reports the docs --web fetch cache instead: its directory, total size,
file counts by doc, migration and index, and the freshness of the cached
versions.json (24h TTL). The cache mirrors the URL path, so the cached files
can be read directly when debugging.`,
	Flags: []FlagSpec{
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty | json | plain", Default: "pretty on TTY, json when piped", Group: "Output"},
		{Name: "all", Argument: "", Desc: "List every entry instead of the ten most recent", Group: "Output"},
		{Name: "docs", Desc: "Report the docs --web fetch cache instead of pipeline binaries", ConflictsWith: []string{"all"}, Group: "Output"},
	},
	GroupOrder: []string{"Output", "Other"},
	Examples: []Example{
		{"Human-readable", "sparkwing cache info"},
		{"Agent-readable", "sparkwing cache info -o json"},
		{"Every entry", "sparkwing cache info --all"},
		{"The docs --web fetch cache", "sparkwing cache info --docs"},
	},
}

var cmdCachePrune = Command{
	Path:     "sparkwing cache prune",
	Synopsis: "Evict least recently used binaries down to the ceilings",
	Description: `Removes the least recently used cached binaries until the cache
fits both the byte ceiling and the entry ceiling. Defaults come
from cache.max_bytes and cache.max_entries in config.yaml; either
accepts 0 to disable that dimension.

An execution lease protects each running binary. Prune skips active
and busy entries, bounds the number examined, and reports observed
capacity separately from removed entries. Callers making admission
decisions remeasure filesystem capacity after pruning.

--docs deletes every file in the docs --web fetch cache instead, so the next
--web call fetches afresh; it refuses paths that do not resolve inside the
cache directory, so a stray symlink cannot escape. It takes no ceiling flag.`,
	Flags: []FlagSpec{
		{Name: "max-bytes", Argument: "SIZE", Desc: "Byte ceiling (512MiB and similar sizes)", Group: "Limits"},
		{Name: "max-entries", Argument: "N", Desc: "Entry ceiling", Group: "Limits"},
		{Name: "all", Argument: "", Desc: "Remove every entry, ignoring both ceilings", Group: "Limits"},
		{Name: "docs", Desc: "Clear the docs --web fetch cache instead of pipeline binaries", ConflictsWith: []string{"all", "max-bytes", "max-entries"}, Group: "Limits"},
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty | json | plain", Default: "pretty on TTY, json when piped", Group: "Output"},
	},
	GroupOrder: []string{"Limits", "Output", "Other"},
	Examples: []Example{
		{"Trim to the configured ceilings", "sparkwing cache prune"},
		{"Trim to a smaller budget", "sparkwing cache prune --max-bytes 512MiB"},
		{"Reclaim everything", "sparkwing cache prune --all"},
		{"Force-refresh the next docs --web call", "sparkwing cache prune --docs"},
	},
}

var cmdCacheExplain = Command{
	Path:     "sparkwing cache explain",
	Synopsis: "Show a pipeline's cache key and the inputs behind it",
	Description: `Prints the cache key for a pipeline module, whether that key is
already cached, and every input that produced it -- the Go toolchain,
the platform, the module tree, each local replace target, a covering
go.work, and the resolved module pins -- each with its own digest and
how much it covered.

File counts show how many files were excluded because Git ignores them.
Edits to excluded files leave the cache key unchanged.

When other cached entries came from the same checkout, each is listed
with the inputs that differ from the current key. That is the direct
answer to why a rebuild happened.`,
	Flags: []FlagSpec{
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty | json | plain", Default: "pretty on TTY, json when piped", Group: "Output"},
	},
	GroupOrder: []string{"Target", "Output", "Other"},
	Examples: []Example{
		{"Why did this rebuild?", "sparkwing cache explain"},
		{"Agent-readable", "sparkwing cache explain -o json"},
	},
}

var cmdDebug = Command{
	Path:     "sparkwing debug",
	Synopsis: "Interactive debugging for pipeline runs",
	Description: `Pause nodes at selected execution points, inspect them, open a shell,
or resume execution. Pause settings apply to the launched run.`,
	SubcommandOrder: []string{"run", "release", "attach", "env", "rerun", "replay"},
	Examples: []Example{
		{"Pause before the tests node", "sparkwing debug run build --pause-before tests"},
		{"Resume a paused node", "sparkwing debug release --run run-fictional --node tests"},
	},
}

var cmdDebugRun = Command{
	Path:     "sparkwing debug run",
	Synopsis: "Run a pipeline with ephemeral pause directives",
	Description: `Runs the named pipeline exactly as 'sparkwing run <pipeline>' would, with
additional pause hooks the orchestrator honors before and after
each matching node. Directives travel as env vars to the
pipeline binary; they never land in tracked code.

--pause-before <node> holds the node BEFORE its Run is invoked.
--pause-after  <node> holds the node AFTER its Run returns
  (success or failure). Both flags are repeatable.
--pause-on-failure holds ANY node whose Run returns a non-nil
  error. Skipped / cancelled / OnFailure-recovered nodes do not
  pause -- only Run errors.

Paused nodes hold for 30 minutes by default; set debug.pause_timeout
in config.yaml (a duration such as 10m) to change it. An expired pause
is released with reason 'timeout-released' and surfaces in the
run record.

See 'sparkwing debug release' to resume, 'sparkwing debug env'
to inspect, and 'sparkwing debug attach' (cluster mode) to shell
into the pod holding the paused node.`,
	Flags: []FlagSpec{
		{Name: "pipeline", Argument: "NAME", Desc: "Pipeline name to run under debug supervision", Required: true, Group: "Target"},
		{Name: "pause-before", Argument: "NODE", Desc: "Hold NODE before Run (repeatable)", Group: "Debug"},
		{Name: "pause-after", Argument: "NODE", Desc: "Hold NODE after Run (repeatable)", Group: "Debug"},
		{Name: "pause-on-failure", Desc: "Hold any node whose Run errors", Group: "Debug"},
	},
	GroupOrder: []string{"Target", "Debug", "Source", "System", "Other"},
	Examples: []Example{
		{"Pause before tests", "sparkwing debug run --pipeline build --pause-before tests"},
		{"Pause on failure", "sparkwing debug run --pipeline build --pause-on-failure"},
	},
}

var cmdDebugRelease = Command{
	Path:     "sparkwing debug release",
	Synopsis: "Resume a paused node",
	Description: `Flips the pause row's released_at timestamp so the
orchestrator's poll loop wakes and continues dispatching past
the pause point. Local and cluster modes share this surface.`,
	Flags: []FlagSpec{
		{Name: "run", Argument: "ID", Desc: "Run ID holding the paused node", Required: true, Group: "Target"},
		{Name: "node", Argument: "NAME", Desc: "Node ID to release", Required: true, Group: "Target"},
		{Name: "profile", Argument: "NAME", Desc: "Profile name (cluster mode)", Group: "System"},
	},
	Examples: []Example{
		{"Release locally", "sparkwing debug release --run run-fictional --node tests"},
		{"Release in prod", "sparkwing debug release --run run-fictional --node tests --profile prod"},
	},
}

var cmdDebugAttach = Command{
	Path:     "sparkwing debug attach",
	Synopsis: "kubectl exec into a paused node's pod (cluster mode)",
	Description: `Looks up the pod holding the paused node's claim-lease from
the controller's node row, then shells out to kubectl exec -it
-- bash. Local mode prints a note that attach does not apply
(the process is already in your current shell's world) and
exits 0.`,
	Flags: []FlagSpec{
		{Name: "run", Argument: "ID", Desc: "Run ID holding the paused node", Required: true, Group: "Target"},
		{Name: "node", Argument: "NAME", Desc: "Node ID to attach to", Required: true, Group: "Target"},
		{Name: "namespace", Argument: "NAME", Desc: "Kubernetes namespace of the runner pods", Default: "sparkwing", Group: "Target"},
		{Name: "profile", Argument: "NAME", Desc: "Profile name (cluster mode)", Group: "System"},
	},
	Examples: []Example{
		{"Attach in prod", "sparkwing debug attach --run run-fictional --node tests --profile prod"},
	},
}

var cmdDebugRerun = Command{
	Path:     "sparkwing debug rerun",
	Synopsis: "Reproduce a node's dispatch frame in an interactive shell",
	Description: `Opens an interactive shell using a node's recorded environment and working
directory. Local execution writes upstream reference outputs beneath the
run's rerun directory. Cluster execution creates a temporary pod using
--image, else the profile's rerun_image in config.yaml, attaches to it,
and deletes it on exit.

Snapshots omit credential names and values and remove URL credentials.
Controller access to the captured environment requires an admin token.
The command lists omitted keys so you can supply required credentials.
Secrets resolve when accessed, and the selected runner image applies.

--seq selects an attempt index; its default selects the latest attempt.`,
	Flags: []FlagSpec{
		{Name: "run", Argument: "ID", Desc: "Run ID holding the node", Required: true, Group: "Target"},
		{Name: "node", Argument: "NAME", Desc: "Node ID to reproduce", Required: true, Group: "Target"},
		{Name: "seq", Argument: "N", Desc: "Attempt index; -1 selects most recent", Group: "Target"},
		{Name: "profile", Argument: "NAME", Desc: "Profile name (cluster mode)", Group: "System"},
		{Name: "image", Argument: "REF", Desc: "Runner image for cluster-mode debug pod (cluster mode)", Group: "System"},
	},
	Examples: []Example{
		{"Rerun locally", "sparkwing debug rerun --run run-fictional --node tests"},
		{"Rerun a specific attempt", "sparkwing debug rerun --run run-fictional --node tests --seq 1"},
		{"Rerun in prod", "sparkwing debug rerun --run run-fictional --node tests --profile prod --image ghcr.io/me/runner:v1"},
	},
}

var cmdDebugReplay = Command{
	Path:     "sparkwing debug replay",
	Synopsis: "Re-execute a single node headlessly using its dispatch snapshot",
	Description: `Mints a new run row linked to the original via replay_of_run_id /
replay_of_node_id, creates a single nodes row for the target, and
exec's the pipeline binary to execute that one node. The
node's input struct is reconstituted from the stored dispatch
snapshot; upstream Refs resolve against the original
run's outputs without re-executing them.

Replay is "what would this node do now, with the same arguments and
environment?":
secrets re-resolve fresh through sparkwing.Secret, BeforeRun hooks
re-fire, and any code drift in the registered job struct (renamed
type, removed field) returns an error.

With --profile PROF, the original run + target node + dep outputs +
dispatch snapshot are first fetched from the named controller via
HTTP and side-loaded into the local store. Replay execution itself
always runs locally because the user's sparkwing binary owns the
registered pipeline factories.`,
	Flags: []FlagSpec{
		{Name: "run", Argument: "ID", Desc: "Run ID holding the original node", Required: true, Group: "Target"},
		{Name: "node", Argument: "NAME", Desc: "Node ID to re-execute", Required: true, Group: "Target"},
		{Name: "profile", Argument: "PROF", Desc: "Sideload from this profile's controller before replaying locally", Group: "System"},
	},
	Examples: []Example{
		{"Replay a node locally", "sparkwing debug replay --run run-fictional --node deploy"},
		{"Replay a prod run on your laptop", "sparkwing debug replay --profile prod --run run-fictional --node deploy"},
	},
}

var cmdDebugEnv = Command{
	Path:     "sparkwing debug env",
	Synopsis: "Print a paused node's environment and working directory + claim holder",
	Description: `Prints the environment, working directory, process owner, and state
captured when a node paused. If the node is not paused, prints a warning
and exits zero.`,
	Flags: []FlagSpec{
		{Name: "run", Argument: "ID", Desc: "Run ID holding the node", Required: true, Group: "Target"},
		{Name: "node", Argument: "NAME", Desc: "Node ID to inspect", Required: true, Group: "Target"},
		{Name: "profile", Argument: "NAME", Desc: "Profile name (cluster mode)", Group: "System"},
	},
	Examples: []Example{
		{"Inspect locally", "sparkwing debug env --run run-fictional --node tests"},
	},
}

var runFlagSpecs = runFlagSpecsFromDocs()

func runFlagSpecsFromDocs() []FlagSpec {
	docs := sparkwing.SparkwingFlagDocs()
	out := make([]FlagSpec, 0, len(docs))
	for _, document := range docs {
		out = append(out, FlagSpec{
			Name:     document.Name,
			Short:    document.Short,
			Argument: document.Argument,
			Desc:     document.Desc,
			Group:    document.Group,
			Hot:      document.Hot,
		})
	}
	return out
}

var cmdPipeline = Command{
	Path:     "sparkwing pipeline",
	Synopsis: "This repo's pipelines",
	Description: `Per-project namespace. Every verb here operates on the
nearest .sparkwing/ walking up from the current directory.

Discovery (list / describe / plan) shows what pipelines this repo
defines. 'new' scaffolds a fresh pipeline (auto-bootstraps .sparkwing/
on first use); 'sparkwing run <name>' invokes one. 'hooks' wires
pipelines to git pre-commit / pre-push / post-commit.
'sparks' manages reusable spark libraries declared in the
sparks: block of .sparkwing/sparkwing.yaml.

The discovery verbs (list / describe / plan)
support -o json so an agent can parse output directly rather
than scraping tab-complete.

To bump the pipeline SDK pin in .sparkwing/go.mod, use
'sparkwing repos update --in-place'. To see the current pin, run
'sparkwing version' (composite card).`,
	SubcommandOrder: []string{"list", "describe", "new", "lint", "plan", "trigger", "hooks", "sparks"},
	Examples: []Example{
		{"Machine-readable catalog", "sparkwing pipeline list -o json"},
		{"One pipeline's details", "sparkwing pipeline describe --name fictional-release -o json"},
		{"Search by intent", `sparkwing pipeline list --query "tag a release"`},
		{"First pipeline in a new repository (auto-bootstraps)", "sparkwing pipeline new --name release"},
		{"Inspect the DAG before running", "sparkwing pipeline plan --static --name fictional-release"},
		{"Run a pipeline", "sparkwing run release"},
	},
}

var cmdPipelineTrigger = Command{
	Path:     "sparkwing pipeline trigger",
	Synopsis: "Submit a pipeline to a profile's controller (remote execution)",
	Description: `Submits a trigger to the controller defined by --profile and
follows the remote run until it reaches a terminal state.

When the profile defines a logs URL, the follow streams full log
output; otherwise it shows node-status updates from the
controller. --detach skips the follow and prints the run id once
the trigger is registered (the trigger POST itself always
completes before the command exits, so the run is guaranteed
queued).

A follow exits on the run's outcome, matching a local run:
0 when the run succeeded, 1 when it failed or was cancelled,
and 3 when the follow ended without a readable terminal status
(the run may still be in progress -- re-check it with
'sparkwing runs status --run <id> --profile <p>'). The status
block and failing-node errors print to stderr on either follow
mode, so redirecting stdout still shows why a run failed.
--detach exits 0 once the trigger is queued -- it reports
submission, not outcome.

Any flag not recognized here is forwarded to the pipeline as a
typed argument. For example, 'sparkwing pipeline trigger release --profile
prod --version v1.2.3' passes --version through to the trigger
payload -- same shape as 'sparkwing run'.

--working-tree freezes tracked changes and untracked non-ignored
files into an immutable Git snapshot, uploads it before admission,
and runs that exact snapshot without pushing to the origin. It
requires a complete SHA-1 repository; shallow and SHA-256 checkouts
fail before upload.

A snapshot carrying a secret-shaped file is refused before upload:
a dotenv, a key, keystore or certificate file, any text file
outside source holding a key block, or a settings or manifest file
whose bytes read as a credential. The refusal names
each path and whether it is tracked. Untrack it with
'git rm --cached', ignore it, or send it anyway by naming it with
--allow-secret-file PATH once per file; there is no blanket bypass.

Requires a profile with controller: set. For local execution
against a profile's storage, use 'sparkwing run --profile X'.`,
	PosArgs: []PosArg{
		{Name: "<pipeline>", Desc: "Pipeline name registered on the controller", Required: true},
	},
	Flags: []FlagSpec{
		{Name: "profile", Argument: "NAME", Desc: "Profile (from ~/.config/sparkwing/config.yaml) whose controller runs the pipeline", Group: "System", Required: true},
		{Name: "detach", Desc: "Return once the trigger is registered (print the run id); don't follow", Group: "System"},
		{Name: "working-tree", Desc: "Run tracked changes and untracked non-ignored files from an immutable remote snapshot", Group: "Source"},
		{Name: "allow-secret-file", Argument: "PATH", Desc: "Send this secret-shaped working-tree file anyway; PATH is repository-relative (repeatable)", Group: "Source"},
	},
	GroupOrder:  []string{"Source", "System", "Other"},
	UsageSuffix: "[-- pipeline-flags...]",
	Examples: []Example{
		{"Submit and follow", "sparkwing pipeline trigger fictional-release --profile prod --version v1.2.3"},
		{"Fire-and-forget; print run id and exit", "sparkwing pipeline trigger fictional-release --profile prod --detach"},
		{"Run the current dirty tree remotely", "sparkwing pipeline trigger test --profile gaming --working-tree"},
		{"Send a secret-shaped file the snapshot refuses", "sparkwing pipeline trigger test --profile gaming --working-tree --allow-secret-file config/local.env"},
	},
}

var cmdPipelineList = Command{
	Path:     "sparkwing pipeline list",
	Synopsis: "Enumerate every pipeline with metadata",
	Description: `Walks up from the current directory to locate .sparkwing/,
merges sparkwing.yaml entries with the describe cache's typed
metadata, and prints a grouped aligned table.

-o json emits structured records instead; agents should prefer
-o json since tab-complete / table output is for human reading.

--all includes entries marked 'hidden: true'. By default they're
omitted.

--query searches by intent instead: every token must match some field
(name / short / help / group / tags / triggers), hidden entries included,
and matches in the name rank above matches in prose. -o json adds a score
to each record, highest first.`,
	Flags: []FlagSpec{
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty | json | plain", Default: "pretty on TTY, json when piped", Group: "Output"},
		{Name: "all", Desc: "Include entries marked hidden", Group: "Output"},
		{Name: "query", Argument: "TEXT", Desc: "Rank entries whose fields match every token", Group: "Filter"},
	},
	GroupOrder: []string{"Filter", "Output", "Other"},
	Examples: []Example{
		{"Human-readable table", "sparkwing pipeline list"},
		{"Agent-readable catalog", "sparkwing pipeline list -o json"},
		{"Include hidden entries", "sparkwing pipeline list --all"},
		{"Find release-related pipelines", "sparkwing pipeline list --query release"},
		{"Agent-readable ranked hits", "sparkwing pipeline list --query \"tag release\" -o json"},
	},
}

var cmdPipelineDescribe = Command{
	Path:     "sparkwing pipeline describe",
	Synopsis: "Print one pipeline's full metadata",
	Description: `Emits the full record for a single pipeline: kind, group,
description, typed args, examples, triggers, and (for scripts)
frontmatter-declared positional args and flags. Always resolves
hidden entries -- if you're asking for a name explicitly, the
hidden flag shouldn't surprise you.

--secrets compiles the pipeline and prints each declared secret, its
source binding, and its resolution status instead of the metadata.`,
	Flags: []FlagSpec{
		{Name: "name", Argument: "NAME", Desc: "Pipeline name to describe", Required: true, Group: "Target"},
		{Name: "secrets", Desc: "Print the pipeline's declared secrets with provenance instead of its metadata", Group: "Target"},
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty | json | plain", Default: "pretty on TTY, json when piped", Group: "Output"},
	},
	GroupOrder: []string{"Target", "Output", "Other"},
	Examples: []Example{
		{"Human-readable", "sparkwing pipeline describe --name release"},
		{"Agent-readable", "sparkwing pipeline describe --name fictional-release -o json"},
		{"Inspect the declared secrets", "sparkwing pipeline describe --name fictional-release --secrets -o json"},
	},
}

var cmdPipelineNew = Command{
	Path:     "sparkwing pipeline new",
	Synopsis: "Scaffold a new Go pipeline",
	Description: `Creates a pipeline source file and registers its name. Creates the pipeline
module when the repository has none. An existing pipeline name is refused
before files are written.

--template selects the dependency graph. --on selects triggers independently:
  pull_request   opened, synchronize, and reopened events
  push           any branch
  schedule       daily at 09:00 UTC
  manual         explicit invocation only

Repeat --on or separate events with commas. 'manual' must stand alone.
Edit the generated trigger configuration to add filters.

Templates:
  minimal            one node with a placeholder action; no trigger
  build-test-deploy  sequential build, test, and deploy; no trigger
  ci-pr-check        parallel lint and test, followed by a gate; pull_request
  release            version, changelog, and publish sequence; no trigger
  scheduled-report   collect, parallel gatherers, then publish; schedule

Generated actions print placeholder output. Replace them with the work the
pipeline should perform. Use 'sparkwing docs read --guide authoring' for
pipeline authoring guidance and 'sparkwing examples' for complete examples.

'sparkwing -C DIR pipeline new' selects another repository. --hidden hides the entry from default
listings. --short sets its description.`,
	Flags: []FlagSpec{
		{Name: "name", Argument: "NAME", Desc: "New pipeline's kebab-case name (a-z, 0-9, -)", Required: true, Group: "Target"},
		{Name: "template", Argument: "SHAPE", Desc: "DAG to scaffold: minimal (1 node) | build-test-deploy (3) | ci-pr-check (3) | release (3) | scheduled-report (5)", Default: "minimal", Group: "Scaffold"},
		{Name: "on", Argument: "EVENT", Desc: "Trigger(s) to declare: pull_request | push | schedule | pre_commit | pre_push | post_commit | manual (repeatable or comma-separated)", Default: "the shape's own", Group: "Scaffold"},
		{Name: "hidden", Desc: "Mark the entry hidden in default tab-complete menus", Group: "Scaffold"},
		{Name: "short", Argument: "TEXT", Desc: "Pre-fill the ShortHelp / desc line", Group: "Scaffold"},
	},
	GroupOrder: []string{"Target", "Scaffold", "Other"},
	Examples: []Example{
		{"Single-node pipeline (default shape)", "sparkwing pipeline new --name release"},
		{"Build/test/deploy DAG (three-node)", "sparkwing pipeline new --name fictional-release --template build-test-deploy"},
		{"Pull-request gate (lint + test -> gate)", "sparkwing pipeline new --name pr-check --template ci-pr-check"},
		{"Scheduled fan-out report", "sparkwing pipeline new --name daily-report --template scheduled-report"},
		{"One job, fired by pull requests", "sparkwing pipeline new --name pr-test --template minimal --on pull_request"},
		{"Fired by both push and pull requests", "sparkwing pipeline new --name ci --template ci-pr-check --on push,pull_request"},
		{"A gate you invoke by hand, not on every PR", "sparkwing pipeline new --name gate --template ci-pr-check --on manual"},
	},
}

var cmdExamples = Command{
	Path:     "sparkwing examples",
	Synopsis: "Read complete example pipelines",
	Description: `Read complete pipelines from the sparks-core example registry. Examples
cover container deployment, migrations, release publishing, test sharding,
and similar tasks.

--category and --cloud filter the list. Cloud-independent examples match
every cloud filter. --name reads one example's description, prerequisites,
parameters, applicability, and README. --body includes source with default
parameter values.

JSON output contains manifests for a list, or a manifest and README for one
example. Use 'sparkwing pipeline new --template <shape>' to start a pipeline.`,
	Flags: []FlagSpec{
		{Name: "name", Argument: "EXAMPLE", Desc: "Show full detail for one example instead of the list", Group: "Target"},
		{Name: "body", Desc: "With --name, print the pipeline source (default + <placeholder> params)", Group: "Target"},
		{Name: "category", Argument: "CATEGORY", Desc: "Filter the list by applicability category", Group: "Filter"},
		{Name: "cloud", Argument: "CLOUD", Desc: "Filter the list by cloud (aws | gcp); cloud-agnostic examples always match", Group: "Filter"},
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty | json | plain", Default: "pretty on TTY, json when piped", Group: "Output"},
	},
	Examples: []Example{
		{"Browse them", "sparkwing examples"},
		{"Read one", "sparkwing examples --name container-deploy-ecs-fargate --body"},
		{"Search deployment guidance", "sparkwing docs search -q \"ecs fargate\""},
	},
}

var cmdPipelineLint = Command{
	Path:     "sparkwing pipeline lint",
	Synopsis: "Check pipeline source for idiomatic anti-patterns (enforced gate)",
	Description: `Statically analyzes pipeline source for the anti-patterns
that make a Plan() non-deterministic, impure, or misconfigured,
and exits non-zero on any violation. It inspects the Go syntax tree without
compilation or execution, including source pinned to another SDK version.

Only the Plan() body is inspected; code inside job/step closures
and SkipIf / BeforeRun bodies runs at dispatch, so I/O and
environment reads there are idiomatic and never flagged.

The rule set (see --rules for each rule's charter):
  plan-io              I/O (shell, exec, file, http) in Plan()
  plan-runtime-branch  os.Getenv / runtime.GOOS / IsLocal branching in Plan()
  runner-label         blank Requires/Prefers/WhenRunner labels; Inline +
                       Requires on one job
  unused-ref           a RefTo result discarded into _ or a bare statement
  group-cache-shared   Memoize on a fan-out or group, whose members then
                       share one cache entry
  dynamic-group-inert  a JobGroup setter on a JobFanOutDynamic result, which
                       has no members to apply it to
  guard-misuse         pipeline guards that can never be satisfied together

With no target it sweeps every pipeline in .sparkwing/sparkwing.yaml
and exits non-zero if any violates a rule -- designed as a CI gate
alongside 'explain --all'. --all says the same thing explicitly.
--name lints a single pipeline. Source defaults to <.sparkwing>/jobs;
override with --dir.`,
	Flags: []FlagSpec{
		{Name: "name", Argument: "NAME", Desc: "Pipeline to lint (default: every pipeline)", Group: "Target"},
		{Name: "all", Desc: "Lint every pipeline in this repo's sparkwing.yaml; the default, non-zero exit on any violation", Group: "Target"},
		{Name: "rules", Desc: "Print each rule's charter (what it forbids and why) and exit", Group: "Target"},
		{Name: "dir", Argument: "DIR", Desc: "Directory of pipeline source to scan (default: <.sparkwing>/jobs)", Group: "Target"},
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty | json | plain", Default: "pretty on TTY, json when piped", Group: "Output"},
	},
	GroupOrder: []string{"Target", "Output", "System", "Other"},
	Examples: []Example{
		{"Lint one pipeline", "sparkwing pipeline lint --name release"},
		{"Lint every pipeline (CI gate)", "sparkwing pipeline lint --all"},
		{"Agent-readable findings", "sparkwing pipeline lint --all -o json"},
		{"Show the rule set", "sparkwing pipeline lint --rules"},
	},
}

var cmdPipelinePlan = Command{
	Path:     "sparkwing pipeline plan",
	Synopsis: "Render the runtime-resolved DAG without dispatching any jobs",
	Description: `Compiles the pipeline and evaluates its plan under the supplied arguments.
Each step reports would_run or would_skip with its reason. Step actions are
not executed.

Skip reasons:
  user_skipif  the SkipIf predicate matches
  range_skip   the step falls outside --start-at and --stop-at

Dynamic fan-out counts remain unresolved when they require execution.
Skipping a state-loading step with --start-at leaves that state empty;
downstream predicates are evaluated with the resulting state.

--static prints the Plan DAG as the pipeline declares it -- nodes,
dependencies, approval gates -- without resolving runtime skips. Missing
required arguments are tolerated so the plan can be inspected before every
input is supplied. --static --all constructs every declared pipeline with
no extra arguments and exits non-zero if any plan fails validation
(mismatched typed references, inconsistent declared outputs, duplicate node
IDs, and similar errors), which makes it a CI gate.`,
	Flags: []FlagSpec{
		{Name: "name", Argument: "NAME", Desc: "Pipeline to plan", RequiredWhen: "unless --static --all", Group: "Target"},
		{Name: "static", Desc: "Print the declared Plan DAG without resolving runtime skips", Group: "Target"},
		{Name: "all", Desc: "With --static, validate every pipeline in sparkwing.yaml; non-zero exit on any failure", RequiresFlags: []string{"static"}, Group: "Target"},
		{Name: "start-at", Argument: "STEP", Desc: "Skip every WorkStep upstream of STEP in the resulting plan", Group: "Range"},
		{Name: "stop-at", Argument: "STEP", Desc: "Skip every WorkStep downstream of STEP in the resulting plan", Group: "Range"},
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty | json | plain", Default: "pretty on TTY, json when piped", Group: "Output"},
	},
	GroupOrder:  []string{"Target", "Range", "Output", "Other"},
	UsageSuffix: "[-- pipeline-flags...]",
	Examples: []Example{
		{"Resolve the example cluster DAG with supplied arguments", "sparkwing pipeline plan --name fictional-cluster"},
		{"Preview a resume-from-step", "sparkwing pipeline plan --name fictional-cluster --start-at fictional-install"},
		{"Agent-readable JSON for diff against expectations", "sparkwing pipeline plan --name fictional-release -o json"},
		{"The declared DAG, with args forwarded to the pipeline", "sparkwing pipeline plan --static --name example-release --env prod"},
		{"Validate every pipeline (CI gate)", "sparkwing pipeline plan --static --all"},
	},
}

var cmdRun = Command{
	Path:     "sparkwing run",
	Synopsis: "Invoke a pipeline",
	Description: `Compiles the nearest .sparkwing/ binary and exec's it
with the named pipeline.

Pipeline-module builds honor the highest go directive in go.mod and an active
resolved overlay. A fixed GOTOOLCHAIN below that floor selects the required Go
for the build only. GOTOOLCHAIN=local refuses with installation or unpinning
guidance. Pipeline steps retain the original environment.

Runner options use the --sw- prefix. Unknown --sw- options fail before
execution setup. Other arguments pass to the pipeline. Put -- before
pipeline arguments that resemble runner options; every argument after the
separator passes through unchanged.

For remote execution on a profile's controller, use
'sparkwing pipeline trigger <name> --profile PROF'.

Output: pretty on a terminal, compact NDJSON otherwise. JSON runs
emit a start, at most 20 node completions and five diagnostics, and
a terminal record with status, outcome counts and log commands.
Child output remains in the stored logs. Display strings are capped
at 256 bytes; truncation and omitted event/failure counts are explicit.
Use --sw-verbose for the complete live event stream, or
'sparkwing runs logs --run RUN_ID --follow' to read stored output.

SPARKWING_LOG_FORMAT=pretty|json|quiet overrides terminal detection.
quiet is the human summary used by managed git hooks. Explicit json
uses the same compact records on a terminal; --sw-verbose expands it.

--sw-detached queues the run instead of executing it here. It
returns as soon as the run is durable, and a resident consumer
process on this machine owns it from then on: close the
terminal, drop the ssh session, log out -- the run keeps going.
The acknowledgment is a run id and the directory its logs land
in. Address the run by that id afterwards:

  sparkwing runs status --run RUN_ID
  sparkwing runs logs   --run RUN_ID --follow
  sparkwing runs cancel --run RUN_ID

The following flags are read only by a detached launch and are refused
without --sw-detached: --sw-idempotency-key,
--sw-request-id, --sw-consumer-idle, --sw-consumer-claim-lease,
and --sw-output,
which picks the acknowledgment's format (pretty on a TTY, json
when piped; plain prints the bare id for scripting).

--sw-idempotency-key deduplicates on key plus pipeline: a repeat
carrying a key an earlier launch used returns the original run
id and its current status and creates nothing, which is what
prevents duplicate launches after a dropped connection. Reusing a key
with different arguments is refused, because a key names one
intent and different arguments are a different request.
--sw-request-id is tracing only and never affects deduplication.

--sw-ref and --sw-priority both work detached. The ref resolves
to a commit when you launch, so the run executes that commit
even if the ref moves first; the consumer executes the worktree
and removes it when the run ends. 'front' and 'back' stay
unresolved until the consumer launches the run, so 'front' means
ahead of the queue the run actually joins.

A pipeline can declare source: origin/main in sparkwing.yaml. Every launch
compiles that source while jobs execute in the submitting checkout.
An explicit --sw-pipeline-ref must resolve to the declared source commit.
Source-backed schedules use --follow rather than a pinned binary.

--sw-pipeline-ref works in foreground and detached runs. It compiles
the pipeline from another commit while
the run executes in the checkout it was launched from.
The execution checkout must retain .sparkwing/sparkwing.yaml;
its pipeline source may be missing or unbuildable.
The ref resolves when you
launch; its tree is checked out only to compile and is removed when
the run ends. It cannot be combined with --sw-ref.

A flag a detached run cannot carry (--sw-index, --sw-dry-run,
--profile, --sw-fleet, and the other run-shaping --sw- flags)
is refused with the reason instead of ignored; run those in
the foreground.

PIPELINE resolves against the checkout you are standing in (or the
one 'sparkwing -C DIR run' names) first, then the repo registry, and the chosen
checkout is recorded on the run. A detached run executes with an
allow-listed snapshot of the launching environment -- SPARKWING_*,
GITHUB_*, PATH, HOME, HOSTNAME, and KUBERNETES_SERVICE_HOST, minus
every credential-shaped name -- widened by the names and NAME_*
prefixes listed in run.submit_env_allow in config.yaml. A consumer starts automatically if none
is running and exits after five idle minutes; see
'sparkwing runs consumer'.`,
	PosArgs: []PosArg{
		{Name: "<pipeline>", Desc: "Pipeline name registered in .sparkwing/sparkwing.yaml", Required: true},
	},
	Flags:       runFlagSpecs,
	GroupOrder:  []string{"Source", "Range", "Safety", "System", "Other"},
	UsageSuffix: "[-- pipeline-flags...]",
	Examples: []Example{
		{"Run with no flags", "sparkwing run fictional-build"},
		{"Pass a typed pipeline arg", "sparkwing run fictional-release --version v0.28.1"},
		{"Run from a different git ref", "sparkwing run fictional-build --sw-ref feature/xyz"},
		{"Queue a run that outlives the terminal", "sparkwing run nightly-report --sw-detached"},
		{"Capture the id for scripting", "RUN=$(sparkwing run build --sw-detached --sw-output plain)"},
		{"Compile the pipeline from another ref", "sparkwing run fictional-build --sw-detached --sw-pipeline-ref main"},
		{"Deduplicate a detached retry", "sparkwing run deploy --sw-detached --sw-idempotency-key fictional-deploy-attempt --env staging"},
		{"Detach a pipeline from another checkout", "sparkwing -C ~/code/other-project run lint --sw-detached"},
		{"Retry a failed run", "sparkwing runs retry --run run-fictional --failed"},
		{"Submit to a remote controller", "sparkwing pipeline trigger deploy --profile prod"},
	},
}

var cmdDashboard = Command{
	Path:     "sparkwing serve",
	Synopsis: "Manage the local dashboard + API server",
	Description: `Background lifecycle for the laptop-local dashboard.
'start' spawns a detached server (writes PID + log under
$SPARKWING_HOME), 'stop' stops it, 'restart' replaces it, and 'status' reports readiness.

The server is one Go process that hosts the embedded Next.js SPA,
the JSON API, the log endpoints, and the SQLite store on the same
port.`,
	SubcommandOrder: []string{"start", "stop", "restart", "status", "logs"},
	Examples: []Example{
		{"Start the dashboard", "sparkwing serve start"},
		{"Check liveness", "sparkwing serve status"},
		{"Stop the dashboard", "sparkwing serve stop"},
	},
}

var cmdDashboardStart = Command{
	Path:     "sparkwing serve start",
	Synopsis: "Start the dashboard, preserving every running instance",
	Description: `Detaches a child process that runs the in-process
dashboard + API + logs server (pkg/localws). PID is written to
$SPARKWING_HOME/dashboard.pid; stdout/stderr are appended to
$SPARKWING_HOME/dashboard.log. Returns once the listener is
confirming an HTTP readiness response from that exact instance.

A running instance is left unchanged, including its effective options.
Use serve restart for replacement. Build identity is reported separately
from readiness; missing artifact evidence is unknown.

Every API request except GET /api/v1/version and signed POST /webhooks/
needs a bearer: the token in serve-token under the Sparkwing home, which
only your account can read, or a browser session derived from it. The CLI
sends the token. A browser opens the dashboard link start and status
print once; its #code= fragment is a single-use sign-in code the page
trades for a session kept in that origin's localStorage. No credential
rides a cookie or a URL a server sees.

The listener accepts loopback Host headers and the host of each
--allow-origin entry, and rejects a browser Origin that is neither a
loopback origin on the served port, the --addr host, nor listed in
--allow-origin; a pnpm dev server on port 3100 needs --allow-origin
http://localhost:3100. A browser write with a body must send
application/json. --allow-remote widens the Host check only.`,
	Flags: []FlagSpec{
		{Name: "output", Short: "o", Argument: "pretty|json|plain", Desc: "Pretty on a terminal, NDJSON otherwise. Plain prints running or stopped.", Group: "Output"},
		{Name: "addr", Argument: "HOST:PORT", Desc: "Bind address", Default: "127.0.0.1:4343", Group: "Bind"},
		{Name: "allow-remote", Desc: "Serve a non-loopback --addr. Every host that reaches it can try the serve token, and a holder of the token can run pipelines and list, overwrite and delete this machine's local secrets. It never serves a masked value.", Group: "Bind"},
		{Name: "allow-origin", Argument: "ORIGINS", Desc: "Comma-separated browser origins (`https://dash.example`) allowed alongside loopback ones, as Origin and as Host. Needed when a same-host proxy or --allow-remote serves the dashboard under a name that is not the --addr host.", Group: "Bind"},
		{Name: "profile", Argument: "PROFILE", Desc: "Profile from ~/.config/sparkwing/config.yaml (uses its logs + cache surfaces)", Group: "Storage"},
		{Name: "log-store", Argument: "URL", Desc: "Pluggable log backend URL (fs:///abs/path, s3://bucket/prefix). Overrides --profile.", Group: "Storage"},
		{Name: "artifact-store", Argument: "URL", Desc: "Pluggable artifact backend URL (fs:///abs/path, s3://bucket/prefix). Overrides --profile.", Group: "Storage"},
		{Name: "read-only", Desc: "Reject writes on /api/v1/* (auth + webhooks remain open)", Group: "Storage"},
		{Name: "no-local-store", Desc: "Skip local SQLite; list runs from --artifact-store. Requires --log-store + --artifact-store.", Group: "Storage"},
	},
	GroupOrder: []string{"Bind", "Storage", "System", "Other"},
	Examples: []Example{
		{"Start with defaults", "sparkwing serve start"},
		{"Use an alternate port", "sparkwing serve start --addr 127.0.0.1:5000"},
		{"Isolate state under a scratch dir", "SPARKWING_HOME=" + helpExampleScratchDir("sparkwing-x") + " sparkwing serve start"},
		{"Tail CI runs from S3 (no SQLite)", "sparkwing serve start --profile ci-smoke --no-local-store --read-only"},
		{"Serve a LAN bind under a browser-facing name", "sparkwing serve start --addr 192.168.1.20:4343 --allow-remote --allow-origin http://dashboard.example.com:4343"},
	},
}

var cmdDashboardStop = Command{
	Path:     "sparkwing serve stop",
	Synopsis: "Stop a running dashboard server",
	Description: `Verifies the persisted process birth and boot identity before stopping.
Sends TERM, waits five seconds, then forces that owned process to exit and
waits up to two more seconds. Linux uses a process handle; macOS repeats
identity checks immediately before signaling. Unknown ownership is refused.
An absent service succeeds.`,
	Flags: []FlagSpec{
		{Name: "output", Short: "o", Argument: "pretty|json|plain", Desc: "Pretty on a terminal, NDJSON otherwise. Plain prints running or stopped.", Group: "Output"},
	},
	Examples: []Example{
		{"Stop the dashboard", "sparkwing serve stop"},
	},
}

var cmdDashboardRestart = Command{
	Path:        "sparkwing serve restart",
	Synopsis:    "Replace an owned dashboard and wait for readiness",
	Description: "Stops the verified owned instance, then starts the invoked binary. Preserves effective options unless explicitly overridden. Address and storage URL syntax are checked before stopping; a valid replacement can still fail during startup. Unknown ownership is refused.",
	Flags:       cmdDashboardStart.Flags,
	Examples:    []Example{{"Restart with existing options", "sparkwing serve restart"}},
}

var cmdDashboardLogs = Command{
	Path:        "sparkwing serve logs",
	Synopsis:    "Read a bounded dashboard log tail",
	Description: "Reads the last 40 lines by default, scanning at most the final 1 MiB. --limit 0 skips history. --follow waits for appended lines until interrupted; log rotation requires restarting the command. Lines larger than 16 KiB are marked truncated. A requested history exceeding the byte window reports an error. Follow retains incomplete lines until a newline arrives.",
	Flags: []FlagSpec{
		{Name: "output", Short: "o", Argument: "pretty|json|plain", Desc: "Output format"},
		{Name: "limit", Argument: "N", Default: "40", Desc: "Last N lines; 0 skips history"},
		{Name: "follow", Desc: "Follow appended lines until interrupted"},
	},
	Examples: []Example{{"Read recent log lines", "sparkwing serve logs"}},
}

var cmdDashboardStatus = Command{
	Path:     "sparkwing serve status",
	Synopsis: "Report whether the dashboard is running",
	Description: `Reports persisted effective options, owned process identity, dashboard/API
URLs, readiness and artifact comparison. Stopped exits 1; unknown ownership
or failed readiness exits 2. Matching hashes establish build equality;
missing artifact evidence is unknown.`,
	Flags: []FlagSpec{
		{Name: "output", Short: "o", Argument: "pretty|json|plain", Desc: "Pretty on a terminal, NDJSON otherwise. Plain prints running or stopped.", Group: "Output"},
	},
	Examples: []Example{
		{"Check liveness", "sparkwing serve status"},
	},
}

var cmdWorker = Command{
	Path:     "sparkwing cluster worker",
	Synopsis: "Claim triggers from a profile's controller and run them in-process",
	Description: `Polls the trigger queue at the selected profile's
controller and executes each claimed trigger in-process on this host.
For k8s or warm execution, run sparkwing-runner runner --also-claim-triggers
--trigger-runner k8s|warm, which carries the image and service-account flags.

Run against a remote controller via --profile prod (or whichever profile),
or against a local 'sparkwing serve start' via --profile local.`,
	Flags: []FlagSpec{
		{Name: "profile", Argument: "PROFILE", Desc: "Profile name from config.yaml", Required: true, Group: "Connection"},
		{Name: "poll", Argument: "DUR", Desc: "Claim poll interval when the queue is empty", Default: "1s", Group: "Tuning"},
		{Name: "heartbeat", Argument: "DUR", Desc: "Claim-lease heartbeat cadence", Default: "5s", Group: "Tuning"},
	},
	GroupOrder: []string{"Connection", "Tuning", "Other"},
	Examples: []Example{
		{"Run against a named profile", "sparkwing cluster worker --profile local"},
		{"Faster polling for tight dev loops", "sparkwing cluster worker --profile local --poll 250ms"},
	},
}

var cmdDoctor = Command{
	Path:     "sparkwing doctor",
	Synopsis: "Inspect and repair abandoned local state",
	Description: `Inspects local state and repairs entries whose owners have stopped.
--dry-run reports proposed repairs. The command preserves live processes,
active daemon state, and cluster-scoped records.

Repairs cover home permissions, abandoned local run records, ended local
concurrency records, and orphaned run directories. Run-record repair requires
a reachable daemon so held runs remain protected.

Run-directory removal requires a local store with recorded runs and profiles
that all use that store. Directories written within the grace period remain.
Unaccounted directories are reported for inspection.

On POSIX systems, permission repair removes group, other, and special bits
while retaining existing owner access. The walk preserves symlinks without
following them. Windows access permissions are reported as unverified.

The report includes daemon reachability, repeated admission rejections,
version mismatches, quarantined ledgers, and capacity measurement problems.
It names the reset command for excessive learned demand floors.

In a project, the Go toolchain finding reports the running Go version,
GOTOOLCHAIN and its source, and the .sparkwing module's Go floor. It identifies
fixed pins sparkwing will raise for builds and a blocking GOTOOLCHAIN=local,
with the installation or unpinning command needed to proceed.

Standalone stores are listed with run counts and the oldest run's age.
Inspect their records before deleting a store directory.

Settings files that config.yaml replaced are copied into it and left in place
for older binaries. Doctor lists the ones already copied, which are safe to
delete once nothing older reads them, apart from any it could not copy and
any replaced path variable still set.

--timeout bounds the daemon and local-state checks, each taking a slice of it,
so a daemon that accepts connections and answers nothing is reported as wedged
rather than spending the whole budget. Recovering a wedged daemon means
stopping the process holding its socket; a restart needs a handshake it will
not answer. When the budget runs out mid-sweep, doctor prints what it reached
alongside the error.`,
	Flags: []FlagSpec{
		{Name: "dry-run", Desc: "Report what would be repaired without changing anything", Group: "Input"},
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty | json | plain", Group: "Output"},
		{Name: "timeout", Argument: "DURATION", Desc: "Budget for the daemon and local-state checks; each takes a slice of it", Default: "10s", Group: "System"},
	},
	GroupOrder: []string{"Input", "Output", "System", "Other"},
	Examples: []Example{
		{"Diagnose and repair now", "sparkwing doctor"},
		{"Report without changing anything", "sparkwing doctor --dry-run"},
		{"Agent-readable report", "sparkwing doctor -o json"},
		{"Answer quickly on a machine that is already stuck", "sparkwing doctor --timeout 3s"},
	},
}

var cmdCompletion = Command{
	Path:             "sparkwing completion",
	Synopsis:         "Emit a shell completion script (bash|zsh|fish)",
	HideFromComplete: true,
	Description: `Prints a completion script for the selected shell. Source it
from your shell rc:

  # bash
  source <(sparkwing completion --shell bash --output plain)

  # zsh (add 'autoload -U compinit; compinit' once above)
  source <(sparkwing completion --shell zsh --output plain)

  # fish
  sparkwing completion --shell fish --output plain | source

zsh and fish get per-item descriptions; bash is name-only because
compgen lacks the facility.`,
	Flags: []FlagSpec{
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty | json | plain (pretty on a terminal, json when piped)", Group: "Output"},
		{Name: "shell", Argument: "NAME", Desc: "bash | zsh | fish", Required: true, Group: "Target"},
	},
	GroupOrder: []string{"Target", "Other"},
	Examples: []Example{
		{"Wire completion for the current zsh session", "source <(sparkwing completion --shell zsh --output plain)"},
		{"Install persistent completion for fish", "sparkwing completion --shell fish --output plain > ~/.config/fish/completions/sparkwing.fish"},
	},
}

var cmdProfiles = Command{
	Path:     "sparkwing configure profiles",
	Synopsis: "Manage connection profiles for remote controllers",
	Description: `Profiles are the profiles section of config.yaml:
$SPARKWING_CONFIG (if set), else $XDG_CONFIG_HOME/sparkwing/config.yaml,
else ~/.config/sparkwing/config.yaml. Permissions on save are 0600, and a
save rewrites only the profiles section.

SPARKWING_HOME does not move this file. It is the state, cache and
logs root; profiles are machine-wide connections that outlive any
one home. A write from a command running under a home of its own is
refused rather than sent to the machine's profiles: set
SPARKWING_CONFIG to a path inside that home to keep it there.

Every human-driven client command (tokens, users, runs
retry/cancel/prune/logs) reads connection info from the
selected profile via --profile NAME. No --controller/--token flags
exist on other commands; profiles are the only config surface.

Add a profile with 'sparkwing cloud connect', check it with
'sparkwing cloud status', and remove it with 'sparkwing cloud disconnect'.`,
	SubcommandOrder: []string{"list", "show", "set"},
}

var cmdProfilesList = Command{
	Path:     "sparkwing configure profiles list",
	Synopsis: "Print every registered profile",
	Description: `Prints a table of profile name, controller URL, logs URL, and
token. JSON is one profile per line; the token is redacted in
every mode.`,
	Flags: []FlagSpec{
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty | json | plain", Default: "pretty on TTY, json when piped", Group: "Output"},
	},
	GroupOrder: []string{"Output", "Other"},
	Examples: []Example{
		{"List profiles", "sparkwing configure profiles list"},
		{"Agent-readable record", "sparkwing configure profiles list -o json"},
	},
}

var cmdProfilesShow = Command{
	Path:     "sparkwing configure profiles show",
	Synopsis: "Print one profile's config, or the profile a command would select",
	Description: `With --name, prints all fields of that config.yaml entry. The token is
redacted unless --show-token is passed.

Without --name, reports the profile a sparkwing command would resolve to and
the chain that picked it: --profile, then SPARKWING_PROFILE, then the
project's defaults.profile -- the resolver 'sparkwing run' and 'sparkwing
pipeline trigger' use, so the answer matches what they would do. --profile
NAME shows what adding that flag to your next command would select. That
report never prints tokens.`,
	Flags: []FlagSpec{
		{Name: "name", Argument: "NAME", Desc: "Profile name in config.yaml", Group: "Input"},
		{Name: "profile", Argument: "NAME", Desc: "Without --name: the resolution --profile NAME would make", ConflictsWith: []string{"name"}, Group: "Input"},
		{Name: "show-token", Desc: "Print the raw token (redacted by default)", Group: "Output"},
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format for the resolution report: pretty|json", Group: "Output"},
	},
	GroupOrder: []string{"Input", "Output", "Other"},
	Examples: []Example{
		{"The profile a command would use, and why", "sparkwing configure profiles show"},
		{"What --profile prod would pick", "sparkwing configure profiles show --profile prod -o json"},
		{"Show a named profile", "sparkwing configure profiles show --name prod"},
		{"Show a named profile with the raw token", "sparkwing configure profiles show --name prod --show-token"},
	},
}

var cmdProfilesSet = Command{
	Path:     "sparkwing configure profiles set",
	Synopsis: "Update fields on an existing profile",
	Description: `Only flags you pass are overwritten. --token="" explicitly
clears the token (empty value, not an omitted flag), and
--token-stdin with empty input clears it too. --token-stdin
reads the token from stdin and prompts without echo when stdin
is a terminal; prefer it over --token, which is visible to
other processes in the process list and recorded in shell
history. Use --show-token on 'profiles show' afterward to
confirm.`,
	Flags: []FlagSpec{
		{Name: "name", Argument: "NAME", Desc: "Profile name to mutate", Required: true, Group: "Input"},
		{Name: "controller", Argument: "URL", Desc: "New controller URL", Group: "Connection"},
		{Name: "token", Argument: "TOKEN", Desc: "New bearer token, visible to other processes and shell history (empty string clears)", ConflictsWith: []string{"token-stdin"}, Group: "Connection"},
		{Name: "token-stdin", Desc: "Read the new bearer token from stdin, prompting without echo on a terminal", ConflictsWith: []string{"token"}, Group: "Connection"},
	},
	GroupOrder: []string{"Input", "Connection", "Other"},
	Examples: []Example{
		{"Rotate a profile's token", "sparkwing configure profiles set --name prod --token-stdin"},
		{"Change a profile's controller", "sparkwing configure profiles set --name prod --controller https://api.sparkwing.example"},
	},
}

var cmdTokens = Command{
	Path:     "sparkwing cluster tokens",
	Synopsis: "Manage controller API tokens",
	Description: `All subcommands resolve controller URL + admin bearer from the
profile named by --profile.
Token creation prints the raw value to stdout once --
save it before leaving this command.`,
	SubcommandOrder: []string{"create", "list", "revoke", "rotate"},
}

var cmdTokensCreate = Command{
	Path:     "sparkwing cluster tokens create",
	Synopsis: "Mint a new API token",
	Description: `Creates a token of the given --type scoped to --principal.
Comma-separated --scope lists which API surfaces the token may
call. The raw token is printed to stdout exactly once; after
this command exits it cannot be recovered.`,
	Flags: []FlagSpec{
		{Name: "type", Argument: "KIND", Desc: "Token type: user | runner | service", Required: true, Group: "Input"},
		{Name: "principal", Argument: "NAME", Desc: "Name identifying the token holder", Required: true, Group: "Input"},
		{Name: "scope", Argument: "CSV", Desc: "Comma-separated scopes; use sparkwing docs read --topic auth for the supported set", Group: "Input"},
		{Name: "ttl", Argument: "DURATION", Desc: "Token lifetime (30d, 720h, and similar durations). 0 = never expires", Group: "Input"},
		{Name: "profile", Argument: "NAME", Desc: "Profile name", Required: true, Group: "System"},
	},
	Examples: []Example{
		{"Mint a service token with write scopes", "sparkwing cluster tokens create --type service --principal deploy-bot --scope runs.read,runs.write --profile prod"},
		{"Mint a user token that expires in 30 days", "sparkwing cluster tokens create --type user --principal fictional-user --scope admin --ttl 720h --profile prod"},
	},
}

var cmdLimits = Command{
	Path:     "sparkwing cluster limits",
	Synopsis: "Read and set the compute guards",
	Description: `Compute guards bound what the controller starts before the credit
ledger bills it: the cloud runners one principal holds at once, the cloud
runners the whole controller holds, the wall-clock seconds a run may hold them
for, the nodes one run may carry, the runs created per hour, and the shortest
interval a cloud schedule may declare. Every guard is zero by default, which is
unlimited, so a controller that sets none behaves as it did before the guards
existed.`,
	SubcommandOrder: []string{"show", "set"},
	Examples: []Example{
		{"Read the guards and what they measure", "sparkwing cluster limits show --profile prod"},
		{"Cap the cloud runners one team holds", "sparkwing cluster limits set --name max_concurrent_runners --value 20 --profile prod"},
	},
}

var cmdLimitsShow = Command{
	Path:     "sparkwing cluster limits show",
	Synopsis: "Print every compute guard and visible runner usage",
	Description: `Prints each guard with its ceiling, or "unlimited" when nothing set
one. An operator also sees the cloud runners claimed now in total and per
principal, and whether runner_alarm has been reached. Team readers see their
own paid runner cap without another team's fleet activity.`,
	Flags: []FlagSpec{
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty | json | plain", Default: "pretty on TTY, json when piped", Group: "Output"},
		{Name: "profile", Argument: "NAME", Desc: "Profile name", Required: true, Group: "System"},
	},
	Examples: []Example{
		{"Read the guards", "sparkwing cluster limits show --profile prod"},
		{"Read the guards as JSON", "sparkwing cluster limits show --profile prod -o json"},
	},
}

var cmdLimitsSet = Command{
	Path:     "sparkwing cluster limits set",
	Synopsis: "Set one compute guard",
	Description: `Sets one guard to a ceiling, or to zero to remove it. The guards are
max_concurrent_runners, max_global_runners, runner_alarm, max_run_seconds,
max_nodes_per_run, max_runs_per_hour, max_global_nodes_per_run,
max_global_runs_per_hour and min_cron_interval_seconds.
max_concurrent_runners counts a team's cloud runners across all its tokens;
the other per-principal guards bind a principal holding a metered token in its
team; the two max_global settings bind every run. Work past a guard answers
429 with a Retry-After and the run records a compute_limit_blocked event.
Requires the admin scope.`,
	Flags: []FlagSpec{
		{Name: "name", Argument: "GUARD", Desc: "Guard to set", Required: true, Group: "Input"},
		{Name: "value", Argument: "N", Desc: "Ceiling; 0 removes it", Required: true, Group: "Input"},
		{Name: "profile", Argument: "NAME", Desc: "Profile name", Required: true, Group: "System"},
	},
	Examples: []Example{
		{"Hold the fleet under fifty cloud runners", "sparkwing cluster limits set --name max_global_runners --value 50 --profile prod"},
		{"Warn at forty", "sparkwing cluster limits set --name runner_alarm --value 40 --profile prod"},
		{"Remove the per-run node cap", "sparkwing cluster limits set --name max_nodes_per_run --value 0 --profile prod"},
	},
}

var cmdTokensList = Command{
	Path:     "sparkwing cluster tokens list",
	Synopsis: "List token prefixes + metadata",
	Description: `Prints the non-secret prefix + metadata (type, principal,
scopes, last-used) for every token. The raw token value is
never printed by this command.

The SCOPES column shows the comma-separated scope set granted
to each token. Tokens carrying the controller's "admin"
superset render as "*" since admin short-circuits every other
scope check. An empty scope set renders as "-".

Use -o json to get a structured array with explicit
scope arrays, suitable for piping into jq.

--prefix PREFIX prints the full record of one token as indented JSON.`,
	Flags: []FlagSpec{
		{Name: "prefix", Argument: "PREFIX", Desc: "Print the full record of the token with this non-secret prefix", Group: "Filter"},
		{Name: "type", Argument: "KIND", Desc: "Filter by token type", Group: "Filter"},
		{Name: "include-revoked", Desc: "Include revoked tokens in the output", Group: "Filter"},
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty | json | plain", Default: "pretty on TTY, json when piped", Group: "Output"},
		{Name: "profile", Argument: "NAME", Desc: "Profile name", Required: true, Group: "System"},
	},
	Examples: []Example{
		{"List all active tokens", "sparkwing cluster tokens list --profile prod"},
		{"One token's full record", "sparkwing cluster tokens list --prefix swu_abc123 --profile prod"},
		{"Audit every revoked service token", "sparkwing cluster tokens list --type service --include-revoked --profile prod"},
		{"Inspect the warm-runner pool token's scopes as JSON", "sparkwing cluster tokens list --profile prod -o json | jq 'select(.principal==\"agent:fictional-runner\") | .scopes'"},
	},
}

var cmdTokensRevoke = Command{
	Path:     "sparkwing cluster tokens revoke",
	Synopsis: "Mark a token revoked",
	Description: `Subsequent requests using the token receive HTTP 401. Revocation is immediate
and irreversible.`,
	Flags: []FlagSpec{
		{Name: "prefix", Argument: "PREFIX", Desc: "Non-secret token prefix (from 'tokens list')", Required: true, Group: "Input"},
		{Name: "profile", Argument: "NAME", Desc: "Profile name", Required: true, Group: "System"},
	},
	Examples: []Example{
		{"Revoke a leaked token", "sparkwing cluster tokens revoke --prefix a1b2c3d4 --profile prod"},
	},
}

var cmdTokensRotate = Command{
	Path:     "sparkwing cluster tokens rotate",
	Synopsis: "Mint a replacement token with a grace window",
	Description: `Creates a new token and schedules the old token for revocation
after --grace. During the grace window, both tokens work, which
lets callers cycle credentials without downtime. The controller
caps --grace at 7 days, and revoking the old prefix cuts a grace
window short.`,
	Flags: []FlagSpec{
		{Name: "prefix", Argument: "PREFIX", Desc: "Non-secret prefix of the token to rotate", Required: true, Group: "Input"},
		{Name: "grace", Argument: "DURATION", Desc: "Window during which the old token still authenticates (maximum 168h)", Default: "24h", Group: "Input"},
		{Name: "ttl", Argument: "DURATION", Desc: "TTL of the new token (0 = preserve the old token's remaining TTL)", Group: "Input"},
		{Name: "profile", Argument: "NAME", Desc: "Profile name", Required: true, Group: "System"},
	},
	Examples: []Example{
		{"Rotate a token with a 48h grace window", "sparkwing cluster tokens rotate --prefix a1b2c3d4 --grace 48h --profile prod"},
	},
}

var cmdUsers = Command{
	Path:     "sparkwing cluster users",
	Synopsis: "Manage dashboard login users",
	Description: `Seeds admin credentials in the controller's users table, used
by the dashboard's password sign-in. Connection info comes from the
profile named by --profile.`,
	SubcommandOrder: []string{"add", "list", "delete"},
}

var cmdUsersAdd = Command{
	Path:     "sparkwing cluster users add",
	Synopsis: "Create a dashboard user",
	Description: `Prompts for a password on stdin with echo disabled when stdin
is a TTY (the password is not shown on-screen or recorded in
shell history). Passing --password skips the prompt -- useful
for CI seed flows but leaks via shell history if used
interactively. --scope sets what the account's dashboard
sessions may reach; omitting it grants admin. The first account
on a controller must be an admin, so a --scope list that omits
admin is refused until one exists.`,
	Flags: []FlagSpec{
		{Name: "name", Argument: "NAME", Desc: "Dashboard username", Required: true, Group: "Input"},
		{Name: "password", Argument: "PASSWORD", Desc: "Password (omit to prompt interactively)", Group: "Input"},
		{Name: "scope", Argument: "LIST", Desc: "Comma-separated scopes (omit to grant admin; the first account must include admin)", Group: "Input"},
		{Name: "profile", Argument: "NAME", Desc: "Profile name", Required: true, Group: "System"},
	},
	Examples: []Example{
		{"Interactive add of the first admin", "sparkwing cluster users add --name fictional-user --profile prod"},
		{"Read-only account, once an admin exists", "sparkwing cluster users add --name viewer --scope runs.read,logs.read --profile prod"},
		{"Non-interactive add for CI", `sparkwing cluster users add --name ci-bot --password "$CI_BOT_PW" --profile prod`},
	},
}

var cmdUsersList = Command{
	Path:     "sparkwing cluster users list",
	Synopsis: "Print every user",
	Description: `Prints name, scopes, created_at, and last_login_at for every
user in the controller's users table.`,
	Flags: []FlagSpec{
		{Name: "profile", Argument: "NAME", Desc: "Profile name", Required: true, Group: "System"},
	},
	Examples: []Example{
		{"List users", "sparkwing cluster users list --profile prod"},
	},
}

var cmdUsersDelete = Command{
	Path:     "sparkwing cluster users delete",
	Synopsis: "Remove a dashboard user",
	Description: `Deletes the user row, every session that user holds, and
revokes every token minted under that principal name except the token
this request authenticates with, in one transaction. The sessions and
tokens are revoked and the auth cache on the serving replica is
cleared; auth.md describes the windows that remain elsewhere.`,
	Flags: []FlagSpec{
		{Name: "name", Argument: "NAME", Desc: "Dashboard username to remove", Required: true, Group: "Input"},
		{Name: "profile", Argument: "NAME", Desc: "Profile name", Required: true, Group: "System"},
	},
	Examples: []Example{
		{"Delete a user", "sparkwing cluster users delete --name fictional-user --profile prod"},
	},
}

var cmdJobs = Command{
	Path:     "sparkwing runs",
	Synopsis: "Inspect and control pipeline runs",
	Description: `Inspect recorded pipeline executions and control their lifecycle.
Commands support local runs and runs stored through a named profile.
Pass --profile NAME to select that profile's backend.`,
	SubcommandOrder: []string{"consumer", "list", "status", "logs", "stats", "annotations", "approvals", "retry", "cancel", "bounce", "prune"},
}

var cmdJobsList = Command{
	Path:     "sparkwing runs list",
	Synopsis: "List recent pipeline runs",
	Description: `Reads runs from the selected backend. Pass --profile NAME to select a
named profile. Filters compose with AND semantics across flag types
(pipeline=X AND status=Y), OR
semantics within a repeated flag (pipeline=X OR pipeline=Y).

A local listing merges this home's own store with every standalone
store under it -- the ones runs that could not reach the admission
daemon wrote -- newest first. Each row carries the store it came
from: 'shared', or the store's path under the home. An id in both
stores lists once, from the shared store. The STORE column appears
only when a standalone run is in the table; every run record in
-o json carries the field. A standalone store this build cannot read is named on
stderr after the table instead of listed.

With -q / --quiet the output contains run identifiers, one per line, for
shell piping:

  sparkwing runs list --pipeline X --limit 1 -q --profile prod \
      | xargs -I{} sparkwing runs logs --run {} --profile prod --follow

Results are paged. JSON ends with a kind:page record reporting returned,
limit, truncated and next_cursor, plus total where the count can be
exact; limit is the page size served, so a request above the ceiling
reports the ceiling rather than the number asked for. Continue with
--cursor and the same filters until truncated is false. Under -q, and in
the other formats, a cut listing says so on stderr instead. --limit 0 is
refused: this listing serves pages, so a page of zero has no meaning.

--by-pipeline aggregates every run the filters admit. Its JSON ends with
a kind:summary record carrying truncated and, where it stopped short,
reason, in place of a kind:page record.

--status failed --group-by run prints the page this listing selects as
failures, each run with its failing step and error; --group-by step or node
clusters that page's failures. Filters, --limit, --cursor and the page record
work as they do for the table. --wait blocks
until a run matches (a CI job waiting for the run its push started), and
--watch keeps printing each newer matching run. Both match on --pipeline,
--status, --branch, --sha, --repo, --root-only and --since.`,
	Flags: []FlagSpec{
		{Name: "pipeline", Argument: "NAME", Desc: "Filter by pipeline name (repeatable; prefix `!` to exclude)", Group: "Filter"},
		{Name: "status", Argument: "STATUS", Desc: "Filter by status: running|success|failed|cancelled (repeatable; prefix `!` to exclude)", Group: "Filter"},
		{Name: "branch", Argument: "BRANCH", Desc: "Filter by git branch (repeatable; prefix `!` to exclude)", Group: "Filter"},
		{Name: "sha", Argument: "PREFIX", Desc: "Filter by git sha prefix (repeatable; prefix `!` to exclude)", Group: "Filter"},
		{Name: "error", Argument: "SUBSTR", Desc: "Substring match against the persisted failure reason", Group: "Filter"},
		{Name: "search", Argument: "QUERY", Desc: "Free-text search across pipeline/branch/sha/id/error; prefix a term with `-` to exclude", Group: "Filter"},
		{Name: "since", Argument: "DURATION", Desc: "Only runs newer than this (1h, 24h, 7d, and similar durations)", Group: "Filter"},
		{Name: "started-after", Argument: "DATE", Desc: "Only runs whose StartedAt >= this (today, yesterday, 24h, 7d, or a date)", Group: "Filter"},
		{Name: "started-before", Argument: "DATE", Desc: "Only runs whose StartedAt <= this", Group: "Filter"},
		{Name: "finished-after", Argument: "DATE", Desc: "Only runs whose FinishedAt >= this (excludes still-running)", Group: "Filter"},
		{Name: "finished-before", Argument: "DATE", Desc: "Only runs whose FinishedAt <= this (excludes still-running)", Group: "Filter"},
		{Name: "limit", Argument: "N", Desc: "Runs per page; a request above the ceiling is served at the ceiling, which the page record reports", Default: "20", Group: "Output"},
		{Name: "cursor", Argument: "CURSOR", Desc: "Continue after next_cursor with the same filters", Group: "Output"},
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty|json|plain", Group: "Output"},
		{Name: "quiet", Short: "q", Desc: "Print only run ids, one per line (JSON strings with -o json)", Group: "Output"},
		{Name: "by-pipeline", Desc: "Pivot into one row per pipeline with a status sparkline of the last N runs", Group: "Output"},
		{Name: "sparkline", Argument: "N", Desc: "Sparkline length when --by-pipeline is set", Default: "30", Group: "Output"},
		{Name: "style", Argument: "STYLE", Desc: "Sparkline glyph style: ascii|block|dot", Default: "ascii", Group: "Output"},
		{Name: "repo", Argument: "OWNER/NAME", Desc: "Filter by the repository a run declared (repeatable)", Group: "Filter"},
		{Name: "root-only", Desc: "Exclude child runs", Group: "Filter"},
		{Name: "group-by", Argument: "KEY", Desc: "With --status failed: list each failure (run) or cluster them by step or node", Group: "Output"},
		{Name: "wait", Desc: "Block until at least one run matches, then list (exit 2 on timeout)", Group: "Behavior"},
		{Name: "wait-timeout", Argument: "DURATION", Desc: "How long --wait blocks", Default: "2m", Group: "Behavior"},
		{Name: "watch", Short: "w", Desc: "After listing, print each newer matching run as it appears", Group: "Behavior"},
		{Name: "profile", Argument: "NAME", Desc: "Profile name; omit for local-only", Group: "System"},
	},
	GroupOrder: []string{"Filter", "Output", "Behavior", "System", "Other"},
	Examples: []Example{
		{"Last 20 local runs", "sparkwing runs list"},
		{"Continue after a truncated page", "sparkwing runs list --since 30d --cursor 1700000000000000000:run-fictional:1697408000000000000"},
		{"Failed runs in the past day", "sparkwing runs list --status failed --since 24h"},
		{"Exclude success from the list", "sparkwing runs list --status '!success' --since 24h"},
		{"Runs on main, excluding canary", "sparkwing runs list --branch main --search '-canary'"},
		{"Runs that hit a specific failure", "sparkwing runs list --error 'permission denied'"},
		{"Runs finished today", "sparkwing runs list --finished-after today"},
		{"List prod runs", "sparkwing runs list --profile prod --limit 50"},
		{"By-pipeline rollup with sparkline", "sparkwing runs list --by-pipeline --since 7d"},
		{"By-pipeline JSON for an agent", "sparkwing runs list --by-pipeline -o json --since 24h"},
		{"Pipe the most recent run id into another verb", "sparkwing runs list --limit 1 -q | xargs -I{} sparkwing runs logs --run {}"},
		{"Cluster the past week's failures by step", "sparkwing runs list --status failed --since 7d --group-by step"},
		{"Wait for the run a push started", "sparkwing runs list --sha abc123 --repo acme/web --root-only --wait -q"},
		{"Print each new run as it starts", "sparkwing runs list --limit 1 --watch"},
	},
}

var cmdJobsStatus = Command{
	Path:     "sparkwing runs status",
	Synopsis: "Show one run's status (non-zero exit unless status=success)",
	Description: `Prints a summary of the run (pipeline, status, node states).
With --follow, polls until the run reaches a terminal status. Pass
--profile NAME to read from a remote controller.

A local read looks the id up in this home's own store first and then
in each standalone store, and reports which one held it. The verbs
that write to a run -- bounce, annotations add, approvals approve
and deny, debug rerun, debug replay -- write in whichever store held
it. Cancel and retry cannot act on a standalone run at all, because
no daemon arbitrates one, and say so instead of reporting it
missing.

Runs that wrote their logs to a filesystem also report log_path: the
directory holding the run's per-node .log files, on the machine that
executed the run. With -o json it is a top-level field, so an agent
holding a run id can read the logs off disk instead of scraping them
out of a stream. That machine may not be this one -- a cluster run
records its own pod-local path -- so the text output marks a directory
that is not present here; the JSON reports it as recorded. Runs whose
logs live on a controller or in an object store omit it.

Exit code contract: after rendering, 'runs status' exits 0 only when
status == success. Any non-success terminal status (failed, cancelled)
exits 1; a run that is still running when the (non-follow) read
returns also exits 1. Pass --exit-zero to inspect a known-failed run
while returning zero.

--follow --timeout D blocks until the run is terminal, polling every
--poll, then renders once. It exits 0 on success, 1 on failed or
cancelled, 2 when D elapses first, and 3 when the run cannot be read.

--view renders one view of the run instead of the status summary:
  summary   groups, work items, modifiers and annotations
  timeline  an ASCII waterfall of nodes (--steps adds steps, --width sets bars)
  receipt   the audit and cost receipt, always JSON; a local run carries
            zero cost because no rate is configured on this machine
  errors    each failed node's error chain
  tree      the run and every descendant run`,
	PosArgs: []PosArg{
		{Name: "[RUN_ID]", Desc: "Run identifier, when --run is not supplied"},
	},
	Flags: []FlagSpec{
		{Name: "run", Argument: "RUN_ID", Desc: "Run identifier. Positional fallback accepted.", Group: "Input"},
		{Name: "follow", Short: "f", Desc: "Poll until the run reaches a terminal state", Group: "Output"},
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty|json|plain", Group: "Output"},
		{Name: "steps", Desc: "Render every step under every node (plain output). Failed / skipped / annotated nodes always include their steps; this flag forces success nodes too.", Group: "Output"},
		{Name: "exit-zero", Desc: "Return exit code 0 even when the run failed/cancelled", Group: "Output"},
		{Name: "view", Argument: "VIEW", Desc: "Render one view: summary|timeline|receipt|errors|tree", Group: "Output"},
		{Name: "width", Argument: "N", Desc: "Timeline bar width (--view timeline)", Default: "60", Group: "Output"},
		{Name: "timeout", Argument: "DURATION", Desc: "With --follow, exit 2 when the run is not terminal after this long", Group: "Output"},
		{Name: "poll", Argument: "DURATION", Desc: "Poll interval while --follow waits without the live view", Default: "3s", Group: "Output"},
		{Name: "profile", Argument: "NAME", Desc: "Profile name; omit for local-only", Group: "System"},
	},
	GroupOrder: []string{"Input", "Output", "System", "Other"},
	Examples: []Example{
		{"Check a local run once", "sparkwing runs status run-fictional"},
		{"Block until a run finishes (exit 2 after 30m)", "sparkwing runs status run-fictional --follow --timeout 30m"},
		{"The run's full record as JSON", "sparkwing runs status run-fictional -o json --exit-zero"},
		{"Waterfall of a run's nodes and steps", "sparkwing runs status run-fictional --view timeline --steps"},
		{"Why a run failed", "sparkwing runs status run-fictional --view errors"},
		{"Follow a running job to completion", "sparkwing runs status --run run-fictional --follow"},
		{"Inspect a known-failed run without nonzero exit", "sparkwing runs status --run run-fictional --exit-zero"},
		{"Expand every step on every node", "sparkwing runs status --run run-fictional --steps"},
		{"Check a prod run", "sparkwing runs status --run run-fictional --profile prod"},
	},
}

var cmdJobsLogs = Command{
	Path:     "sparkwing runs logs",
	Synopsis: "Print a run's logs",
	Description: `Without --profile, reads logs from the local run directory. Pass --profile
NAME to read from a remote controller's logs service (profile must
carry both controller + logs URLs). Line-selection filters
(--tail/--head/--lines/--grep) apply server-side in cluster mode so
the CLI never tails giant logs over the wire.

--since D drops nodes whose StartedAt is older than now-D; useful for
runs that have been retried several times where only the newest
attempt matters. Filtering is node-level (log lines aren't
timestamped on disk). --events-only and --no-events are mutually
exclusive views of the unified stream.

--events-only emits the envelope records the dispatcher writes beside a
local run (run_start, node_start, run_finish, ...). A run read through a
backend emits that run's stored event records instead (admission_wait,
concurrency_wait, cache_hit, ...) -- a different record shape. That is
any profile whose state is a shared database, an object store or a
controller, and any profile that declares its own logs surface.

--grep without --run searches the logs of recent runs instead, scanning up
to --limit runs the run filters (--pipeline, --status, --branch, --sha,
--since, --started-after, --started-before) select and printing up to
--max-matches lines per node; -q prints only the matching run ids.

When a node's logs live in a logs service, a line framed by em dashes
follows its log when the log is not known to be whole: lines missing
after the runner sealed it, a stream that ended without the runner's
seal, or a runner that does not seal. The line is the reader's, never
part of the stored log, and JSON output omits it.`,
	Flags: []FlagSpec{
		{Name: "run", Argument: "RUN_ID", Desc: "Run identifier", RequiredWhen: "unless --grep searches across runs", Group: "Input"},
		{Name: "node", Argument: "NODE_ID", Desc: "Limit output to one node id", Group: "Filter"},
		{Name: "tail", Argument: "N", Desc: "Print only the last N lines", Group: "Filter"},
		{Name: "head", Argument: "N", Desc: "Print only the first N lines", Group: "Filter"},
		{Name: "lines", Argument: "A:B", Desc: "1-indexed inclusive line range", Group: "Filter"},
		{Name: "grep", Argument: "PATTERN", Desc: "Substring match (case-sensitive)", Group: "Filter"},
		{Name: "since", Argument: "DURATION", Desc: "Only include nodes that started within the last D; with --grep and no --run, only runs newer than D (5m, 1h, 7d, and similar durations)", Group: "Filter"},
		{Name: "tree", Desc: "Merge root + descendant runs into one stream (local only)", Group: "Filter"},
		{Name: "events-only", Desc: "Include event records and omit node body output", ConflictsWith: []string{"no-events"}, Group: "Filter"},
		{Name: "no-events", Desc: "Include node body output and omit event records", ConflictsWith: []string{"events-only"}, Group: "Filter"},
		{Name: "follow", Short: "f", Desc: "Tail the log(s) until the run terminates", Group: "Output"},
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty|json|plain", Group: "Output"},
		{Name: "profile", Argument: "NAME", Desc: "Profile name (omit for local-only reads)", Group: "System"},
		{Name: "pipeline", Argument: "NAME", Desc: "Cross-run search: filter runs by pipeline (repeatable; prefix `!` to exclude)", Group: "Search"},
		{Name: "status", Argument: "STATUS", Desc: "Cross-run search: filter runs by status (repeatable; prefix `!` to exclude)", Group: "Search"},
		{Name: "branch", Argument: "BRANCH", Desc: "Cross-run search: filter runs by git branch (repeatable; prefix `!` to exclude)", Group: "Search"},
		{Name: "sha", Argument: "PREFIX", Desc: "Cross-run search: filter runs by git sha prefix (repeatable; prefix `!` to exclude)", Group: "Search"},
		{Name: "started-after", Argument: "DATE", Desc: "Cross-run search: only runs whose StartedAt >= this", Group: "Search"},
		{Name: "started-before", Argument: "DATE", Desc: "Cross-run search: only runs whose StartedAt <= this", Group: "Search"},
		{Name: "limit", Argument: "N", Desc: "Cross-run search: max candidate runs to scan", Default: "50", Group: "Search"},
		{Name: "max-matches", Argument: "N", Desc: "Cross-run search: per-node match cap (0 = no cap)", Default: "5", Group: "Search"},
		{Name: "quiet", Short: "q", Desc: "Cross-run search: print only the unique matching run ids", Group: "Search"},
	},
	GroupOrder: []string{"Input", "Filter", "Search", "Output", "System", "Other"},
	Examples: []Example{
		{"Read local logs", "sparkwing runs logs --run run-fictional"},
		{"Last 20 lines of a remote run", "sparkwing runs logs --run run-fictional --profile prod --tail 20"},
		{"Only the most recent attempt's output", "sparkwing runs logs --run run-fictional --profile prod --since 5m"},
		{"Search logs for an error substring", "sparkwing runs logs --run run-fictional --grep 'permission denied'"},
		{"Find which recent failed runs logged a message", "sparkwing runs logs --grep 'connection reset' --status failed --since 24h"},
		{"Merge a parent run with every descendant", "sparkwing runs logs --run run-fictional --tree"},
		{"Read only structured event records", "sparkwing runs logs --run run-fictional --events-only"},
		{"JSON stream for an agent", "sparkwing runs logs --run run-fictional -o json"},
		{"Plain text with node/step prefix", "sparkwing runs logs --run run-fictional -o plain"},
		{"Force the colored renderer when piping", "sparkwing runs logs --run run-fictional -o pretty | less -R"},
	},
}

var cmdJobsStats = Command{
	Path:     "sparkwing runs stats",
	Synopsis: "Report run counts, success rate, and duration percentiles",
	Description: `Reports per-pipeline counts and durations over the selected run window.
Running runs contribute to counts and are excluded from duration percentiles.

--capacity reports measured duration, CPU, memory, admission charge,
queue wait, sample count, and the source of each charge. Memory charges use
peak demand; CPU charges use sustained demand. Explicit resource pins remain
in effect when measurements are reset.

Capacity profiles are local and scoped by repository identity and pipeline.
Linked worktrees and clones with the same origin share measurements.
The table shows each key as repository/pipeline.

--reset clears samples and learned demand floors. --pipeline accepts the key
shown by --capacity; a bare pipeline name matches that name across
repositories.
--all --yes resets every profile. The result reports removed rows, cleared
pinned rows, samples, and demand floors.`,
	Flags: []FlagSpec{
		{Name: "pipeline", Argument: "NAME", Desc: "Restrict to one pipeline (required with --reset unless --all)", Group: "Filter"},
		{Name: "since", Argument: "DURATION", Desc: "Only runs newer than this (7d and similar durations)", Group: "Filter"},
		{Name: "capacity", Desc: "Show measured capacity profiles instead of run aggregates", Group: "Output"},
		{Name: "reset", Desc: "Delete a pipeline's learned capacity profile so it re-learns (keeps pins)", Group: "Recovery"},
		{Name: "all", Desc: "With --reset, reset every pipeline's learned profile", RequiresFlags: []string{"reset", "yes"}, Group: "Recovery"},
		{Name: "yes", Desc: "Confirm --reset --all", Group: "Recovery"},
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty|json|plain", Group: "Output"},
		{Name: "profile", Argument: "NAME", Desc: "Profile name; omit for local-only", Group: "System"},
	},
	GroupOrder: []string{"Filter", "Output", "Recovery", "System", "Other"},
	Examples: []Example{
		{"7-day local stats", "sparkwing runs stats --since 7d"},
		{"Prod stats as JSON", "sparkwing runs stats --profile prod -o json"},
		{"Measured capacity per pipeline", "sparkwing runs stats --capacity"},
		{"Reset a poisoned profile", "sparkwing runs stats --reset --pipeline myrepo/build"},
		{"Reset every learned profile", "sparkwing runs stats --reset --all --yes"},
	},
}

var cmdJobsRetry = Command{
	Path:     "sparkwing runs retry",
	Synopsis: "Trigger fresh runs copying pipeline + args from old ones",
	Description: `Issues a new trigger per source run with the same pipeline, args,
branch, and SHA. Each new run is tagged with retry_of=<old-id>.

Local retries are refused because the original execution environment is
unavailable. Captured submission environments are deleted when execution
starts. Submit a new run from the intended environment.
A queued local retry whose execution environment is unavailable also fails
before execution. Controller-backed retries use their configured execution
context; select one with --profile.

A retry is not weighed against the pipeline's risk labels the way a launch is:
it re-queues the source run's own declarations, so a retry of a run whose step
declares a Risk is queued with no allow behind it.

Pick a rerun scope explicitly:
  --failed   reuse cached/passed nodes from the source run;
             re-execute only the failed or unreached subset.
  --all      ignore prior outcomes and re-execute every node.

One of --failed or --all is required.

Pass --run once per source id (repeatable). Use --run - to read ids
from stdin, one per line. Failures on individual ids don't abort
the batch; the verb prints a per-id status line and exits non-zero
only when at least one id failed.`,
	Flags: []FlagSpec{
		{Name: "run", Argument: "RUN_ID", Desc: "Source run id (repeatable; use --run - to read ids from stdin)", Group: "Input"},
		{Name: "failed", Desc: "Rerun from failed: reuse passed nodes, re-execute only failed/unreached", ConflictsWith: []string{"all"}, Group: "Input"},
		{Name: "all", Desc: "Rerun all: re-execute every node from scratch", ConflictsWith: []string{"failed"}, Group: "Input"},
		{Name: "profile", Argument: "NAME", Desc: "Profile name for remote runs; omit for local runs", Group: "System"},
	},
	GroupOrder: []string{"Input", "System", "Other"},
	Examples: []Example{
		{"Rerun only the failed nodes", "sparkwing runs retry --failed --run run-fictional --profile prod"},
		{"Rerun every node from scratch", "sparkwing runs retry --all --run run-fictional --profile prod"},
		{"Rerun every recently failed run", "sparkwing runs list --status failed --since 1h -q | sparkwing runs retry --failed --run - --profile prod"},
	},
}

var cmdJobsConsumer = Command{
	Path:     "sparkwing runs consumer",
	Synopsis: "Inspect or control the process that executes submitted runs",
	Description: `One consumer per Sparkwing home claims queued triggers and executes them.
A file lock grants exclusive ownership; a dashboard uses the same lock.
A detached launch starts a consumer when needed.

Stopping the consumer leaves queued runs available for a later consumer.
An interrupted executing run returns to the queue. Cancel a run to prevent
further execution.

A detached launch from a different build replaces the consumer. Replacement
interrupts active work and returns it to the queue for the new consumer.`,
	SubcommandOrder: []string{"start", "status", "stop"},
}

var cmdJobsConsumerStart = Command{
	Path:     "sparkwing runs consumer start",
	Synopsis: "Start a consumer for this home if none is running",
	Description: `Starts the resident trigger consumer and waits until it owns the
home's queue. A no-op when one is already running.

Rarely needed by hand: 'sparkwing run --sw-detached' does this
before it acknowledges a run.`,
	Flags: []FlagSpec{
		{Name: "output", Short: "o", Argument: "pretty|json|plain", Desc: "Pretty on a terminal, NDJSON otherwise. Plain prints running or stopped.", Group: "Output"},
		{Name: "idle", Argument: "DUR", Desc: "Exit after this long with no work (default 5m)", Group: "System"},
		{Name: "claim-lease", Argument: "DUR", Desc: "Lease stamped on each claimed run, renewed while it executes (default 3m)", Group: "System"},
	},
	Examples: []Example{
		{"Start one for the default home", "sparkwing runs consumer start"},
		{"Keep one resident for an hour", "sparkwing runs consumer start --idle 1h"},
	},
}

var cmdJobsConsumerStatus = Command{
	Path:     "sparkwing runs consumer status",
	Synopsis: "Report whether a consumer is resident",
	Description: `Prints the resident consumer's pid, home, and log path. Exits 1
when no consumer is running, so it composes in shell conditions.`,
	Flags: []FlagSpec{
		{Name: "output", Short: "o", Argument: "pretty|json|plain", Desc: "Pretty on a terminal, NDJSON otherwise. Plain prints running or stopped.", Group: "Output"},
	},
	Examples: []Example{
		{"Check for a resident consumer", "sparkwing runs consumer status"},
	},
}

var cmdJobsConsumerStop = Command{
	Path:     "sparkwing runs consumer stop",
	Synopsis: "Stop the resident consumer",
	Description: `Signals the resident consumer to drain and exit. Queued runs are
not cancelled -- they stay queued and execute when a consumer
comes back, which the next 'sparkwing run --sw-detached' arranges.

To cancel a queued run instead, use 'sparkwing runs cancel'.`,
	Flags: []FlagSpec{
		{Name: "output", Short: "o", Argument: "pretty|json|plain", Desc: "Pretty on a terminal, NDJSON otherwise. Plain prints running or stopped.", Group: "Output"},
	},
	Examples: []Example{
		{"Stop the resident consumer", "sparkwing runs consumer stop"},
	},
}

var cmdJobsCancel = Command{
	Path:     "sparkwing runs cancel",
	Synopsis: "Request cancellation of in-flight runs",
	Description: `Sends a cancel request per run to the controller. Each run
transitions to 'cancelling' and then 'cancelled' once the runner
acknowledges. Already-finished runs surface a per-id error but
don't abort the batch.

Pass --run once per id (repeatable). Use --run - to read ids
from stdin, one per line. For local runs sharing an admission lease,
cancelling a child also cancels its descendants. Its parent and siblings
continue. Cancelling the root cancels every member of that lease.
Children launched after a parent exits attach under its nearest live ancestor
while a live descendant retains its lineage. Otherwise they attach under the
lease root, and the daemon logs the parent resolution.`,
	Flags: []FlagSpec{
		{Name: "run", Argument: "RUN_ID", Desc: "Run id to cancel (repeatable; use --run - to read ids from stdin)", Group: "Input"},
		{Name: "profile", Argument: "NAME", Desc: "Profile name for remote runs; omit for local runs", Group: "System"},
	},
	GroupOrder: []string{"Input", "System", "Other"},
	Examples: []Example{
		{"Cancel one run", "sparkwing runs cancel --run run-fictional --profile prod"},
		{"Cancel every running prod run", "sparkwing runs list --status running --profile prod -q | sparkwing runs cancel --run - --profile prod"},
	},
}

var cmdJobsBounce = Command{
	Path:     "sparkwing runs bounce",
	Synopsis: "Restart one running job's process without failing the run",
	Description: `Stops the process executing one running job and runs that
job again, in place. The run keeps going: the job never reaches a
terminal state, so nothing downstream sees a failure and no other
job is disturbed.

Use it for a job that is wedged or misbehaving when cancelling the
whole run would cost more than it saves.

The request is recorded and the verb returns; the runner supervising
the job picks it up within a few seconds, stops the process (SIGTERM,
then SIGKILL after the grace period), and re-runs the job from its
first step. Steps therefore run again, so a job with side effects
needs the same idempotency a restarted pod already demands.

A job that finishes before the stop lands is left alone. Bouncing
again is allowed -- one request is one restart.

The local runner is what acts on the request, whether the run's state
lives here or on a controller. A job the in-cluster Kubernetes runner
executes records the request and nothing consumes it, so the job keeps
running; cancel the run and retry it instead.`,
	Flags: []FlagSpec{
		{Name: "run", Argument: "RUN_ID", Desc: "Run id owning the job", Group: "Input"},
		{Name: "node", Argument: "NODE_ID", Desc: "Job id to bounce", Group: "Input"},
		{Name: "profile", Argument: "NAME", Desc: "Profile name for remote runs; omit for local runs", Group: "System"},
	},
	GroupOrder: []string{"Input", "System", "Other"},
	Examples: []Example{
		{"Bounce a wedged job", "sparkwing runs bounce --run run-fictional --node build"},
		{"Bounce a job in a run a controller holds", "sparkwing runs bounce --run run-fictional --node build --profile prod"},
	},
}

var cmdJobsPrune = Command{
	Path:     "sparkwing runs prune",
	Synopsis: "Delete finished runs older than a threshold, or by id",
	Description: `Prunes terminal runs (success / failed / cancelled) so the
controller's SQLite store doesn't grow unbounded. Supply either
--older-than DUR (batch by age) or one-or-more run ids via --run
(repeatable). Use --run - to read ids from stdin. The two modes
are mutually exclusive.

Use --dry-run first to confirm the matching runs.`,
	Flags: []FlagSpec{
		{Name: "older-than", Argument: "DURATION", Desc: "Prune runs older than this", RequiredWhen: "when no --run ids are supplied", ConflictsWith: []string{"run"}, Group: "Input"},
		{Name: "run", Argument: "RUN_ID", Desc: "Run id to prune (repeatable; use --run - to read ids from stdin)", RequiredWhen: "when --older-than is not set", ConflictsWith: []string{"older-than"}, Group: "Input"},
		{Name: "dry-run", Desc: "List matching runs without deleting", Group: "Output"},
		{Name: "profile", Argument: "NAME", Desc: "Profile name for remote runs; omit for local runs", Group: "System"},
	},
	Examples: []Example{
		{"Preview what a 7-day prune would delete", "sparkwing runs prune --older-than 7d --dry-run --profile prod"},
		{"Delete a few specific runs", "sparkwing runs prune --run run-A --run run-B --profile prod"},
		{"Prune ids from another query", "sparkwing runs list --pipeline scratch -q | sparkwing runs prune --run - --profile prod"},
	},
}

var cmdHooks = Command{
	Path:     "sparkwing pipeline hooks",
	Synopsis: "Install / uninstall git pre-commit + pre-push + post-commit hooks",
	Description: `Writes small git hook scripts into the repo's .git/hooks/
directory that call 'sparkwing run <pipeline>' for every pipeline that
declares pre_commit:, pre_push:, or post_commit: in its
.sparkwing/sparkwing.yaml triggers block.

The post-commit hook is non-blocking: the commit has already
landed, so it runs its pipelines, tolerates failures, and never
aborts. pre-commit and pre-push abort the git action on the first
failing pipeline.

Managed hooks carry an "Installed by sparkwing" marker so
uninstall and status can tell them apart from hand-written
hooks. Existing unmanaged hooks are left alone; install skips
them with a warning.`,
	SubcommandOrder: []string{"install", "uninstall", "status"},
}

var cmdHooksInstall = Command{
	Path:     "sparkwing pipeline hooks install",
	Synopsis: "Install pre-commit / pre-push / post-commit git hooks from sparkwing.yaml triggers",
	Description: `Installs managed hooks for declared pre_commit, pre_push, and post_commit
triggers. Existing unmanaged hooks are preserved and reported.

Each gate runs successfully through the invoking executable before replacement
hooks are published. Existing hooks remain callable during verification. Publication uses atomic rename;
an installation failure restores prior managed hooks, forwarders, modes,
and configuration. A rejected installation exits nonzero and reports its
reason on stderr. --no-prove skips gate execution.

Without --profile, hooks use --sw-local-only. --profile NAME selects shared
storage. --fleet processes registered repositories and distinguishes installed
gates, gates that could not execute, and repositories declaring no blocking
gate.`,
	Flags: []FlagSpec{
		{Name: "fleet", Desc: "Install into every registered repo instead of one", Group: "Input"},
		{Name: "no-prove", Desc: "Claim core.hooksPath without running the gate first", Group: "Behavior"},
		{Name: "profile", Argument: "NAME", Desc: "Pin the hook's runs to this storage profile (default: local-only)", Group: "Storage"},
	},
	Examples: []Example{
		{"Install in the current repo", "sparkwing pipeline hooks install"},
		{"Install in a different repo", "sparkwing -C /path/to/repo pipeline hooks install"},
		{"Arm every registered repo", "sparkwing pipeline hooks install --fleet"},
		{"Pin the gate's runs to one store", "sparkwing pipeline hooks install --profile bucket"},
	},
}

var cmdHooksUninstall = Command{
	Path:     "sparkwing pipeline hooks uninstall",
	Synopsis: "Remove sparkwing-managed git hooks",
	Description: `Deletes every file under .git/hooks/ that carries the "Installed by sparkwing"
marker. Hand-written hooks are left alone.`,
	Flags: []FlagSpec{},
	Examples: []Example{
		{"Uninstall in the current repo", "sparkwing pipeline hooks uninstall"},
	},
}

var cmdHooksStatus = Command{
	Path:     "sparkwing pipeline hooks status",
	Synopsis: "Report declared, installed, and missing sparkwing hooks",
	Description: `Lists every managed hook file under .git/hooks/ along with the pipelines it
invokes. Declared hooks that are missing, shadowed, or borrowed are named with
the command that repairs them.

--prove attempts a commit with a managed gate instructed to refuse it and
reports whether the gate blocked Git. A control attempt with hooks disabled
must succeed, so an unrelated commit failure cannot count as a passing
diagnostic. The attempts use a temporary detached worktree and index; the
checkout and its branches remain unchanged. Only managed hooks carrying the
diagnostic guard execute; other hooks are reported as unprovable. It exits
nonzero unless every applicable repository refused the test commit through
its own gate, and it proves pre-commit hooks only. --fleet proves every
registered repository.

--all reports declared hooks for every registered repository as armed,
shadowed, uninstalled, or undeclared. A shadowed hook is installed but
core.hooksPath selects another location. The STATE column reads no-gate where
every declared hook fires and none of them is pre-commit or pre-push, because
nothing there refuses a commit or a push. Register other checkouts before
expecting them in the report; an unreadable registry produces an error.
--ungated selects repositories where commits or pushes run without a gate.`,
	Flags: []FlagSpec{
		{Name: "output", Short: "o", Argument: "pretty|json|plain", Desc: "Pretty on a terminal, NDJSON otherwise. Plain prints hook names.", Group: "Output"},
		{Name: "prove", Desc: "Make the gate refuse a test commit and report whether it did", ConflictsWith: []string{"all"}, Group: "Mode"},
		{Name: "fleet", Desc: "With --prove, prove every registered repo", RequiresFlags: []string{"prove"}, Group: "Mode"},
		{Name: "all", Desc: "Report the effective gates of every registered repo", Group: "Mode"},
		{Name: "ungated", Desc: "With --all, list only the repos git runs no gate for", RequiresFlags: []string{"all"}, Group: "Mode"},
	},
	Examples: []Example{
		{"Show hook status", "sparkwing pipeline hooks status"},
		{"Prove this repo's gate refuses a commit", "sparkwing pipeline hooks status --prove"},
		{"Prove every registered repo's gate", "sparkwing pipeline hooks status --prove --fleet"},
		{"Survey every registered repo's gates", "sparkwing pipeline hooks status --all"},
		{"Just the ungated repos", "sparkwing pipeline hooks status --all --ungated"},
	},
}

var cmdSecret = Command{
	Path:     "sparkwing secrets",
	Synopsis: "Manage secrets in this machine's local store or on a controller",
	Description: `Without --profile, reads and writes this machine's local secret store:
the secrets table of state.db in SPARKWING_HOME, which the sparkwing
daemon serves on its API socket and starts when needed. Local runs,
the dashboard ('sparkwing serve') and these commands share it. Every value is sealed
under the key in ~/.config/sparkwing/secrets.key
($XDG_CONFIG_HOME/sparkwing when that variable is set), which the first
stored secret creates; SPARKWING_SECRETS_KEY (base64 of 32 bytes)
overrides the file and SPARKWING_SECRETS_KEY_FILE moves it. Set either
in the environment the daemon starts in.

With --profile PROF, reads/writes the named profile's controller.
Used for prod / staging secrets that the cluster needs at run
time. Pipelines declare a typed Secrets provider to resolve their secrets.
'secrets list' masks values; 'secrets get' prints them.

A local run reads every local secret, including an unscoped one that
is not shared, because the daemon answers this machine's own account
as its administrator. The local store no longer reads secrets.env or
config.env: the daemon imports each file once and leaves it in place.`,
	SubcommandOrder: []string{"set", "get", "list", "delete", "rotate"},
}

var cmdSecretSet = Command{
	Path:     "sparkwing secrets set",
	Synopsis: "Store (or replace) a secret value",
	Description: `Stores --value (or the contents of --file) in the local secret store
when --profile is omitted, or uploads it to the named profile's
controller. Replaces any existing secret with that name.
Prefer --file for long or multi-line values so the raw text
does not land in shell history. A local secret without --pipeline is
shared with every pipeline.`,
	Flags: []FlagSpec{
		{Name: "name", Type: FlagString, Argument: "NAME", Desc: "Secret name (unique per controller)", Required: true, Group: "Input"},
		{Name: "value", Type: FlagString, Argument: "VALUE", Desc: "Secret value (prefer --file for long values)", RequiredWhen: "when --file is not set", ConflictsWith: []string{"file"}, Group: "Input"},
		{Name: "file", Type: FlagString, Argument: "PATH", Desc: "Read value from file (keeps value out of shell history)", RequiredWhen: "when --value is not set", ConflictsWith: []string{"value"}, Group: "Input"},
		{Name: "plain", Type: FlagBool, Desc: "Store a configuration value visible in run logs. Values are masked by default.", Group: "Input"},
		{Name: "pipeline", Type: FlagString, Argument: "NAME", Desc: "Scope the secret to one pipeline", ConflictsWith: []string{"shared"}, Group: "Input"},
		{Name: "shared", Type: FlagBool, Desc: "Let every run read this unscoped secret. On a controller, without --pipeline or --shared the secret answers admin callers only; locally it is always shared.", ConflictsWith: []string{"pipeline"}, Group: "Input"},
		{Name: "profile", Type: FlagString, Argument: "NAME", Desc: "Profile name (omit for the local store)", Group: "System"},
	},
	GroupOrder: []string{"Input", "System", "Other"},
	Examples: []Example{
		{"Set a local masked secret", "sparkwing secrets set --name API_TOKEN --value abc123"},
		{"Set from a file", "sparkwing secrets set --name TLS_CERT --file ./tls.crt --profile prod"},
		{"Set non-masked config", "sparkwing secrets set --name REGION --value us-east-1 --plain --profile prod"},
		{"Scope a secret to one pipeline", "sparkwing secrets set --name DEPLOY_KEY --file ./key --pipeline deploy --profile prod"},
		{"Let every run read one secret", "sparkwing secrets set --name NPM_TOKEN --file ./npmrc --shared --profile prod"},
	},
}

var cmdSecretGet = Command{
	Path:     "sparkwing secrets get",
	Synopsis: "Print a secret's raw value to stdout",
	Description: `Reads the local secret store when --profile is omitted, or the
named profile's controller. Prints only the raw value (no trailing newline)
so it can be piped into another command. Use 'secrets list' for metadata.`,
	Flags: []FlagSpec{
		{Name: "name", Type: FlagString, Argument: "NAME", Desc: "Secret name", Required: true, Group: "Input"},
		{Name: "pipeline", Type: FlagString, Argument: "NAME", Desc: "Read the row owned by one pipeline, falling back to the unscoped row", Group: "Input"},
		{Name: "profile", Type: FlagString, Argument: "NAME", Desc: "Profile name (omit for the local store)", Group: "System"},
	},
	GroupOrder: []string{"Input", "System", "Other"},
	Examples: []Example{
		{"Fetch a local secret", "sparkwing secrets get --name API_TOKEN"},
		{"Fetch a remote secret", "sparkwing secrets get --name API_TOKEN --profile prod"},
	},
}

var cmdSecretList = Command{
	Path:     "sparkwing secrets list",
	Synopsis: "List secret names + metadata",
	Description: `Lists secret names and metadata from the local secret store when --profile is
omitted, or from the named profile's controller. Raw values are never printed by this
command.`,
	Flags: []FlagSpec{
		{Name: "grep", Type: FlagString, Argument: "PATTERN", Desc: "Filter by name substring (case-sensitive)", Group: "Filter"},
		{Name: "profile", Type: FlagString, Argument: "NAME", Desc: "Profile name (omit for the local store)", Group: "System"},
	},
	GroupOrder: []string{"Filter", "System", "Other"},
	Examples: []Example{
		{"List local secrets", "sparkwing secrets list"},
		{"List secrets on prod", "sparkwing secrets list --profile prod"},
		{"Filter to API-related names", "sparkwing secrets list --profile prod --grep API"},
	},
}

var cmdSecretDelete = Command{
	Path:     "sparkwing secrets delete",
	Synopsis: "Remove a secret",
	Description: `Deletes the secret from the local secret store when --profile is omitted, or
from the named profile's controller. Pipelines that reference the name will fail to
resolve until the secret is re-added.`,
	Flags: []FlagSpec{
		{Name: "name", Type: FlagString, Argument: "NAME", Desc: "Secret name to remove", Required: true, Group: "Input"},
		{Name: "pipeline", Type: FlagString, Argument: "NAME", Desc: "Remove the row owned by one pipeline; omit for the unscoped row", Group: "Input"},
		{Name: "profile", Type: FlagString, Argument: "NAME", Desc: "Profile name (omit for the local store)", Group: "System"},
	},
	GroupOrder: []string{"Input", "System", "Other"},
	Examples: []Example{
		{"Delete a local secret", "sparkwing secrets delete --name API_TOKEN"},
		{"Delete a remote secret", "sparkwing secrets delete --name API_TOKEN --profile prod"},
	},
}

var cmdSecretRotate = Command{
	Path:     "sparkwing secrets rotate",
	Synopsis: "Re-encrypt every stored secret under the current key",
	Description: `Reads every secret the named profile's controller holds, or the local
store without --profile, and writes it back sealed under the key that
controller or this machine's daemon is running with now, in one
transaction. Run it after moving onto a new key with the old one still
configured as the controller's secrets-key.previous credential
(locally: set SPARKWING_SECRETS_KEY and SPARKWING_SECRETS_PREVIOUS_KEY and
run 'sparkwing daemon restart'); drop the old key once a rotation reports
nothing skipped. A value the controller was holding as plaintext comes out
encrypted too, which is how an existing install turns encryption on
without re-setting each secret by hand.

A row that opens under no configured key keeps the bytes it had and is
listed by name; the rest of the table still rotates. A controller
refuses when it has no key configured. Values never leave it: the
rotation opens and reseals them in place.`,
	Flags: []FlagSpec{
		{Name: "profile", Type: FlagString, Argument: "NAME", Desc: "Profile naming the controller to rotate (omit for the local store)", Group: "System"},
	},
	GroupOrder: []string{"System", "Other"},
	Examples: []Example{
		{"Re-encrypt prod secrets under the current key", "sparkwing secrets rotate --profile prod"},
		{"Re-encrypt local secrets after a key change", "sparkwing secrets rotate"},
	},
}

var cmdTriggers = Command{
	Path:     "sparkwing cluster triggers",
	Synopsis: "List or inspect controller triggers",
	Description: `Inspect the controller's queue of pipeline triggers. 'list' shows pending,
claimed, and completed entries. 'get' reads one trigger by identifier.
Select the controller with --profile NAME.

Submit work with 'sparkwing pipeline trigger <pipeline> --profile NAME'.`,
	SubcommandOrder: []string{"list", "get"},
	Examples: []Example{
		{"List pending triggers on prod", "sparkwing cluster triggers list --profile prod --status pending"},
		{"Inspect one trigger", "sparkwing cluster triggers get --id run-fictional --profile prod"},
		{"Submit a trigger", "sparkwing pipeline trigger fictional-deploy --profile prod"},
	},
}

var cmdTriggersList = Command{
	Path:     "sparkwing cluster triggers list",
	Synopsis: "List pending / claimed / done / failed triggers",
	Description: `Queries GET /api/v1/triggers on the selected profile's
controller. Empty filters return the most recent 20 entries
across all statuses.

Useful when the queue looks stuck ("why isn't my trigger being
claimed?"): --status pending shows unclaimed work, --status
claimed shows what a worker has in-flight. The repo filter
matches GITHUB_REPOSITORY on the trigger env so webhook-driven
entries match the selected repository; that value is not indexed, so the
search covers the newest 5,000 triggers matching the other filters and
an older entry is not reported.`,
	Flags: []FlagSpec{
		{Name: "status", Argument: "STATUS", Desc: "Filter by status: pending | claimed | done | failed", Group: "Filter"},
		{Name: "pipeline", Argument: "NAME", Desc: "Filter by pipeline name", Group: "Filter"},
		{Name: "repo", Argument: "OWNER/NAME", Desc: "Match GITHUB_REPOSITORY on the trigger env, over the newest 5,000 triggers", Group: "Filter"},
		{Name: "limit", Argument: "N", Desc: "Maximum triggers to show", Default: "20", Group: "Output"},
		{Name: "quiet", Short: "q", Desc: "Print only trigger ids, newline-separated", Group: "Output"},
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: json emits the raw triggers array", Group: "Output"},
		{Name: "profile", Argument: "NAME", Desc: "Profile name", Required: true, Group: "System"},
	},
	GroupOrder: []string{"Filter", "Output", "System", "Other"},
	Examples: []Example{
		{"Recent triggers on prod", "sparkwing cluster triggers list --profile prod"},
		{"Just pending", "sparkwing cluster triggers list --profile prod --status pending"},
		{"Pipeline-specific, JSON", "sparkwing cluster triggers list --profile prod --pipeline fictional-build --limit 5 -o json"},
	},
}

var cmdTriggersGet = Command{
	Path:     "sparkwing cluster triggers get",
	Synopsis: "Inspect one trigger's full metadata by id",
	Description: `Fetches GET /api/v1/triggers/{id} and prints the full row (pipeline, args,
git, env, status, claim lease). Defaults to a compact multi-line rendering; -o
json emits the raw response.`,
	Flags: []FlagSpec{
		{Name: "id", Argument: "TRIGGER_ID", Desc: "Trigger / run identifier (the value 'pipeline trigger' prints)", Required: true, Group: "Input"},
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: json emits the raw response", Group: "Output"},
		{Name: "profile", Argument: "NAME", Desc: "Profile name", Required: true, Group: "System"},
	},
	GroupOrder: []string{"Input", "Output", "System", "Other"},
	Examples: []Example{
		{"Inspect one trigger", "sparkwing cluster triggers get --id run-fictional --profile prod"},
		{"Raw JSON for scripting", "sparkwing cluster triggers get --id run-fictional --profile prod -o json"},
	},
}

var cmdAgents = Command{
	Path:     "sparkwing cluster agents",
	Synopsis: "Inspect the controller's fleet view",
	Description: `Hits GET /api/v1/agents on the selected profile's controller.
Prints persisted executor registrations, including idle and
offline agents and gateways, plus recent legacy claim-only runners.`,
	SubcommandOrder: []string{"list", "enroll"},
	Examples: []Example{
		{"List prod agents", "sparkwing cluster agents list --profile prod"},
	},
}

var cmdRunners = Command{
	Path:     "sparkwing cluster runners",
	Synopsis: "Enroll and retire this machine as a runner",
	Description: `Turns one machine into a runner for the selected profile's
controller in a single command. 'add' mints a scoped runner token, writes the
owner-only agent config, and installs the user service. 'remove' stops that
service and revokes the token.

Use 'sparkwing cluster agents list' to see the runners a controller knows
about.`,
	SubcommandOrder: []string{"add", "remove"},
	Examples: []Example{
		{"Enroll this machine", "sparkwing cluster runners add --profile prod --name dev-laptop"},
		{"Retire this machine", "sparkwing cluster runners remove --profile prod"},
	},
}

var cmdRunnersAdd = Command{
	Path:     "sparkwing cluster runners add",
	Synopsis: "Mint a runner token, write the config, start the service",
	Description: `Mints a runner token carrying nodes.claim, triggers.claim,
runs.state, secrets.read and logs.write against the profile's controller,
writes the agent section of ~/.config/sparkwing/config.yaml at mode 0600, then installs and starts
the user service: a systemd user unit on Linux, a LaunchAgent on macOS. On
Windows it prints the manual supervision steps instead.

The section is written in claim mode, which is the mode that executes work.
An existing agent section is never replaced without --force, because the token
it holds stays live until it is revoked. Every other section of the file is
kept.

Nothing is minted until the config validates and the machine answers: a
missing sparkwing-runner, an unreachable service manager, or an unusable
setting fails first. If a step after the mint fails, the output names the live
token and the command that revokes it.

With --allow-repo the agent fetches each run's source itself, from the
repositories the list names, with the credential the controller releases or
else this machine's own git credentials. Without it the agent fetches through
the controller's gitcache proxy.

The command prints the token prefix and the revoke command. The raw token
reaches only config.yaml.`,
	Flags: []FlagSpec{
		{Name: "name", Argument: "NAME", Desc: "Runner name, shown in the dashboard", Required: true, Group: "Identity"},
		{Name: "allow-repo", Argument: "PATTERN", Desc: "Repository this machine may build and fetch directly, as host/path with '*' within one segment (repeatable)", Group: "Identity"},
		{Name: "labels", Argument: "CSV", Desc: "Comma-separated self-asserted placement labels", Group: "Identity"},
		{Name: "max-concurrent", Argument: "N", Desc: "Concurrent jobs this machine accepts", Default: "2", Group: "Limits"},
		{Name: "contribution", Argument: "SPEC", Desc: "CPU and memory this machine contributes (4,8gb or 50%,50%)", Default: "50%,50%", Group: "Limits"},
		{Name: "logs", Argument: "URL", Desc: "Logs service URL (default: the profile's logs surface, then the controller's announcement)", Group: "Input"},
		{Name: "config", Argument: "PATH", Desc: "config.yaml whose agent section to write (default: ~/.config/sparkwing/config.yaml)", Group: "Input"},
		{Name: "force", Desc: "Replace an existing agent section", Group: "Input"},
		{Name: "no-service", Desc: "Write the config without installing or starting the service", Group: "System"},
		{Name: "profile", Argument: "NAME", Desc: "Profile naming the controller to enroll against", Required: true, Group: "System"},
	},
	GroupOrder: []string{"Identity", "Limits", "Input", "System", "Other"},
	Examples: []Example{
		{"Enroll this machine", "sparkwing cluster runners add --profile prod --name dev-laptop"},
		{"Enroll with a capacity ceiling and labels", "sparkwing cluster runners add --profile prod --name build-box --max-concurrent 4 --contribution 4,8gb --labels linux,arch=amd64"},
		{"Write the config and supervise the agent yourself", "sparkwing cluster runners add --profile prod --name dev-laptop --no-service"},
		{"Fetch source directly for the team's repositories", "sparkwing cluster runners add --profile prod --name dev-laptop --allow-repo 'github.com/acme/*'"},
	},
}

var cmdRunnersRemove = Command{
	Path:     "sparkwing cluster runners remove",
	Synopsis: "Stop the runner service and revoke its token",
	Description: `Reads the token out of the agent config, stops and removes the
user service, then revokes that token on the profile's controller. The service
stops first, so a claim in flight finishes against a credential that still
authenticates. A prefix the controller reports as anything but a runner token
is refused, naming what it found.

A service file that runs a different agent config is left alone.

The config file stays on disk holding the revoked token; 'runners add --force'
replaces it.`,
	Flags: []FlagSpec{
		{Name: "config", Argument: "PATH", Desc: "config.yaml whose agent section holds the token (default: ~/.config/sparkwing/config.yaml)", Group: "Input"},
		{Name: "no-service", Desc: "Revoke the token without touching the service", Group: "System"},
		{Name: "profile", Argument: "NAME", Desc: "Profile naming the controller that issued the token", Required: true, Group: "System"},
	},
	GroupOrder: []string{"Input", "System", "Other"},
	Examples: []Example{
		{"Retire this machine", "sparkwing cluster runners remove --profile prod"},
	},
}

var cmdAgentsEnroll = Command{
	Path:     "sparkwing cluster agents enroll",
	Synopsis: "Enroll or update a trusted executor",
	Description: `Binds one exact runner or service token prefix to an
operator-owned executor envelope. The token must be live and carry
nodes.claim; its stored principal becomes audit metadata. Re-enrollment
with the same credential updates trusted scheduling fields without changing
live headroom. Changing the prefix requires a new heartbeat.

Use a distinct revocable token for every coordinator membership. The
prefix is accepted as input but is never returned by the agents API. A
controller accepts at most 256 enrolled executors. Adding another returns ` + "`executor enrollment limit reached: maximum 256 per controller`" + `.`,
	Flags: []FlagSpec{
		{Name: "name", Argument: "NAME", Desc: "Executor name", Required: true, Group: "Identity"},
		{Name: "token-prefix", Argument: "PREFIX", Desc: "Exact runner or service token prefix", Required: true, Group: "Identity"},
		{Name: "kind", Argument: "KIND", Desc: "Executor kind (agent|gateway)", Default: "agent", Group: "Identity"},
		{Name: "location", Argument: "WHERE", Desc: "Trusted placement location (local|cloud|unknown)", Default: "unknown", Group: "Identity"},
		{Name: "capability", Argument: "LABEL", Desc: "Trusted capability (repeatable)", Group: "Trust"},
		{Name: "base-priority", Argument: "N", Desc: "Base scheduling priority (0-100)", Default: "0", Group: "Trust"},
		{Name: "priority-ceiling", Argument: "N", Desc: "Highest effective priority (0-100)", Default: "100", Group: "Trust"},
		{Name: "max-concurrent", Argument: "N", Desc: "Trusted concurrent slot ceiling", Default: "1", Group: "Limits"},
		{Name: "budget-cores", Argument: "N", Desc: "CPU contribution ceiling (0 = uncapped)", Default: "0", Group: "Limits"},
		{Name: "budget-memory-bytes", Argument: "N", Desc: "Memory contribution ceiling in bytes (0 = uncapped)", Default: "0", Group: "Limits"},
		{Name: "profile", Argument: "NAME", Desc: "Admin controller profile", Required: true, Group: "System"},
	},
	GroupOrder: []string{"Identity", "Trust", "Limits", "System", "Other"},
	Examples: []Example{
		{"Enroll a workstation agent", "sparkwing cluster agents enroll --profile prod --name desk --token-prefix swr_01234567 --kind agent --location local --capability linux --max-concurrent 2 --budget-cores 4 --budget-memory-bytes 8589934592"},
		{"Enroll a capacity gateway", "sparkwing cluster agents enroll --profile prod --name build-gateway --token-prefix sws_01234567 --kind gateway --location cloud --capability linux-amd64 --max-concurrent 8"},
	},
}

var cmdAgentsList = Command{
	Path:     "sparkwing cluster agents list",
	Synopsis: "Print the controller's known agents",
	Description: `Fetches /api/v1/agents and renders a table of fleet members.
Registered executors report their operator-assigned identity,
kind, trusted placement location, capabilities, concurrency limit, and
measured resource headroom. A stale registration remains visible
as offline; recent legacy claim-only runners remain visible too.

Use -q to print names, one per line, for shell piping
(xargs and similar commands).`,
	Flags: []FlagSpec{
		{Name: "profile", Argument: "NAME", Desc: "Profile name", Required: true, Group: "System"},
		{Name: "output", Short: "o", Argument: "FMT", Desc: "Output format (json|table)", Group: "Output"},
		{Name: "quiet", Short: "q", Desc: "Print agent names, one per line", Group: "Output"},
	},
	GroupOrder: []string{"Output", "System", "Other"},
	Examples: []Example{
		{"List agents on prod", "sparkwing cluster agents list --profile prod"},
		{"Just agent names for piping", "sparkwing cluster agents list --profile prod -q"},
	},
}

var cmdFleet = Command{
	Path:     "sparkwing fleet",
	Synopsis: "Configure foreground assisted execution",
	Description: `Local fleet configuration. Running a pipeline with assistance uses
sparkwing run PIPELINE --sw-fleet, and the fleet section of config.yaml names
the helpers it trusts.

Fleet runs transmit an immutable snapshot containing every tracked file and
every non-ignored untracked file to the executor that wins a node. Review
'git status' and ignore local secret files before starting a fleet run. Normal
output reports only the source digest, file count, and total bytes, never file
names. The snapshot commit has no parent and does not transmit repository
history.`,
	SubcommandOrder: []string{"init"},
}

var cmdFleetInit = Command{
	Path:     "sparkwing fleet init",
	Synopsis: "Create an owner-only foreground fleet policy",
	Description: `Writes the fleet section of config.yaml without replacing an existing
policy or any other section. The listener is
fixed for the life of each foreground run. HTTPS public URLs assume a local
Tailscale Serve or reverse proxy and therefore require a literal loopback
listener. Plain HTTP is accepted only at a literal IP that the local Tailscale
client confirms belongs to this machine. Tailscale supplies transport, not
Sparkwing authorization: only explicitly enrolled helpers receive credentials,
and no peer discovery occurs.

SPARKWING_HOME does not move config.yaml; it is the state, cache and
logs root, and the fleet policy is machine-wide. A write from a
command running under a home of its own is refused rather than sent
to the machine's policy: set SPARKWING_CONFIG to a path inside
that home to keep it there.`,
	Flags: []FlagSpec{
		{Name: "tailnet", Desc: "Use this machine's Tailscale IPv4 address on port 4346", Group: "Network"},
		{Name: "listen", Argument: "HOST:PORT", Desc: "Fixed private listener address", Group: "Network"},
		{Name: "public-url", Argument: "URL", Desc: "Helper-reachable coordinator origin", Group: "Network"},
		{Name: "allow-tailnet-http", Desc: "Allow HTTP at a verified literal local Tailscale IP", Group: "Network"},
	},
	GroupOrder: []string{"Network", "Other"},
	Examples: []Example{
		{"Direct Tailscale transport", "sparkwing fleet init --tailnet"},
		{"Tailscale Serve or a local proxy", "sparkwing fleet init --listen 127.0.0.1:4346 --public-url https://runner.example.com"},
		{"Advanced direct Tailscale transport", "sparkwing fleet init --listen 100.64.1.2:4346 --public-url http://100.64.1.2:4346 --allow-tailnet-http"},
	},
}

var cmdClusterConcurrency = Command{
	Path:     "sparkwing cluster concurrency",
	Synopsis: "Inspect a single concurrency namespace: holders + queue",
	Description: `Shows who holds a concurrency namespace's slots
and the queue of waiters behind it, each with its admission-rank
position. Weighted admission can run a later fitting waiter before
an earlier non-fitting waiter, so position is not always run order.
Use it to tell whether a node is wedged or waiting for budget.

Hits GET /api/v1/concurrency/{namespace}/state on the
selected profile's controller.

For a controller's whole admission state -- every key, its holders and
waiters, and each registered runner's free capacity -- through the same
view as the local queue, use 'sparkwing queue --profile NAME'. This
command narrows to one namespace.`,
	Flags: []FlagSpec{
		{Name: "namespace", Argument: "NAME", Desc: "Concurrency namespace to inspect", Required: true, Group: "Input"},
		{Name: "profile", Argument: "NAME", Desc: "Profile selecting the controller", Required: true, Group: "System"},
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format (json|table)", Group: "Output"},
	},
	GroupOrder: []string{"Input", "Output", "System", "Other"},
	Examples: []Example{
		{"Who holds and who's queued", "sparkwing cluster concurrency --namespace deploy-prod --profile prod"},
	},
}

var cmdClusterObjectStore = Command{
	Path:     "sparkwing cluster object-store",
	Synopsis: "Operate the controller's object-store request budget",
	Description: `The controller counts every object-store request it makes, by class
(put, get, list, delete), against a per-minute rate and a per-day
budget. A class that spends either budget trips: writes of that class
fail closed and reads keep serving until their own budget trips. The
state appears on 'sparkwing cloud status' and on the controller's
Prometheus metrics as sparkwing_object_store_requests_total,
sparkwing_object_store_trips_total, and sparkwing_object_store_tripped.

Budgets come from the controller's --object-store-budget, as
class:window=count entries such as put:minute=600. A class tripped by its
day budget clears when the day window rolls.

The same breaker carries the bucket ceiling. A controller started with
--max-bucket-bytes or --max-bucket-objects measures the bucket on an
interval, freezes object writes once it holds more than the ceiling,
and reports the freeze on health and as
sparkwing_object_store_bucket_ceiling_frozen. Buckets are unlimited by
default.`,
	SubcommandOrder: []string{"status", "reset-breaker"},
	Examples: []Example{
		{"Clear a tripped budget", "sparkwing cluster object-store reset-breaker --profile prod"},
	},
}

var cmdClusterObjectStoreStatus = Command{
	Path:     "sparkwing cluster object-store status",
	Synopsis: "Show the controller's object-store request budget",
	Description: `Prints each request class with its per-minute rate, its per-day budget,
how much of each window the controller has spent, how many times the
class has tripped, and whether it is refusing requests now, followed by
the bucket ceiling: what the bucket holds, the ceilings it is held to,
and whether object writes are frozen. Changes nothing.

Hits GET /api/v1/object-store/breaker on the selected profile's
controller, which needs an admin-scoped token.`,
	Flags: []FlagSpec{
		{Name: "profile", Argument: "NAME", Desc: "Profile selecting the controller", Required: true, Group: "System"},
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format (json|table)", Group: "Output"},
	},
	GroupOrder: []string{"Output", "System", "Other"},
	Examples: []Example{
		{"Read the budget", "sparkwing cluster object-store status --profile prod"},
	},
}

var cmdClusterObjectStoreResetBreaker = Command{
	Path:     "sparkwing cluster object-store reset-breaker",
	Synopsis: "Clear a tripped object-store budget or ceiling freeze",
	Description: `Clears every tripped request class on the selected controller, resets
its per-minute and per-day window counters, and thaws a frozen bucket
ceiling, then prints the budget as it stands. Lifetime request and trip
totals survive, so the metrics keep their history. A thawed bucket that
is still over its ceiling freezes again at the next measurement, so a
thaw buys the window to delete objects or raise the ceiling.

Reach for this after fixing what caused the trip. A budget that keeps
tripping wants a larger limit or a caller that stops retrying, not a
repeated reset.

Hits POST /api/v1/object-store/reset-breaker on the selected
profile's controller, which needs an admin-scoped token.`,
	Flags: []FlagSpec{
		{Name: "profile", Argument: "NAME", Desc: "Profile selecting the controller", Required: true, Group: "System"},
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format (json|table)", Group: "Output"},
	},
	GroupOrder: []string{"Output", "System", "Other"},
	Examples: []Example{
		{"Clear a tripped budget", "sparkwing cluster object-store reset-breaker --profile prod"},
	},
}

var cmdSparks = Command{
	Path:     "sparkwing pipeline sparks",
	Synopsis: "Manage sparks libraries declared in .sparkwing/sparkwing.yaml",
	Description: `Sparks libraries are Go modules that add opinionated helpers
(Docker builds, GitOps deploys, ECR auth, language-specific
checks) on top of the unopinionated SDK. Consumers declare
which libraries they want live-tracked in the sparks: block
of .sparkwing/sparkwing.yaml; the resolver writes an overlay modfile
at .sparkwing/.resolved.mod that the compile step uses via
'go build -modfile='. The consumer's tracked go.mod is
never modified.

See docs/sparks.md for the full spec (spark.json schema,
sparks: block shape, resolution rules, warmup).`,
	SubcommandOrder: []string{"catalog", "list", "lint", "resolve", "update", "add", "remove", "warmup", "inflate"},
	Examples: []Example{
		{"See what a library offers", "sparkwing pipeline sparks catalog"},
		{"List declared sparks libraries", "sparkwing pipeline sparks list"},
		{"Validate a library's spark.json", "sparkwing pipeline sparks lint ~/code/fictional-sparks"},
		{"Re-materialize the overlay modfile", "sparkwing pipeline sparks resolve"},
		{"Add a library pinned to latest", "sparkwing pipeline sparks add example.com/fictional/sparks"},
	},
}

var cmdSparksList = Command{
	Path:     "sparkwing pipeline sparks list",
	Synopsis: "Show declared sparks libraries and their resolved versions",
	Description: `Reads the sparks: block and prints one row per declared
library with its declared constraint and the resolved tag
(found via the module proxy). Use --no-resolve to skip the
proxy calls when offline.`,
	Flags: []FlagSpec{
		{Name: "output", Short: "o", Argument: "FMT", Desc: "Output format: pretty|json|plain", Group: "Output"},
		{Name: "no-resolve", Desc: "Skip module-proxy lookups; print declared versions only", Group: "Input"},
	},
	GroupOrder: []string{"Input", "Output", "Other"},
	Examples: []Example{
		{"Table output", "sparkwing pipeline sparks list"},
		{"JSON for scripting", "sparkwing pipeline sparks list -o json"},
		{"Offline (no proxy calls)", "sparkwing pipeline sparks list --no-resolve"},
	},
}

var cmdSparksLint = Command{
	Path:     "sparkwing pipeline sparks lint",
	Synopsis: "Validate a spark.json library manifest",
	Description: `Loads spark.json from the given path (or the current directory
if omitted) and checks: required fields (name, description,
author), that the manifest declares exactly one non-empty
entry array -- packages[] for a library that is one Go module,
modules[] for a monorepo of independently tagged modules --
that each entry path exists as a directory under the manifest
root and describes itself, that a modules[] entry names the Go
module its directory's go.mod declares, that stability values
are valid, and that paths are not duplicated. Unknown fields
are a soft warning, not an error. Exits non-zero on any hard
failure.`,
	PosArgs: []PosArg{
		{Name: "[path]", Desc: "Library directory or spark.json path, when --path is not supplied"},
	},
	Flags: []FlagSpec{
		{Name: "path", Argument: "PATH", Desc: "Library directory or direct spark.json path. Positional fallback accepted.", Default: ".", Group: "Input"},
	},
	GroupOrder: []string{"Input", "Other"},
	Examples: []Example{
		{"Lint the library in the current directory", "sparkwing pipeline sparks lint"},
		{"Lint a sibling library by path", "sparkwing pipeline sparks lint --path ~/code/fictional-sparks"},
		{"Lint a multi-module monorepo", "sparkwing pipeline sparks lint ~/code/fictional-sparks"},
	},
}

var cmdSparksResolve = Command{
	Path:     "sparkwing pipeline sparks resolve",
	Synopsis: "Resolve versions and materialize the overlay modfile",
	Description: `Resolves declared libraries through the Go module proxy and writes the
module overlay used for pipeline builds. Prints 'up-to-date' when the
overlay already matches. The repository's module file stays unchanged.`,
	Flags: []FlagSpec{
		{Name: "quiet", Short: "q", Desc: "Suppress the 'up-to-date' message", Group: "Output"},
	},
	Examples: []Example{
		{"Resolve and write the overlay", "sparkwing pipeline sparks resolve"},
		{"Quiet mode for scripts", "sparkwing pipeline sparks resolve -q"},
	},
}

var cmdSparksUpdate = Command{
	Path:     "sparkwing pipeline sparks update",
	Synopsis: "Re-resolve every declared library",
	Description: `Re-runs resolution for every declared library and
re-materializes the overlay modfile. For a range or 'latest'
constraint this picks up any new tag from the module proxy;
for an exact pin it is a no-op.

The overlay is rebuilt from the whole manifest in one pass, so
there is no single-library update: --name is refused. To hold
one library still, pin its "version:" field in
.sparkwing/sparkwing.yaml.`,
	Flags: []FlagSpec{
		{Name: "name", Argument: "NAME", Desc: "Refused; update re-resolves every declared library", Group: "Input"},
	},
	GroupOrder: []string{"Input", "Other"},
	Examples: []Example{
		{"Update every declared library", "sparkwing pipeline sparks update"},
	},
}

var cmdSparksAdd = Command{
	Path:     "sparkwing pipeline sparks add",
	Synopsis: "Add a library to the sparks: block",
	Description: `Appends an entry to the sparks: block. Defaults the
version to 'latest' when --version is omitted. Refuses to add
a duplicate (same source or same name).`,
	Flags: []FlagSpec{
		{Name: "source", Argument: "PATH", Desc: "Go module path", Required: true, Group: "Input"},
		{Name: "version", Argument: "VER", Desc: "Declared version ('latest', exact tag, or semver range)", Group: "Input"},
		{Name: "name", Argument: "NAME", Desc: "Short library name (default: last path segment of --source)", Group: "Input"},
	},
	GroupOrder: []string{"Input", "Other"},
	Examples: []Example{
		{"Add a library pinned to latest", "sparkwing pipeline sparks add --source example.com/fictional/sparks"},
		{"Add with a semver range", `sparkwing pipeline sparks add --source example.com/fictional/sparks --version "^v0.10.0"`},
	},
}

var cmdSparksRemove = Command{
	Path:        "sparkwing pipeline sparks remove",
	Synopsis:    "Remove a library from the sparks: block",
	Description: `Removes the entry matching NAME (or matching its source path).`,
	Flags: []FlagSpec{
		{Name: "name", Argument: "NAME", Desc: "Library name or source path to remove", Required: true, Group: "Input"},
	},
	GroupOrder: []string{"Input", "Other"},
	Examples: []Example{
		{"Remove by short name", "sparkwing pipeline sparks remove --name fictional-sparks"},
		{"Remove by source path", "sparkwing pipeline sparks remove --name example.com/fictional/sparks"},
	},
}

var cmdSparksWarmup = Command{
	Path:     "sparkwing pipeline sparks warmup",
	Synopsis: "Pre-compile pipeline binaries after a sparks release",
	Description: `Resolves libraries, compiles the pipeline binary, and uploads it to the
binary cache. Subsequent runs with matching build inputs can reuse it.
Warmup uses the same compilation path and cache key as 'sparkwing run'.`,
	Flags: []FlagSpec{
		{Name: "clear-cache", Desc: "Delete the local pipeline binary cache before compiling", Group: "Input"},
	},
	Examples: []Example{
		{"Warm up the current repo's pipelines", "sparkwing pipeline sparks warmup"},
		{"Force a fresh compile", "sparkwing pipeline sparks warmup --clear-cache"},
	},
}

var cmdSparksCatalog = Command{
	Path:     "sparkwing pipeline sparks catalog",
	Synopsis: "List the blocks a spark library offers",
	Description: `Reads a library's spark.json and prints one row per block it
declares, with its stability and what it does. 'sparks list'
shows the libraries this repo already declares; catalog shows
what is inside one.

A monorepo library declares 'modules', each independently
tagged, and each row's name is what 'sparks inflate --module'
takes. A single-module library declares 'packages' instead, and
those are import packages rather than modules; inflate that
library by its own module path.

Without --library the catalog reads sparks-core. A library the
repo declares in its sparks: block is read at the version
declared there; any other resolves to latest. --path reads a
checkout on disk and never touches the network.

-o plain prints one row per line: a modules[] row as its module
path, which is what 'sparks inflate --module' takes, and a
packages[] row as its package name.`,
	Flags: []FlagSpec{
		{Name: "library", Argument: "MODULE", Desc: "Spark library module path (default: github.com/sparkwing-dev/sparks-core)", Group: "Input"},
		{Name: "path", Argument: "DIR", Desc: "Read a library checkout on disk instead of downloading it", Group: "Input"},
		{Name: "output", Short: "o", Argument: "FMT", Desc: "Output format: pretty|json|plain", Group: "Output"},
	},
	GroupOrder: []string{"Input", "Output", "Other"},
	Examples: []Example{
		{"What sparks-core offers", "sparkwing pipeline sparks catalog"},
		{"Block names for a script", "sparkwing pipeline sparks catalog -o plain"},
		{"Another library", "sparkwing pipeline sparks catalog --library example.com/fictional/sparks"},
		{"A checkout on disk", "sparkwing pipeline sparks catalog --path ~/code/fictional-sparks"},
	},
}

var cmdSparksInflate = Command{
	Path:     "sparkwing pipeline sparks inflate",
	Synopsis: "Copy a spark library's source into this repo so you can edit it",
	Description: `Copies a library from the Go module cache into the pipeline's local sources,
adds a module replacement pointing at that copy, and runs 'go mod tidy'.
Imports retain their module paths.

--module accepts a sparks-core module name or a full module path. The version
comes from the pipeline's required modules, or resolves to latest when absent.
The destination must be unused. To undo the copy, remove its directory and
module replacement.

'sparkwing pipeline sparks catalog' names every module a library offers.`,
	Flags: []FlagSpec{
		{
			Name: "module", Argument: "NAME", Desc: "Sparks-core module name or full module path",
			Required:     true,
			RequiredHint: "`sparkwing pipeline sparks catalog` names every module the library offers",
			Group:        "Input",
		},
		{Name: "output", Short: "o", Argument: "FMT", Desc: "Output format: pretty|json", Group: "Output"},
	},
	GroupOrder: []string{"Input", "Output", "Other"},
	Examples: []Example{
		{"Inflate the sparks-core templates module", "sparkwing pipeline sparks inflate --module templates"},
		{"Inflate any spark library by module path", "sparkwing pipeline sparks inflate --module github.com/example/my-sparks"},
		{"See the module names first", "sparkwing pipeline sparks catalog"},
	},
}

var cmdApprove = Command{
	Path:     "sparkwing runs approvals approve",
	Synopsis: "Approve a pending approval-gate node",
	Description: `Resolves the named approval gate as 'approved'. The gate's
downstream nodes begin dispatching on the next orchestrator
poll (roughly 500ms). The approver is recorded from the
authenticated principal when --profile is set, or from $USER in
local mode.

Exit code is 0 on success, non-zero if the gate doesn't exist
or was already resolved (409).`,
	Flags: []FlagSpec{
		{Name: "run", Argument: "ID", Desc: "Run ID holding the approval gate", Required: true, Group: "Target"},
		{Name: "node", Argument: "ID", Desc: "Node ID of the approval gate", Required: true, Group: "Target"},
		{Name: "comment", Argument: "STR", Desc: "Optional note recorded on the approval", Group: "Input"},
		{Name: "profile", Argument: "NAME", Desc: "Profile name; omit for local-only", Group: "System"},
	},
	GroupOrder: []string{"Target", "Input", "System", "Other"},
	Examples: []Example{
		{"Approve a local gate", "sparkwing runs approvals approve --run run-fictional --node approve-prod"},
		{"Approve a prod gate with a comment", `sparkwing runs approvals approve --run run-fictional --node approve-prod --profile prod --comment "release notes ok"`},
	},
}

var cmdDeny = Command{
	Path:     "sparkwing runs approvals deny",
	Synopsis: "Deny a pending approval-gate node",
	Description: `Resolves the named approval gate as 'denied'. The gated node
fails; downstream nodes see the failure and propagate per
their ContinueOnError / Optional settings.`,
	Flags: []FlagSpec{
		{Name: "run", Argument: "ID", Desc: "Run ID holding the approval gate", Required: true, Group: "Target"},
		{Name: "node", Argument: "ID", Desc: "Node ID of the approval gate", Required: true, Group: "Target"},
		{Name: "comment", Argument: "STR", Desc: "Optional note recorded on the approval", Group: "Input"},
		{Name: "profile", Argument: "NAME", Desc: "Profile name; omit for local-only", Group: "System"},
	},
	GroupOrder: []string{"Target", "Input", "System", "Other"},
	Examples: []Example{
		{"Deny a local gate", "sparkwing runs approvals deny --run run-fictional --node approve-prod"},
		{"Deny a prod gate with a reason", `sparkwing runs approvals deny --run run-fictional --node approve-prod --profile prod --comment "tests still red"`},
	},
}

var cmdApprovals = Command{
	Path:     "sparkwing runs approvals",
	Synopsis: "List approval gates (pending and history)",
	Description: `Inspect approval gates. Without --run returns every pending
gate across all runs; with --run returns one run's full history
(pending + resolved).`,
	SubcommandOrder: []string{"list", "approve", "deny"},
}

var cmdApprovalsList = Command{
	Path:     "sparkwing runs approvals list",
	Synopsis: "List pending approvals (or one run's history)",
	Description: `Prints a table of approval rows. Without --run the list is the
cross-run pending queue; with --run it's every approval for that
run, both pending and resolved.`,
	Flags: []FlagSpec{
		{Name: "run", Argument: "RUN_ID", Desc: "Restrict to one run's approvals", Group: "Filter"},
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty|json|plain", Group: "Output"},
		{Name: "profile", Argument: "NAME", Desc: "Profile name; omit for local-only", Group: "System"},
	},
	GroupOrder: []string{"Filter", "Output", "System", "Other"},
	Examples: []Example{
		{"Pending gates on the local store", "sparkwing runs approvals list"},
		{"Pending gates on prod", "sparkwing runs approvals list --profile prod"},
		{"Full history for one run", "sparkwing runs approvals list --run run-fictional"},
		{"Emit JSON for an agent", "sparkwing runs approvals list -o json"},
	},
}

var cmdAnnotations = Command{
	Path:     "sparkwing runs annotations",
	Synopsis: "Read or append persistent node + step annotations",
	Description: `Annotations are short summary strings that pipelines (via
sparkwing.Annotate) and agents append to a node or step during a
run. They show up on the dashboard alongside outcome. This verb
lets an agent read every annotation on a run or contribute one
without going through the SDK.`,
	SubcommandOrder: []string{"list", "add"},
}

var cmdAnnotationsList = Command{
	Path:     "sparkwing runs annotations list",
	Synopsis: "List annotations on a run",
	Description: `Prints node-level annotations by default. Pass --steps to also
include per-step annotations as separate rows; passing --step
implies step-scope and limits to the matching step.`,
	Flags: []FlagSpec{
		{Name: "run", Argument: "RUN_ID", Desc: "Run identifier", Required: true, Group: "Input"},
		{Name: "node", Argument: "NODE_ID", Desc: "Limit to one node", Group: "Filter"},
		{Name: "step", Argument: "STEP_ID", Desc: "Limit to one step (implies step-scope reads)", Group: "Filter"},
		{Name: "steps", Desc: "Include per-step annotations", Group: "Filter"},
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty|json|plain", Group: "Output"},
		{Name: "profile", Argument: "NAME", Desc: "Profile name; omit for local-only", Group: "System"},
	},
	GroupOrder: []string{"Input", "Filter", "Output", "System", "Other"},
	Examples: []Example{
		{"Every node annotation on a run", "sparkwing runs annotations list --run run-fictional"},
		{"Include per-step annotations", "sparkwing runs annotations list --run run-fictional --steps"},
		{"One node's annotations as JSON", "sparkwing runs annotations list --run run-fictional --node build -o json"},
	},
}

var cmdAnnotationsAdd = Command{
	Path:     "sparkwing runs annotations add",
	Synopsis: "Append an annotation to a node or step",
	Description: `Appends one message to the annotations list on a node, or on a
step when --step is given. Annotations are append-only; the same
message string can be added more than once and the order is
preserved as the dashboard renders them.`,
	Flags: []FlagSpec{
		{Name: "run", Argument: "RUN_ID", Desc: "Run identifier", Required: true, Group: "Input"},
		{Name: "node", Argument: "NODE_ID", Desc: "Node identifier", Required: true, Group: "Input"},
		{Name: "step", Argument: "STEP_ID", Desc: "Step identifier (annotates the step instead of the node)", Group: "Input"},
		{Name: "message", Short: "m", Argument: "TEXT", Desc: "Annotation text", Required: true, Group: "Input"},
		{Name: "profile", Argument: "NAME", Desc: "Profile name; omit for local-only", Group: "System"},
	},
	GroupOrder: []string{"Input", "System", "Other"},
	Examples: []Example{
		{"Note something on a node", "sparkwing runs annotations add --run run-fictional --node deploy -m 'agent: retried after 502'"},
		{"Note something on a step inside a node", "sparkwing runs annotations add --run run-fictional --node deploy --step canary -m 'rolled out 5%'"},
	},
}

var cmdRepos = Command{
	Path:     "sparkwing repos",
	Synopsis: "The machine's fleet of sparkwing repos and their SDK pins",
	Description: `'list' shows registered repositories and repositories with recorded pipeline
runs, with their SDK pins; 'info' inspects one; 'update' validates or applies
SDK upgrades. 'add', 'remove' and 'prune' edit the registry of checkouts.

The registry maps pipeline names to local checkouts so
cross-repo RunAndAwait calls resolve without hardcoded WithFreshRepo
annotations. Auto-populated when you run 'sparkwing run <pipeline>'
in a .sparkwing/-bearing repo; set repos.auto_register: false in
config.yaml to disable it, and repos.include_worktrees: true to let it
register linked git worktrees, which it otherwise skips.

The registry is the repos section of config.yaml: $SPARKWING_CONFIG
(if set), else $XDG_CONFIG_HOME/sparkwing/config.yaml, else
~/.config/sparkwing/config.yaml. SPARKWING_HOME does not move it; it
is the state, cache and logs root, and a registered checkout is a
machine-wide fact that outlives any one home. A write from a command
running under a home of its own is refused rather than sent to the
machine's registry: set SPARKWING_CONFIG to a path inside that home
to keep it there.`,
	SubcommandOrder: []string{"list", "info", "update", "add", "remove", "prune"},
	Examples: []Example{
		{"List the fleet", "sparkwing repos list"},
		{"Register the current checkout", "sparkwing repos add"},
		{"Drop entries whose checkout is gone", "sparkwing repos prune"},
	},
}

var cmdReposList = Command{
	Path:     "sparkwing repos list",
	Synopsis: "List the machine's fleet of sparkwing repos",
	Description: `Lists registered repositories and repositories with recorded pipeline runs.
Each row shows the SDK version, last run, and intervening migration guides.
Linked worktrees appear under their primary checkout, with differing SDK
versions reported separately.

--checkouts lists the registry itself instead: each registered checkout, its
status, and the pipelines it provides (--pipelines=false skips the per-repo
describe call).`,
	Flags: []FlagSpec{
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty | json | plain", Default: "pretty on TTY, json when piped", Group: "Output"},
		{Name: "checkouts", Desc: "List registered checkouts and their pipelines instead of SDK pins", Group: "Output"},
		{Name: "pipelines", Desc: "With --checkouts, include pipeline names", Default: "true", RequiresFlags: []string{"checkouts"}, Group: "Output"},
	},
	GroupOrder: []string{"Output", "Other"},
	Examples: []Example{
		{"List the fleet", "sparkwing repos list"},
		{"Agent-readable record", "sparkwing repos list -o json"},
		{"Registered checkouts and their pipelines", "sparkwing repos list --checkouts"},
		{"Skip pipeline discovery", "sparkwing repos list --checkouts --pipelines=false"},
	},
}

var cmdReposAdd = Command{
	Path:        "sparkwing repos add",
	Synopsis:    "Register a checkout",
	Description: "Registers a checkout explicitly. The path defaults to the current directory.",
	PosArgs: []PosArg{
		{Name: "[path]", Desc: "Checkout path; defaults to the current directory"},
	},
	Examples: []Example{
		{"Register the current checkout", "sparkwing repos add"},
		{"Register another checkout", "sparkwing repos add ../service"},
	},
}

var cmdReposRemove = Command{
	Path:        "sparkwing repos remove",
	Synopsis:    "Remove a registered checkout",
	Description: "Removes every registry entry matching a path or basename.",
	PosArgs: []PosArg{
		{Name: "<path-or-basename>", Desc: "Registered path or basename to remove", Required: true},
	},
	Examples: []Example{
		{"Remove a checkout by basename", "sparkwing repos remove service"},
	},
}

var cmdReposPrune = Command{
	Path:        "sparkwing repos prune",
	Synopsis:    "Remove checkouts whose pipeline directory is gone",
	Description: "Removes registered checkouts that no longer contain a .sparkwing directory.",
	Examples: []Example{
		{"Remove stale registry entries", "sparkwing repos prune"},
	},
}

var cmdReposInfo = Command{
	Path:     "sparkwing repos info",
	Synopsis: "Inspect repository versions, worktrees, store compatibility, and pipelines",
	Description: `Reports a repository's SDK version, intervening migration guides, linked
worktrees, source revision, uncommitted changes, store compatibility, and
pipeline outcomes. It suggests a next action when a check finds a problem.

Defaults to the enclosing repository. --repo selects another repository by
name or checkout path. The command reads existing state.`,
	Flags: []FlagSpec{
		{Name: "repo", Argument: "NAME_OR_PATH", Desc: "Repo by name or checkout path. Default: the repo containing the current directory.", Group: "Filter"},
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty | json | plain", Default: "pretty on TTY, json when piped", Group: "Output"},
	},
	GroupOrder: []string{"Filter", "Output", "Other"},
	Examples: []Example{
		{"Deep dive on the current repo", "sparkwing repos info"},
		{"Deep dive on a named repo", "sparkwing repos info --repo fictional-app"},
		{"Agent-readable record", "sparkwing repos info --repo fictional-app -o json"},
	},
}

var cmdReposUpdate = Command{
	Path:     "sparkwing repos update",
	Synopsis: "Update repository SDK versions and compare pipeline plans",
	Description: `Previews SDK updates across tracked repositories. For each repository with
no uncommitted changes, compares pipeline plans before and after the update
and reports one result:
  clean         compiled and compared plans are byte-identical
  plan-differs  compiled, with a difference in a compared plan
  broken        update, compilation, or verification failed

Plan equality covers the compared structure; execution behavior still needs
verification. Plans that already failed before the update are reported as
not compared. Repositories with uncommitted changes or missing directories
are skipped and named.

--apply writes and commits updates per repository. --verify also runs each
repository's pre-commit gate. --repo selects one repository.

--in-place updates the checkout you stand in (or the one -C names) instead:
native go get for the resolved release, then go mod tidy, with no plan
comparison and no commit. Go keeps its toolchain selection, module
verification and dependency rules. --in-place --check reads the pin and the
release metadata without changing anything: exit 0 means current or ahead, 1
means an update is available, and 2 means unknown, diverged or a check
failure; a local SDK replacement reports unknown. Output is one update_check
record for a check and one update receipt for an update.

Progress goes to stderr. The first interrupt allows the active repository to
restore module files and prints completed results. A second interrupt exits
immediately. The report also identifies divergent SDK pins that may conflict
with the shared store schema.`,
	Flags: []FlagSpec{
		{Name: "version", Argument: "TAG", Desc: "Target SDK release (vX.Y.Z). Default: latest.", Group: "Input"},
		{Name: "apply", Desc: "Write the bumps and commit per repo (default is a dry run)", Group: "Behavior"},
		{Name: "verify", Desc: "Run each repo's pre-commit gate after the bump", Group: "Behavior"},
		{Name: "repo", Argument: "NAME_OR_PATH", Desc: "Scope to a single repo by name or checkout path", Group: "Filter"},
		{Name: "in-place", Desc: "Bump this checkout's pin with go get and go mod tidy; no plan comparison, no commit", ConflictsWith: []string{"apply", "verify", "repo"}, Group: "Behavior"},
		{Name: "check", Desc: "With --in-place, compare the pin with the release without changing it", RequiresFlags: []string{"in-place"}, Group: "Behavior"},
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty | json | plain", Default: "pretty on TTY, json when piped", Group: "Output"},
	},
	GroupOrder: []string{"Input", "Behavior", "Filter", "Output", "Other"},
	Examples: []Example{
		{"Preview a fleet-wide bump to latest (dry run)", "sparkwing repos update"},
		{"Preview a bump to a specific release", "sparkwing repos update --version v0.16.0"},
		{"Apply the bump and commit per repo", "sparkwing repos update --version v0.16.0 --apply"},
		{"Scope to one repo and run its gate", "sparkwing repos update --repo fictional-app --verify"},
		{"Bump this checkout's SDK pin", "sparkwing repos update --in-place"},
		{"Check this checkout's SDK pin", "sparkwing repos update --in-place --check"},
	},
}

var cmdCrons = Command{
	Path:     "sparkwing crons",
	Synopsis: "Arm, inspect and drive this host's local pipeline schedules",
	Description: `Runs the pipelines that declare an on.schedule cadence in
their .sparkwing/sparkwing.yaml, on this machine, from this home's runs
store.

Declaring a cadence does not arm it. ` + "`sparkwing crons install`" + ` arms a
repo's schedules on the host it is run from, and installs one OS timer -- a
systemd user timer on Linux, a launchd agent on macOS -- that calls ` + "`sparkwing crons tick`" + ` every minute. Sparkwing evaluates every cron
expression itself inside that tick, so the machine holds one timer however
many schedules are armed.

Each tick resolves every due instant exactly once: it launches the run, skips
it when the previous scheduled run is still going and the policy is skip, or
records it missed when it fell outside the catch-up window. A scheduled run
carries the trigger source "schedule" and executes through the same detached
path as ` + "`sparkwing run --sw-detached`" + `.

Arming pins by default: install compiles the pipeline and keeps that binary, so
a checkout updated afterwards does not change what runs unattended. Re-run
install to move the pin, ` + "`crons set --unpin`" + ` to follow the checkout
again, and ` + "`crons set`" + ` to override a declared cadence on this host
alone.

--profile NAME points every verb but tick, set --pin and set --unpin at a
controller instead of this host.
` + "`crons install --profile`" + ` pushes the repo's ` + "`where: controller`" + `
entries to it, pinned at HEAD unless --follow; the controller evaluates them
from a loop of its own, one evaluator per store, and each fire becomes a
trigger the cluster clones and runs.`,
	SubcommandOrder: []string{
		"install", "uninstall", "list", "show", "set", "run",
	},
	Examples: []Example{
		{"Arm this repo's schedules on this host", "sparkwing crons install"},
		{"See what is armed and when it next fires", "sparkwing crons list"},
		{"Check the timer and the last tick", "sparkwing crons list --timer"},
		{"Push this repo's controller schedules", "sparkwing crons install --profile prod"},
	},
}

var cmdCronsInstall = Command{
	Path:     "sparkwing crons install",
	Synopsis: "Arm a repo's declared schedules on this host and install the OS timer",
	Description: `Reads .sparkwing/sparkwing.yaml, records every on.schedule
entry that declares "where: local" against this home, and ensures the OS timer
that runs the tick. An entry declaring "where: controller" is reported and left
alone: this host does not fire it.

Each pipeline is compiled first and has to appear in the binary's own
description, because a schedule fires unattended: a pipeline that will not
build is refused here before unattended execution. That compile is also
the pin: the binary is copied under the sparkwing home and recorded with the
checkout's HEAD, so every fire runs what was armed however the checkout moves
afterwards. --follow arms without a pin, and each fire compiles the checkout.
--no-prove skips the compile, and so pins nothing.

--only arms a subset, naming pipelines or pipeline/name entries; a name the
repo does not declare is refused before anything is written.

A repo that declares no schedule is reported as nothing to arm and installs no
timer. Re-running install is the explicit update: it re-pins at the current
checkout, republishes what the repo declares, marks a pipeline that stopped
declaring a cadence undeclared, and re-bases this host's overrides onto the new
declaration. Pause state, cursor, fire history and the override values survive.

Arming is per host. Another machine runs the same schedule only when the
schedule is also armed on that machine.

--profile NAME pushes the repo's "where: controller" entries to that
controller instead, and reports the "where: local" ones as this host's. The
push needs a git origin, because the cluster clones the source at each fire; it
pins every fire to the checkout's HEAD unless --follow, which clones the branch
tip. A HEAD no remote branch carries is refused, because every fire would fail
at the clone; uncommitted edits are a warning, since the pushed commit is what
runs. Re-running the push is the explicit update, and it moves the pin.`,
	Flags: []FlagSpec{
		{Name: "profile", Argument: "NAME", Desc: "Profile name; omit for this host", Group: "Input"},
		{Name: "fleet", Desc: "Arm every registered repo instead of one", Group: "Input"},
		{Name: "only", Argument: "NAMES", Desc: "Arm only these pipelines or pipeline/name entries (comma-separated or repeatable)", Group: "Filter"},
		{Name: "follow", Desc: "Arm without pinning, so every fire compiles the checkout", Group: "Behavior"},
		{Name: "no-prove", Desc: "Arm without compiling the pipelines first, which pins nothing", Group: "Behavior"},
		{Name: "no-timer", Desc: "Arm without installing the OS timer, for a host that runs the tick from its own scheduler", Group: "Behavior", Hidden: true},
		{Name: "output", Short: "o", Argument: "FMT", Desc: "Output format: pretty|json|plain", Group: "Output"},
	},
	GroupOrder: []string{"Input", "Filter", "Behavior", "Output"},
	Examples: []Example{
		{"Arm the current repo", "sparkwing crons install"},
		{"Arm a different repo", "sparkwing -C /path/to/repo crons install"},
		{"Arm two entries only", "sparkwing crons install --only nightly,sweep/quick"},
		{"Arm without pinning", "sparkwing crons install --follow"},
		{"Arm every registered repo", "sparkwing crons install --fleet"},
		{"Push the controller entries to a cluster", "sparkwing crons install --profile prod"},
		{"Push them following the branch tip", "sparkwing crons install --profile prod --follow"},
	},
}

var cmdCronsUninstall = Command{
	Path:     "sparkwing crons uninstall",
	Synopsis: "Disarm a repo's schedules, and remove the timer when nothing is left",
	Description: `Deletes every schedule of one checkout, and its fire history,
from this home. When no schedule remains armed anywhere, the OS timer goes
too: the timer exists to serve armed schedules and nothing else.

--fleet disarms every schedule this home holds.

--name NAME removes one schedule instead: its fire history and its pinned
pipeline binary go with it, and every other schedule of the same pipeline and
repo stays armed. The timer is left as it is. To stop a schedule without
losing its history, ` + "`crons set NAME --pause`" + ` instead.

--profile NAME deletes the repo's schedules from that controller instead,
naming the repo by its git origin, or with --name the one schedule.`,
	Flags: []FlagSpec{
		{Name: "profile", Argument: "NAME", Desc: "Profile name; omit for this host", Group: "Input"},
		{Name: "fleet", Desc: "Disarm every schedule this home holds", Group: "Input"},
		{Name: "name", Argument: "NAME", Desc: "Disarm this one schedule (id, repo/pipeline[/name], pipeline/name, or a unique pipeline name)", Group: "Input"},
		{Name: "output", Short: "o", Argument: "FMT", Desc: "Output format: pretty|json|plain", Group: "Output"},
	},
	Examples: []Example{
		{"Disarm the current repo", "sparkwing crons uninstall"},
		{"Disarm everything on this host", "sparkwing crons uninstall --fleet"},
		{"Remove one named entry", "sparkwing crons uninstall --name sweep/quick"},
		{"Remove this repo from a controller", "sparkwing crons uninstall --profile prod"},
	},
}

var cmdCronsSet = Command{
	Path:     "sparkwing crons set",
	Synopsis: "Override a declared cadence on this host",
	Description: `Lays this host's own value over what the repo declares, for
the cron expression, the zone, the overlap policy, the catch-up window and the
launch's arguments. Everything left unnamed keeps the declared value, and a
field named again replaces the previous override.

--arg replaces the declared argument set whole, so name every argument the
schedule should launch with.

The override survives re-arming; --reset drops it, returning the schedule to
what the repo declares while the pin, the pause state, the cursor and the fire
history stay.

--pin compiles the pipeline, keeps that binary under the sparkwing home, and
records the checkout's HEAD, so every later fire runs that binary however the
checkout moves. The pin covers the pipeline the repo declares; scripts and
binaries it runs from the checkout or from PATH are outside it. --unpin drops
the pin and its binary, so every later fire compiles the checkout as it stands
at that minute and the tick's refresh reads the declaration again. A
controller schedule is pinned by the commit it was pushed at, so neither takes
--profile.

--pause stops the schedule firing and keeps it armed. A paused schedule still
advances its cursor on every tick, so --resume fires the next due instant
instead of replaying the ones that passed while it was paused.

--pin, --unpin, --pause, --resume and --reset each stand alone on a call.
` + "`sparkwing crons list`" + ` marks an overridden expression with *, and
` + "`sparkwing crons show`" + ` prints the declared, override and effective
value side by side.`,
	PosArgs: []PosArg{
		{Name: "NAME", Desc: "Schedule id, repo/pipeline[/name], pipeline/name, or a unique pipeline name", Required: true},
	},
	Flags: []FlagSpec{
		{Name: "profile", Argument: "NAME", Desc: "Profile name; omit for this host", Group: "Input"},
		{Name: "cron", Argument: "EXPR", Desc: "Cron expression to run instead of the declared one", Group: "Input"},
		{Name: "tz", Argument: "ZONE", Desc: "Zone the expression is read in, such as America/Denver or local", Group: "Input"},
		{Name: "overlap", Argument: "POLICY", Desc: "What a due instant does while the previous run is going: skip|queue", Group: "Input"},
		{Name: "catch-up", Argument: "DUR", Desc: "How late a due instant may still fire, such as 6h", Group: "Input"},
		{Name: "arg", Argument: "K=V", Desc: "Argument the launch passes (repeatable; replaces the declared set)", Group: "Input"},
		{Name: "reset", Desc: "Drop this host's override and run what the repo declares", Group: "Behavior"},
		{Name: "pin", Desc: "Pin the schedule to the checkout as it stands", Group: "Behavior"},
		{Name: "unpin", Desc: "Let the schedule follow the checkout again", Group: "Behavior"},
		{Name: "pause", Desc: "Stop the schedule firing, keeping it armed", Group: "Behavior"},
		{Name: "resume", Desc: "Let a paused schedule fire again", Group: "Behavior"},
		{Name: "output", Short: "o", Argument: "FMT", Desc: "Output format: pretty|json|plain", Group: "Output"},
	},
	GroupOrder: []string{"Input", "Behavior", "Output"},
	Examples: []Example{
		{"Run it later on this host", "sparkwing crons set nightly --cron '0 5 * * *'"},
		{"Read the expression locally", "sparkwing crons set nightly --tz local"},
		{"Launch with arguments", "sparkwing crons set sweep/quick --arg depth=shallow --arg dry-run=true"},
		{"Run what the repo declares", "sparkwing crons set nightly --reset"},
		{"Pin a schedule at HEAD", "sparkwing crons set nightly --pin"},
		{"Follow the checkout again", "sparkwing crons set nightly --unpin"},
		{"Pause a schedule", "sparkwing crons set fictional-nightly --pause"},
		{"Resume it", "sparkwing crons set fictional-nightly --resume"},
	},
}

var cmdCronsList = Command{
	Path:     "sparkwing crons list",
	Synopsis: "List the schedules armed on this host",
	Description: `One row per schedule: its id, its repo/pipeline name, the
cron expression and zone it is read in, when it next fires, when it last
fired, that fire's outcome, and whether it is armed, paused, or undeclared.

Schedules the repo no longer declares are hidden behind a count; --all shows
them. They keep their history and never fire.

--timer answers whether this host is actually evaluating what it armed: whether
the timer is installed and running, whether it runs this sparkwing or one that
has since moved, when the tick last landed and what it reported, and how many
schedules are armed, paused, and undeclared. It exits non-zero when schedules
are armed and the timer is not running, runs another binary, or has not ticked
in the last few minutes, so a check script can read the exit code. A host with
nothing armed is healthy. With --profile it reads the controller's scheduler:
its counts, when its loop last ticked, and what that tick reported.

--next N merges the next N instants of every armed schedule, each in its
configured zone; ` + "`crons show NAME --next N`" + ` reads one schedule.`,
	Flags: []FlagSpec{
		{Name: "profile", Argument: "NAME", Desc: "Profile name; omit for this host", Group: "Input"},
		{Name: "all", Desc: "Include schedules the repo no longer declares", Group: "Output"},
		{Name: "timer", Desc: "Report the OS timer, the last tick and the counts instead of the rows", Group: "Output"},
		{Name: "next", Argument: "N", Desc: "Show the next N instants across every armed schedule instead of the rows", Group: "Output"},
		{Name: "output", Short: "o", Argument: "FMT", Desc: "Output format: pretty|json|plain", Group: "Output"},
		{Name: "sw-now", Argument: "RFC3339", Desc: "Read the relative times as of this instant", Group: "Behavior", Hidden: true},
	},
	Examples: []Example{
		{"What is armed here", "sparkwing crons list"},
		{"Include withdrawn schedules", "sparkwing crons list --all"},
		{"Check the timer and the last tick", "sparkwing crons list --timer"},
		{"What fires next on this host", "sparkwing crons list --next 5"},
		{"What a controller evaluates", "sparkwing crons list --profile prod"},
		{"Machine-readable (NDJSON)", "sparkwing crons list -o json"},
	},
}

var cmdCronsShow = Command{
	Path:     "sparkwing crons show",
	Synopsis: "Show one schedule's full record and its recent fires",
	Description: `Prints every stored field with absolute times, then the
instants that have resolved, newest first: when each was due, when the tick
decided it, what it decided, the run it launched and that run's current
status, and the reason for any outcome that is not a launch.

NAME is a schedule id, a repo/pipeline name, or a bare pipeline name that is
unique across this host's schedules.

--next N prints the next N instants the schedule fires, in its configured
zone, instead of the record.`,
	PosArgs: []PosArg{
		{Name: "NAME", Desc: "Schedule id, repo/pipeline[/name], pipeline/name, or a unique pipeline name", Required: true},
	},
	Flags: []FlagSpec{
		{Name: "profile", Argument: "NAME", Desc: "Profile name; omit for this host", Group: "Input"},
		{Name: "fires", Argument: "N", Desc: "How many recent fires to show", Default: "10", Group: "Output"},
		{Name: "next", Argument: "N", Desc: "Show the next N instants this schedule fires instead of its record", Group: "Output"},
		{Name: "output", Short: "o", Argument: "FMT", Desc: "Output format: pretty|json|plain", Group: "Output"},
		{Name: "sw-now", Argument: "RFC3339", Desc: "Walk --next forward from this instant instead of now", Group: "Behavior", Hidden: true},
	},
	Examples: []Example{
		{"Inspect one schedule", "sparkwing crons show fictional-nightly"},
		{"Read further back", "sparkwing crons show fictional-nightly --fires 50"},
		{"Check one expression", "sparkwing crons show fictional-nightly --next 10"},
	},
}

var cmdCronsRun = Command{
	Path:     "sparkwing crons run",
	Synopsis: "Launch a schedule's pipeline now",
	Description: `Runs the pipeline immediately, whatever the cadence says and
whether or not the schedule is paused, and records the launch in the
schedule's history as a manual fire.

The cursor does not move: a manual run is not one of the cadence's due
instants, so the next one still fires on time.`,
	PosArgs: []PosArg{
		{Name: "NAME", Desc: "Schedule id, repo/pipeline[/name], pipeline/name, or a unique pipeline name", Required: true},
	},
	Flags: []FlagSpec{
		{Name: "profile", Argument: "NAME", Desc: "Profile name; omit for this host", Group: "Input"},
		{Name: "output", Short: "o", Argument: "FMT", Desc: "Output format: pretty|json|plain", Group: "Output"},
	},
	Examples: []Example{
		{"Run a schedule's pipeline now", "sparkwing crons run fictional-nightly"},
	},
}

var cmdCronsTick = Command{
	Path:     "sparkwing crons tick",
	Synopsis: "Evaluate every armed schedule once (the OS timer's entry point)",
	Hidden:   true,
	Description: `What the systemd timer or launchd agent runs every minute.
It takes an exclusive lock so two ticks never resolve the same instant,
re-reads the declaration of every schedule that follows its checkout -- a
pinned schedule keeps the declaration it was armed with -- evaluates every
declared unpaused schedule against its cursor, launches what is due, and
records each outcome.

Quiet on success: one summary line and the id of each run it launched. It
exits non-zero only when the tick itself could not run, so a schedule that
fails to launch is recorded against that schedule and the timer stays green.

--dry-run prints what this minute would resolve and writes nothing.

Run it by hand on a host whose platform has no sparkwing timer, from that
machine's own scheduler, once a minute.`,
	Flags: []FlagSpec{
		{Name: "dry-run", Desc: "Evaluate and report without launching or recording anything", Group: "Behavior"},
		{Name: "output", Short: "o", Argument: "FMT", Desc: "Output format: pretty|json|plain", Group: "Output"},
		{Name: "sw-now", Argument: "RFC3339", Desc: "Evaluate as if it were this instant", Group: "Behavior", Hidden: true},
	},
	Examples: []Example{
		{"Evaluate every armed schedule once", "sparkwing crons tick"},
		{"See what this minute would do", "sparkwing crons tick --dry-run"},
	},
}

// safety: an older daemon's takeover, a detached child's argv and installed
// completion scripts invoke these names, so renaming one strands its caller.

var cmdWingd = Command{
	Path:     "sparkwing wingd",
	Synopsis: "Host the local admission daemon (spawned by the CLI)",
	Hidden:   true,
}

var cmdWingdRun = Command{
	Path:     "sparkwing wingd run",
	Synopsis: "Serve the admission daemon in this process",
	Hidden:   true,
}

var cmdWingdSupervise = Command{
	Path:     "sparkwing wingd supervise",
	Synopsis: "Supervise a wingd run child and restart it when it wedges",
	Hidden:   true,
}

var cmdDashboardSupervise = Command{
	Path:     "sparkwing __dashboard-supervise",
	Synopsis: "Supervise a detached dashboard started by serve start",
	Hidden:   true,
}

var cmdRunsConsume = Command{
	Path:     "sparkwing __runs-consume",
	Synopsis: "Serve the detached-run consumer for one home",
	Hidden:   true,
}

var cmdHandleTrigger = Command{
	Path:     "sparkwing handle-trigger",
	Synopsis: "Run one claimed trigger (spawned by cluster worker)",
	Hidden:   true,
}

var cmdComplete = Command{
	Path:     "sparkwing __complete",
	Synopsis: "Print completion candidates for the shell scripts",
	Hidden:   true,
	PosArgs: []PosArg{
		{Name: "KIND", Desc: "profiles | pipelines | flags | verbs | hint | pipeline-flags", Required: true},
	},
}
