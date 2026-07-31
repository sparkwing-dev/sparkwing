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

// lintLockWait bounds how long the lint step waits for golangci-lint's
// box-wide parallel-runner lock. --allow-serial-runners on its own
// waits forever, so without a bound one wedged linter anywhere on the
// machine would pin every gate that queues behind it.
const lintLockWait = 5 * time.Minute

// golangciContention is what golangci-lint prints when it gives up on
// the lock. Worth matching even with --allow-serial-runners passed: an
// older binary that predates the flag, or a config that re-enables the
// default, still produces it, and it must not read as a finding.
const golangciContention = "parallel golangci-lint is running"

// Lint concurrency is decided by two numbers measured on 2026-07-30 in
// two real worktrees on this box (10 cores, 16 GB):
//
//	cold repo-wide lint   43.0s at 4.03 avg cores, 50.1s at 3.44, 2.5 GB peak RSS
//	warm repo-wide lint    2.0s at ~3.5 avg cores, 134 MB peak RSS
//
// Cold is the number that matters because GOLANGCI_LINT_CACHE is scoped
// per worktree (BW-1223), so every fresh agent worktree pays a cold lint
// on its first gate. Cores set the budget rather than memory because
// wingd's external memory reading is currently pinned at 80% of capacity
// and is not a real measurement (BW-1454), while its core reading is.
//
// lintCoreCost ASSUMES THE CURRENT LINT SCOPE. BW-1455 will widen lint
// from `.sparkwing/` alone to the whole product tree, which makes a lint
// materially more expensive and must re-derive this one number. It is a
// cost in cores rather than a slot count precisely so that widening the
// scope changes one measured constant instead of a hand-tuned N.
const lintCoreCost = 4.0

// lintReserveCores is what the budget leaves for the rest of the machine,
// matching the admission daemon's own reserve so the lint budget cannot
// hand out capacity the daemon is deliberately holding back.
const lintReserveCores = 2.0

// lintBudget is the box-wide golangci-lint budget every gate in this
// repo draws from. It is box-scoped because golangci-lint's own
// parallel-runner lock is box-wide, so replacing that lock requires a
// budget with the same reach.
var lintBudget = sparkwing.BoxToolBudget("golangci-lint", grantableCores(), lintLockWait)

// grantableCores is what this machine will lend to linting: its core
// count less the daemon's reserve, floored at one core so a small box
// still runs exactly one lint rather than none.
func grantableCores() float64 {
	c := float64(goruntime.NumCPU()) - lintReserveCores
	if c < 1 {
		c = 1
	}
	return c
}

// lintSlotCost is the budget one lint draws, clamped to the whole budget
// so a machine too small to fit the measured cost runs one lint at a time
// instead of panicking on a cost that could never be admitted.
func lintSlotCost() int {
	cost := sparkwing.ToolCostCenticores(lintCoreCost)
	if capacity := lintBudget.Limit().Capacity; cost > capacity {
		return capacity
	}
	return cost
}

// lintCommand builds this repo's only golangci-lint command line.
//
// The flag is the whole point of the budget. --allow-parallel-runners
// drops golangci-lint's private box-wide lock, which is safe only while
// something else bounds how many linters run at once; that something is
// the wingd slot the caller holds. --allow-serial-runners keeps the
// private lock and waits on it, which is correct but admits exactly one
// linter per box no matter how much headroom there is.
//
// Passing the parallel flag without holding the slot would leave nothing
// serializing lint at all, so the caller decides by whether it was
// granted, never by configuration.
func lintCommand(holdsBudget bool) string {
	flag := "--allow-serial-runners"
	if holdsBudget {
		flag = "--allow-parallel-runners"
	}
	return "cd .sparkwing && golangci-lint run " + flag + " ./..."
}

// runGolangciLint lints the .sparkwing module under the box-wide
// lintBudget, waiting out any other golangci-lint on the box for up to
// lintLockWait. On a fixed-workdir k8s runner it seeds the cache from the
// blob store before running and saves the result after a clean run. The
// returned error already reads as a gate failure line, so a caller
// collecting failures can take it unchanged.
//
// The budget is held around the linter only, not the whole job, because a
// gate step that serializes a linter must free the budget the moment the
// linter exits rather than pin it through the rest of a multi-minute
// pre-push run.
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

	release, holdsBudget := sparkwing.ToolSlot(ctx, lintBudget, lintSlotCost())
	defer release()

	cacheDir := sparkwing.ToolCacheDir("golangci-lint")
	if holdsBudget {
		sparkwing.Info(ctx, "golangci-lint: holding %s; running parallel (cache %s)", lintBudget, cacheDir)
	} else {
		sparkwing.Warn(ctx, "golangci-lint: no box budget; falling back to the tool's own box-wide lock (cache %s)", cacheDir)
	}

	lintCtx, cancel := context.WithTimeout(ctx, lintLockWait)
	defer cancel()

	lintStart := time.Now()
	_, err := sparkwing.Bash(lintCtx, lintCommand(holdsBudget)).
		Env("GOLANGCI_LINT_CACHE", cacheDir).
		Run()
	lintDur := time.Since(lintStart)
	if err != nil {
		return errors.New(describeLintFailure(lintCtx, lintDur, err))
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

// describeLintFailure says what is known about a failed lint step. The
// bare exec error is "command failed (exit 1)", which reads as a lint
// finding and sends the reader hunting for a regression in a tree that
// is clean. Contention is named only where golangci-lint itself
// reported it; a wait that runs out establishes that the step did not
// finish, never why, so that case reports the time actually waited and
// leaves the cause as the likelihood it is.
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
