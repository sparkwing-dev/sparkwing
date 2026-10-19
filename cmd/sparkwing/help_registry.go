package main

import (
	"fmt"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
)

func surfaceHelp(surface clisurface.Command) Command {
	command := Command{
		Path:        surface.Path,
		Synopsis:    commandSynopses[surface.Path],
		Description: strings.Join(surface.Rules, "\n\n"),
		Hidden:      surface.Hidden,
	}
	switch surface.Path {
	case "sparkwing":
		command.Description = "Run pipelines locally with run, or submit them to a controller with pipeline trigger. Use commands --search to find commands and --help for their flags."
		command.Examples = []Example{{"Run a pipeline locally", "sparkwing run build"}, {"Inspect a pipeline", "sparkwing pipeline describe --pipeline build"}, {"Start the local dashboard", "sparkwing dashboard start"}}
	case "sparkwing commands":
		command.Description = "Search command paths and summaries. Lists return bounded pages and a next_cursor when more matches exist. Markdown export includes the full selected command tree."
		command.Examples = []Example{{"Find status commands", "sparkwing commands --search status"}, {"Export the reference", "sparkwing commands --format markdown --output plain"}}
	}
	if command.Synopsis == "" {
		command.Synopsis = strings.TrimPrefix(surface.Path, "sparkwing ")
	}
	if surface.PipelineArgument {
		command.PosArgs = []PosArg{{Name: "<pipeline>", Desc: "Pipeline name", Required: !surface.PipelineOptionalOnResume}}
	}
	if surface.PipelineArgsAfterSeparator {
		command.UsageSuffix = "[-- <pipeline args>...]"
	}
	for _, flag := range append(clisurface.GlobalFlags(), surface.Flags...) {
		spec := surfaceFlag(flag)
		if surface.Path == "sparkwing commands" && flag.Name == "path" {
			spec.Desc = "Command path prefix, with or without the sparkwing binary name."
		}
		if flag.Name == "token" && flag.Argument == "TOKEN" {
			spec.Desc = "Controller credential."
		}
		command.Flags = append(command.Flags, spec)
		if flag.Name == "profile" {
			command.ReachesController = true
		}
	}
	return command
}

func surfaceFlag(flag clisurface.Flag) FlagSpec {
	spec := FlagSpec{
		Name: flag.Name, Short: flag.Short, Argument: flag.Argument,
		Desc: flag.Meaning, Required: flag.Required,
		RequiresFlags: flag.Requires, ConflictsWith: flag.Conflicts,
		Type: FlagString,
	}
	if spec.Desc == "" {
		spec.Desc = clisurface.CommonRules()[flag.Name]
	}
	if spec.Desc == "" {
		spec.Desc = flagDescriptions[flag.Name]
	}
	if spec.Desc == "" {
		spec.Desc = strings.ReplaceAll(flag.Name, "-", " ")
	}
	if len(flag.Values) > 0 {
		spec.Desc += " Values: " + strings.Join(flag.Values, ", ") + "."
	}
	if flag.HasMinimum && flag.Name != "limit" {
		spec.Desc += fmt.Sprintf(" Must be at least %d.", flag.Minimum)
	}
	switch {
	case flag.Argument == "":
		spec.Type = FlagBool
	case flag.Repeatable:
		spec.Type = FlagStringSlice
		spec.Desc += " Repeatable; comma-separated values are accepted."
	case flag.Argument == "N" || flag.Argument == "START_LINE":
		spec.Type = FlagInt
	case flag.Argument == "DURATION":
		spec.Type = FlagDuration
	}
	return spec
}

func surfaceCommand(path string) Command {
	for _, command := range clisurface.Registry() {
		if command.Path == path {
			return surfaceHelp(command)
		}
	}
	panic("unregistered command: " + path)
}

