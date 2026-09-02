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
for agent-facing discovery.`,
	SubcommandOrder: []string{"info", "pipeline", "run", "runs", "repos", "queue", "cache", "daemon", "profile", "version", "update", "dashboard", "doctor", "cluster", "secrets", "configure", "debug", "docs", "examples", "commands", "completion"},
	Examples: []Example{
		{"Run a pipeline (positional shortcut)", "sparkwing run build-test-deploy"},
		{"First command an agent should run", "sparkwing info --for-agent"},
		{"List every invocable (agents)", "sparkwing pipeline list -o json"},
		{"Inspect one pipeline's full metadata", "sparkwing pipeline describe --name release -o json"},
		{"Bootstrap + scaffold your first pipeline in a fresh repo", "sparkwing pipeline new --name release"},
		{"Start the local dashboard", "sparkwing dashboard start"},
	},
}

var cmdDaemon = Command{
	Path:            "sparkwing daemon",
	Synopsis:        "Inspect or refresh the local admission daemon",
	Description:     `The admission daemon starts on demand when a pipeline needs it. Status never starts one. Restart replaces only an answering daemon with this installed build, using the same drain, durable lease, and reattachment path as automatic version takeover; a stopped daemon stays stopped.`,
	SubcommandOrder: []string{"status", "restart", "recover-state"},
	Examples: []Example{
		{"Machine-readable status", "sparkwing daemon status -o json"},
		{"Refresh only if already running", "sparkwing daemon restart"},
	},
}

var cmdDaemonRecoverState = Command{
	Path:        "sparkwing daemon recover-state",
	Synopsis:    "Preserve unreadable daemon state after guarded commands stop",
	Description: `Fail-closed recovery for a daemon that cannot parse its durable state. The unreadable bytes may describe guarded commands that are still running, so first stop or verify those commands, then pass --yes. Recovery holds the daemon election lock, moves state.json to a state.json.corrupt-<time> forensic copy, and never discards readable state.`,
	Flags: []FlagSpec{
		{Name: "home", Argument: "DIR", Desc: "Sparkwing home whose unreadable daemon state should be preserved", Group: "Input"},
		{Name: "yes", Desc: "Confirm every guarded command described by the unreadable state has stopped", Required: true, Group: "Safety"},
	},
	GroupOrder: []string{"Input", "Safety", "Other"},
	Examples: []Example{
		{"Recover only after verifying guarded commands stopped", "sparkwing daemon recover-state --home /path/to/home --yes"},
	},
}

var cmdDaemonStatus = Command{
	Path:        "sparkwing daemon status",
	Synopsis:    "Report whether wingd is running and which build it serves",
	Description: `Read-only daemon status. An absent daemon is a healthy stopped state and exits zero. An unreachable socket fails instead of pretending the admission queue is empty. The JSON running_revision identifies the exact source build when available.`,
	Flags: []FlagSpec{
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty|json|plain (default: pretty on TTY, json when piped)", Group: "Output"},
		{Name: "home", Argument: "DIR", Desc: "Sparkwing state directory", Group: "Input"},
	},
	GroupOrder: []string{"Input", "Output", "Other"},
	Examples: []Example{
		{"Machine-readable status", "sparkwing daemon status -o json"},
	},
}

var cmdDaemonRestart = Command{
	Path:        "sparkwing daemon restart",
	Synopsis:    "Refresh an answering wingd to this installed build",
	Description: `Refresh an answering daemon when its build differs from this installed build. With --force, drain and replace an answering daemon even when the builds match. Existing holders reconnect and reattach through durable leases. If no daemon is running, nothing is started.`,
	Flags: []FlagSpec{
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty|json|plain (default: pretty on TTY, json when piped)", Group: "Output"},
		{Name: "home", Argument: "DIR", Desc: "Sparkwing state directory", Group: "Input"},
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
	Synopsis: "Self-describe sparkwing + the current project (agent entrypoint)",
	Description: `One command that answers "what is sparkwing, am I in a
project, what should I run next" without prior knowledge. Prints
the CLI version, whether the current directory is inside a
sparkwing project (and how many pipelines it has), whether the Go
toolchain is on PATH, a curated list of next-step commands, and
the docs URL. When a project declares a git hook that is not
firing, repairing it is the first next step.

This is the canonical first command an agent runs after install.
Use -o json for structured output that an agent can parse, or
-o plain to emit one next-step command per line for shell
pipelines (head -n1 yields the most-likely next command).`,
	Flags: []FlagSpec{
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty | json | plain", Default: "pretty", Group: "Output"},
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
	Description: `Cluster-scoped operations and state. 'status' rolls up
controller health + fleet + queue state into one report;
individual verbs drill in (agents for fleet detail, users /
tokens for controller-stored config, image rollout
for deploys, webhooks for GitHub delivery debug).

Secrets used to live here; they're now top-level
('sparkwing secrets ...') since they straddle laptop dotenv
+ controller storage and are referenced constantly.

'worker' runs a laptop-side queue drainer against a remote
cluster. 'gc' sweeps stale warm-runner PVCs.

For the laptop-local dashboard server, see
'sparkwing dashboard start'.

Profiles (via --profile) pick which cluster these commands
address; set them up with 'sparkwing configure profiles'.`,
	SubcommandOrder: []string{"status", "agents", "worker", "gc", "users", "tokens", "image", "webhooks", "concurrency"},
	Examples: []Example{
		{"Cluster health summary", "sparkwing cluster status --profile prod"},
		{"List fleet agents", "sparkwing cluster agents --profile prod"},
	},
}

var cmdConfigure = Command{
	Path:     "sparkwing configure",
	Synopsis: "Configure laptop-local settings",
	Description: `Laptop-local setup commands. 'init' is the one-shot
"prepare ~/.config/sparkwing/ + report what's there" command;
'profiles' manages remote-cluster connection profiles. Future
laptop-level surfaces (aliases, default flags, per-repo config)
land here.

Controller-side state (users, tokens) lives under
'sparkwing cluster ...' since it writes to the remote
controller, not the local config. Secrets are top-level
('sparkwing secrets ...').`,
	SubcommandOrder: []string{"init", "profiles", "xrepo"},
	Examples: []Example{
		{"First-time laptop setup", "sparkwing configure init"},
		{"Status of laptop config", "sparkwing configure init -o json"},
		{"List profiles", "sparkwing configure profiles list"},
		{"Add a new profile", "sparkwing configure profiles add --name prod --controller https://api.sparkwing.example --token $TOKEN"},
		{"Register the current repo with the cross-repo registry", "sparkwing configure xrepo add"},
	},
}

var cmdConfigureXrepo = Command{
	Path:     "sparkwing configure xrepo",
	Synopsis: "Manage the laptop-local repo registry",
	Description: `The registry maps pipeline names to local checkouts so
cross-repo RunAndAwait calls resolve without hardcoded WithFreshRepo
annotations. Auto-populated when you run 'sparkwing run <pipeline>'
in a .sparkwing/-bearing repo (set SPARKWING_NO_AUTO_REGISTER=1 to
disable).`,
	SubcommandOrder: []string{"list", "add", "remove", "prune"},
	Examples: []Example{
		{"Register the current checkout", "sparkwing configure xrepo add"},
		{"Show the fleet the registry reaches", "sparkwing configure xrepo list"},
		{"Drop entries whose checkout is gone", "sparkwing configure xrepo prune"},
	},
}

var cmdConfigureXrepoList = Command{
	Path:        "sparkwing configure xrepo list",
	Synopsis:    "List registered checkouts and their pipelines",
	Description: "Shows each registered checkout, its status, and the pipelines it provides.",
	Flags: []FlagSpec{
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: json | table", Group: "Output"},
		{Name: "pipelines", Desc: "Include pipeline names", Default: "true", Group: "Output"},
	},
	GroupOrder: []string{"Output", "Other"},
	Examples: []Example{
		{"List registered checkouts", "sparkwing configure xrepo list"},
		{"Emit one JSON record per checkout", "sparkwing configure xrepo list -o json"},
		{"Skip pipeline discovery", "sparkwing configure xrepo list --pipelines=false"},
	},
}

var cmdConfigureXrepoAdd = Command{
	Path:        "sparkwing configure xrepo add",
	Synopsis:    "Register a checkout",
	Description: "Registers a checkout explicitly. The path defaults to the current directory.",
	PosArgs: []PosArg{
		{Name: "[path]", Desc: "Checkout path; defaults to the current directory"},
	},
	Examples: []Example{
		{"Register the current checkout", "sparkwing configure xrepo add"},
		{"Register another checkout", "sparkwing configure xrepo add ../service"},
	},
}

var cmdConfigureXrepoRemove = Command{
	Path:        "sparkwing configure xrepo remove",
	Synopsis:    "Remove a registered checkout",
	Description: "Removes every registry entry matching a path or basename.",
	PosArgs: []PosArg{
		{Name: "<path-or-basename>", Desc: "Registered path or basename to remove", Required: true},
	},
	Examples: []Example{
		{"Remove a checkout by basename", "sparkwing configure xrepo remove service"},
	},
}

var cmdConfigureXrepoPrune = Command{
	Path:        "sparkwing configure xrepo prune",
	Synopsis:    "Remove checkouts whose pipeline directory is gone",
	Description: "Removes registered checkouts that no longer contain a .sparkwing directory.",
	Examples: []Example{
		{"Remove stale registry entries", "sparkwing configure xrepo prune"},
	},
}

var cmdConfigureInit = Command{
	Path:     "sparkwing configure init",
	Synopsis: "Set up ~/.config/sparkwing/ and report laptop-level config status",
	Description: `Idempotent setup + status command for laptop-level
sparkwing config. Creates ~/.config/sparkwing/ if it doesn't exist,
then reports which config files are present (profiles.yaml,
repos.yaml, secrets.env), the running CLI + Go toolchain version,
and a curated list of next-step commands.

Pairs with the per-project flow: use this one on a fresh laptop
after install, then run 'sparkwing pipeline new --name <name>'
inside each project to scaffold .sparkwing/ + your first pipeline
in one step (no separate init needed).

Re-running on an already-set-up laptop is a no-op status report.
--dry-run skips the mkdir so the command pure-probes.`,
	Flags: []FlagSpec{
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty | json | plain", Default: "pretty", Group: "Output"},
		{Name: "dry-run", Desc: "Probe + report without creating ~/.config/sparkwing/", Group: "Behavior"},
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
	Synopsis: "Show + update versions (CLI, SDK, sparks)",
	Description: `Reports the installed CLI version + build provenance, the
latest published release on GitHub (with a short network
fetch -- bounded by ~3s, fail-soft when offline), and the
.sparkwing/go.mod SDK pin + any sparks-* libraries declared
alongside it.

Behind-by-version is computed via semver compare for both the
CLI itself and the SDK pin so an agent reading -o json can
trigger an upgrade without parsing prose.

--offline skips the network fetch entirely; -o json emits the
structured report; -o plain prints semver lines (CLI then
latest) for shell pipelines.`,
	SubcommandOrder:    []string{"update", "hold"},
	SubcommandOptional: true,
	Flags: []FlagSpec{
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty | json | plain", Default: "pretty", Group: "Output"},
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
		{"Update the CLI binary", "sparkwing version update --cli"},
		{"Bump the SDK pin in this project", "sparkwing version update --sdk"},
	},
}

var cmdVersionHold = Command{
	Path:     "sparkwing version hold",
	Synopsis: "Show, set, or clear the operator ceiling on CLI upgrades",
	Description: `A version hold is an operator-set ceiling that the tool enforces:
once set, 'sparkwing version update --cli' (and 'sparkwing update')
refuse to install anything beyond it, so an agent cannot perform a
major upgrade against operator instruction.

The ceiling shape controls its reach:

  vMAJOR.MINOR       caps a whole minor series -- every patch of that
                     minor is allowed, the next minor is refused
                     (e.g. v0.15 allows v0.15.9 but refuses v0.16.0).
  vMAJOR.MINOR.PATCH exact ceiling -- nothing above that patch installs.

With no flags, prints the current hold and where it is set. The hold
persists in the user config (XDG_CONFIG_HOME or ~/.config/sparkwing/
version-hold); the SPARKWING_VERSION_HOLD environment variable
overrides the file for a shell or a whole fleet. Releases beyond the
hold still show in 'sparkwing version' so the operator sees what is
being deferred.`,
	Flags: []FlagSpec{
		{Name: "set", Argument: "VERSION", Desc: "Set the ceiling (e.g. v0.15 or v0.15.4)", Group: "Action"},
		{Name: "clear", Desc: "Remove the hold so upgrades are unrestricted", Group: "Action"},
	},
	GroupOrder: []string{"Action", "Other"},
	Examples: []Example{
		{"Show the current hold", "sparkwing version hold"},
		{"Hold the minor series at v0.15", "sparkwing version hold --set v0.15"},
		{"Pin an exact ceiling", "sparkwing version hold --set v0.15.4"},
		{"Lift the hold", "sparkwing version hold --clear"},
	},
}

var cmdUpdate = Command{
	Path:     "sparkwing update",
	Synopsis: "Self-update the CLI binary",
	Description: `Downloads, authenticates, and atomically installs the latest
(or a specific) sparkwing release from GitHub Releases.

By default the command fetches the latest version pointer, pulls
the matching binary for the current OS/arch, verifies Ed25519
signatures over the manifest and asset plus the manifest digest,
and replaces the running binary atomically. Verification failure
is terminal; the updater never selects an unsigned fallback.

--check is the read-only probe: it reports the installed version
and the latest published release, exits 0 when already current,
and exits 1 when a newer release exists (useful for CI/notifications).

Downgrades are blocked by default. Pass --force to install an older
release (e.g. bisecting a regression).

For SDK (go.mod) bumps, use 'sparkwing version update --sdk'.`,
	Flags: []FlagSpec{
		{Name: "check", Desc: "Report installed vs latest; exit 1 if a newer release exists (read-only)", Group: "Behavior"},
		{Name: "force", Desc: "Allow downgrading to an older release", Group: "Behavior"},
		{Name: "override-hold", Desc: "Cross an operator version hold", Group: "Behavior"},
		{Name: "version", Argument: "TAG", Desc: "Target release tag (e.g. v0.17.0). Default: latest.", Group: "Input"},
	},
	GroupOrder: []string{"Behavior", "Input", "Other"},
	Examples: []Example{
		{"Check for a newer release (read-only)", "sparkwing update --check"},
		{"Update to latest", "sparkwing update"},
		{"Pin to a specific release", "sparkwing update --version v0.44.0"},
		{"Downgrade to an older release", "sparkwing update --version v0.40.0 --force"},
	},
}

var cmdVersionUpdate = Command{
	Path:     "sparkwing version update",
	Synopsis: "Self-update the CLI binary (--cli) or bump this project's SDK pin (--sdk)",
	Description: `Two targets, one verb:

  --cli   Replace the running sparkwing binary with the target
          release. Resolves the version pointer from GitHub Releases,
          downloads the binary, verifies the ed25519 signature over
          SHA256SUMS and the signed digest, atomically installs, and
          re-hashes the installed file against the verified digest.
          A verification or install failure is terminal.

  --sdk   Bump the SDK pin in this project's .sparkwing/go.mod via
          'go get github.com/sparkwing-dev/sparkwing@<version>',
          then 'go mod tidy'. Doesn't touch the running binary.

Exactly one of --cli or --sdk must be set; they conflict with
each other so a typo can't update the wrong half. --version
applies to whichever target is selected.`,
	Flags: []FlagSpec{
		{Name: "cli", Desc: "Self-update the sparkwing CLI binary", Group: "Target", ConflictsWith: []string{"sdk"}},
		{Name: "sdk", Desc: "Bump the SDK pin in this project's .sparkwing/go.mod", Group: "Target", ConflictsWith: []string{"cli"}},
		{Name: "version", Argument: "TAG", Desc: "Target release tag (e.g. v0.17.0). Omit for latest.", Group: "Input"},
		{Name: "force", Desc: "Allow downgrading to an older release (--cli only)", Group: "Input"},
		{Name: "override-hold", Desc: "Cross an operator version hold (--cli only)", Group: "Input"},
	},
	GroupOrder: []string{"Target", "Input", "Other"},
	Examples: []Example{
		{"Update the CLI to latest", "sparkwing version update --cli"},
		{"Pin the CLI to a specific release", "sparkwing version update --cli --version v0.44.0"},
		{"Downgrade the CLI", "sparkwing version update --cli --version v0.40.0 --force"},
		{"Bump the SDK in this project to latest", "sparkwing version update --sdk"},
		{"Pin the SDK to a specific release", "sparkwing version update --sdk --version v0.44.0"},
	},
}

var cmdCommands = Command{
	Path:             "sparkwing commands",
	Synopsis:         "Index of every command: one path and synopsis per line",
	HideFromComplete: true,
	Description: `The whole CLI as one index -- 139 verbs, one line each, so
"what is this CLI" is answered by reading rather than by
walking every -h page.

Drill down two ways: '<any path> --help' for one verb's flags,
arguments, and examples, or --path PREFIX to narrow this list
to a subtree. The prefix may leave off the leading 'sparkwing'
(--path runs and --path "sparkwing runs" select the same
subtree). It matches whole path components, so --path run
selects 'run' and its subcommands and not the separate 'runs'
group, and a prefix that matches nothing is an error rather
than an empty listing.

-o json is this same index for a program to parse: path,
synopsis, and subcommand_count per verb, as NDJSON -- one
complete JSON object per line, so 'head -5' returns five whole
records instead of a truncated array. It carries no
description, flags, or examples; that is what '<path> --help'
prints, from the same Command values and always current.
Hidden commands are dispatchable but stay out of every
listing, because their help points at what to use instead;
--include-hidden lists them, flagged.

-o plain is one path per line for shell consumption; -o
markdown renders the full reference page, and with --split-dir
writes the docs/cli-*.md reference (one page per top-level
command group plus a cli-reference.md index).`,
	Flags: []FlagSpec{
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty | json | markdown | plain", Default: "pretty", Group: "Output"},
		{Name: "split-dir", Argument: "DIR", Desc: "With -o markdown: write one page per top-level command group into DIR (plus a cli-reference.md index), pruning stale generated pages", Group: "Output"},
		{Name: "path", Argument: "PREFIX", Desc: "Only emit commands at or under PREFIX, matched by whole path components, with or without the leading 'sparkwing' (runs, sparkwing runs, runs list); a prefix matching nothing is an error", Group: "Filter"},
		{Name: "include-hidden", Desc: "Also emit Hidden:true commands (default: skip)", Group: "Filter"},
	},
	GroupOrder: []string{"Output", "Filter", "Other"},
	Examples: []Example{
		{"Full CLI surface (agent self-discovery)", "sparkwing commands"},
		{"Just the pipelines subtree", "sparkwing commands --path pipeline"},
		{"The same subtree, fully qualified", "sparkwing commands --path \"sparkwing pipeline\""},
		{"All paths, one per line", "sparkwing commands -o plain"},
	},
}

var cmdQueue = Command{
	Path:     "sparkwing queue",
	Synopsis: "The truthful view of local admission: holders, connections, waiters, and why",
	Description: `Reads the local admission daemon and prints one honest picture of
where every run stands: each resource (host cores, memory, and every
named concurrency semaphore) with its capacity and how much is in use;
every run currently holding resources, with the repo it came from, how
long it has held, and what it is charged; connected run registrations that
hold no resources, labeled separately; and every waiter in admission
order, with its position, priority, cost, and exactly what it is waiting on.
A child run attached to its parent's lease renders indented under that
parent. The header carries a one-line summary of the daemon's recent
admission outcomes -- runs granted, median wait, evictions, queue
timeouts, younger backfills, and protected waiters -- so chronic patterns show
up at a glance.

A holder that is alive but has burned near-zero CPU while runs queue
behind it is flagged as stalled, together with the exact command to
clear it -- 'sparkwing runs cancel --run <id>'. The queue never kills a
run for you and never points at a host-wide destructive verb.

Pretty on a terminal, JSON when piped (add -o json to force it), and
one tab-separated record per line with -o plain for shell pipelines.

Every view says outright whether it reached the daemon, because an empty
queue and an unanswered one look identical otherwise. With no daemon
running there is nothing to arbitrate, so the command reports an empty
queue and exits 0. When the daemon's socket cannot be reached at all --
blocked by a sandbox, wedged, gone mid-read -- what is queued is unknown
rather than empty: the command says so, names the dial failure, and exits
4 instead of reporting a quiet machine it never looked at.

