// The sparkwing binary. Exposes pipeline dispatch (`sparkwing run`),
// infrastructure verbs (cluster, secrets, configure), and observation
// verbs (runs, dashboard).
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

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/internal/profile"
	"github.com/sparkwing-dev/sparkwing/internal/repos"
	"github.com/sparkwing-dev/sparkwing/pkg/color"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/docs"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func init() {
	docs.Version = installedVersion()
}

func main() {
	// Windows self-update defers deletion of the running binary; clean it up here.
	cleanupStaleUpdate()

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

// setEnv returns env with key set to value, removing any prior
// occurrence of the same key. Use instead of append when the flag
// value must override a preexisting shell-environment value; bare
// append leaves duplicates and POSIX getenv returns the first
// match, so the shell var would silently shadow the flag.
func setEnv(env []string, key, value string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env)+1)
	for _, e := range env {
		if !strings.HasPrefix(e, prefix) {
			out = append(out, e)
		}
	}
	return append(out, prefix+value)
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

// dispatchRun implements `sparkwing run <pipeline> [flags...]`.
// Locates the enclosing .sparkwing/, strips sparkwing-owned flags,
// optionally re-roots on a git ref (--sw-ref), then compiles + execs
// the user's pipeline binary. `sparkwing run <pipeline> --help`
// cannot short-circuit here because pipeline flags are parsed by the
// user's compiled binary.
func dispatchRun(args []string) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		if len(args) == 0 {
			PrintHelp(cmdRun, os.Stderr)
			return errors.New("run: pipeline name required (e.g. `sparkwing run hello`)")
		}
		PrintHelp(cmdRun, os.Stdout)
		return nil
	}

	pipelineName := args[0]
	if strings.HasPrefix(pipelineName, "-") {
		PrintHelp(cmdRun, os.Stderr)
		return fmt.Errorf("run: pipeline name must come first; got flag %q", pipelineName)
	}
	wf, passthrough := parseRunFlags(args[1:])
	var err error

	// Catch a retired/renamed where-flag (--on / --sw-on / --sw-profile /
	// --sw-target) before it falls through to the pipeline binary, so the
	// user gets a migration pointer instead of an opaque pipeline error.
	if err := checkRetiredWhereFlags(passthrough); err != nil {
		return err
	}
	// Validate --profile against profiles.yaml up front so a bad name
	// errors before we compile or exec the pipeline binary. The inner
	// binary re-resolves from SPARKWING_PROFILE at run time.
	if wf.profile != "" {
		if _, perr := resolveProfileFlag(wf.profile); perr != nil {
			return perr
		}
	}

	// Hard-error on a half-migrated repo (legacy pipelines.yaml etc. in
	// .sparkwing/) before compile so the failure is fast and names the
	// migration guide. A fully-migrated repo (only sparkwing.yaml) is
	// silent.
	legacyStart := wf.changeDir
	if legacyStart == "" {
		if cwd, cerr := os.Getwd(); cerr == nil {
			legacyStart = cwd
		}
	}
	if err := projectconfig.CheckLegacy(legacyStart); err != nil {
		return err
	}

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
	// a hardcoded WithFreshRepo. Errors dropped: read-only home shouldn't break dispatch.
	_ = repos.AutoRegister(filepath.Dir(dir))

	// Pipeline-level dispatch gating now flows through the runner
	// resolution rule: pipelines.yaml declares which runners a
	// target accepts, the orchestrator picks one whose labels
	// satisfy the job's Requires terms, and a mismatch produces a
	// clear error at run start. No CLI-side venue check is needed.

	// Risk-label gate. Walk per-step risk labels via the describe
	// cache and refuse dispatch when any reachable step declares a
	// label the operator hasn't authorized via --sw-allow (or
	// --sw-dry-run, which bypasses every gate per the safe-mode
	// contract). A profile-level auto_allow can pre-authorize
	// specific labels for a low-stakes environment. A cold cache
	// degrades to "no labels detected, no gate fires"; the next
	// --describe refresh populates it.
	if findings := lookupCachedRisks(dir, pipelineName); len(findings) > 0 {
		// Resolve the active profile (flag, else default/detect/laptop)
		// so a profile-level auto_allow can pre-authorize risk labels.
		// Best-effort: a resolution failure leaves the gate profile-less.
		var prof *profile.Profile
		if p, perr := resolveProfileFlag(wf.profile); perr == nil {
			prof = p
		}
		if err := enforceRiskGate(pipelineName, findings, wf, prof); err != nil {
			return err
		}
	}

	// --sw-ref re-roots compilation on a git worktree; cleanup must run on both paths.
	if wf.ref != "" {
		_, sparkwingSub, cleanup, err := setupRefWorktree(dir, wf.ref)
		if err != nil {
			return fmt.Errorf("--sw-ref %s: %w", wf.ref, err)
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
	// Forward --start-at / --stop-at via env so the pipeline binary's
	// orchestrator/main.go can lift them onto Options.
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
	if wf.only != "" {
		env = append(env, "SPARKWING_ONLY="+wf.only)
	}
	if wf.noCache {
		env = append(env, "SPARKWING_NO_CACHE=1")
	}
	if wf.localOnly {
		env = append(env, "SPARKWING_LOCAL_ONLY=1")
	}
	// --sw-allow forwards the operator-authorized risk labels to the
	// orchestrator. Surfaced on the run record (run_start.attrs.flags)
	// so an agent re-invoking knows which labels were authorized.
	if len(wf.allow) > 0 {
		env = append(env, "SPARKWING_ALLOW="+strings.Join(wf.allow, ","))
	}
	// Forward pre-flight sparkwing flags as env vars purely so
	// emitRunStart can surface them on run_start.attrs.flags. The
	// pipeline binary itself doesn't read these (--sw-ref is
	// consumed before exec via setupRefWorktree, --sw-no-update
	// gates sparks resolve in compile.go) -- they appear only as
	// reproducibility breadcrumbs in the run record.
	if wf.ref != "" {
		env = append(env, "SPARKWING_REF="+wf.ref)
	}
	if wf.noUpdate {
		env = append(env, "SPARKWING_NO_UPDATE=1")
	}
	// --profile drives local execution against a storage profile. The
	// inner binary reads SPARKWING_PROFILE, resolves it, and routes
	// state/logs/cache through the profile (with a local mirror).
	// setEnv overrides any shell-inherited SPARKWING_PROFILE so the
	// flag wins. --sw-local-only still takes precedence inside the
	// inner binary's resolver.
	if wf.profile != "" {
		env = setEnv(env, "SPARKWING_PROFILE", wf.profile)
	}
	if wf.secrets != "" {
		env = append(env, "SPARKWING_SECRETS_PROFILE="+wf.secrets)
	}

	if wf.mode != "" {
		env = append(env, "SPARKWING_MODE="+wf.mode)
		if wf.workers > 0 {
			env = append(env, fmt.Sprintf("SPARKWING_WORKERS=%d", wf.workers))
		}
	}

	// Host-local box-slot semaphore knobs (see internal/boxslot).
	// Forwarded to the inner pipeline binary, which acquires the
	// slot before RunLocal so the wait blocks at run start rather
	// than wasting compile cycles on a pre-rejected run.
	//
	// Use setEnv (not append) so the flag overrides any preexisting
	// shell-environment SPARKWING_BOX_SLOTS / SPARKWING_BOX_NO_WAIT.
	// Plain append would leave duplicates, and POSIX getenv returns
	// the first match, so the user's shell var would silently
	// shadow the explicit flag.
	if wf.boxSlots != "" {
		env = setEnv(env, "SPARKWING_BOX_SLOTS", wf.boxSlots)
	}
	if wf.boxNoWait {
		env = setEnv(env, "SPARKWING_BOX_NO_WAIT", "1")
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
		return dispatchRun(args[1:])
	case "runs":
		return runJobs(args[1:])
	case "profile":
		return runProfileCmd(args[1:])

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
		return dispatchRun(args)
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
	case "_complete-targets":
		return runInternalCompleteTargets(args[1:])
	case "_complete-runners":
		return runInternalCompleteRunners(args[1:])
	case "_complete-profiles-for-pipeline":
		return runInternalCompleteProfilesForPipeline(args[1:])
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
		outFmt := fs.StringP("output", "o", "", "output format: pretty|json|plain (default: table)")
		quiet := fs.BoolP("quiet", "q", false, "print only run ids, one per line")
		since := fs.Duration("since", 0, "only runs newer than this (e.g. 1h, 24h, 7d)")
		pipelines := multiFlagVar(fs, "pipeline", "filter by pipeline (repeatable; OR semantics; prefix `!` to exclude)")
		statuses := multiFlagVar(fs, "status", "filter by status (repeatable; OR semantics; prefix `!` to exclude)")
		tags := multiFlagVar(fs, "tag", "filter by pipelines.yaml tag (repeatable, OR semantics)")
		branches := multiFlagVar(fs, "branch", "filter by git branch (repeatable; prefix `!` to exclude)")
		shas := multiFlagVar(fs, "sha", "filter by git sha prefix (repeatable; prefix `!` to exclude)")
		errorSubstr := fs.String("error", "", "substring match against the persisted failure reason")
		search := fs.String("search", "", "free-text search across pipeline/branch/sha/id/error; prefix a term with `-` to exclude")
		startedAfter := fs.String("started-after", "", "only runs whose StartedAt >= this (today, yesterday, 24h, 7d, or a date)")
		startedBefore := fs.String("started-before", "", "only runs whose StartedAt <= this")
		finishedAfter := fs.String("finished-after", "", "only runs whose FinishedAt >= this (excludes still-running)")
		finishedBefore := fs.String("finished-before", "", "only runs whose FinishedAt <= this (excludes still-running)")
		byPipeline := fs.Bool("by-pipeline", false, "pivot into one row per pipeline with a status sparkline of the last N runs")
		sparkline := fs.Int("sparkline", 30, "length of the sparkline when --by-pipeline is set")
		style := fs.String("style", "ascii", "sparkline glyph style: ascii|block|dot")
		profileName := fs.String("profile", "", "read against the named storage profile from ~/.config/sparkwing/profiles.yaml")
		if err := checkRetiredWhereFlags(args[1:]); err != nil {
			return err
		}
		if err := parseAndCheck(cmdJobsList, fs, args[1:]); err != nil {
			if errors.Is(err, errHelpRequested) {
				return nil
			}
			return err
		}
		resolvedFmt, err := resolveOutputFormat(*outFmt, "jobs list")
		if err != nil {
			return err
		}

		pipelineInc, pipelineExc := orchestrator.SplitExcludes(*pipelines)
		statusInc, statusExc := orchestrator.SplitExcludes(*statuses)
		branchInc, branchExc := orchestrator.SplitExcludes(*branches)
		shaInc, shaExc := orchestrator.SplitExcludes(*shas)

		pipelineSet := pipelineInc
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

		compiled := orchestrator.CompiledFilter{
			Branches:       branchInc,
			BranchExcludes: branchExc,
			SHAPrefixes:    shaInc,
			SHAExcludes:    shaExc,
			ErrorSubstr:    *errorSubstr,
			StatusExcludes: statusExc,
			PipelineExcl:   pipelineExc,
			Search:         orchestrator.ParseSearch(*search),
		}
		for _, ts := range []struct {
			raw  string
			into *time.Time
			name string
		}{
			{*startedAfter, &compiled.StartedAfter, "started-after"},
			{*startedBefore, &compiled.StartedBefore, "started-before"},
			{*finishedAfter, &compiled.FinishedAfter, "finished-after"},
			{*finishedBefore, &compiled.FinishedBefore, "finished-before"},
		} {
			if ts.raw == "" {
				continue
			}
			t, err := orchestrator.ParseLooseDate(ts.raw)
			if err != nil {
				return fmt.Errorf("jobs list: --%s: %w", ts.name, err)
			}
			*ts.into = t
		}

		var sparkStyle orchestrator.SparklineStyle
		switch *style {
		case "ascii", "":
			sparkStyle = orchestrator.SparkASCII
		case "block":
			sparkStyle = orchestrator.SparkBlock
		case "dot":
			sparkStyle = orchestrator.SparkDot
		default:
			return fmt.Errorf("jobs list: --style must be ascii|block|dot, got %q", *style)
		}

		listOpts := orchestrator.ListOpts{
			Limit:      *limit,
			Pipelines:  pipelineSet,
			Statuses:   statusInc,
			Since:      *since,
			JSON:       resolvedFmt == "json",
			Quiet:      *quiet,
			Filter:     compiled,
			ByPipeline: *byPipeline,
			Pivot: orchestrator.PivotOpts{
				SparklineLen: *sparkline,
				Style:        sparkStyle,
			},
		}
		if *profileName != "" {
			p, perr := resolveProfileFlag(*profileName)
			if perr != nil {
				return perr
			}
			listOpts.Profile = p
		}
		return orchestrator.ListJobs(ctx, paths, listOpts, os.Stdout)

	case "status":
		fs := flag.NewFlagSet(cmdJobsStatus.Path, flag.ContinueOnError)
		runID := fs.String("run", "", "run identifier")
		outFmt := fs.StringP("output", "o", "", "output format: json|table|plain (default: table)")
		follow := fs.BoolP("follow", "f", false, "poll until the run reaches a terminal state")
		steps := fs.Bool("steps", false, "render every step on every node in plain output")
		profileName := fs.String("profile", "", "read against the named storage profile from ~/.config/sparkwing/profiles.yaml")
		exitZero := fs.Bool("exit-zero", false,
			"return exit code 0 even when the run failed/cancelled (opt out of the scriptable exit contract)")
		if err := checkRetiredWhereFlags(args[1:]); err != nil {
			return err
		}
		if err := parseAndCheck(cmdJobsStatus, fs, args[1:]); err != nil {
			if errors.Is(err, errHelpRequested) {
				return nil
			}
			return err
		}
		*runID = normalizeRunID(*runID)
		resolvedFmt, err := resolveOutputFormat(*outFmt, "jobs status")
		if err != nil {
			return err
		}
		statusOpts := orchestrator.StatusOpts{JSON: resolvedFmt == "json", Follow: *follow, Steps: *steps}
		if *profileName != "" {
			p, perr := resolveProfileFlag(*profileName)
			if perr != nil {
				return perr
			}
			statusOpts.Profile = p
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
		outFmt := fs.StringP("output", "o", "", "output format: pretty|json|plain (default: pretty on TTY, json when piped)")
		follow := fs.BoolP("follow", "f", false, "tail the log(s) until the run terminates")
		profileName := fs.String("profile", "", "read against the named storage profile from ~/.config/sparkwing/profiles.yaml")
		tail := fs.Int("tail", 0, "print only the last N lines (server-side in cluster mode)")
		head := fs.Int("head", 0, "print only the first N lines (server-side in cluster mode)")
		lines := fs.String("lines", "", "1-indexed inclusive line range A:B (server-side in cluster mode)")
		grep := fs.String("grep", "", "substring filter (server-side in cluster mode)")
		since := fs.Duration("since", 0,
			"only include output from nodes whose StartedAt >= now-D (e.g. 5m, 1h)")
		tree := fs.Bool("tree", false, "merge parent run + descendants into one chronological stream (local only)")
		eventsOnly := fs.Bool("events-only", false, "filter to run-level envelope events (run_start, node_start, node_end, step_start, step_end, run_finish, plan_warn, ...) -- the bracketing NDJSON the dispatcher streams to stdout")
		noEvents := fs.Bool("no-events", false, "filter to per-node body output only -- useful when scripts depend on the legacy shape")
		if err := checkRetiredWhereFlags(args[1:]); err != nil {
			return err
		}
		if err := parseAndCheck(cmdJobsLogs, fs, args[1:]); err != nil {
			if errors.Is(err, errHelpRequested) {
				return nil
			}
			return err
		}
		*runID = normalizeRunID(*runID)
		resolvedFmt, err := resolveTTYAwareOutput(*outFmt, "jobs logs")
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
		if *profileName != "" {
			p, perr := resolveProfileFlag(*profileName)
			if perr != nil {
				return perr
			}
			opts.Profile = p
		}
		return orchestrator.JobLogs(ctx, paths, *runID, opts, os.Stdout)

	case "errors":
		fs := flag.NewFlagSet(cmdJobsErrors.Path, flag.ContinueOnError)
		runID := fs.String("run", "", "run identifier")
		outFmt := fs.StringP("output", "o", "", "output format: pretty|json|plain")
		if err := checkRetiredWhereFlags(args[1:]); err != nil {
			return err
		}
		if err := parseAndCheck(cmdJobsErrors, fs, args[1:]); err != nil {
			if errors.Is(err, errHelpRequested) {
				return nil
			}
			return err
		}
		*runID = normalizeRunID(*runID)
		resolvedFmt, err := resolveOutputFormat(*outFmt, "jobs errors")
		if err != nil {
			return err
		}
		emitJSON := resolvedFmt == "json"
		return orchestrator.JobErrors(ctx, paths, *runID, emitJSON, os.Stdout)

	case "cancel":
		return runRunsCancel(ctx, args[1:])
	case "retry":
		return runRunsRetry(ctx, args[1:])
	case "prune":
		return runRunsPrune(ctx, args[1:])

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
	case "timeline":
		return runJobsTimeline(ctx, paths, args[1:])
	case "summary":
		return runJobsSummary(ctx, paths, args[1:])
	case "grep":
		return runJobsGrep(ctx, paths, args[1:])
	default:
		return fmt.Errorf("jobs: unknown command %q", args[0])
	}
}

// resolveOutputFormat canonicalizes -o/--output into one of
// {"pretty","json","plain"}. Empty string means "no value set" and
// resolves to the default "pretty".
func resolveOutputFormat(outFmt, cmdPath string) (string, error) {
	switch outFmt {
	case "", "pretty", "json", "plain":
	default:
		return "", fmt.Errorf("%s: -o/--output must be one of pretty|json|plain, got %q", cmdPath, outFmt)
	}
	if outFmt == "" {
		return "pretty", nil
	}
	return outFmt, nil
}

// resolveTTYAwareOutput canonicalizes -o/--output for verbs that
// want runs-logs-style behavior: TTY-derived default fallback when
// no -o value is provided.
//
// Rules:
//   - With an explicit -o value, that value passes through.
//   - With nothing set: "pretty" when stdout is a TTY, "json" otherwise.
//     The auto-default lets agents pipe `... | jq` without typing
//     -o json, while humans get the readable form by default.
func resolveTTYAwareOutput(outFmt, cmdPath string) (string, error) {
	switch outFmt {
	case "", "pretty", "json", "plain":
	default:
		return "", fmt.Errorf("%s: -o/--output must be one of pretty|json|plain, got %q", cmdPath, outFmt)
	}
	if outFmt != "" {
		return outFmt, nil
	}
	if color.IsInteractiveStdout() {
		return "pretty", nil
	}
	return "json", nil
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
	defer func() { _ = st.Close() }()
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
	_, cfg, err := projectconfig.DiscoverPipelines(cwd)
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
