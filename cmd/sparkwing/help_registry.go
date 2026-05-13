// Command registry -- the source of truth for help output, flag
// validation, and shell completion. Every leaf handler pulls its
// Command from here so the help page, the error messages, and the
// tab-completion menu all describe the same thing.
package main

import (
	"runtime"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// helpExampleScratchDir returns a per-OS scratch directory string
// suitable for inlining into help text. Linux gets "/tmp/<name>",
// macOS the same (since /tmp is a symlink to /private/tmp and works
// fine), and Windows gets "%TEMP%\<name>" -- using the env-var form
// rather than a hardcoded C:\Temp so the example is portable across
// users and matches what they'd type at a cmd.exe prompt. Avoids
// os.TempDir() because that resolves to ugly per-process paths on
// macOS (/var/folders/...) that aren't useful to copy-paste.
func helpExampleScratchDir(name string) string {
	if runtime.GOOS == "windows" {
		return `%TEMP%\` + name
	}
	return "/tmp/" + name
}

// ---- top-level --------------------------------------------------

var cmdSparkwing = Command{
	Path:     "sparkwing",
	Synopsis: "sparkwing -- CI/CD pipelines written in Go",
	Description: `Sparkwing is a self-hosted pipeline runner. Pipelines are Go
programs in a repo's .sparkwing/ directory, triggered by git hooks,
webhooks, schedules, or manual invocation. Use 'wing <pipeline>' as
the shorthand for 'sparkwing pipeline run <pipeline>'. Use 'sparkwing
pipeline list' / 'describe' for agent-facing discovery.`,
	Subcommands: []SubcommandRef{
		// Project flow (most-used)
		{"info", "What is sparkwing, what's in this repo, what to run next"},
		{"pipeline", "This repo's pipelines"},
		{"run", "Run a pipeline (shortcut for `pipeline run`)"},
		{"runs", "Inspect or manage runs"},
		{"version", "Show + update versions"},
		{"update", "Self-update the CLI binary"},
		// Local + remote ops
		{"dashboard", "Local dashboard server"},
		{"cluster", "Cluster ops"},
		{"secrets", "Manage secrets"},
		{"configure", "Laptop-local config"},
		{"debug", "Interactive run debugging"},
		// Docs + agent surface
		{"docs", "Embedded user docs (offline)"},
		{"commands", "Full CLI surface as JSON (agent self-discovery)"},
		{"completion", "Shell completion script"},
	},
	Examples: []Example{
		{"Run a pipeline (positional shortcut)", "sparkwing run build-test-deploy"},
		{"First command an agent should run", "sparkwing info --json"},
		{"List every invocable (agents)", "sparkwing pipeline list --json"},
		{"Inspect one pipeline's full metadata", "sparkwing pipeline describe --name release --json"},
		{"Bootstrap + scaffold your first pipeline in a fresh repo", "sparkwing pipeline new --name release"},
		{"Start the local dashboard", "sparkwing dashboard start"},
	},
}

// ---- sparkwing info -------------------------------------------

var cmdInfo = Command{
	Path:     "sparkwing info",
	Synopsis: "Self-describe sparkwing + the current project (agent entrypoint)",
	Description: `One command that answers "what is sparkwing, am I in a
project, what should I run next" without prior knowledge. Prints
the CLI version, whether the current directory is inside a
sparkwing project (and how many pipelines it has), whether the Go
toolchain is on PATH, a curated list of next-step commands, and
the docs URL.

This is the canonical first command an agent runs after install.
Use --json for structured output that an agent can parse, or
-o plain to emit one next-step command per line for shell
pipelines (head -n1 yields the most-likely next command).`,
	Flags: []FlagSpec{
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty | json | plain", Default: "pretty", Group: "Output"},
		{Name: "json", Desc: "Alias for --output json", Group: "Output"},
		{Name: "for-agent", Desc: "Emit a paste-ready block for CLAUDE.md / AGENTS.md (no ANSI, no extras)", Group: "Output"},
		{Name: "first-time", Desc: "Print the post-install onboarding card (used by install.sh; re-runnable any time)", Group: "Output"},
	},
	GroupOrder: []string{"Output", "Other"},
	Examples: []Example{
		{"Human-readable card", "sparkwing info"},
		{"Agent-readable record", "sparkwing info --json"},
		{"Paste into CLAUDE.md / AGENTS.md", "sparkwing info --for-agent >> CLAUDE.md"},
		{"Reprint the post-install onboarding card", "sparkwing info --first-time"},
	},
}

// ---- sparkwing cluster ----------------------------------------

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

Profiles (via --on) pick which cluster these commands
address; set them up with 'sparkwing configure profiles'.`,
	Subcommands: []SubcommandRef{
		{"status", "Roll-up report: controller health + fleet + queue + recent runs"},
		{"agents", "Fleet-view detail (GET /api/v1/agents)"},
		{"worker", "Run a laptop-side worker against a remote cluster"},
		{"gc", "Sweep stale warm-PVC state"},
		{"push", "Publish the current repo's HEAD to the profile's gitcache"},
		{"users", "Create / list / delete dashboard login users"},
		{"tokens", "Create / list / revoke / rotate controller API tokens"},
		{"image", "Image rollout helpers for gitops-managed deployments"},
		{"webhooks", "Inspect / replay GitHub webhooks (wraps gh api)"},
	},
	Examples: []Example{
		{"Cluster health summary", "sparkwing cluster status --on prod"},
		{"List fleet agents", "sparkwing cluster agents --on prod"},
	},
}

// ---- sparkwing configure --------------------------------------

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
	Subcommands: []SubcommandRef{
		{"init", "Set up ~/.config/sparkwing/ and report laptop-level config status"},
		{"profiles", "Manage connection profiles for remote controllers"},
		{"xrepo", "Cross-repo registry: list / add / remove / prune local checkouts"},
	},
	Examples: []Example{
		{"First-time laptop setup", "sparkwing configure init"},
		{"Status of laptop config", "sparkwing configure init --json"},
		{"List profiles", "sparkwing configure profiles list"},
		{"Add a new profile", "sparkwing configure profiles add --name prod --controller https://api.sparkwing.example --token $TOKEN"},
		{"Register the current repo with the cross-repo registry", "sparkwing configure xrepo add"},
	},
}

// ---- sparkwing configure init --------------------------------

var cmdConfigureInit = Command{
	Path:     "sparkwing configure init",
	Synopsis: "Set up ~/.config/sparkwing/ and report laptop-level config status",
	Description: `Idempotent setup + status command for laptop-level
sparkwing config. Creates ~/.config/sparkwing/ if it doesn't exist,
then reports which config files are present (profiles.yaml,
repos.yaml, config.yaml, secrets.env), the running CLI + Go
toolchain version, and a curated list of next-step commands.

Pairs with the per-project flow: use this one on a fresh laptop
after install, then run 'sparkwing pipeline new --name <name>'
inside each project to scaffold .sparkwing/ + your first pipeline
in one step (no separate init needed).

Re-running on an already-set-up laptop is a no-op status report.
--dry-run skips the mkdir so the command pure-probes.`,
	Flags: []FlagSpec{
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty | json | plain", Default: "pretty", Group: "Output"},
		{Name: "json", Desc: "Alias for --output json", Group: "Output"},
		{Name: "dry-run", Desc: "Probe + report without creating ~/.config/sparkwing/", Group: "Behavior"},
	},
	GroupOrder: []string{"Output", "Behavior", "Other"},
	Examples: []Example{
		{"First-time laptop setup", "sparkwing configure init"},
		{"Status of laptop config (agent-readable)", "sparkwing configure init --json"},
		{"Probe without writing anything", "sparkwing configure init --dry-run"},
	},
}

// ---- sparkwing version ----------------------------------------

var cmdVersion = Command{
	Path:     "sparkwing version",
	Synopsis: "Show + update versions (CLI, SDK, sparks)",
	Description: `Reports the installed CLI version + build provenance, the
latest published release on GitHub (with a short network
fetch -- bounded by ~3s, fail-soft when offline), and the
.sparkwing/go.mod SDK pin + any sparks-* libraries declared
alongside it.

Behind-by-version is computed via semver compare for both the
CLI itself and the SDK pin so an agent reading --json can
trigger an upgrade without parsing prose.

--offline skips the network fetch entirely; --json emits the
structured report; -o plain prints semver lines (CLI then
latest) for shell pipelines.`,
	Subcommands: []SubcommandRef{
		{"update", "Self-update CLI binary or bump SDK pin (requires --cli or --sdk)"},
	},
	Flags: []FlagSpec{
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty | json | plain", Default: "pretty", Group: "Output"},
		{Name: "json", Desc: "Alias for --output json", Group: "Output"},
		{Name: "offline", Desc: "Skip the network fetch for latest release", Group: "Behavior"},
	},
	GroupOrder: []string{"Output", "Behavior", "Other"},
	Examples: []Example{
		{"Human-readable card", "sparkwing version"},
		{"Agent-readable record", "sparkwing version --json"},
		{"CLI semver only (scripts)", "sparkwing version -o plain | head -n1"},
		{"Local-only (no network)", "sparkwing version --offline"},
		{"Update the CLI binary", "sparkwing version update --cli"},
		{"Bump the SDK pin in this project", "sparkwing version update --sdk"},
	},
}

// ---- sparkwing update ----------------------------------------

// cmdUpdate is the top-level binary self-update verb. Binary-only
// (no --cli/--sdk split); for SDK updates use `version update --sdk`.
// --check reports installed vs latest without modifying anything;
// --force overrides the downgrade safety guard.
var cmdUpdate = Command{
	Path:     "sparkwing update",
	Synopsis: "Self-update the CLI binary",
	Description: `Downloads, checksum-verifies, and atomically installs the latest
(or a specific) sparkwing release from GitHub Releases.

By default the command fetches the latest version pointer, pulls
the matching tarball for the current OS/arch, verifies its SHA256
against the published SHA256SUMS, and replaces the running binary
via an atomic rename. macOS arm64 binaries are ad-hoc-codesigned
after installation to avoid SIGKILL on first run.

--check is the read-only probe: it reports the installed version
and the latest published release, exits 0 when already current,
and exits 1 when a newer release exists (useful for CI/notifications).

Downgrades are blocked by default. Pass --force to install an older
release (e.g. bisecting a regression).

For SDK (go.mod) bumps, use 'sparkwing version update --sdk'.`,
	Flags: []FlagSpec{
		{Name: "check", Desc: "Report installed vs latest; exit 1 if a newer release exists (read-only)", Group: "Behavior"},
		{Name: "force", Desc: "Allow downgrading to an older release", Group: "Behavior"},
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

// cmdVersionUpdate is the unified update verb. Picks one of two
// targets explicitly via --cli (binary self-update) or --sdk
// (per-project go.mod bump). Neither flag is the default --
// the operator must state intent so a stray `version update`
// can't quietly do the wrong half.
var cmdVersionUpdate = Command{
	Path:     "sparkwing version update",
	Synopsis: "Self-update the CLI binary (--cli) or bump this project's SDK pin (--sdk)",
	Description: `Two targets, one verb:

  --cli   Replace the running sparkwing binary with the target
          release. Resolves the version pointer from GitHub Releases,
          downloads + checksum-verifies the tarball, and atomically
          installs it. macOS arm64 binaries are ad-hoc-codesigned
          to avoid SIGKILL on first run.

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

// ---- sparkwing commands ---------------------------------------

var cmdCommands = Command{
	Path:             "sparkwing commands",
	Synopsis:         "Emit the full CLI surface as structured data (agent self-discovery)",
	HideFromComplete: true,
	Description: `Returns every registered verb -- path, synopsis, description,
positional args, flags, examples, subcommand list -- as one
JSON document. Designed as the agent's "what is this CLI"
probe: one tool call replaces walking every -h page.

The same Command record powers --help on every verb; this just
emits all of them in one shot. Filter with --path PREFIX to
narrow to a subtree (e.g. --path "sparkwing pipeline"). Default
output is JSON because agents are the primary audience; -o
plain emits one path per line for shell consumption.`,
	Flags: []FlagSpec{
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: json | plain | pretty", Default: "json", Group: "Output"},
		{Name: "path", Argument: "PREFIX", Desc: "Only emit commands whose Path starts with PREFIX", Group: "Filter"},
		{Name: "include-hidden", Desc: "Also emit Hidden:true commands (default: skip)", Group: "Filter"},
	},
	GroupOrder: []string{"Output", "Filter", "Other"},
	Examples: []Example{
		{"Full CLI surface (agent self-discovery)", "sparkwing commands"},
		{"Just the pipelines subtree", "sparkwing commands --path \"sparkwing pipeline\""},
		{"All paths, one per line", "sparkwing commands -o plain"},
	},
}

// ---- sparkwing docs -------------------------------------------

var cmdDocs = Command{
	Path:     "sparkwing docs",
	Synopsis: "Embedded user docs (offline)",
	Description: `The sparkwing docs are shipped inside the binary. ` +
		"`sparkwing docs read --topic getting-started`" + ` returns the
raw markdown to stdout; ` + "`sparkwing docs all`" + ` dumps every
doc in one shot for an agent that wants the full corpus in
context. The docs match the binary version exactly -- no risk of
the website explaining a flag your CLI doesn't have.

Discovery: ` + "`sparkwing docs list --json`" + ` returns slug + title +
summary for every topic. ` + "`sparkwing docs search --query \"warm pool\"`" + `
substring-matches across slug + title + body.`,
	Subcommands: []SubcommandRef{
		{"list", "Enumerate every doc topic (slug, title, summary)"},
		{"read", "Print one doc's markdown to stdout (--topic NAME)"},
		{"all", "Concatenate every doc to stdout (full corpus dump)"},
		{"search", "Substring search across docs (--query TEXT)"},
	},
	Examples: []Example{
		{"List all topics (table)", "sparkwing docs list"},
		{"List all topics (agent-readable)", "sparkwing docs list --json"},
		{"Read one topic", "sparkwing docs read --topic pipelines"},
		{"Slurp the whole corpus into context", "sparkwing docs all"},
		{"Find docs that mention warm pool", "sparkwing docs search --query \"warm pool\""},
	},
}

var cmdDocsList = Command{
	Path:     "sparkwing docs list",
	Synopsis: "Enumerate every doc topic",
	Description: `Walks the embedded docs and prints one row per topic with its
slug, first-H1 title, and first-paragraph summary. Use --json for
agent-readable structured output, --output plain for one slug per
line (pipe-friendly).`,
	Flags: []FlagSpec{
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty | json | plain", Default: "pretty", Group: "Output"},
		{Name: "json", Desc: "Alias for --output json", Group: "Output"},
	},
	GroupOrder: []string{"Output", "Other"},
	Examples: []Example{
		{"Human-readable table", "sparkwing docs list"},
		{"Agent-readable", "sparkwing docs list --json"},
		{"Slug-per-line for shell loops", "sparkwing docs list -o plain"},
	},
}

var cmdDocsRead = Command{
	Path:     "sparkwing docs read",
	Synopsis: "Print one doc's raw markdown to stdout",
	Description: `Prints the raw markdown body for the named topic. The slug is
the filename under /docs/ minus .md (run ` + "`sparkwing docs list`" + ` to
see them all). Subdirs use slash-separated paths (e.g.
design/remote-retry).`,
	Flags: []FlagSpec{
		{Name: "topic", Argument: "NAME", Desc: "Doc slug (e.g. getting-started, pipelines, mcp)", Required: true, Group: "Selection"},
	},
	GroupOrder: []string{"Selection", "Other"},
	Examples: []Example{
		{"Read the getting-started page", "sparkwing docs read --topic getting-started"},
		{"Pipe through a pager", "sparkwing docs read --topic pipelines | less"},
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

var cmdDocsSearch = Command{
	Path:     "sparkwing docs search",
	Synopsis: "Substring search across embedded docs",
	Description: `Returns every doc whose slug + title + body contains every
space-separated token in --query (case-insensitive). Hits in
title/slug rank above body-only matches. Output shape matches
` + "`sparkwing docs list`" + ` so --json composes the same way.`,
	Flags: []FlagSpec{
		{Name: "query", Short: "q", Argument: "TEXT", Desc: "Search terms (every token must match)", Required: true, Group: "Selection"},
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty | json | plain", Default: "pretty", Group: "Output"},
		{Name: "json", Desc: "Alias for --output json", Group: "Output"},
	},
	GroupOrder: []string{"Selection", "Output", "Other"},
	Examples: []Example{
		{"Find docs about the warm pool", "sparkwing docs search --query \"warm pool\""},
		{"JSON for agents", "sparkwing docs search -q approval --json"},
	},
}

// ---- sparkwing debug ------------------------------------------

var cmdDebug = Command{
	Path:     "sparkwing debug",
	Synopsis: "Interactive debugging for pipeline runs",
	Description: `Pause nodes at selected hook points, inspect the paused pod,
drop into a shell, or release the node. Every debug verb is
ephemeral -- pause directives live only on the run they launch,
never in pipeline source. Pipelines stay production-clean.`,
	Subcommands: []SubcommandRef{
		{"run", "Run a pipeline with --pause-before / --pause-after / --pause-on-failure"},
		{"release", "Resume a paused node"},
		{"attach", "kubectl exec into a paused node's pod (cluster mode)"},
		{"env", "Print a paused node's env + workdir + claim holder"},
		{"rerun", "Reproduce a node's dispatch frame and drop into a shell"},
		{"replay", "Headlessly re-execute a single node from a prior run"},
	},
	Examples: []Example{
		{"Pause before the tests node", "sparkwing debug run build --pause-before tests"},
		{"Resume a paused node", "sparkwing debug release --run run-X --node tests"},
	},
}

var cmdDebugRun = Command{
	Path:     "sparkwing debug run",
	Synopsis: "Run a pipeline with ephemeral pause directives",
	Description: `Runs the named pipeline exactly as 'wing <pipeline>' would, with
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
		{Name: "on", Argument: "NAME", Desc: "Profile name (cluster mode)", Group: "System"},
	},
	Examples: []Example{
		{"Release locally", "sparkwing debug release --run run-X --node tests"},
		{"Release in prod", "sparkwing debug release --run run-X --node tests --on prod"},
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
		{Name: "on", Argument: "NAME", Desc: "Profile name (cluster mode)", Group: "System"},
	},
	Examples: []Example{
		{"Attach in prod", "sparkwing debug attach --run run-X --node tests --on prod"},
	},
}

var cmdDebugRerun = Command{
	Path:     "sparkwing debug rerun",
	Synopsis: "Reproduce a node's dispatch frame in an interactive shell",
	Description: `Reads the dispatch snapshot for the given run/node and reproduces
the env + workdir the orchestrator saw at dispatch time. Local mode
exec's $SHELL with the snapshot env applied and writes upstream Ref
outputs to ~/.sparkwing/rerun/<run>/<node>/refs so they're cat-able
from the shell. Cluster mode shells out to 'kubectl run' against a
runner image (--image or $SPARKWING_RERUN_IMAGE) with the snapshot
env materialized as --env=K=V flags.

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
		{Name: "on", Argument: "NAME", Desc: "Profile name (cluster mode)", Group: "System"},
		{Name: "image", Argument: "REF", Desc: "Runner image for cluster-mode debug pod (cluster mode)", Group: "System"},
	},
	Examples: []Example{
		{"Rerun locally", "sparkwing debug rerun --run run-X --node tests"},
		{"Rerun a specific attempt", "sparkwing debug rerun --run run-X --node tests --seq 1"},
		{"Rerun in prod", "sparkwing debug rerun --run run-X --node tests --on prod --image ghcr.io/me/runner:v1"},
	},
}

var cmdDebugReplay = Command{
	Path:     "sparkwing debug replay",
	Synopsis: "Re-execute a single node headlessly using its dispatch snapshot",
	Description: `Mints a new run row linked to the original via replay_of_run_id /
replay_of_node_id, creates a single nodes row for the target, and
exec's the wing pipeline binary to execute that one node. The
node's input struct is reconstituted from the stored dispatch
snapshot; upstream Refs resolve against the original
run's outputs without re-executing them.

Replay is "what would this node do now, with the same args+env?":
secrets re-resolve fresh through sparkwing.Secret, BeforeRun hooks
re-fire, and any code drift in the registered job struct (renamed
type, removed field) aborts loud rather than silently producing
wrong results.

With --on PROF, the original run + target node + dep outputs +
dispatch snapshot are first fetched from the named controller via
HTTP and side-loaded into the local store. Replay execution itself
always runs locally because the user's wing binary owns the
registered pipeline factories.`,
	Flags: []FlagSpec{
		{Name: "run", Argument: "ID", Desc: "Run ID holding the original node", Required: true, Group: "Target"},
		{Name: "node", Argument: "NAME", Desc: "Node ID to re-execute", Required: true, Group: "Target"},
		{Name: "on", Argument: "PROF", Desc: "Sideload from this profile's controller before replaying locally", Group: "System"},
	},
	Examples: []Example{
		{"Replay a node locally", "sparkwing debug replay --run run-X --node deploy"},
		{"Replay a prod run on your laptop", "sparkwing debug replay --on prod --run run-X --node deploy"},
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
		{Name: "on", Argument: "NAME", Desc: "Profile name (cluster mode)", Group: "System"},
	},
	Examples: []Example{
		{"Inspect locally", "sparkwing debug env --run run-X --node tests"},
	},
}

// ---- sparkwing run / wing --------------------------------------

// wingFlagSpecs is shared between cmdWing (the `wing <pipeline>` help
// page) and cmdPipelineRun (the `sparkwing pipeline run` help page),
// since the two invocation surfaces are the same execution path
// under different names. Derived from sparkwing.WingFlagDocs() so
// the per-pipeline footer (orchestrator/main.go's printPipelineHelp)
// and the top-level `wing --help` enumerate from the same source.
// Adding a flag in sparkwing/wing_flag_docs.go surfaces it in every
// wing help page simultaneously.
var wingFlagSpecs = wingFlagSpecsFromDocs()

func wingFlagSpecsFromDocs() []FlagSpec {
	docs := sparkwing.WingFlagDocs()
	out := make([]FlagSpec, 0, len(docs))
	for _, d := range docs {
		out = append(out, FlagSpec{
			Name:     d.Name,
			Short:    d.Short,
			Argument: d.Argument,
			Desc:     d.Desc,
			Group:    d.Group,
		})
	}
	return out
}

var cmdWing = Command{
	Path:     "wing",
	Synopsis: "Run a pipeline from the nearest .sparkwing/",
	Description: `Walks up from the current working directory to locate
.sparkwing/main.go, compiles that binary (cached at
~/.sparkwing/bin/<hash>), and exec's it with the pipeline name +
forwarded args. Equivalent to 'sparkwing pipeline run <name>'.

Pipeline-specific arguments are passed as typed flags named after
the pipeline's Args fields, e.g. 'wing deploy --env prod --version
v1.2.3'. Run 'wing <pipeline> --help' to see the typed flags a
given pipeline accepts, or 'sparkwing pipeline describe <name>' for
a machine-readable view.

--from <ref> checks out a different git ref (branch, tag, SHA)
via 'git worktree add' and compiles the pipeline from there --
no impact on your current working tree.

--retry-of <run-id> runs this invocation as a retry of the named
run: nodes that passed in the prior run are rehydrated from the
store (their outputs are reused without re-executing); nodes
that failed, were cancelled, or weren't reached re-run. Pair
with --full to force a complete re-execution instead.

Args from the original run are preserved on retry: any args
passed on this invocation are ignored. This keeps Plan() output
deterministic across retries (e.g. an auto-bumped --version
won't drift just because a new tag landed between runs). To run
with different args, trigger fresh without --retry-of.

--on <profile> dispatches remotely: POST /api/v1/triggers on the
profile's controller.

--config <preset> layers named flag bundles from config.yaml
(repo-level at .sparkwing/config.yaml wins over user-level at
~/.config/sparkwing/config.yaml). Explicit flags always override
the preset.`,
	PosArgs: []PosArg{
		{Name: "<pipeline>", Desc: "Pipeline name registered in .sparkwing/pipelines.yaml", Required: true},
	},
	Flags: wingFlagSpecs,
	// Source: which ref / config / cwd. Range: --start-at/--stop-at.
	// Safety: --dry-run, --allow-*. System: where it runs (--on).
	// The Range + Safety sections are absent from per-leaf help in
	// much of the rest of the registry; here they're load-bearing
	// because the wing-level flags are encountered on every
	// invocation.
	GroupOrder:  []string{"Pipeline Args", "Source", "Range", "Safety", "System", "Other"},
	UsageSuffix: "[-- extra args]",
	Examples: []Example{
		{"Run with no arguments", "wing build-test-deploy"},
		{"Pass typed arguments", "wing release --version v0.28.1"},
		{"Run a specific branch without checking it out", "wing build-test-deploy --from feature/xyz"},
		{"Retry a failed run (skip previously-passed nodes)", "wing build-test-deploy --retry-of run-a1b2c3"},
		{"Long form via sparkwing", "sparkwing pipeline run --pipeline release --version v0.28.1"},
	},
	// Hidden from the default `sparkwing commands` listing because
	// wing is the human shortcut, not a peer command. Agents should
	// see the long form (`sparkwing pipeline run --pipeline <name>`)
	// as the canonical run path; surfacing wing alongside it
	// undermines that. `wing -h` still works; `sparkwing commands
	// --include-hidden` still emits the spec.
	Hidden: true,
}

// ---- sparkwing pipeline------------------------------------------

var cmdPipeline = Command{
	Path:     "sparkwing pipeline",
	Synopsis: "This repo's pipelines",
	Description: `Per-project namespace. Every verb here operates on the
nearest .sparkwing/ walking up from the current directory.

Discovery (list / describe / discover / explain) shows what
pipelines this repo defines. 'new' scaffolds a fresh pipeline
(auto-bootstraps .sparkwing/ on first use). 'run' invokes one
(positional name; same as 'wing <name>' or 'sparkwing run
<name>'). 'hooks' wires pipelines to git pre-commit / pre-push.
'sparks' manages reusable spark libraries declared in
.sparkwing/sparks.yaml.

Every verb supports --json so an agent can parse output
directly rather than scraping tab-complete.

To bump the pipeline SDK pin in .sparkwing/go.mod, use
'sparkwing version update --sdk'. To see the current pin, run
'sparkwing version' (composite card).`,
	Subcommands: []SubcommandRef{
		{"list", "Enumerate every pipeline with metadata"},
		{"describe", "Print one pipeline's full metadata"},
		{"discover", "Fuzzy search over names, descriptions, tags"},
		{"new", "Scaffold a new pipeline (auto-bootstraps .sparkwing/ if missing)"},
		{"explain", "Render the pipeline's Plan DAG without running"},
		{"plan", "Render the runtime-resolved DAG (would-run/would-skip) without running"},
		{"run", "Invoke a pipeline (canonical form of `sparkwing run <name>`)"},
		{"hooks", "Git pre-commit / pre-push hooks: install / uninstall / status"},
		{"sparks", "Manage sparks libraries: list / add / remove / lint / resolve / update / warmup"},
	},
	Examples: []Example{
		{"Machine-readable catalog", "sparkwing pipeline list --json"},
		{"One pipeline's details", "sparkwing pipeline describe --name release --json"},
		{"Search by intent", `sparkwing pipeline discover --query "tag a release"`},
		{"First pipeline in a fresh repo (auto-bootstraps)", "sparkwing pipeline new --name release"},
		{"Inspect the DAG before running", "sparkwing pipeline explain --name release-all"},
		{"Run a pipeline", "sparkwing pipeline run release"},
	},
}

// cmdPipelineRun is the canonical run verb under the pipeline
// namespace. `sparkwing run <name>` aliases to this. Positional
// pipeline name (the deliberate exception in an otherwise
// flag-only sparkwing surface) -- run is the hot path, typed
// many times a day.
var cmdPipelineRun = Command{
	Path:     "sparkwing pipeline run",
	Synopsis: "Invoke a pipeline (canonical form of `sparkwing run <name>`)",
	Description: `Compiles the nearest .sparkwing/ binary and exec's it
with the named pipeline. Identical to 'wing <name>' and to
the top-level shortcut 'sparkwing run <name>'.

The pipeline name is the only positional in the sparkwing
surface -- a deliberate exception, kept short because run is
typed many times a day.

Any flag not recognized by run itself is forwarded to the
pipeline binary, e.g. 'sparkwing pipeline run release
--version v1.2.3' passes --version through to the pipeline's
Args.`,
	PosArgs: []PosArg{
		{Name: "<pipeline>", Desc: "Pipeline name registered in .sparkwing/pipelines.yaml", Required: true},
	},
	Flags:       wingFlagSpecs,
	GroupOrder:  []string{"Source", "Range", "Safety", "System", "Other"},
	UsageSuffix: "[-- pipeline-flags...]",
	Examples: []Example{
		{"Run with no flags", "sparkwing pipeline run build-test-deploy"},
		{"Pass a typed pipeline arg", "sparkwing pipeline run release --version v0.28.1"},
		{"Run from a different git ref", "sparkwing pipeline run build-test-deploy --from feature/xyz"},
		{"Dispatch remotely", "sparkwing pipeline run deploy --on prod"},
	},
}

var cmdPipelineList = Command{
	Path:     "sparkwing pipeline list",
	Synopsis: "Enumerate every pipeline with metadata",
	Description: `Walks up from the current directory to locate .sparkwing/,
merges pipelines.yaml entries with the describe cache's typed
metadata, and prints a grouped aligned table.

--json emits structured records instead; agents should prefer
--json since tab-complete / table output is for human reading.

--all includes entries marked 'hidden: true'. By default they're
omitted.`,
	Flags: []FlagSpec{
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty | json | plain", Default: "pretty", Group: "Output"},
		{Name: "json", Desc: "Alias for --output json", Group: "Output"},
		{Name: "all", Desc: "Include entries marked hidden", Group: "Output"},
	},
	GroupOrder: []string{"Output", "Other"},
	Examples: []Example{
		{"Human-readable table", "sparkwing pipeline list"},
		{"Agent-readable catalog", "sparkwing pipeline list --json"},
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
		{Name: "json", Desc: "Alias for --output json", Group: "Output"},
	},
	GroupOrder: []string{"Target", "Output", "Other"},
	Examples: []Example{
		{"Human-readable", "sparkwing pipeline describe --name release"},
		{"Agent-readable", "sparkwing pipeline describe --name release --json"},
	},
}

var cmdPipelineDiscover = Command{
	Path:     "sparkwing pipeline discover",
	Synopsis: "Fuzzy search over pipeline names + descriptions + tags",
	Description: `Search the catalog by intent. Every token in --query
must match some haystack field (name / short / help / group /
tags / triggers); matches in the name score higher than matches
in prose so direct hits surface first.

--json emits {name, kind, group, ..., score} records sorted by
score descending; agents should prefer --json for consumption.`,
	Flags: []FlagSpec{
		{Name: "query", Argument: "TEXT", Desc: "Search query (one or more tokens, all must hit some field)", Required: true, Group: "Target"},
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty | json | plain", Default: "pretty", Group: "Output"},
		{Name: "json", Desc: "Alias for --output json (ranked, with score field)", Group: "Output"},
	},
	GroupOrder: []string{"Target", "Output", "Other"},
	Examples: []Example{
		{"Find release-related pipelines", `sparkwing pipeline discover --query release`},
		{"Multi-token, all must hit", `sparkwing pipeline discover --query "tag release"`},
		{"Agent-readable ranked hits", `sparkwing pipeline discover --query deploy --json`},
	},
}

var cmdPipelineNew = Command{
	Path:     "sparkwing pipeline new",
	Synopsis: "Scaffold a new Go pipeline",
	Description: `Creates a stub pipeline in the nearest .sparkwing/:
jobs/<snake>.go plus a pipelines.yaml entry. Auto-bootstraps
.sparkwing/ on first use, so a fresh repo's first scaffold sets
up the package skeleton too -- no separate init step, no
sample pipeline you didn't ask for.

Templates:
  - minimal (default): single-node Plan with a stubbed Run.
    Smallest viable shape; the editor's first move is replacing
    the Log("TODO") with real logic.
  - build-test-deploy: three-node Plan (build -> test -> deploy)
    with echo Run bodies that print a TODO line on each step.
    The canonical CI shape; first 'wing <name>' surfaces three
    exec banners + three echoed lines so the structure is
    visible end-to-end.

Refuses to clobber: if the name already exists in pipelines.yaml
the command fails before writing anything.

Supply --group to set the tab-complete section header; --hidden
to hide from default listings; --short to pre-fill the description.

See also:
  If your pipeline is a single linear shell sequence with no DAG,
  retry, or cross-runner concerns, a plain shell-script runner
  (e.g. just / make / a wrapper over ./bin/*.sh) is probably a
  better fit -- it skips the compile cycle.`,
	Flags: []FlagSpec{
		{Name: "name", Argument: "NAME", Desc: "New pipeline's kebab-case name (a-z, 0-9, -)", Required: true, Group: "Target"},
		{Name: "template", Argument: "KIND", Desc: "minimal (one node, default) | build-test-deploy (three-node build->test->deploy DAG)", Default: "minimal", Group: "Scaffold"},
		{Name: "group", Argument: "NAME", Desc: "Tab-complete section header (e.g. CI, Release)", Group: "Scaffold"},
		{Name: "hidden", Desc: "Mark the entry hidden in default tab-complete menus", Group: "Scaffold"},
		{Name: "short", Argument: "TEXT", Desc: "Pre-fill the ShortHelp / desc line", Group: "Scaffold"},
	},
	GroupOrder: []string{"Target", "Scaffold", "Other"},
	Examples: []Example{
		{"Single-node pipeline (default template)", "sparkwing pipeline new --name release"},
		{"Build/test/deploy DAG (three-node)", "sparkwing pipeline new --name release-all --template build-test-deploy --group Release"},
		{"Pre-fill the ShortHelp", `sparkwing pipeline new --name release --short "Cut a release"`},
	},
}

var cmdPipelineExplain = Command{
	Path:     "sparkwing pipeline explain",
	Synopsis: "Render the pipeline's Plan DAG without dispatching any jobs",
	Description: `Compiles the nearest .sparkwing/ binary, calls the named
pipeline's Plan method, and prints the resulting DAG (nodes,
dependencies, approval gates) without running a single job.

Any --flag value tokens that are NOT recognized by explain itself
(i.e. anything other than --name / --all / --json / --help) are
forwarded to the pipeline so Plans that branch on --env / --version
/ etc. can be previewed under realistic inputs. Missing required
args are non-fatal here -- explain renders a best-effort plan so
the shape is visible before every flag is provided.

--all sweeps every pipeline in .sparkwing/pipelines.yaml, runs
Plan() on each with no extra args, and exits non-zero if any
pipeline fails. Designed as a CI gate: a Plan-time validation
mismatch (sparkwing.RefTo[T] type drift, Produces[T] / SetResult
asymmetry, duplicate node ID, etc.) blocks merges before the
pipeline ever runs.`,
	Flags: []FlagSpec{
		{Name: "name", Argument: "NAME", Desc: "Pipeline to explain (one of --name or --all required)", Group: "Target"},
		{Name: "all", Desc: "Validate every pipeline in this repo's pipelines.yaml; non-zero exit on any failure", Group: "Target"},
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty | json", Default: "pretty", Group: "Output"},
		{Name: "json", Desc: "Alias for --output json (emits raw plan-snapshot JSON, or per-pipeline result rows under --all)", Group: "Output"},
	},
	GroupOrder:  []string{"Target", "Output", "Other"},
	UsageSuffix: "[-- pipeline-flags...]",
	Examples: []Example{
		{"Inspect release-all's DAG", "sparkwing pipeline explain --name release-all"},
		{"Preview with args (forwarded to the pipeline)", "sparkwing pipeline explain --name example-release --env prod"},
		{"Agent-readable JSON", "sparkwing pipeline explain --name release-all --json"},
		{"Validate every pipeline (CI gate)", "sparkwing pipeline explain --all"},
	},
}

// cmdPipelinePlan: same DAG as `explain` plus per-step would-run /
// would-skip decisions evaluated against the supplied args +
// --start-at / --stop-at bounds. NO step bodies execute. Designed
// as the canonical pre-flight verb -- agents and humans inspect the
// runtime-resolved plan before destructive operations,
// terraform-style.
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
with 'wing <name>' (or 'sparkwing run <name>') to actually
dispatch.`,
	Flags: []FlagSpec{
		{Name: "name", Argument: "NAME", Desc: "Pipeline to plan", Group: "Target"},
		{Name: "start-at", Argument: "STEP", Desc: "Skip every WorkStep upstream of STEP in the resulting plan", Group: "Range"},
		{Name: "stop-at", Argument: "STEP", Desc: "Skip every WorkStep downstream of STEP in the resulting plan", Group: "Range"},
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty | json", Default: "pretty", Group: "Output"},
		{Name: "json", Desc: "Alias for --output json (emits raw plan-preview JSON for agents)", Group: "Output"},
	},
	GroupOrder:  []string{"Target", "Range", "Output", "Other"},
	UsageSuffix: "[-- pipeline-flags...]",
	Examples: []Example{
		{"Resolve cluster-up's DAG with current args", "sparkwing pipeline plan --name cluster-up"},
		{"Preview a resume-from-step", "sparkwing pipeline plan --name cluster-up --start-at install-argocd"},
		{"Agent-readable JSON for diff against expectations", "sparkwing pipeline plan --name release-all --json"},
	},
}

// cmdRun is the top-level invoke verb: `sparkwing run <pipeline>`.
// Takes the pipeline name as a positional (the deliberate exception
// to the otherwise-flag-only sparkwing surface) because typing the
// pipeline name many times a day is the hot path; the symmetry cost
// is worth the ergonomic win, and `wing <name>` already proves the
// shape works.
var cmdRun = Command{
	Path:     "sparkwing run",
	Synopsis: "Invoke a pipeline (positional shortcut, same as 'wing <name>')",
	Description: `Compiles the nearest .sparkwing/ binary and exec's it
with the named pipeline. Identical to 'wing <name>'; this top-
level form exists so agents and scripts have a stable
'sparkwing run X' verb without typing 'wing'.

The pipeline name is the only positional in the sparkwing
surface -- a deliberate exception, kept short because run is
typed many times a day. Every other input is a named flag.

Any flag not recognized by run itself is forwarded to the
pipeline binary, e.g. 'sparkwing run release --version
v1.2.3' passes --version through to the pipeline's Args.`,
	PosArgs: []PosArg{
		{Name: "<pipeline>", Desc: "Pipeline name registered in .sparkwing/pipelines.yaml", Required: true},
	},
	Flags:       wingFlagSpecs,
	GroupOrder:  []string{"Source", "Range", "Safety", "System", "Other"},
	UsageSuffix: "[-- pipeline-flags...]",
	Examples: []Example{
		{"Run with no flags", "sparkwing run build-test-deploy"},
		{"Pass a typed pipeline arg", "sparkwing run release --version v0.28.1"},
		{"Run from a different git ref", "sparkwing run build-test-deploy --from feature/xyz"},
		{"Retry a failed run", "sparkwing run build-test-deploy --retry-of run-a1b2c3"},
		{"Dispatch remotely", "sparkwing run deploy --on prod --region us-west-2"},
	},
}

// ---- sparkwing dashboard ---------------------------------------

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
	Subcommands: []SubcommandRef{
		{"start", "Spawn the detached dashboard server (idempotent)"},
		{"kill", "Stop a running dashboard server"},
		{"status", "Report whether the dashboard is running"},
	},
	Examples: []Example{
		{"Start the dashboard", "sparkwing dashboard start"},
		{"Check liveness", "sparkwing dashboard status"},
		{"Stop the dashboard", "sparkwing dashboard kill"},
	},
}

var cmdDashboardStart = Command{
	Path:     "sparkwing dashboard start",
	Synopsis: "Spawn the detached dashboard server (idempotent)",
	Description: `Detaches a child process that runs the in-process
dashboard + API + logs server (pkg/localws). PID is written to
$SPARKWING_HOME/dashboard.pid; stdout/stderr are appended to
$SPARKWING_HOME/dashboard.log. Returns once the listener is
accepting TCP connections so callers can immediately curl it.

Idempotent: if a live server is already on file, prints the URL
and returns 0 without spawning a duplicate.`,
	Flags: []FlagSpec{
		{Name: "addr", Argument: "HOST:PORT", Desc: "Bind address", Default: "127.0.0.1:4343", Group: "Bind"},
		{Name: "home", Argument: "DIR", Desc: "State directory (default: $SPARKWING_HOME or ~/.sparkwing)", Group: "System"},
		{Name: "on", Argument: "PROFILE", Desc: "Profile from ~/.config/sparkwing/profiles.yaml (uses its log_store + artifact_store)", Group: "Storage"},
		{Name: "log-store", Argument: "URL", Desc: "Pluggable log backend URL (fs:///abs/path, s3://bucket/prefix). Overrides --on.", Group: "Storage"},
		{Name: "artifact-store", Argument: "URL", Desc: "Pluggable artifact backend URL (fs:///abs/path, s3://bucket/prefix). Overrides --on.", Group: "Storage"},
		{Name: "read-only", Desc: "Reject writes on /api/v1/* (auth + webhooks remain open)", Group: "Storage"},
		{Name: "no-local-store", Desc: "Skip local SQLite; list runs from --artifact-store. Requires --log-store + --artifact-store.", Group: "Storage"},
	},
	GroupOrder: []string{"Bind", "Storage", "System", "Other"},
	Examples: []Example{
		{"Start with defaults", "sparkwing dashboard start"},
		{"Use an alternate port", "sparkwing dashboard start --addr 127.0.0.1:5000"},
		{"Isolate state under a scratch dir", "sparkwing dashboard start --home " + helpExampleScratchDir("sparkwing-x")},
		{"Tail CI runs from S3 (no SQLite)", "sparkwing dashboard start --on ci-smoke --no-local-store --read-only"},
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

// ---- sparkwing worker ------------------------------------------

var cmdWorker = Command{
	Path:     "sparkwing cluster worker",
	Synopsis: "Claim triggers from a profile's controller and run them in-process",
	Description: `Polls the trigger queue at the selected profile's
controller and executes each claimed trigger in-process. Laptop-local:
no K8s, no warm pool, no image dispatch. For the cluster-mode worker
with --runner k8s|warm and image / service-account flags, use
sparkwing-runner.

Run against a remote controller via --on prod (or whichever profile),
or against a local 'sparkwing dashboard start' via --on local. With
--on omitted, uses the default profile from profiles.yaml.`,
	Flags: []FlagSpec{
		{Name: "on", Argument: "PROFILE", Desc: "Profile name from profiles.yaml (default: default profile)", Group: "Connection"},
		{Name: "poll", Argument: "DUR", Desc: "Claim poll interval when the queue is empty", Default: "1s", Group: "Tuning"},
		{Name: "heartbeat", Argument: "DUR", Desc: "Claim-lease heartbeat cadence", Default: "5s", Group: "Tuning"},
	},
	GroupOrder: []string{"Connection", "Tuning", "Other"},
	Examples: []Example{
		{"Run against the default profile", "sparkwing cluster worker"},
		{"Run against a named profile", "sparkwing cluster worker --on local"},
		{"Faster polling for tight dev loops", "sparkwing cluster worker --on local --poll 250ms"},
	},
}

// ---- sparkwing gc ----------------------------------------------

var cmdGC = Command{
	Path:     "sparkwing cluster gc",
	Synopsis: "Sweep stale warm-PVC state",
	Description: `Operator-facing manual invocation of the warm-PVC sweep.
Normally fires at 'wing runner' startup; exposed as a subcommand
so operators can trigger it against a running pod via kubectl
exec during incident response.

When --on is omitted, the run-directory sweep is skipped; the
mtime-based git/ and tmp/ sweeps still run and free disk. Supply
--on to enable the full sweep.`,
	Flags: []FlagSpec{
		{Name: "root", Argument: "DIR", Desc: "Warm-PVC root (default: $SPARKWING_HOME resolution)", Group: "Input"},
		{Name: "on", Argument: "NAME", Desc: "Profile name; without it run-dir sweep is skipped", Group: "System"},
	},
	Examples: []Example{
		{"mtime-only sweep in-pod (no controller)", "sparkwing cluster gc"},
		{"Full sweep against prod controller", "sparkwing cluster gc --on prod"},
		{"Target a specific warm root", "sparkwing cluster gc --root /var/lib/sparkwing --on prod"},
	},
}

// ---- sparkwing completion --------------------------------------

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

// ---- sparkwing profiles ---------------------------------------

var cmdProfiles = Command{
	Path:     "sparkwing configure profiles",
	Synopsis: "Manage connection profiles for remote controllers",
	Description: `Profile config lives at $SPARKWING_PROFILES (if set), else
$XDG_CONFIG_HOME/sparkwing/profiles.yaml, else
~/.config/sparkwing/profiles.yaml. Permissions on save are 0600.

Every human-driven client command (tokens, users, jobs
retry/cancel/prune/logs, gc) reads connection info from the
selected profile via --on NAME. No --controller/--token flags
exist on other commands; profiles are the only config surface.`,
	Subcommands: []SubcommandRef{
		{"add", "Register a new profile"},
		{"list", "Print every profile; * marks the default"},
		{"show", "Print one profile's full config"},
		{"use", "Set the default profile"},
		{"remove", "Delete a profile"},
		{"duplicate", "Copy one profile's config into another"},
		{"set", "Update fields on an existing profile"},
		{"test", "Probe controller/auth/logs/gitcache for one profile"},
	},
}

var cmdProfilesAdd = Command{
	Path:     "sparkwing configure profiles add",
	Synopsis: "Register a new connection profile",
	Description: `Creates a new entry in profiles.yaml. --name and --controller
are mandatory; the rest are optional. When this is the first
profile registered, it's auto-set as the default.`,
	Flags: []FlagSpec{
		{Name: "name", Argument: "NAME", Desc: "Profile name (unique per profiles.yaml)", Required: true, Group: "Input"},
		{Name: "controller", Argument: "URL", Desc: "Controller base URL", Required: true, Group: "Connection"},
		{Name: "logs", Argument: "URL", Desc: "Logs-service base URL", Group: "Connection"},
		{Name: "token", Argument: "TOKEN", Desc: "Bearer token (omit for local/unauthed stacks)", Group: "Connection"},
		{Name: "gitcache", Argument: "URL", Desc: "gitcache URL (fleet-worker uses this)", Group: "Connection"},
		{Name: "default", Desc: "Set this profile as the default", Group: "System"},
	},
	GroupOrder: []string{"Input", "Connection", "System", "Other"},
	Examples: []Example{
		{"Add a prod profile", "sparkwing configure profiles add --name prod --controller https://api.sparkwing.example --token $TOKEN"},
		{"Add a local profile without auth", "sparkwing configure profiles add --name local --controller http://127.0.0.1:4344"},
	},
}

var cmdProfilesList = Command{
	Path:     "sparkwing configure profiles list",
	Synopsis: "Print every registered profile",
	Description: `Prints a table of profile name, controller URL, logs URL, token
(redacted), and gitcache URL. The default profile is marked with
a leading '*'.`,
	Examples: []Example{
		{"List profiles", "sparkwing configure profiles list"},
	},
}

var cmdProfilesShow = Command{
	Path:     "sparkwing configure profiles show",
	Synopsis: "Print one profile's full config",
	Description: `Prints all fields of the profile named by --name. Token is
redacted unless --show-token is passed. Omitting --name prints
the current default profile.`,
	Flags: []FlagSpec{
		{Name: "name", Argument: "NAME", Desc: "Profile name (default: current default)", Group: "Input"},
		{Name: "show-token", Desc: "Print the raw token (redacted by default)", Group: "Output"},
	},
	GroupOrder: []string{"Input", "Output", "Other"},
	Examples: []Example{
		{"Show the default profile", "sparkwing configure profiles show"},
		{"Show a named profile with the raw token", "sparkwing configure profiles show --name prod --show-token"},
	},
}

var cmdProfilesUse = Command{
	Path:     "sparkwing configure profiles use",
	Synopsis: "Set the default profile",
	Description: `Updates profiles.yaml so commands run without --on target this
profile. The previous default is untouched beyond losing its
default status.`,
	Flags: []FlagSpec{
		{Name: "name", Argument: "NAME", Desc: "Profile name to mark as default", Required: true, Group: "Input"},
	},
	Examples: []Example{
		{"Switch the default to prod", "sparkwing configure profiles use --name prod"},
	},
}

var cmdProfilesRemove = Command{
	Path:        "sparkwing configure profiles remove",
	Synopsis:    "Delete a profile",
	Description: `Removes the entry from profiles.yaml. If the removed profile was the default, no new default is auto-picked -- operators must pass --on on every call or set one via 'sparkwing profiles use --name <X>'.`,
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
		{Name: "logs", Argument: "URL", Desc: "New logs-service URL", Group: "Connection"},
		{Name: "token", Argument: "TOKEN", Desc: "New bearer token (empty string clears)", Group: "Connection"},
		{Name: "gitcache", Argument: "URL", Desc: "New gitcache URL", Group: "Connection"},
	},
	GroupOrder: []string{"Input", "Connection", "System", "Other"},
	Examples: []Example{
		{"Rotate a profile's token", "sparkwing configure profiles set --name prod --token $NEW_TOKEN"},
		{"Clear a stale logs URL", `sparkwing profiles set --name prod --logs=""`},
	},
}

// ---- sparkwing tokens ------------------------------------------

var cmdTokens = Command{
	Path:     "sparkwing cluster tokens",
	Synopsis: "Manage controller API tokens",
	Description: `All subcommands resolve controller URL + admin bearer from the
named profile (or the default profile when --on is omitted).
Token creation prints the raw value to stdout exactly ONCE --
stash it immediately.`,
	Subcommands: []SubcommandRef{
		{"create", "Mint a new token (prints raw value once)"},
		{"list", "List token prefixes + metadata (never prints raw)"},
		{"revoke", "Mark a token revoked so further requests 401"},
		{"lookup", "Print metadata for a single token by prefix"},
		{"rotate", "Mint a replacement token with a grace window"},
	},
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
		{Name: "scope", Argument: "CSV", Desc: "Comma-separated scopes (e.g. jobs:read,jobs:write)", Group: "Input"},
		{Name: "ttl", Argument: "DURATION", Desc: "Token lifetime (e.g. 30d, 720h). 0 = never expires", Group: "Input"},
		{Name: "on", Argument: "NAME", Desc: "Profile name (default: current default)", Group: "System"},
	},
	Examples: []Example{
		{"Mint a service token with write scopes", "sparkwing cluster tokens create --type service --principal deploy-bot --scope jobs:read,jobs:write"},
		{"Mint a user token that expires in 30 days", "sparkwing cluster tokens create --type user --principal alice --scope admin --ttl 720h"},
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

Use --json (or -o json) to get a structured array with explicit
scope arrays, suitable for piping into jq.`,
	Flags: []FlagSpec{
		{Name: "type", Argument: "KIND", Desc: "Filter by token type", Group: "Filter"},
		{Name: "include-revoked", Desc: "Include revoked tokens in the output", Group: "Filter"},
		{Name: "json", Desc: "Emit JSON (alias for -o json)", Group: "Output"},
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty | json", Default: "pretty", Group: "Output"},
		{Name: "on", Argument: "NAME", Desc: "Profile name (default: current default)", Group: "System"},
	},
	Examples: []Example{
		{"List all active tokens", "sparkwing cluster tokens list"},
		{"Audit every revoked service token", "sparkwing cluster tokens list --type service --include-revoked"},
		{"Inspect the warm-runner pool token's scopes as JSON", "sparkwing cluster tokens list --on prod -o json | jq '.[] | select(.principal==\"agent:sparkwing-warm-runner\") | .scopes'"},
	},
}

var cmdTokensRevoke = Command{
	Path:        "sparkwing cluster tokens revoke",
	Synopsis:    "Mark a token revoked",
	Description: `Subsequent requests using the token receive HTTP 401. Revocation is immediate and irreversible.`,
	Flags: []FlagSpec{
		{Name: "prefix", Argument: "PREFIX", Desc: "Non-secret token prefix (from 'tokens list')", Required: true, Group: "Input"},
		{Name: "on", Argument: "NAME", Desc: "Profile name (default: current default)", Group: "System"},
	},
	Examples: []Example{
		{"Revoke a leaked token", "sparkwing cluster tokens revoke --prefix a1b2c3d4"},
	},
}

var cmdTokensLookup = Command{
	Path:        "sparkwing cluster tokens lookup",
	Synopsis:    "Print metadata for a single token",
	Description: `Prints the JSON metadata for a token given its non-secret prefix. Useful for confirming principal + scopes before revoking or rotating.`,
	Flags: []FlagSpec{
		{Name: "prefix", Argument: "PREFIX", Desc: "Non-secret token prefix", Required: true, Group: "Input"},
		{Name: "on", Argument: "NAME", Desc: "Profile name (default: current default)", Group: "System"},
	},
	Examples: []Example{
		{"Inspect a token before revoking", "sparkwing cluster tokens lookup --prefix a1b2c3d4"},
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
		{Name: "on", Argument: "NAME", Desc: "Profile name (default: current default)", Group: "System"},
	},
	Examples: []Example{
		{"Rotate a token with a 48h grace window", "sparkwing cluster tokens rotate --prefix a1b2c3d4 --grace 48h"},
	},
}

// ---- sparkwing users -------------------------------------------

var cmdUsers = Command{
	Path:     "sparkwing cluster users",
	Synopsis: "Manage dashboard login users",
	Description: `Seeds admin credentials in the controller's users table, used
by the web pod's login flow. Connection info comes from the
selected profile; --on overrides the default.`,
	Subcommands: []SubcommandRef{
		{"add", "Create a user with a password (prompts hidden on stdin)"},
		{"list", "Print every user + created_at + last_login_at"},
		{"delete", "Remove a user (active sessions stay until expiry)"},
	},
}

var cmdUsersAdd = Command{
	Path:     "sparkwing cluster users add",
	Synopsis: "Create a dashboard user",
	Description: `Prompts for a password on stdin with echo disabled when stdin
is a TTY (the password is not shown on-screen or recorded in
shell history). Passing --password skips the prompt -- useful
for CI seed flows but leaks via shell history if used
interactively.`,
	Flags: []FlagSpec{
		{Name: "name", Argument: "NAME", Desc: "Dashboard username", Required: true, Group: "Input"},
		{Name: "password", Argument: "PASSWORD", Desc: "Password (omit to prompt interactively)", Group: "Input"},
		{Name: "on", Argument: "NAME", Desc: "Profile name (default: current default)", Group: "System"},
	},
	Examples: []Example{
		{"Interactive add", "sparkwing cluster users add --name alice"},
		{"Non-interactive add for CI", `sparkwing users add --name ci-bot --password "$CI_BOT_PW"`},
	},
}

var cmdUsersList = Command{
	Path:     "sparkwing cluster users list",
	Synopsis: "Print every user",
	Description: `Prints name, created_at, and last_login_at for every user in
the controller's users table.`,
	Flags: []FlagSpec{
		{Name: "on", Argument: "NAME", Desc: "Profile name (default: current default)", Group: "System"},
	},
	Examples: []Example{
		{"List users on the default profile", "sparkwing cluster users list"},
		{"List users on prod", "sparkwing cluster users list --on prod"},
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
		{Name: "on", Argument: "NAME", Desc: "Profile name (default: current default)", Group: "System"},
	},
	Examples: []Example{
		{"Delete a user", "sparkwing cluster users delete --name alice"},
	},
}

// ---- sparkwing runs --------------------------------------------

var cmdJobs = Command{
	Path:     "sparkwing runs",
	Synopsis: "Inspect and control pipeline runs",
	Description: `Runs are the per-invocation records of pipeline execution.
Every 'wing <pipeline>' produces a run; cluster mode surfaces
the same runs remotely via the controller.

Local-mode subcommands (list, status, logs, errors) read from
~/.sparkwing/runs/. Controller-mode subcommands (cancel, retry,
prune) require a profile; 'jobs logs' supports both.`,
	Subcommands: []SubcommandRef{
		{"list", "List recent runs with filters (pipeline, status, branch, sha, search, etc.)"},
		{"status", "Show a single run's status (with per-step + approval state)"},
		{"summary", "Aggregated work view: groups, work items, modifiers, annotations"},
		{"timeline", "ASCII waterfall of a run's nodes (and steps)"},
		{"wait", "Block until a run reaches a terminal status"},
		{"find", "Find runs matching a git SHA / repo / pipeline filter"},
		{"grep", "Search log bodies across recent runs for a substring"},
		{"logs", "Print a run's logs (optionally --follow)"},
		{"errors", "Surface the error trail for a failed run"},
		{"failures", "List recent failed runs; optional clustering by step"},
		{"stats", "Aggregate stats (pass/fail, success %, avg/p95 duration)"},
		{"last", "Show the most recent run; --watch tails new runs"},
		{"tree", "ASCII tree of a run and every descendant run"},
		{"get", "Emit one run's raw JSON (run + nodes)"},
		{"receipt", "Emit a run's audit + cost receipt as JSON"},
		{"annotations", "Read or append persistent node + step annotations"},
		{"approvals", "List, approve, or deny approval gates"},
		{"triggers", "Inspect trigger envelopes that produced runs"},
		{"retry", "Trigger fresh runs copying pipeline + args from old ones"},
		{"cancel", "Request cancellation of in-flight runs"},
		{"prune", "Delete finished runs older than a threshold, or by id"},
	},
}

var cmdJobsList = Command{
	Path:     "sparkwing runs list",
	Synopsis: "List recent pipeline runs",
	Description: `Without --on, reads from the local run directory. With --on NAME,
fetches from the named profile's controller. Filters compose with
AND semantics across flag types (pipeline=X AND status=Y), OR
semantics within a repeated flag (pipeline=X OR pipeline=Y).

With -q / --quiet the output is just run ids, one per line, for
shell piping:

  sparkwing runs list --pipeline X --limit 1 -q --on prod \
      | xargs -I{} sparkwing runs logs --run {} --on prod --follow`,
	Flags: []FlagSpec{
		{Name: "pipeline", Argument: "NAME", Desc: "Filter by pipeline name (repeatable; prefix `!` to exclude)", Group: "Filter"},
		{Name: "status", Argument: "STATUS", Desc: "Filter by status: running|success|failed|cancelled (repeatable; prefix `!` to exclude)", Group: "Filter"},
		{Name: "tag", Argument: "TAG", Desc: "Filter by pipelines.yaml tag (repeatable)", Group: "Filter"},
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
		{Name: "on", Argument: "NAME", Desc: "Profile name; omit for local-only", Group: "System"},
	},
	GroupOrder: []string{"Filter", "Output", "System", "Other"},
	Examples: []Example{
		{"Last 20 local runs", "sparkwing runs list"},
		{"Failed runs in the past day", "sparkwing runs list --status failed --since 24h"},
		{"Exclude success from the list", "sparkwing runs list --status '!success' --since 24h"},
		{"Runs on main, excluding canary", "sparkwing runs list --branch main --search '-canary'"},
		{"Runs that hit a specific failure", "sparkwing runs list --error 'permission denied'"},
		{"Runs finished today", "sparkwing runs list --finished-after today"},
		{"List prod runs", "sparkwing runs list --on prod --limit 50"},
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
--on NAME to read from a remote controller.

Exit code contract: after rendering, 'jobs status' exits 0 only when
status == success. Any non-success terminal status (failed, cancelled)
exits 1; a run that is still running when the (non-follow) read
returns also exits 1. Pass --exit-zero to inspect a known-failed run
without the shell redline. For a blocking wait, use 'jobs wait'.`,
	Flags: []FlagSpec{
		{Name: "run", Argument: "RUN_ID", Desc: "Run identifier (e.g. run-20260422-142501-abcd)", Required: true, Group: "Input"},
		{Name: "follow", Short: "f", Desc: "Poll until the run reaches a terminal state", Group: "Output"},
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty|json|plain", Group: "Output"},
		{Name: "steps", Desc: "Render every step under every node (plain output). Failed / skipped / annotated nodes always include their steps; this flag forces success nodes too.", Group: "Output"},
		{Name: "exit-zero", Desc: "Return exit code 0 even when the run failed/cancelled", Group: "Output"},
		{Name: "on", Argument: "NAME", Desc: "Profile name; omit for local-only", Group: "System"},
	},
	GroupOrder: []string{"Input", "Output", "System", "Other"},
	Examples: []Example{
		{"Check a local run once", "sparkwing runs status --run run-20260422-142501-abcd"},
		{"Follow a running job to completion", "sparkwing runs status --run run-... --follow"},
		{"Inspect a known-failed run without nonzero exit", "sparkwing runs status --run run-... --exit-zero"},
		{"Expand every step on every node", "sparkwing runs status --run run-... --steps"},
		{"Check a prod run", "sparkwing runs status --run run-... --on prod"},
	},
}

var cmdJobsLogs = Command{
	Path:     "sparkwing runs logs",
	Synopsis: "Print a run's logs",
	Description: `Without --on, reads logs from the local run directory. Pass --on
NAME to read from a remote controller's logs service (profile must
carry both controller + logs URLs). Line-selection filters
(--tail/--head/--lines/--grep) apply server-side in cluster mode so
the CLI never tails giant logs over the wire.

--since D drops nodes whose StartedAt is older than now-D; useful for
runs that have been retried several times where only the newest
attempt matters. Filtering is node-level (log lines aren't
timestamped on disk).`,
	Flags: []FlagSpec{
		{Name: "run", Argument: "RUN_ID", Desc: "Run identifier", Required: true, Group: "Input"},
		{Name: "node", Argument: "NODE_ID", Desc: "Limit output to one node id", Group: "Filter"},
		{Name: "tail", Argument: "N", Desc: "Print only the last N lines", Group: "Filter"},
		{Name: "head", Argument: "N", Desc: "Print only the first N lines", Group: "Filter"},
		{Name: "lines", Argument: "A:B", Desc: "1-indexed inclusive line range", Group: "Filter"},
		{Name: "grep", Argument: "PATTERN", Desc: "Substring match (case-sensitive)", Group: "Filter"},
		{Name: "since", Argument: "DURATION", Desc: "Only include nodes that started within the last D (e.g. 5m, 1h)", Group: "Filter"},
		{Name: "tree", Desc: "Merge root + descendant runs into one stream (local only)", Group: "Filter"},
		{Name: "follow", Short: "f", Desc: "Tail the log(s) until the run terminates", Group: "Output"},
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty|json|ndjson|plain (ndjson alias for json)", Group: "Output"},
		{Name: "on", Argument: "NAME", Desc: "Profile name (omit for local-only reads)", Group: "System"},
	},
	GroupOrder: []string{"Input", "Filter", "Output", "System", "Other"},
	Examples: []Example{
		{"Read local logs", "sparkwing runs logs --run run-20260422-142501-abcd"},
		{"Last 20 lines of a remote run", "sparkwing runs logs --run run-... --on prod --tail 20"},
		{"Only the most recent attempt's output", "sparkwing runs logs --run run-... --on prod --since 5m"},
		{"Search logs for an error substring", "sparkwing runs logs --run run-... --grep 'permission denied'"},
		{"Merge a parent run with every descendant", "sparkwing runs logs --run run-... --tree"},
		{"NDJSON stream for an agent", "sparkwing runs logs --run run-... -o ndjson"},
		{"Plain text with node/step prefix", "sparkwing runs logs --run run-... -o plain"},
		{"Force the colored renderer when piping", "sparkwing runs logs --run run-... --pretty | less -R"},
	},
}

var cmdJobsErrors = Command{
	Path:        "sparkwing runs errors",
	Synopsis:    "Surface the error trail for a failed run",
	Description: `Walks the run's node DAG and prints the error chain for any node that failed. Quicker than paging through full logs when you only care about the terminal failure. Pass --on NAME to read from a remote controller.`,
	Flags: []FlagSpec{
		{Name: "run", Argument: "RUN_ID", Desc: "Run identifier", Required: true, Group: "Input"},
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty|json|plain", Group: "Output"},
		{Name: "on", Argument: "NAME", Desc: "Profile name; omit for local-only", Group: "System"},
	},
	GroupOrder: []string{"Input", "Output", "System", "Other"},
	Examples: []Example{
		{"Inspect a local failure", "sparkwing runs errors --run run-20260422-142501-abcd"},
		{"Inspect a prod failure", "sparkwing runs errors --run run-... --on prod --json"},
	},
}

// --- new verbs: failures / stats / last / tree / get -------------

var cmdJobsFailures = Command{
	Path:        "sparkwing runs failures",
	Synopsis:    "List recent failed runs, optionally clustered",
	Description: `Fetches recent runs with status=failed and extracts the first failing node's step + error message for each. --group-by clusters the output by step so a systemic failure surfaces as one row with a count.`,
	Flags: []FlagSpec{
		{Name: "pipeline", Argument: "NAME", Desc: "Restrict to one pipeline", Group: "Filter"},
		{Name: "since", Argument: "DURATION", Desc: "Only failures newer than this (e.g. 24h, 7d)", Group: "Filter"},
		{Name: "limit", Argument: "N", Desc: "Max failures to analyze", Default: "20", Group: "Filter"},
		{Name: "group-by", Argument: "KEY", Desc: "Cluster by: step | node", Group: "Output"},
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty|json|plain", Group: "Output"},
		{Name: "on", Argument: "NAME", Desc: "Profile name; omit for local-only", Group: "System"},
	},
	GroupOrder: []string{"Filter", "Output", "System", "Other"},
	Examples: []Example{
		{"Recent local failures", "sparkwing runs failures --since 24h"},
		{"Prod failures clustered by step", "sparkwing runs failures --on prod --group-by step"},
	},
}

var cmdJobsStats = Command{
	Path:        "sparkwing runs stats",
	Synopsis:    "Aggregate run counts, success %, avg + p95 duration",
	Description: `Per-pipeline aggregates across the last 500 root runs (or the --since window). In-flight runs count toward RUN (running) but do not contribute to timing percentiles.`,
	Flags: []FlagSpec{
		{Name: "pipeline", Argument: "NAME", Desc: "Restrict to one pipeline", Group: "Filter"},
		{Name: "since", Argument: "DURATION", Desc: "Only runs newer than this (e.g. 7d)", Group: "Filter"},
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty|json|plain", Group: "Output"},
		{Name: "on", Argument: "NAME", Desc: "Profile name; omit for local-only", Group: "System"},
	},
	GroupOrder: []string{"Filter", "Output", "System", "Other"},
	Examples: []Example{
		{"7-day local stats", "sparkwing runs stats --since 7d"},
		{"Prod stats as JSON", "sparkwing runs stats --on prod -o json"},
	},
}

var cmdJobsLast = Command{
	Path:        "sparkwing runs last",
	Synopsis:    "Print the most recent run",
	Description: `Shorthand for 'jobs list --limit 1' with a compact one-line output. --watch tails for new runs, reprinting whenever a newer run ID appears.`,
	Flags: []FlagSpec{
		{Name: "pipeline", Argument: "NAME", Desc: "Restrict to one pipeline", Group: "Filter"},
		{Name: "watch", Short: "w", Desc: "Tail for new runs", Group: "Output"},
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty|json|plain", Group: "Output"},
		{Name: "on", Argument: "NAME", Desc: "Profile name; omit for local-only", Group: "System"},
	},
	GroupOrder: []string{"Filter", "Output", "System", "Other"},
	Examples: []Example{
		{"Local last run", "sparkwing runs last"},
		{"Watch prod for new runs", "sparkwing runs last --on prod --watch"},
	},
}

var cmdJobsTree = Command{
	Path:        "sparkwing runs tree",
	Synopsis:    "Show a run and every descendant run as an ASCII tree",
	Description: `Walks parent_run_id links so cross-pipeline spawns (RunAndAwait) show up under their originating run. Local mode reads from SQLite; --on NAME reads from the profile's controller.`,
	Flags: []FlagSpec{
		{Name: "run", Argument: "RUN_ID", Desc: "Root run identifier", Required: true, Group: "Input"},
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty|json|plain", Group: "Output"},
		{Name: "on", Argument: "NAME", Desc: "Profile name; omit for local-only", Group: "System"},
	},
	GroupOrder: []string{"Input", "Output", "System", "Other"},
	Examples: []Example{
		{"Tree for a local run", "sparkwing runs tree --run run-20260422-142501-abcd"},
		{"Tree for a prod run as JSON", "sparkwing runs tree --run run-... --on prod -o json"},
	},
}

var cmdJobsGet = Command{
	Path:        "sparkwing runs get",
	Synopsis:    "Emit one run's raw JSON (run + nodes)",
	Description: `Prints a combined {run, nodes} JSON blob to stdout. Consumed by agents and scripts that need the full store shape rather than the summary 'status' command renders.`,
	Flags: []FlagSpec{
		{Name: "run", Argument: "RUN_ID", Desc: "Run identifier", Required: true, Group: "Input"},
		{Name: "on", Argument: "NAME", Desc: "Profile name; omit for local-only", Group: "System"},
	},
	GroupOrder: []string{"Input", "System", "Other"},
	Examples: []Example{
		{"Fetch a local run as JSON", "sparkwing runs get --run run-..."},
		{"Fetch a prod run", "sparkwing runs get --run run-... --on prod"},
	},
}

var cmdJobsReceipt = Command{
	Path:     "sparkwing runs receipt",
	Synopsis: "Emit a run's audit + cost receipt as JSON",
	Description: `Recomputes the per-run receipt from the run + nodes
rows on demand and prints it as JSON. The receipt bundles identity
hashes (pipeline_version_hash, inputs_hash, plan_hash, per-node
outputs_hash), per-step observability (durations, outcomes), and
runner-time × profile-rate compute cost.

Local mode reads from the SQLite store and uses the local profile's
cost_per_runner_hour for the cost calc. --on NAME reads from the
remote controller's receipt endpoint; in that case the controller's
configured rate (not the local profile) supplies cost.`,
	Flags: []FlagSpec{
		{Name: "run", Argument: "RUN_ID", Desc: "Run identifier", Required: true, Group: "Input"},
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: json (default)", Group: "Output"},
		{Name: "on", Argument: "NAME", Desc: "Profile name; omit for local-only", Group: "System"},
	},
	GroupOrder: []string{"Input", "Output", "System", "Other"},
	Examples: []Example{
		{"Local receipt as JSON", "sparkwing runs receipt --run run-..."},
		{"Prod receipt", "sparkwing runs receipt --run run-... --on prod"},
	},
}

// --- jobs wait / find -------------------------------------------

var cmdJobsWait = Command{
	Path:     "sparkwing runs wait",
	Synopsis: "Block until a run reaches a terminal status",
	Description: `Polls the run until its status is success / failed /
cancelled, then exits. Exit code contract:

  0   status == success
  1   status == failed or cancelled
  2   timed out before the run reached a terminal status
  3+  infrastructure error (controller unreachable, run not found, ...)

Pair with 'jobs find --wait' for the "push then find then wait" loop
described in the CLI wishlist.`,
	Flags: []FlagSpec{
		{Name: "run", Argument: "RUN_ID", Desc: "Run identifier to wait on", Required: true, Group: "Input"},
		{Name: "timeout", Argument: "DURATION", Desc: "Give up (exit 2) after this long", Default: "10m", Group: "Input"},
		{Name: "poll", Argument: "DURATION", Desc: "Poll interval", Default: "3s", Group: "Input"},
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty|json|plain", Group: "Output"},
		{Name: "on", Argument: "NAME", Desc: "Profile name (cluster mode). Omit to poll the local SQLite store.", Group: "System"},
	},
	GroupOrder: []string{"Input", "Output", "System", "Other"},
	Examples: []Example{
		{"Wait for a local run", "sparkwing runs wait --run run-20260422-142501-abcd"},
		{"Wait with a custom timeout", "sparkwing runs wait --run run-... --timeout 30m --on prod"},
		{"Tight polling on a fast run", "sparkwing runs wait --run run-... --poll 500ms --on prod"},
	},
}

var cmdJobsFind = Command{
	Path:     "sparkwing runs find",
	Synopsis: "Find runs by git SHA / repo / pipeline filter",
	Description: `Searches recent runs for a match. Use --git-sha to find
the run that was fired by a specific commit; add --pipeline to
disambiguate when multiple pipelines respond to the same push. --repo
matches the GITHUB_REPOSITORY env on the trigger (owner/name), useful
when one controller handles webhooks from multiple repos.

With --wait, blocks until at least one match appears, up to
--find-timeout. Pairs with 'jobs wait' for the push-and-follow loop:

  git push && \
  sparkwing runs find --git-sha $(git rev-parse HEAD) --pipeline X --wait --on prod -q | \
    xargs -n1 -I{} sparkwing runs wait --run {} --on prod

Exit code 0 on match, non-zero on timeout-without-match or
infrastructure error.`,
	Flags: []FlagSpec{
		{Name: "git-sha", Argument: "SHA", Desc: "Match runs whose git SHA starts with this value (prefix match)", Group: "Filter"},
		{Name: "pipeline", Argument: "NAME", Desc: "Restrict to one pipeline", Group: "Filter"},
		{Name: "repo", Argument: "OWNER/NAME", Desc: "Match trigger's GITHUB_REPOSITORY env", Group: "Filter"},
		{Name: "since", Argument: "DURATION", Desc: "Lookback window", Default: "1h", Group: "Filter"},
		{Name: "limit", Argument: "N", Desc: "Max results", Default: "20", Group: "Filter"},
		{Name: "wait", Desc: "Block until at least one match appears", Group: "Output"},
		{Name: "find-timeout", Argument: "DURATION", Desc: "Give up (nonzero exit) after this long when --wait is set", Default: "2m", Group: "Output"},
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty|json|plain", Group: "Output"},
		{Name: "quiet", Short: "q", Desc: "Print only run ids, one per line (or a JSON array of ids with -o json)", Group: "Output"},
		{Name: "on", Argument: "NAME", Desc: "Profile name (cluster mode). Omit to search the local SQLite store.", Group: "System"},
	},
	GroupOrder: []string{"Filter", "Output", "System", "Other"},
	Examples: []Example{
		{"Find a run by SHA + pipeline on prod", "sparkwing runs find --git-sha $(git rev-parse HEAD) --pipeline build-test-deploy --on prod"},
		{"Block until the matching run appears", "sparkwing runs find --git-sha abc123 --pipeline X --wait --on prod"},
		{"Pipe matching ids into jobs wait", "sparkwing runs find --git-sha abc --wait -q --on prod | xargs -n1 -I{} sparkwing runs wait --run {} --on prod"},
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
		{Name: "json", Desc: "Emit JSON (alias for -o json)", Group: "Output"},
		{Name: "pretty", Desc: "Force pretty rendering even when piped (alias for -o pretty)", Group: "Output"},
		{Name: "quiet", Short: "q", Desc: "Print only the unique matching run ids", Group: "Output"},
		{Name: "on", Argument: "NAME", Desc: "Profile name; omit for local-only", Group: "System"},
	},
	GroupOrder: []string{"Input", "Filter", "Output", "System", "Other"},
	Examples: []Example{
		{"Find every run that hit a permission-denied line in the past week", "sparkwing runs grep --pattern 'permission denied' --since 7d"},
		{"Pipe matching run ids into runs logs", "sparkwing runs grep --pattern OOMKilled --since 24h -q | xargs -I{} sparkwing runs logs --run {}"},
		{"Search prod runs as JSON for an agent", "sparkwing runs grep --pattern 'connection refused' --on prod --since 24h --json"},
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
		{Name: "json", Desc: "Emit JSON (alias for -o json)", Group: "Output"},
		{Name: "pretty", Desc: "Force pretty rendering even when piped (alias for -o pretty)", Group: "Output"},
		{Name: "on", Argument: "NAME", Desc: "Profile name; omit for local-only", Group: "System"},
	},
	GroupOrder: []string{"Input", "Output", "System", "Other"},
	Examples: []Example{
		{"Quick run rollup", "sparkwing runs summary --run run-..."},
		{"JSON for an agent", "sparkwing runs summary --run run-... --json"},
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
		{Name: "json", Desc: "Emit JSON (alias for -o json)", Group: "Output"},
		{Name: "pretty", Desc: "Force the waterfall view even when piped (alias for -o pretty)", Group: "Output"},
		{Name: "on", Argument: "NAME", Desc: "Profile name; omit for local-only", Group: "System"},
	},
	GroupOrder: []string{"Input", "Output", "System", "Other"},
	Examples: []Example{
		{"Default node waterfall", "sparkwing runs timeline --run run-..."},
		{"Expand into per-step bars", "sparkwing runs timeline --run run-... --steps"},
		{"JSON for an agent", "sparkwing runs timeline --run run-... --steps --json"},
	},
}

var cmdJobsRetry = Command{
	Path:     "sparkwing runs retry",
	Synopsis: "Trigger fresh runs copying pipeline + args from old ones",
	Description: `Issues a new trigger per source run with the same pipeline, args,
branch, and SHA. Each new run is tagged with retry_of=<old-id>.

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
		{Name: "on", Argument: "NAME", Desc: "Profile name (default: current default)", Group: "System"},
	},
	GroupOrder: []string{"Input", "System", "Other"},
	Examples: []Example{
		{"Rerun only the failed nodes", "sparkwing runs retry --failed --run run-..."},
		{"Rerun every node from scratch", "sparkwing runs retry --all --run run-..."},
		{"Rerun every recently failed run", "sparkwing runs list --status failed --since 1h -q | sparkwing runs retry --failed --run - --on prod"},
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
		{Name: "on", Argument: "NAME", Desc: "Profile name (default: current default)", Group: "System"},
	},
	GroupOrder: []string{"Input", "System", "Other"},
	Examples: []Example{
		{"Cancel one run", "sparkwing runs cancel --run run-... --on prod"},
		{"Cancel every running prod run", "sparkwing runs list --status running --on prod -q | sparkwing runs cancel --run - --on prod"},
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
		{Name: "on", Argument: "NAME", Desc: "Profile name (default: current default)", Group: "System"},
	},
	Examples: []Example{
		{"Preview what a 7-day prune would delete", "sparkwing runs prune --older-than 7d --dry-run --on prod"},
		{"Delete a few specific runs", "sparkwing runs prune --run run-A --run run-B --on prod"},
		{"Prune ids from another query", "sparkwing runs list --pipeline scratch -q | sparkwing runs prune --run - --on prod"},
	},
}

// ---- sparkwing push --------------------------------------------

var cmdPush = Command{
	Path:     "sparkwing cluster push",
	Synopsis: "Publish the current repo's HEAD to gitcache",
	Description: `Pushes the current git HEAD to the selected profile's gitcache
as a timestamped ref (local-YYYY-MM-DDTHH-MM-SSZ). Use the ref
it prints with 'sparkwing run --on <profile> --from <ref>' to
run a pipeline against uncommitted-to-upstream code without
waiting for GitHub to have it.

Only tracks committed work -- staged or unstaged changes are
NOT uploaded. Commit first (a throwaway amend is fine), push,
trigger.`,
	Flags: []FlagSpec{
		{Name: "name", Argument: "NAME", Desc: "Repo name registered with gitcache (default: basename of repo root)", Group: "Input"},
		{Name: "on", Argument: "NAME", Desc: "Profile name (default: current default)", Group: "System"},
	},
	GroupOrder: []string{"Input", "System", "Other"},
	Examples: []Example{
		{"Push the current repo's HEAD to prod's gitcache", "sparkwing cluster push --on prod"},
		{"Override the repo name (useful for forks)", "sparkwing cluster push --on prod --name my-fork"},
	},
}

// ---- sparkwing hooks -------------------------------------------

var cmdHooks = Command{
	Path:     "sparkwing pipeline hooks",
	Synopsis: "Install / uninstall git pre-commit + pre-push hooks",
	Description: `Writes small git hook scripts into the repo's .git/hooks/
directory that call 'wing <pipeline>' for every pipeline that
declares pre_commit: or pre_push: in its .sparkwing/pipelines.yaml
triggers block.

Managed hooks carry a "Installed by sparkwing" marker so
uninstall and status can tell them apart from hand-written
hooks. Existing unmanaged hooks are left alone; install skips
them with a warning.`,
	Subcommands: []SubcommandRef{
		{"install", "Write pre-commit / pre-push hooks for the enclosing repo"},
		{"uninstall", "Remove sparkwing-managed git hooks"},
		{"status", "Report which sparkwing hooks are installed"},
	},
}

var cmdHooksInstall = Command{
	Path:     "sparkwing pipeline hooks install",
	Synopsis: "Install pre-commit / pre-push git hooks from pipelines.yaml triggers",
	Description: `Discovers the enclosing .sparkwing/pipelines.yaml, reads
pre_commit / pre_push triggers, and writes one hook file per
hook name that fans out to the matching pipelines. Existing
non-sparkwing hooks are skipped so hand-written ones survive.`,
	Flags: []FlagSpec{
		{Name: "repo", Argument: "DIR", Desc: "Repo directory (default: discovered via nearest .sparkwing/)", Group: "Input"},
	},
	Examples: []Example{
		{"Install in the current repo", "sparkwing pipeline hooks install"},
		{"Install in a different repo", "sparkwing pipeline hooks install --repo /path/to/repo"},
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
	Synopsis:    "Report which sparkwing hooks are installed",
	Description: `Lists every managed hook file under .git/hooks/ along with the pipelines it invokes. Prints a hint when nothing is installed.`,
	Flags: []FlagSpec{
		{Name: "repo", Argument: "DIR", Desc: "Repo directory (default: discovered via nearest .sparkwing/)", Group: "Input"},
	},
	Examples: []Example{
		{"Show hook status", "sparkwing pipeline hooks status"},
	},
}

// ---- sparkwing secrets -----------------------------------------

var cmdSecret = Command{
	Path:     "sparkwing secrets",
	Synopsis: "Manage secrets (local dotenv or controller-stored)",
	Description: `Without --on, reads/writes the laptop dotenv at
~/.config/sparkwing/secrets.env (masked) or
~/.config/sparkwing/config.env (--plain). Used by jobs invoked
through 'wing <pipeline>' locally.

With --on PROF, reads/writes the named profile's controller.
Used for prod / staging secrets that the cluster needs at run
time. Pipelines pull a secret by listing it in the
pipelines.yaml 'secrets:' block. Raw values never transit the
CLI except via 'secrets get'.`,
	Subcommands: []SubcommandRef{
		{"set", "Store (or replace) a secret value"},
		{"get", "Print a secret's raw value to stdout"},
		{"list", "List secret names + metadata (never prints values)"},
		{"delete", "Remove a secret"},
	},
}

var cmdSecretSet = Command{
	Path:     "sparkwing secrets set",
	Synopsis: "Store (or replace) a secret value",
	Description: `Uploads --value (or the contents of --file) to the controller
under --name. Replaces any existing secret with that name.
Prefer --file for long or multi-line values so the raw text
does not land in shell history.`,
	Flags: []FlagSpec{
		{Name: "name", Type: FlagString, Argument: "NAME", Desc: "Secret name (unique per controller)", Required: true, Group: "Input"},
		{Name: "value", Type: FlagString, Argument: "VALUE", Desc: "Secret value (prefer --file for long values)", RequiredWhen: "when --file is not set", ConflictsWith: []string{"file"}, Group: "Input"},
		{Name: "file", Type: FlagString, Argument: "PATH", Desc: "Read value from file (keeps value out of shell history)", RequiredWhen: "when --value is not set", ConflictsWith: []string{"value"}, Group: "Input"},
		{Name: "plain", Type: FlagBool, Desc: "Store as non-masked config (e.g. REGION, LOG_LEVEL) -- value will NOT be redacted in run logs. Default is masked.", Group: "Input"},
		{Name: "on", Type: FlagString, Argument: "NAME", Desc: "Profile name (default: current default)", Group: "System"},
	},
	GroupOrder: []string{"Input", "System", "Other"},
	Examples: []Example{
		{"Set a masked secret (default)", "sparkwing secrets set --name API_TOKEN --value abc123 --on prod"},
		{"Set from a file", "sparkwing secrets set --name TLS_CERT --file ./tls.crt --on prod"},
		{"Set non-masked config", "sparkwing secrets set --name REGION --value us-east-1 --plain --on prod"},
	},
}

var cmdSecretGet = Command{
	Path:     "sparkwing secrets get",
	Synopsis: "Print a secret's raw value to stdout",
	Description: `Prints only the raw value (no trailing newline) so it can be
piped into another command. Use 'secrets list' for metadata.`,
	Flags: []FlagSpec{
		{Name: "name", Type: FlagString, Argument: "NAME", Desc: "Secret name", Required: true, Group: "Input"},
		{Name: "on", Type: FlagString, Argument: "NAME", Desc: "Profile name (default: current default)", Group: "System"},
	},
	GroupOrder: []string{"Input", "System", "Other"},
	Examples: []Example{
		{"Fetch a secret", "sparkwing secrets get --name API_TOKEN --on prod"},
	},
}

var cmdSecretList = Command{
	Path:        "sparkwing secrets list",
	Synopsis:    "List secret names + metadata",
	Description: `Prints a table of name, created_at, and the principal that last updated each secret. Raw values are never printed by this command.`,
	Flags: []FlagSpec{
		{Name: "grep", Type: FlagString, Argument: "PATTERN", Desc: "Filter by name substring (case-sensitive)", Group: "Filter"},
		{Name: "on", Type: FlagString, Argument: "NAME", Desc: "Profile name (default: current default)", Group: "System"},
	},
	GroupOrder: []string{"Filter", "System", "Other"},
	Examples: []Example{
		{"List secrets on prod", "sparkwing secrets list --on prod"},
		{"Filter to API-related names", "sparkwing secrets list --on prod --grep API"},
	},
}

var cmdSecretDelete = Command{
	Path:        "sparkwing secrets delete",
	Synopsis:    "Remove a secret",
	Description: `Deletes the secret row from the controller. Pipelines that reference the name will fail to resolve until the secret is re-added.`,
	Flags: []FlagSpec{
		{Name: "name", Type: FlagString, Argument: "NAME", Desc: "Secret name to remove", Required: true, Group: "Input"},
		{Name: "on", Type: FlagString, Argument: "NAME", Desc: "Profile name (default: current default)", Group: "System"},
	},
	GroupOrder: []string{"Input", "System", "Other"},
	Examples: []Example{
		{"Delete a secret", "sparkwing secrets delete --name API_TOKEN --on prod"},
	},
}

// ---- sparkwing triggers ----------------------------------------

var cmdTriggers = Command{
	Path:     "sparkwing runs triggers",
	Synopsis: "Fire, list, or inspect controller triggers",
	Description: `Triggers are the controller's queue of pending work. Every
pipeline run starts as a trigger (from a webhook, hook, 'wing
--on', or 'triggers fire') that a worker atomically claims and
turns into a run.

'fire' posts a synthetic trigger -- the sparkwing equivalent of
'gh workflow run'. 'list' surfaces queued / in-flight / done
entries so operators can see what's stuck without diving into
controller logs. 'get' inspects one trigger by id.

Connection info comes from the selected profile (--on NAME);
there are no --controller / --token flags on this command.`,
	Subcommands: []SubcommandRef{
		{"list", "List pending / claimed / done triggers"},
		{"get", "Inspect one trigger's full metadata by id"},
	},
	Examples: []Example{
		{"List pending triggers on prod", "sparkwing runs triggers list --on prod --status pending"},
		{"Inspect one trigger", "sparkwing runs triggers get --id run-... --on prod"},
		{"Fire a trigger (use pipeline run)", "sparkwing pipeline run --pipeline deploy --on prod"},
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
		{Name: "json", Desc: "Alias for -o json (hidden)", Group: "Output"},
		{Name: "on", Argument: "NAME", Desc: "Profile name (default: current default)", Required: true, Group: "System"},
	},
	GroupOrder: []string{"Filter", "Output", "System", "Other"},
	Examples: []Example{
		{"Recent triggers on prod", "sparkwing runs triggers list --on prod"},
		{"Just pending", "sparkwing runs triggers list --on prod --status pending"},
		{"Pipeline-specific, JSON", "sparkwing runs triggers list --on prod --pipeline build-test-deploy --limit 5 -o json"},
	},
}

var cmdTriggersGet = Command{
	Path:        "sparkwing runs triggers get",
	Synopsis:    "Inspect one trigger's full metadata by id",
	Description: `Fetches GET /api/v1/triggers/{id} and prints the full row (pipeline, args, git, env, status, claim lease). Defaults to a compact multi-line rendering; -o json emits the raw response.`,
	Flags: []FlagSpec{
		{Name: "id", Argument: "TRIGGER_ID", Desc: "Trigger / run identifier (same value 'fire' prints)", Required: true, Group: "Input"},
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: json emits the raw response", Group: "Output"},
		{Name: "json", Desc: "Alias for -o json (hidden)", Group: "Output"},
		{Name: "on", Argument: "NAME", Desc: "Profile name (default: current default)", Required: true, Group: "System"},
	},
	GroupOrder: []string{"Input", "Output", "System", "Other"},
	Examples: []Example{
		{"Inspect one trigger", "sparkwing runs triggers get --id run-20260422-142501-abcd --on prod"},
		{"Raw JSON for scripting", "sparkwing runs triggers get --id run-... --on prod -o json"},
	},
}

// ---- sparkwing image -------------------------------------------

var cmdImage = Command{
	Path:     "sparkwing cluster image",
	Synopsis: "Rollout helpers for images referenced by a gitops repo",
	Description: `Composite verbs that operate on the images: block of a
kustomization.yaml plus the downstream ArgoCD / kubectl dance.
Building and pushing images stays with the consumer pipeline --
this subcommand only owns the "bump tag, commit, push, sync,
wait for rollout" path.`,
	Subcommands: []SubcommandRef{
		{"rollout", "Bump a kustomization newTag, commit+push, sync ArgoCD, wait for rollout"},
	},
	Examples: []Example{
		{"Bump sparkwing-runner to a new commit tag", "sparkwing cluster image rollout --image sparkwing-runner --tag commit-abc123 --on prod --wait"},
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
  2. ~/code/gitops (the author's default layout)

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
		{Name: "on", Argument: "NAME", Desc: "Profile name. Reserved for future per-profile gitops repo + argocd context discovery.", Required: true, Group: "System"},
		{Name: "gitops-repo", Argument: "PATH", Desc: "Gitops repo path (default: ~/code/gitops)", Group: "Input"},
		{Name: "namespace", Argument: "NS", Desc: "Kubernetes namespace for rollout status + logs", Default: "sparkwing", Group: "Input"},
		{Name: "argocd-app", Argument: "NAME", Desc: "ArgoCD app name (default: derived from --image)", Group: "Input"},
		{Name: "message", Argument: "MSG", Desc: "Commit message (default: 'chore: bump <image> to <tag>')", Group: "Input"},
		{Name: "wait", Desc: "Block until 'kubectl rollout status deployment/<image>' returns", Group: "Toggles"},
		{Name: "tail-logs", Desc: "After rollout, 'kubectl logs -f -l app=<image>' until ctrl-c", Group: "Toggles"},
		{Name: "dry-run", Desc: "Print what would happen without writing, committing, pushing, or syncing", Group: "Toggles"},
	},
	GroupOrder: []string{"Input", "Toggles", "System", "Other"},
	Examples: []Example{
		{"Dry-run against the sparkwing-runner image", "sparkwing cluster image rollout --image sparkwing-runner --tag commit-abc123 --on prod --dry-run"},
		{"Bump and wait for the rollout", "sparkwing cluster image rollout --image sparkwing-runner --tag commit-abc123 --on prod --wait"},
		{"Bump, sync, wait, then tail pod logs", "sparkwing cluster image rollout --image sparkwing --tag commit-abc123 --on prod --wait --tail-logs"},
	},
}

// ---- sparkwing profiles test ----------------------------------

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
		{Name: "on", Argument: "NAME", Desc: "Profile name (default: current default)", Group: "System"},
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty | json | plain", Default: "pretty", Group: "Output"},
		{Name: "json", Desc: "Alias for --output json", Group: "Output"},
		{Name: "output", Short: "o", Argument: "FMT", Desc: "Output format (json|table)", Group: "Output"},
	},
	GroupOrder: []string{"Output", "System", "Other"},
	Examples: []Example{
		{"Probe the default profile", "sparkwing configure profiles test"},
		{"Probe a named profile", "sparkwing configure profiles test --on prod"},
		{"JSON for scripting", "sparkwing configure profiles test --on prod -o json"},
	},
}

// ---- sparkwing health -----------------------------------------

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
		{Name: "on", Argument: "NAME", Desc: "Profile name (default: current default)", Required: true, Group: "System"},
		{Name: "json", Desc: "Emit JSON (alias of -o json)", Group: "Output"},
		{Name: "output", Short: "o", Argument: "FMT", Desc: "Output format: pretty|json", Group: "Output"},
	},
	GroupOrder: []string{"Output", "System", "Other"},
	Examples: []Example{
		{"Quick-check prod", "sparkwing cluster status --on prod"},
		{"Structured output for a status dashboard", "sparkwing cluster status --on prod -o json"},
	},
}

