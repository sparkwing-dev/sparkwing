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
		"through internal/paths.DefaultPaths, and `go build` over the packages holding the changed Go " +
		"files. Each scoped step reads the staged change, or the change since origin/main when nothing is " +
		"staged, and names the mode it ran in. go vet, the full test suite, golangci-lint, the race gate, " +
		"the Postgres suite and the dashboard suites run in `gate`, which hosted CI runs on every pull " +
		"request and every push to main."
}

func (PrePush) Examples() []sparkwing.Example {
	return []sparkwing.Example{
		{Comment: "Judge the push the way the hook does", Command: "sparkwing run pre-push"},
	}
}

func (p *PrePush) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	// perf: the compile of the touched packages is the one step that fans out;
	// it measured 3.3 cores over a second on a 16-core Linux host, and two is
	// the largest pin that still fits beside a running gate.
	plan.Resources(sparkwing.Cores(2))
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
	sparkwing.Step(w, "gofmt", runGofmtOnTheChange)
	sparkwing.Step(w, "formatters", runFormatters)
	sparkwing.Step(w, "comments", checkComments)
	sparkwing.Step(w, "test-sleeps", checkTestSleeps)
	sparkwing.Step(w, "docs-mirror", checkDocsMirror)
	sparkwing.Step(w, "home-resolution", checkHomeResolution)
	sparkwing.Step(w, "changelog", checkChangelogRequired)
	sparkwing.Step(w, "api-spec", checkAPISpec)
	sparkwing.Step(w, "api-snapshot", checkAPISnapshot)
	sparkwing.Step(w, "build-touched", runBuildTouched)
	return nil, nil
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

// perf: eight packages compiled in about a second on a 16-core Linux host,
// which is the share of the push budget this step gets. A wider change
// compiles in `gate` and in hosted CI, both of which build every module.
const touchedBuildPackageCap = 8

func runBuildTouched(ctx context.Context) error {
	files, scope, err := changeScope(ctx, "buildable Go file(s)", buildableGoFiles)
	if err != nil {
		return err
	}
	modules, err := committedModuleDirs(ctx)
	if err != nil {
		return err
	}
	targets := touchedPackageTargets(files, modules)
	sparkwing.Info(ctx, "build-touched: %s", scope)

	count := 0
	for _, pkgs := range targets {
		count += len(pkgs)
	}
	switch {
	case count == 0:
		sparkwing.Info(ctx, "build-touched: no Go package changed; nothing to compile")
		return nil
	case count > touchedBuildPackageCap:
		sparkwing.Info(ctx, "build-touched: %d packages changed, above the %d this tier compiles; `sparkwing run gate` and hosted CI build every module",
			count, touchedBuildPackageCap)
		return nil
	}

	var failures []string
	for _, module := range mapKeys(targets) {
		pkgs := targets[module]
		sparkwing.Info(ctx, "build-touched: %s: %s", module, strings.Join(pkgs, " "))
		cmd := boundedGoCommand(runtime.NumCPU(), "build", strings.Join(pkgs, " "))
		if _, runErr := sparkwing.Bash(ctx, cmd).Dir(module).Run(); runErr != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", module, runErr))
		}
	}
	if len(failures) == 0 {
		return nil
	}
	return fmt.Errorf("go build failed in %d module(s):\n  - %s",
		len(failures), strings.Join(failures, "\n  - "))
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