var flagDescriptions = map[string]string{
	"pipeline":            "Pipeline name.",
	"job":                 "Job within the selected run.",
	"step":                "Step within the selected job.",
	"schedule":            "Schedule name.",
	"secret":              "Secret name.",
	"variable":            "Store an unmasked variable instead of a secret.",
	"runner":              "Runner name or selection.",
	"pool":                "Runner pool name.",
	"token":               "Token identifier or prefix.",
	"format":              "Artifact format; markdown exports the full command reference.",
	"split-dir":           "Directory for one generated Markdown page per command family.",
	"path":                "Path to select.",
	"cursor":              "Continue from next_cursor using the same filters.",
	"shell":               "Shell for completion output.",
	"section":             "First line of the documentation section.",
	"topic":               "Documentation topic.",
	"body":                "Include document bodies in search results.",
	"full":                "Read the full documentation topic.",
	"publish":             "Copy this local run's results to the named controller profile.",
	"sw-cache-from":       "Read authorized cache entries from the named controller profile; empty disables the machine default.",
	"sw-no-memo":          "Execute jobs without reusing memoized results.",
	"sw-allow-risk":       "Authorize the listed pipeline risks.",
	"stay":                "Keep the local controller running until stopped.",
	"sw-ref":              "Build the pipeline and run against this Git ref.",
	"sw-pipeline-ref":     "Load the pipeline from this Git ref.",
	"sw-pause-before":     "Pause before the selected job.",
	"sw-pause-after":      "Pause after the selected job.",
	"sw-pause-on-failure": "Keep failed jobs paused for inspection.",
	"sw-detached":         "Submit work to the local controller and return before it finishes.",
	"sw-idempotency-key":  "Deduplicate detached submissions using this key.",
	"sw-request-id":       "Request identifier for a detached submission.",
	"sw-output":           "Format for run output.",
	"sw-verbose":          "Include detailed run output.",
	"sw-start-at":         "Start execution at the selected step.",
	"sw-stop-at":          "Stop execution after the selected step.",
	"sw-only":             "Select jobs matching this pattern.",
	"sw-priority":         "Scheduling priority for this run.",
	"sw-no-update":        "Skip automatic updates for this invocation.",
	"sw-no-bincache":      "Build without reusing the local pipeline binary cache.",
	"sw-hash-all-files":   "Include all files when computing the local binary cache key.",
	"sw-allow-unadmitted": "Use an exclusive in-process local controller for CI admission.",
	"sw-dry-run":          "Preview the run without executing its jobs.",
	"sw-index":            "Capture a Git index for local staged-content checks.",
	"sw-run-handle-file":  "Write the submitted run handle to this file.",
	"controller":          "Controller URL for this connection profile.",
	"token-stdin":         "Read the controller credential from standard input.",
	"scope":               "Credential scope.",
	"no-probe":            "Skip checking the controller connection.",
	"keep-token":          "Retain the credential when removing the profile.",
}

