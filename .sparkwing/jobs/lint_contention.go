package jobs

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// perf: linting both modules measures about four to five minutes on a loaded box; the gate allows ten,
// including any wait for a neighbor's lint (decision 96).
const gateLintDeadline = 10 * time.Minute

const golangciContention = "parallel golangci-lint is running"

const golangciTimeout = "Timeout exceeded"

// safety: golangci-lint reserves exit 4 for its own timeout, and the text
// formatter echoes source lines, so a finding against this file would match the
// message alone.
const golangciTimeoutExit = 4

const gateBaselineRef = "origin/main"

func lintCommand(base string) string {
	return "golangci-lint run --allow-serial-runners --new-from-merge-base= --new-from-rev=" + base + " ./..."
}

// perf: affected lint has its own checkout cache and bounded workers, so it
// can judge a small diff while a neighboring full scan owns the global lock.
func affectedLintCommand(base string) string {
	cores := goStepParallelism(currentHost())
	return fmt.Sprintf("GOMAXPROCS=%d golangci-lint run --allow-parallel-runners -j %d --new-from-merge-base= --new-from-rev=%s ./...", cores, cores, base)
}

// perf: the subset that answers in about a second over the packages one push
// touches. Every name is enabled in .golangci.yml, the whole set the release
// cut runs; what the subset leaves out is the type-and-SSA family, which
// costs minutes over a module.
var prePushLinters = []string{
	"bidichk",
	"copyloopvar",
	"depguard",
	"ineffassign",
	"misspell",
	"nolintlint",
	"unconvert",
	"usestdlibvars",
}

// perf: the hook holds a push open, so it never queues behind the box-wide
// golangci-lint lock; the subset over a handful of packages is small enough
// load to run beside whatever holds it. The timeout only bounds a hung run:
// in a fresh worktree the lint cache is cold, and loading 17 packages beside
// vet-touched measured over 77 s.
func fastLintCommand(cores int, pkgs []string) string {
	return fmt.Sprintf("GOMAXPROCS=%d GOLANGCI_LINT_CACHE=%s golangci-lint run --allow-parallel-runners -j %d --timeout 5m --enable-only %s %s",
		cores, shellQuote(lintCacheDir()), cores, strings.Join(prePushLinters, ","), strings.Join(pkgs, " "))
}

// safety: golangci-lint queues serial runners on its own box-wide lock, and
// that wait counts against the deadline; a lint timeout means checking for the
// neighboring run (DELIVERY.md).
func runGolangciLint(ctx context.Context) error {
	return runGolangciLintOver(ctx, nil)
}

func runGolangciLintOver(ctx context.Context, targets map[string][]string) error {
	return lintModules(ctx, gateLintDeadline, lintCommand, targets)
}

func runGolangciLintWithin(ctx context.Context, deadline time.Duration) error {
	return lintModules(ctx, deadline, lintCommand, nil)
}

// perf: --new-from-rev drops findings outside the change, so linting only the
// packages holding changed Go files reaches the same verdict (cold: 561 s for
// the root module, 10 to 110 s for one package). Broken importers fail the
// build step; a module or lint configuration change lints everything.
func runGolangciLintTouched(ctx context.Context) error {
	files, scope, err := affectedChangeScope(ctx)
	if err != nil {
		return err
	}
	for _, f := range files {
		switch path.Base(f) {
		case "go.mod", "go.sum", ".golangci.yml":
			sparkwing.Info(ctx, "golangci-lint: %s changed, so every package is linted", f)
			return runGolangciLintOver(ctx, nil)
		}
	}
	modules, err := committedModuleDirs(ctx)
	if err != nil {
		return err
	}
	targets := touchedPackageTargets(existingGoFiles(files), modules)
	for module, pkgs := range targets {
		targets[module] = defaultBuildPackages(module, pkgs, true)
	}
	sparkwing.Info(ctx, "golangci-lint: linting the packages that hold changed Go files (%s)", scope)
	return lintModules(ctx, gateLintDeadline, affectedLintCommand, targets)
}

// safety: nil targets lint every package; a module absent from targets is skipped.
func lintModules(ctx context.Context, deadline time.Duration, command func(base string) string, targets map[string][]string) error {
	dirs, err := committedModuleDirs(ctx)
	if err != nil {
		return fmt.Errorf("golangci-lint: could not run -- listing the modules to lint failed: %w", err)
	}
	base, baseline, err := resolveGateBase(ctx)
	if err != nil {
		return err
	}
	sparkwing.Info(ctx, "golangci-lint: %s", describeLintScope(dirs, baseline))

	cacheDir := lintCacheDir()
	lintCtx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()

	var failures []string
	for _, dir := range dirs {
		script := command(base)
		if targets != nil {
			if len(targets[dir]) == 0 {
				sparkwing.Info(lintCtx, "golangci-lint: %s holds no changed Go package", dir)
				continue
			}
			script = strings.TrimSuffix(script, "./...") + strings.Join(targets[dir], " ")
		} else if empty, emptyErr := moduleHasNoPackages(lintCtx, dir); emptyErr == nil && empty {
			sparkwing.Info(lintCtx, "golangci-lint: %s holds no packages to lint", dir)
			continue
		}
		doing := "linting with bounded workers"
		if strings.Contains(script, "--allow-serial-runners") {
			doing = "waiting for the box-wide lock if held, then linting"
		}
		stepStart := time.Now()
		sparkwing.Info(lintCtx, "golangci-lint: %s %s (deadline %s)", dir, doing, deadline)
		cmd := sparkwing.Bash(lintCtx, script).
			Dir(dir).
			Env("GOLANGCI_LINT_CACHE", cacheDir)
		if _, runErr := cmd.Run(); runErr != nil {
			failures = append(failures,
				fmt.Sprintf("%s: %s", dir, describeLintFailure(lintCtx, time.Since(stepStart), runErr)))
			continue
		}
		sparkwing.Info(lintCtx, "golangci-lint: %s clean (%s)", dir, time.Since(stepStart).Round(time.Second))
	}
	if len(failures) > 0 {
		return fmt.Errorf("golangci-lint failed in %d of %d module(s):\n  - %s",
			len(failures), len(dirs), strings.Join(failures, "\n  - "))
	}

	return nil
}

func describeLintScope(dirs []string, baseline string) string {
	return fmt.Sprintf("scope is %d committed module(s) -- %s (%s)",
		len(dirs), strings.Join(dirs, ", "), baseline)
}

func describeLintFailure(ctx context.Context, waited time.Duration, err error) string {
	var execErr *sparkwing.ExecError
	if errors.As(err, &execErr) && strings.Contains(execErr.Stdout+execErr.Stderr, golangciContention) {
		return "golangci-lint: could not run -- another golangci-lint holds the box-wide " +
			"lock. That is contention, not a finding in this tree."
	}
	if errors.As(err, &execErr) && execErr.ExitCode == golangciTimeoutExit &&
		strings.Contains(execErr.Stdout+execErr.Stderr, golangciTimeout) {
		return fmt.Sprintf("golangci-lint: could not finish -- it stopped at its own run.timeout after %s, "+
			"so the packages it never reached were not judged and anything it printed is partial. "+
			"Raise run.timeout in .golangci.yml rather than acting on the findings above.",
			waited.Round(time.Second))
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Sprintf("golangci-lint: could not run -- no result after %s. Most often "+
			"that is the box-wide golangci-lint lock held by another run; either way "+
			"nothing was learned about this tree.", waited.Round(time.Second))
	}
	return fmt.Sprintf("golangci-lint: %v", err)
}
