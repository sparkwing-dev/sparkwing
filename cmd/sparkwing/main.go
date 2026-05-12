// The sparkwing binary. When invoked as "wing" (symlink or renamed
// copy) it dispatches to the repo's local .sparkwing/ pipeline
// runner. Otherwise it exposes infrastructure and observation
// subcommands.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/controller/client"
	"github.com/sparkwing-dev/sparkwing/orchestrator"
	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
	"github.com/sparkwing-dev/sparkwing/pkg/color"
	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/sparkwinglogs"
	"github.com/sparkwing-dev/sparkwing/pkg/wingconfig"
	"github.com/sparkwing-dev/sparkwing/profile"
	"github.com/sparkwing-dev/sparkwing/repos"
)

func main() {
	// Windows self-update defers deletion of the running binary; clean it up here.
	cleanupStaleUpdate()

	base := strings.TrimSuffix(filepath.Base(os.Args[0]), ".exe")
	if base == "wing" {
		if err := runWing(os.Args[1:]); err != nil {
			fmt.Fprintln(os.Stderr, color.Red(color.Bold("wing error:")), err)
			os.Exit(exitCodeFor(err))
		}
		return
	}
	if err := runSparkwing(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, color.Red(color.Bold("sparkwing error:")), err)
		os.Exit(exitCodeFor(err))
	}
}

// cliError carries an explicit exit code so verbs can distinguish
// "outcome = failed" from "timed out" from "infrastructure error".
type cliError struct {
	code int
	err  error
}

func (e *cliError) Error() string {
	if e == nil || e.err == nil {
		return ""
	}
	return e.err.Error()
}

func (e *cliError) Unwrap() error { return e.err }

func exitErrorf(code int, format string, args ...any) error {
	return &cliError{code: code, err: fmt.Errorf(format, args...)}
}

func exitError(code int, err error) error {
	if err == nil {
		return nil
	}
	return &cliError{code: code, err: err}
}

func exitCodeFor(err error) int {
	if err == nil {
		return 0
	}
	var ce *cliError
	if errors.As(err, &ce) {
		if ce.code == 0 {
			return 1
		}
		return ce.code
	}
	return 1
}