// ---- sparkwing webhooks ---------------------------------------

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
	Subcommands: []SubcommandRef{
		{"list", "List hooks on a repo + derived pipeline name"},
		{"deliveries", "Recent deliveries for one hook, joined with trigger state"},
		{"replay", "Queue a redelivery of a specific delivery UUID"},
	},
	Examples: []Example{
		{"List hooks on a repo", "sparkwing cluster webhooks list --repo your-org/my-app --on prod"},
		{"Recent deliveries for a hook", "sparkwing cluster webhooks deliveries --repo your-org/my-app --hook 608819334 --since 1h --on prod"},
	},
}

var cmdWebhooksList = Command{
	Path:     "sparkwing cluster webhooks list",
	Synopsis: "List GitHub hooks configured on a repo",
	Description: `Calls 'gh api /repos/OWNER/NAME/hooks' and prints id, derived
pipeline, active flag, last-delivery status, and URL.

The PIPELINE column is parsed from the hook URL path
(/webhooks/github/<pipeline>). Hooks posting to the legacy
unscoped /webhooks/github endpoint render as "(unscoped,
legacy)" so operators can spot them for cleanup. Non-sparkwing
hooks render as "(non-sparkwing)".`,
	Flags: []FlagSpec{
		{Name: "repo", Argument: "OWNER/NAME", Desc: "GitHub repo (owner can be omitted if gh has a default)", Required: true, Group: "Input"},
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty | json | plain", Default: "pretty", Group: "Output"},
		{Name: "json", Desc: "Alias for --output json", Group: "Output"},
		{Name: "output", Short: "o", Argument: "FMT", Desc: "Output format (json|table)", Group: "Output"},
		{Name: "on", Argument: "NAME", Desc: "Profile name (reserved for symmetry; unused by list)", Group: "System"},
	},
	GroupOrder: []string{"Input", "Output", "System", "Other"},
	Examples: []Example{
		{"List hooks on a repo", "sparkwing cluster webhooks list --repo your-org/my-app --on prod"},
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
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty | json | plain", Default: "pretty", Group: "Output"},
		{Name: "json", Desc: "Alias for --output json", Group: "Output"},
		{Name: "output", Short: "o", Argument: "FMT", Desc: "Output format (json|table)", Group: "Output"},
		{Name: "on", Argument: "NAME", Desc: "Profile name (used for trigger/run lookups)", Required: true, Group: "System"},
	},
	GroupOrder: []string{"Input", "Filter", "Output", "System", "Other"},
	Examples: []Example{
		{"Recent deliveries for a hook", "sparkwing cluster webhooks deliveries --repo your-org/my-app --hook 608819334 --since 1h --on prod"},
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
		{Name: "on", Argument: "NAME", Desc: "Profile name (reserved; unused by replay)", Group: "System"},
	},
	GroupOrder: []string{"Input", "System", "Other"},
	Examples: []Example{
		{"Redeliver a webhook attempt", "sparkwing cluster webhooks replay --repo your-org/my-app --hook 608819334 --delivery 0ac55946-3e96-11f1-9de8-f33e32f0060f --on prod"},
	},
}

