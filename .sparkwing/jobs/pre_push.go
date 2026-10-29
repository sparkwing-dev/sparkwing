package jobs

import (
	"context"
	"errors"
	"fmt"
	"go/build"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// PrePush is the fast tier at the push boundary, and it repeats nothing the
// commit tier already ran: the contract gates a single commit cannot answer,
// and a compile, a vet and a fast lint of the packages the whole push touches.
// Everything that races, lints or tests a whole module is Gate, which runs on
// demand and in hosted CI.
type PrePush struct{ sparkwing.Base }

func (PrePush) ShortHelp() string {
	return "Fast gate for the push boundary: contract gates, and a compile, vet and fast lint of the touched packages"
}

func (PrePush) Help() string {
	return "Judges the push against what the whole change since origin/main can answer and one commit cannot: " +
		"a CHANGELOG.md entry for every covered surface the push changes (bin/check-changelog.sh), " +
		"api/openapi.yaml agreeing with the controller's route table (bin/check-api-spec.sh), the public API " +
		"surface matching the .apidiff/ snapshot (bin/check-api-snapshot.sh), and `go build`, `go vet` and the " +
		"fast linter subset over every package holding changed Go files, plus the depguard architecture guards " +
		"over the whole tree against their .guards/ allow-lists. It repeats " +
		"nothing `pre-commit` already ran per file: the formatters, the comment, sleep and regex sweeps, the " +
		"docs mirror and home resolution are the commit tier's, and a push whose commits skipped that hook is " +
		"judged by `gate`. Every step reads the commits being pushed, the range origin/main..HEAD, and never " +
		"the index, so whatever is staged cannot narrow what the push is judged against. The tier has a " +
		"one-minute hard limit and fails when its steps overrun it, naming the slowest. Run timings measure " +
		"progress toward its ten-second warm-cache target; the run has no compiler-cache temperature signal. The full test suite, " +
		"golangci-lint over every module, the race gate, the Postgres suite and the dashboard suites run in " +
		"`gate`, which hosted CI runs on every pull request and every push to main."
}

func (PrePush) Examples() []sparkwing.Example {
	return []sparkwing.Example{
		{Comment: "Judge the push the way the hook does", Command: "sparkwing run pre-push"},
	}
}

func (p *PrePush) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	plan.Priority(hookTierPriority)
	sparkwing.Job(plan, rc.Pipeline, p).Cache(lintCache())
	return nil
}

// Work declares the fast push-boundary steps.
//
// perf: they run in parallel rather than cheapest-first, so the tier costs
// what its slowest member costs and a chain would only make the verdict later.
// FailFast still stops the first failure. The budget step enforces the one-minute
// hard limit; run timings measure the separate ten-second warm-cache target.
func (p *PrePush) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	w.FailurePolicy(sparkwing.FailFast)
	budget := newTierBudget("pre-push", prePushBudget).over(pushRangeScope)
	budget.step(w, "changelog", checkChangelogRequired)
	budget.step(w, "api-spec", checkAPISpec)
	budget.step(w, "api-snapshot", checkAPISnapshot)
	budget.step(w, "build-touched", runBuildTouched)
	budget.step(w, "vet-touched", runVetTouched)
	budget.step(w, "lint-touched", runFastLintTouched)
	budget.step(w, "guards", checkGuardsFast)
	budget.verdict(w)
	return nil, nil
}

func checkChangelogRequired(ctx context.Context) error {
	if _, err := sparkwing.Bash(ctx, "bash bin/check-changelog.sh").Run(); err != nil {
		return fmt.Errorf("a covered surface changed without a CHANGELOG.md entry under [Unreleased]; add one in the category CHANGELOG-STYLE.md names: %w", err)
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

// perf: build, vet and lint compile concurrently. Each task stays small, while
// the plan reserves their combined demand so admission can account for the
// work before it starts.
const prePushCoreCap = 3

const prePushCompilerTasks = 3

func prePushReservationCores(cpuCount int) int {
	if cpuCount < 1 {
		return 1
	}
	if cap := prePushCompilerTasks * prePushCoreCap; cpuCount > cap {
		return cap
	}
	return cpuCount
}

func prePushTaskCores(cpuCount int) int {
	cores := prePushReservationCores(cpuCount) / prePushCompilerTasks
	if cores < 1 {
		return 1
	}
	return cores
}

func runBuildTouched(ctx context.Context) error {
	return goOverTouchedPackages(ctx, "build", "buildable Go file(s)", buildableGoFiles)
}

// safety: go vet type-checks the test files go build never reads, so a test
// that does not compile fails here rather than in the first suite that runs.
// It also vets the qatrust flavor, which no default build compiles.
func runVetTouched(ctx context.Context) error {
	return overTouchedPackages(ctx, "vet-touched", "compile", "Go file(s)", existingGoFiles,
		func(module string, pkgs []string) string {
			cores := prePushTaskCores(runtime.NumCPU())
			qa := goCommandAt(cores, "vet", "-tags qatrust "+strings.Join(pkgs, " "))
			if def := defaultBuildPackages(module, pkgs, true); len(def) > 0 {
				return goCommandAt(cores, "vet", strings.Join(def, " ")) + " && " + qa
			}
			return qa
		})
}

// perf: the fast linter subset over the packages the push touches. The full
// set measured ninety seconds over both modules, which is the release cut's
// price to pay, not the push boundary's.
func runFastLintTouched(ctx context.Context) error {
	return overTouchedPackages(ctx, "lint-touched", "lint", "Go file(s)", existingGoFiles,
		func(module string, pkgs []string) string {
			pkgs = defaultBuildPackages(module, pkgs, true)
			if len(pkgs) == 0 {
				return "true"
			}
			return fastLintCommand(prePushTaskCores(runtime.NumCPU()), pkgs)
		})
}

func goOverTouchedPackages(ctx context.Context, verb, noun string, keep func([]string) []string) error {
	return overTouchedPackages(ctx, verb+"-touched", "compile", noun, keep,
		func(module string, pkgs []string) string {
			pkgs = defaultBuildPackages(module, pkgs, false)
			if len(pkgs) == 0 {
				return "true"
			}
			args := strings.Join(pkgs, " ")
			// safety: go build discards its output for several packages but writes
			// a lone package's binary beside the module, where ./cmd/sparkwing
			// collides with the sparkwing/ SDK directory and refuses to build.
			if verb == "build" && len(pkgs) == 1 {
				args = "-o /dev/null " + args
			}
			return goCommandAt(prePushTaskCores(runtime.NumCPU()), verb, args)
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

// safety: go build, go vet and golangci-lint refuse a package whose every file
// the default build excludes, such as the qatrust-only bin/qaindex, so the
// default pass drops it; vet's qatrust pass still judges it.
func defaultBuildPackages(module string, pkgs []string, withTests bool) []string {
	out := make([]string, 0, len(pkgs))
	for _, pkg := range pkgs {
		p, err := build.Default.ImportDir(filepath.Join(sparkwing.WorkDir(), module, pkg), 0)
		var noGo *build.NoGoError
		switch {
		case errors.As(err, &noGo):
		case err != nil, len(p.GoFiles)+len(p.CgoFiles) > 0,
			withTests && len(p.TestGoFiles)+len(p.XTestGoFiles) > 0:
			out = append(out, pkg)
		}
	}
	return out
}

func init() {
	sparkwing.Register("PrePush", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &PrePush{} })
}