// runWing implements `wing <pipeline> [flags...]`. Locates the enclosing
// .sparkwing/, strips wing-owned flags, optionally re-roots on a git
// ref (--from), then compiles + execs the user's pipeline binary.
// `wing <pipeline> --help` cannot short-circuit here because pipeline
// flags are parsed by the user's compiled binary.
func runWing(args []string) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		if len(args) == 0 {
			PrintHelp(cmdWing, os.Stderr)
			return errors.New("wing: pipeline name required")
		}
		PrintHelp(cmdWing, os.Stdout)
		return nil
	}

	// Wing-owned flags must precede the pipeline-name positional.
	// extractPipelineName enforces that; without it the parser would
	// take args[0] as the pipeline name unconditionally, so
	// `sparkwing run -C /path foo` would silently treat `-C` as the
	// pipeline name and `/path` as a pipeline arg.
	pipelineName, rest, err := extractPipelineName(args)
	if err != nil {
		return fmt.Errorf("wing: %w", err)
	}
	wf, passthrough := parseWingFlags(rest)

	// `-C <path>` re-anchors discovery (same shape as `git -C`).
	var dir string
	if wf.changeDir != "" {
		dir, err = findSparkwingDirFrom(wf.changeDir)
	} else {
		dir, err = findSparkwingDir()
	}
	if err != nil {
		return err
	}

	// Auto-register so cross-repo RunAndAwait can resolve names without
	// a hardcoded WithFreshRepo. Errors dropped: read-only home shouldn't break wing.
	_ = repos.AutoRegister(filepath.Dir(dir))

	// --config PRESET: explicit flags always win over the preset.
	if wf.config != "" {
		preset, found, perr := wingconfig.Resolve(dir, wf.config)
		if perr != nil {
			return fmt.Errorf("--config %s: %w", wf.config, perr)
		}
		if !found {
			return fmt.Errorf("--config %s: preset not found in .sparkwing/config.yaml or ~/.config/sparkwing/config.yaml", wf.config)
		}
		if wf.on == "" {
			wf.on = preset.On
		}
		if wf.from == "" {
			wf.from = preset.From
		}
	}

	// Gate dispatch against the pipeline's author-declared venue.
	// LocalOnly pipelines refuse `--on PROFILE` (cluster-up
	// shells out to terraform / aws against laptop credentials);
	// ClusterOnly pipelines refuse bare invocation (in-cluster
	// state-touching chores). Venue is resolved from the describe
	// cache so the gate fires before the dispatch round-trip; a cold
	// cache silently degrades to "either" (the safe permissive
	// default), which is the same behavior pipelines without an
	// explicit Venue() get.
	if v := lookupCachedVenue(dir, pipelineName); v != "" {
		if err := enforcePipelineVenue(v, pipelineName, wf.on); err != nil {
			return err
		}
	}

	// Blast-radius gate. Walk per-step markers via the describe
	// cache and refuse dispatch when a Destructive /
	// AffectsProduction / CostsMoney step is reachable without the
	// matching --allow-* escape (or --dry-run, which bypasses every
	// gate per the safe-mode contract). A profile-level auto_allow
	// can pre-authorize specific markers for a low-stakes
	// environment. A cold cache or older binary silently degrades
	// to "no markers detected, no gate fires" -- the next --describe
	// refresh populates the cache.
	if findings := lookupCachedBlastRadius(dir, pipelineName); len(findings) > 0 {
		var prof *profile.Profile
		if wf.on != "" {
			p, perr := resolveProfile(wf.on)
			if perr == nil {
				prof = p
			}
		}
		if err := enforceBlastRadius(pipelineName, findings, wf, prof); err != nil {
			return err
		}
	}

	if wf.on != "" {
		return dispatchRemote(pipelineName, wf, passthrough)
	}

	// --from re-roots compilation on a git worktree; cleanup must run on both paths.
	if wf.from != "" {
		_, sparkwingSub, cleanup, err := setupFromRef(dir, wf.from)
		if err != nil {
			return fmt.Errorf("wing: --from %s: %w", wf.from, err)
		}
		defer cleanup()
		dir = sparkwingSub
	}

	env := os.Environ()
	// Decide renderer here so a CLI upgrade fixes TTY detection without
	// needing per-project SDK pin bumps. User-set value always wins.
	if os.Getenv("SPARKWING_LOG_FORMAT") == "" {
		if color.IsInteractiveStdout() {
			env = append(env, "SPARKWING_LOG_FORMAT=pretty")
		} else {
			env = append(env, "SPARKWING_LOG_FORMAT=json")
		}
	}
	if wf.verbose {
		env = append(env, "SPARKWING_LOG_LEVEL=debug")
	}
	// Wing-owned knobs ride env vars the pipeline binary reads at Options-build time.
	if wf.retryOf != "" {
		env = append(env, "SPARKWING_RETRY_OF="+wf.retryOf)
		if wf.fullRetry {
			env = append(env, "SPARKWING_RETRY_FULL=1")
		}
	}
	// Forward --start-at / --stop-at via env so the pipeline binary's
	// orchestrator/main.go can lift them onto Options alongside the
	// existing --retry-of plumbing.
	if wf.startAt != "" {
		env = append(env, "SPARKWING_START_AT="+wf.startAt)
	}
	if wf.stopAt != "" {
		env = append(env, "SPARKWING_STOP_AT="+wf.stopAt)
	}
	// Same env-var protocol for --dry-run; the pipeline binary lifts
	// SPARKWING_DRY_RUN onto Options.DryRun and the orchestrator
	// installs WithDryRun(ctx) on the run.
	if wf.dryRun {
		env = append(env, "SPARKWING_DRY_RUN=1")
	}
	// allow-* gate flags: consumed by the wing wrapper for the
	// blast-radius pre-flight check, but still surfaced on the
	// run record for reproducibility (an agent re-invoking needs to
	// know which gates were authorized). Forwarded as env vars; the
	// orchestrator reads them in emitRunStart and includes the names
	// in run_start.attrs.flags.
	if wf.allowDestructive {
		env = append(env, "SPARKWING_ALLOW_DESTRUCTIVE=1")
	}
	if wf.allowProd {
		env = append(env, "SPARKWING_ALLOW_PROD=1")
	}
	if wf.allowMoney {
		env = append(env, "SPARKWING_ALLOW_MONEY=1")
	}
	// Forward pre-flight wing flags as env vars purely so emitRunStart
	// can surface them on run_start.attrs.flags. The pipeline binary
	// itself doesn't read these (--from is consumed before exec via
	// setupFromRef, --config is resolved into other flags above,
	// --no-update gates sparks resolve in compile.go) -- they appear
	// only as reproducibility breadcrumbs in the run record.
	if wf.from != "" {
		env = append(env, "SPARKWING_FROM="+wf.from)
	}
	if wf.config != "" {
		env = append(env, "SPARKWING_CONFIG="+wf.config)
	}
	if wf.noUpdate {
		env = append(env, "SPARKWING_NO_UPDATE=1")
	}
	if wf.on != "" {
		// --on routes to dispatchRemote() and we wouldn't reach the
		// local exec path; this branch only fires when something
		// upstream silently flipped that. Forward anyway so the run
		// record reflects the intent.
		env = append(env, "SPARKWING_ON="+wf.on)
	}
	if wf.secrets != "" {
		env = append(env, "SPARKWING_SECRETS_PROFILE="+wf.secrets)
	}

	if wf.mode != "" {
		env = append(env, "SPARKWING_MODE="+wf.mode)
		if wf.workers > 0 {
			env = append(env, fmt.Sprintf("SPARKWING_WORKERS=%d", wf.workers))
		}
		var profLogStore, profArtStore string
		if wf.on != "" {
			prof, err := resolveProfile(wf.on)
			if err != nil {
				return err
			}
			profLogStore = prof.LogStore
			profArtStore = prof.ArtifactStore
		}
		if v := firstNonEmptyStr(os.Getenv("SPARKWING_LOG_STORE"), profLogStore); v != "" {
			env = append(env, "SPARKWING_LOG_STORE="+v)
		}
		if v := firstNonEmptyStr(os.Getenv("SPARKWING_ARTIFACT_STORE"), profArtStore); v != "" {
			env = append(env, "SPARKWING_ARTIFACT_STORE="+v)
		}
	}

	return compileAndExec(dir, append([]string{pipelineName}, passthrough...), env,
		compileOptions{NoUpdate: wf.noUpdate})
}