// ---- sparkwing agents -----------------------------------------

var cmdAgents = Command{
	Path:     "sparkwing cluster agents",
	Synopsis: "Inspect the controller's fleet view",
	Description: `Hits GET /api/v1/agents on the selected profile's controller.
Prints one row per agent seen claiming work in the last hour
(the controller infers agents from recent node claims; there
is no explicit registration table yet).`,
	Subcommands: []SubcommandRef{
		{"list", "Print agents (name, type, status, active jobs, last-seen, labels)"},
	},
	Examples: []Example{
		{"List prod agents", "sparkwing cluster agents list --on prod"},
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
		{Name: "on", Argument: "NAME", Desc: "Profile name", Required: true, Group: "System"},
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty | json | plain", Default: "pretty", Group: "Output"},
		{Name: "json", Desc: "Alias for --output json", Group: "Output"},
		{Name: "output", Short: "o", Argument: "FMT", Desc: "Output format (json|table)", Group: "Output"},
		{Name: "quiet", Short: "q", Desc: "Print just agent names, one per line", Group: "Output"},
	},
	GroupOrder: []string{"Output", "System", "Other"},
	Examples: []Example{
		{"List agents on prod", "sparkwing cluster agents list --on prod"},
		{"Just agent names for piping", "sparkwing cluster agents list --on prod -q"},
	},
}

