package orchestrator

import (
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

const StoreWedgeBudgetEnvVar = "SPARKWING_STORE_WEDGE_BUDGET"

const DefaultStoreWedgeBudget = 5 * time.Minute

func storeWedgeBudget() (time.Duration, error) {
	raw := os.Getenv(StoreWedgeBudgetEnvVar)
	if raw == "" {
		return DefaultStoreWedgeBudget, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s=%q: want a Go duration such as \"5m\"", StoreWedgeBudgetEnvVar, raw)
	}
	return d, nil
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