func runSparkwing(args []string) error {
	if len(args) == 0 {
		PrintHelp(cmdSparkwing, os.Stderr)
		os.Exit(2)
	}
	switch args[0] {
	case "info":
		return runInfo(args[1:])
	case "pipeline":
		return runPipeline(args[1:])
	case "run":
		return runRun(args[1:])
	case "runs":
		return runJobs(args[1:])

	case "dashboard":
		return runDashboard(args[1:])

	case "cluster":
		return runCluster(args[1:])
	case "secrets":
		return runSecret(args[1:])

	case "configure":
		return runConfigure(args[1:])
	case "completion":
		return runCompletion(args[1:])
	case "docs":
		return runDocs(args[1:])
	case "commands":
		return runCommands(args[1:])
	case "update":
		return runUpdate(args[1:])
	case "version", "--version", "-V":
		return runVersion(args[1:])

	case "debug":
		return runDebug(args[1:])

	case "handle-trigger":
		return runWing(args)
	case "__dashboard-supervise":
		return runDashboardSupervise(args[1:])
	case "_complete-profiles":
		return runInternalCompleteProfiles(args[1:])
	case "_complete-pipelines":
		return runInternalCompletePipelines(args[1:])
	case "_complete-flags":
		return runInternalCompleteFlags(args[1:])
	case "_complete-verbs":
		return runInternalCompleteVerbs(args[1:])
	case "_complete-hint":
		return runInternalCompleteHint(args[1:])
	case "_complete-pipeline-flags":
		return runInternalCompletePipelineFlags(args[1:])
	case "help", "-h", "--help":
		PrintHelp(cmdSparkwing, os.Stdout)
		return nil
	default:
		PrintHelp(cmdSparkwing, os.Stderr)
		return fmt.Errorf("unknown subcommand %q", args[0])
	}
}

func runRunsApprovals(ctx context.Context, paths orchestrator.Paths, args []string) error {
	if handleParentHelp(cmdApprovals, args) {
		return nil
	}
	if len(args) == 0 {
		PrintHelp(cmdApprovals, os.Stdout)
		return nil
	}
	switch args[0] {
	case "list":
		return runApprovals(args[1:])
	case "approve":
		return runApprove(ctx, paths, args[1:])
	case "deny":
		return runDeny(ctx, paths, args[1:])
	default:
		PrintHelp(cmdApprovals, os.Stderr)
		return fmt.Errorf("runs approvals: unknown subcommand %q", args[0])
	}
}

