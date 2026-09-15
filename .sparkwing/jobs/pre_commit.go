package jobs

import (
	"context"
	"runtime"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// PreCommit is the source-policy tier: everything a commit can be judged
// against without compiling, linking, or asking the network. The fast tier the
// pre-push hook runs is PrePush; the broad tier that vets, builds, tests and
// lints the whole tree is Gate, which runs on demand and in hosted CI.
type PreCommit struct{ sparkwing.Base }

func (PreCommit) ShortHelp() string {
	return "Fast source-policy gate for the commit boundary: formatting, sweeps, and mirrors"
}

func (PreCommit) Help() string {
	return "Judges the change against this repository's source policy and nothing else. Each scoped step " +
		"reads the staged change, or the change since origin/main when nothing is staged, and names the " +
		"mode it ran in: " +
		"gofmt and the configured formatters (gofumpt + goimports) over the Go files, " +
		"no em dashes and no internal tracker IDs, no disallowed comments (only GoDoc on " +
		"exported APIs and // hack:/safety:/bug:/perf: tags), which in range mode also reads " +
		"every untracked Go file, no test that sleeps or reads the wall clock as a wait, and repo-wide, no tracked " +
		"ELF, Mach-O or PE executable, an embedded pkg/docs/ mirror that matches docs/ and CHANGELOG.md, " +
		"live links in released changelog entries, and no product file that resolves the sparkwing home " +
		"itself instead of through internal/paths.DefaultPaths. Every step reads files; none compiles, " +
		"links, or reaches the network, which is what keeps the git pre-commit hook under ten seconds. " +
		"The push boundary adds the contract gates and a compile of the touched packages in `pre-push`; " +
		"go vet, go build, go test, golangci-lint, the race gate and the dashboard suites run in `gate`, " +
		"on demand and in hosted CI. Set SPARKWING_REGEX_SWEEP_ALL=1 to sweep the whole tree for em dashes " +
		"and tracker IDs. The tier is budgeted at three seconds and fails when its steps overrun it, " +
		"naming the slowest."
}

func (PreCommit) Examples() []sparkwing.Example {
	return []sparkwing.Example{
		{Comment: "Judge the staged change", Command: "sparkwing run pre-commit"},
	}
}

// perf: admits the two hook tiers ahead of the queued multi-minute gates
// sharing this machine. A tier a human waits on cannot keep its promise from
// behind one in FIFO.
const hookTierPriority = 10

func (p *PreCommit) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	// perf: the formatters split their file list one process per reserved
	// core, and the runs store measured this tier at 3.55 cores sustained
	// against its old one-core pin, so the pin is the fan-out width.
	plan.Resources(sparkwing.Cores(float64(formatterWorkers(runtime.NumCPU()))))
	plan.Priority(hookTierPriority)
	sparkwing.Job(plan, rc.Pipeline, p)
	return nil
}

// Work declares the source-policy steps.
//
// perf: they run in parallel rather than cheapest-first. Ordering a chain buys
// an early failure when the steps are minutes apart; the slowest step here
// measured 0.9 s, so the tier costs what its slowest member costs and ordering
// would only make the verdict later. FailFast still stops the first failure.
func (p *PreCommit) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	w.ParallelFailures(sparkwing.FailFast)
	budget := newTierBudget("pre-commit", preCommitBudget).over(changeScope)
	budget.step(w, "gofmt", runGofmtOnTheChange)
	budget.step(w, "formatters", runFormatters)
	budget.step(w, "tracker-ids", checkTrackerIDs)
	budget.step(w, "em-dashes", checkEmDashes)
	budget.step(w, "comments", checkComments)
	budget.step(w, "test-sleeps", checkTestSleeps)
	budget.step(w, "tracked-binaries", checkTrackedBinaries)
	budget.step(w, "docs-mirror", checkDocsMirror)
	budget.step(w, "changelog-links", checkChangelogLinks)
	budget.step(w, "home-resolution", checkHomeResolution)
	budget.verdict(w)
	return nil, nil
}

// perf: the broad tier reads every tracked Go file, which measured 1.3 s under
// the parallelism of this tier; the staged scope measured 40 ms. gofmt is a
// subset of what the formatters step enforces and runs beside it because it
// answers first and names the file without loading a package graph.
func runGofmtOnTheChange(ctx context.Context) error {
	return gofmtOverScope(ctx, changeScope)
}

func gofmtOverScope(ctx context.Context, scopeOf scopeFunc) error {
	files, scope, err := scopeOf(ctx, "Go file(s)", existingGoFiles)
	if err != nil {
		return err
	}
	sparkwing.Info(ctx, "gofmt: %s", scope)
	if len(files) == 0 {
		return nil
	}
	return sparkwing.Bash(ctx, "gofmt -l -- "+shellQuoteAll(files)).MustBeEmpty("files need formatting")
}

func init() {
	sparkwing.Register("pre-commit", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &PreCommit{} })
}
