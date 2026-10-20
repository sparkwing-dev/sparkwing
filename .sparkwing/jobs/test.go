package jobs

import (
	"context"
	"strings"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type Test struct{ sparkwing.Base }

type TestArgs struct {
	Packages string `flag:"packages" desc:"Space-separated package patterns to test instead of the whole module (./...)."`
}

// safety: a focused run declares its share of the machine so the admission
// daemon queues parallel test runs instead of letting them exhaust memory.
const (
	focusedTestCores    = 4
	focusedTestMemoryGB = 10
)

func (Test) ShortHelp() string { return "Run the Go test suite (go test ./...)" }

func (Test) Help() string {
	return "Runs the Go test suite for the public sparkwing module (`go test ./...`)."
}

func (Test) Examples() []sparkwing.Example {
	return []sparkwing.Example{
		{Comment: "Run the full test suite", Command: "sparkwing run test"},
		{Comment: "Run only some packages", Command: "sparkwing run test --packages './internal/nodehost/ ./internal/store/'"},
	}
}

func (p *Test) Plan(_ context.Context, plan *sparkwing.Plan, in TestArgs, rc sparkwing.RunContext) error {
	pkgs := strings.Fields(in.Packages)
	if len(pkgs) == 0 {
		sparkwing.Job(plan, rc.Pipeline, p.run(testGoCommand(currentHost())))
		return nil
	}
	plan.Resources(sparkwing.Cores(focusedTestCores), sparkwing.MemoryGB(focusedTestMemoryGB))
	cmd := goCommandAt(focusedTestCores, "test", "-count=1 -timeout "+productGoTestTimeout+" "+strings.Join(pkgs, " "))
	sparkwing.Job(plan, rc.Pipeline, p.run(cmd))
	return nil
}

func (p *Test) run(cmd string) func(context.Context) error {
	return func(ctx context.Context) error {
		return withProductTestHome(func(home string) error {
			if _, err := sparkwing.Bash(ctx, productTestScript(cmd, home)).Run(); err != nil {
				return err
			}
			sparkwing.Info(ctx, "go test: all packages passed")
			return nil
		})
	}
}

const productGoTestTimeout = "25m"

func testGoCommand(h hostShape) string {
	if h.cpus == overlappedGoSuiteCPUs && h.singleTenant {
		return goCommandWithLimits(1, 2, "test", "-timeout "+productGoTestTimeout+" ./...")
	}
	return boundedGoCommand(h, "test", "-timeout "+productGoTestTimeout+" ./...")
}

func init() {
	sparkwing.Register("Test", func() sparkwing.Pipeline[TestArgs] { return &Test{} })
}
