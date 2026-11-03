package jobs

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

const lintLockWait = 5 * time.Minute

// safety: the gate waits for a neighbor's lint on this separate bound, so a
// held lock fails by name instead of eating the lint deadline.
const gateLintLockWait = 20 * time.Minute

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

// safety: only for a caller that already holds the box-wide lock itself, so
// golangci-lint must not wait on the lock its own parent holds.
func lockedLintCommand(base string) string {
	return "golangci-lint run --allow-parallel-runners --new-from-merge-base= --new-from-rev=" + base + " ./..."
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
		cores, shellQuote(pipelineCacheDir("golangci-lint")), cores, strings.Join(prePushLinters, ","), strings.Join(pkgs, " "))
}

func runGolangciLintWithin(ctx context.Context, deadline time.Duration) error {
	return lintModules(ctx, deadline, lintCommand, false)
}

func lintModules(ctx context.Context, deadline time.Duration, command func(base string) string, holdsLock bool) error {
	doing := "waiting for the box-wide lock if held, then linting"
	if holdsLock {
		doing = "holding the box-wide lock, linting"
	}
	dirs, err := committedModuleDirs(ctx)
	if err != nil {
		return fmt.Errorf("golangci-lint: could not run -- listing the modules to lint failed: %w", err)
	}
	base, baseline, err := resolveGateBase(ctx)
	if err != nil {
		return err
	}
	sparkwing.Info(ctx, "golangci-lint: %s", describeLintScope(dirs, baseline))

	// safety: each worktree keeps its own cache. A cache shared between trees
	// replays findings under the path of the tree that filled it, and those sit
	// outside this tree's diff, so the baseline filter drops every one.
	cacheDir := pipelineCacheDir("golangci-lint")
	lintCtx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()

	var failures []string
	for _, dir := range dirs {
		if empty, emptyErr := moduleHasNoPackages(lintCtx, dir); emptyErr == nil && empty {
			sparkwing.Info(lintCtx, "golangci-lint: %s holds no packages to lint", dir)
			continue
		}
		stepStart := time.Now()
		sparkwing.Info(lintCtx, "golangci-lint: %s %s (deadline %s)", dir, doing, deadline)
		cmd := sparkwing.Bash(lintCtx, command(base)).
			Dir(dir).
			Env("GOLANGCI_LINT_CACHE", cacheDir)
		if _, runErr := cmd.Run(); runErr != nil {
			failures = append(failures,
				fmt.Sprintf("%s: %s", dir, describeLintFailure(lintCtx, time.Since(stepStart), runErr, holdsLock)))
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

func describeLintFailure(ctx context.Context, waited time.Duration, err error, holdsLock bool) string {
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
	if errors.Is(ctx.Err(), context.DeadlineExceeded) && holdsLock {
		return fmt.Sprintf("golangci-lint: could not finish -- linting itself took more than its deadline "+
			"(%s so far, the box-wide lock held throughout), so nothing was learned about this tree.", waited.Round(time.Second))
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Sprintf("golangci-lint: could not run -- no result after %s. Most often "+
			"that is the box-wide golangci-lint lock held by another run; either way "+
			"nothing was learned about this tree.", waited.Round(time.Second))
	}
	return fmt.Sprintf("golangci-lint: %v", err)
}
