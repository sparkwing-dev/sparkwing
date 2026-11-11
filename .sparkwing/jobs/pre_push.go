package jobs

import (
	"context"
	"errors"
	"fmt"
	"go/build"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/gitenv"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// PrePush runs fast push checks and adds affected Go tests for main updates.
type PrePush struct{ sparkwing.Base }

func (PrePush) ShortHelp() string {
	return "Push gate: fast checks, plus affected short and critical Go tests for main updates"
}

func (PrePush) Help() string {
	return "Judges the push against what the whole change since origin/main can answer and one commit cannot: " +
		"a CHANGELOG.md entry for every covered surface the push changes (bin/check-changelog.sh), " +
		"api/openapi.yaml agreeing with the controller's route table (bin/check-api-spec.sh), the public API " +
		"surface matching the .apidiff/ snapshot (bin/check-api-snapshot.sh), and `go build`, `go vet` and the " +
		"fast linter subset, including depguard dependency rules, over every package holding changed Go files. It repeats " +
		"nothing `pre-commit` already ran per file: the formatters, the comment, sleep and regex sweeps, the " +
		"docs mirror and home resolution are the commit tier's, and a push whose commits skipped that hook is " +
		"judged by `gate`. Every fast check reads the committed change from the merge base with origin/main to HEAD, and never " +
		"the index, so whatever is staged cannot narrow what the push is judged against. The fast checks have a " +
		"one-minute budget on each step's own run time; a step past it warns, naming the slowest, and the fast phase still passes when every check did. Run timings measure " +
		"progress toward their ten-second warm-cache target; the run has no compiler-cache temperature signal. " +
		"Affected tests use Git's advertised remote OID for main, never local tracking, including type changes and both paths of renames. Non-fast-forward main updates are refused; creating main runs every committed Go module's short tests and the full critical set. Main hook updates must push the checked-out HEAD; check out a different source commit before retrying. Deletions add no tests. For hook updates to refs/heads/main, or an explicit run outside the hook, the affected-tests job then runs the hosted gate's reverse dependency selection with go test -short, including test imports and file readers, plus every critical test without -short when Go inputs changed. Branch pushes keep only the fast checks. " +
		"It has a separate 45-minute execution deadline and reports package and test timings. Changelog-only changes select no tests; documentation with registered readers runs those readers. " +
		"Broader lint, race and dashboard checks remain in `gate`; full Go and PostgreSQL verification run with `gate -- --full` and at the release boundary. Hosted CI provides additional verification."
}

func (PrePush) Examples() []sparkwing.Example {
	return []sparkwing.Example{
		{Comment: "Judge the push the way the hook does", Command: "sparkwing run pre-push"},
	}
}

func (p *PrePush) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	plan.Priority(hookTierPriority)
	fast := sparkwing.Job(plan, rc.Pipeline, p)
	refs, hook := os.LookupEnv(gitenv.PushRefsVar)
	updates, err := mainPushUpdates(refs)
	if err != nil {
		return err
	}
	if !hook || len(updates) > 0 {
		affectedPushTestsJobNode(plan).Needs(fast)
	}
	return nil
}

type mainPushUpdate struct {
	local, remote string
}

func mainPushUpdates(refs string) ([]mainPushUpdate, error) {
	var updates []mainPushUpdate
	for line := range strings.SplitSeq(refs, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 4 {
			return nil, fmt.Errorf("pre-push: invalid Git ref update %q", line)
		}
		if fields[2] == "refs/heads/main" && strings.Trim(fields[1], "0") != "" {
			updates = append(updates, mainPushUpdate{local: fields[1], remote: fields[3]})
		}
	}
	return updates, nil
}

// safety: a plan error hides its remedy until secret discovery completes.
// The job judges Git's advertised destination, which local tracking may not match.
func mainPushScope(ctx context.Context) ([]string, string, error) {
	refs, hook := os.LookupEnv(gitenv.PushRefsVar)
	if !hook {
		return pushRangeScope(ctx, "file(s)", func(files []string) []string { return files })
	}
	updates, err := mainPushUpdates(refs)
	if err != nil {
		return nil, "", err
	}
	if len(updates) == 0 {
		return nil, "no main updates", nil
	}
	git := gitIn(sparkwing.WorkDir())
	head, err := git(ctx, "rev-parse", "HEAD")
	if err != nil {
		return nil, "", fmt.Errorf("pre-push: resolve checked-out HEAD: %w", err)
	}
	whole := false
	for _, update := range updates {
		if update.local != head {
			return nil, "", fmt.Errorf("pre-push: refusing main update from %s: checked-out HEAD is %s; check out the commit being pushed and retry", update.local, head)
		}
		if strings.Trim(update.remote, "0") == "" {
			whole = true
			continue
		}
		if _, err := git(ctx, "merge-base", "--is-ancestor", update.remote, update.local); err != nil {
			return nil, "", fmt.Errorf("pre-push: refusing non-fast-forward or unverifiable main update %s -> %s; fetch the destination and push a descendant of its main: %w", update.remote, update.local, err)
		}
	}
	var commands [][]string
	scope := "file(s) since Git's advertised main destination"
	if whole {
		commands = [][]string{{"ls-tree", "-r", "--name-only", "-z", head}}
		scope = "committed file(s) for new main; every Go module and critical test"
	} else {
		for _, update := range updates {
			commands = append(commands, []string{"diff", "-z", "--name-only", "--no-renames", update.remote, update.local})
		}
	}
	files := map[string]bool{}
	for _, args := range commands {
		out, err := runGitIn(ctx, sparkwing.WorkDir(), args...)
		if err != nil {
			return nil, "", fmt.Errorf("pre-push: list committed main update files: %w", err)
		}
		for file := range strings.SplitSeq(out, "\x00") {
			if file != "" {
				files[file] = true
			}
		}
	}
	return mapKeys(files), fmt.Sprintf("%d %s", len(files), scope), nil
}

