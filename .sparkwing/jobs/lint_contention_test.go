package jobs

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestLintCommandWaitsForTheBoxWideLockInsteadOfFailingOnIt(t *testing.T) {
	got := lintCommandFor(".", false)
	if !strings.Contains(got, "--allow-serial-runners") {
		t.Fatalf("the gate would fail on a neighboring lint instead of waiting for it: %s", got)
	}
}

// TestLintCommandNeverDropsTheToolLockWithoutABudget pins the invariant
// the whole box budget rests on. golangci-lint's private lock is the only
// thing serializing lint when no wingd slot is held, so dropping it
// without the slot would leave concurrent linters with nothing bounding
// them at all. This is the corruption case, not a performance case.
//
// It matters more now than when it was written. The step runs one command
// per committed module, so a regression here drops the lock once per
// module rather than once per gate.
func TestLintCommandNeverDropsTheToolLockWithoutABudget(t *testing.T) {
	withoutBudget := lintCommandFor(".", false)
	if strings.Contains(withoutBudget, "--allow-parallel-runners") {
		t.Fatalf("lint dropped its own lock while holding no budget: %s", withoutBudget)
	}

	withBudget := lintCommandFor(".", true)
	if !strings.Contains(withBudget, "--allow-parallel-runners") {
		t.Fatalf("lint kept the serializing lock while holding a budget, so the budget buys nothing: %s", withBudget)
	}
	if strings.Contains(withBudget, "--allow-serial-runners") {
		t.Fatalf("lint passed both runner flags: %s", withBudget)
	}
}

// TestLintSlotCostIsAdmissibleOnThisBox guards the plan-time panic path:
// a cost larger than the budget could never be admitted, so a machine
// smaller than one measured lint must clamp to exclusive rather than
// crash the gate.
func TestLintSlotCostIsAdmissibleOnThisBox(t *testing.T) {
	cost, capacity := lintSlotCost(), lintBudget.Limit().Capacity
	if cost < 1 {
		t.Fatalf("lint draws no budget, so any number of lints would be admitted: cost %d", cost)
	}
	if cost > capacity {
		t.Fatalf("lint cost %d exceeds budget capacity %d, so it could never be admitted", cost, capacity)
	}
}

// TestLintBudgetIsBoxScoped pins the scope. golangci-lint's own lock is
// box-wide, so a budget replacing it has to reach exactly as far; a
// run-scoped budget would let every concurrent gate hold its own slot and
// serialize nothing.
func TestLintBudgetIsBoxScoped(t *testing.T) {
	if got := lintBudget.Limit().Scope; got != sparkwing.ScopeBox {
		t.Fatalf("lint budget scope is %q, so it does not bound the machine the tool lock bounded", got)
	}
	if got := lintBudget.Limit().OnLimit; got != sparkwing.Queue {
		t.Fatalf("lint budget on-limit is %q, so a contended gate fails instead of waiting", got)
	}
}

// Lint cost is bimodal: a cold run draws 2.67-4.84 average cores and a
// warm one 1.04-2.60. Pricing the budget at the warm mode is the tempting
// wrong answer, because it is the mode most runs exhibit once a cache is
// hot, and it would let the box admit four concurrent linters that each
// want three to four cores the moment any of them runs cold. Caches here
// are per-worktree (BW-1223) and worktrees are disposable, so cold is the
// common case and the safe one to price.
//
// This does not forbid re-pricing. It forbids re-pricing to a number below
// what a cold lint was measured to draw without also changing what makes
// cold rare, which is a cache that survives a worktree.
func TestLintCostIsPricedForTheColdRunNotTheWarmOne(t *testing.T) {
	if lintCoreCost < measuredColdCoreDemand {
		t.Fatalf("lint is priced at %.2f cores against a cold run measured at %.2f, so a full "+
			"budget of concurrent lints demands more cores than the box has",
			lintCoreCost, measuredColdCoreDemand)
	}
}

// The command has to name the module it lints, or the walk runs the same
// directory once per module and the widened scope is decorative. The
// budget flag must survive that, because the two concerns landed in this
// one function from two different branches.
func TestLintCommandNamesTheModuleItLints(t *testing.T) {
	for _, dir := range []string{".", ".sparkwing", "tools"} {
		got := lintCommandFor(dir, true)
		if !strings.Contains(got, `cd "`+dir+`"`) {
			t.Errorf("the command does not enter %q: %s", dir, got)
		}
		if !strings.Contains(got, "--allow-parallel-runners") {
			t.Errorf("the per-module command lost the budget flag for %q: %s", dir, got)
		}
	}
}

func TestDescribeLintFailureOnAnExpiredWaitReportsTheWaitNotACause(t *testing.T) {
	got := describeLintFailure(expiredContext(t), 7*time.Second, errors.New("command failed (exit 1)"))

	if !strings.Contains(got, "could not run") {
		t.Fatalf("expired wait did not report could-not-run: %s", got)
	}
	if !strings.Contains(got, "7s") {
		t.Fatalf("expired wait did not report how long it actually waited: %s", got)
	}
	if strings.Contains(got, lintLockWait.String()) {
		t.Fatalf("expired wait quoted the bound rather than the wait it measured: %s", got)
	}
	if strings.Contains(got, "That is contention") {
		t.Fatalf("expired wait asserted a cause it never observed: %s", got)
	}
	if strings.Contains(got, "exit 1") {
		t.Fatalf("expired wait still reported a bare exit code: %s", got)
	}
}

func TestDescribeLintFailureNamesContentionFromTheLinterOwnMessage(t *testing.T) {
	err := &sparkwing.ExecError{
		Command:  "golangci-lint run ./...",
		Stderr:   "Error: parallel golangci-lint is running\n",
		ExitCode: 3,
	}

	got := describeLintFailure(context.Background(), time.Second, err)

	if !strings.Contains(got, "could not run") {
		t.Fatalf("contention signature did not report could-not-run: %s", got)
	}
	if !strings.Contains(got, "contention") {
		t.Fatalf("contention signature did not name contention as the cause: %s", got)
	}
}

func TestDescribeLintFailureReportsRealFindingsUnchanged(t *testing.T) {
	err := &sparkwing.ExecError{
		Command:  "golangci-lint run ./...",
		Stdout:   "main.go:7:2: declared and not used: x (typecheck)\n",
		ExitCode: 1,
	}

	got := describeLintFailure(context.Background(), time.Second, err)

	if strings.Contains(got, "could not run") {
		t.Fatalf("a genuine finding was excused as contention: %s", got)
	}
	if !strings.Contains(got, "golangci-lint:") {
		t.Fatalf("finding lost its golangci-lint attribution: %s", got)
	}
}

// TestRunGolangciLint_AttemptsRestoreFromBlobStoreBeforeLint verifies that
// runGolangciLint sends a GET to the cache endpoint before golangci-lint runs.
// The server returns 404 (no cached blob), so lint proceeds and is allowed to
// fail if the binary is not installed; the assertion is on the GET itself.
func TestRunGolangciLint_AttemptsRestoreFromBlobStoreBeforeLint(t *testing.T) {
	var gets atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/cache/lint-cache-") {
			gets.Add(1)
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)

	t.Setenv("SPARKWING_GITCACHE_URL", srv.URL)
	_ = runGolangciLint(context.Background())

	if gets.Load() == 0 {
		t.Fatal("blob store GET not sent before golangci-lint ran")
	}
}

// expiredContext returns a context whose deadline has already passed,
// which is the state the lint step is in when its bounded wait runs out.
func expiredContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	t.Cleanup(cancel)
	<-ctx.Done()
	return ctx
}