var commandSynopses = map[string]string{
	"sparkwing operator teams":                      "Create teams under operator authority",
	"sparkwing operator teams show":                 "Show a team and its protected node label",
	"sparkwing operator teams create":               "Create a team and issue its first administrator credential",
	"sparkwing":                                     "Run and inspect pipelines",
	"sparkwing __complete":                          "Print completion candidates for the shell scripts",
	"sparkwing __host-trigger":                      "Host one claimed local trigger's run (spawned by the trigger loop)",
	"sparkwing __tick":                              "Evaluate every armed schedule once (the OS timer's entry point)",
	"sparkwing cache":                               "Inspect or trim the compiled pipeline binary cache",
	"sparkwing cache explain":                       "Show a pipeline's cache key and the inputs behind it",
	"sparkwing cache info":                          "Print cache dir, size, ceilings, and recent entries",
	"sparkwing cache prune":                         "Evict least recently used binaries down to the ceilings",
	"sparkwing cache warm":                          "Prepare the local pipeline cache and dependencies",
	"sparkwing commands":                            "Search the command index or export the CLI reference",
	"sparkwing completion":                          "Emit a shell completion script",
	"sparkwing daemon":                              "Inspect and manage the local controller",
	"sparkwing daemon events":                       "Read retained local controller events",
	"sparkwing daemon recover-state":                "Preserve unreadable daemon state after its holders stop",
	"sparkwing daemon restart":                      "Restart an answering local controller",
	"sparkwing daemon status":                       "Report local controller status",
	"sparkwing daemon stop":                         "Stop the local controller",
	"sparkwing dashboard":                           "Manage the local dashboard",
	"sparkwing dashboard install":                   "Install a dashboard service that starts at login",
	"sparkwing dashboard logs":                      "Read a bounded dashboard log tail",
	"sparkwing dashboard open":                      "Print a one-time sign-in link for the local dashboard",
	"sparkwing dashboard start":                     "Start the local dashboard and controller on demand",
	"sparkwing dashboard status":                    "Report whether the dashboard is running",
	"sparkwing dashboard stop":                      "Stop a running dashboard server",
	"sparkwing dashboard uninstall":                 "Remove the dashboard login service",
	"sparkwing debug":                               "Interactive debugging for pipeline runs",
	"sparkwing debug attach":                        "kubectl exec into a paused job's pod (cluster mode)",
	"sparkwing debug env":                           "Inspect public job, executor and pause diagnostics",
	"sparkwing debug resume":                        "Resume a paused job",
	"sparkwing docs":                                "Embedded user docs (offline)",
	"sparkwing docs list":                           "Enumerate every doc topic",
	"sparkwing docs migrations":                     "Per-version migration guides (agent-friendly)",
	"sparkwing docs read":                           "Read one document",
	"sparkwing docs search":                         "Find the section that answers a question",
	"sparkwing doctor":                              "Inspect local controller and machine diagnostics",
	"sparkwing examples":                            "Read complete example pipelines",
	"sparkwing info":                                "Describe Sparkwing and the selected project",
	"sparkwing operator":                            "Manage deployment operations",
	"sparkwing operator limits":                     "Manage deployment operational ceilings",
	"sparkwing operator limits set":                 "Set a deployment operational ceiling",
	"sparkwing operator limits show":                "Print every compute guard and visible runner usage",
	"sparkwing operator object-store":               "Operate the controller's object-store request budget",
	"sparkwing operator object-store reset-breaker": "Clear a tripped object-store budget or ceiling freeze",
	"sparkwing operator object-store status":        "Show the controller's object-store request budget",
	"sparkwing operator secrets":                    "Maintain the controller's sealed values",
	"sparkwing operator secrets rotate":             "Reseal every stored secret and key under the current key",
	"sparkwing operator tokens":                     "Manage deployment service credentials",
	"sparkwing operator tokens create":              "Issue a deployment service credential",
	"sparkwing operator tokens list":                "List deployment service credentials",
	"sparkwing operator tokens revoke":              "Revoke a deployment service credential",
	"sparkwing operator tokens rotate":              "Rotate a deployment service credential",
	"sparkwing pipeline":                            "This repo's pipelines",
	"sparkwing pipeline describe":                   "Print one pipeline's full metadata",
	"sparkwing pipeline hooks":                      "Install / uninstall git pre-commit + pre-push + post-commit hooks",
	"sparkwing pipeline hooks install":              "Install pre-commit / pre-push / post-commit git hooks from sparkwing.yaml triggers",
	"sparkwing pipeline hooks status":               "Report declared, installed, and missing sparkwing hooks",
	"sparkwing pipeline hooks uninstall":            "Remove sparkwing-managed git hooks",
	"sparkwing pipeline lint":                       "Check pipeline source for idiomatic anti-patterns (enforced gate)",
	"sparkwing pipeline list":                       "Enumerate every pipeline with metadata",
	"sparkwing pipeline new":                        "Scaffold a new Go pipeline",
	"sparkwing pipeline plan":                       "Render the runtime-resolved DAG without dispatching any jobs",
	"sparkwing pipeline sparks":                     "Manage sparks libraries declared in .sparkwing/sparkwing.yaml",
	"sparkwing pipeline sparks add":                 "Add a library to the sparks: block",
	"sparkwing pipeline sparks catalog":             "List the blocks a spark library offers",
	"sparkwing pipeline sparks inflate":             "Copy a spark library's source into this repo so you can edit it",
	"sparkwing pipeline sparks lint":                "Validate a spark.json library manifest",
	"sparkwing pipeline sparks list":                "Show declared sparks libraries and their resolved versions",
	"sparkwing pipeline sparks remove":              "Remove a library from the sparks: block",
	"sparkwing pipeline sparks update":              "Re-resolve every declared library",
	"sparkwing pipeline trigger":                    "Submit a pipeline run to a controller",
	"sparkwing overflow":                            "Manage your Cloud overflow allowance",
	"sparkwing billing":                             "Inspect Cloud standing and request a higher tier",
	"sparkwing billing standing":                    "Show the team's Cloud standing and latest tier request",
	"sparkwing billing request-tier":                "Ask the operators to grant High standing",
	"sparkwing team":                                "Manage your team",
	"sparkwing team settings":                       "Manage team settings",
	"sparkwing team settings show":                  "Show the team's machine enrollment setting",
	"sparkwing team settings set":                   "Change the team's machine enrollment setting",
	"sparkwing overflow show":                       "Show your monthly overflow allowance and usage",
	"sparkwing overflow set":                        "Set or remove your monthly overflow cap",
	"sparkwing pools":                               "Manage team runner pools",
	"sparkwing pools allow":                         "Allow a repository and branch to use a pool",
	"sparkwing pools disallow":                      "Remove a pool access rule",
	"sparkwing pools list":                          "List runner pools",
	"sparkwing profiles":                            "Manage controller connection profiles",
	"sparkwing profiles add":                        "Add a controller connection profile",
	"sparkwing profiles list":                       "Print every registered profile",
	"sparkwing profiles remove":                     "Remove a controller connection profile",
	"sparkwing profiles set":                        "Update fields on an existing profile",
	"sparkwing profiles show":                       "Print one controller connection profile",
	"sparkwing queue":                               "Inspect queued work and scheduling priority",
	"sparkwing queue list":                          "List running and queued work with expected start and finish",
	"sparkwing queue priority":                      "Change the priority of queued work",
	"sparkwing repos":                               "The machine's fleet of sparkwing repos and their SDK pins",
	"sparkwing repos add":                           "Register a checkout",
	"sparkwing repos info":                          "Inspect repository versions, worktrees, and pipelines",
	"sparkwing repos list":                          "List the machine's fleet of sparkwing repos",
	"sparkwing repos prune":                         "Remove checkouts whose pipeline directory is gone",
	"sparkwing repos remove":                        "Remove a registered checkout",
	"sparkwing repos update":                        "Update repository SDK versions and compare pipeline plans",
	"sparkwing run":                                 "Invoke a pipeline",
	"sparkwing runners":                             "Manage runners",
	"sparkwing runners add":                         "Register a runner",
	"sparkwing runners automation":                  "Request or approve a personal machine's automated runs",
	"sparkwing runners list":                        "List runners and their capacity",
	"sparkwing runners remove":                      "Remove a runner",
	"sparkwing runs":                                "Inspect and control pipeline runs",
	"sparkwing runs annotations":                    "Read or append persistent job + step annotations",
	"sparkwing runs annotations add":                "Append an annotation to a job or step",
	"sparkwing runs annotations list":               "List annotations on a run",
	"sparkwing runs approvals":                      "List approval gates (pending and history)",
	"sparkwing runs approvals approve":              "Approve a pending approval-gate job",
	"sparkwing runs approvals deny":                 "Deny a pending approval-gate job",
	"sparkwing runs approvals list":                 "List pending approvals (or one run's history)",
	"sparkwing runs cancel":                         "Request cancellation of in-flight runs",
	"sparkwing runs grep":                           "Search run log text",
	"sparkwing runs list":                           "List recent pipeline runs",
	"sparkwing runs logs":                           "Print a run's logs",
	"sparkwing runs prune":                          "Delete finished runs older than a threshold, or by id",
	"sparkwing runs retry":                          "Trigger fresh runs copying pipeline + args from old ones",
	"sparkwing runs stats":                          "Report run counts, success rate, and duration percentiles",
	"sparkwing runs status":                         "Read one run's status, attempts, notices and selected view",
	"sparkwing schedules":                           "Manage scheduled pipeline runs",
	"sparkwing schedules install":                   "Arm a repo's declared schedules on this host and install the OS timer",
	"sparkwing schedules list":                      "List the schedules armed on this host",
	"sparkwing schedules pause":                     "Pause a schedule",
	"sparkwing schedules resume":                    "Resume a paused schedule",
	"sparkwing schedules run":                       "Launch a schedule's pipeline now",
	"sparkwing schedules set":                       "Override a declared cadence on this host",
	"sparkwing schedules show":                      "Show one schedule's full record and its recent fires",
	"sparkwing schedules uninstall":                 "Disarm a repo's schedules, and remove the timer when nothing is left",
	"sparkwing secrets":                             "Manage secrets in this machine's local store or on a controller",
	"sparkwing secrets delete":                      "Remove a secret",
	"sparkwing secrets get":                         "Read a secret's value",
	"sparkwing secrets list":                        "List secret names + metadata",
	"sparkwing secrets rotate":                      "Re-encrypt every stored secret under the current key",
	"sparkwing secrets set":                         "Store (or replace) a secret value",
	"sparkwing tokens":                              "Manage team credentials",
	"sparkwing tokens create":                       "Mint a new API token",
	"sparkwing tokens list":                         "List token prefixes + metadata",
	"sparkwing tokens revoke":                       "Mark a token revoked",
	"sparkwing tokens rotate":                       "Mint a replacement token with a grace window",
	"sparkwing update":                              "Update the CLI binary",
	"sparkwing upgrade":                             "Upgrade repository SDK pins",
	"sparkwing version":                             "Inspect versions (CLI, SDK, sparks)",
	"sparkwing version hold":                        "Show, set, or clear the operator ceiling on CLI upgrades",
	"sparkwing wingd":                               "Host the local controller (spawned by the CLI)",
	"sparkwing wingd run":                           "Serve the local controller in this process",
}

