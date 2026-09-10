package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/internal/fleet"
	"github.com/sparkwing-dev/sparkwing/internal/gitenv"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/internal/repos"
	"github.com/sparkwing-dev/sparkwing/pkg/color"
	"github.com/sparkwing-dev/sparkwing/pkg/docs"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
	"github.com/sparkwing-dev/sparkwing/pkg/store"

	// safety: a systemd or launchd job has no zoneinfo of its own on a slim
	// host, and a schedule that declares tz: would fail to resolve its zone.
	_ "time/tzdata"
)

const fleetUntrackedSourceWarning = "fleet source: every non-ignored untracked file is included; review Git ignores before sharing with enrolled helpers"

func init() {
	docs.Version = installedVersion()
	store.SetBinaryVersion(installedVersion())
}

func main() {
	cleanupStaleUpdate()
	toolchainActive = takeToolchainActive()

	if err := runSparkwing(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, color.Red(color.Bold("sparkwing error:")), err)
		os.Exit(exitCodeFor(err))
	}
}

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
	var commandError *cliError
	if errors.As(err, &commandError) {
		if commandError.code == 0 {
			return 1
		}
		return commandError.code
	}
	return 1
}

func dispatchRun(args []string) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		if len(args) == 0 {
			PrintHelp(cmdRun, os.Stderr)
			return errors.New("run: pipeline name required; use `sparkwing run <pipeline>`")
		}
		PrintHelp(cmdRun, os.Stdout)
		return nil
	}

	pipelineName := args[0]
	if strings.HasPrefix(pipelineName, "-") {
		PrintHelp(cmdRun, os.Stderr)
		return fmt.Errorf("run: pipeline name must come first; got flag %q", pipelineName)
	}
	flags, passthrough := parseRunFlags(args[1:])
	if flags.parseErr != nil {
		return flags.parseErr
	}
	var err error

	runnerArgs := passthrough
	if separator := slices.Index(runnerArgs, "--"); separator >= 0 {
		runnerArgs = runnerArgs[:separator]
	}
	if err := checkRetiredWhereFlags(runnerArgs, nil); err != nil {
		return err
	}
	if flags.unknownRunnerFlag != "" {
		return fmt.Errorf("run: unknown runner flag %q; see `sparkwing run --help`, or put pipeline arguments after --", flags.unknownRunnerFlag)
	}
	if separator := slices.Index(passthrough, "--"); separator >= 0 {
		passthrough = slices.Delete(passthrough, separator, separator+1)
	}
	priority := ""
	if flags.prioritySet {
		priority, err = validatePriorityFlag(flags.priority)
		if err != nil {
			return err
		}
	}
	// safety: the consumer owns execution setup for detached runs.
	if flags.detached {
		return runDetached(context.Background(), pipelineName, flags, passthrough)
	}
	if err := refuseDetachedOnlyFlags(flags); err != nil {
		return err
	}
	// safety: before the toolchain re-exec and the daemon pre-warm, because both
	// resolve this machine's home from the environment this call rewrites.
	if flags.isolatedHome != "" {
		if err := applyIsolatedHome(flags.isolatedHome); err != nil {
			return err
		}
	}
	if flags.profile != "" {
		if _, profileErr := resolveProfileFlag(flags.profile); profileErr != nil {
			return profileErr
		}
	}

	projectStart := flags.changeDir
	if projectStart == "" {
		if cwd, workingDirectoryErr := os.Getwd(); workingDirectoryErr == nil {
			projectStart = cwd
		}
	}
	if err := projectconfig.CheckLegacy(projectStart); err != nil {
		return err
	}

	var dir string
	if flags.changeDir != "" {
		dir, err = findSparkwingDirFrom(flags.changeDir)
	} else {
		dir, err = findSparkwingDir()
	}
	if err != nil {
		return err
	}

	// safety: the exec replays this command, so the pin must settle before any daemon, worktree, run, or output.
	if err := switchToolchain(dir); err != nil {
		return err
	}

	// safety: errors dropped intentionally; read-only home shouldn't break dispatch.
	_ = repos.AutoRegister(filepath.Dir(dir))

	if findings := lookupCachedRisks(dir, pipelineName); len(findings) > 0 {
		if err := enforceRiskGate(pipelineName, findings, flags); err != nil {
			return err
		}
	}

	if flags.ref != "" {
		_, pipelineDirectory, cleanup, err := setupRefWorktree(dir, flags.ref)
		if err != nil {
			return fmt.Errorf("--sw-ref %s: %w", flags.ref, err)
		}
		defer cleanup()
		dir = pipelineDirectory
	}

	env := os.Environ()
	env = removeEnv(env, "SPARKWING_RUN_HANDLE_FILE")
	logFormat := os.Getenv("SPARKWING_LOG_FORMAT")
	if logFormat == "" {
		logFormat = logFormatJSON
		if color.IsInteractiveStdout() {
			logFormat = logFormatPretty
		}
		env = append(env, "SPARKWING_LOG_FORMAT="+logFormat)
	}

	if flags.index != "" {
		bound, bindErr := bindRunIndex(env, flags.index, os.Stdout, logFormat)
		if bindErr != nil {
			return bindErr
		}
		env = bound
	}
	if flags.runHandleFile != "" {
		path, pathErr := filepath.Abs(flags.runHandleFile)
		if pathErr != nil {
			return fmt.Errorf("--sw-run-handle-file %s: %w", flags.runHandleFile, pathErr)
		}
		env = setEnv(env, "SPARKWING_RUN_HANDLE_FILE", path)
	}
	if flags.verbose {
		env = append(env, "SPARKWING_LOG_LEVEL=debug")
	}
	if flags.startAt != "" {
		env = append(env, "SPARKWING_START_AT="+flags.startAt)
	}
	if flags.stopAt != "" {
		env = append(env, "SPARKWING_STOP_AT="+flags.stopAt)
	}
	if flags.dryRun {
		env = append(env, "SPARKWING_DRY_RUN=1")
	}
	if flags.only != "" {
		env = append(env, "SPARKWING_ONLY="+flags.only)
	}
	if flags.noCache {
		env = append(env, "SPARKWING_NO_CACHE=1")
	}
	if flags.localOnly {
		env = append(env, "SPARKWING_LOCAL_ONLY=1")
	}
	var fleetSnapshot *worktreeSnapshot
	if flags.fleet {
		configPath := os.Getenv("SPARKWING_FLEET_CONFIG")
		if configPath == "" {
			configPath, err = fleet.DefaultPath()
			if err != nil {
				return fmt.Errorf("--sw-fleet config: %w", err)
			}
		}
		fleetConfig, err := fleet.Load(configPath, fleet.LocalTailscaleIPs)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("--sw-fleet config %s does not exist; create it with `sparkwing fleet init --tailnet` or explicit --listen and --public-url", configPath)
			}
			return fmt.Errorf("--sw-fleet config %s: %w", configPath, err)
		}
		if len(fleetConfig.Executors) == 0 {
			return errors.New("--sw-fleet has no enrolled helpers; add one with `sparkwing fleet agents enroll --name ... --location ...`")
		}
		env = setEnv(env, "SPARKWING_FLEET_CONFIG", configPath)
		if err := resolveSparks(context.Background(), dir, compileOptions{NoUpdate: flags.noUpdate}); err != nil {
			return err
		}
		fleetSnapshot, err = captureWorktreeSnapshot(context.Background(), filepath.Dir(dir))
		if err != nil {
			return fmt.Errorf("--sw-fleet source: %w", err)
		}
		defer func() { _ = fleetSnapshot.close() }()
		checkout, repoURL, checkoutErr := fleetSnapshot.materialize(context.Background())
		if checkoutErr != nil {
			return fmt.Errorf("--sw-fleet source: %w", checkoutErr)
		}
		fmt.Fprintf(os.Stderr, "fleet source: snapshot %s (%d files, %s uncompressed; %s bundle)\n",
			fleetSnapshot.SHA, fleetSnapshot.FileCount, snapshotBytes(fleetSnapshot.Size), snapshotBytes(fleetSnapshot.BundleSize))
		fmt.Fprintln(os.Stderr, fleetUntrackedSourceWarning)
		dir = filepath.Join(checkout, ".sparkwing")
		env = setEnv(env, "SPARKWING_FLEET", "1")
		env = setEnv(env, "SPARKWING_FLEET_SOURCE_ROOT", fleetSnapshot.tempDir)
		env = setEnv(env, "SPARKWING_FLEET_SOURCE_BUNDLE", fleetSnapshot.BundlePath)
		env = setEnv(env, "SPARKWING_FLEET_SOURCE_SHA", fleetSnapshot.SHA)
		env = setEnv(env, "SPARKWING_FLEET_SOURCE_MANIFEST_DIGEST", fleetSnapshot.ManifestDigest)
		env = setEnv(env, "SPARKWING_FLEET_SOURCE_REPO_URL", repoURL)
		env = setEnv(env, "SPARKWING_FLEET_SOURCE_FILES", strconv.Itoa(fleetSnapshot.FileCount))
		env = setEnv(env, "SPARKWING_FLEET_SOURCE_BYTES", strconv.FormatInt(fleetSnapshot.Size, 10))
		env = setEnv(env, "SPARKWING_FLEET_SOURCE_BUNDLE_BYTES", strconv.FormatInt(fleetSnapshot.BundleSize, 10))
	}
	if len(flags.allow) > 0 {
		env = append(env, "SPARKWING_ALLOW="+strings.Join(flags.allow, ","))
	}
	if flags.ref != "" {
		env = append(env, "SPARKWING_REF="+flags.ref)
	}
	if flags.noUpdate {
		env = append(env, "SPARKWING_NO_UPDATE=1")
	}
	if flags.profile != "" {
		env = setEnv(env, "SPARKWING_PROFILE", flags.profile)
	}
	if flags.secrets != "" {
		env = append(env, "SPARKWING_SECRETS_PROFILE="+flags.secrets)
	}

	if flags.mode != "" {
		env = append(env, "SPARKWING_MODE="+flags.mode)
		if flags.workers > 0 {
			env = append(env, fmt.Sprintf("SPARKWING_WORKERS=%d", flags.workers))
		}
	}

	// safety: relative priority resolves against the queue at admission time.
	if priority != "" {
		env = setEnv(env, orchestrator.PriorityEnv, priority)
	}

	if runNeedsDaemon(flags, passthrough) {
		ensureRunDaemonFn()
	}
	var fleetParentGuard *fleet.ParentGuard
	if flags.fleet {
		fleetParentGuard, err = fleet.StartParentGuard()
		if err != nil {
			return fmt.Errorf("--sw-fleet coordinator lifetime: %w", err)
		}
		defer fleetParentGuard.Close()
		env = setEnv(env, "SPARKWING_FLEET_PARENT_GUARD", fleetParentGuard.Address)
		env = setEnv(env, "SPARKWING_FLEET_PARENT_TOKEN", fleetParentGuard.Token)
	}
	sweepStraySessionsBeforeRun()
	return compileAndExec(dir, append([]string{pipelineName}, passthrough...), env,
		compileOptions{NoUpdate: flags.noUpdate || flags.fleet})
}