With --profile NAME the view switches to that profile's controller: the
same renderer prints the controller's admission state -- every
concurrency key, its holders and waiters, and each registered runner's
free capacity -- so one vocabulary reads local and cluster admission
alike.`,
	SubcommandOrder:    []string{"exec"},
	SubcommandOptional: true,
	Flags: []FlagSpec{
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty | json | plain", Group: "Output"},
		{Name: "home", Argument: "DIR", Desc: "Sparkwing home to inspect (default: $SPARKWING_HOME or ~/.sparkwing)", Group: "System"},
		{Name: "profile", Argument: "NAME", Desc: "Inspect this profile's controller instead of the local daemon", Group: "System"},
	},
	GroupOrder: []string{"Output", "System", "Other"},
	Examples: []Example{
		{"Show the current queue", "sparkwing queue"},
		{"Agent-readable snapshot", "sparkwing queue -o json"},
		{"One record per line for shell pipelines", "sparkwing queue -o plain"},
		{"Inspect a controller's admission state", "sparkwing queue --profile prod"},
	},
}

var cmdQueueExec = Command{
	Path:        "sparkwing queue exec",
	Synopsis:    "Run a command under local machine admission",
	Description: `Submits the command to the local admission daemon before starting it. While blocked, the command is visible in sparkwing queue. Once granted, its complete process tree runs under the lease; interruption or cancellation terminates and reaps that tree before the lease is released. Exact process-session ownership is available on Linux and macOS; queue exec refuses before admission on Windows and other Unix platforms.`,
	PosArgs: []PosArg{
		{Name: "command", Desc: "Command and arguments to execute after --", Required: true},
	},
	Flags: []FlagSpec{
		{Name: "run-id", Argument: "ID", Desc: "Unique admission participant identifier", Required: true, Group: "Identity"},
		{Name: "name", Argument: "NAME", Desc: "Short operation name shown in the queue", Group: "Identity"},
		{Name: "repo", Argument: "NAME", Desc: "Repository name shown in the queue", Group: "Identity"},
		{Name: "cores", Argument: "N", Desc: "CPU cores reserved while the command runs", Required: true, Group: "Resources"},
		{Name: "memory-bytes", Argument: "N", Desc: "Memory bytes reserved while the command runs", Group: "Resources"},
		{Name: "semaphore", Argument: "NAME", Desc: "Logical semaphore shared with equivalent commands", Group: "Resources"},
		{Name: "semaphore-capacity", Argument: "N", Desc: "Capacity declared for --semaphore", Default: "1", RequiresFlags: []string{"semaphore"}, Group: "Resources"},
		{Name: "ready-file", Argument: "PATH", Desc: "Write queued or granted readiness to a new JSON file", Group: "Output"},
		{Name: "home", Argument: "DIR", Desc: "Sparkwing state directory", Group: "System"},
	},
	GroupOrder:  []string{"Identity", "Resources", "Output", "System", "Other"},
	UsageSuffix: "-- <command> [args...]",
	Examples: []Example{
		{"Serialize a bootstrap command", "sparkwing queue exec --run-id build-123 --name bootstrap --cores 1 --semaphore bootstrap -- make prepare"},
	},
}

var cmdDocs = Command{
	Path:     "sparkwing docs",
	Synopsis: "Embedded user docs (offline)",
	Description: `The sparkwing docs are shipped inside the binary. ` +
		"`sparkwing docs read --topic getting-started`" + ` returns the
raw markdown to stdout; ` + "`sparkwing docs all`" + ` dumps every
doc in one shot for an agent that wants the full corpus in
context. The docs match the binary version exactly -- no risk of
the website explaining a flag your CLI doesn't have.

Discovery: ` + "`sparkwing docs list -o json`" + ` returns slug + title +
summary for every topic. ` + "`sparkwing docs search --query pull_request`" + `
returns the matching sections -- topic, heading, line range -- so a
narrow question does not cost a whole page.

When one page leaves you a lookup short, ` + "`sparkwing docs guides`" + `
lists task-sized sets of topics; ` + "`sparkwing docs read --guide authoring`" + `
returns the whole set in one call.`,
	SubcommandOrder: []string{"list", "read", "guides", "all", "search", "migrations", "versions", "cache"},
	Examples: []Example{
		{"List all topics (table)", "sparkwing docs list"},
		{"List all topics (agent-readable)", "sparkwing docs list -o json"},
		{"Read one topic", "sparkwing docs read --topic pipelines"},
		{"Read one topic at a specific version (online)", "sparkwing docs read --topic pipelines --version v0.3.0 --web"},
		{"Slurp the whole corpus into context", "sparkwing docs all"},
		{"Find docs that mention warm pool", "sparkwing docs search --query \"warm pool\""},
		{"List migration guides this CLI knows", "sparkwing docs migrations list"},
		{"Pipe every guide up to v0.4.0 into context", "sparkwing docs migrations between --to v0.4.0"},
		{"List every version available online", "sparkwing docs versions --web"},
	},
}

var cmdDocsList = Command{
	Path:     "sparkwing docs list",
	Synopsis: "Enumerate every doc topic",
	Description: `Walks the docs corpus and prints one row per topic with its
slug, first-H1 title, and first-paragraph summary. By default reads
the binary's embedded copy (hermetic, version-locked); pass --web
to fetch from sparkwing.dev for another version.`,
	Flags: []FlagSpec{
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty | json | plain", Default: "pretty", Group: "Output"},
		{Name: "web", Desc: "Fetch from sparkwing.dev instead of the embedded corpus", Group: "Source"},
		{Name: "version", Argument: "vX.Y.Z", Desc: "Doc version (e.g. v0.4.0, 'latest'). Defaults to this CLI's embedded version.", Group: "Source"},
		{Name: "no-cache", Desc: "With --web, bypass the on-disk cache for this invocation", Group: "Source"},
	},
	GroupOrder: []string{"Source", "Output", "Other"},
	Examples: []Example{
		{"Human-readable table", "sparkwing docs list"},
		{"Agent-readable", "sparkwing docs list -o json"},
		{"Slug-per-line for shell loops", "sparkwing docs list -o plain"},
		{"List the v0.3.0 corpus from sparkwing.dev", "sparkwing docs list --web --version v0.3.0"},
	},
}

var cmdDocsRead = Command{
	Path:     "sparkwing docs read",
	Synopsis: "Print one doc's raw markdown to stdout",
	Description: `Prints the raw markdown body for the named topic. The slug is
the filename under /docs/ minus .md (run ` + "`sparkwing docs list`" + ` to
see them all). Subdirs use slash-separated paths (e.g.
design/remote-retry).

Default source is the binary's embedded corpus. Use --web to fetch
from sparkwing.dev, optionally pinned to --version vX.Y.Z or
--version latest.`,
	Flags: []FlagSpec{
		{Name: "topic", Argument: "NAME", Desc: "Doc slug (e.g. getting-started, pipelines, mcp)", Group: "Selection"},
		{Name: "guide", Argument: "NAME", Desc: "Read a task-sized set of topics instead of one (`sparkwing docs guides`)", Group: "Selection"},
		{Name: "web", Desc: "Fetch from sparkwing.dev instead of the embedded corpus", Group: "Source"},
		{Name: "version", Argument: "vX.Y.Z", Desc: "Doc version (e.g. v0.4.0, 'latest'). Defaults to this CLI's embedded version.", Group: "Source"},
		{Name: "no-cache", Desc: "With --web, bypass the on-disk cache for this invocation", Group: "Source"},
	},
	GroupOrder: []string{"Selection", "Source", "Other"},
	Examples: []Example{
		{"Read the getting-started page", "sparkwing docs read --topic getting-started"},
		{"Everything needed to write a pipeline, one call", "sparkwing docs read --guide authoring"},
		{"Pipe through a pager", "sparkwing docs read --topic pipelines | less"},
		{"Read v0.3.0's pipelines page online", "sparkwing docs read --topic pipelines --version v0.3.0 --web"},
		{"Always fetch the freshest version", "sparkwing docs read --topic pipelines --version latest --web"},
	},
}

var cmdDocsAll = Command{
	Path:     "sparkwing docs all",
	Synopsis: "Concatenate every doc to stdout (full corpus dump)",
	Description: `Prints every embedded doc to stdout, separated by short ASCII
headers. The "give me everything" path for an agent that wants
the full corpus in context with one Bash invocation.`,
	Examples: []Example{
		{"Slurp every doc into context", "sparkwing docs all"},
	},
}

var cmdDocsGuides = Command{
	Path:     "sparkwing docs guides",
	Synopsis: "List the task-sized doc sets (--guide on `docs read`)",
	Description: `A guide is a named set of topics that answer one task
together, for the case where reading a single page leaves you one
lookup short. ` + "`sparkwing docs read --guide authoring`" + ` returns the
whole set in one call.

Guides carry narrative topics only. The generated references
(sdk-reference, cli-reference) are lookup tables rather than pages to
read end to end; reach those with ` + "`sparkwing docs search`" + `.`,
	Flags: []FlagSpec{
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty | json | plain", Default: "pretty", Group: "Output"},
	},
	Examples: []Example{
		{"What sets exist", "sparkwing docs guides"},
		{"Read the authoring set", "sparkwing docs read --guide authoring"},
		{"Agent-readable", "sparkwing docs guides -o json"},
	},
}

var cmdDocsSearch = Command{
	Path:     "sparkwing docs search",
	Synopsis: "Find the section that answers a question",
	Description: `Returns the doc sections containing every space-separated
token in --query (case-insensitive), best first: a heading hit outranks
a body hit, and a shorter section outranks a longer one holding the same
match. Each result names its topic, heading, and line range.

Sections rather than whole topics because the reference pages run to
tens of thousands of tokens, and the question is usually narrow -- what
a ` + "`pull_request`" + ` trigger looks like, what fields ` + "`ApprovalConfig`" + ` has.
Add --body to print the matching sections in full.

--topics restores the old behavior, listing whole matching topics in the
same shape as ` + "`sparkwing docs list`" + `.`,
	Flags: []FlagSpec{
		{Name: "query", Short: "q", Argument: "TEXT", Desc: "Search terms (every token must match)", Required: true, Group: "Selection"},
		{Name: "body", Desc: "Print each matching section in full instead of a snippet", Group: "Selection"},
		{Name: "topics", Desc: "List whole matching topics instead of sections", Group: "Selection"},
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty | json | plain", Default: "pretty", Group: "Output"},
	},
	GroupOrder: []string{"Selection", "Output", "Other"},
	Examples: []Example{
		{"Where a PR trigger is defined", "sparkwing docs search --query pull_request"},
		{"Read the matching sections in full", "sparkwing docs search -q ApprovalConfig --body"},
		{"JSON for agents (topic, heading, line range, body)", "sparkwing docs search -q approval -o json"},
		{"Whole topics, as before", "sparkwing docs search -q \"warm pool\" --topics"},
	},
}

var cmdDocsMigrations = Command{
	Path:     "sparkwing docs migrations",
	Synopsis: "Per-version migration guides (agent-friendly)",
	Description: `Surface the migration guides shipped under docs/migrations/.
Each released sparkwing version that introduces breaking changes
gets a guide; ` + "`sparkwing docs migrations between`" + ` concatenates
every guide in a version range into one blob you can pipe straight
into an agent context.

The same files are also reachable as regular docs (e.g.
` + "`sparkwing docs read --topic migrations/v0.4.0`" + `); this
subcommand is the ergonomics layer with semver-aware filtering and
range output.`,
	SubcommandOrder: []string{"list", "read", "between"},
	Examples: []Example{
		{"List embedded migration guides", "sparkwing docs migrations list"},
		{"Read one guide", "sparkwing docs migrations read --version v0.4.0"},
		{"Every guide upgrading from v0.3.0 to v0.4.0", "sparkwing docs migrations between --from v0.3.0 --to v0.4.0"},
		{"Every guide this CLI knows (one-shot agent context)", "sparkwing docs migrations between"},
	},
}

var cmdDocsMigrationsList = Command{
	Path:     "sparkwing docs migrations list",
	Synopsis: "Table of every embedded migration guide",
	Description: `Lists each migration guide bundled with this binary in
descending semver order, with date and one-line summary parsed
from docs/migrations/README.md. Use --output json for an
agent-readable array of {version, date, summary, slug, bytes}.

When the CLI's own version is older than the newest embedded
guide a one-line stderr note suggests rebuilding.`,
	Flags: []FlagSpec{
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty | json | plain", Default: "pretty", Group: "Output"},
		{Name: "web", Desc: "Fetch the index from sparkwing.dev/migrations/index.json instead of the embed", Group: "Source"},
		{Name: "no-cache", Desc: "With --web, bypass the on-disk cache for this invocation", Group: "Source"},
	},
	GroupOrder: []string{"Source", "Output", "Other"},
	Examples: []Example{
		{"Human-readable table", "sparkwing docs migrations list"},
		{"Agent-readable", "sparkwing docs migrations list -o json"},
		{"Version-per-line for shell loops", "sparkwing docs migrations list -o plain"},
		{"Online (every release on sparkwing.dev)", "sparkwing docs migrations list --web"},
	},
}

var cmdDocsMigrationsRead = Command{
	Path:     "sparkwing docs migrations read",
	Synopsis: "Print one migration guide's markdown to stdout",
	Description: `Outputs the markdown body for a single migration guide. Default
output is the raw markdown so an agent can pipe straight into
its context. Cross-doc markdown links to other topics are
rewritten into ` + "`sparkwing docs read --topic <slug>`" + ` form
(same transform as ` + "`sparkwing docs read`" + `).`,
	PosArgs: []PosArg{
		{Name: "[vX.Y.Z]", Desc: "Migration guide version, when --version is not supplied"},
	},
	Flags: []FlagSpec{
		{Name: "version", Argument: "vX.Y.Z", Desc: "Migration guide version (e.g. v0.4.0). Positional fallback accepted.", Group: "Selection"},
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: markdown | plain", Default: "markdown", Group: "Output"},
		{Name: "web", Desc: "Fetch from sparkwing.dev instead of the embedded corpus", Group: "Source"},
		{Name: "no-cache", Desc: "With --web, bypass the on-disk cache for this invocation", Group: "Source"},
	},
	GroupOrder: []string{"Selection", "Source", "Output", "Other"},
	Examples: []Example{
		{"Read the v0.4.0 guide", "sparkwing docs migrations read --version v0.4.0"},
		{"Positional shortcut", "sparkwing docs migrations read v0.4.0"},
		{"Read v0.5.0 from sparkwing.dev (not yet embedded)", "sparkwing docs migrations read --version v0.5.0 --web"},
	},
}

var cmdDocsMigrationsBetween = Command{
	Path:     "sparkwing docs migrations between",
	Synopsis: "Concatenate every guide in a version range into one blob",
	Description: `Returns every migration guide whose version is in (--from, --to],
in ascending version order, separated by markdown horizontal rules.
The output starts with a "Migration: vA -> vB" header so an agent
knows the range up-front.

This is the agent-killer command: one invocation produces the full
migration context for an N-version jump in a form ready to pipe.

--from defaults to v0.0.0 (every guide up through --to).
--to defaults to the highest version this CLI knows about.`,
	Flags: []FlagSpec{
		{Name: "from", Argument: "vX.Y.Z", Desc: "Exclusive lower bound (default v0.0.0)", Group: "Selection"},
		{Name: "to", Argument: "vA.B.C", Desc: "Inclusive upper bound (default = latest embedded version)", Group: "Selection"},
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: markdown | plain", Default: "markdown", Group: "Output"},
		{Name: "web", Desc: "Fetch every guide in the range from sparkwing.dev", Group: "Source"},
		{Name: "no-cache", Desc: "With --web, bypass the on-disk cache for this invocation", Group: "Source"},
	},
	GroupOrder: []string{"Selection", "Source", "Output", "Other"},
	Examples: []Example{
		{"Every guide for a v0.3.0 -> v0.4.0 jump", "sparkwing docs migrations between --from v0.3.0 --to v0.4.0"},
		{"Every guide up to a target version", "sparkwing docs migrations between --to v0.4.0"},
		{"Every guide this CLI knows (one-shot agent context)", "sparkwing docs migrations between"},
		{"Full range from sparkwing.dev (includes versions not yet embedded)", "sparkwing docs migrations between --web"},
	},
}

var cmdDocsVersions = Command{
	Path:     "sparkwing docs versions",
	Synopsis: "List doc versions known to this CLI (and sparkwing.dev with --web)",
	Description: `Reports each doc version the source knows about. Default
output is hermetic: only the binary's embedded version (plus every
migration-guide version shipped in the embed) appears, with no
network calls.

With --web, fetches sparkwing.dev/versions.json and merges in every
release available online -- useful for discovering newer versions
this CLI can render via --web on the read / list verbs.`,
	Flags: []FlagSpec{
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty | json | plain", Default: "pretty", Group: "Output"},
		{Name: "web", Desc: "Merge in sparkwing.dev/versions.json (network)", Group: "Source"},
		{Name: "no-cache", Desc: "With --web, bypass the on-disk cache for this invocation", Group: "Source"},
	},
	GroupOrder: []string{"Source", "Output", "Other"},
	Examples: []Example{
		{"Embedded only (default)", "sparkwing docs versions"},
		{"Every version available online", "sparkwing docs versions --web"},
		{"Agent-readable JSON", "sparkwing docs versions --web -o json"},
	},
}

var cmdDocsCache = Command{
	Path:     "sparkwing docs cache",
	Synopsis: "Inspect or clear the on-disk cache used by --web",
	Description: `--web fetches are cached to $XDG_CACHE_HOME/sparkwing/web/ (or
~/.cache/sparkwing/web/). The cache mirrors the URL path, so you
can ` + "`cat`" + ` the cached files directly when debugging.

Use ` + "`cache info`" + ` to see size / counts; use ` + "`cache clear`" + ` to wipe it.`,
	SubcommandOrder: []string{"info", "clear"},
	Examples: []Example{
		{"How big is the cache?", "sparkwing docs cache info"},
		{"Force-refresh on next --web call", "sparkwing docs cache clear"},
	},
}

var cmdDocsCacheInfo = Command{
	Path:     "sparkwing docs cache info",
	Synopsis: "Print cache dir, total size, per-resource breakdown",
	Description: `Walks the cache and prints a summary: total size, file counts
broken down by doc / migration / index, and the freshness state of
the cached versions.json (24h TTL).`,
	Flags: []FlagSpec{
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty | json", Default: "pretty", Group: "Output"},
	},
	GroupOrder: []string{"Output", "Other"},
	Examples: []Example{
		{"Human-readable", "sparkwing docs cache info"},
		{"Agent-readable", "sparkwing docs cache info -o json"},
	},
}

var cmdDocsCacheClear = Command{
	Path:     "sparkwing docs cache clear",
	Synopsis: "Remove every cached file",
	Description: `Deletes every file under the cache directory. Safe: the
implementation refuses to remove paths that don't resolve inside
the cache dir, so a stray symlink in the cache can't escape.

Useful when a cached versions.json or index.json has gone stale
faster than the 24h TTL window, or when debugging --web behavior.`,
	Examples: []Example{
		{"Wipe the cache", "sparkwing docs cache clear"},
	},
}

var cmdCache = Command{
	Path:     "sparkwing cache",
	Synopsis: "Inspect or trim the compiled pipeline binary cache",
	Description: `Every pipeline invocation compiles .sparkwing/ to a binary keyed
on a fingerprint of its source, and those binaries are cached under
$SPARKWING_HOME/cache/pipelines. They are large -- often 90 MB or
more each -- so the cache is bounded rather than allowed to grow.

Pruning runs automatically after a compile, keeping the most
recently used entries within a byte ceiling and an entry count.
These verbs are for looking at what is cached and for reclaiming
space on demand.`,
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
pruning evicts on -- not by when they were built.`,
	Flags: []FlagSpec{
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty | json", Default: "pretty", Group: "Output"},
		{Name: "all", Argument: "", Desc: "List every entry rather than the ten most recent", Group: "Output"},
	},
	GroupOrder: []string{"Output", "Other"},
	Examples: []Example{
		{"Human-readable", "sparkwing cache info"},
		{"Agent-readable", "sparkwing cache info -o json"},
		{"Every entry", "sparkwing cache info --all"},
	},
}