func runCluster(args []string) error {
	if handleParentHelp(cmdCluster, args) {
		return nil
	}
	if len(args) == 0 {
		PrintHelp(cmdCluster, os.Stdout)
		return nil
	}
	switch args[0] {
	case "status":
		return runHealth(args[1:])
	case "agents":
		return runAgents(args[1:])
	case "worker":
		return runWorker(args[1:])
	case "gc":
		return runGC(args[1:])
	case "push":
		return runPush(args[1:])
	case "users":
		return runUsers(args[1:])
	case "tokens":
		return runTokens(args[1:])
	case "image":
		return runImage(args[1:])
	case "webhooks":
		return runWebhooks(args[1:])
	default:
		PrintHelp(cmdCluster, os.Stderr)
		return fmt.Errorf("cluster: unknown subcommand %q", args[0])
	}
}

func runConfigure(args []string) error {
	if handleParentHelp(cmdConfigure, args) {
		return nil
	}
	if len(args) == 0 {
		PrintHelp(cmdConfigure, os.Stdout)
		return nil
	}
	switch args[0] {
	case "init":
		return runConfigureInit(args[1:])
	case "profiles":
		return runProfiles(args[1:])
	case "xrepo":
		return runXrepo(args[1:])
	default:
		PrintHelp(cmdConfigure, os.Stderr)
		return fmt.Errorf("configure: unknown subcommand %q", args[0])
	}
}

