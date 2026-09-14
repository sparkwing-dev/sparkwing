package jobs

import (
	"context"
	"fmt"
	"strings"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// PrePush is the fast tier at the push boundary: the source-policy steps the
// commit tier scopes to one commit, judged here over the whole push, the
// contract gates a single commit cannot answer, and a compile of the packages
// the push touches. Everything that vets, lints, races or tests a whole module
// is Gate, which runs on demand and in hosted CI.
type PrePush struct{ sparkwing.Base }

func (PrePush) ShortHelp() string {
	return "Fast gate for the push boundary: scoped source policy, contract gates, and a compile of the touched packages"
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
		"through internal/paths.DefaultPaths, and `go build` and `go vet` over the packages holding the " +
		"changed Go files. Every scoped step reads the commits being pushed, the range " +
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
	plan.Resources(sparkwing.Cores(prePushCores))
	plan.Priority(hookTierPriority)
	sparkwing.Job(plan, rc.Pipeline, p)
	return nil
}

// Work declares the fast push-boundary steps.
//
// perf: they run in parallel rather than cheapest-first. The slowest measured
// a second, so the tier costs what its slowest member costs and a chain would
// only make the verdict later. FailFast still stops the first failure.
func (p *PrePush) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	w.ParallelFailures(sparkwing.FailFast)
	sparkwing.Step(w, "gofmt", runGofmtOverThePush)
	sparkwing.Step(w, "formatters", runFormattersOverThePush)
	sparkwing.Step(w, "comments", checkCommentsOverThePush)
	sparkwing.Step(w, "test-sleeps", checkTestSleepsOverThePush)
	sparkwing.Step(w, "docs-mirror", checkDocsMirror)
	sparkwing.Step(w, "home-resolution", checkHomeResolution)
	sparkwing.Step(w, "changelog", checkChangelogRequired)
	sparkwing.Step(w, "api-spec", checkAPISpec)
	sparkwing.Step(w, "api-snapshot", checkAPISnapshot)
	sparkwing.Step(w, "build-touched", runBuildTouched)
	sparkwing.Step(w, "vet-touched", runVetTouched)
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
const prePushCores = 3

func runBuildTouched(ctx context.Context) error {
	return goOverTouchedPackages(ctx, "build", "buildable Go file(s)", buildableGoFiles)
}

// safety: go vet type-checks the test files go build never reads, so a test
// that does not compile fails here rather than in the first suite that runs.
func runVetTouched(ctx context.Context) error {
	return goOverTouchedPackages(ctx, "vet", "Go file(s)", existingGoFiles)
}

func goOverTouchedPackages(ctx context.Context, verb, noun string, keep func([]string) []string) error {
	step := verb + "-touched"
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
		sparkwing.Info(ctx, "%s: no Go package changed; nothing to compile", step)
		return nil
	}

	var failures []string
	for _, module := range mapKeys(targets) {
		pkgs := targets[module]
		sparkwing.Info(ctx, "%s: %s: %s", step, module, strings.Join(pkgs, " "))
		cmd := goCommandAt(prePushCores, verb, strings.Join(pkgs, " "))
		if _, runErr := sparkwing.Bash(ctx, cmd).Dir(module).Run(); runErr != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", module, runErr))
		}
	}
	if len(failures) == 0 {
		return nil
	}
	return fmt.Errorf("go %s failed in %d module(s):\n  - %s",
		verb, len(failures), strings.Join(failures, "\n  - "))
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

func init() {
	sparkwing.Register("pre-push", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &PrePush{} })
}
