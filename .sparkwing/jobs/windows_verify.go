package jobs

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

const windowsVerifyRuntimeTests = `^Test(Sh_|Cmd_|Bash_|Exec_|ExecError_|WindowsShellLauncher|WindowsHostCommand|WindowsCapturedEnvironment|LookupCommandWindows|SDKCommandWindows|NPMCommand|PublishRunHandle|ProbeReportsNoDaemonWhenSocketDirectoryIsAbsentOnWindows|WindowsNetDown|BuildEnv|OverlayFloor|LocalBlockedSources|ExplainDependencyMismatch|InvalidModule|CanceledProbe|FailedProbeLeavesEnvUnchanged|ExplicitSessionEnvironment|ResolveAndWrite|ResolverWithDir|CompilePipelineOverridesFixedPin|CompilePipelineExplainsLocalDependencyFloor|ResolveSparksToolchainFailure|PipelineScaffoldDoesNotRequireGoPatch|DiscoveryRefusesTheDefaultStateDirWhenTheHomeIsRedirected|QueryReportsNoDaemonForFreshWindowsHome)`

const windowsVerifyTimeout = 20 * time.Minute

const windowsVerifyTemplateTests = `^TestTemplateVerify(CLIPath|Windows)`

// WindowsVerify checks native Windows process ownership, SDK commands, and installers.
type WindowsVerify struct{ sparkwing.Base }

func (WindowsVerify) ShortHelp() string {
	return "Verify native Windows execution and Git Bash installers"
}

func (WindowsVerify) Help() string {
	return "Runs the native Windows process-lifetime, SDK Bash/Exec, npm invocation, daemon discovery, and run-handle Go tests with the race detector, " +
		"checks Go toolchain fixtures, template executable paths, locking, and disk space, " +
		"then bin/install-test.sh and bin/release-install-test.sh through Git Bash. " +
		"Requires Windows, Go, a working native C compiler, Git Bash, Node, and npm on PATH. " +
		"Reserves four cores and runs the checks sequentially. Every check reports its result even when an earlier check fails."
}

func (WindowsVerify) Examples() []sparkwing.Example {
	return []sparkwing.Example{{Comment: "Check native Windows support", Command: "sparkwing run windows-verify"}}
}

func (p *WindowsVerify) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	sparkwing.Job(plan, rc.Pipeline, p).Requires("os=windows").Timeout(windowsVerifyTimeout).Cores(4)
	return nil
}

type platformCheck struct {
	id  string
	run func(context.Context) error
}

func (p *WindowsVerify) Work(work *sparkwing.Work) (*sparkwing.WorkStep, error) {
	addPlatformChecks(work, []platformCheck{
		{id: "runtime-race", run: runWindowsVerifyRuntime},
		{id: "template-platform", run: func(ctx context.Context) error {
			command := shellQuoteAll([]string{"go", "-C", ".sparkwing", "test", "./jobs", "-run", windowsVerifyTemplateTests, "-count=1", "-timeout", "1m"})
			return runPlatformIsolated(ctx, command)
		}},
		{id: "source-installer", run: func(ctx context.Context) error { return runPlatformInstaller(ctx, "bin/install-test.sh") }},
		{id: "release-installer", run: func(ctx context.Context) error { return runPlatformInstaller(ctx, "bin/release-install-test.sh") }},
	})
	return nil, nil
}

func addPlatformChecks(work *sparkwing.Work, checks []platformCheck) {
	var previous *sparkwing.WorkStep
	for _, check := range checks {
		step := sparkwing.Step(work, check.id, check.run).ContinueOnError()
		if previous != nil {
			step.Needs(previous)
		}
		previous = step
	}
}

func runWindowsVerifyRuntime(ctx context.Context) error {
	for _, tool := range []string{"go", "bash", "node", "npm"} {
		if _, err := exec.LookPath(tool); err != nil {
			return fmt.Errorf("windows-verify requires %s on PATH: %w", tool, err)
		}
	}
	command := shellQuoteAll([]string{
		"go", "test", "-race", "-p", "2", "-timeout", "5m", "-count=1", "-run", windowsVerifyRuntimeTests,
		"./internal/testshell", "./internal/gotoolchain", "./internal/sparks", "./internal/bincache", "./cmd/sparkwing",
		"./sparkwing", "./internal/nodehost", "./internal/procenv", "./internal/pipelineentry", "./internal/wingd/client", "./internal/orchestrator",
	})
	return runPlatformIsolated(ctx, withPinned(command, []string{"CGO_ENABLED=1", "GOOS=windows", "GOARCH=" + shellQuote(runtime.GOARCH)}))
}

func runPlatformInstaller(ctx context.Context, script string) error {
	return runPlatformIsolated(ctx, `"$BASH" `+shellQuote(script))
}

func runPlatformIsolated(ctx context.Context, command string) error {
	return withProductTestHome(func(home string) error {
		_, err := sparkwing.Bash(ctx, productTestScript(command, home)).Dir(sparkwing.WorkDir()).Env("GOWORK", "auto").Env("GOMAXPROCS", "2").Run()
		return err
	})
}

func init() {
	sparkwing.Register("windows-verify", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &WindowsVerify{} })
}
