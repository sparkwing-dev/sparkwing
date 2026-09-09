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
	cores := float64(goruntime.NumCPU()) - lintReserveCores
	if cores < 1 {
		cores = 1
	}
	return cores
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
	// SAFETY: Shared lint slots can return findings cached for another checkout.
	return false
}

func runGolangciLint(ctx context.Context) error {
	cacheURL := os.Getenv("SPARKWING_GITCACHE_URL")
	cacheToken := os.Getenv("SPARKWING_CACHE_TOKEN")

	restored, restoredBytes, restoreErr := sparkwing.RestoreLintCache(ctx, cacheURL)
	switch {
	case restoreErr != nil:
		sparkwing.Warn(ctx, "lint cache: restore: %v", restoreErr)
	case restored:
		sparkwing.Info(ctx, "lint cache: restored %d bytes from blob store", restoredBytes)
	}

	directories, err := committedModuleDirs(ctx)
	if err != nil {
		return fmt.Errorf("golangci-lint: could not run -- listing the modules to lint failed: %w", err)
	}
	baseline, err := resolveLintBaseline(ctx)
	if err != nil {
		return err
	}
	sparkwing.Info(ctx, "golangci-lint: %s", describeLintScope(directories, baseline))

	release, holdsBudget := sparkwing.ToolSlot(ctx, lintBudget, lintSlotCost())
	defer release()

	cacheDirectory := sparkwing.ToolCacheDir("golangci-lint")
	var lintSlot *sparkwing.LintSlot
	if shouldLeaseLintPath(cacheURL) {
		lintSlot, err = sparkwing.AcquireLintSlot("golangci-lint")
		if err != nil {
			return fmt.Errorf("golangci-lint: could not acquire a reusable cache path: %w", err)
		}
		defer lintSlot.Release()
		cacheDirectory = lintSlot.Cache
	}
	if holdsBudget {
		sparkwing.Info(ctx, "golangci-lint: holding %s; running parallel (cache %s)", lintBudget, cacheDirectory)
	} else {
		sparkwing.Warn(ctx, "golangci-lint: no box budget; falling back to the tool's own box-wide lock (cache %s)", cacheDirectory)
	}

	lintCtx, cancel := context.WithTimeout(ctx, lintLockWait)
	defer cancel()

	lintStart := time.Now()
	var failures []string
	for _, directory := range directories {
		empty, err := moduleHasNoPackages(lintCtx, directory)
		if err != nil {
			return fmt.Errorf("golangci-lint: %w", err)
		}
		if empty {
			sparkwing.Info(lintCtx, "golangci-lint: %s holds no packages to lint", directory)
			continue
		}
		stepStart := time.Now()
		command := sparkwing.Bash(lintCtx, lintCommandFor(holdsBudget))
		if lintSlot != nil {
			command = lintSlot.ConfigureIn(command, directory, "GOLANGCI_LINT_CACHE")
		} else {
			command = command.Dir(directory).Env("GOLANGCI_LINT_CACHE", cacheDirectory)
		}
		if _, runErr := command.Run(); runErr != nil {
			failures = append(failures,
				fmt.Sprintf("%s: %s", directory, describeLintFailure(lintCtx, time.Since(stepStart), runErr)))
			continue
		}
		sparkwing.Info(lintCtx, "golangci-lint: %s clean (%s)", directory, time.Since(stepStart).Round(time.Second))
	}
	lintDuration := time.Since(lintStart)
	if len(failures) > 0 {
		return fmt.Errorf("golangci-lint failed in %d of %d module(s):\n  - %s",
			len(failures), len(directories), strings.Join(failures, "\n  - "))
	}

	savedBytes, saveErr := sparkwing.SaveLintCache(ctx, cacheURL, cacheToken)
	switch {
	case saveErr != nil:
		sparkwing.Warn(ctx, "lint cache: save: %v", saveErr)
	case savedBytes > 0:
		sparkwing.Info(ctx, "lint cache: saved %d bytes (lint ran %s)", savedBytes, lintDuration.Round(time.Second))
	}
	return nil
}

func describeLintScope(directories []string, baseline string) string {
	return fmt.Sprintf("scope is %d committed module(s) -- %s (%s)",
		len(directories), strings.Join(directories, ", "), baseline)
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