// perf: a warm broad short replay exceeded twenty minutes, so tests have a
// separate deadline from the fast checks' one-minute warning budget.
const affectedPushTestsTimeout = 45 * time.Minute

type affectedPushTestsJob struct{ sparkwing.Base }

func affectedPushTestsJobNode(plan *sparkwing.Plan) *sparkwing.JobNode {
	return sparkwing.Job(plan, "affected-tests", &affectedPushTestsJob{}).
		Timeout(affectedPushTestsTimeout).
		NoProgressTimeout(affectedPushTestsTimeout).
		Cores(float64(goStepParallelism(affectedPushHost())))
}

// perf: a submitter's Go limits describe its own work, not the runner. Local
// landing always shares the machine, including when the SDK sets CI.
func affectedPushHost() hostShape {
	return hostShape{cpus: runtime.NumCPU()}
}

func (*affectedPushTestsJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	sparkwing.Step(w, "test", runAffectedPushTests)
	return nil, nil
}

func runAffectedPushTests(ctx context.Context) error {
	files, scope, err := mainPushScope(ctx)
	if err != nil {
		return err
	}
	return runAffectedTestFiles(ctx, files, scope, affectedPushHost())
}

// Work declares the fast push-boundary steps.
//
// perf: they run in parallel rather than cheapest-first, so the tier costs
// what its slowest member costs and a chain would only make the verdict later.
// FailFast still stops the first failure. The budget step warns past the one-minute
// budget; run timings measure the separate ten-second warm-cache target.
func (p *PrePush) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	w.FailurePolicy(sparkwing.FailFast)
	budget := newTierBudget("pre-push", prePushBudget).over(pushRangeScope)
	budget.step(w, "changelog", checkChangelogRequired)
	budget.step(w, "api-spec", checkAPISpec)
	budget.step(w, "api-snapshot", checkAPISnapshot)
	budget.step(w, "build-touched", runBuildTouched)
	budget.step(w, "vet-touched", runVetTouched)
	budget.step(w, "lint-touched", runFastLintTouched)
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
	if _, err := sparkwing.Bash(ctx, "bash bin/check-api-spec.sh").Env("GOWORK", "auto").Run(); err != nil {
		return fmt.Errorf("api/openapi.yaml disagrees with the controller's route table; run `bash bin/gen-api-docs.sh`: %w", err)
	}
	return nil
}

func checkAPISnapshot(ctx context.Context) error {
	if _, err := sparkwing.Bash(ctx, "bash bin/check-api-snapshot.sh").Env("GOWORK", "auto").Run(); err != nil {
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
	return overTouchedPackages(ctx, "vet-touched", "compile", "Go file(s)", existingGoFiles, vetTouchedCommand)
}

func vetTouchedCommand(module string, pkgs []string) string {
	cores := prePushTaskCores(runtime.NumCPU())
	qa := goCommandAt(cores, "vet", "-tags qatrust "+strings.Join(pkgs, " "))
	if def := defaultBuildPackages(module, pkgs, true); len(def) > 0 {
		return goCommandAt(cores, "vet", strings.Join(def, " ")) + " && " + qa
	}
	return qa
}

// safety: the gate vets the qatrust flavor of the packages a change touches, so
// land can push the tree it gated without re-running the pre-push hook.
func runQATrustVetTouched(ctx context.Context) error {
	return overTouchedPackages(ctx, "vet-qatrust", "compile", "Go file(s)", existingGoFiles,
		func(_ string, pkgs []string) string {
			return goCommandAt(prePushTaskCores(runtime.NumCPU()), "vet", "-tags qatrust "+strings.Join(pkgs, " "))
		})
}

// perf: the fast linter subset over the packages the push touches. The full
// set measured ninety seconds over both modules, which is the release cut's
// price to pay, not the push boundary's.
func runFastLintTouched(ctx context.Context) error {
	return overTouchedPackages(ctx, "lint-touched", "lint", "Go file(s)", existingGoFiles, fastLintTouchedCommand)
}

func fastLintTouchedCommand(module string, pkgs []string) string {
	pkgs = defaultBuildPackages(module, pkgs, true)
	if len(pkgs) == 0 {
		return "true"
	}
	return fastLintCommand(prePushTaskCores(runtime.NumCPU()), pkgs)
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
	return overPackageFiles(ctx, step, verb, files, scope, command)
}

func overPackageFiles(ctx context.Context, step, verb string, files []string, scope string, command func(module string, pkgs []string) string) error {
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
		if _, runErr := sparkwing.Bash(ctx, command(module, pkgs)).Dir(module).Env("GOWORK", "auto").Run(); runErr != nil {
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