func runJobs(args []string) error {
	if handleParentHelp(cmdJobs, args) {
		return nil
	}
	if len(args) == 0 {
		PrintHelp(cmdJobs, os.Stderr)
		return errors.New("jobs: subcommand required")
	}
	paths, err := orchestrator.DefaultPaths()
	if err != nil {
		return err
	}
	ctx := context.Background()

	switch args[0] {
	case "approvals":
		return runRunsApprovals(ctx, paths, args[1:])
	case "annotations":
		return runRunsAnnotations(ctx, paths, args[1:])
	case "triggers":
		return runTriggers(args[1:])
	case "list":
		fs := flag.NewFlagSet(cmdJobsList.Path, flag.ContinueOnError)
		limit := fs.Int("limit", 20, "max runs to show")
		outFmt := fs.StringP("output", "o", "", "output format: table|json|plain (default: table)")
		asJSON := fs.Bool("json", false, "emit JSON (hidden alias for -o json)")
		_ = fs.MarkHidden("json")
		quiet := fs.BoolP("quiet", "q", false, "print only run ids, one per line")
		since := fs.Duration("since", 0, "only runs newer than this (e.g. 1h, 24h, 7d)")
		pipelines := multiFlagVar(fs, "pipeline", "filter by pipeline (repeatable, OR semantics)")
		statuses := multiFlagVar(fs, "status", "filter by status (repeatable, OR semantics)")
		tags := multiFlagVar(fs, "tag", "filter by pipelines.yaml tag (repeatable, OR semantics)")
		on := fs.String("on", "", "profile name (default: current default). Omit for local-only reads.")
		if err := parseAndCheck(cmdJobsList, fs, args[1:]); err != nil {
			if errors.Is(err, errHelpRequested) {
				return nil
			}
			return err
		}
		resolvedFmt, err := resolveOutputFormat(*outFmt, fs.Changed("output"), *asJSON, "jobs list")
		if err != nil {
			return err
		}

		pipelineSet := *pipelines
		if len(*tags) > 0 {
			extra, err := pipelinesWithTags(*tags)
			if err != nil {
				return err
			}
			pipelineSet = append(pipelineSet, extra...)
			if len(pipelineSet) == 0 {
				// Tag filter matched nothing; don't degrade to "no filter".
				if resolvedFmt == "json" {
					fmt.Fprintln(os.Stdout, "[]")
					return nil
				}
				fmt.Fprintln(os.Stdout, "no runs match the requested tags")
				return nil
			}
		}
		listOpts := orchestrator.ListOpts{
			Limit:     *limit,
			Pipelines: pipelineSet,
			Statuses:  *statuses,
			Since:     *since,
			JSON:      resolvedFmt == "json",
			Quiet:     *quiet,
		}
		if *on != "" {
			prof, err := resolveProfile(*on)
			if err != nil {
				return err
			}
			if err := requireController(prof, "jobs list"); err != nil {
				return err
			}
			return orchestrator.ListJobsRemote(ctx, prof.Controller, prof.Token, listOpts, os.Stdout)
		}
		return orchestrator.ListJobs(ctx, paths, listOpts, os.Stdout)

	case "status":
		fs := flag.NewFlagSet(cmdJobsStatus.Path, flag.ContinueOnError)
		runID := fs.String("run", "", "run identifier")
		outFmt := fs.StringP("output", "o", "", "output format: json|table|plain (default: table)")
		asJSON := fs.Bool("json", false, "emit JSON (hidden alias for -o json)")
		_ = fs.MarkHidden("json")
		follow := fs.BoolP("follow", "f", false, "poll until the run reaches a terminal state")
		on := fs.String("on", "", "profile name (default: current default). Omit for local-only reads.")
		exitZero := fs.Bool("exit-zero", false,
			"return exit code 0 even when the run failed/cancelled (opt out of the scriptable exit contract)")
		if err := parseAndCheck(cmdJobsStatus, fs, args[1:]); err != nil {
			if errors.Is(err, errHelpRequested) {
				return nil
			}
			return err
		}
		*runID = normalizeRunID(*runID)
		resolvedFmt, err := resolveOutputFormat(*outFmt, fs.Changed("output"), *asJSON, "jobs status")
		if err != nil {
			return err
		}
		statusOpts := orchestrator.StatusOpts{JSON: resolvedFmt == "json", Follow: *follow}
		if *on != "" {
			prof, err := resolveProfile(*on)
			if err != nil {
				return err
			}
			if err := requireController(prof, "jobs status"); err != nil {
				return err
			}
			if err := orchestrator.JobStatusRemote(ctx, prof.Controller, prof.Token,
				*runID, statusOpts, os.Stdout); err != nil {
				return err
			}
			if *exitZero {
				return nil
			}
			return remoteStatusExitCheck(ctx, prof.Controller, prof.Token, *runID)
		}
		if err := orchestrator.JobStatus(ctx, paths, *runID, statusOpts, os.Stdout); err != nil {
			return err
		}
		if *exitZero {
			return nil
		}
		return localStatusExitCheck(ctx, paths, *runID)

	case "logs":
		fs := flag.NewFlagSet(cmdJobsLogs.Path, flag.ContinueOnError)
		runID := fs.String("run", "", "run identifier")
		node := fs.String("node", "", "limit output to one node id")
		outFmt := fs.StringP("output", "o", "", "output format: table|json|plain (default: table on TTY, json when piped)")
		asJSON := fs.Bool("json", false, "emit JSON (alias for -o json)")
		pretty := fs.Bool("pretty", false, "force the human-readable colored renderer even when stdout isn't a terminal (alias for -o table)")
		follow := fs.BoolP("follow", "f", false, "tail the log(s) until the run terminates")
		on := fs.String("on", "",
			"profile name (cluster mode). Omit to read logs from the local SQLite store.")
		tail := fs.Int("tail", 0, "print only the last N lines (server-side in cluster mode)")
		head := fs.Int("head", 0, "print only the first N lines (server-side in cluster mode)")
		lines := fs.String("lines", "", "1-indexed inclusive line range A:B (server-side in cluster mode)")
		grep := fs.String("grep", "", "substring filter (server-side in cluster mode)")
		since := fs.Duration("since", 0,
			"only include output from nodes whose StartedAt >= now-D (e.g. 5m, 1h)")
		format := fs.String("format", "", "DEPRECATED alias for -o/--output")
		_ = fs.MarkHidden("format")
		tree := fs.Bool("tree", false, "merge parent run + descendants into one chronological stream (local only)")
		eventsOnly := fs.Bool("events-only", false, "filter to run-level envelope events (run_start, node_start, node_end, step_start, step_end, run_finish, plan_warn, ...) -- the bracketing NDJSON the dispatcher streams to stdout")
		noEvents := fs.Bool("no-events", false, "filter to per-node body output only -- useful when scripts depend on the legacy shape")
		if err := parseAndCheck(cmdJobsLogs, fs, args[1:]); err != nil {
			if errors.Is(err, errHelpRequested) {
				return nil
			}
			return err
		}
		*runID = normalizeRunID(*runID)
		if *tree && *on != "" {
			return errors.New("jobs logs: --tree is local-mode only (cannot combine with --on)")
		}
		// Merge --format into --output for backward compat.
		effectiveOut := *outFmt
		if effectiveOut == "" && *format != "" {
			switch *format {
			case "pretty":
				effectiveOut = "table"
			case "json", "plain":
				effectiveOut = *format
			default:
				return fmt.Errorf("jobs logs: --format must be one of json|pretty|plain, got %q", *format)
			}
		}
		if *pretty {
			if effectiveOut != "" && effectiveOut != "table" {
				return fmt.Errorf("jobs logs: --pretty and -o %s disagree", effectiveOut)
			}
			effectiveOut = "table"
		}
		// Default to JSONL when piped so agents/CI get structured output without --json.
		if effectiveOut == "" && !*asJSON && !color.IsInteractiveStdout() {
			effectiveOut = "json"
		}
		// jobs logs has its own pre-resolution above (--pretty / --format
		// fold-in + auto-JSONL when piped), so the explicit-set bit
		// reflects whether *anyone* (user or pre-resolution) settled on
		// a non-empty effectiveOut. effectiveOut == "" means "let
		// resolveOutputFormat default to table"; non-empty means a
		// concrete choice was made (user-typed -o, or pre-resolution
		// derived a value), so it should compete with --json on equal
		// footing -- which matches the kubectl-style explicit-bit
		// contract for the resolver.
		resolvedFmt, err := resolveOutputFormat(effectiveOut, effectiveOut != "", *asJSON, "jobs logs")
		if err != nil {
			return err
		}
		if *tail > 0 && *head > 0 {
			return errors.New("jobs logs: --tail and --head cannot be combined")
		}
		opts := orchestrator.LogsOpts{
			Node:       *node,
			JSON:       resolvedFmt == "json",
			Follow:     *follow,
			Format:     resolvedFmt,
			Tail:       *tail,
			Head:       *head,
			Lines:      *lines,
			Grep:       *grep,
			Since:      *since,
			Tree:       *tree,
			EventsOnly: *eventsOnly,
			NoEvents:   *noEvents,
		}
		if *on != "" {
			prof, err := resolveProfile(*on)
			if err != nil {
				return err
			}
			if prof.Controller == "" || prof.Logs == "" {
				return fmt.Errorf("jobs logs: profile %q must have both controller and logs URLs", prof.Name)
			}
			return orchestrator.JobLogsRemoteWithTokens(ctx, prof.Controller, prof.Logs, prof.Token,
				*runID, opts, os.Stdout)
		}
		return orchestrator.JobLogs(ctx, paths, *runID, opts, os.Stdout)

	case "errors":
		fs := flag.NewFlagSet(cmdJobsErrors.Path, flag.ContinueOnError)
		runID := fs.String("run", "", "run identifier")
		outFmt := fs.StringP("output", "o", "", "output format: table|json|plain")
		asJSON := fs.Bool("json", false, "emit JSON (hidden alias for -o json)")
		_ = fs.MarkHidden("json")
		on := fs.String("on", "", "profile name (default: current default). Omit for local-only reads.")
		if err := parseAndCheck(cmdJobsErrors, fs, args[1:]); err != nil {
			if errors.Is(err, errHelpRequested) {
				return nil
			}
			return err
		}
		*runID = normalizeRunID(*runID)
		resolvedFmt, err := resolveOutputFormat(*outFmt, fs.Changed("output"), *asJSON, "jobs errors")
		if err != nil {
			return err
		}
		emitJSON := resolvedFmt == "json"
		if *on != "" {
			prof, err := resolveProfile(*on)
			if err != nil {
				return err
			}
			if err := requireController(prof, "jobs errors"); err != nil {
				return err
			}
			return orchestrator.JobErrorsRemote(ctx, prof.Controller, prof.Token,
				*runID, emitJSON, os.Stdout)
		}
		return orchestrator.JobErrors(ctx, paths, *runID, emitJSON, os.Stdout)

	case "cancel":
		fs := flag.NewFlagSet(cmdJobsCancel.Path, flag.ContinueOnError)
		runID := fs.String("run", "", "run identifier to cancel")
		on := fs.String("on", "", "profile name (default: current default)")
		if err := parseAndCheck(cmdJobsCancel, fs, args[1:]); err != nil {
			if errors.Is(err, errHelpRequested) {
				return nil
			}
			return err
		}
		*runID = normalizeRunID(*runID)
		prof, err := resolveProfile(*on)
		if err != nil {
			return err
		}
		if err := requireController(prof, "jobs cancel"); err != nil {
			return err
		}
		c := client.NewWithToken(prof.Controller, nil, prof.Token)
		if err := c.CancelRun(ctx, *runID); err != nil {
			return fmt.Errorf("cancel %s: %w", *runID, err)
		}
		fmt.Fprintf(os.Stdout, "cancel requested for %s\n", *runID)
		return nil

	case "retry":
		fs := flag.NewFlagSet(cmdJobsRetry.Path, flag.ContinueOnError)
		srcRunIDFlag := fs.String("run", "", "run identifier to retry")
		on := fs.String("on", "", "profile name (default: current default)")
		if err := parseAndCheck(cmdJobsRetry, fs, args[1:]); err != nil {
			if errors.Is(err, errHelpRequested) {
				return nil
			}
			return err
		}
		*srcRunIDFlag = normalizeRunID(*srcRunIDFlag)
		prof, err := resolveProfile(*on)
		if err != nil {
			return err
		}
		if err := requireController(prof, "jobs retry"); err != nil {
			return err
		}
		srcRunID := *srcRunIDFlag
		c := client.NewWithToken(prof.Controller, nil, prof.Token)
		run, err := c.GetRun(ctx, srcRunID)
		if err != nil {
			return fmt.Errorf("lookup %s: %w", srcRunID, err)
		}
		resp, err := c.CreateTrigger(ctx, client.TriggerRequest{
			Pipeline: run.Pipeline,
			Args:     run.Args,
			Trigger: client.TriggerMeta{
				// Audit trail to origin run for "retry of run-X" surfaces.
				Source: "retry:" + srcRunID,
			},
			Git:     client.GitMeta{Branch: run.GitBranch, SHA: run.GitSHA},
			RetryOf: srcRunID,
		})
		if err != nil {
			return fmt.Errorf("trigger retry: %w", err)
		}
		fmt.Fprintf(os.Stdout, "retried %s as %s\n", srcRunID, resp.RunID)
		return nil

	case "prune":
		fs := flag.NewFlagSet(cmdJobsPrune.Path, flag.ContinueOnError)
		on := fs.String("on", "", "profile name (default: current default)")
		olderThan := fs.Duration("older-than", 0, "prune runs older than this (e.g. 7d, 48h). Required unless --run is set.")
		dryRun := fs.Bool("dry-run", false, "list matching runs without deleting")
		oneRun := fs.String("run", "", "prune this specific run id")
		if err := parseAndCheck(cmdJobsPrune, fs, args[1:]); err != nil {
			if errors.Is(err, errHelpRequested) {
				return nil
			}
			return err
		}
		*oneRun = normalizeRunID(*oneRun)
		prof, err := resolveProfile(*on)
		if err != nil {
			return err
		}
		if err := requireController(prof, "jobs prune"); err != nil {
			return err
		}
		if *oneRun == "" && *olderThan <= 0 {
			return errors.New("jobs prune: either --older-than DUR or --run RUN_ID is required")
		}
		c := client.NewWithToken(prof.Controller, nil, prof.Token)
		var logc storage.LogStore
		if prof.Logs != "" {
			logc = sparkwinglogs.New(prof.Logs, nil, prof.Token)
		}
		var victims []string
		if *oneRun != "" {
			victims = []string{*oneRun}
		} else {
			runs, err := c.ListRuns(ctx, store.RunFilter{Limit: 10000})
			if err != nil {
				return fmt.Errorf("list runs: %w", err)
			}
			cutoff := time.Now().Add(-*olderThan)
			for _, r := range runs {
				if !r.StartedAt.Before(cutoff) {
					continue
				}
				// Never prune in-flight work; the worker owns that row.
				if r.Status != "success" && r.Status != "failed" && r.Status != "cancelled" {
					continue
				}
				victims = append(victims, r.ID)
			}
		}
		if len(victims) == 0 {
			fmt.Fprintln(os.Stdout, "no runs match prune criteria")
			return nil
		}
		if *dryRun {
			fmt.Fprintf(os.Stdout, "would prune %d run(s):\n", len(victims))
			for _, id := range victims {
				fmt.Fprintln(os.Stdout, "  "+id)
			}
			return nil
		}
		for _, id := range victims {
			if err := c.DeleteRun(ctx, id); err != nil {
				return fmt.Errorf("delete run %s: %w", id, err)
			}
			if logc != nil {
				if err := logc.DeleteRun(ctx, id); err != nil {
					// Don't abort: controller row is gone; warn and continue.
					fmt.Fprintf(os.Stderr, "warn: logs delete %s: %v\n", id, err)
				}
			}
		}
		fmt.Fprintf(os.Stdout, "pruned %d run(s)\n", len(victims))
		return nil

	case "failures":
		return runJobsFailures(ctx, paths, args[1:])
	case "stats":
		return runJobsStats(ctx, paths, args[1:])
	case "last":
		return runJobsLast(ctx, paths, args[1:])
	case "tree":
		return runJobsTree(ctx, paths, args[1:])
	case "get":
		return runJobsGet(ctx, paths, args[1:])
	case "receipt":
		return runJobsReceipt(ctx, paths, args[1:])
	case "wait":
		return runJobsWait(ctx, paths, args[1:])
	case "find":
		return runJobsFind(ctx, paths, args[1:])
	default:
		return fmt.Errorf("jobs: unknown command %q", args[0])
	}
}