// ---- sparkwing pipeline sparks ------------------------------------------

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
	Subcommands: []SubcommandRef{
		{"list", "Show declared libraries and resolved versions"},
		{"lint", "Validate a spark.json library manifest"},
		{"resolve", "Resolve versions and materialize the overlay modfile"},
		{"update", "Re-resolve one or all libraries"},
		{"add", "Add a library to sparks.yaml"},
		{"remove", "Remove a library from sparks.yaml"},
		{"warmup", "Pre-compile pipeline binaries and upload to gitcache"},
	},
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
		{Name: "json", Desc: "Emit JSON (hidden alias for -o json)", Group: "Output"},
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
author, packages), that each packages[] path exists as a
directory under the module root, stability values are valid,
and package paths are not duplicated. Unknown fields are a
soft warning, not an error. Exits non-zero on any hard
failure.`,
	Flags: []FlagSpec{
		{Name: "path", Argument: "PATH", Desc: "Library directory or direct spark.json path", Default: ".", Group: "Input"},
	},
	GroupOrder: []string{"Input", "Other"},
	Examples: []Example{
		{"Lint the library in the current directory", "sparkwing pipeline sparks lint"},
		{"Lint a sibling library by path", "sparkwing pipeline sparks lint --path ~/code/sparks-core"},
	},
}

var cmdSparksResolve = Command{
	Path:     "sparkwing pipeline sparks resolve",
	Synopsis: "Resolve versions and materialize the overlay modfile",
	Description: `Runs the same pipeline as 'wing <name>' takes before compile:
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
upload to gitcache so the next 'wing' run in-cluster or on a
fresh laptop gets a cache hit instead of paying the full
compile cost.

