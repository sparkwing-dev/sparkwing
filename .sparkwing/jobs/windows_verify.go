package jobs

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

const windowsVerifyRuntimeTests = `^Test(WindowsNode|Sh_|Cmd_|Bash_|Exec_|ExecWindows|ExecError_|NpmCache|DefaultNpmCache|ResolveNpmCache|PublishRunHandle|ProbeQueueReportsNoDaemonWhenSocketDirectoryIsAbsentOnWindows|WindowsNetDown|QueryReportsNoDaemonForFreshWindowsHome)`

const windowsVerifyTemplateTests = `^TestTemplateVerify(CLIPath|Windows)`

// WindowsVerify checks native Windows process ownership, SDK commands, and installers.
type WindowsVerify struct{ sparkwing.Base }

func (WindowsVerify) ShortHelp() string {
	return "Verify native Windows execution and Git Bash installers"
}

func (WindowsVerify) Help() string {
	return "Runs the native Windows process-lifetime, SDK Bash/Exec, npm-cache, daemon discovery, and run-handle Go tests with the race detector, " +
		"checks template executable paths, locking, and disk space, " +
		"then bin/install-test.sh and bin/release-install-test.sh through Git Bash. " +
		"Requires Windows, Go, a working native C compiler, Git Bash, Node, and npm on PATH. " +
		"Reserves four cores and runs the checks sequentially. Every check reports its result even when an earlier check fails."
}

func (WindowsVerify) Examples() []sparkwing.Example {
	return []sparkwing.Example{{Comment: "Check native Windows support", Command: "sparkwing run windows-verify"}}
}

func (p *WindowsVerify) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	return p.planForPlatform(plan, rc.Pipeline, runtime.GOOS)
}

func (p *WindowsVerify) planForPlatform(plan *sparkwing.Plan, pipeline, goos string) error {
	if goos != "windows" {
		return fmt.Errorf("windows-verify requires native Windows; host is %s", goos)
	}
	plan.Resources(sparkwing.Cores(4))
	sparkwing.Job(plan, pipeline, p).Timeout(20 * time.Minute)
	return nil
}

type windowsVerifyCheck struct {
	id  string
	run func(context.Context) error
}

func (p *WindowsVerify) Work(work *sparkwing.Work) (*sparkwing.WorkStep, error) {
	addWindowsVerifyChecks(work, []windowsVerifyCheck{
		{id: "runtime-race", run: runWindowsVerifyRuntime},
		{id: "template-platform", run: func(ctx context.Context) error {
			command := shellQuoteAll([]string{"go", "-C", ".sparkwing", "test", "./jobs", "-run", windowsVerifyTemplateTests, "-count=1", "-timeout", "1m"})
			return runWindowsVerifyIsolated(ctx, command)
		}},
		{id: "source-installer", run: func(ctx context.Context) error { return runWindowsVerifyInstaller(ctx, "bin/install-test.sh") }},
		{id: "release-installer", run: func(ctx context.Context) error { return runWindowsVerifyInstaller(ctx, "bin/release-install-test.sh") }},
	})
	return nil, nil
}

func addWindowsVerifyChecks(work *sparkwing.Work, checks []windowsVerifyCheck) {
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
		"./internal/runners/local", "./sparkwing", "./internal/wingd/client", "./internal/orchestrator",
	})
	return runWindowsVerifyIsolated(ctx, withPinned(command, []string{"CGO_ENABLED=1", "GOOS=windows", "GOARCH=" + shellQuote(runtime.GOARCH)}))
}

func runWindowsVerifyInstaller(ctx context.Context, script string) error {
	return runWindowsVerifyIsolated(ctx, `"$BASH" `+shellQuote(script))
}

func runWindowsVerifyIsolated(ctx context.Context, command string) error {
	return withProductTestHome(func(home string) error {
		_, err := sparkwing.Bash(ctx, productTestScript(command, home)).Dir(sparkwing.WorkDir()).Env("GOWORK", "off").Env("GOMAXPROCS", "2").Run()
		return err
	})
}

func init() {
	sparkwing.Register("windows-verify", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &WindowsVerify{} })
}