// resolveOutputFormat canonicalizes -o/--output + --json into one of
// {"table","json","plain"}. Disagreeing values error rather than silently win.
//
// outputChanged distinguishes "user explicitly typed -o/--output" from
// "the flag took its default value." Callers using pflag pass
// fs.Changed("output"); hand-parsers pass `parsed.output != ""`. The
// distinction matters because a default-shaped --output (e.g. the
// "table" pflag default a leaf may register) must not collide with a
// user-set --json: --json is documented as an alias for --output=json
// and "default + --json" should resolve to JSON, not error. Only when
// BOTH flags are user-set AND disagree do we surface a conflict.
// Mirrors the kubectl / gh / aws CLI convention.
func resolveOutputFormat(outFmt string, outputChanged bool, jsonAlias bool, cmdPath string) (string, error) {
	switch outFmt {
	case "", "table", "json", "plain":
	default:
		return "", fmt.Errorf("%s: -o/--output must be one of table|json|plain, got %q", cmdPath, outFmt)
	}
	if jsonAlias {
		if outputChanged && outFmt != "" && outFmt != "json" {
			return "", fmt.Errorf("%s: --json and -o %s disagree", cmdPath, outFmt)
		}
		return "json", nil
	}
	if outFmt == "" {
		return "table", nil
	}
	return outFmt, nil
}

