package jobs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

// Lint concurrency is decided by two numbers, re-derived on 2026-07-30
// against the widened scope this step now covers. Measured on this box
// (10 cores, 16 GB) with `/usr/bin/time -l` and a fresh isolated cache,
// under real agent load, so the core figures are floors rather than
// ceilings: a linter that wants more than the box has spare cannot show
// it wanted more.
//
//	cold root module   43.9s-67.3s at 2.67-3.80 avg cores, 2.1-2.7 GB peak RSS
//	cold .sparkwing/   11.3s-18.5s at 3.25-4.84 avg cores, 2.6-2.7 GB peak RSS
//	warm, either module     2s-3s at 1.04-2.60 avg cores, 100-130 MB peak RSS
//
// Lint cost is bimodal, not distributed, and those two rows are the two
// modes. 4.0 is the cold mode. It is deliberately NOT an average of the
// two, because an average is a number no run ever exhibits: it
// over-admits against cold and under-admits against warm, and the mode
// that decides safety is the expensive one.
//
// Local worktrees now reuse four canonical cache paths, so steady-state
// lint is warm. Cold remains the safe admission price because all four
// paths still need a first fill, and a run falls back to its private cache
// whenever every path is leased. Admission has no cache-temperature
// signal, so it cannot safely price one run as warm and the next as cold.
//
// Pricing the warm mode today would be unsafe rather than merely
// optimistic. At 2.0 the budget admits four concurrent lints; four cold
// linters want 12-16 cores on a 10-core box, which is the
// oversubscription the budget exists to prevent, on a machine that has
// already been observed in swap.
//
// Cores set the budget rather than memory because wingd's external memory
// reading is pinned at 80% of capacity rather than measured, while its core
// reading reflects observed demand.
//
// Widening the scope did not move this number, which is worth stating
// because it is the opposite of what it looks like. lintCoreCost prices
// what ONE linter demands while the slot is held, and the step below runs
// its modules one at a time inside a single slot, so the instantaneous
// demand is one linter's either way. `.sparkwing/` was never the small
// module it reads as: its go.mod replaces the SDK with `..`, so linting it
// already type-checked the whole parent tree, which is why it measures the
// same 3-5 cores the root module does and why the widening roughly doubled
// total CPU rather than multiplying it by ten.
//
// What the widening did move is how long the slot is held: about 15s cold
// for `.sparkwing/` alone against about 55s cold for both modules. That is
// paid in queue wait, not in cores, and it is not what this constant
// expresses.
//
// Raising it anyway would be a regression rather than caution. Grantable
// capacity here is 8 cores, so 4.0 admits two concurrent lints and 5.0
// admits one, and admitting one is exactly the box-wide serialization the
// budget was built to remove.
const lintCoreCost = 4.0

// measuredColdCoreDemand is the lowest average core draw a cold lint was
// measured at, across both modules. It exists so the cold mode is a
// number the tests can hold lintCoreCost against rather than a claim in
// the comment above: pricing the budget below what a cold run actually
// draws lets the box admit more concurrent linters than it has cores to
// run, and the warm mode is the tempting wrong answer that would do it.
const measuredColdCoreDemand = 2.67

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

// lintBaselineRef is the ref .golangci.yml grandfathers findings against
// (issues.new-from-merge-base). The gate resolves it itself because
// golangci-lint does not: handed a ref it cannot resolve, the linter
// lints the whole tree and exits 1 without ever saying the baseline went
// missing. Across the modules this step now covers that is every standing
// finding in the repo reported as if the commit in front of it wrote them.
const lintBaselineRef = "origin/main"

// lintCommandFor builds the golangci-lint line for one module.
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
func lintCommandFor(holdsBudget bool) string {
	flag := "--allow-serial-runners"
	if holdsBudget {
		flag = "--allow-parallel-runners"
	}
	return fmt.Sprintf("golangci-lint run %s ./...", flag)
}

