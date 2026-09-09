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
		t.Fatalf("lint kept the serializing lock while holding a budget: %s", withBudget)
	}
	if strings.Contains(withBudget, "--allow-serial-runners") {
		t.Fatalf("lint passed both runner flags: %s", withBudget)
	}
}

func TestLintSlotCostIsAdmissibleOnThisBox(t *testing.T) {
	cost, capacity := lintSlotCost(), lintBudget.Limit().Capacity
	if cost < 1 {
		t.Fatalf("lint cost = %d, want positive cost", cost)
	}
	if cost > capacity {
		t.Fatalf("lint cost %d exceeds budget capacity %d", cost, capacity)
	}
}

func TestLintBudgetIsBoxScoped(t *testing.T) {
	if got := lintBudget.Limit().Scope; got != sparkwing.ScopeBox {
		t.Fatalf("lint budget scope = %q, want box scope", got)
	}
	if got := lintBudget.Limit().OnLimit; got != sparkwing.Queue {
		t.Fatalf("lint budget on-limit = %q, want queue", got)
	}
}

func TestLintCostIsPricedForTheColdRunNotTheWarmOne(t *testing.T) {
	if lintCoreCost < measuredColdCoreDemand {
		t.Fatalf("lint cost %.2f cores is below measured demand %.2f",
			lintCoreCost, measuredColdCoreDemand)
	}
}

func TestDescribeLintFailureOnAnExpiredWaitReportsTheWaitNotACause(t *testing.T) {
	got := describeLintFailure(expiredContext(t), 7*time.Second, errors.New("command failed (exit 1)"))

	if !strings.Contains(got, "no result before the deadline") {
		t.Fatalf("expired wait did not report the deadline: %s", got)
	}
	if !strings.Contains(got, "7s") {
		t.Fatalf("expired wait did not report how long it actually waited: %s", got)
	}
	if strings.Contains(got, lintLockWait.String()) {
		t.Fatalf("expired wait quoted the bound rather than the wait it measured: %s", got)
	}
	if strings.Contains(got, "lock") {
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

	if !strings.Contains(got, "another process holds the machine-wide lock") {
		t.Fatalf("contention diagnostic lost the observed lock: %s", got)
	}
}

func TestDescribeLintFailureReportsRealFindingsUnchanged(t *testing.T) {
	err := &sparkwing.ExecError{
		Command:  "golangci-lint run ./...",
		Stdout:   "main.go:7:2: declared and not used: x (typecheck)\n",
		ExitCode: 1,
	}

	got := describeLintFailure(context.Background(), time.Second, err)

	if strings.Contains(got, "machine-wide lock") {
		t.Fatalf("a genuine finding was excused as contention: %s", got)
	}
	if !strings.Contains(got, "golangci-lint:") {
		t.Fatalf("finding lost its golangci-lint attribution: %s", got)
	}
}

func TestRunGolangciLint_AttemptsRestoreFromBlobStore(t *testing.T) {
	var getRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet && strings.HasPrefix(request.URL.Path, "/cache/lint-cache-") {
			getRequests.Add(1)
		}
		http.NotFound(response, request)
	}))
	t.Cleanup(server.Close)
	binaryDirectory := t.TempDir()
	linter := filepath.Join(binaryDirectory, "golangci-lint")
	if err := os.WriteFile(linter, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binaryDirectory+string(os.PathListSeparator)+os.Getenv("PATH"))

	t.Setenv("SPARKWING_GITCACHE_URL", server.URL)
	_ = runGolangciLint(context.Background())

	if getRequests.Load() == 0 {
		t.Fatal("blob store GET was not attempted")
	}
}

func expiredContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	t.Cleanup(cancel)
	<-ctx.Done()
	return ctx
}