func isTerminalRunStatus(s string) bool {
	return s == "success" || s == "failed" || s == "cancelled"
}

// normalizeRunID auto-prefixes "run-" when the operator dropped it
// from the `--run` value (a frequent friction point: `runs list`
// shows the full id, but copy-paste of just the timestamp+suffix
// portion still resolves to the same row). Bare "run-" is left alone
// so an explicit prefix never gets doubled up.
func normalizeRunID(id string) string {
	if id == "" || strings.HasPrefix(id, "run-") {
		return id
	}
	return "run-" + id
}

// statusExitCode maps run status to the scripted exit contract:
// success -> nil, anything else -> exit 1.
func statusExitCode(status string) error {
	if status == "success" {
		return nil
	}
	return exitErrorf(1, "run status: %s", status)
}

func localStatusExitCheck(ctx context.Context, paths orchestrator.Paths, runID string) error {
	st, err := store.Open(paths.StateDB())
	if err != nil {
		return err
	}
	defer st.Close()
	run, err := st.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	return statusExitCode(run.Status)
}

func remoteStatusExitCheck(ctx context.Context, controllerURL, token, runID string) error {
	c := client.NewWithToken(controllerURL, nil, token)
	run, err := c.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	return statusExitCode(run.Status)
}

func multiFlagVar(fs *flag.FlagSet, name, usage string) *[]string {
	var dest []string
	fs.StringSliceVar(&dest, name, nil, usage)
	return &dest
}