func shouldLeaseLintPath(gcURL string) bool {
	if gcURL != "" {
		return false
	}
	gitMarker, err := os.Stat(filepath.Join(sparkwing.WorkDir(), ".git"))
	return err == nil && !gitMarker.IsDir()
}

// runGolangciLint lints every committed Go module under the box-wide
// lintBudget, waiting out any other golangci-lint on the box for up to
// lintLockWait. It walks committedModuleDirs rather than naming a module,
// so the product tree (cmd/, internal/, pkg/, sparkwing/) is covered
// alongside .sparkwing/ and a module added later is covered the day its
// go.mod lands. Before this walked the tree the step ran inside
// .sparkwing/ alone and reported the repo healthy while never opening 90%
// of it.
//
// It prints the modules and the baseline it is judging against before it
// runs any of them, because a step that narrows its own scope is
// indistinguishable from a step that found nothing, and that is how the
// .sparkwing-only scope survived for two months.
//
// One admission grant and one cache-path lease cover the whole walk.
// Releasing either between modules could over-admit another linter or
// let another worktree repoint the canonical path while this run still
// owns results from it. It would also pay the queue wait once per module.
//
// The walk and the baseline are resolved before the slot is taken. Both
// are cheap local git reads, and a checkout that cannot name its baseline
// should fail without first occupying box-wide capacity it is only going
// to give back.
//
// Fixed checkouts and blob-store-backed runners keep their private
// ToolCacheDir so restore and save seed the directory lint reads; local
// disposable worktrees lease a canonical path instead. The returned error
// already reads as a gate failure line, so a caller collecting failures can
// take it unchanged.
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

// describeLintScope is the line the step prints before it lints anything.
// It names every module it is about to cover and the baseline it will
// judge them against, because an exit code cannot tell a reader whether
// the step looked at the repo or at a tenth of it. That is not a
// hypothetical distinction here: the step read .sparkwing/ alone from
// 2026-05-20 and its silence was indistinguishable from a clean repo.
func describeLintScope(dirs []string, baseline string) string {
	return fmt.Sprintf("scope is %d committed module(s) -- %s (%s)",
		len(dirs), strings.Join(dirs, ", "), baseline)
}

// resolveLintBaseline describes the baseline the lint run will be judged
// against, and fails the step when the ref does not resolve. Failing here
// is the cheaper red: the fix is one `git fetch origin main`, where the
// alternative is golangci-lint quietly dropping the baseline and charging
// the author for every finding the repo has ever grandfathered. A check
// that cannot tell new code from old must say so rather than guess.
func resolveLintBaseline(ctx context.Context) (string, error) {
	sha, err := sparkwing.Bash(ctx,
		`git -C "$SPARKWING_WORKDIR" rev-parse --verify --quiet "$LINT_BASELINE_REF^{commit}"`,
	).Env("SPARKWING_WORKDIR", sparkwing.Path()).
		Env("LINT_BASELINE_REF", lintBaselineRef).
		String()
	sha = strings.TrimSpace(sha)
	if err != nil || sha == "" {
		return "", fmt.Errorf("golangci-lint: could not run -- .golangci.yml baselines findings against "+
			"%s and this checkout cannot resolve it, so the linter would report every standing "+
			"finding in the tree against this change. Run `%s`", lintBaselineRef, fetchBaselineHint())
	}
	if len(sha) > 12 {
		sha = sha[:12]
	}
	return fmt.Sprintf("baseline %s at %s", lintBaselineRef, sha), nil
}

// fetchBaselineHint turns the configured baseline ref into the fetch that
// would make it resolvable. Derived rather than written out, because the
// thing that went missing is whatever .golangci.yml names, and a hint that
// says "git fetch origin main" to somebody whose baseline is a different
// remote or default branch sends them to fix a ref they do not have.
func fetchBaselineHint() string {
	remote, branch, ok := strings.Cut(lintBaselineRef, "/")
	if !ok || remote == "" || branch == "" {
		return "git fetch --all"
	}
	return fmt.Sprintf("git fetch %s %s", remote, branch)
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
