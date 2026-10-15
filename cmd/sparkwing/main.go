package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/bincache"
	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/gotoolchain"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/internal/pipelineentry"
	"github.com/sparkwing-dev/sparkwing/internal/repos"
	"github.com/sparkwing-dev/sparkwing/pkg/color"
	"github.com/sparkwing-dev/sparkwing/pkg/docs"
	"github.com/sparkwing-dev/sparkwing/pkg/gitenv"
	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"

	// safety: a systemd or launchd job has no zoneinfo of its own on a slim
	// host, and a schedule that declares tz: would fail to resolve its zone.
	_ "time/tzdata"
)

func init() {
	docs.Version = installedVersion()
}

func main() {
	toolchainActive = takeToolchainActive()

	if err := runSparkwing(os.Args[1:]); err != nil {
		if writeErr := writeInvocationError(os.Stderr, err, apierr.WriteJSON); writeErr != nil {
			fmt.Fprintln(os.Stderr, "sparkwing: write diagnostic:", writeErr)
		}
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
	var usage *clisurface.UsageError
	if errors.As(err, &usage) {
		return int(clisurface.ExitUsage)
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
	for _, option := range []struct{ name, value string }{
		{"publish", flags.publish}, {"sw-cache-from", flags.cacheFrom}, {"target", flags.target},
	} {
		if option.value != "" {
			return &apierr.Error{Code: apierr.CapabilityUnsupported, Message: "run: --" + option.name + " is unavailable in this build"}
		}
	}
	var err error

	if flags.profile != "" {
		return fmt.Errorf("run: --profile is removed; sparkwing run executes on this machine against its own store, so run on the profile's controller with sparkwing pipeline trigger %s --profile %s",
			pipelineName, flags.profile)
	}
	if flags.unknownRunnerFlag != "" {
		return fmt.Errorf("run: unknown runner flag %q; see `sparkwing run --help`, or put pipeline arguments after --", flags.unknownRunnerFlag)
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
	projectStart := ""
	if cwd, workingDirectoryErr := os.Getwd(); workingDirectoryErr == nil {
		projectStart = cwd
	}
	if err := projectconfig.CheckLegacy(projectStart); err != nil {
		return err
	}

	dir, err := findSparkwingDir()
	if err != nil {
		return err
	}
	helpRequested := slices.Contains(passthrough, "-h") || slices.Contains(passthrough, "--help")
	if !helpRequested {
		entry, err := pinnedPipelineAt(dir, flags.ref, pipelineName)
		if err != nil {
			return fmt.Errorf("run: %w", err)
		}
		if err := orchestrator.RefusePinnedLocalRun(entry); err != nil {
			return fmt.Errorf("run: %w", err)
		}
	}

	// safety: the exec replays this command, so the pin must settle before any daemon, worktree, run, or output.
	if err := switchToolchain(dir); err != nil {
		return err
	}

	// safety: errors dropped intentionally; read-only home shouldn't break dispatch.
	_ = repos.AutoRegister(filepath.Dir(dir))

	executionDir := dir
	source, err := orchestrator.ResolvePipelineSource(context.Background(), filepath.Dir(dir), pipelineName, flags.pipelineRef)
	if err != nil {
		return err
	}
	if source != "" && flags.ref != "" {
		return errors.New("a declared pipeline source cannot be combined with --sw-ref")
	}
	var afterChild func()
	if flags.ref != "" {
		_, pipelineDirectory, cleanup, err := setupRefWorktree(dir, flags.ref)
		if err != nil {
			return fmt.Errorf("--sw-ref %s: %w", flags.ref, err)
		}
		afterChild = cleanup
		defer cleanup()
		dir = pipelineDirectory
	}

	if source != "" {
		_, selectedDir, cleanup, err := setupRefWorktree(dir, string(source))
		if err != nil {
			return err
		}
		afterChild = cleanup
		defer cleanup()
		dir = selectedDir
	} else {
		executionDir = ""
	}

	env := os.Environ()
	logFormat := flags.outputFormat
	if flags.index != "" {
		bound, bindErr := bindRunIndex(env, flags.index, os.Stdout, logFormat)
		if bindErr != nil {
			return bindErr
		}
		env = bound
	}
	runHandle := ""
	if flags.runHandleFile != "" {
		path, pathErr := filepath.Abs(flags.runHandleFile)
		if pathErr != nil {
			return fmt.Errorf("--sw-run-handle-file %s: %w", flags.runHandleFile, pathErr)
		}
		runHandle = path
	}
	bincache.SetHashAllFiles(flags.hashAllFiles)
	run := newPipelineRun(dir, compileOptions{
		NoUpdate:     flags.noUpdate,
		NoBincache:   flags.noBincache,
		ExecutionDir: executionDir,
		AfterChild:   afterChild,
	})
	defer run.stop()
	run.ctx = gotoolchain.WithSession(run.ctx, env, nil)

	run.sparkwingDir = dir
	run.opts.ExecutionDir = executionDir
	run.opts.AfterChild = afterChild
	if err := run.materialize(env); err != nil {
		return run.finish(err)
	}
	if helpRequested {
		schema, err := run.describedPipeline(pipelineName)
		if err == nil {
			printPipelineHelp(os.Stdout, schema)
		}
		return run.finish(err)
	}
	schema, err := run.describedPipeline(pipelineName)
	var pipelineArgs map[string]string
	if err == nil {
		pipelineArgs, err = parsePipelineArgs(schema, passthrough)
	}
	if err != nil {
		return run.finish(fmt.Errorf("%s: %w", pipelineName, err))
	}
	findings := declaredRisks(run.ctx, dir, pipelineName)
	if err := enforceRiskGate(pipelineName, findings, flags); err != nil {
		return run.finish(err)
	}

	if !flags.dryRun {
		ensureRunDaemonFn()
	}
	sweepStraySessionsBeforeRun()
	return run.finish(run.host(hostedRun{
		schema: schema, args: pipelineArgs, flags: flags, env: env, source: string(source),
		priority: priority, logFormat: logFormat, runHandle: runHandle,
	}))
}

func pinnedPipeline(sparkwingDir, pipelineName string) (*pipelines.Pipeline, error) {
	cfg, err := projectconfig.LoadOld(filepath.Join(sparkwingDir, projectconfig.Filename))
	if err != nil || cfg == nil {
		return nil, err
	}
	return (&pipelines.Config{Pipelines: cfg.Pipelines}).Find(pipelineName), nil
}

// safety: --sw-ref executes another revision, so its sparkwing.yaml, not the
// checkout's, says whether the pipeline is pinned.
func pinnedPipelineAt(sparkwingDir, ref, pipelineName string) (*pipelines.Pipeline, error) {
	if ref == "" {
		return pinnedPipeline(sparkwingDir, pipelineName)
	}
	repoRoot := filepath.Dir(sparkwingDir)
	commit, err := orchestrator.ResolveRefCommit(context.Background(), repoRoot, ref, slog.Default())
	if err != nil {
		return nil, fmt.Errorf("--sw-ref %s: %w", ref, err)
	}
	path := ".sparkwing/" + projectconfig.Filename
	// #nosec G204 -- the commit is a resolved object ID and the path a constant.
	listed, err := exec.Command("git", "-C", repoRoot, "ls-tree", "--name-only", string(commit), "--", path).Output()
	if err != nil {
		return nil, fmt.Errorf("--sw-ref %s: list %s: %w", ref, path, err)
	}
	if strings.TrimSpace(string(listed)) == "" {
		return nil, nil
	}
	// #nosec G204 -- the commit is a resolved object ID and the path a constant.
	raw, err := exec.Command("git", "-C", repoRoot, "show", string(commit)+":./"+path).Output()
	if err != nil {
		return nil, fmt.Errorf("--sw-ref %s: read %s: %w", ref, path, err)
	}
	cfg, err := projectconfig.ParseOld(raw)
	if err != nil {
		return nil, fmt.Errorf("--sw-ref %s: %s: %w", ref, path, err)
	}
	return (&pipelines.Config{Pipelines: cfg.Pipelines}).Find(pipelineName), nil
}

func pinnedProfileHere(pipelineName string) (string, error) {
	if dir, dirErr := findSparkwingDir(); dirErr == nil {
		entry, err := pinnedPipeline(dir, pipelineName)
		if err != nil {
			return "", err
		}
		if entry != nil {
			return entry.Profile, nil
		}
	}
	return "", nil
}

func printPin(pipelineName string) {
	if pin, err := pinnedProfileHere(pipelineName); err == nil && pin != "" {
		fmt.Printf("Pinned: profile %s; run it with `sparkwing pipeline trigger %s --profile %s`\n", pin, pipelineName, pin)
	}
}

func runSparkwing(args []string) (result error) {
	inv, err := clisurface.Parse("sparkwing", args)
	defer func() {
		if result != nil {
			result = &invocationError{inv: inv, cause: result}
		}
	}()
	if err != nil {
		return err
	}
	if inv.Help() || inv.Command.Path != "sparkwing version" && len(childCommands(inv.Command.Path)) > 0 {
		return writeCommandHelp(surfaceHelp(inv.Command), os.Stdout, inv.Output(color.IsInteractiveStdout()))
	}
	if checkout := inv.Value("checkout"); checkout != "" {
		previous, err := os.Getwd()
		if err != nil {
			return err
		}
		if err := os.Chdir(checkout); err != nil {
			return err
		}
		defer func() { result = errors.Join(result, os.Chdir(previous)) }()
	}
	cleanupStaleUpdate()
	args = invocationArgs(inv)
	toolchainReplayArgs = args
	gitenv.Unbind()
	noteVersionTransition(os.Stderr, args[0])
	return dispatchCommand(args)
}

func invocationArgs(inv clisurface.Invocation) []string {
	args := strings.Fields(strings.TrimPrefix(inv.Command.Path, "sparkwing "))
	if inv.Pipeline != "" {
		args = append(args, inv.Pipeline)
	}
	for _, f := range append(clisurface.GlobalFlags(), inv.Command.Flags...) {
		if f.Name == "checkout" || f.Name == "help" || f.Name == "help-all" {
			continue
		}
		for _, value := range inv.Flags[f.Name] {
			args = append(args, "--"+f.Name+"="+value)
		}
	}
	if inv.Args != nil {
		args = append(args, "--")
		args = append(args, inv.Args...)
	}
	return args
}

func dispatchCommand(args []string) error {
	switch args[0] {
	case "info":
		return runInfo(args[1:])
	case "pipeline":
		return runPipeline(args[1:])
	case "run":
		return dispatchRun(args[1:])
	case "runs":
		return runJobs(args[1:])
	case "queue":
		return runQueue(args[1:])
	case "cache":
		return runCache(args[1:])
	case "daemon":
		return runDaemon(args[1:])
	case "dashboard":
		return runDashboard(args[1:])
	case "repos":
		return runRepos(args[1:])
	case "schedules":
		return runCrons(args[1:])
	case "runners":
		if args[1] == "list" {
			return runAgentsList(args[2:])
		}
		return runRunners(args[1:])
	case "tokens":
		return runTokens(args[1:])
	case "pools":
		return runPools(context.Background(), args[1:])
	case "profiles":
		return runProfiles(args[1:])
	case "operator":
		switch args[1] {
		case "limits":
			return runComputeLimits(args[2:])
		case "object-store":
			return runClusterObjectStore(args[2:])
		case "tokens":
			return runTokenCommand(context.Background(), args[2:], true)
		}
	case "doctor":
		return runDoctor(args[1:])
	case "secrets":
		return runSecret(args[1:])
	case "completion":
		return runCompletion(args[1:])
	case "examples":
		return runExamples(args[1:])
	case "docs":
		return runDocs(args[1:])
	case "commands":
		return runCommands(args[1:])
	case "upgrade":
		return runUpgrade(args[1:])
	case "update":
		return runUpdate(args[1:])
	case "version":
		return runVersion(args[1:])
	case "debug":
		return runDebug(args[1:])
	case "wingd":
		return runWingd(args[1:])
	case "__tick":
		return runCronsTick(args[1:])
	case "__host-trigger":
		return runTriggerHost(args[1:])
	case "__complete":
		return runInternalComplete(args[1:])
	}
	return fmt.Errorf("%s: execution is not implemented", args[0])
}

func runRunsApprovals(ctx context.Context, args []string) error {
	return runReviewCommand(ctx, append([]string{"runs", "approvals"}, args...), os.Stdout, resolveRunReview)
}

func runJobs(args []string) error {
	if handleParentHelp(cmdJobs, args) {
		return nil
	}
	if len(args) == 0 {
		PrintHelp(cmdJobs, os.Stderr)
		return errors.New("jobs: subcommand required")
	}
	contextValue := context.Background()

	switch args[0] {
	case "approvals":
		return runRunsApprovals(contextValue, args[1:])
	case "annotations":
		return runRunsAnnotations(contextValue, args[1:])
	case "list":
		return runRunsList(contextValue, args[1:])

	case "status":
		return runRunsStatus(contextValue, args[1:])

	case "logs", "grep":
		return runRunsLogs(contextValue, args[0], args[1:])

	case "consumer":
		return runRunsConsumer(args[1:])
	case "cancel":
		return runRunsCancel(contextValue, args[1:])
	case "retry":
		return runRunsRetry(contextValue, args[1:])
	case "prune":
		return runRunsPrune(contextValue, args[1:])

	case "stats":
		return runJobsStats(contextValue, args[1:])
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

func runIDFromArgs(path string, rest []string, flagValue string) (string, error) {
	switch {
	case len(rest) == 0 && flagValue == "":
		return "", fmt.Errorf("%s: a run id is required, as --run RUN_ID or as a positional", path)
	case len(rest) == 0:
		return flagValue, nil
	case len(rest) > 1:
		return "", fmt.Errorf("%s: unexpected positional %q", path, rest[1])
	case flagValue != "":
		return "", fmt.Errorf("%s: run id given twice; pass it as --run or as a positional", path)
	default:
		return rest[0], nil
	}
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
			ok, err := pipelineentry.IsPipelineDir(candidate)
			if err != nil {
				return "", err
			}
			if ok {
				return candidate, nil
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no .sparkwing/main.go or TypeScript .sparkwing/package.json found from %s up", abs)
		}
		dir = parent
	}
}

func mustGetwd() string {
	d, _ := os.Getwd()
	return d
}
