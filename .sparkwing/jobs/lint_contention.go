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

func runGolangciLint(ctx context.Context) error {
	cacheURL := os.Getenv("SPARKWING_GITCACHE_URL")
	cacheToken := os.Getenv("SPARKWING_CACHE_TOKEN")

	restored, restoredBytes, restoreErr := sparkwing.RestoreLintCache(ctx, cacheURL)
	switch {
	case restoreErr != nil:
		return fmt.Errorf("lint cache: restore: %w", restoreErr)
	case restored:
		sparkwing.Info(ctx, "lint cache: restored %d bytes from blob store", restoredBytes)
	}

	directories, err := committedModuleDirs(ctx)
	if err != nil {
		return fmt.Errorf("golangci-lint: list modules: %w", err)
	}
	baseline, err := resolveLintBaseline(ctx)
	if err != nil {
		return err
	}
	sparkwing.Info(ctx, "golangci-lint: %s", describeLintScope(directories, baseline))

	release, holdsBudget := sparkwing.ToolSlot(ctx, lintBudget, lintSlotCost())
	defer release()

	cacheDirectory := sparkwing.ToolCacheDir("golangci-lint")
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
		packages, err := modulePackageArgs(lintCtx, directory, true)
		if err != nil {
			return fmt.Errorf("golangci-lint: %w", err)
		}
		if len(packages) == 0 {
			sparkwing.Info(lintCtx, "golangci-lint: %s holds no packages to lint", directory)
			continue
		}
		stepStart := time.Now()
		invocation := strings.TrimSuffix(lintCommandFor(holdsBudget), "./...") + strings.Join(packages, " ")
		command := sparkwing.Bash(lintCtx, invocation).Dir(directory).Env("GOLANGCI_LINT_CACHE", cacheDirectory)
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
		return fmt.Errorf("lint cache: save: %w", saveErr)
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
	commit, err := resolveBaselineCommit(ctx)
	if err != nil {
		return "", fmt.Errorf("golangci-lint: %w", err)
	}
	return fmt.Sprintf("baseline %s at %s", gateBaselineRef, commit), nil
}

func resolveBaselineCommit(ctx context.Context) (string, error) {
	root, err := sourcePolicyRoot()
	if err != nil {
		return "", err
	}
	output, err := sparkwing.Exec(ctx, "git", "rev-parse", "--verify", gateBaselineRef+"^{commit}").Dir(root).Capture()
	if err != nil {
		cause := errors.Join(err, ctx.Err())
		if ctx.Err() != nil {
			return "", fmt.Errorf("resolve baseline %s: %w", gateBaselineRef, cause)
		}
		return "", fmt.Errorf("cannot resolve baseline %s; run `%s`: %w", gateBaselineRef, fetchBaselineHint(), cause)
	}
	commit := strings.TrimSpace(output.Stdout)
	if commit == "" {
		return "", fmt.Errorf("git rev-parse returned an empty commit ID for %s", gateBaselineRef)
	}
	return commit, nil
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
		return "golangci-lint: another process holds the machine-wide lock"
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Sprintf("golangci-lint: no result before the deadline after %s", waited.Round(time.Second))
	}
	return fmt.Sprintf("golangci-lint: %v", err)
}
