package jobs

import (
	"context"
	"fmt"
	"os/exec"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// safety: these packages hold the process, socket and SDK command code whose behavior differs on macOS.
var macosVerifyPackages = []string{"./sparkwing", "./internal/pipelineentry", "./internal/wingd/client", "./internal/orchestrator"}

const macosVerifyTimeout = 30 * time.Minute

// MacOSVerify checks native macOS builds, process and socket handling, and installers.
type MacOSVerify struct{ sparkwing.Base }

func (MacOSVerify) ShortHelp() string {
	return "Verify native macOS execution and installers"
}

func (MacOSVerify) Help() string {
	return "Builds every command for macOS, runs the SDK, pipeline entry, daemon client and orchestrator Go tests " +
		"with the race detector, then bin/install-test.sh and bin/release-install-test.sh. " +
		"Requires macOS with Go, a C compiler, Bash, Node and npm on PATH. " +
		"Reserves four cores and runs the checks sequentially. Every check reports its result even when an earlier check fails."
}

func (MacOSVerify) Examples() []sparkwing.Example {
	return []sparkwing.Example{{Comment: "Check native macOS support", Command: "sparkwing run macos-verify"}}
}

func (p *MacOSVerify) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	sparkwing.Job(plan, rc.Pipeline, p).Requires("os=darwin").Timeout(macosVerifyTimeout).Cores(4)
	return nil
}

func (p *MacOSVerify) Work(work *sparkwing.Work) (*sparkwing.WorkStep, error) {
	addPlatformChecks(work, []platformCheck{
		{id: "build", run: func(ctx context.Context) error {
			return runPlatformIsolated(ctx, shellQuoteAll([]string{"go", "build", "-o", "/dev/null", "./cmd/..."}))
		}},
		{id: "runtime-race", run: runMacOSVerifyRuntime},
		{id: "source-installer", run: func(ctx context.Context) error { return runPlatformInstaller(ctx, "bin/install-test.sh") }},
		{id: "release-installer", run: func(ctx context.Context) error { return runPlatformInstaller(ctx, "bin/release-install-test.sh") }},
	})
	return nil, nil
}

func runMacOSVerifyRuntime(ctx context.Context) error {
	for _, tool := range []string{"go", "bash", "node", "npm"} {
		if _, err := exec.LookPath(tool); err != nil {
			return fmt.Errorf("macos-verify requires %s on PATH: %w", tool, err)
		}
	}
	command := append([]string{"go", "test", "-race", "-p", "2", "-timeout", "10m", "-count=1"}, macosVerifyPackages...)
	return runPlatformIsolated(ctx, withPinned(shellQuoteAll(command), []string{"CGO_ENABLED=1"}))
}

func init() {
	sparkwing.Register("macos-verify", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &MacOSVerify{} })
}