func pipelinesWithTags(tags []string) ([]string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	_, cfg, err := pipelines.Discover(cwd)
	if err != nil {
		// Missing pipelines.yaml = no tag resolution possible; not a hard error.
		return nil, nil
	}
	want := map[string]struct{}{}
	for _, t := range tags {
		want[t] = struct{}{}
	}
	var matched []string
	for _, p := range cfg.Pipelines {
		for _, t := range p.Tags {
			if _, ok := want[t]; ok {
				matched = append(matched, p.Name)
				break
			}
		}
	}
	return matched, nil
}

// findSparkwingDir walks up from cwd looking for a .sparkwing/main.go.
func findSparkwingDir() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return findSparkwingDirFrom(dir)
}

func findSparkwingDirFrom(start string) (string, error) {
	abs, err := filepath.Abs(start)
	if err != nil {
		return "", fmt.Errorf("resolve %q: %w", start, err)
	}
	if info, err := os.Stat(abs); err != nil || !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", start)
	}
	dir := abs
	for {
		candidate := filepath.Join(dir, ".sparkwing")
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			if _, err := os.Stat(filepath.Join(candidate, "main.go")); err == nil {
				return candidate, nil
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no .sparkwing/main.go found from %s up", abs)
		}
		dir = parent
	}
}

func mustGetwd() string {
	d, _ := os.Getwd()
	return d
}
