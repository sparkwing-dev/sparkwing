package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/internal/pipelineentry"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
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

// safety: main prints the upgrade notice after the command's output and any failure, so the
// first line a reader sees is the answer; a run that hands off to a pinned engine skips it.
var noticeVerb string

func main() {
	err := runSparkwing(os.Args[1:])
	if err != nil {
		writeFailure(os.Stderr, err)
	}
	if noticeVerb != "" {
		noteVersionTransition(os.Stderr, noticeVerb)
	}
	if err != nil {
		os.Exit(exitCodeFor(err))
	}
}

func writeFailure(w io.Writer, err error) {
	var reported *cliError
	if errors.As(err, &reported) && reported.reported {
		return
	}
	if writeErr := writeInvocationError(w, err, apierr.WriteJSON); writeErr != nil {
		fmt.Fprintln(w, "sparkwing: write diagnostic:", writeErr)
	}
}

type cliError struct {
	code int
	err  error
	// reported marks an exit whose output already stated the outcome, so no
	// diagnostic follows the command's document.
	reported bool
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

const exitRefused = 4

func observationError(err error) error {
	var classified *cliError
	var usage *clisurface.UsageError
	if err == nil || errors.As(err, &classified) || errors.As(err, &usage) {
		return err
	}
	var failure *apierr.Error
	if errors.As(err, &failure) {
		switch failure.Code {
		case apierr.NotFound, apierr.Unauthenticated, apierr.Forbidden, apierr.BadRequest, apierr.InvalidArgs,
			apierr.UnsupportedRoute, apierr.MethodNotAllowed, apierr.VersionRevoked, apierr.VersionUnsupported:
			return exitError(exitRefused, err)
		}
	}
	return exitError(3, err)
}

func reportedExit(err error) error {
	if err == nil {
		return nil
	}
	return &cliError{code: exitCodeFor(err), err: err, reported: true}
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

func dispatchRunWithEngine(inv clisurface.Invocation, selectEngine func(string, func()) error) (resultErr error) {
	pipelineName := inv.Pipeline
	flags, passthrough := runFlagsFromInvocation(inv)
	if flags.parseErr != nil {
		return flags.parseErr
	}
	helpRequested := flags.invocation.Help() || slices.Contains(passthrough, "-h") || slices.Contains(passthrough, "--help")
	if !helpRequested {
		if err := validateLocalRunFlags(flags); err != nil {
			return err
		}
	}
	dir, err := findSparkwingDir()
	if err != nil {
		return err
	}
	if helpRequested {
		return runSelectedHelp(dir, pipelineName, flags)
	}
	home, err := wingd.HomeDir("")
	if err != nil {
		return err
	}
	route, err := loadRunRouting(dir, pipelineName)
	if err != nil {
		return err
	}
	flags.environment = prepareLocalEnvironment(os.Environ(), route.LocalSecrets)
	captured, masker := flags.environment.captured, flags.environment.masker
	selection, err := selectLocalRunSource(context.Background(), home, dir, pipelineName, flags, captured, masker)
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, selection.execution.Close()) }()
	code, err := selection.checkout(context.Background(), home, captured)
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, code.Close()) }()
	flags.sourceSelection = &selection
	entry, err := pinnedPipeline(selection.execution.Directory, pipelineName)
	if err != nil {
		return fmt.Errorf("run: %w", err)
	}
	var closeErr error
	beforeExec := func() { closeErr = errors.Join(closeErr, code.Close(), selection.execution.Close()) }
	route, err = loadRunRouting(selection.execution.Directory, pipelineName)
	if err != nil {
		return err
	}
	if route.Route == nil || *route.Route == "local" {
		if err := orchestrator.RefusePinnedLocalRun(entry); err != nil {
			return fmt.Errorf("run: %w", err)
		}
	}

	if err := selectEngine(code.Directory, beforeExec); err != nil {
		return errors.Join(err, closeErr)
	}
	if closeErr != nil {
		return closeErr
	}

	return runConfiguredInvocation(pipelineName, flags, passthrough)
}

