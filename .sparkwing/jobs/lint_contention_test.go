package jobs

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestLintCommandWaitsForTheBoxWideLockInsteadOfFailingOnIt(t *testing.T) {
	got := lintCommandFor(false)
	if !strings.Contains(got, "--allow-serial-runners") {
		t.Fatalf("the gate would fail on a neighboring lint instead of waiting for it: %s", got)
	}
}

func TestLintCommandNeverDropsTheToolLockWithoutABudget(t *testing.T) {
	withoutBudget := lintCommandFor(false)
	if strings.Contains(withoutBudget, "--allow-parallel-runners") {
		t.Fatalf("lint dropped its own lock while holding no budget: %s", withoutBudget)
	}

	withBudget := lintCommandFor(true)
	if !strings.Contains(withBudget, "--allow-parallel-runners") {
		t.Fatalf("lint kept the serializing lock while holding a budget, so the budget buys nothing: %s", withBudget)
	}
	if strings.Contains(withBudget, "--allow-serial-runners") {
		t.Fatalf("lint passed both runner flags: %s", withBudget)
	}
}

func TestLinkedWorktreeLeasesReusableLintPath(t *testing.T) {
	worktree := t.TempDir()
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: elsewhere"), 0o600); err != nil {
		t.Fatal(err)
	}
	previous := sparkwing.WorkDir()
	sparkwing.SetWorkDir(worktree)
	t.Cleanup(func() { sparkwing.SetWorkDir(previous) })

	if !shouldLeaseLintPath("") {
		t.Fatal("a disposable linked worktree would keep paying for a private cold cache")
	}
	if shouldLeaseLintPath("https://cache.invalid") {
		t.Fatal("a blob-backed worktree would restore a cache directory lint does not read")
	}
}

func TestFixedCheckoutDoesNotNeedLintAlias(t *testing.T) {
	checkout := t.TempDir()
	if err := os.Mkdir(filepath.Join(checkout, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	previous := sparkwing.WorkDir()
	sparkwing.SetWorkDir(checkout)
	t.Cleanup(func() { sparkwing.SetWorkDir(previous) })

	if shouldLeaseLintPath("") {
		t.Fatal("a fixed checkout does not need a canonical alias")
	}
}

func TestLintSlotCostIsAdmissibleOnThisBox(t *testing.T) {
	cost, capacity := lintSlotCost(), lintBudget.Limit().Capacity
	if cost < 1 {
		t.Fatalf("lint draws no budget, so any number of lints would be admitted: cost %d", cost)
	}
	if cost > capacity {
		t.Fatalf("lint cost %d exceeds budget capacity %d, so it could never be admitted", cost, capacity)
	}
}

func TestLintBudgetIsBoxScoped(t *testing.T) {
	if got := lintBudget.Limit().Scope; got != sparkwing.ScopeBox {
		t.Fatalf("lint budget scope is %q, so it does not bound the machine the tool lock bounded", got)
	}
	if got := lintBudget.Limit().OnLimit; got != sparkwing.Queue {
		t.Fatalf("lint budget on-limit is %q, so a contended gate fails instead of waiting", got)
	}
}

func TestLintCostIsPricedForTheColdRunNotTheWarmOne(t *testing.T) {
	if lintCoreCost < measuredColdCoreDemand {
		t.Fatalf("lint is priced at %.2f cores against a cold run measured at %.2f, so a full "+
			"budget of concurrent lints demands more cores than the box has",
			lintCoreCost, measuredColdCoreDemand)
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

func TestRunGolangciLint_AttemptsRestoreFromBlobStoreBeforeLint(t *testing.T) {
	var gets atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/cache/lint-cache-") {
			gets.Add(1)
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	binDir := t.TempDir()
	linter := filepath.Join(binDir, "golangci-lint")
	if err := os.WriteFile(linter, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	t.Setenv("SPARKWING_GITCACHE_URL", srv.URL)
	_ = runGolangciLint(context.Background())

	if gets.Load() == 0 {
		t.Fatal("blob store GET not sent before golangci-lint ran")
	}
}

func expiredContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	t.Cleanup(cancel)
	<-ctx.Done()
	return ctx
}