Uses the exact same build path as 'wing', so the cache key
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

// ---- sparkwing approvals / approve / deny ------------------------

var cmdApprove = Command{
	Path:     "sparkwing runs approvals approve",
	Synopsis: "Approve a pending approval-gate node",
	Description: `Resolves the named approval gate as 'approved'. The gate's
downstream nodes begin dispatching on the next orchestrator
poll (roughly 500ms). The approver is recorded from the
authenticated principal when --on is set, or from $USER in
local mode.

Exit code is 0 on success, non-zero if the gate doesn't exist
or was already resolved (409).`,
	Flags: []FlagSpec{
		{Name: "run", Argument: "ID", Desc: "Run ID holding the approval gate", Required: true, Group: "Target"},
		{Name: "node", Argument: "ID", Desc: "Node ID of the approval gate", Required: true, Group: "Target"},
		{Name: "comment", Argument: "STR", Desc: "Optional note recorded on the approval", Group: "Input"},
		{Name: "on", Argument: "NAME", Desc: "Profile name; omit for local-only", Group: "System"},
	},
	GroupOrder: []string{"Target", "Input", "System", "Other"},
	Examples: []Example{
		{"Approve a local gate", "sparkwing approve --run run-20260423-143012-abcd --node approve-prod"},
		{"Approve a prod gate with a comment", `sparkwing approve --run run-... --node approve-prod --on prod --comment "release notes ok"`},
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
		{Name: "on", Argument: "NAME", Desc: "Profile name; omit for local-only", Group: "System"},
	},
	GroupOrder: []string{"Target", "Input", "System", "Other"},
	Examples: []Example{
		{"Deny a local gate", "sparkwing deny --run run-20260423-143012-abcd --node approve-prod"},
		{"Deny a prod gate with a reason", `sparkwing deny --run run-... --node approve-prod --on prod --comment "tests still red"`},
	},
}

