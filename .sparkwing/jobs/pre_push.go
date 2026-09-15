package jobs

import (
	"context"
	"fmt"
	"runtime"
	"strings"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// PrePush is the fast tier at the push boundary: the source-policy steps the
// commit tier scopes to one commit, judged here over the whole push, the
// contract gates a single commit cannot answer, and a compile, a vet and a
// fast lint of the packages the push touches. Everything that races, lints or
// tests a whole module is Gate, which runs on demand and in hosted CI.
type PrePush struct{ sparkwing.Base }

func (PrePush) ShortHelp() string {
	return "Fast gate for the push boundary: scoped source policy, contract gates, and a compile, vet and fast lint of the touched packages"
}

func (PrePush) Help() string {
	return "Judges the push against the checks that answer in seconds: gofmt and the configured formatters " +
		"(gofumpt + goimports) over the changed Go files, no disallowed comments (only GoDoc on exported APIs " +
		"and // hack:/safety:/bug:/perf: tags), no test that sleeps or waits on the wall clock over that " +
		"same scope, an embedded pkg/docs/ mirror that matches docs/ and " +
		"CHANGELOG.md, a CHANGELOG.md entry for every covered surface the push changes " +
		"(bin/check-changelog.sh), api/openapi.yaml agreeing with the controller's route table " +
		"(bin/check-api-spec.sh), the public API surface matching the .apidiff/ snapshot " +
		"(bin/check-api-snapshot.sh), no product file that resolves the sparkwing home itself instead of " +
		"through internal/paths.DefaultPaths, `go build` and `go vet` over the packages holding the " +
		"changed Go files, and the fast linter subset over those same packages. The tier is budgeted at " +
		"ten seconds and fails when its steps overrun it, naming the slowest. Every scoped step reads the commits being pushed, the range " +
		"origin/main..HEAD, and never the index, so whatever is staged cannot narrow what the push is " +
		"judged against. go vet, the full test suite, golangci-lint, the race gate, " +
		"the Postgres suite and the dashboard suites run in `gate`, which hosted CI runs on every pull " +
		"request and every push to main."
}

func (PrePush) Examples() []sparkwing.Example {
	return []sparkwing.Example{
		{Comment: "Judge the push the way the hook does", Command: "sparkwing run pre-push"},
	}
}

func (p *PrePush) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	plan.Resources(sparkwing.Cores(float64(prePushCores(runtime.NumCPU()))))
	plan.Priority(hookTierPriority)
	sparkwing.Job(plan, rc.Pipeline, p)
	return nil
}

// Work declares the fast push-boundary steps.
//
// perf: they run in parallel rather than cheapest-first, so the tier costs
// what its slowest member costs and a chain would only make the verdict later.
// FailFast still stops the first failure, and the budget step fails the tier
// when the class overruns its ten seconds.
func (p *PrePush) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	w.ParallelFailures(sparkwing.FailFast)
	budget := newTierBudget("pre-push", prePushBudget)
	budget.step(w, "gofmt", runGofmtOverThePush)
	budget.step(w, "formatters", runFormattersOverThePush)
	budget.step(w, "comments", checkCommentsOverThePush)
	budget.step(w, "test-sleeps", checkTestSleepsOverThePush)
	budget.step(w, "docs-mirror", checkDocsMirror)
	budget.step(w, "home-resolution", checkHomeResolution)
	budget.step(w, "changelog", checkChangelogRequired)
	budget.step(w, "api-spec", checkAPISpec)
	budget.step(w, "api-snapshot", checkAPISnapshot)
	budget.step(w, "build-touched", runBuildTouched)
	budget.step(w, "vet-touched", runVetTouched)
	budget.step(w, "lint-touched", runFastLintTouched)
	budget.verdict(w)
	return nil, nil
}

func runGofmtOverThePush(ctx context.Context) error {
	return gofmtOverScope(ctx, pushRangeScope)
}

func runFormattersOverThePush(ctx context.Context) error {
	return formattersOverScope(ctx, pushRangeScope)
}

func checkCommentsOverThePush(ctx context.Context) error {
	return runScopedChecker(ctx, "comments", pushRangeCommentCommand)
}

func checkTestSleepsOverThePush(ctx context.Context) error {
	return runScopedChecker(ctx, "test-sleeps", pushRangeSleepCommand)
}

func pushRangeCommentCommand(ctx context.Context) (command, scope string, err error) {
	return pushRangeCheckerCommand(ctx, "commentcheck", "Go file", existingGoFiles)
}

func pushRangeSleepCommand(ctx context.Context) (command, scope string, err error) {
	return pushRangeCheckerCommand(ctx, "sleepcheck", "test file", existingGoTestFiles)
}

func checkChangelogRequired(ctx context.Context) error {
	if _, err := sparkwing.Bash(ctx, "bash bin/check-changelog.sh").Run(); err != nil {
		return fmt.Errorf("a covered surface changed without a CHANGELOG.md entry under [Unreleased]; add one in the category docs/changelog-style.md names: %w", err)
	}
	return nil
}

