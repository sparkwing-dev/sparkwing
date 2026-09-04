package jobs

import (
	"context"
	"errors"
	"fmt"
	"os"
	goruntime "runtime"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

const lintLockWait = 5 * time.Minute

const golangciContention = "parallel golangci-lint is running"

const lintCoreCost = 4.0

const measuredColdCoreDemand = 2.67

const lintReserveCores = 2.0

var lintBudget = sparkwing.BoxToolBudget("golangci-lint", grantableCores(), lintLockWait)

func grantableCores() float64 {
	c := float64(goruntime.NumCPU()) - lintReserveCores
	if c < 1 {
		c = 1
	}
	return c
}

func lintSlotCost() int {
	cost := sparkwing.ToolCostCenticores(lintCoreCost)
	if capacity := lintBudget.Limit().Capacity; cost > capacity {
		return capacity
	}
	return cost
}

const gateBaselineRef = "origin/main"

func lintCommandFor(holdsBudget bool) string {
	flag := "--allow-serial-runners"
	if holdsBudget {
		flag = "--allow-parallel-runners"
	}
	return fmt.Sprintf("golangci-lint run %s ./...", flag)
}

func shouldLeaseLintPath(string) bool {
	// safety: the slot's cache is shared by every worktree that leases it, and
	// golangci-lint answers from cached findings whose paths belonged to the
	// tree the slot pointed at last, so a leased lint reported 0 issues for a
	// tree with eight. Each checkout keeps its own cache until the slot keys
	// its cache by tree.
	return false
}

func runGolangciLint(ctx context.Context) error {
	gcURL := os.Getenv("SPARKWING_GITCACHE_URL")
	gcToken := os.Getenv("SPARKWING_CACHE_TOKEN")

	restored, restoredBytes, restoreErr := sparkwing.RestoreLintCache(ctx, gcURL)
	switch {
	case restoreErr != nil:
		sparkwing.Warn(ctx, "lint cache: restore: %v", restoreErr)
	case restored:
		sparkwing.Info(ctx, "lint cache: restored %d bytes from blob store", restoredBytes)
	}

	dirs, err := committedModuleDirs(ctx)
	if err != nil {
		return fmt.Errorf("golangci-lint: could not run -- listing the modules to lint failed: %w", err)
	}
	baseline, err := resolveLintBaseline(ctx)
	if err != nil {
		return err
	}
	sparkwing.Info(ctx, "golangci-lint: %s", describeLintScope(dirs, baseline))

	release, holdsBudget := sparkwing.ToolSlot(ctx, lintBudget, lintSlotCost())
	defer release()

	cacheDir := sparkwing.ToolCacheDir("golangci-lint")
	var lintSlot *sparkwing.LintSlot
	if shouldLeaseLintPath(gcURL) {
		lintSlot, err = sparkwing.AcquireLintSlot("golangci-lint")
		if err != nil {
			return fmt.Errorf("golangci-lint: could not acquire a reusable cache path: %w", err)
		}
		defer lintSlot.Release()
		cacheDir = lintSlot.Cache
	}
	if holdsBudget {
		sparkwing.Info(ctx, "golangci-lint: holding %s; running parallel (cache %s)", lintBudget, cacheDir)
	} else {
		sparkwing.Warn(ctx, "golangci-lint: no box budget; falling back to the tool's own box-wide lock (cache %s)", cacheDir)
	}

	lintCtx, cancel := context.WithTimeout(ctx, lintLockWait)
	defer cancel()

	lintStart := time.Now()
	var failures []string
	for _, dir := range dirs {
		if empty, emptyErr := moduleHasNoPackages(lintCtx, dir); emptyErr == nil && empty {
			sparkwing.Info(lintCtx, "golangci-lint: %s holds no packages to lint", dir)
			continue
		}
		stepStart := time.Now()
		cmd := sparkwing.Bash(lintCtx, lintCommandFor(holdsBudget))
		if lintSlot != nil {
			cmd = lintSlot.ConfigureIn(cmd, dir, "GOLANGCI_LINT_CACHE")
		} else {
			cmd = cmd.Dir(dir).Env("GOLANGCI_LINT_CACHE", cacheDir)
		}
		if _, runErr := cmd.Run(); runErr != nil {
			failures = append(failures,
				fmt.Sprintf("%s: %s", dir, describeLintFailure(lintCtx, time.Since(stepStart), runErr)))
			continue
		}
		sparkwing.Info(lintCtx, "golangci-lint: %s clean (%s)", dir, time.Since(stepStart).Round(time.Second))
	}
	lintDur := time.Since(lintStart)
	if len(failures) > 0 {
		return fmt.Errorf("golangci-lint failed in %d of %d module(s):\n  - %s",
			len(failures), len(dirs), strings.Join(failures, "\n  - "))
	}

	savedBytes, saveErr := sparkwing.SaveLintCache(ctx, gcURL, gcToken)
	switch {
	case saveErr != nil:
		sparkwing.Warn(ctx, "lint cache: save: %v", saveErr)
	case savedBytes > 0:
		sparkwing.Info(ctx, "lint cache: saved %d bytes (lint ran %s)", savedBytes, lintDur.Round(time.Second))
	}
	return nil
}

func describeLintScope(dirs []string, baseline string) string {
	return fmt.Sprintf("scope is %d committed module(s) -- %s (%s)",
		len(dirs), strings.Join(dirs, ", "), baseline)
}

func resolveLintBaseline(ctx context.Context) (string, error) {
	sha, err := sparkwing.Bash(ctx,
		`git -C "$SPARKWING_WORKDIR" rev-parse --verify --quiet "$LINT_BASELINE_REF^{commit}"`,
	).Env("SPARKWING_WORKDIR", sparkwing.Path()).
		Env("LINT_BASELINE_REF", gateBaselineRef).
		String()
	sha = strings.TrimSpace(sha)
	if err != nil || sha == "" {
		return "", fmt.Errorf("golangci-lint: could not run -- .golangci.yml baselines findings against "+
			"%s and this checkout cannot resolve it, so the linter would report every standing "+
			"finding in the tree against this change. Run `%s`", gateBaselineRef, fetchBaselineHint())
	}
	if len(sha) > 12 {
		sha = sha[:12]
	}
	return fmt.Sprintf("baseline %s at %s", gateBaselineRef, sha), nil
}

func fetchBaselineHint() string {
	remote, branch, ok := strings.Cut(gateBaselineRef, "/")
	if !ok || remote == "" || branch == "" {
		return "git fetch --all"
	}
	return fmt.Sprintf("git fetch %s %s", remote, branch)
}

func describeLintFailure(ctx context.Context, waited time.Duration, err error) string {
	var execErr *sparkwing.ExecError
	if errors.As(err, &execErr) && strings.Contains(execErr.Stdout+execErr.Stderr, golangciContention) {
		return "golangci-lint: could not run -- another golangci-lint holds the box-wide " +
			"lock. That is contention, not a finding in this tree."
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Sprintf("golangci-lint: could not run -- no result after %s. Most often "+
			"that is the box-wide golangci-lint lock held by another run; either way "+
			"nothing was learned about this tree.", waited.Round(time.Second))
	}
	return fmt.Sprintf("golangci-lint: %v", err)
}