func pinnedPipeline(sparkwingDir, pipelineName string) (*pipelines.Pipeline, error) {
	cfg, err := projectconfig.Load(filepath.Join(sparkwingDir, projectconfig.Filename))
	if err != nil || cfg == nil {
		return nil, err
	}
	return cfg.Registry().Find(pipelineName), nil
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
	gitenv.Unbind()
	inv, err := clisurface.Parse("sparkwing", args)
	defer func() {
		if result != nil {
			result = &invocationError{inv: inv, cause: result}
		}
	}()
	if err != nil {
		return err
	}
	pipelineHelp := inv.Command.Path == "sparkwing run" && inv.Pipeline != ""
	if inv.Help() && !pipelineHelp || inv.Command.Path != "sparkwing version" && len(childCommands(inv.Command.Path)) > 0 {
		return writeCommandHelp(surfaceHelp(inv.Command), os.Stdout, inv.Output(color.IsInteractiveStdout()))
	}
	replayDirectory := ""
	if checkout := inv.Value("checkout"); checkout != "" {
		previous, err := os.Getwd()
		if err != nil {
			return err
		}
		if err := os.Chdir(checkout); err != nil {
			return err
		}
		replayDirectory = previous
		defer func() { result = errors.Join(result, os.Chdir(previous)) }()
	}
	if inv.Command.Path == "sparkwing __sdk-install" {
		return runSDKInstall(inv)
	}
	cleanupStaleUpdate()
	previousArgs, previousDirectory := toolchainReplayArgs, toolchainReplayDirectory
	toolchainReplayArgs = append([]string(nil), args...)
	toolchainReplayDirectory = replayDirectory
	defer func() { toolchainReplayArgs, toolchainReplayDirectory = previousArgs, previousDirectory }()
	// Scripts often merge stderr into the JSON they parse, so a JSON
	// invocation leaves the stamp for the next human-facing one; info carries
	// the notice inside its own document.
	if verb := strings.Fields(inv.Command.Path)[1]; verb == "info" {
		noteVersionTransition(os.Stderr, verb)
	} else if inv.Output(color.IsInteractiveStdout()) != "json" {
		noticeVerb = verb
	}
	switch {
	case strings.HasPrefix(inv.Command.Path, "sparkwing tokens "), strings.HasPrefix(inv.Command.Path, "sparkwing operator tokens "):
		return runTokenCommand(context.Background(), inv)
	case strings.HasPrefix(inv.Command.Path, "sparkwing queue "):
		return runQueue(inv)
	case strings.HasPrefix(inv.Command.Path, "sparkwing dashboard "):
		return runDashboard(inv)
	case strings.HasPrefix(inv.Command.Path, "sparkwing profiles "):
		return runProfiles(inv)
	case strings.HasPrefix(inv.Command.Path, "sparkwing secrets "):
		return runSecret(inv)
	case strings.HasPrefix(inv.Command.Path, "sparkwing schedules "):
		return runSchedules(context.Background(), inv)
	case strings.HasPrefix(inv.Command.Path, "sparkwing pools "):
		return runPools(context.Background(), inv)
	case strings.HasPrefix(inv.Command.Path, "sparkwing overflow "):
		return runOverflow(context.Background(), inv)
	case strings.HasPrefix(inv.Command.Path, "sparkwing billing "):
		return runBilling(context.Background(), inv)
	case strings.HasPrefix(inv.Command.Path, "sparkwing operator teams "):
		return runOperatorTeams(inv)
	case strings.HasPrefix(inv.Command.Path, "sparkwing operator limits "):
		return runOperatorLimits(inv)
	case strings.HasPrefix(inv.Command.Path, "sparkwing operator object-store "):
		return runOperatorObjectStore(inv)
	case strings.HasPrefix(inv.Command.Path, "sparkwing operator secrets "):
		return runOperatorSecretsCommand(context.Background(), inv, os.Stdout, resolveOperatorManager)
	case strings.HasPrefix(inv.Command.Path, "sparkwing runs approvals "), strings.HasPrefix(inv.Command.Path, "sparkwing runs annotations "):
		return runReviewCommand(context.Background(), inv, os.Stdout, resolveRunReview)
	}
	switch inv.Command.Path {
	case "sparkwing team settings show", "sparkwing team settings set":
		return runTeamSettings(context.Background(), inv)
	case "sparkwing run":
		return dispatchRunWithEngine(inv, switchToolchain)
	case "sparkwing pipeline list":
		return runPipelineList(inv)
	case "sparkwing pipeline describe":
		return runPipelineDescribe(inv)
	case "sparkwing pipeline plan":
		return runPipelinePlan(inv)
	case "sparkwing pipeline lint":
		return runPipelineLint(inv)
	case "sparkwing pipeline trigger":
		return runPipelineTrigger(inv)
	case "sparkwing pipeline sparks list":
		return runSparksList(inv)
	case "sparkwing pipeline sparks add":
		return runSparksAdd(inv)
	case "sparkwing pipeline sparks remove":
		return runSparksRemove(inv)
	case "sparkwing pipeline sparks catalog":
		return runSparksCatalog(inv)
	case "sparkwing pipeline sparks lint":
		return runSparksLint(inv)
	case "sparkwing pipeline sparks update":
		return runSparksUpdate(inv)
	case "sparkwing pipeline sparks inflate":
		return runSparksInflate(inv)
	case "sparkwing cache info":
		return runCacheInfo(inv)
	case "sparkwing cache explain":
		return runCacheExplain(inv)
	case "sparkwing cache prune":
		return runCachePrune(inv)
	case "sparkwing daemon recover-state":
		return runDaemonRecoverState(inv)
	case "sparkwing repos list":
		return runReposList(inv)
	case "sparkwing repos add":
		return runXrepoAdd(inv)
	case "sparkwing repos remove":
		return runXrepoRemove(inv)
	case "sparkwing repos prune":
		return runXrepoPrune(inv)
	case "sparkwing repos info":
		return runReposInfo(inv)
	case "sparkwing repos update":
		return runReposUpdate(inv)
	case "sparkwing runners add":
		return runRunnersAdd(inv)
	case "sparkwing runners remove":
		return runRunnersRemove(inv)
	case "sparkwing completion":
		return runCompletion(inv)
	case "sparkwing runs logs", "sparkwing runs grep":
		return runRunsLogs(context.Background(), inv)
	case "sparkwing debug attach", "sparkwing debug env":
		return runDebug(inv)
	case "sparkwing __complete":
		return runInternalComplete(inv)
	case "sparkwing update":
		return runUpdate(inv)
	case "sparkwing upgrade":
		return runUpgrade(inv)
	case "sparkwing examples":
		return runExamples(inv)
	case "sparkwing doctor":
		return runDoctor(inv)
	case "sparkwing cache warm":
		return runCacheWarm(inv)
	case "sparkwing runs list":
		return runRunsList(context.Background(), inv)
	case "sparkwing runs status":
		return runRunsStatus(context.Background(), inv)
	case "sparkwing runs stats":
		return runJobsStats(context.Background(), inv)
	case "sparkwing runs cancel":
		return runRunsCancel(context.Background(), inv)
	case "sparkwing runs prune":
		return runRunsPrune(context.Background(), inv)
	case "sparkwing runs retry":
		return runRunsRetry(context.Background(), inv)
	case "sparkwing runners list":
		return runRunnersList(context.Background(), inv)
	case "sparkwing __tick":
		return runScheduleTick(inv)
	case "sparkwing __host-trigger":
		return runTriggerHost(inv)
	case "sparkwing wingd run":
		return runWingdRun(inv)
	case "sparkwing daemon status":
		return runDaemonLifecycle(inv)
	case "sparkwing daemon restart":
		return runDaemonLifecycle(inv)
	case "sparkwing daemon stop":
		return runDaemonLifecycle(inv)
	case "sparkwing daemon events":
		return runDaemonEvents(inv)
	case "sparkwing debug resume":
		return runDebug(inv)
	case "sparkwing docs list":
		return runDocsList(inv)
	case "sparkwing docs read":
		return runDocsRead(inv)
	case "sparkwing docs search":
		return runDocsSearch(inv)
	case "sparkwing docs migrations":
		return runDocsMigrations(inv)
	case "sparkwing commands":
		return runCommands(inv)
	case "sparkwing info":
		return runInfo(inv)
	case "sparkwing version":
		return runVersion(inv)
	case "sparkwing version hold":
		return runVersionHold(inv)
	case "sparkwing pipeline new":
		return runPipelineNew(inv)
	case "sparkwing pipeline hooks install":
		return runHooksInstall(inv)
	case "sparkwing pipeline hooks uninstall":
		return runHooksUninstall(inv)
	case "sparkwing pipeline hooks status":
		return runHooksStatus(inv)
	}
	return &clisurface.UsageError{Path: inv.Command.Path, Message: "command execution is unavailable"}
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

func statusExitCode(status string) error {
	if status == "success" {
		return nil
	}
	return exitErrorf(1, "run status: %s", status)
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
			return "", &apierr.Error{Code: apierr.NotFound, Message: fmt.Sprintf("no .sparkwing/main.go or TypeScript .sparkwing/package.json found from %s up", abs), Hint: "Run sparkwing pipeline new --pipeline NAME to create one, or select a checkout with -C."}
		}
		dir = parent
	}
}