func checkAPISpec(ctx context.Context) error {
	if _, err := sparkwing.Bash(ctx, "bash bin/check-api-spec.sh").Run(); err != nil {
		return fmt.Errorf("api/openapi.yaml disagrees with the controller's route table; run `bash bin/gen-api-docs.sh`: %w", err)
	}
	return nil
}

func checkAPISnapshot(ctx context.Context) error {
	if _, err := sparkwing.Bash(ctx, "bash bin/check-api-snapshot.sh").Run(); err != nil {
		return fmt.Errorf("the public API surface drifted from .apidiff/; record the change in CHANGELOG.md, then run `bash bin/regen-api-snapshot.sh` and stage .apidiff/: %w", err)
	}
	return nil
}

// perf: the compiles are the one part of this tier that fans out, so they are
// bounded to what the tier reserves. A burst past the reservation is load the
// admission daemon cannot schedule against, and it measured an order of
// magnitude over the pin when the bound was the whole machine.
const prePushCoreCap = 3

// prePushCores holds the tier to what the gate reserves on this machine. The cap
// is what the compiles need; a machine whose gate reserves less than that cannot
// seat the tier beside a running gate, so the smaller figure wins.
func prePushCores(cpuCount int) int {
	if reserved := int(gateCoreReservation(cpuCount)); reserved < prePushCoreCap {
		return reserved
	}
	return prePushCoreCap
}

func runBuildTouched(ctx context.Context) error {
	return goOverTouchedPackages(ctx, "build", "buildable Go file(s)", buildableGoFiles)
}

// safety: go vet type-checks the test files go build never reads, so a test
// that does not compile fails here rather than in the first suite that runs.
func runVetTouched(ctx context.Context) error {
	return goOverTouchedPackages(ctx, "vet", "Go file(s)", existingGoFiles)
}

// perf: the fast linter subset over the packages the push touches. The full
// set measured ninety seconds over both modules, which is the release cut's
// price to pay, not the push boundary's.
func runFastLintTouched(ctx context.Context) error {
	return overTouchedPackages(ctx, "lint-touched", "lint", "Go file(s)", existingGoFiles,
		func(_ string, pkgs []string) string {
			return fastLintCommand(prePushCores(runtime.NumCPU()), pkgs)
		})
}

func goOverTouchedPackages(ctx context.Context, verb, noun string, keep func([]string) []string) error {
	return overTouchedPackages(ctx, verb+"-touched", "compile", noun, keep,
		func(_ string, pkgs []string) string {
			args := strings.Join(pkgs, " ")
			// safety: go build discards its output for several packages but writes
			// a lone package's binary beside the module, where ./cmd/sparkwing
			// collides with the sparkwing/ SDK directory and refuses to build.
			if verb == "build" && len(pkgs) == 1 {
				args = "-o /dev/null " + args
			}
			return goCommandAt(prePushCores(runtime.NumCPU()), verb, args)
		})
}

func overTouchedPackages(ctx context.Context, step, verb, noun string, keep func([]string) []string, command func(module string, pkgs []string) string) error {
	files, scope, err := pushRangeScope(ctx, noun, keep)
	if err != nil {
		return err
	}
	modules, err := committedModuleDirs(ctx)
	if err != nil {
		return err
	}
	targets := touchedPackageTargets(files, modules)
	sparkwing.Info(ctx, "%s: %s", step, scope)
	if len(targets) == 0 {
		sparkwing.Info(ctx, "%s: no Go package changed; nothing to %s", step, verb)
		return nil
	}
	if count := countTargets(targets); count > prePushPackageCap {
		sparkwing.Info(ctx, "%s: %d packages changed, past the %d this tier fits; the whole-tree form runs in gate",
			step, count, prePushPackageCap)
		return nil
	}

	var failures []string
	for _, module := range mapKeys(targets) {
		pkgs := targets[module]
		sparkwing.Info(ctx, "%s: %s: %s", step, module, strings.Join(pkgs, " "))
		if _, runErr := sparkwing.Bash(ctx, command(module, pkgs)).Dir(module).Run(); runErr != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", module, runErr))
		}
	}
	if len(failures) == 0 {
		return nil
	}
	return fmt.Errorf("%s failed in %d module(s):\n  - %s",
		step, len(failures), strings.Join(failures, "\n  - "))
}

// safety: go build reads no test file, and a directory holding only tests has
// nothing for it to compile, so both drop out here rather than failing the
// step with "no non-test Go files".
func buildableGoFiles(all []string) []string {
	out := make([]string, 0, len(all))
	for _, f := range existingGoFiles(all) {
		if strings.HasSuffix(f, "_test.go") || strings.HasPrefix(f, "vendor/") || strings.Contains(f, "/vendor/") {
			continue
		}
		out = append(out, f)
	}
	return out
}

// perf: a push wide enough to change more packages than this cannot be
// compiled, linted and tested inside the tier's ten seconds, so it names the
// count and leaves the whole-tree forms to the broad tier rather than holding
// the push for minutes.
const prePushPackageCap = 8

func countTargets(targets map[string][]string) int {
	count := 0
	for _, pkgs := range targets {
		count += len(pkgs)
	}
	return count
}

func init() {
	sparkwing.Register("pre-push", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &PrePush{} })
}