var (
	cmdSparkwing          = surfaceCommand("sparkwing")
	cmdDaemon             = surfaceCommand("sparkwing daemon")
	cmdDaemonEvents       = surfaceCommand("sparkwing daemon events")
	cmdDaemonStop         = surfaceCommand("sparkwing daemon stop")
	cmdDaemonRecoverState = surfaceCommand("sparkwing daemon recover-state")
	cmdDaemonStatus       = surfaceCommand("sparkwing daemon status")
	cmdDaemonRestart      = surfaceCommand("sparkwing daemon restart")
	cmdInfo               = surfaceCommand("sparkwing info")
	cmdVersion            = surfaceCommand("sparkwing version")
	cmdVersionHold        = surfaceCommand("sparkwing version hold")
	cmdUpdate             = surfaceCommand("sparkwing update")
	cmdCommands           = surfaceCommand("sparkwing commands")
	cmdQueue              = surfaceCommand("sparkwing queue")
	cmdDocs               = surfaceCommand("sparkwing docs")
	cmdDocsList           = surfaceCommand("sparkwing docs list")
	cmdDocsRead           = surfaceCommand("sparkwing docs read")
	cmdDocsSearch         = surfaceCommand("sparkwing docs search")
	cmdDocsMigrations     = surfaceCommand("sparkwing docs migrations")
	cmdCache              = surfaceCommand("sparkwing cache")
	cmdCacheInfo          = surfaceCommand("sparkwing cache info")
	cmdCachePrune         = surfaceCommand("sparkwing cache prune")
	cmdCacheExplain       = surfaceCommand("sparkwing cache explain")
	cmdDebug              = surfaceCommand("sparkwing debug")
	cmdDebugAttach        = surfaceCommand("sparkwing debug attach")
	cmdDebugEnv           = surfaceCommand("sparkwing debug env")
	cmdPipeline           = surfaceCommand("sparkwing pipeline")
	cmdPipelineTrigger    = surfaceCommand("sparkwing pipeline trigger")
	cmdPipelineList       = surfaceCommand("sparkwing pipeline list")
	cmdPipelineDescribe   = surfaceCommand("sparkwing pipeline describe")
	cmdPipelineNew        = surfaceCommand("sparkwing pipeline new")
	cmdExamples           = surfaceCommand("sparkwing examples")
	cmdPipelineLint       = surfaceCommand("sparkwing pipeline lint")
	cmdPipelinePlan       = surfaceCommand("sparkwing pipeline plan")
	cmdRun                = surfaceCommand("sparkwing run")
	cmdDashboard          = surfaceCommand("sparkwing dashboard")
	cmdDashboardStart     = surfaceCommand("sparkwing dashboard start")
	cmdDashboardStop      = surfaceCommand("sparkwing dashboard stop")
	cmdDashboardLogs      = surfaceCommand("sparkwing dashboard logs")
	cmdDashboardStatus    = surfaceCommand("sparkwing dashboard status")
	cmdDoctor             = surfaceCommand("sparkwing doctor")
	cmdCompletion         = surfaceCommand("sparkwing completion")
	cmdProfiles           = surfaceCommand("sparkwing profiles")
	cmdProfilesAdd        = surfaceCommand("sparkwing profiles add")
	cmdProfilesRemove     = surfaceCommand("sparkwing profiles remove")
	cmdProfilesList       = surfaceCommand("sparkwing profiles list")
	cmdProfilesShow       = surfaceCommand("sparkwing profiles show")
	cmdProfilesSet        = surfaceCommand("sparkwing profiles set")
	cmdTokens             = surfaceCommand("sparkwing tokens")
	cmdTokensCreate       = surfaceCommand("sparkwing tokens create")
	cmdTokensList         = surfaceCommand("sparkwing tokens list")
	cmdTokensRevoke       = surfaceCommand("sparkwing tokens revoke")
	cmdTokensRotate       = surfaceCommand("sparkwing tokens rotate")
	cmdJobs               = surfaceCommand("sparkwing runs")
	cmdJobsList           = surfaceCommand("sparkwing runs list")
	cmdJobsStatus         = surfaceCommand("sparkwing runs status")
	cmdJobsLogs           = surfaceCommand("sparkwing runs logs")
	cmdJobsStats          = surfaceCommand("sparkwing runs stats")
	cmdJobsRetry          = surfaceCommand("sparkwing runs retry")
	cmdJobsCancel         = surfaceCommand("sparkwing runs cancel")
	cmdJobsPrune          = surfaceCommand("sparkwing runs prune")
	cmdHooks              = surfaceCommand("sparkwing pipeline hooks")
	cmdHooksInstall       = surfaceCommand("sparkwing pipeline hooks install")
	cmdHooksUninstall     = surfaceCommand("sparkwing pipeline hooks uninstall")
	cmdHooksStatus        = surfaceCommand("sparkwing pipeline hooks status")
	cmdRunners            = surfaceCommand("sparkwing runners")
	cmdRunnersAdd         = surfaceCommand("sparkwing runners add")
	cmdRunnersRemove      = surfaceCommand("sparkwing runners remove")
	cmdSparks             = surfaceCommand("sparkwing pipeline sparks")
	cmdSparksList         = surfaceCommand("sparkwing pipeline sparks list")
	cmdSparksLint         = surfaceCommand("sparkwing pipeline sparks lint")
	cmdSparksUpdate       = surfaceCommand("sparkwing pipeline sparks update")
	cmdSparksAdd          = surfaceCommand("sparkwing pipeline sparks add")
	cmdSparksRemove       = surfaceCommand("sparkwing pipeline sparks remove")
	cmdCacheWarm          = surfaceCommand("sparkwing cache warm")
	cmdSparksCatalog      = surfaceCommand("sparkwing pipeline sparks catalog")
	cmdApprove            = surfaceCommand("sparkwing runs approvals approve")
	cmdDeny               = surfaceCommand("sparkwing runs approvals deny")
	cmdApprovals          = surfaceCommand("sparkwing runs approvals")
	cmdApprovalsList      = surfaceCommand("sparkwing runs approvals list")
	cmdAnnotations        = surfaceCommand("sparkwing runs annotations")
	cmdAnnotationsList    = surfaceCommand("sparkwing runs annotations list")
	cmdAnnotationsAdd     = surfaceCommand("sparkwing runs annotations add")
	cmdRepos              = surfaceCommand("sparkwing repos")
	cmdReposList          = surfaceCommand("sparkwing repos list")
	cmdReposAdd           = surfaceCommand("sparkwing repos add")
	cmdReposRemove        = surfaceCommand("sparkwing repos remove")
	cmdReposPrune         = surfaceCommand("sparkwing repos prune")
	cmdReposInfo          = surfaceCommand("sparkwing repos info")
	cmdReposUpdate        = surfaceCommand("sparkwing repos update")
	cmdCrons              = surfaceCommand("sparkwing schedules")
	cmdCronsInstall       = surfaceCommand("sparkwing schedules install")
	cmdCronsUninstall     = surfaceCommand("sparkwing schedules uninstall")
	cmdCronsSet           = surfaceCommand("sparkwing schedules set")
	cmdCronsList          = surfaceCommand("sparkwing schedules list")
	cmdCronsShow          = surfaceCommand("sparkwing schedules show")
	cmdCronsRun           = surfaceCommand("sparkwing schedules run")
	cmdCronsTick          = surfaceCommand("sparkwing __tick")
	cmdWingd              = surfaceCommand("sparkwing wingd")
	cmdWingdRun           = surfaceCommand("sparkwing wingd run")
	cmdTriggerHost        = surfaceCommand("sparkwing __host-trigger")
	cmdComplete           = surfaceCommand("sparkwing __complete")
)