var cmdCachePrune = Command{
	Path:     "sparkwing cache prune",
	Synopsis: "Evict least recently used binaries down to the ceilings",
	Description: `Removes the least recently used cached binaries until the cache
fits both the byte ceiling and the entry ceiling. Defaults come
from $SPARKWING_CACHE_MAX_BYTES and $SPARKWING_CACHE_MAX_ENTRIES;
either accepts 0 to disable that dimension.

An execution lease protects each running binary. Prune skips active
and busy entries, bounds the number examined, and reports observed
capacity separately from removed entries. Callers making admission
decisions remeasure filesystem capacity after pruning.`,
	Flags: []FlagSpec{
		{Name: "max-bytes", Argument: "SIZE", Desc: "Byte ceiling, e.g. 512MiB", Group: "Limits"},
		{Name: "max-entries", Argument: "N", Desc: "Entry ceiling", Group: "Limits"},
		{Name: "all", Argument: "", Desc: "Remove every entry, ignoring both ceilings", Group: "Limits"},
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty | json", Default: "pretty", Group: "Output"},
	},
	GroupOrder: []string{"Limits", "Output", "Other"},
	Examples: []Example{
		{"Trim to the configured ceilings", "sparkwing cache prune"},
		{"Trim to a smaller budget", "sparkwing cache prune --max-bytes 512MiB"},
		{"Reclaim everything", "sparkwing cache prune --all"},
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

File counts note how many files git ignores and excluded, which is
the usual explanation when an edit does not trigger a rebuild.

When other cached entries came from the same checkout, each is listed
with the inputs that differ from the current key. That is the direct
answer to why a rebuild happened.`,
	Flags: []FlagSpec{
		{Name: "dir", Argument: "PATH", Desc: "Pipeline module directory", Default: "./.sparkwing", Group: "Target"},
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty | json", Default: "pretty", Group: "Output"},
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
	Description: `Pause nodes at selected hook points, inspect the paused pod,
drop into a shell, or release the node. Every debug verb is
ephemeral -- pause directives live only on the run they launch,
never in pipeline source. Pipelines stay production-clean.`,
	SubcommandOrder: []string{"run", "release", "attach", "env", "rerun", "replay"},
	Examples: []Example{
		{"Pause before the tests node", "sparkwing debug run build --pause-before tests"},
		{"Resume a paused node", "sparkwing debug release --run run-X --node tests"},
	},
}

var cmdDebugRun = Command{
	Path:     "sparkwing debug run",
	Synopsis: "Run a pipeline with ephemeral pause directives",
	Description: `Runs the named pipeline exactly as 'sparkwing run <pipeline>' would, with
additional pause hooks the orchestrator honors before and after
each matching node. Directives travel as env vars to the
pipeline binary; they never land in git-tracked code.

--pause-before <node> holds the node BEFORE its Run is invoked.
--pause-after  <node> holds the node AFTER its Run returns
  (success or failure). Both flags are repeatable.
--pause-on-failure holds ANY node whose Run returns a non-nil
  error. Skipped / cancelled / OnFailure-recovered nodes do NOT
  pause -- only honest Run errors.

Paused nodes hold for 30 minutes by default; set
SPARKWING_PAUSE_TIMEOUT=<duration> to change. A timed-out pause
is released with reason 'timeout-released' and surfaces in the
run record.

See 'sparkwing debug release' to resume, 'sparkwing debug env'
to inspect, and 'sparkwing debug attach' (cluster mode) to shell
into the pod holding the paused node.`,
	Flags: []FlagSpec{
		{Name: "pipeline", Argument: "NAME", Desc: "Pipeline (pipeline) name to run under debug supervision", Required: true, Group: "Target"},
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
		{"Release locally", "sparkwing debug release --run run-X --node tests"},
		{"Release in prod", "sparkwing debug release --run run-X --node tests --profile prod"},
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
		{Name: "profile", Argument: "NAME", Desc: "Profile name (cluster mode)", Group: "System"},
	},
	Examples: []Example{
		{"Attach in prod", "sparkwing debug attach --run run-X --node tests --profile prod"},
	},
}

var cmdDebugRerun = Command{
	Path:     "sparkwing debug rerun",
	Synopsis: "Reproduce a node's dispatch frame in an interactive shell",
	Description: `Reads the dispatch snapshot for the given run/node and reproduces
the env + workdir the orchestrator saw at dispatch time. Local mode
exec's $SHELL with the snapshot env applied and writes upstream Ref
outputs to ~/.sparkwing/rerun/<run>/<node>/refs so they're cat-able
from the shell. Cluster mode pipes a debug-pod manifest to 'kubectl
create' against a runner image (--image or $SPARKWING_RERUN_IMAGE),
carrying the snapshot env on stdin, then attaches to the pod and
deletes it on exit.

Snapshots never capture credential-shaped variables, and the
controller serves the captured env only to an admin token. The
banner names the keys the snapshot dropped; export those yourself.

Replays do NOT freeze the rest of the cluster: secrets re-resolve
through the standard sparkwing.Secret API on demand, and the runner
image is whatever the cluster runs today. Replay is "what would this
node do now, with the args+env it had then?", not a frozen
reproduction.

Default --seq selects the most-recent attempt for the node; pass
--seq 0 (or another integer) to target a specific attempt index.`,
	Flags: []FlagSpec{
		{Name: "run", Argument: "ID", Desc: "Run ID holding the node", Required: true, Group: "Target"},
		{Name: "node", Argument: "NAME", Desc: "Node ID to reproduce", Required: true, Group: "Target"},
		{Name: "seq", Argument: "N", Desc: "Attempt index; -1 selects most recent", Group: "Target"},
		{Name: "profile", Argument: "NAME", Desc: "Profile name (cluster mode)", Group: "System"},
		{Name: "image", Argument: "REF", Desc: "Runner image for cluster-mode debug pod (cluster mode)", Group: "System"},
	},
	Examples: []Example{
		{"Rerun locally", "sparkwing debug rerun --run run-X --node tests"},
		{"Rerun a specific attempt", "sparkwing debug rerun --run run-X --node tests --seq 1"},
		{"Rerun in prod", "sparkwing debug rerun --run run-X --node tests --profile prod --image ghcr.io/me/runner:v1"},
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

Replay is "what would this node do now, with the same args+env?":
secrets re-resolve fresh through sparkwing.Secret, BeforeRun hooks
re-fire, and any code drift in the registered job struct (renamed
type, removed field) aborts loud rather than silently producing
wrong results.

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
		{"Replay a node locally", "sparkwing debug replay --run run-X --node deploy"},
		{"Replay a prod run on your laptop", "sparkwing debug replay --profile prod --run run-X --node deploy"},
	},
}

var cmdDebugEnv = Command{
	Path:     "sparkwing debug env",
	Synopsis: "Print a paused node's env + workdir + claim holder",
	Description: `Inspection-only command: reads the stored node record (env map,
claim holder, current pause state) and prints them to stdout.
Does NOT spawn a shell. If the node is not paused, prints a
warning and exits 0 -- env info is captured at pause time, not
continuously.`,
	Flags: []FlagSpec{
		{Name: "run", Argument: "ID", Desc: "Run ID holding the node", Required: true, Group: "Target"},
		{Name: "node", Argument: "NAME", Desc: "Node ID to inspect", Required: true, Group: "Target"},
		{Name: "profile", Argument: "NAME", Desc: "Profile name (cluster mode)", Group: "System"},
	},
	Examples: []Example{
		{"Inspect locally", "sparkwing debug env --run run-X --node tests"},
	},
}

var runFlagSpecs = runFlagSpecsFromDocs()

func runFlagSpecsFromDocs() []FlagSpec {
	docs := sparkwing.SparkwingFlagDocs()
	out := make([]FlagSpec, 0, len(docs))
	for _, d := range docs {
		out = append(out, FlagSpec{
			Name:     d.Name,
			Short:    d.Short,
			Argument: d.Argument,
			Desc:     d.Desc,
			Group:    d.Group,
			Hot:      d.Hot,
		})
	}
	return out
}

var cmdPipeline = Command{
	Path:     "sparkwing pipeline",
	Synopsis: "This repo's pipelines",
	Description: `Per-project namespace. Every verb here operates on the
nearest .sparkwing/ walking up from the current directory.

Discovery (list / describe / discover / explain) shows what
pipelines this repo defines. 'new' scaffolds a fresh pipeline
(auto-bootstraps .sparkwing/ on first use). 'run' invokes one
(positional name; same as 'sparkwing run <name>'). 'hooks' wires
pipelines to git pre-commit / pre-push / post-commit.
'sparks' manages reusable spark libraries declared in
.sparkwing/sparks.yaml.

The discovery verbs (list / describe / discover / templates)
support -o json so an agent can parse output directly rather
than scraping tab-complete.

To bump the pipeline SDK pin in .sparkwing/go.mod, use
'sparkwing version update --sdk'. To see the current pin, run
'sparkwing version' (composite card).`,
	SubcommandOrder: []string{"list", "describe", "discover", "new", "explain", "lint", "plan", "run", "trigger", "hooks", "sparks"},
	Examples: []Example{
		{"Machine-readable catalog", "sparkwing pipeline list -o json"},
		{"One pipeline's details", "sparkwing pipeline describe --name release -o json"},
		{"Search by intent", `sparkwing pipeline discover --query "tag a release"`},
		{"First pipeline in a fresh repo (auto-bootstraps)", "sparkwing pipeline new --name release"},
		{"Inspect the DAG before running", "sparkwing pipeline explain --name release-all"},
		{"Run a pipeline", "sparkwing pipeline run release"},
	},
}

var cmdPipelineRun = Command{
	Path:     "sparkwing pipeline run",
	Synopsis: "Invoke a pipeline (canonical form of `sparkwing run <name>`)",
	Description: `Compiles the nearest .sparkwing/ binary and exec's it
with the named pipeline. Identical to the top-level shortcut
'sparkwing run <name>'.

The pipeline name is the only positional in the sparkwing
surface -- a deliberate exception, kept short because run is
typed many times a day.

Any flag not recognized by run itself is forwarded to the
pipeline binary, e.g. 'sparkwing pipeline run release
--version v1.2.3' passes --version through to the pipeline's
Args.`,
	PosArgs: []PosArg{
		{Name: "<pipeline>", Desc: "Pipeline name registered in .sparkwing/sparkwing.yaml", Required: true},
	},
	Flags:       runFlagSpecs,
	GroupOrder:  []string{"Source", "Range", "Safety", "System", "Other"},
	UsageSuffix: "[-- pipeline-flags...]",
	Examples: []Example{
		{"Run with no flags", "sparkwing pipeline run build-test-deploy"},
		{"Pass a typed pipeline arg", "sparkwing pipeline run release --version v0.28.1"},
		{"Run from a different git ref", "sparkwing pipeline run build-test-deploy --sw-ref feature/xyz"},
		{"Dispatch remotely", "sparkwing pipeline trigger deploy --profile prod"},
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
typed Arg, e.g. 'sparkwing pipeline trigger release --profile
prod --version v1.2.3' passes --version through to the trigger
payload -- same shape as 'sparkwing run'.

--working-tree freezes tracked changes and untracked non-ignored
files into an immutable Git snapshot, uploads it before admission,
and runs that exact snapshot without pushing to the origin. It
requires a complete SHA-1 repository; shallow and SHA-256 checkouts
fail before upload.

Requires a profile with controller: set. For local execution
against a profile's storage, use 'sparkwing run --profile X'.`,
	PosArgs: []PosArg{
		{Name: "<pipeline>", Desc: "Pipeline name registered on the controller", Required: true},
	},
	Flags: []FlagSpec{
		{Name: "profile", Argument: "NAME", Desc: "Profile (from ~/.config/sparkwing/profiles.yaml) whose controller runs the pipeline", Group: "System", Required: true},
		{Name: "detach", Desc: "Return once the trigger is registered (print the run id); don't follow", Group: "System"},
		{Name: "working-tree", Desc: "Run tracked changes and untracked non-ignored files from an immutable remote snapshot", Group: "Source"},
	},
	GroupOrder:  []string{"Source", "System", "Other"},
	UsageSuffix: "[-- pipeline-flags...]",
	Examples: []Example{
		{"Submit and follow", "sparkwing pipeline trigger release --profile prod --version v1.2.3"},
		{"Fire-and-forget; print run id and exit", "sparkwing pipeline trigger release --profile prod --detach"},
		{"Run the current dirty tree remotely", "sparkwing pipeline trigger test --profile gaming --working-tree"},
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
omitted.`,
	Flags: []FlagSpec{
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty | json | plain", Default: "pretty", Group: "Output"},
		{Name: "all", Desc: "Include entries marked hidden", Group: "Output"},
	},
	GroupOrder: []string{"Output", "Other"},
	Examples: []Example{
		{"Human-readable table", "sparkwing pipeline list"},
		{"Agent-readable catalog", "sparkwing pipeline list -o json"},
		{"Include hidden entries", "sparkwing pipeline list --all"},
	},
}

var cmdPipelineDescribe = Command{
	Path:     "sparkwing pipeline describe",
	Synopsis: "Print one pipeline's full metadata",
	Description: `Emits the full record for a single pipeline: kind, group,
description, typed args, examples, triggers, and (for scripts)
frontmatter-declared positional args and flags. Always resolves
hidden entries -- if you're asking for a name explicitly, the
hidden flag shouldn't surprise you.`,
	Flags: []FlagSpec{
		{Name: "name", Argument: "NAME", Desc: "Pipeline name to describe", Required: true, Group: "Target"},
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty | json | plain", Default: "pretty", Group: "Output"},
	},
	GroupOrder: []string{"Target", "Output", "Other"},
	Examples: []Example{
		{"Human-readable", "sparkwing pipeline describe --name release"},
		{"Agent-readable", "sparkwing pipeline describe --name release -o json"},
	},
}

var cmdPipelineDiscover = Command{
	Path:     "sparkwing pipeline discover",
	Synopsis: "Fuzzy search over pipeline names + descriptions + tags",
	Description: `Search the catalog by intent. Every token in --query
must match some haystack field (name / short / help / group /
tags / triggers); matches in the name score higher than matches
in prose so direct hits surface first.

-o json emits {name, kind, group, ..., score} records sorted by
score descending; agents should prefer -o json for consumption.`,
	Flags: []FlagSpec{
		{Name: "query", Argument: "TEXT", Desc: "Search query (one or more tokens, all must hit some field)", Required: true, Group: "Target"},
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty | json | plain", Default: "pretty", Group: "Output"},
	},
	GroupOrder: []string{"Target", "Output", "Other"},
	Examples: []Example{
		{"Find release-related pipelines", `sparkwing pipeline discover --query release`},
		{"Multi-token, all must hit", `sparkwing pipeline discover --query "tag release"`},
		{"Agent-readable ranked hits", `sparkwing pipeline discover --query deploy -o json`},
	},
}

var cmdPipelineNew = Command{
	Path:     "sparkwing pipeline new",
	Synopsis: "Scaffold a new Go pipeline",
	Description: `Creates a stub pipeline in the nearest .sparkwing/:
jobs/<snake>.go plus a sparkwing.yaml entry. Auto-bootstraps
.sparkwing/ on first use, so a fresh repo's first scaffold sets
up the package skeleton too -- no separate init step, no
sample pipeline you didn't ask for.

--template takes a shape, not a task: it picks the DAG. --on picks
what fires the pipeline, independently. Every combination runs green
in any repo before you edit it, because the Run bodies are echoes.

  --on pull_request   opened / synchronize / reopened
  --on push           any branch
  --on schedule       cron, 09:00 UTC daily
  --on manual         no trigger; runs only when invoked

One pipeline can declare several: --on push,pull_request, or repeat
the flag. 'manual' is the opt-out and cannot be combined with the rest.

Omit --on and the shape's own default applies (below). A trigger is
declarative -- the controller dispatches whichever pipeline its webhook
names -- so it changes nothing about 'sparkwing run <name>' locally,
and the scaffolded block carries a comment naming the filter it does
not have. Edit the 'on:' block in .sparkwing/sparkwing.yaml to change
any of it.

New to authoring? 'sparkwing docs read --guide authoring' returns the
DAG model, the idioms the linter enforces, how a pipeline fires, and
the sparkwing.yaml schema in one call -- the four pages you would
otherwise open one at a time.

Pass --sw-cd/-C to scaffold into a repo other than the current
directory (the .sparkwing search re-anchors there).

Shapes:
  - minimal (default): single-node Plan with a stubbed Run.
    Smallest viable shape; the editor's first move is replacing
    the placeholder Info() line with real logic.
  - build-test-deploy: three-node Plan (build -> test -> deploy)
    with echo Run bodies that print a placeholder line on each step.
    The canonical CI shape; first 'sparkwing run <name>' surfaces three
    exec banners + three echoed lines so the structure is
    visible end-to-end.
  - ci-pr-check: 3 nodes. lint and test run in parallel and a final
    gate job depends on both, so the pipeline is green only when every
    check passes. test Prefers a CI runner label. Defaults to
    '--on pull_request'.
  - release: linear version-bump -> changelog -> publish flow. The
    canonical release shape; publish Prefers a release runner label.
  - scheduled-report: 5 nodes. One collect job seeds three parallel
    gatherers (metrics, errors, usage) and publish-report converges
    them. Defaults to '--on schedule'.

The other three shapes default to no trigger. Any shape takes any
--on, so "PR-triggered single check" is
'--template minimal --on pull_request' rather than a three-node gate
with two nodes deleted.

Every shape scaffolds a pipeline that compiles, renders clean under
'pipeline explain', and passes 'pipeline lint': pure Plan(), runner-label
preferences over host branching, echo Run bodies so the first
'sparkwing run <name>' succeeds end-to-end.

For how a real pipeline is written -- container deploys, migrations,
canary rollouts -- read a worked one: 'sparkwing docs search -q <task>',
then 'sparkwing examples --name <name> --body'. Those are for reading,
not scaffolding; --template will not take one.

Refuses to clobber: if the name already exists in sparkwing.yaml
the command fails before writing anything.

Supply --hidden to hide from default listings; --short to pre-fill
the description.

See also:
  If your pipeline is a single linear shell sequence with no DAG,
  retry, or cross-runner concerns, a plain shell-script runner
  (e.g. just / make / a wrapper over ./bin/*.sh) is probably a
  better fit -- it skips the compile cycle.`,
	Flags: []FlagSpec{
		{Name: "name", Argument: "NAME", Desc: "New pipeline's kebab-case name (a-z, 0-9, -)", Required: true, Group: "Target"},
		{Name: "sw-cd", Short: "C", Argument: "DIR", Desc: "Scaffold as if started in this directory (re-anchors the .sparkwing search)", Group: "Target"},
		{Name: "template", Argument: "SHAPE", Desc: "DAG to scaffold: minimal (1 node) | build-test-deploy (3) | ci-pr-check (3) | release (3) | scheduled-report (5)", Default: "minimal", Group: "Scaffold"},
		{Name: "on", Argument: "EVENT", Desc: "Trigger(s) to declare: pull_request | push | schedule | manual (repeatable or comma-separated)", Default: "the shape's own", Group: "Scaffold"},
		{Name: "hidden", Desc: "Mark the entry hidden in default tab-complete menus", Group: "Scaffold"},
		{Name: "short", Argument: "TEXT", Desc: "Pre-fill the ShortHelp / desc line", Group: "Scaffold"},
	},
	GroupOrder: []string{"Target", "Scaffold", "Other"},
	Examples: []Example{
		{"Single-node pipeline (default shape)", "sparkwing pipeline new --name release"},
		{"Build/test/deploy DAG (three-node)", "sparkwing pipeline new --name release-all --template build-test-deploy"},
		{"Pull-request gate (lint + test -> gate)", "sparkwing pipeline new --name pr-check --template ci-pr-check"},
		{"Scheduled fan-out report", "sparkwing pipeline new --name daily-report --template scheduled-report"},
		{"One job, fired by pull requests", "sparkwing pipeline new --name pr-test --template minimal --on pull_request"},
		{"Fired by both push and pull requests", "sparkwing pipeline new --name ci --template ci-pr-check --on push,pull_request"},
		{"A gate you invoke by hand, not on every PR", "sparkwing pipeline new --name gate --template ci-pr-check --on manual"},
	},
}

var cmdExamples = Command{
	Path:     "sparkwing examples",
	Synopsis: "Worked pipelines to read, not starting points to scaffold",
	Description: `The sparks-core registry: complete, working pipelines --
container deploys for AWS and GCP, migrations, canary rollouts,
release publishing, test sharding. The template-verify pipeline
proves every one compiles, lints, and explains, and runs the
runnable-tier ones, so unlike prose they cannot quietly stop
being true.

Read them, do not scaffold from them. 'sparkwing pipeline new
--template <shape>' starts a pipeline; an example shows how a real
one is built once you have the shape. Reach for one when you want
to know how something is done rather than to begin.

Most arrivals should come through 'sparkwing docs search', which
ranks examples alongside the docs -- searching "ecs fargate"
answers the question without anyone browsing a list.

--category and --cloud narrow the list; a cloud-agnostic example
always passes a --cloud filter. --name switches to a full detail
view: description, when-to-use, prerequisite, parameters,
applicability, and README. Add --body for the pipeline source
rendered with each parameter's default.

-o json emits manifests for the list, or manifest + README
(+ rendered body with --body) for one example.`,
	Flags: []FlagSpec{
		{Name: "name", Argument: "EXAMPLE", Desc: "Show full detail for one example instead of the list", Group: "Target"},
		{Name: "body", Desc: "With --name, print the pipeline source (default + <placeholder> params)", Group: "Target"},
		{Name: "category", Argument: "CATEGORY", Desc: "Filter the list by applicability category", Group: "Filter"},
		{Name: "cloud", Argument: "CLOUD", Desc: "Filter the list by cloud (aws | gcp); cloud-agnostic examples always match", Group: "Filter"},
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty | json", Default: "pretty", Group: "Output"},
	},
	Examples: []Example{
		{"Browse them", "sparkwing examples"},
		{"Read one", "sparkwing examples --name container-deploy-ecs-fargate --body"},
		{"Usually you want this instead", "sparkwing docs search -q \"ecs fargate\""},
	},
}

var cmdPipelineExplain = Command{
	Path:     "sparkwing pipeline explain",
	Synopsis: "Render the pipeline's Plan DAG without dispatching any jobs",
	Description: `Compiles the nearest .sparkwing/ binary, calls the named
pipeline's Plan method, and prints the resulting DAG (nodes,
dependencies, approval gates) without running a single job.

Any --flag value tokens that are NOT recognized by explain itself
(i.e. anything other than --name / --all / -o/--output / --help) are
forwarded to the pipeline so Plans that branch on --env / --version
/ etc. can be previewed under realistic inputs. Missing required
args are non-fatal here -- explain renders a best-effort plan so
the shape is visible before every flag is provided.

--all sweeps every pipeline in .sparkwing/sparkwing.yaml, runs
Plan() on each with no extra args, and exits non-zero if any
pipeline fails. Designed as a CI gate: a Plan-time validation
mismatch (sparkwing.RefTo[T] type drift, Produces[T] / SetResult
asymmetry, duplicate node ID, etc.) blocks merges before the
pipeline ever runs.`,
	Flags: []FlagSpec{
		{Name: "name", Argument: "NAME", Desc: "Pipeline to explain (one of --name or --all required)", Group: "Target"},
		{Name: "all", Desc: "Validate every pipeline in this repo's sparkwing.yaml; non-zero exit on any failure", Group: "Target"},
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty | json", Default: "pretty", Group: "Output"},
	},
	GroupOrder:  []string{"Target", "Output", "Other"},
	UsageSuffix: "[-- pipeline-flags...]",
	Examples: []Example{
		{"Inspect release-all's DAG", "sparkwing pipeline explain --name release-all"},
		{"Preview with args (forwarded to the pipeline)", "sparkwing pipeline explain --name example-release --env prod"},
		{"Agent-readable JSON", "sparkwing pipeline explain --name release-all -o json"},
		{"Validate every pipeline (CI gate)", "sparkwing pipeline explain --all"},
	},
}

var cmdExampleScaffold = Command{
	Path:     "sparkwing examples scaffold",
	Synopsis: "Materialize an example into a repo (verification path)",
	Hidden:   true,
	Description: `Renders one example's source into the target repo, the way
'pipeline new' renders a shape. Used by the template-verify pipeline to
prove every example still compiles, lints, and explains (and that the
runnable-tier ones run).

To start a pipeline use 'sparkwing pipeline new --template <shape>'.`,
	Flags: []FlagSpec{
		{Name: "name", Argument: "EXAMPLE", Desc: "Example to materialize", Required: true, Group: "Target"},
		{Name: "param", Argument: "K=V", Desc: "Example parameter (repeatable)", Group: "Target"},
		{Name: "sw-cd", Short: "C", Argument: "DIR", Desc: "Operate as if started in this directory", Group: "System"},
	},
}

var cmdPipelineLint = Command{
	Path:     "sparkwing pipeline lint",
	Synopsis: "Check pipeline source for idiomatic anti-patterns (enforced gate)",
	Description: `Statically analyzes pipeline source for the anti-patterns
that make a Plan() non-deterministic, impure, or misconfigured,
and exits non-zero on any violation. Unlike 'explain' (which
builds and runs Plan to validate the resulting DAG), 'lint' reads
the Go source with go/ast -- it never compiles or runs anything,
so it works against a pinned-SDK .sparkwing/ tree.

Only the Plan() body is inspected; code inside job/step closures
and SkipIf / BeforeRun bodies runs at dispatch, so I/O and
environment reads there are idiomatic and never flagged.

The rule set (see --rules for each rule's charter):
  plan-io              I/O (shell, exec, file, http) in Plan()
  plan-runtime-branch  os.Getenv / runtime.GOOS / IsLocal branching in Plan()
  runner-label         blank runner labels; Inline + Requires on one job
  unused-ref           a RefTo result discarded into _ or a bare statement
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
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty | json | plain", Default: "pretty", Group: "Output"},
		{Name: "sw-cd", Short: "C", Argument: "DIR", Desc: "Operate as if started in this directory (re-anchors the .sparkwing search)", Group: "System"},
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
	Description: `Compiles the nearest .sparkwing/ binary, calls the named
pipeline's Plan method, and prints the runtime-resolved DAG --
the same structure 'explain' shows plus a per-step decision
("would_run" / "would_skip <reason>") evaluated under the
supplied args and --start-at / --stop-at bounds. NO step bodies
execute.

Skip reasons surface their cause:
  - user_skipif    : a SkipIf predicate would match at run time
  - range_skip     : item is outside the --start-at..--stop-at window

For SpawnNodeForEach generators (dynamic fan-out), cardinality is
reported as "unresolved" with a pointer to the source item -- the
honest answer when the count depends on a runtime value.

State-loading caveat: if a step normally populates in-memory state
that downstream steps consume, --start-at past it leaves state
empty. The plan output reflects this honestly (downstream steps
show "would_run") but operators should design step bodies to
lazy-load when state isn't populated, so resume-from-step is safe.

Like 'explain', this is the read-only pre-flight surface; pair
with 'sparkwing run <name>' to actually dispatch.`,
	Flags: []FlagSpec{
		{Name: "name", Argument: "NAME", Desc: "Pipeline to plan", Group: "Target"},
		{Name: "start-at", Argument: "STEP", Desc: "Skip every WorkStep upstream of STEP in the resulting plan", Group: "Range"},
		{Name: "stop-at", Argument: "STEP", Desc: "Skip every WorkStep downstream of STEP in the resulting plan", Group: "Range"},
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty | json", Default: "pretty", Group: "Output"},
	},
	GroupOrder:  []string{"Target", "Range", "Output", "Other"},
	UsageSuffix: "[-- pipeline-flags...]",
	Examples: []Example{
		{"Resolve cluster-up's DAG with current args", "sparkwing pipeline plan --name cluster-up"},
		{"Preview a resume-from-step", "sparkwing pipeline plan --name cluster-up --start-at install-argocd"},
		{"Agent-readable JSON for diff against expectations", "sparkwing pipeline plan --name release-all -o json"},
	},
}

var cmdRunConfig = Command{
	Path:     "sparkwing run config",
	Synopsis: "Print a pipeline's declared Secrets with provenance",
	Description: `Pure inspection: lists every Secret the pipeline
declares, each with its source binding and resolution status when a
source is configured -- useful before driving destructive runs to
confirm you'd hit the right vault. No Plan() runs, nothing
dispatches, nothing mutates.

Invocation: ` + "`sparkwing run <pipeline> config`" + ` -- the
pipeline binary handles the subverb directly.`,
	Flags: []FlagSpec{
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty | json", Default: "pretty", Group: "Output"},
	},
	GroupOrder: []string{"Output", "Other"},
	Examples: []Example{
		{"Inspect the declared secrets", "sparkwing run release config"},
		{"Agent-readable form", "sparkwing run release config -o json"},
	},
	HideFromComplete: true,
}

var cmdRun = Command{
	Path:     "sparkwing run",
	Synopsis: "Invoke a pipeline",
	Description: `Compiles the nearest .sparkwing/ binary and exec's it
with the named pipeline.

The pipeline name is the only positional in the sparkwing
surface -- a deliberate exception, kept short because run is
typed many times a day. Every other input is a named flag.

Any flag not recognized by run itself is forwarded to the
pipeline binary, e.g. 'sparkwing run release --version
v1.2.3' passes --version through to the pipeline's Args.

For remote execution on a profile's controller, use
'sparkwing pipeline trigger <name> --profile PROF'.

Output: a human-readable per-node summary when stdout is a
terminal, line-delimited JSON otherwise (so piped/agent/CI
consumers get a stable JSONL stream). Force a format with
SPARKWING_LOG_FORMAT=pretty|json|quiet. quiet collapses the
run to a progress line plus a one-line pass/fail status with
the run id, surfacing the failing step only on failure; it is
the default for managed git hooks.`,
	PosArgs: []PosArg{
		{Name: "<pipeline>", Desc: "Pipeline name registered in .sparkwing/sparkwing.yaml", Required: true},
	},
	Flags:              runFlagSpecs,
	GroupOrder:         []string{"Source", "Range", "Safety", "System", "Other"},
	SubcommandOrder:    []string{"config"},
	SubcommandOptional: true,
	UsageSuffix:        "[-- pipeline-flags...]",
	Examples: []Example{
		{"Run with no flags", "sparkwing run build-test-deploy"},
		{"Pass a typed pipeline arg", "sparkwing run release --version v0.28.1"},
		{"Run from a different git ref", "sparkwing run build-test-deploy --sw-ref feature/xyz"},
		{"Retry a failed run", "sparkwing runs retry RUN_ID --failed"},
		{"Submit to a remote controller", "sparkwing pipeline trigger deploy --profile prod"},
	},
}

var cmdProfile = Command{
	Path:     "sparkwing profile",
	Synopsis: "Show which profile sparkwing would use right now, and why",
	Description: `Reports the profile a sparkwing command would resolve to and
the chain that picked it (flag > project hint > detect > default
> builtin laptop), using the same resolver 'sparkwing run' and
'sparkwing pipeline trigger' use -- so the answer matches what
they would actually do.

With no flag it shows the active no-flag resolution. With
--profile NAME it shows the hypothetical: what adding that flag
to your next command would select. Tokens are never printed.`,
	Flags: []FlagSpec{
		{Name: "profile", Argument: "NAME", Desc: "Show the hypothetical resolution for `--profile NAME`", Group: "Input"},
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty|json", Default: "pretty", Group: "Output"},
	},
	GroupOrder: []string{"Input", "Output", "Other"},
	Examples: []Example{
		{"Active profile with no flag", "sparkwing profile"},
		{"What would --profile prod pick", "sparkwing profile --profile prod"},
		{"Machine-readable", "sparkwing profile -o json"},
	},
}

var cmdDashboard = Command{
	Path:     "sparkwing dashboard",
	Synopsis: "Manage the local dashboard + API server",
	Description: `Background lifecycle for the laptop-local dashboard.
'start' spawns a detached server (writes PID + log under
$SPARKWING_HOME), 'kill' stops it, 'status' reports liveness.

The server is one Go process that hosts the embedded Next.js SPA,
the JSON API, the log endpoints, and the SQLite store on the same
port. There is no separate Node process. The dashboard is purely
for visualization -- everything it shows is reachable from the
CLI as well.`,
	SubcommandOrder: []string{"start", "kill", "status"},
	Examples: []Example{
		{"Start the dashboard", "sparkwing dashboard start"},
		{"Check liveness", "sparkwing dashboard status"},
		{"Stop the dashboard", "sparkwing dashboard kill"},
	},
}

var cmdDashboardStart = Command{
	Path:     "sparkwing dashboard start",
	Synopsis: "Spawn the detached dashboard server (replaces any running one)",
	Description: `Detaches a child process that runs the in-process
dashboard + API + logs server (pkg/localws). PID is written to
$SPARKWING_HOME/dashboard.pid; stdout/stderr are appended to
$SPARKWING_HOME/dashboard.log. Returns once the listener is
accepting TCP connections so callers can immediately curl it.

Replaces any resident dashboard: a live server on file is drained
and a fresh one takes its place. It refuses only when the resident
dashboard is a newer version than this CLI.`,
	Flags: []FlagSpec{
		{Name: "addr", Argument: "HOST:PORT", Desc: "Bind address", Default: "127.0.0.1:4343", Group: "Bind"},
		{Name: "allow-remote", Desc: "Serve a non-loopback --addr. The API has no authentication, so every host that reaches it can run pipelines and read secrets.", Group: "Bind"},
		{Name: "home", Argument: "DIR", Desc: "State directory (default: $SPARKWING_HOME or ~/.sparkwing)", Group: "System"},
		{Name: "profile", Argument: "PROFILE", Desc: "Profile from ~/.config/sparkwing/profiles.yaml (uses its log_store + artifact_store)", Group: "Storage"},
		{Name: "log-store", Argument: "URL", Desc: "Pluggable log backend URL (fs:///abs/path, s3://bucket/prefix). Overrides --profile.", Group: "Storage"},
		{Name: "artifact-store", Argument: "URL", Desc: "Pluggable artifact backend URL (fs:///abs/path, s3://bucket/prefix). Overrides --profile.", Group: "Storage"},
		{Name: "read-only", Desc: "Reject writes on /api/v1/* (auth + webhooks remain open)", Group: "Storage"},
		{Name: "no-local-store", Desc: "Skip local SQLite; list runs from --artifact-store. Requires --log-store + --artifact-store.", Group: "Storage"},
	},
	GroupOrder: []string{"Bind", "Storage", "System", "Other"},
	Examples: []Example{
		{"Start with defaults", "sparkwing dashboard start"},
		{"Use an alternate port", "sparkwing dashboard start --addr 127.0.0.1:5000"},
		{"Isolate state under a scratch dir", "sparkwing dashboard start --home " + helpExampleScratchDir("sparkwing-x")},
		{"Tail CI runs from S3 (no SQLite)", "sparkwing dashboard start --profile ci-smoke --no-local-store --read-only"},
	},
}

var cmdDashboardKill = Command{
	Path:     "sparkwing dashboard kill",
	Synopsis: "Stop a running dashboard server",
	Description: `Sends SIGTERM to the PID recorded in
$SPARKWING_HOME/dashboard.pid, polls for exit, escalates to SIGKILL
after 5s if necessary, and removes the PID file. No-op (exit 0)
when nothing is running.`,
	Flags: []FlagSpec{
		{Name: "home", Argument: "DIR", Desc: "State directory (default: $SPARKWING_HOME or ~/.sparkwing)", Group: "System"},
	},
	Examples: []Example{
		{"Stop the dashboard", "sparkwing dashboard kill"},
	},
}

var cmdDashboardStatus = Command{
	Path:     "sparkwing dashboard status",
	Synopsis: "Report whether the dashboard is running",
	Description: `Reads $SPARKWING_HOME/dashboard.pid, probes the PID
with kill(0), and reports running state + URL. Exit code 0 when
running, 1 when not.`,
	Flags: []FlagSpec{
		{Name: "home", Argument: "DIR", Desc: "State directory (default: $SPARKWING_HOME or ~/.sparkwing)", Group: "System"},
	},
	Examples: []Example{
		{"Check liveness", "sparkwing dashboard status"},
	},
}

var cmdWorker = Command{
	Path:     "sparkwing cluster worker",
	Synopsis: "Claim triggers from a profile's controller and run them in-process",
	Description: `Polls the trigger queue at the selected profile's
controller and executes each claimed trigger in-process. Laptop-local:
no K8s, no warm pool, no image dispatch. For the cluster-mode worker
with --runner k8s|warm and image / service-account flags, use
sparkwing-runner.

Run against a remote controller via --profile prod (or whichever profile),
or against a local 'sparkwing dashboard start' via --profile local.`,
	Flags: []FlagSpec{
		{Name: "profile", Argument: "PROFILE", Desc: "Profile name from profiles.yaml", Required: true, Group: "Connection"},
		{Name: "poll", Argument: "DUR", Desc: "Claim poll interval when the queue is empty", Default: "1s", Group: "Tuning"},
		{Name: "heartbeat", Argument: "DUR", Desc: "Claim-lease heartbeat cadence", Default: "5s", Group: "Tuning"},
	},
	GroupOrder: []string{"Connection", "Tuning", "Other"},
	Examples: []Example{
		{"Run against a named profile", "sparkwing cluster worker --profile local"},
		{"Faster polling for tight dev loops", "sparkwing cluster worker --profile local --poll 250ms"},
	},
}

var cmdGC = Command{
	Path:     "sparkwing cluster gc",
	Synopsis: "Sweep stale warm-PVC state",
	Description: `Operator-facing manual invocation of the warm-PVC sweep.
Normally fires at 'sparkwing cluster worker' startup; exposed as a subcommand
so operators can trigger it against a running pod via kubectl
exec during incident response.

When --profile is omitted, the run-directory sweep is skipped; the
mtime-based git/ and tmp/ sweeps still run and free disk. Supply
--profile to enable the full sweep.`,
	Flags: []FlagSpec{
		{Name: "root", Argument: "DIR", Desc: "Warm-PVC root (default: $SPARKWING_HOME resolution)", Group: "Input"},
		{Name: "profile", Argument: "NAME", Desc: "Profile name; without it run-dir sweep is skipped", Group: "System"},
	},
	Examples: []Example{
		{"mtime-only sweep in-pod (no controller)", "sparkwing cluster gc"},
		{"Full sweep against prod controller", "sparkwing cluster gc --profile prod"},
		{"Target a specific warm root", "sparkwing cluster gc --root /var/lib/sparkwing --profile prod"},
	},
}

var cmdDoctor = Command{
	Path:     "sparkwing doctor",
	Synopsis: "Diagnose and safely repair local state",
	Description: `Checks the sparkwing home for state that is safe to
remove because the process behind it is provably gone, repairs what it
finds, and reports everything -- so it is safe to run at any time and a
healthy machine reports a clean bill. It never kills a process, never
touches the admission daemon's live state, and never touches
cluster-scoped (global) rows.

It repairs five things: permissive files and directories in the Sparkwing
home; local run rows still marked running whose process
is gone and which the daemon does not know about; leftover box-slot lock
files from older binaries (a file whose owner is still alive is reported,
never removed); local-scope concurrency rows whose run has ended; and
run directories on disk whose run row no longer exists.

On POSIX systems, doctor removes group, other, and special permission bits
without granting new owner access; cached executables retain any existing
owner execute bit. The walk never follows symlinks. Windows access
is governed by DACLs that this portable check cannot inspect or repair,
so doctor reports the permission audit as unverified rather than healthy.

If an older-pinned pipeline binary is still admitting outside the daemon
through a held box-slot lock, doctor reports it and points at the fix --
bump that repo's sparkwing pin -- rather than deleting live state.

Every report opens with the admission daemon's state -- serving (with its
version and protocol), none running, or unreachable. That line is always
there, because a sweep that never reached the daemon otherwise printed
the same counts a healthy machine prints. An unreachable daemon is not a
clean bill: the checks below it did not run, and the run-row repair is
skipped rather than risk finalizing a run that daemon is still holding.

It also reports (never repairs) standing problems that otherwise surface
only as opaque per-run failures: repeated admission rejections, a version
skew with the resident daemon, quarantined admission ledgers, and a
capacity profile poisoned by contention -- one whose learned demand floor
prices every run at the whole machine, named with the exact
runs stats --reset command that clears it.

Use --dry-run to report what it would repair without changing anything.`,
	Flags: []FlagSpec{
		{Name: "dry-run", Desc: "Report what would be repaired without changing anything", Group: "Input"},
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty | json | plain", Group: "Output"},
		{Name: "home", Argument: "DIR", Desc: "Sparkwing home to inspect (default: $SPARKWING_HOME or ~/.sparkwing)", Group: "System"},
	},
	GroupOrder: []string{"Input", "Output", "System", "Other"},
	Examples: []Example{
		{"Diagnose and repair now", "sparkwing doctor"},
		{"Report without changing anything", "sparkwing doctor --dry-run"},
		{"Agent-readable report", "sparkwing doctor -o json"},
	},
}

var cmdCompletion = Command{
	Path:             "sparkwing completion",
	Synopsis:         "Emit a shell completion script (bash|zsh|fish)",
	HideFromComplete: true,
	Description: `Prints a completion script for the selected shell. Source it
from your shell rc:

  # bash
  source <(sparkwing completion --shell bash)

  # zsh (add 'autoload -U compinit; compinit' once above)
  source <(sparkwing completion --shell zsh)

  # fish
  sparkwing completion --shell fish | source

zsh and fish get per-item descriptions; bash is name-only because
compgen lacks the facility.`,
	Flags: []FlagSpec{
		{Name: "shell", Argument: "NAME", Desc: "bash | zsh | fish", Required: true, Group: "Target"},
	},
	GroupOrder: []string{"Target", "Other"},
	Examples: []Example{
		{"Wire completion for the current zsh session", "source <(sparkwing completion --shell zsh)"},
		{"Install persistent completion for fish", "sparkwing completion --shell fish > ~/.config/fish/completions/sparkwing.fish"},
	},
}

var cmdProfiles = Command{
	Path:     "sparkwing configure profiles",
	Synopsis: "Manage connection profiles for remote controllers",
	Description: `Profile config lives at $SPARKWING_PROFILES (if set), else
$XDG_CONFIG_HOME/sparkwing/profiles.yaml, else
~/.config/sparkwing/profiles.yaml. Permissions on save are 0600.

Every human-driven client command (tokens, users, runs
retry/cancel/prune/logs, gc) reads connection info from the
selected profile via --profile NAME. No --controller/--token flags
exist on other commands; profiles are the only config surface.`,
	SubcommandOrder: []string{"add", "list", "show", "remove", "duplicate", "set", "test"},
}

var cmdProfilesAdd = Command{
	Path:     "sparkwing configure profiles add",
	Synopsis: "Register a new connection profile",
	Description: `Creates a new entry in profiles.yaml. --name and --controller
are required; --token is optional. Configure storage and service
backends by editing profiles.yaml.`,
	Flags: []FlagSpec{
		{Name: "name", Argument: "NAME", Desc: "Profile name (unique per profiles.yaml)", Required: true, Group: "Input"},
		{Name: "controller", Argument: "URL", Desc: "Controller base URL", Required: true, Group: "Connection"},
		{Name: "token", Argument: "TOKEN", Desc: "Bearer token (omit for local/unauthed stacks)", Group: "Connection"},
	},
	GroupOrder: []string{"Input", "Connection", "Other"},
	Examples: []Example{
		{"Add a prod profile", "sparkwing configure profiles add --name prod --controller https://api.sparkwing.example --token $TOKEN"},
		{"Add a local profile without auth", "sparkwing configure profiles add --name local --controller http://127.0.0.1:4344"},
	},
}

var cmdProfilesList = Command{
	Path:     "sparkwing configure profiles list",
	Synopsis: "Print every registered profile",
	Description: `Prints a table of profile name, controller URL, logs URL, and
token. JSON is one profile per line; the token is redacted in
every mode.`,
	Flags: []FlagSpec{
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty | json | plain", Default: "pretty", Group: "Output"},
	},
	GroupOrder: []string{"Output", "Other"},
	Examples: []Example{
		{"List profiles", "sparkwing configure profiles list"},
		{"Agent-readable record", "sparkwing configure profiles list -o json"},
	},
}

var cmdProfilesShow = Command{
	Path:     "sparkwing configure profiles show",
	Synopsis: "Print one profile's full config",
	Description: `Prints all fields of the profile named by --name. Token is
redacted unless --show-token is passed.`,
	Flags: []FlagSpec{
		{Name: "name", Argument: "NAME", Desc: "Profile name", Required: true, Group: "Input"},
		{Name: "show-token", Desc: "Print the raw token (redacted by default)", Group: "Output"},
	},
	GroupOrder: []string{"Input", "Output", "Other"},
	Examples: []Example{
		{"Show a named profile", "sparkwing configure profiles show --name prod"},
		{"Show a named profile with the raw token", "sparkwing configure profiles show --name prod --show-token"},
	},
}

var cmdProfilesRemove = Command{
	Path:        "sparkwing configure profiles remove",
	Synopsis:    "Delete a profile",
	Description: `Removes the named entry from profiles.yaml.`,
	Flags: []FlagSpec{
		{Name: "name", Argument: "NAME", Desc: "Profile name to remove", Required: true, Group: "Input"},
	},
	Examples: []Example{
		{"Remove a stale profile", "sparkwing configure profiles remove --name old-stage"},
	},
}

var cmdProfilesDuplicate = Command{
	Path:        "sparkwing configure profiles duplicate",
	Synopsis:    "Copy one profile's config into another",
	Description: `Useful when you want to tweak a known-good profile (say, change the token for a staging rotation) without hand-editing yaml.`,
	Flags: []FlagSpec{
		{Name: "src", Argument: "NAME", Desc: "Source profile name", Required: true, Group: "Input"},
		{Name: "dst", Argument: "NAME", Desc: "Destination profile name (must not exist yet)", Required: true, Group: "Input"},
	},
	Examples: []Example{
		{"Branch prod into a staging-prod profile", "sparkwing configure profiles duplicate --src prod --dst staging-prod"},
	},
}

var cmdProfilesSet = Command{
	Path:     "sparkwing configure profiles set",
	Synopsis: "Update fields on an existing profile",
	Description: `Only flags you pass are overwritten. --token="" explicitly
clears the token (empty value, not an omitted flag). Use
--show-token on 'profiles show' afterward to confirm.`,
	Flags: []FlagSpec{
		{Name: "name", Argument: "NAME", Desc: "Profile name to mutate", Required: true, Group: "Input"},
		{Name: "controller", Argument: "URL", Desc: "New controller URL", Group: "Connection"},
		{Name: "token", Argument: "TOKEN", Desc: "New bearer token (empty string clears)", Group: "Connection"},
	},
	GroupOrder: []string{"Input", "Connection", "Other"},
	Examples: []Example{
		{"Rotate a profile's token", "sparkwing configure profiles set --name prod --token $NEW_TOKEN"},
		{"Change a profile's controller", "sparkwing configure profiles set --name prod --controller https://api.sparkwing.example"},
	},
}

var cmdTokens = Command{
	Path:     "sparkwing cluster tokens",
	Synopsis: "Manage controller API tokens",
	Description: `All subcommands resolve controller URL + admin bearer from the
profile named by --profile.
Token creation prints the raw value to stdout exactly ONCE --
stash it immediately.`,
	SubcommandOrder: []string{"create", "list", "revoke", "lookup", "rotate"},
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
		{Name: "principal", Argument: "NAME", Desc: "Free-form label identifying the token holder", Required: true, Group: "Input"},
		{Name: "scope", Argument: "CSV", Desc: "Comma-separated scopes (e.g. runs.read,runs.write); auth.md lists the full set", Group: "Input"},
		{Name: "ttl", Argument: "DURATION", Desc: "Token lifetime (e.g. 30d, 720h). 0 = never expires", Group: "Input"},
		{Name: "profile", Argument: "NAME", Desc: "Profile name", Required: true, Group: "System"},
	},
	Examples: []Example{
		{"Mint a service token with write scopes", "sparkwing cluster tokens create --type service --principal deploy-bot --scope runs.read,runs.write --profile prod"},
		{"Mint a user token that expires in 30 days", "sparkwing cluster tokens create --type user --principal alice --scope admin --ttl 720h --profile prod"},
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
scope arrays, suitable for piping into jq.`,
	Flags: []FlagSpec{
		{Name: "type", Argument: "KIND", Desc: "Filter by token type", Group: "Filter"},
		{Name: "include-revoked", Desc: "Include revoked tokens in the output", Group: "Filter"},
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty | json", Default: "pretty", Group: "Output"},
		{Name: "profile", Argument: "NAME", Desc: "Profile name", Required: true, Group: "System"},
	},
	Examples: []Example{
		{"List all active tokens", "sparkwing cluster tokens list --profile prod"},
		{"Audit every revoked service token", "sparkwing cluster tokens list --type service --include-revoked --profile prod"},
		{"Inspect the warm-runner pool token's scopes as JSON", "sparkwing cluster tokens list --profile prod -o json | jq '.[] | select(.principal==\"agent:sparkwing-warm-runner\") | .scopes'"},
	},
}

var cmdTokensRevoke = Command{
	Path:        "sparkwing cluster tokens revoke",
	Synopsis:    "Mark a token revoked",
	Description: `Subsequent requests using the token receive HTTP 401. Revocation is immediate and irreversible.`,
	Flags: []FlagSpec{
		{Name: "prefix", Argument: "PREFIX", Desc: "Non-secret token prefix (from 'tokens list')", Required: true, Group: "Input"},
		{Name: "profile", Argument: "NAME", Desc: "Profile name", Required: true, Group: "System"},
	},
	Examples: []Example{
		{"Revoke a leaked token", "sparkwing cluster tokens revoke --prefix a1b2c3d4 --profile prod"},
	},
}

var cmdTokensLookup = Command{
	Path:        "sparkwing cluster tokens lookup",
	Synopsis:    "Print metadata for a single token",
	Description: `Prints the JSON metadata for a token given its non-secret prefix. Useful for confirming principal + scopes before revoking or rotating.`,
	Flags: []FlagSpec{
		{Name: "prefix", Argument: "PREFIX", Desc: "Non-secret token prefix", Required: true, Group: "Input"},
		{Name: "profile", Argument: "NAME", Desc: "Profile name", Required: true, Group: "System"},
	},
	Examples: []Example{
		{"Inspect a token before revoking", "sparkwing cluster tokens lookup --prefix a1b2c3d4 --profile prod"},
	},
}

var cmdTokensRotate = Command{
	Path:     "sparkwing cluster tokens rotate",
	Synopsis: "Mint a replacement token with a grace window",
	Description: `Creates a new token and schedules the old token for revocation
after --grace. During the grace window, both tokens work, which
lets callers cycle credentials without downtime.`,
	Flags: []FlagSpec{
		{Name: "prefix", Argument: "PREFIX", Desc: "Non-secret prefix of the token to rotate", Required: true, Group: "Input"},
		{Name: "grace", Argument: "DURATION", Desc: "Window during which the old token still authenticates", Default: "24h", Group: "Input"},
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
by the web pod's login flow. Connection info comes from the
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
sessions may reach; omitting it grants admin.`,
	Flags: []FlagSpec{
		{Name: "name", Argument: "NAME", Desc: "Dashboard username", Required: true, Group: "Input"},
		{Name: "password", Argument: "PASSWORD", Desc: "Password (omit to prompt interactively)", Group: "Input"},
		{Name: "scope", Argument: "LIST", Desc: "Comma-separated scopes (omit to grant admin)", Group: "Input"},
		{Name: "profile", Argument: "NAME", Desc: "Profile name", Required: true, Group: "System"},
	},
	Examples: []Example{
		{"Interactive add", "sparkwing cluster users add --name alice --profile prod"},
		{"Read-only dashboard account", "sparkwing cluster users add --name viewer --scope runs.read,logs.read --profile prod"},
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
	Description: `Deletes the user row. Any sessions that user holds remain
valid until their individual expiry -- sparkwing does not
proactively invalidate active cookies on delete.`,
	Flags: []FlagSpec{
		{Name: "name", Argument: "NAME", Desc: "Dashboard username to remove", Required: true, Group: "Input"},
		{Name: "profile", Argument: "NAME", Desc: "Profile name", Required: true, Group: "System"},
	},
	Examples: []Example{
		{"Delete a user", "sparkwing cluster users delete --name alice --profile prod"},
	},
}

var cmdJobs = Command{
	Path:     "sparkwing runs",
	Synopsis: "Inspect and control pipeline runs",
	Description: `Runs are the per-invocation records of pipeline execution.
Every 'sparkwing run <pipeline>' produces a run; cluster mode surfaces
the same runs remotely via the controller.

Local-mode subcommands (list, status, logs, errors) read from
~/.sparkwing/runs/. Controller-mode subcommands (cancel, retry,
prune) require a profile; 'runs logs' supports both.`,
	SubcommandOrder: []string{"submit", "consumer", "list", "status", "summary", "timeline", "wait", "find", "grep", "logs", "errors", "failures", "stats", "last", "tree", "get", "receipt", "annotations", "approvals", "triggers", "retry", "cancel", "bounce", "prune"},
}

var cmdJobsList = Command{
	Path:     "sparkwing runs list",
	Synopsis: "List recent pipeline runs",
	Description: `Without --profile, reads from the local run directory. With --profile NAME,
fetches from the named profile's controller. Filters compose with
AND semantics across flag types (pipeline=X AND status=Y), OR
semantics within a repeated flag (pipeline=X OR pipeline=Y).

With -q / --quiet the output is just run ids, one per line, for
shell piping:

  sparkwing runs list --pipeline X --limit 1 -q --profile prod \
      | xargs -I{} sparkwing runs logs --run {} --profile prod --follow`,
	Flags: []FlagSpec{
		{Name: "pipeline", Argument: "NAME", Desc: "Filter by pipeline name (repeatable; prefix `!` to exclude)", Group: "Filter"},
		{Name: "status", Argument: "STATUS", Desc: "Filter by status: running|success|failed|cancelled (repeatable; prefix `!` to exclude)", Group: "Filter"},
		{Name: "branch", Argument: "BRANCH", Desc: "Filter by git branch (repeatable; prefix `!` to exclude)", Group: "Filter"},
		{Name: "sha", Argument: "PREFIX", Desc: "Filter by git sha prefix (repeatable; prefix `!` to exclude)", Group: "Filter"},
		{Name: "error", Argument: "SUBSTR", Desc: "Substring match against the persisted failure reason", Group: "Filter"},
		{Name: "search", Argument: "QUERY", Desc: "Free-text search across pipeline/branch/sha/id/error; prefix a term with `-` to exclude", Group: "Filter"},
		{Name: "since", Argument: "DURATION", Desc: "Only runs newer than this (e.g. 1h, 24h, 7d)", Group: "Filter"},
		{Name: "started-after", Argument: "DATE", Desc: "Only runs whose StartedAt >= this (today, yesterday, 24h, 7d, or a date)", Group: "Filter"},
		{Name: "started-before", Argument: "DATE", Desc: "Only runs whose StartedAt <= this", Group: "Filter"},
		{Name: "finished-after", Argument: "DATE", Desc: "Only runs whose FinishedAt >= this (excludes still-running)", Group: "Filter"},
		{Name: "finished-before", Argument: "DATE", Desc: "Only runs whose FinishedAt <= this (excludes still-running)", Group: "Filter"},
		{Name: "limit", Argument: "N", Desc: "Max runs to show", Default: "20", Group: "Output"},
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty|json|plain", Group: "Output"},
		{Name: "quiet", Short: "q", Desc: "Print only run ids, one per line (or JSON array of ids with -o json)", Group: "Output"},
		{Name: "by-pipeline", Desc: "Pivot into one row per pipeline with a status sparkline of the last N runs", Group: "Output"},
		{Name: "sparkline", Argument: "N", Desc: "Sparkline length when --by-pipeline is set", Default: "30", Group: "Output"},
		{Name: "style", Argument: "STYLE", Desc: "Sparkline glyph style: ascii|block|dot", Default: "ascii", Group: "Output"},
		{Name: "profile", Argument: "NAME", Desc: "Profile name; omit for local-only", Group: "System"},
	},
	GroupOrder: []string{"Filter", "Output", "System", "Other"},
	Examples: []Example{
		{"Last 20 local runs", "sparkwing runs list"},
		{"Failed runs in the past day", "sparkwing runs list --status failed --since 24h"},
		{"Exclude success from the list", "sparkwing runs list --status '!success' --since 24h"},
		{"Runs on main, excluding canary", "sparkwing runs list --branch main --search '-canary'"},
		{"Runs that hit a specific failure", "sparkwing runs list --error 'permission denied'"},
		{"Runs finished today", "sparkwing runs list --finished-after today"},
		{"List prod runs", "sparkwing runs list --profile prod --limit 50"},
		{"By-pipeline rollup with sparkline", "sparkwing runs list --by-pipeline --since 7d"},
		{"By-pipeline JSON for an agent", "sparkwing runs list --by-pipeline -o json --since 24h"},
		{"Pipe the most recent run id into another verb", "sparkwing runs list --limit 1 -q | xargs -I{} sparkwing runs logs --run {}"},
	},
}

var cmdJobsStatus = Command{
	Path:     "sparkwing runs status",
	Synopsis: "Show one run's status (non-zero exit unless status=success)",
	Description: `Prints a summary of the run (pipeline, status, node states).
With --follow, polls until the run reaches a terminal status. Pass
--profile NAME to read from a remote controller.

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
without the shell redline. For a blocking wait, use 'runs wait'.`,
	Flags: []FlagSpec{
		{Name: "run", Argument: "RUN_ID", Desc: "Run identifier (e.g. run-20260422-142501-abcd)", Required: true, Group: "Input"},
		{Name: "follow", Short: "f", Desc: "Poll until the run reaches a terminal state", Group: "Output"},
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty|json|plain", Group: "Output"},
		{Name: "steps", Desc: "Render every step under every node (plain output). Failed / skipped / annotated nodes always include their steps; this flag forces success nodes too.", Group: "Output"},
		{Name: "exit-zero", Desc: "Return exit code 0 even when the run failed/cancelled", Group: "Output"},
		{Name: "profile", Argument: "NAME", Desc: "Profile name; omit for local-only", Group: "System"},
	},
	GroupOrder: []string{"Input", "Output", "System", "Other"},
	Examples: []Example{
		{"Check a local run once", "sparkwing runs status --run run-20260422-142501-abcd"},
		{"Follow a running job to completion", "sparkwing runs status --run run-... --follow"},
		{"Inspect a known-failed run without nonzero exit", "sparkwing runs status --run run-... --exit-zero"},
		{"Expand every step on every node", "sparkwing runs status --run run-... --steps"},
		{"Check a prod run", "sparkwing runs status --run run-... --profile prod"},
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
exclusive views of the unified stream.`,
	Flags: []FlagSpec{
		{Name: "run", Argument: "RUN_ID", Desc: "Run identifier", Required: true, Group: "Input"},
		{Name: "node", Argument: "NODE_ID", Desc: "Limit output to one node id", Group: "Filter"},
		{Name: "tail", Argument: "N", Desc: "Print only the last N lines", Group: "Filter"},
		{Name: "head", Argument: "N", Desc: "Print only the first N lines", Group: "Filter"},
		{Name: "lines", Argument: "A:B", Desc: "1-indexed inclusive line range", Group: "Filter"},
		{Name: "grep", Argument: "PATTERN", Desc: "Substring match (case-sensitive)", Group: "Filter"},
		{Name: "since", Argument: "DURATION", Desc: "Only include nodes that started within the last D (e.g. 5m, 1h)", Group: "Filter"},
		{Name: "tree", Desc: "Merge root + descendant runs into one stream (local only)", Group: "Filter"},
		{Name: "events-only", Desc: "Include event records and omit node body output", ConflictsWith: []string{"no-events"}, Group: "Filter"},
		{Name: "no-events", Desc: "Include node body output and omit event records", ConflictsWith: []string{"events-only"}, Group: "Filter"},
		{Name: "follow", Short: "f", Desc: "Tail the log(s) until the run terminates", Group: "Output"},
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty|json|plain", Group: "Output"},
		{Name: "profile", Argument: "NAME", Desc: "Profile name (omit for local-only reads)", Group: "System"},
	},
	GroupOrder: []string{"Input", "Filter", "Output", "System", "Other"},
	Examples: []Example{
		{"Read local logs", "sparkwing runs logs --run run-20260422-142501-abcd"},
		{"Last 20 lines of a remote run", "sparkwing runs logs --run run-... --profile prod --tail 20"},
		{"Only the most recent attempt's output", "sparkwing runs logs --run run-... --profile prod --since 5m"},
		{"Search logs for an error substring", "sparkwing runs logs --run run-... --grep 'permission denied'"},
		{"Merge a parent run with every descendant", "sparkwing runs logs --run run-... --tree"},
		{"Read only structured event records", "sparkwing runs logs --run run-... --events-only"},
		{"JSON stream for an agent", "sparkwing runs logs --run run-... -o json"},
		{"Plain text with node/step prefix", "sparkwing runs logs --run run-... -o plain"},
		{"Force the colored renderer when piping", "sparkwing runs logs --run run-... -o pretty | less -R"},
	},
}

var cmdJobsErrors = Command{
	Path:        "sparkwing runs errors",
	Synopsis:    "Surface the error trail for a failed run",
	Description: `Walks the run's node DAG and prints the error chain for any node that failed. Quicker than paging through full logs when you only care about the terminal failure. Reads from the local run store.`,
	Flags: []FlagSpec{
		{Name: "run", Argument: "RUN_ID", Desc: "Run identifier", Required: true, Group: "Input"},
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty|json|plain", Group: "Output"},
	},
	GroupOrder: []string{"Input", "Output", "System", "Other"},
	Examples: []Example{
		{"Inspect a local failure", "sparkwing runs errors --run run-20260422-142501-abcd"},
		{"As JSON", "sparkwing runs errors --run run-... -o json"},
	},
}

var cmdJobsFailures = Command{
	Path:        "sparkwing runs failures",
	Synopsis:    "List recent failed runs, optionally clustered",
	Description: `Fetches recent runs with status=failed and extracts the first failing node's step + error message for each. --group-by clusters the output by step so a systemic failure surfaces as one row with a count.`,
	Flags: []FlagSpec{
		{Name: "pipeline", Argument: "NAME", Desc: "Restrict to one pipeline", Group: "Filter"},
		{Name: "git-sha", Argument: "SHA", Desc: "Restrict to a git SHA prefix", Group: "Filter"},
		{Name: "branch", Argument: "NAME", Desc: "Restrict to one git branch", Group: "Filter"},
		{Name: "repo", Argument: "OWNER/NAME", Desc: "Restrict to one repository", Group: "Filter"},
		{Name: "since", Argument: "DURATION", Desc: "Only failures newer than this (e.g. 24h, 7d)", Group: "Filter"},
		{Name: "limit", Argument: "N", Desc: "Max failures to analyze", Default: "20", Group: "Filter"},
		{Name: "group-by", Argument: "KEY", Desc: "Cluster by: step | node", Group: "Output"},
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty|json|plain", Group: "Output"},
		{Name: "profile", Argument: "NAME", Desc: "Profile name; omit for local-only", Group: "System"},
	},
	GroupOrder: []string{"Filter", "Output", "System", "Other"},
	Examples: []Example{
		{"Recent local failures", "sparkwing runs failures --since 24h"},
		{"Prod failures clustered by step", "sparkwing runs failures --profile prod --group-by step"},
	},
}

var cmdJobsStats = Command{
	Path:     "sparkwing runs stats",
	Synopsis: "Aggregate run counts, success %, avg + p95 duration",
	Description: `Per-pipeline aggregates across the last 500 root runs (or the --since window). In-flight runs count toward RUN (running) but do not contribute to timing percentiles.

--capacity switches to the measured capacity profiles admission learns from: each pipeline's p50/p99 duration, its CPU and memory distributions (p50/p95/peak across recent runs), the CPU CHARGE column holding the core figure admission actually reserves, its queue-wait p50/p99, sample count, and whether the admission charge comes from a pin, measurement, or the cold-start default. The resource percentiles show whether a pipeline is steady or spiky. Admission charges memory from the peak, because under-reserving memory recreates the oversubscription admission exists to prevent, and charges cores from each run's sustained demand instead, because the kernel time-slices a transient CPU collision and reserving a burst peak for a whole run only refuses work the box could have run. A pipeline whose pin has drifted from its measured peaks carries the exact fix. Capacity profiles are local-only and repo-scoped for runs launched inside a git repo, so same-named pipelines in different repos never share a profile. The repo scope is the repository's canonical identity: host/owner/path from its origin remote, the object store it borrows from when it has no remote, or a private hash of a local-path remote. Every tree of one repository -- the main checkout, a linked worktree, a clone in an ephemeral directory -- therefore shares one profile: a pipeline costs what it costs whichever tree runs it, and a gate cloning into a fresh directory arrives already knowing its price. A repo with no remote at all keys by its directory name. The table prints each key as its repo scope and pipeline joined with "/".

--reset clears a pipeline's learned capacity profile so it re-learns from a cold start, the escape hatch for a poisoned measurement -- one freak run that recorded an absurd peak, or a contention-ratcheted demand floor (sparkwing doctor flags those). Name the pipeline with --pipeline NAME as --capacity shows it (repo/pipeline inside a git repo); a bare pipeline name resets every repo-scoped key that carries it, a repo/pipeline name reaches every stored encoding of that profile, and the summary names each profile it reached in the same repo/pipeline form. Reset every pipeline with --all --yes. The demand floor goes whether or not measured samples sit behind it, since a pipeline that never finished a clean run is still priced off its floor. An explicit .Resources() pin is preserved: admission keeps charging the pin while the profile re-learns. The command prints how many rows were dropped, how many pinned rows were cleared, and how many samples and demand floors were discarded.`,
	Flags: []FlagSpec{
		{Name: "pipeline", Argument: "NAME", Desc: "Restrict to one pipeline (required with --reset unless --all)", Group: "Filter"},
		{Name: "since", Argument: "DURATION", Desc: "Only runs newer than this (e.g. 7d)", Group: "Filter"},
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

var cmdJobsLast = Command{
	Path:        "sparkwing runs last",
	Synopsis:    "Print the most recent run",
	Description: `Shorthand for 'runs list --limit 1' with a compact one-line output. --watch tails for new runs, reprinting whenever a newer run ID appears.`,
	Flags: []FlagSpec{
		{Name: "pipeline", Argument: "NAME", Desc: "Restrict to one pipeline", Group: "Filter"},
		{Name: "watch", Short: "w", Desc: "Tail for new runs", Group: "Output"},
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty|json|plain", Group: "Output"},
		{Name: "profile", Argument: "NAME", Desc: "Profile name; omit for local-only", Group: "System"},
	},
	GroupOrder: []string{"Filter", "Output", "System", "Other"},
	Examples: []Example{
		{"Local last run", "sparkwing runs last"},
		{"Watch prod for new runs", "sparkwing runs last --profile prod --watch"},
	},
}

var cmdJobsTree = Command{
	Path:        "sparkwing runs tree",
	Synopsis:    "Show a run and every descendant run as an ASCII tree",
	Description: `Walks parent_run_id links so cross-pipeline spawns (RunAndAwait) show up under their originating run. Local mode reads from SQLite; --profile NAME reads from the profile's controller.`,
	Flags: []FlagSpec{
		{Name: "run", Argument: "RUN_ID", Desc: "Root run identifier", Required: true, Group: "Input"},
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty|json|plain", Group: "Output"},
		{Name: "profile", Argument: "NAME", Desc: "Profile name; omit for local-only", Group: "System"},
	},
	GroupOrder: []string{"Input", "Output", "System", "Other"},
	Examples: []Example{
		{"Tree for a local run", "sparkwing runs tree --run run-20260422-142501-abcd"},
		{"Tree for a prod run as JSON", "sparkwing runs tree --run run-... --profile prod -o json"},
	},
}

var cmdJobsGet = Command{
	Path:        "sparkwing runs get",
	Synopsis:    "Emit one run's raw JSON (run + nodes)",
	Description: `Prints a combined {run, nodes} JSON blob to stdout, plus a top-level log_path when the run wrote its logs to a filesystem (the directory on the machine that executed it). Consumed by agents and scripts that need the full store shape rather than the summary 'status' command renders.`,
	Flags: []FlagSpec{
		{Name: "run", Argument: "RUN_ID", Desc: "Run identifier", Required: true, Group: "Input"},
		{Name: "profile", Argument: "NAME", Desc: "Profile name; omit for local-only", Group: "System"},
	},
	GroupOrder: []string{"Input", "System", "Other"},
	Examples: []Example{
		{"Fetch a local run as JSON", "sparkwing runs get --run run-..."},
		{"Fetch a prod run", "sparkwing runs get --run run-... --profile prod"},
	},
}

var cmdJobsReceipt = Command{
	Path:     "sparkwing runs receipt",
	Synopsis: "Emit a run's audit + cost receipt as JSON",
	Description: `Recomputes the per-run receipt from the run + nodes
rows on demand and prints it as JSON. The receipt bundles identity
hashes (pipeline_version_hash, inputs_hash, plan_hash, per-node
outputs_hash), per-step observability (durations, outcomes), and
runner-time and compute-cost accounting.

inputs_hash is empty when the run carries a caller-supplied
secret:"true" argument, so the receipt cannot verify guesses of that value.

Local mode reads from the SQLite store and reports zero cost because no
local billing rate is configured. --profile NAME reads from the remote
controller's receipt endpoint and uses the controller's configured rate.`,
	Flags: []FlagSpec{
		{Name: "run", Argument: "RUN_ID", Desc: "Run identifier", Required: true, Group: "Input"},
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: json (default)", Group: "Output"},
		{Name: "profile", Argument: "NAME", Desc: "Profile name; omit for local-only", Group: "System"},
	},
	GroupOrder: []string{"Input", "Output", "System", "Other"},
	Examples: []Example{
		{"Local receipt as JSON", "sparkwing runs receipt --run run-..."},
		{"Prod receipt", "sparkwing runs receipt --run run-... --profile prod"},
	},
}

var cmdJobsWait = Command{
	Path:     "sparkwing runs wait",
	Synopsis: "Block until a run reaches a terminal status",
	Description: `Polls the run until its status is success / failed /
cancelled, then exits. Exit code contract:

  0   status == success
  1   status == failed or cancelled
  2   timed out before the run reached a terminal status
  3+  infrastructure error (controller unreachable, run not found, ...)

Pair with 'runs find --wait' for the "push then find then wait" loop
described in the CLI wishlist.`,
	Flags: []FlagSpec{
		{Name: "run", Argument: "RUN_ID", Desc: "Run identifier to wait on", Required: true, Group: "Input"},
		{Name: "timeout", Argument: "DURATION", Desc: "Give up (exit 2) after this long", Default: "10m", Group: "Input"},
		{Name: "poll", Argument: "DURATION", Desc: "Poll interval", Default: "3s", Group: "Input"},
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty|json|plain", Group: "Output"},
		{Name: "profile", Argument: "NAME", Desc: "Profile name (cluster mode). Omit to poll the local SQLite store.", Group: "System"},
	},
	GroupOrder: []string{"Input", "Output", "System", "Other"},
	Examples: []Example{
		{"Wait for a local run", "sparkwing runs wait --run run-20260422-142501-abcd"},
		{"Wait with a custom timeout", "sparkwing runs wait --run run-... --timeout 30m --profile prod"},
		{"Tight polling on a fast run", "sparkwing runs wait --run run-... --poll 500ms --profile prod"},
	},
}

var cmdJobsFind = Command{
	Path:     "sparkwing runs find",
	Synopsis: "Find runs by source identity or pipeline",
	Description: `Searches recent runs for a match. Use --git-sha to find
the run that was fired by a specific commit; add --pipeline to
disambiguate when multiple pipelines respond to the same push. --repo
matches the repository identity stored on the run (owner/name).

With --wait, blocks until at least one match appears, up to
--find-timeout. Pairs with 'runs wait' for the push-and-follow loop:

  git push && \
  sparkwing runs find --git-sha $(git rev-parse HEAD) --pipeline X --wait --profile prod -q | \
    xargs -n1 -I{} sparkwing runs wait --run {} --profile prod

Exit code 0 on match, non-zero on timeout-without-match or
infrastructure error.`,
	Flags: []FlagSpec{
		{Name: "git-sha", Argument: "SHA", Desc: "Match runs whose git SHA starts with this value (prefix match)", Group: "Filter"},
		{Name: "branch", Argument: "NAME", Desc: "Restrict to one git branch", Group: "Filter"},
		{Name: "pipeline", Argument: "NAME", Desc: "Restrict to one pipeline", Group: "Filter"},
		{Name: "repo", Argument: "OWNER/NAME", Desc: "Restrict to one stored repository identity", Group: "Filter"},
		{Name: "root-only", Desc: "Exclude child runs", Group: "Filter"},
		{Name: "since", Argument: "DURATION", Desc: "Lookback window", Default: "1h", Group: "Filter"},
		{Name: "limit", Argument: "N", Desc: "Max results", Default: "20", Group: "Filter"},
		{Name: "wait", Desc: "Block until at least one match appears", Group: "Output"},
		{Name: "find-timeout", Argument: "DURATION", Desc: "Give up (nonzero exit) after this long when --wait is set", Default: "2m", Group: "Output"},
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty|json|plain", Group: "Output"},
		{Name: "quiet", Short: "q", Desc: "Print only run ids, one per line (or a JSON array of ids with -o json)", Group: "Output"},
		{Name: "profile", Argument: "NAME", Desc: "Profile name (cluster mode). Omit to search the local SQLite store.", Group: "System"},
	},
	GroupOrder: []string{"Filter", "Output", "System", "Other"},
	Examples: []Example{
		{"Find a run by SHA + pipeline on prod", "sparkwing runs find --git-sha $(git rev-parse HEAD) --pipeline build-test-deploy --profile prod"},
		{"Block until the matching run appears", "sparkwing runs find --git-sha abc123 --pipeline X --wait --profile prod"},
		{"Pipe matching ids into runs wait", "sparkwing runs find --git-sha abc --wait -q --profile prod | xargs -n1 -I{} sparkwing runs wait --run {} --profile prod"},
	},
}

var cmdJobsGrep = Command{
	Path:     "sparkwing runs grep",
	Synopsis: "Search log bodies across recent runs for a substring",
	Description: `Walks the runs matching the filter set and substring-greps
every node's log. Reuses the same filter flags as ` + "`runs list`" + ` so
the candidate set is identical to what that verb would return.
In cluster mode the grep runs server-side per (run, node), so only
matching bytes come back over the wire.

Default output is a table of RUN / NODE / LINE / TEXT. -q
(quiet) prints just the unique matching run ids -- the usual
shape for piping into ` + "`runs logs`" + ` or ` + "`runs status`" + `.

Exit code 0 even when there are no matches.`,
	Flags: []FlagSpec{
		{Name: "pattern", Argument: "TEXT", Desc: "Substring to match (case-sensitive)", Required: true, Group: "Input"},
		{Name: "pipeline", Argument: "NAME", Desc: "Restrict candidate runs to one pipeline (repeatable; `!` to exclude)", Group: "Filter"},
		{Name: "status", Argument: "STATUS", Desc: "Restrict by status (repeatable; `!` to exclude)", Group: "Filter"},
		{Name: "branch", Argument: "BRANCH", Desc: "Restrict by git branch (repeatable; `!` to exclude)", Group: "Filter"},
		{Name: "sha", Argument: "PREFIX", Desc: "Restrict by git sha prefix (repeatable; `!` to exclude)", Group: "Filter"},
		{Name: "since", Argument: "DURATION", Desc: "Only runs newer than this", Group: "Filter"},
		{Name: "started-after", Argument: "DATE", Desc: "Only runs whose StartedAt >= this", Group: "Filter"},
		{Name: "started-before", Argument: "DATE", Desc: "Only runs whose StartedAt <= this", Group: "Filter"},
		{Name: "limit", Argument: "N", Desc: "Max candidate runs to scan", Default: "50", Group: "Output"},
		{Name: "max-matches", Argument: "M", Desc: "Per-node match cap (0 = no cap)", Default: "5", Group: "Output"},
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty|json|plain (default: pretty on TTY, json when piped)", Group: "Output"},
		{Name: "quiet", Short: "q", Desc: "Print only the unique matching run ids", Group: "Output"},
		{Name: "profile", Argument: "NAME", Desc: "Profile name; omit for local-only", Group: "System"},
	},
	GroupOrder: []string{"Input", "Filter", "Output", "System", "Other"},
	Examples: []Example{
		{"Find every run that hit a permission-denied line in the past week", "sparkwing runs grep --pattern 'permission denied' --since 7d"},
		{"Pipe matching run ids into runs logs", "sparkwing runs grep --pattern OOMKilled --since 24h -q | xargs -I{} sparkwing runs logs --run {}"},
		{"Search prod runs as JSON for an agent", "sparkwing runs grep --pattern 'connection refused' --profile prod --since 24h -o json"},
	},
}

var cmdJobsSummary = Command{
	Path:     "sparkwing runs summary",
	Synopsis: "Aggregated work view: groups, work items, modifiers, annotations",
	Description: `Run-level rollup of every node in one render. Mirrors the
dashboard's Summary tab: run header + run-wide annotations +
node groups + work items (nodes and inner steps) + modifiers
in effect + any approval-gate state. Useful for the
"did this run actually do what I asked" agent question.`,
	Flags: []FlagSpec{
		{Name: "run", Argument: "RUN_ID", Desc: "Run identifier", Required: true, Group: "Input"},
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty|json (default: pretty on TTY, json when piped)", Group: "Output"},
		{Name: "profile", Argument: "NAME", Desc: "Profile name; omit for local-only", Group: "System"},
	},
	GroupOrder: []string{"Input", "Output", "System", "Other"},
	Examples: []Example{
		{"Quick run rollup", "sparkwing runs summary --run run-..."},
		{"JSON for an agent", "sparkwing runs summary --run run-... -o json"},
	},
}

var cmdJobsTimeline = Command{
	Path:     "sparkwing runs timeline",
	Synopsis: "ASCII waterfall of nodes (and optional steps) for a run",
	Description: `Renders one row per node, laid out along the run's wall-clock
span. With --steps each node also expands into its inner Work
steps. Useful for an agent reasoning about parallelism and the
critical path without correlating logs by hand. JSON output
emits start/end offsets in milliseconds per row.`,
	Flags: []FlagSpec{
		{Name: "run", Argument: "RUN_ID", Desc: "Run identifier", Required: true, Group: "Input"},
		{Name: "steps", Desc: "Include per-step rows under each node", Group: "Output"},
		{Name: "width", Argument: "N", Desc: "Bar width in characters", Default: "60", Group: "Output"},
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty|json (default: pretty on TTY, json when piped)", Group: "Output"},
		{Name: "profile", Argument: "NAME", Desc: "Profile name; omit for local-only", Group: "System"},
	},
	GroupOrder: []string{"Input", "Output", "System", "Other"},
	Examples: []Example{
		{"Default node waterfall", "sparkwing runs timeline --run run-..."},
		{"Expand into per-step bars", "sparkwing runs timeline --run run-... --steps"},
		{"JSON for an agent", "sparkwing runs timeline --run run-... --steps -o json"},
	},
}

var cmdJobsRetry = Command{
	Path:     "sparkwing runs retry",
	Synopsis: "Trigger fresh runs copying pipeline + args from old ones",
	Description: `Issues a new trigger per source run with the same pipeline, args,
branch, and SHA. Each new run is tagged with retry_of=<old-id>.

On a local dashboard, the retry is bound to the source run's full origin
identity, Git revision, and complete plan snapshot. Sparkwing compiles and runs
an immutable detached snapshot of that recorded revision; uncommitted or later
working-tree edits are deliberately excluded. If the source checkout is gone or
any identity has drifted, the retry fails before compilation; it never falls
back to the current directory or another repo.

Pick a rerun scope explicitly:
  --failed   reuse cached/passed nodes from the source run;
             re-execute only the failed or unreached subset.
  --all      ignore prior outcomes and re-execute every node.

One of --failed or --all is required -- the silent default
caused operators to ship a partial rerun when they meant a full
one (and vice versa).

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
		{"Rerun only the failed nodes", "sparkwing runs retry --failed --run run-..."},
		{"Rerun every node from scratch", "sparkwing runs retry --all --run run-..."},
		{"Rerun every recently failed run", "sparkwing runs list --status failed --since 1h -q | sparkwing runs retry --failed --run - --profile prod"},
	},
}

var cmdJobsSubmit = Command{
	Path:     "sparkwing runs submit",
	Synopsis: "Queue a local run and return its id immediately",
	Description: `Submits PIPELINE for local execution and returns as soon as the
run is durable. Unlike 'sparkwing run', which executes in your
terminal and dies with it, a submitted run is owned by a resident
consumer process: close the terminal, drop the ssh session, log
out -- the run keeps going.

The acknowledgment is a run id and the directory its logs land
in. Address the run by that id afterwards:

  sparkwing runs status --run RUN_ID
  sparkwing runs logs   --run RUN_ID --follow
  sparkwing runs cancel --run RUN_ID

Everything after PIPELINE is passed to the pipeline as arguments, so
this command's own flags go BEFORE the pipeline name:

  sparkwing runs submit --idempotency-key k deploy --env staging

A submit flag typed after the pipeline name is refused rather than
quietly handed to the pipeline. If a pipeline declares a flag by the
same name, separate the two with '--':

  sparkwing runs submit deploy -- --request-id its-own

Flags that a detached run cannot honor (--sw-index, --sw-ref,
--sw-dry-run, --sw-only, --profile, and the other run-shaping
--sw- flags) are refused with the reason rather than ignored;
run those in the foreground with 'sparkwing run'.

Resolution order for PIPELINE: the checkout you are standing in
(or -C PATH) first, then the repo registry. The chosen checkout
is recorded on the run, so the consumer executes the tree you
submitted from even when another registered checkout declares the
same pipeline name.

Each submitted run executes with the environment captured by its
submission. The owner-only snapshot is removed when dispatch reaches
a terminal outcome; it is never stored in the run or trigger row.

Deduplication is opt-in via --idempotency-key, scoped to the
pipeline. A second submission of the SAME pipeline carrying a key
an earlier one used returns the original run id, its current
status, and creates nothing -- which is what makes a retry after a
dropped connection safe. Reusing a key with different arguments is
refused, because a key names one intent and different arguments
are a different request. --request-id is a separate, tracing-only
field: it is recorded on the run and never affects deduplication.

A consumer is started automatically if none is running, and exits
on its own after five idle minutes. See 'sparkwing runs consumer'.`,
	PosArgs: []PosArg{
		{Name: "pipeline", Desc: "Pipeline to run (see `sparkwing pipeline list`)", Required: true},
		{Name: "[args...]", Desc: "Arguments passed through to the pipeline"},
	},
	UsageSuffix: "<pipeline> [pipeline-flags...]",
	Flags: []FlagSpec{
		{Name: "idempotency-key", Argument: "KEY", Desc: "Deduplication token; a repeat submission with this key returns the original run", Group: "Input"},
		{Name: "request-id", Argument: "ID", Desc: "Tracing identifier recorded on the run; never affects deduplication", Group: "Input"},
		{Name: "cd", Short: "C", Argument: "PATH", Desc: "Resolve the pipeline from this directory instead of the current one", Group: "Target"},
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty|json|plain", Group: "Output"},
		{Name: "home", Argument: "PATH", Desc: "Sparkwing state directory (default: $SPARKWING_HOME or ~/.sparkwing)", Group: "System"},
		{Name: "consumer-idle", Argument: "DUR", Desc: "If this starts a consumer: how long it stays alive with no work (default 5m). A resident consumer keeps its own settings.", Group: "System"},
		{Name: "consumer-claim-lease", Argument: "DUR", Desc: "If this starts a consumer: the lease it stamps on each claimed run, renewed while the run executes (default 3m)", Group: "System"},
	},
	GroupOrder: []string{"Input", "Target", "Output", "System", "Other"},
	Examples: []Example{
		{"Submit a run and keep the id", "sparkwing runs submit nightly-report"},
		{"Submit with pipeline arguments", "sparkwing runs submit deploy --env staging"},
		{"Capture the id for scripting", "RUN=$(sparkwing runs submit -o plain build)"},
		{"Make a retry safe to repeat", "sparkwing runs submit --idempotency-key deploy-2026-08-11-a deploy"},
		{"Submit from another checkout", "sparkwing runs submit -C ~/code/other-project lint"},
	},
}

var cmdJobsConsumer = Command{
	Path:     "sparkwing runs consumer",
	Synopsis: "Inspect or control the process that executes submitted runs",
	Description: `One consumer process per sparkwing home claims queued triggers
and executes them. 'sparkwing runs submit' starts one when none
is running and the consumer exits after a quiet window, so these
verbs are for inspection and deliberate control rather than
routine use.

Exactly one consumer serves a home at a time, enforced by a file
lock rather than a PID check -- a consumer killed with SIGKILL
releases it immediately, so there is no stale state to clear. A
running dashboard consumes the same queue; whichever holds the
lock does the work and the other stands down, so a run is never
dispatched twice.

Stopping a consumer does not cancel queued runs. They stay
queued and execute when a consumer comes back. A run that is
executing when you stop it is interrupted and returned to the
queue, not failed -- it never reached a verdict, so the next
consumer re-executes it. To stop a run for good, cancel it.

A consumer records the sparkwing version it was built from. A
submission from a different build replaces it, so an upgrade takes
effect instead of the first build serving the home forever;
replacing one interrupts whatever it was executing, and that run
returns to the queue for the new consumer to re-execute.`,
	SubcommandOrder: []string{"start", "status", "stop"},
}

var cmdJobsConsumerStart = Command{
	Path:     "sparkwing runs consumer start",
	Synopsis: "Start a consumer for this home if none is running",
	Description: `Starts the resident trigger consumer and waits until it owns the
home's queue. A no-op when one is already running.

Rarely needed by hand: 'sparkwing runs submit' does this before
it acknowledges a run.`,
	Flags: []FlagSpec{
		{Name: "home", Argument: "PATH", Desc: "Sparkwing state directory (default: $SPARKWING_HOME or ~/.sparkwing)", Group: "System"},
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
		{Name: "home", Argument: "PATH", Desc: "Sparkwing state directory (default: $SPARKWING_HOME or ~/.sparkwing)", Group: "System"},
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
comes back, which the next 'sparkwing runs submit' arranges.

To cancel a queued run instead, use 'sparkwing runs cancel'.`,
	Flags: []FlagSpec{
		{Name: "home", Argument: "PATH", Desc: "Sparkwing state directory (default: $SPARKWING_HOME or ~/.sparkwing)", Group: "System"},
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
from stdin, one per line.`,
	Flags: []FlagSpec{
		{Name: "run", Argument: "RUN_ID", Desc: "Run id to cancel (repeatable; use --run - to read ids from stdin)", Group: "Input"},
		{Name: "profile", Argument: "NAME", Desc: "Profile name for remote runs; omit for local runs", Group: "System"},
		{Name: "home", Argument: "DIR", Desc: "Sparkwing home for local daemon and queued-run storage (default: $SPARKWING_HOME or ~/.sparkwing)", Group: "System"},
	},
	GroupOrder: []string{"Input", "System", "Other"},
	Examples: []Example{
		{"Cancel one run", "sparkwing runs cancel --run run-... --profile prod"},
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
again is allowed -- one request is one restart.`,
	Flags: []FlagSpec{
		{Name: "run", Argument: "RUN_ID", Desc: "Run id owning the job", Group: "Input"},
		{Name: "node", Argument: "NODE_ID", Desc: "Job id to bounce", Group: "Input"},
		{Name: "profile", Argument: "NAME", Desc: "Profile name for remote runs; omit for local runs", Group: "System"},
		{Name: "home", Argument: "DIR", Desc: "Sparkwing home holding the run (default: $SPARKWING_HOME or ~/.sparkwing)", Group: "System"},
	},
	GroupOrder: []string{"Input", "System", "Other"},
	Examples: []Example{
		{"Bounce a wedged job in a local run", "sparkwing runs bounce --run run-... --node build"},
		{"Bounce a job in a cluster run", "sparkwing runs bounce --run run-... --node build --profile prod"},
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

Use --dry-run first to confirm the victim list.`,
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

Managed hooks carry a "Installed by sparkwing" marker so
uninstall and status can tell them apart from hand-written
hooks. Existing unmanaged hooks are left alone; install skips
them with a warning.`,
	SubcommandOrder: []string{"install", "uninstall", "status", "survey", "fire"},
}

var cmdHooksInstall = Command{
	Path:     "sparkwing pipeline hooks install",
	Synopsis: "Install pre-commit / pre-push / post-commit git hooks from sparkwing.yaml triggers",
	Description: `Discovers the enclosing .sparkwing/sparkwing.yaml, reads
pre_commit / pre_push / post_commit triggers, and writes one hook
file per hook name that fans out to the matching pipelines. Existing
non-sparkwing hooks are skipped so hand-written ones survive.

Before a gate can fire, install runs it once. While a repo's hooks are inert
a gate that cannot execute looks the same as one that passes, and arming it
turns every commit into a failing one. Every proof finishes before candidate
hook filenames or core.hooksPath are published, so prior hooks remain callable
throughout a proof and unchanged if it fails. Complete replacements publish by
atomic rename; a later installation error restores every prior managed hook,
global-hook forwarder, file mode, and config value. No partial set is armed.
--no-prove arms anyway.

Hooks installed without --profile prove and run their pipelines with
--sw-local-only. Pass --profile NAME when the gate should use shared storage.

--fleet counts as armed only the repos a gate now fires in. A repo whose gates
could not run is named as left ungated, and one that declares no pre_commit or
pre_push trigger is counted apart: nothing there can refuse a commit, so there
was never a gate to arm.`,
	Flags: []FlagSpec{
		{Name: "repo", Argument: "DIR", Desc: "Repo directory (default: discovered via nearest .sparkwing/)", Group: "Input"},
		{Name: "fleet", Desc: "Install into every registered repo instead of one", Group: "Input"},
		{Name: "no-prove", Desc: "Claim core.hooksPath without running the gate first", Group: "Behavior"},
		{Name: "profile", Argument: "NAME", Desc: "Pin the hook's runs to this storage profile (default: local-only)", Group: "Storage"},
	},
	Examples: []Example{
		{"Install in the current repo", "sparkwing pipeline hooks install"},
		{"Install in a different repo", "sparkwing pipeline hooks install --repo /path/to/repo"},
		{"Arm every registered repo", "sparkwing pipeline hooks install --fleet"},
		{"Pin the gate's runs to one store", "sparkwing pipeline hooks install --profile bucket"},
	},
}

var cmdHooksSurvey = Command{
	Path:     "sparkwing pipeline hooks survey",
	Synopsis: "Report which registered repos git actually runs a gate for",
	Description: `Classifies every repo in the local registry by what git does
with the hooks its pipelines declare: armed (a gate runs), shadowed (a gate is
installed but core.hooksPath sends git elsewhere), uninstalled (a declared
hook was never written), or undeclared (no pipeline asks for one).

The repos are the ones repos.yaml reaches: what sparkwing configure xrepo add
registered, plus any fallback_paths it scans. A checkout it does not list is
not surveyed, so register it before reading a clean survey as a clean
machine.

A registry it cannot read is an error, not an empty fleet: the survey names
the file and exits non-zero rather than printing the output of a machine with
nothing registered.

--ungated lists the repos a commit or a push goes through unchecked in. Only
pre-commit and pre-push count, since a post-commit hook runs after the commit
has landed and cannot refuse one.`,
	Flags: []FlagSpec{
		{Name: "output", Short: "o", Argument: "FMT", Desc: "Output format: pretty|json|plain", Group: "Output"},
		{Name: "ungated", Desc: "List only the repos git runs no gate for", Group: "Output"},
	},
	Examples: []Example{
		{"Survey the fleet", "sparkwing pipeline hooks survey"},
		{"Just the ungated repos", "sparkwing pipeline hooks survey --ungated"},
		{"Machine-readable", "sparkwing pipeline hooks survey -o json"},
	},
}

var cmdHooksFire = Command{
	Path:     "sparkwing pipeline hooks fire",
	Synopsis: "Make the gate refuse a commit, to see that it can",
	Description: `Stages a file and commits it with the gate told to refuse, then
reports whether git refused the commit and which hook file did it.

A hook directory cannot answer this. A repo whose core.hooksPath points at a
sibling's hooks holds no gate of its own and refuses commits anyway; a repo
whose hooks are shadowed holds a full set and refuses nothing. Both inspect as
something they are not, so survey and status report what is installed and this
reports what happens.

The attempt runs in a throwaway linked worktree with its own index and its own
detached HEAD, so the repo's working tree, index, branches and HEAD are
untouched whatever the gate does. Only a hook sparkwing wrote that carries the
self-test guard is ever executed -- anything else is reported as unprovable
rather than run, because answering a diagnostic question is not a reason to run
an operator's hook.

Every refusal is checked against a control: the same staged change is committed
again with hooks switched off and has to land, so an unrelated failure is not
read as a gate doing its job.

Exits non-zero unless every repo refused the commit with a gate of its own. A
repo that declares no pre-commit trigger has no gate to fire and does not
count. Pre-push gates are not covered -- firing one needs a remote.`,
	Flags: []FlagSpec{
		{Name: "repo", Argument: "DIR", Desc: "Repo directory (default: discovered via nearest .sparkwing/)", Group: "Input"},
		{Name: "fleet", Desc: "Fire the gate in every registered repo instead of one", Group: "Input"},
		{Name: "output", Short: "o", Argument: "FMT", Desc: "Output format: pretty|json|plain", Group: "Output"},
	},
	Examples: []Example{
		{"Prove this repo's gate refuses a commit", "sparkwing pipeline hooks fire"},
		{"Prove every registered repo's gate", "sparkwing pipeline hooks fire --fleet"},
		{"Machine-readable", "sparkwing pipeline hooks fire -o json"},
	},
}

var cmdHooksUninstall = Command{
	Path:        "sparkwing pipeline hooks uninstall",
	Synopsis:    "Remove sparkwing-managed git hooks",
	Description: `Deletes every file under .git/hooks/ that carries the "Installed by sparkwing" marker. Hand-written hooks are left alone.`,
	Flags: []FlagSpec{
		{Name: "repo", Argument: "DIR", Desc: "Repo directory (default: discovered via nearest .sparkwing/)", Group: "Input"},
	},
	Examples: []Example{
		{"Uninstall in the current repo", "sparkwing pipeline hooks uninstall"},
	},
}

var cmdHooksStatus = Command{
	Path:        "sparkwing pipeline hooks status",
	Synopsis:    "Report declared, installed, and missing sparkwing hooks",
	Description: `Lists every managed hook file under .git/hooks/ along with the pipelines it invokes. Declared hooks that are missing, shadowed, or borrowed are named with the command that repairs them.`,
	Flags: []FlagSpec{
		{Name: "repo", Argument: "DIR", Desc: "Repo directory (default: discovered via nearest .sparkwing/)", Group: "Input"},
	},
	Examples: []Example{
		{"Show hook status", "sparkwing pipeline hooks status"},
	},
}

var cmdSecret = Command{
	Path:     "sparkwing secrets",
	Synopsis: "Manage secrets (local dotenv or controller-stored)",
	Description: `Without --profile, reads/writes the laptop dotenv at
~/.config/sparkwing/secrets.env (masked) or
~/.config/sparkwing/config.env (--plain). Used by jobs invoked
through 'sparkwing run <pipeline>' locally.

With --profile PROF, reads/writes the named profile's controller.
Used for prod / staging secrets that the cluster needs at run
time. Pipelines pull a secret by listing it in the
sparkwing.yaml 'secrets:' block. Raw values never transit the
CLI except via 'secrets get'.`,
	SubcommandOrder: []string{"set", "get", "list", "delete"},
}

var cmdSecretSet = Command{
	Path:     "sparkwing secrets set",
	Synopsis: "Store (or replace) a secret value",
	Description: `Stores --value (or the contents of --file) in the local secret
files when --profile is omitted, or uploads it to the named profile's
controller. Replaces any existing secret with that name.
Prefer --file for long or multi-line values so the raw text
does not land in shell history.`,
	Flags: []FlagSpec{
		{Name: "name", Type: FlagString, Argument: "NAME", Desc: "Secret name (unique per controller)", Required: true, Group: "Input"},
		{Name: "value", Type: FlagString, Argument: "VALUE", Desc: "Secret value (prefer --file for long values)", RequiredWhen: "when --file is not set", ConflictsWith: []string{"file"}, Group: "Input"},
		{Name: "file", Type: FlagString, Argument: "PATH", Desc: "Read value from file (keeps value out of shell history)", RequiredWhen: "when --value is not set", ConflictsWith: []string{"value"}, Group: "Input"},
		{Name: "plain", Type: FlagBool, Desc: "Store as non-masked config (e.g. REGION, LOG_LEVEL) -- value will NOT be redacted in run logs. Default is masked.", Group: "Input"},
		{Name: "profile", Type: FlagString, Argument: "NAME", Desc: "Profile name (omit for local files)", Group: "System"},
	},
	GroupOrder: []string{"Input", "System", "Other"},
	Examples: []Example{
		{"Set a local masked secret", "sparkwing secrets set --name API_TOKEN --value abc123"},
		{"Set from a file", "sparkwing secrets set --name TLS_CERT --file ./tls.crt --profile prod"},
		{"Set non-masked config", "sparkwing secrets set --name REGION --value us-east-1 --plain --profile prod"},
	},
}

var cmdSecretGet = Command{
	Path:     "sparkwing secrets get",
	Synopsis: "Print a secret's raw value to stdout",
	Description: `Reads local secret files when --profile is omitted, or the
named profile's controller. Prints only the raw value (no trailing newline)
so it can be piped into another command. Use 'secrets list' for metadata.`,
	Flags: []FlagSpec{
		{Name: "name", Type: FlagString, Argument: "NAME", Desc: "Secret name", Required: true, Group: "Input"},
		{Name: "profile", Type: FlagString, Argument: "NAME", Desc: "Profile name (omit for local files)", Group: "System"},
	},
	GroupOrder: []string{"Input", "System", "Other"},
	Examples: []Example{
		{"Fetch a local secret", "sparkwing secrets get --name API_TOKEN"},
		{"Fetch a remote secret", "sparkwing secrets get --name API_TOKEN --profile prod"},
	},
}

var cmdSecretList = Command{
	Path:        "sparkwing secrets list",
	Synopsis:    "List secret names + metadata",
	Description: `Lists secret names and metadata from local files when --profile is omitted, or from the named profile's controller. Raw values are never printed by this command.`,
	Flags: []FlagSpec{
		{Name: "grep", Type: FlagString, Argument: "PATTERN", Desc: "Filter by name substring (case-sensitive)", Group: "Filter"},
		{Name: "profile", Type: FlagString, Argument: "NAME", Desc: "Profile name (omit for local files)", Group: "System"},
	},
	GroupOrder: []string{"Filter", "System", "Other"},
	Examples: []Example{
		{"List local secrets", "sparkwing secrets list"},
		{"List secrets on prod", "sparkwing secrets list --profile prod"},
		{"Filter to API-related names", "sparkwing secrets list --profile prod --grep API"},
	},
}

var cmdSecretDelete = Command{
	Path:        "sparkwing secrets delete",
	Synopsis:    "Remove a secret",
	Description: `Deletes the secret from local files when --profile is omitted, or from the named profile's controller. Pipelines that reference the name will fail to resolve until the secret is re-added.`,
	Flags: []FlagSpec{
		{Name: "name", Type: FlagString, Argument: "NAME", Desc: "Secret name to remove", Required: true, Group: "Input"},
		{Name: "profile", Type: FlagString, Argument: "NAME", Desc: "Profile name (omit for local files)", Group: "System"},
	},
	GroupOrder: []string{"Input", "System", "Other"},
	Examples: []Example{
		{"Delete a local secret", "sparkwing secrets delete --name API_TOKEN"},
		{"Delete a remote secret", "sparkwing secrets delete --name API_TOKEN --profile prod"},
	},
}

var cmdTriggers = Command{
	Path:     "sparkwing runs triggers",
	Synopsis: "Fire, list, or inspect controller triggers",
	Description: `Triggers are the controller's queue of pending work. Every
pipeline run starts as a trigger (from a webhook, hook, 'sparkwing
run --profile', or 'triggers fire') that a worker atomically claims and
turns into a run.

'fire' posts a synthetic trigger -- the sparkwing equivalent of
'gh workflow run'. 'list' surfaces queued / in-flight / done
entries so operators can see what's stuck without diving into
controller logs. 'get' inspects one trigger by id.

Connection info comes from the selected profile (--profile NAME);
there are no --controller / --token flags on this command.`,
	SubcommandOrder: []string{"list", "get"},
	Examples: []Example{
		{"List pending triggers on prod", "sparkwing runs triggers list --profile prod --status pending"},
		{"Inspect one trigger", "sparkwing runs triggers get --id run-... --profile prod"},
		{"Fire a trigger (use pipeline run)", "sparkwing pipeline run --pipeline deploy --profile prod"},
	},
}

var cmdTriggersList = Command{
	Path:     "sparkwing runs triggers list",
	Synopsis: "List pending / claimed / done triggers",
	Description: `Queries GET /api/v1/triggers on the selected profile's
controller. Empty filters return the most recent 20 entries
across all statuses.

Useful when the queue looks stuck ("why isn't my trigger being
claimed?"): --status pending shows unclaimed work, --status
claimed shows what a worker has in-flight. The repo filter
matches GITHUB_REPOSITORY on the trigger env so webhook-driven
entries narrow cleanly.`,
	Flags: []FlagSpec{
		{Name: "status", Argument: "STATUS", Desc: "Filter by status: pending | claimed | done", Group: "Filter"},
		{Name: "pipeline", Argument: "NAME", Desc: "Filter by pipeline name", Group: "Filter"},
		{Name: "repo", Argument: "OWNER/NAME", Desc: "Match GITHUB_REPOSITORY on the trigger env", Group: "Filter"},
		{Name: "limit", Argument: "N", Desc: "Max triggers to show", Default: "20", Group: "Output"},
		{Name: "quiet", Short: "q", Desc: "Print only trigger ids, newline-separated", Group: "Output"},
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: json emits the raw triggers array", Group: "Output"},
		{Name: "profile", Argument: "NAME", Desc: "Profile name", Required: true, Group: "System"},
	},
	GroupOrder: []string{"Filter", "Output", "System", "Other"},
	Examples: []Example{
		{"Recent triggers on prod", "sparkwing runs triggers list --profile prod"},
		{"Just pending", "sparkwing runs triggers list --profile prod --status pending"},
		{"Pipeline-specific, JSON", "sparkwing runs triggers list --profile prod --pipeline build-test-deploy --limit 5 -o json"},
	},
}

var cmdTriggersGet = Command{
	Path:        "sparkwing runs triggers get",
	Synopsis:    "Inspect one trigger's full metadata by id",
	Description: `Fetches GET /api/v1/triggers/{id} and prints the full row (pipeline, args, git, env, status, claim lease). Defaults to a compact multi-line rendering; -o json emits the raw response.`,
	Flags: []FlagSpec{
		{Name: "id", Argument: "TRIGGER_ID", Desc: "Trigger / run identifier (same value 'fire' prints)", Required: true, Group: "Input"},
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: json emits the raw response", Group: "Output"},
		{Name: "profile", Argument: "NAME", Desc: "Profile name", Required: true, Group: "System"},
	},
	GroupOrder: []string{"Input", "Output", "System", "Other"},
	Examples: []Example{
		{"Inspect one trigger", "sparkwing runs triggers get --id run-20260422-142501-abcd --profile prod"},
		{"Raw JSON for scripting", "sparkwing runs triggers get --id run-... --profile prod -o json"},
	},
}

var cmdImage = Command{
	Path:     "sparkwing cluster image",
	Synopsis: "Rollout helpers for images referenced by a gitops repo",
	Description: `Composite verbs that operate on the images: block of a
kustomization.yaml plus the downstream ArgoCD / kubectl dance.
Building and pushing images stays with the consumer pipeline --
this subcommand only owns the "bump tag, commit, push, sync,
wait for rollout" path.`,
	SubcommandOrder: []string{"rollout"},
	Examples: []Example{
		{"Bump sparkwing-runner to a new commit tag", "sparkwing cluster image rollout --image sparkwing-runner --tag commit-abc123 --wait"},
	},
}

var cmdImageRollout = Command{
	Path:     "sparkwing cluster image rollout",
	Synopsis: "Bump a kustomization image tag, commit+push, sync ArgoCD, optionally wait",
	Description: `Rewrites the newTag: field for the image whose entry in the
gitops repo's kustomization.yaml matches --image (suffix match
against the ECR / registry URL), commits + pushes the change,
optionally triggers an ArgoCD sync, and optionally blocks on
kubectl rollout status.

Gitops repo resolution order:
  1. --gitops-repo PATH explicit flag
  2. SPARKWING_GITOPS_REPO explicit environment configuration

If neither is set, rollout exits before reading or changing a repository.
Sparkwing never guesses a path from the user's home-directory layout.

The command is idempotent: if the newTag already matches --tag
there is nothing to commit, and the pipeline continues to sync
+ wait without error. Use --dry-run to preview the plan without
writing, committing, pushing, syncing, or waiting.

Optional tools are skipped cleanly when absent from PATH:
  - argocd missing  -> sync is skipped with a one-line notice
  - kubectl missing -> --wait / --tail-logs error before side effects

This verb does NOT build or push the image itself. The consumer
pipeline that produced --tag is responsible for publishing the
image to the registry before calling rollout.`,
	Flags: []FlagSpec{
		{Name: "image", Argument: "NAME", Desc: "Short image name (matches the suffix of the ECR URL)", Required: true, Group: "Input"},
		{Name: "tag", Argument: "TAG", Desc: "New tag to write in kustomization.yaml", Required: true, Group: "Input"},
		{Name: "gitops-repo", Argument: "PATH", Desc: "Gitops repo path (or SPARKWING_GITOPS_REPO)", Group: "Input"},
		{Name: "namespace", Argument: "NS", Desc: "Kubernetes namespace for rollout status + logs", Default: "sparkwing", Group: "Input"},
		{Name: "argocd-app", Argument: "NAME", Desc: "ArgoCD app name (default: derived from --image)", Group: "Input"},
		{Name: "message", Argument: "MSG", Desc: "Commit message (default: 'chore: bump <image> to <tag>')", Group: "Input"},
		{Name: "wait", Desc: "Block until 'kubectl rollout status deployment/<image>' returns", Group: "Toggles"},
		{Name: "tail-logs", Desc: "After rollout, 'kubectl logs -f -l app=<image>' until ctrl-c", Group: "Toggles"},
		{Name: "dry-run", Desc: "Print what would happen without writing, committing, pushing, or syncing", Group: "Toggles"},
	},
	GroupOrder: []string{"Input", "Toggles", "System", "Other"},
	Examples: []Example{
		{"Dry-run against the sparkwing-runner image", "sparkwing cluster image rollout --image sparkwing-runner --tag commit-abc123 --dry-run"},
		{"Bump and wait for the rollout", "sparkwing cluster image rollout --image sparkwing-runner --tag commit-abc123 --wait"},
		{"Bump, sync, wait, then tail pod logs", "sparkwing cluster image rollout --image sparkwing --tag commit-abc123 --wait --tail-logs"},
	},
}

var cmdProfilesTest = Command{
	Path:     "sparkwing configure profiles test",
	Synopsis: "Probe controller/auth/logs/gitcache for one profile",
	Description: `Sequentially checks the profile's controller (/api/v1/health),
auth (/api/v1/runs?limit=1 + /api/v1/auth/whoami), logs
service (if configured), and gitcache (if configured). Each
probe prints ok / warn / fail along with latency and any
error detail.

Exit code is non-zero when any probe fails. Missing optional
services (logs, gitcache) count as warn, not fail, so a
minimally-configured laptop profile can still exit 0.`,
	Flags: []FlagSpec{
		{Name: "profile", Argument: "NAME", Desc: "Profile name", Required: true, Group: "System"},
		{Name: "output", Short: "o", Argument: "FMT", Desc: "Output format (json|table)", Group: "Output"},
	},
	GroupOrder: []string{"Output", "System", "Other"},
	Examples: []Example{
		{"Probe a named profile", "sparkwing configure profiles test --profile prod"},
		{"JSON for scripting", "sparkwing configure profiles test --profile prod -o json"},
	},
}

var cmdHealth = Command{
	Path:     "sparkwing cluster status",
	Synopsis: "Connectivity + fleet + queue health check against a remote cluster",
	Description: `Answers "is this cluster alive?" in one command. Runs the
connectivity / auth probes from 'profiles test' plus cluster-
state probes that hit /api/v1/agents, /api/v1/pool,
/api/v1/triggers (status=claimed), and /api/v1/runs?since=24h.

Sections:

  CONNECTIVITY  controller / auth / logs / gitcache
  FLEET         agents (connected vs stale) + warm-runner pool
  QUEUE         stuck triggers + recent-run success rate

Exit 0 when every probe is ok or warn; exit 1 when any probe
fails (auth reject, controller down, HTTP 5xx). Warnings are
informational -- low success rate, empty pool, stale agents --
and don't change the exit code so scripts can still condition
on "is the cluster reachable at all?".`,
	Flags: []FlagSpec{
		{Name: "profile", Argument: "NAME", Desc: "Profile name", Required: true, Group: "System"},
		{Name: "output", Short: "o", Argument: "FMT", Desc: "Output format: pretty|json", Group: "Output"},
	},
	GroupOrder: []string{"Output", "System", "Other"},
	Examples: []Example{
		{"Quick-check prod", "sparkwing cluster status --profile prod"},
		{"Structured output for a status dashboard", "sparkwing cluster status --profile prod -o json"},
	},
}

var cmdWebhooks = Command{
	Path:     "sparkwing cluster webhooks",
	Synopsis: "Inspect and replay GitHub webhooks",
	Description: `Sparkwing-aware wrapper over the GitHub hooks API. Shells out
to 'gh api' (inherits your gh auth); install gh from
https://cli.github.com if it isn't on PATH.

Value-add over 'gh api' alone: the deliveries view joins
GitHub's delivery log with sparkwing's trigger/run rows so
each delivery shows the run id it produced and the run's
terminal status -- without two separate lookups.`,
	SubcommandOrder: []string{"list", "deliveries", "replay"},
	Examples: []Example{
		{"List hooks on a repo", "sparkwing cluster webhooks list --repo your-org/my-app"},
		{"Recent deliveries for a hook", "sparkwing cluster webhooks deliveries --repo your-org/my-app --hook 608819334 --since 1h --profile prod"},
	},
}

var cmdWebhooksList = Command{
	Path:     "sparkwing cluster webhooks list",
	Synopsis: "List GitHub hooks configured on a repo",
	Description: `Calls 'gh api /repos/OWNER/NAME/hooks' and prints id, derived
pipeline, active flag, last-delivery status, and URL.

The PIPELINE column is parsed from the hook URL path
(/webhooks/github/<pipeline>). Hooks posting to the older
unscoped /webhooks/github endpoint render as "(unscoped)"
so operators can spot them for cleanup. Non-sparkwing hooks
render as "(non-sparkwing)".`,
	Flags: []FlagSpec{
		{Name: "repo", Argument: "OWNER/NAME", Desc: "GitHub repo (owner can be omitted if gh has a default)", Required: true, Group: "Input"},
		{Name: "output", Short: "o", Argument: "FMT", Desc: "Output format (json|table)", Group: "Output"},
	},
	GroupOrder: []string{"Input", "Output", "System", "Other"},
	Examples: []Example{
		{"List hooks on a repo", "sparkwing cluster webhooks list --repo your-org/my-app"},
	},
}

var cmdWebhooksDeliveries = Command{
	Path:     "sparkwing cluster webhooks deliveries",
	Synopsis: "List recent deliveries for a hook, joined with trigger state",
	Description: `Fetches recent deliveries via 'gh api' and, for each one,
looks up the matching sparkwing trigger by GITHUB_DELIVERY env
stamp. Surfaces TRIGGER_ID + RUN_STATUS columns so operators
see GitHub-side status alongside the run it produced.

--since filters deliveries client-side (GitHub's API does not
take a time filter). Default: 24h.`,
	Flags: []FlagSpec{
		{Name: "repo", Argument: "OWNER/NAME", Desc: "GitHub repo", Required: true, Group: "Input"},
		{Name: "hook", Argument: "N", Desc: "GitHub hook id from 'webhooks list'", Required: true, Group: "Input"},
		{Name: "since", Argument: "DURATION", Desc: "Only deliveries newer than this", Default: "24h", Group: "Filter"},
		{Name: "output", Short: "o", Argument: "FMT", Desc: "Output format (json|table)", Group: "Output"},
		{Name: "profile", Argument: "NAME", Desc: "Profile name (used for trigger/run lookups)", Required: true, Group: "System"},
	},
	GroupOrder: []string{"Input", "Filter", "Output", "System", "Other"},
	Examples: []Example{
		{"Recent deliveries for a hook", "sparkwing cluster webhooks deliveries --repo your-org/my-app --hook 608819334 --since 1h --profile prod"},
	},
}

var cmdWebhooksReplay = Command{
	Path:     "sparkwing cluster webhooks replay",
	Synopsis: "Queue a redelivery of a specific delivery UUID",
	Description: `POSTs /repos/OWNER/NAME/hooks/HOOK/deliveries/DELIVERY/attempts
to GitHub. GitHub queues a fresh attempt; the new delivery
appears in the hook's delivery log within seconds.`,
	Flags: []FlagSpec{
		{Name: "repo", Argument: "OWNER/NAME", Desc: "GitHub repo", Required: true, Group: "Input"},
		{Name: "hook", Argument: "N", Desc: "GitHub hook id", Required: true, Group: "Input"},
		{Name: "delivery", Argument: "UUID", Desc: "Delivery GUID to redeliver", Required: true, Group: "Input"},
	},
	GroupOrder: []string{"Input", "System", "Other"},
	Examples: []Example{
		{"Redeliver a webhook attempt", "sparkwing cluster webhooks replay --repo your-org/my-app --hook 608819334 --delivery 0ac55946-3e96-11f1-9de8-f33e32f0060f"},
	},
}

var cmdAgents = Command{
	Path:     "sparkwing cluster agents",
	Synopsis: "Inspect the controller's fleet view",
	Description: `Hits GET /api/v1/agents on the selected profile's controller.
Prints one row per agent seen claiming work in the last hour
(the controller infers agents from recent node claims; there
is no explicit registration table yet).`,
	SubcommandOrder: []string{"list"},
	Examples: []Example{
		{"List prod agents", "sparkwing cluster agents list --profile prod"},
	},
}

var cmdAgentsList = Command{
	Path:     "sparkwing cluster agents list",
	Synopsis: "Print the controller's known agents",
	Description: `Fetches /api/v1/agents and renders a table of fleet members.
The controller infers agents from node claims over the last
hour, so idle agents without any recent claim activity won't
show up -- a known limitation until we add explicit heartbeats.

Use -q to print just names, one per line, for shell piping
(e.g. looping over agents with xargs).`,
	Flags: []FlagSpec{
		{Name: "profile", Argument: "NAME", Desc: "Profile name", Required: true, Group: "System"},
		{Name: "output", Short: "o", Argument: "FMT", Desc: "Output format (json|table)", Group: "Output"},
		{Name: "quiet", Short: "q", Desc: "Print just agent names, one per line", Group: "Output"},
	},
	GroupOrder: []string{"Output", "System", "Other"},
	Examples: []Example{
		{"List agents on prod", "sparkwing cluster agents list --profile prod"},
		{"Just agent names for piping", "sparkwing cluster agents list --profile prod -q"},
	},
}

var cmdClusterConcurrency = Command{
	Path:     "sparkwing cluster concurrency",
	Synopsis: "Inspect a single concurrency namespace: holders + queue",
	Description: `Shows who currently holds a concurrency namespace's slots
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

var cmdSparks = Command{
	Path:     "sparkwing pipeline sparks",
	Synopsis: "Manage sparks libraries declared in .sparkwing/sparks.yaml",
	Description: `Sparks libraries are Go modules that add opinionated helpers
(Docker builds, GitOps deploys, ECR auth, language-specific
checks) on top of the unopinionated SDK. Consumers declare
which libraries they want live-tracked in
.sparkwing/sparks.yaml; the resolver writes an overlay modfile
at .sparkwing/.resolved.mod that the compile step uses via
'go build -modfile='. The consumer's git-tracked go.mod is
never modified.

See docs/sparks.md for the full spec (spark.json schema,
sparks.yaml shape, resolution rules, warmup).`,
	SubcommandOrder: []string{"list", "lint", "resolve", "update", "add", "remove", "warmup", "inflate"},
	Examples: []Example{
		{"List declared sparks libraries", "sparkwing pipeline sparks list"},
		{"Validate a library's spark.json", "sparkwing pipeline sparks lint ~/code/sparks-core"},
		{"Re-materialize the overlay modfile", "sparkwing pipeline sparks resolve"},
		{"Add a library pinned to latest", "sparkwing pipeline sparks add github.com/sparkwing-dev/sparks-core"},
	},
}

var cmdSparksList = Command{
	Path:     "sparkwing pipeline sparks list",
	Synopsis: "Show declared sparks libraries and their resolved versions",
	Description: `Reads .sparkwing/sparks.yaml and prints one row per declared
library with its declared constraint and the resolved tag
(found via the module proxy). Use --no-resolve to skip the
proxy calls when offline.`,
	Flags: []FlagSpec{
		{Name: "sparkwing-dir", Argument: "DIR", Desc: "Path to .sparkwing/ (default: <cwd>/.sparkwing)", Group: "Input"},
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
		{"Lint a sibling library by path", "sparkwing pipeline sparks lint --path ~/code/sparks-core"},
		{"Lint a multi-module monorepo", "sparkwing pipeline sparks lint ~/code/sparks-core"},
	},
}

var cmdSparksResolve = Command{
	Path:     "sparkwing pipeline sparks resolve",
	Synopsis: "Resolve versions and materialize the overlay modfile",
	Description: `Runs the same pipeline as 'sparkwing run <name>' takes before compile:
load sparks.yaml, resolve each entry against the module proxy,
and write .sparkwing/.resolved.mod + .resolved.sum. Idempotent
-- a second run with no upstream change is a fast no-op that
prints 'up-to-date'. Never modifies the git-tracked go.mod.`,
	Flags: []FlagSpec{
		{Name: "sparkwing-dir", Argument: "DIR", Desc: "Path to .sparkwing/ (default: <cwd>/.sparkwing)", Group: "Input"},
		{Name: "quiet", Short: "q", Desc: "Suppress the 'up-to-date' message", Group: "Output"},
	},
	Examples: []Example{
		{"Resolve and write the overlay", "sparkwing pipeline sparks resolve"},
		{"Quiet mode for scripts", "sparkwing pipeline sparks resolve -q"},
	},
}

var cmdSparksUpdate = Command{
	Path:     "sparkwing pipeline sparks update",
	Synopsis: "Re-resolve one or all libraries",
	Description: `Re-runs resolution for every declared library (or a single
named one) and re-materializes the overlay modfile. For a
range or 'latest' constraint this picks up any new tag from
the module proxy; for an exact pin it is a no-op.`,
	Flags: []FlagSpec{
		{Name: "name", Argument: "NAME", Desc: "Restrict update to one library (name or source); omit to update all", Group: "Input"},
		{Name: "sparkwing-dir", Argument: "DIR", Desc: "Path to .sparkwing/ (default: <cwd>/.sparkwing)", Group: "Input"},
	},
	GroupOrder: []string{"Input", "Other"},
	Examples: []Example{
		{"Update every declared library", "sparkwing pipeline sparks update"},
		{"Update one by name", "sparkwing pipeline sparks update --name sparks-core"},
	},
}

var cmdSparksAdd = Command{
	Path:     "sparkwing pipeline sparks add",
	Synopsis: "Add a library to sparks.yaml",
	Description: `Appends a new entry to .sparkwing/sparks.yaml. Defaults the
version to 'latest' when --version is omitted. Refuses to add
a duplicate (same source or same name).`,
	Flags: []FlagSpec{
		{Name: "source", Argument: "PATH", Desc: "Go module path (e.g. github.com/user/sparks-lib)", Required: true, Group: "Input"},
		{Name: "version", Argument: "VER", Desc: "Declared version ('latest', exact tag, or semver range)", Group: "Input"},
		{Name: "name", Argument: "NAME", Desc: "Short library name (default: last path segment of --source)", Group: "Input"},
		{Name: "sparkwing-dir", Argument: "DIR", Desc: "Path to .sparkwing/ (default: <cwd>/.sparkwing)", Group: "Input"},
	},
	GroupOrder: []string{"Input", "Other"},
	Examples: []Example{
		{"Add a library pinned to latest", "sparkwing pipeline sparks add --source github.com/sparkwing-dev/sparks-core"},
		{"Add with a semver range", `sparkwing pipeline sparks add --source github.com/sparkwing-dev/sparks-core --version "^v0.10.0"`},
	},
}

var cmdSparksRemove = Command{
	Path:        "sparkwing pipeline sparks remove",
	Synopsis:    "Remove a library from sparks.yaml",
	Description: `Removes the entry matching NAME (or matching its source path).`,
	Flags: []FlagSpec{
		{Name: "name", Argument: "NAME", Desc: "Library name or source path to remove", Required: true, Group: "Input"},
		{Name: "sparkwing-dir", Argument: "DIR", Desc: "Path to .sparkwing/ (default: <cwd>/.sparkwing)", Group: "Input"},
	},
	GroupOrder: []string{"Input", "Other"},
	Examples: []Example{
		{"Remove by short name", "sparkwing pipeline sparks remove --name sparks-core"},
		{"Remove by source path", "sparkwing pipeline sparks remove --name github.com/sparkwing-dev/sparks-core"},
	},
}

var cmdSparksWarmup = Command{
	Path:     "sparkwing pipeline sparks warmup",
	Synopsis: "Pre-compile pipeline binaries after a sparks release",
	Description: `Post-release optimization: resolve the latest versions, compile
the pipeline binary for the current .sparkwing/ tree, and
upload to gitcache so the next 'sparkwing run' in-cluster or on a
fresh laptop gets a cache hit instead of paying the full
compile cost.

Uses the exact same build path as 'sparkwing run', so the cache key
matches. Warmup is optional -- pipelines always resolve on
build -- it just removes the first-run compile cost after a
new sparks version is published.`,
	Flags: []FlagSpec{
		{Name: "sparkwing-dir", Argument: "DIR", Desc: "Path to .sparkwing/ (default: <cwd>/.sparkwing)", Group: "Input"},
		{Name: "clear-cache", Desc: "Delete the local pipeline binary cache before compiling", Group: "Input"},
	},
	Examples: []Example{
		{"Warm up the current repo's pipelines", "sparkwing pipeline sparks warmup"},
		{"Force a fresh compile", "sparkwing pipeline sparks warmup --clear-cache"},
	},
}

var cmdSparksInflate = Command{
	Path:     "sparkwing pipeline sparks inflate",
	Synopsis: "Copy a spark library's source into this repo so you can edit it",
	Description: `Inflates a spark library: copies its source out of the Go
module cache into .sparkwing/sparks/<name>/, then adds a
'replace <module> => ./sparks/<name>' directive to
.sparkwing/go.mod and runs 'go mod tidy'.

--module takes a sparks-core block name (e.g. 'templates',
which resolves to github.com/sparkwing-dev/sparks-core/templates)
or a full module path for any other spark library.

The version is read from .sparkwing/go.mod's require list, or
'latest' when the module is not yet required.

Because the replace directive points at the copied tree, your
import paths do not change and transitive dependencies keep
resolving -- the code is simply yours now, editable in place.
The command refuses to overwrite an existing destination. To
undo, delete .sparkwing/sparks/<name>/ and drop the replace
directive.`,
	Flags: []FlagSpec{
		{Name: "module", Argument: "NAME", Desc: "Sparks-core block name (e.g. templates) or a full module path", Required: true, Group: "Input"},
		{Name: "sparkwing-dir", Argument: "DIR", Desc: "Path to .sparkwing/ (default: <cwd>/.sparkwing)", Group: "Input"},
		{Name: "output", Short: "o", Argument: "FMT", Desc: "Output format: pretty|json", Group: "Output"},
	},
	GroupOrder: []string{"Input", "Output", "Other"},
	Examples: []Example{
		{"Inflate the sparks-core templates module", "sparkwing pipeline sparks inflate --module templates"},
		{"Inflate any spark library by module path", "sparkwing pipeline sparks inflate --module github.com/example/my-sparks"},
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
		{"Approve a local gate", "sparkwing runs approvals approve --run run-20260423-143012-abcd --node approve-prod"},
		{"Approve a prod gate with a comment", `sparkwing runs approvals approve --run run-... --node approve-prod --profile prod --comment "release notes ok"`},
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
		{"Deny a local gate", "sparkwing runs approvals deny --run run-20260423-143012-abcd --node approve-prod"},
		{"Deny a prod gate with a reason", `sparkwing runs approvals deny --run run-... --node approve-prod --profile prod --comment "tests still red"`},
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
		{"Full history for one run", "sparkwing runs approvals list --run run-..."},
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
		{"Every node annotation on a run", "sparkwing runs annotations list --run run-..."},
		{"Include per-step annotations", "sparkwing runs annotations list --run run-... --steps"},
		{"One node's annotations as JSON", "sparkwing runs annotations list --run run-... --node build -o json"},
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
		{"Note something on a node", "sparkwing runs annotations add --run run-... --node deploy -m 'agent: retried after 502'"},
		{"Note something on a step inside a node", "sparkwing runs annotations add --run run-... --node deploy --step canary -m 'rolled out 5%'"},
	},
}

var cmdRepos = Command{
	Path:     "sparkwing repos",
	Synopsis: "The machine's fleet of sparkwing repos and their SDK pins",
	Description: `Lists every repo on this machine that carries sparkwing
pipelines -- derived from the repos this laptop has run pipelines
for, unioned with the explicit repos.yaml registry. No manual
registration: a repo shows up once it has run a pipeline or been
added to repos.yaml.

Each row reports the repo, its .sparkwing SDK pin, the last run
observed, and how many migration guides sit between its pin and
the latest release. Linked git worktrees are folded into their
primary checkout; a worktree pinned differently from its primary
is reported as a detail line, not a separate repo.

Bare 'sparkwing repos' and 'sparkwing repos list' both print this
fleet. Use 'sparkwing repos info' for a single-repo deep dive, and
'sparkwing repos update' to bump the whole fleet in one sitting
with a compiled per-repo verdict.`,
	SubcommandOrder:    []string{"list", "info", "update"},
	SubcommandOptional: true,
	Flags: []FlagSpec{
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty | json | plain", Default: "pretty", Group: "Output"},
	},
	GroupOrder: []string{"Output", "Other"},
	Examples: []Example{
		{"List the fleet", "sparkwing repos"},
		{"Agent-readable record", "sparkwing repos -o json"},
	},
}

var cmdReposList = Command{
	Path:     "sparkwing repos list",
	Synopsis: "List the machine's fleet of sparkwing repos",
	Description: `Prints the fleet: every repo on this machine that carries
sparkwing pipelines, with its SDK pin, last run, and how many
migration guides sit between its pin and the latest release. This is
the same output as bare 'sparkwing repos'; the explicit verb exists
so the listing has a name alongside 'info' and 'update'.`,
	Flags: []FlagSpec{
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty | json | plain", Default: "pretty", Group: "Output"},
	},
	GroupOrder: []string{"Output", "Other"},
	Examples: []Example{
		{"List the fleet", "sparkwing repos list"},
		{"Agent-readable record", "sparkwing repos list -o json"},
	},
}

var cmdReposInfo = Command{
	Path:     "sparkwing repos info",
	Synopsis: "Deep dive on one repo: pin, guides, worktrees, schema, pipelines",
	Description: `Reports everything worth knowing about one repo without
stitching it together from git, go.mod, and run history by hand. It
defaults to the repo containing the current directory; --repo names
another fleet member by name or checkout path.

It shows the .sparkwing SDK pin (or replace directive) against the
latest release, the migration guides in between with their titles
and summaries, linked worktrees and any that pin a different
version, the working tree's branch, commit, and clean/dirty state,
whether the pin can open the machine's shared state database (a
mismatch is caught here rather than when a run fails), and the
repo's pipelines with their last run time and status. When
something is off it prints one suggested next step.

Read-only: it never builds, bumps, or commits anything.`,
	Flags: []FlagSpec{
		{Name: "repo", Argument: "NAME_OR_PATH", Desc: "Repo by name or checkout path. Default: the repo containing the current directory.", Group: "Filter"},
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty | json", Default: "pretty", Group: "Output"},
	},
	GroupOrder: []string{"Filter", "Output", "Other"},
	Examples: []Example{
		{"Deep dive on the current repo", "sparkwing repos info"},
		{"Deep dive on a named repo", "sparkwing repos info --repo my-app"},
		{"Agent-readable record", "sparkwing repos info --repo my-app -o json"},
	},
}

var cmdReposUpdate = Command{
	Path:     "sparkwing repos update",
	Synopsis: "Bump the fleet's SDK pins with a compiled per-repo verdict",
	Description: `Bumps every tracked repo's .sparkwing SDK pin to a target
release and reports a compiled verdict per repo. For each repo with
a clean working tree it bumps the pin, runs go mod tidy, and
plan-constructs every registered pipeline before and after the
bump:

  - clean: the bump compiled and every plan is byte-identical --
    a guaranteed no-behavior-change upgrade.
  - plan-differs: the bump compiled but a plan changed shape; the
    structured node/dep/step diff is shown.
  - broken: the bump failed to apply, compile, or verify; the
    actual error is shown with the crossed migration guides.

Dirty or missing repos are skipped and named rather than guessed
at. Dry-run by default: nothing is written. --apply commits the
bump per repo with a conventional message (no pushes). --verify
additionally runs each repo's pre-commit gate after the bump.
--repo scopes to one repo by name or path.

Because a shared state database refuses an older pin against a
migrated schema, the fleet is meant to move together; the report
leads with that when pins would diverge.`,
	Flags: []FlagSpec{
		{Name: "version", Argument: "TAG", Desc: "Target SDK release (e.g. v0.16.0). Default: latest.", Group: "Input"},
		{Name: "apply", Desc: "Write the bumps and commit per repo (default is a dry run)", Group: "Behavior"},
		{Name: "verify", Desc: "Run each repo's pre-commit gate after the bump", Group: "Behavior"},
		{Name: "repo", Argument: "NAME_OR_PATH", Desc: "Scope to a single repo by name or checkout path", Group: "Filter"},
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty | json", Default: "pretty", Group: "Output"},
	},
	GroupOrder: []string{"Input", "Behavior", "Filter", "Output", "Other"},
	Examples: []Example{
		{"Preview a fleet-wide bump to latest (dry run)", "sparkwing repos update"},
		{"Preview a bump to a specific release", "sparkwing repos update --version v0.16.0"},
		{"Apply the bump and commit per repo", "sparkwing repos update --version v0.16.0 --apply"},
		{"Scope to one repo and run its gate", "sparkwing repos update --repo my-app --verify"},
	},
}