func removeEnv(env []string, key string) []string {
	prefix := key + "="
	out := env[:0]
	for _, entry := range env {
		if !strings.HasPrefix(entry, prefix) {
			out = append(out, entry)
		}
	}
	return out
}

func runSparkwing(args []string) error {
	if removedDashboardCommand(args) {
		return errors.New("dashboard was removed; use sparkwing serve (for example, sparkwing serve start)")
	}
	args = moveRootOutput(args)
	if cmd, ok := commandHelp(args); ok {
		requested, _, err := requestedOutput(args)
		if err != nil {
			return err
		}
		mode, err := resolveOutputFormat(requested, cmd.Path)
		if err != nil {
			return err
		}
		return writeCommandHelp(*cmd, os.Stdout, mode)
	}
	gitenv.Unbind()
	noteVersionTransition(os.Stderr, args[0])
	switch args[0] {
	case "info":
		return runInfo(args[1:])
	case "pipeline":
		return runPipeline(args[1:])
	case "run":
		return dispatchRun(args[1:])
	case "run-node":
		return runNodeCommand(args[1:])
	case "runs":
		return runJobs(args[1:])
	case "queue":
		return runQueue(args[1:])
	case "cache":
		return runCache(args[1:])
	case "daemon":
		return runDaemon(args[1:])
	case "profile":
		return runProfileCmd(args[1:])

	case "serve":
		return runDashboard(args[1:])

	case "repos":
		return runRepos(args[1:])

	case "crons":
		return runCrons(args[1:])

	case "cluster":
		return runCluster(args[1:])
	case "fleet":
		return runFleet(args[1:])
	case "doctor":
		return runDoctor(args[1:])
	case "secrets":
		return runSecret(args[1:])

	case "configure":
		return runConfigure(args[1:])
	case "completion":
		return runCompletion(args[1:])
	case "examples":
		return runExamples(args[1:])
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
	case "wingd":
		return runWingd(args[1:])
	case queueExecGuardCommandName:
		return runQueueExecGuard(args[1:])
	case "__dashboard-supervise":
		return runDashboardSupervise(args[1:])
	case consumerSpawnVerb:
		return runRunsConsumeDetached(args[1:])
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

func runRunsApprovals(contextValue context.Context, paths orchestrator.Paths, args []string) error {
	if handleParentHelp(cmdApprovals, args) {
		return nil
	}
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return runApprovalsList(contextValue, paths, args)
	}
	switch args[0] {
	case "list":
		return runApprovalsList(contextValue, paths, args[1:])
	case "approve":
		return runApprove(contextValue, paths, args[1:])
	case "deny":
		return runDeny(contextValue, paths, args[1:])
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
	case "users":
		return runUsers(args[1:])
	case "tokens":
		return runTokens(args[1:])
	case "image":
		return runImage(args[1:])
	case "webhooks":
		return runWebhooks(args[1:])
	case "concurrency":
		return runConcurrency(args[1:])
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
	contextValue := context.Background()

	switch args[0] {
	case "approvals":
		return runRunsApprovals(contextValue, paths, args[1:])
	case "annotations":
		return runRunsAnnotations(contextValue, paths, args[1:])
	case "triggers":
		return runTriggers(args[1:])
	case "list":
		fs := flag.NewFlagSet(cmdJobsList.Path, flag.ContinueOnError)
		limit := fs.Int("limit", 20, "maximum runs to show")
		outputFormat := fs.StringP("output", "o", "", "output format: pretty|json|plain (default: table)")
		quiet := fs.BoolP("quiet", "q", false, "print only run ids, one per line")
		since := lookbackDuration(fs, "since", 0, "only runs newer than this (1h, 24h, 7d, and similar durations)")
		pipelines := multiFlagVar(fs, "pipeline", "filter by pipeline (repeatable; OR semantics; prefix `!` to exclude)")
		statuses := multiFlagVar(fs, "status", "filter by status (repeatable; OR semantics; prefix `!` to exclude)")
		branches := multiFlagVar(fs, "branch", "filter by git branch (repeatable; prefix `!` to exclude)")
		shas := multiFlagVar(fs, "sha", "filter by git sha prefix (repeatable; prefix `!` to exclude)")
		errorSubstring := fs.String("error", "", "substring match against the persisted failure reason")
		search := fs.String("search", "", "free-text search across pipeline/branch/sha/id/error; prefix a term with `-` to exclude")
		startedAfter := fs.String("started-after", "", "only runs whose StartedAt >= this (today, yesterday, 24h, 7d, or a date)")
		startedBefore := fs.String("started-before", "", "only runs whose StartedAt <= this")
		finishedAfter := fs.String("finished-after", "", "only runs whose FinishedAt >= this (excludes still-running)")
		finishedBefore := fs.String("finished-before", "", "only runs whose FinishedAt <= this (excludes still-running)")
		byPipeline := fs.Bool("by-pipeline", false, "pivot into one row per pipeline with a status sparkline of the last N runs")
		sparkline := fs.Int("sparkline", 30, "length of the sparkline when --by-pipeline is set")
		style := fs.String("style", "ascii", "sparkline glyph style: ascii|block|dot")
		profileName := fs.String("profile", "", "read against the named storage profile (~/.config/sparkwing/profiles.yaml, then the project's profiles: block; default: the project's defaults.profile)")
		if err := checkRetiredWhereFlags(args[1:], nil); err != nil {
			return err
		}
		if err := parseAndCheck(cmdJobsList, fs, args[1:]); err != nil {
			if errors.Is(err, errHelpRequested) {
				return nil
			}
			return err
		}
		resolvedFormat, err := resolveOutputFormat(*outputFormat, "runs list")
		if err != nil {
			return err
		}

		pipelineInc, pipelineExc := orchestrator.SplitExcludes(*pipelines)
		statusInc, statusExc := orchestrator.SplitExcludes(*statuses)
		branchInc, branchExc := orchestrator.SplitExcludes(*branches)
		shaInc, shaExc := orchestrator.SplitExcludes(*shas)

		pipelineSet := pipelineInc

		compiled := orchestrator.CompiledFilter{
			Branches:       branchInc,
			BranchExcludes: branchExc,
			SHAPrefixes:    shaInc,
			SHAExcludes:    shaExc,
			ErrorSubstr:    *errorSubstring,
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
				return fmt.Errorf("runs list: --%s: %w", ts.name, err)
			}
			*ts.into = t
		}

		var sparklineStyle orchestrator.SparklineStyle
		switch *style {
		case "ascii", "":
			sparklineStyle = orchestrator.SparkASCII
		case "block":
			sparklineStyle = orchestrator.SparkBlock
		case "dot":
			sparklineStyle = orchestrator.SparkDot
		default:
			return fmt.Errorf("runs list: --style must be ascii|block|dot, got %q", *style)
		}

		listOptions := orchestrator.ListOpts{
			Limit:      *limit,
			Pipelines:  pipelineSet,
			Statuses:   statusInc,
			Since:      *since,
			JSON:       resolvedFormat == "json",
			Quiet:      *quiet,
			Filter:     compiled,
			ByPipeline: *byPipeline,
			Pivot: orchestrator.PivotOpts{
				SparklineLen: *sparkline,
				Style:        sparklineStyle,
			},
		}

		p, profileErr := resolveProfileFlag(*profileName)
		if profileErr != nil {
			return profileErr
		}
		listOptions.Profile = p
		return orchestrator.ListJobs(contextValue, paths, listOptions, os.Stdout)

	case "status":
		fs := flag.NewFlagSet(cmdJobsStatus.Path, flag.ContinueOnError)
		runID := fs.String("run", "", "run identifier")
		outputFormat := fs.StringP("output", "o", "", "output format: json|table|plain (default: table)")
		follow := fs.BoolP("follow", "f", false, "poll until the run reaches a terminal state")
		steps := fs.Bool("steps", false, "render every step on every node in plain output")
		profileName := fs.String("profile", "", "read against the named storage profile (~/.config/sparkwing/profiles.yaml, then the project's profiles: block; default: the project's defaults.profile)")
		exitZero := fs.Bool("exit-zero", false,
			"return exit code 0 even when the run failed/cancelled (opt out of the scriptable exit contract)")
		if err := checkRetiredWhereFlags(args[1:], nil); err != nil {
			return err
		}
		if err := parseAndCheck(cmdJobsStatus, fs, args[1:]); err != nil {
			if errors.Is(err, errHelpRequested) {
				return nil
			}
			return err
		}
		*runID = normalizeRunID(*runID)
		resolvedFormat, err := resolveOutputFormat(*outputFormat, "runs status")
		if err != nil {
			return err
		}
		statusOptions := orchestrator.StatusOpts{JSON: resolvedFormat == "json", Follow: *follow, Steps: *steps}
		p, profileErr := resolveProfileFlag(*profileName)
		if profileErr != nil {
			return profileErr
		}
		statusOptions.Profile = p
		if err := orchestrator.JobStatus(contextValue, paths, *runID, statusOptions, os.Stdout); err != nil {
			return err
		}
		if *exitZero {
			return nil
		}

		status, statusErr := orchestrator.RunStatus(contextValue, paths, p, *runID)
		if statusErr != nil {
			return statusErr
		}
		return statusExitCode(status)

	case "logs":
		fs := flag.NewFlagSet(cmdJobsLogs.Path, flag.ContinueOnError)
		runID := fs.String("run", "", "run identifier")
		node := fs.String("node", "", "limit output to one node id")
		outputFormat := fs.StringP("output", "o", "", "output format: pretty|json|plain (default: pretty on TTY, json when piped)")
		follow := fs.BoolP("follow", "f", false, "tail the log(s) until the run terminates")
		profileName := fs.String("profile", "", "read against the named storage profile (~/.config/sparkwing/profiles.yaml, then the project's profiles: block; default: the project's defaults.profile)")
		tail := fs.Int("tail", 0, "print only the last N lines (server-side in cluster mode)")
		head := fs.Int("head", 0, "print only the first N lines (server-side in cluster mode)")
		lines := fs.String("lines", "", "1-indexed inclusive line range A:B (server-side in cluster mode)")
		grep := fs.String("grep", "", "substring filter (server-side in cluster mode)")
		since := fs.Duration("since", 0,
			"only include output from nodes whose StartedAt >= now-D (5m, 1h, and similar durations)")
		tree := fs.Bool("tree", false, "merge parent run + descendants into one chronological stream (local only)")
		eventsOnly := fs.Bool("events-only", false, "show run and step lifecycle events; stored runs use their recorded events")
		noEvents := fs.Bool("no-events", false, "show node output only")
		if err := checkRetiredWhereFlags(args[1:], nil); err != nil {
			return err
		}
		if err := parseAndCheck(cmdJobsLogs, fs, args[1:]); err != nil {
			if errors.Is(err, errHelpRequested) {
				return nil
			}
			return err
		}
		*runID = normalizeRunID(*runID)
		resolvedFormat, err := resolveTTYAwareOutput(*outputFormat, "runs logs")
		if err != nil {
			return err
		}
		if *tail > 0 && *head > 0 {
			return errors.New("runs logs: --tail and --head cannot be combined")
		}
		options := orchestrator.LogsOpts{
			Node:       *node,
			JSON:       resolvedFormat == "json",
			Follow:     *follow,
			Format:     resolvedFormat,
			Tail:       *tail,
			Head:       *head,
			Lines:      *lines,
			Grep:       *grep,
			Since:      *since,
			Tree:       *tree,
			EventsOnly: *eventsOnly,
			NoEvents:   *noEvents,
		}
		p, profileErr := resolveProfileFlag(*profileName)
		if profileErr != nil {
			return profileErr
		}
		options.Profile = p
		return orchestrator.JobLogs(contextValue, paths, *runID, options, os.Stdout)

	case "errors":
		fs := flag.NewFlagSet(cmdJobsErrors.Path, flag.ContinueOnError)
		runID := fs.String("run", "", "run identifier")
		outputFormat := fs.StringP("output", "o", "", "output format: pretty|json|plain")
		if err := checkRetiredWhereFlags(args[1:], nil); err != nil {
			return err
		}
		if err := parseAndCheck(cmdJobsErrors, fs, args[1:]); err != nil {
			if errors.Is(err, errHelpRequested) {
				return nil
			}
			return err
		}
		*runID = normalizeRunID(*runID)
		resolvedFormat, err := resolveOutputFormat(*outputFormat, "runs errors")
		if err != nil {
			return err
		}
		emitJSON := resolvedFormat == "json"
		return orchestrator.JobErrors(contextValue, paths, *runID, emitJSON, os.Stdout)

	case "consumer":
		return runRunsConsumer(args[1:])
	case "cancel":
		return runRunsCancel(contextValue, args[1:])
	case "bounce":
		return runRunsBounce(contextValue, args[1:])
	case "retry":
		return runRunsRetry(contextValue, args[1:])
	case "prune":
		return runRunsPrune(contextValue, args[1:])

	case "failures":
		return runJobsFailures(contextValue, paths, args[1:])
	case "stats":
		return runJobsStats(contextValue, paths, args[1:])
	case "last":
		return runJobsLast(contextValue, paths, args[1:])
	case "tree":
		return runJobsTree(contextValue, paths, args[1:])
	case "get":
		return runJobsGet(contextValue, paths, args[1:])
	case "receipt":
		return runJobsReceipt(contextValue, paths, args[1:])
	case "wait":
		return runJobsWait(contextValue, paths, args[1:])
	case "find":
		return runJobsFind(contextValue, paths, args[1:])
	case "timeline":
		return runJobsTimeline(contextValue, paths, args[1:])
	case "summary":
		return runJobsSummary(contextValue, paths, args[1:])
	case "grep":
		return runJobsGrep(contextValue, paths, args[1:])
	default:
		return fmt.Errorf("runs: unknown command %q", args[0])
	}
}

func resolveOutputFormat(outputFormat, cmdPath string) (string, error) {
	return resolveTTYAwareOutput(outputFormat, cmdPath)
}

func resolveTTYAwareOutput(outputFormat, cmdPath string) (string, error) {
	switch outputFormat {
	case "pretty", "json", "plain":
		return outputFormat, nil
	case "":
		if color.IsInteractiveStdout() {
			return "pretty", nil
		}
		return "json", nil
	default:
		return "", fmt.Errorf("%s: -o/--output must be one of pretty|json|plain, got %q", cmdPath, outputFormat)
	}
}

func isTerminalRunStatus(s string) bool {
	return s == "success" || s == "failed" || s == "cancelled"
}

func normalizeRunID(id string) string {
	if id == "" || strings.HasPrefix(id, "run-") {
		return id
	}
	return "run-" + id
}

func statusExitCode(status string) error {
	if status == "success" {
		return nil
	}
	return exitErrorf(1, "run status: %s", status)
}

func multiFlagVar(fs *flag.FlagSet, name, usage string) *[]string {
	var dest []string
	fs.StringSliceVar(&dest, name, nil, usage)
	return &dest
}

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