var cmdApprovals = Command{
	Path:     "sparkwing runs approvals",
	Synopsis: "List approval gates (pending and history)",
	Description: `Inspect approval gates. Without --run returns every pending
gate across all runs; with --run returns one run's full history
(pending + resolved).`,
	Subcommands: []SubcommandRef{
		{"list", "List pending approvals, or one run's history with --run"},
	},
}

var cmdApprovalsList = Command{
	Path:     "sparkwing runs approvals list",
	Synopsis: "List pending approvals (or one run's history)",
	Description: `Prints a table of approval rows. Without --run the list is the
cross-run pending queue; with --run it's every approval (pending
+ resolved) for that run.`,
	Flags: []FlagSpec{
		{Name: "run", Argument: "RUN_ID", Desc: "Restrict to one run's approvals", Group: "Filter"},
		{Name: "output", Short: "o", Argument: "FORMAT", Desc: "Output format: pretty|json|plain", Group: "Output"},
		{Name: "on", Argument: "NAME", Desc: "Profile name; omit for local-only", Group: "System"},
	},
	GroupOrder: []string{"Filter", "Output", "System", "Other"},
	Examples: []Example{
		{"Pending gates on the local store", "sparkwing runs approvals list"},
		{"Pending gates on prod", "sparkwing runs approvals list --on prod"},
		{"Full history for one run", "sparkwing runs approvals list --run run-..."},
		{"Emit JSON for an agent", "sparkwing runs approvals list -o json"},
	},
}

// ---- sparkwing runs annotations --------------------------------

var cmdAnnotations = Command{
	Path:     "sparkwing runs annotations",
	Synopsis: "Read or append persistent node + step annotations",
	Description: `Annotations are short summary strings that pipelines (via
sparkwing.Annotate) and agents append to a node or step during a
run. They show up on the dashboard alongside outcome. This verb
lets an agent read every annotation on a run or contribute one
without going through the SDK.`,
	Subcommands: []SubcommandRef{
		{"list", "List annotations on a run (optionally filtered to a node/step)"},
		{"add", "Append one annotation to a node or step"},
	},
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
		{Name: "on", Argument: "NAME", Desc: "Profile name; omit for local-only", Group: "System"},
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
		{Name: "on", Argument: "NAME", Desc: "Profile name; omit for local-only", Group: "System"},
	},
	GroupOrder: []string{"Input", "System", "Other"},
	Examples: []Example{
		{"Note something on a node", "sparkwing runs annotations add --run run-... --node deploy -m 'agent: retried after 502'"},
		{"Note something on a step inside a node", "sparkwing runs annotations add --run run-... --node deploy --step canary -m 'rolled out 5%'"},
	},
}
