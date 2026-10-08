package orchestrator

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

const DefaultStoreWedgeBudget = 5 * time.Minute

var storeWedgeBudget = func() (time.Duration, error) { return DefaultStoreWedgeBudget, nil }

// SetTestStoreWedgeBudget makes every store wedge guard this process starts
// take budget, or fail to start with err, until the test ends.
func SetTestStoreWedgeBudget(t interface{ Cleanup(func()) }, budget time.Duration, err error) {
	original := storeWedgeBudget
	t.Cleanup(func() { storeWedgeBudget = original })
	storeWedgeBudget = func() (time.Duration, error) { return budget, err }
}

type storeWedgeGuard struct {
	budget time.Duration

	now func() time.Time

	firstFailure time.Time

	failures int

	logger *slog.Logger
}

func newStoreWedgeGuard(budget time.Duration) *storeWedgeGuard {
	return &storeWedgeGuard{budget: budget, now: time.Now, logger: slog.Default()}
}

func newStoreWedgeGuardFromEnv() (*storeWedgeGuard, error) {
	budget, err := storeWedgeBudget()
	if err != nil {
		return nil, err
	}
	return newStoreWedgeGuard(budget), nil
}

func (g *storeWedgeGuard) success() {
	g.firstFailure = time.Time{}
	g.failures = 0
}

func (g *storeWedgeGuard) fail(op string, err error) error {
	if g.firstFailure.IsZero() {
		g.firstFailure = g.now()
	}
	g.failures++
	elapsed := g.now().Sub(g.firstFailure)
	if store.IsProtocolErr(err) {
		g.emitWedged(op, "protocol", elapsed)
		return fmt.Errorf("%s: %w -- SQLite's WAL lock range is saturated by another live process and retrying cannot clear it; run `sparkwing queue` to see which runs are holding admission", op, err)
	}
	if g.budget > 0 && elapsed >= g.budget {
		g.emitWedged(op, "budget", elapsed)
		return fmt.Errorf("%s: every store call for %s has failed (%d consecutive failures, budget %s, last error: %w) -- the state database looks wedged by another live process; run `sparkwing queue` to see which runs are holding admission", op, elapsed.Round(time.Second), g.failures, g.budget, err)
	}
	return nil
}

func (g *storeWedgeGuard) emitWedged(op, kind string, elapsed time.Duration) {
	g.logger.Error("store wedged",
		"op", op,
		"kind", kind,
		"elapsed", elapsed.Round(time.Second).String(),
		"failures", g.failures)
}
