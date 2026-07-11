package orchestrator

import (
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// StoreWedgeBudgetEnvVar names the environment override for how long a
// store-polling loop tolerates continuously failing store calls before
// it declares the state database wedged and stops. The value is a Go
// duration string ("5m", "90s"). Unset or empty keeps
// [DefaultStoreWedgeBudget]; a zero or negative duration disables the
// budget (a "locking protocol" error stays immediately terminal); an
// unparseable value is a loud error at loop start, never a silent
// fallback -- a typo'd override reverting to the default would hide
// the misconfiguration.
const StoreWedgeBudgetEnvVar = "SPARKWING_STORE_WEDGE_BUDGET"

// DefaultStoreWedgeBudget is the wedge budget applied when
// [StoreWedgeBudgetEnvVar] is unset. Five minutes is several times the
// longest single-statement SQLite wait (the busy_timeout plus the WAL
// retry window), so it never trips on ordinary contention, while a
// genuinely wedged host surfaces within minutes instead of spinning
// until an operator notices.
const DefaultStoreWedgeBudget = 5 * time.Minute

// storeWedgeBudget resolves the wedge budget for one loop start:
// [StoreWedgeBudgetEnvVar] when set and parseable,
// [DefaultStoreWedgeBudget] otherwise. A set-but-unparseable value is
// an error, never a silent fallback.
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

// storeWedgeGuard bounds a store-polling loop. Each tick re-issues
// store statements with a fresh SQLite busy budget, so a loop that
// tolerates per-call failures can spin forever against a database
// wedged by another live process, invisible except for its ticker.
// The loop feeds the guard every store call's outcome; once every
// call has failed continuously for longer than the budget, fail hands
// back a terminal error the loop must return instead of ticking
// again. Not safe for concurrent use; each loop owns its own guard.
type storeWedgeGuard struct {
	// budget is the continuous-failure wall-clock allowance; zero or
	// negative disables the budget trip.
	budget time.Duration
	// now is the clock, injectable for tests.
	now func() time.Time
	// firstFailure is when the current uninterrupted failure streak
	// began; zero when the last store call succeeded.
	firstFailure time.Time
	// failures counts the calls in the current streak, for the
	// terminal error's evidence line and the telemetry event.
	failures int
	// logger receives the one structured event emitted when the guard
	// goes terminal; injectable for tests.
	logger *slog.Logger
}

// newStoreWedgeGuard builds a guard with an explicit budget, for loops
// whose caller already resolved (and error-checked) the environment.
func newStoreWedgeGuard(budget time.Duration) *storeWedgeGuard {
	return &storeWedgeGuard{budget: budget, now: time.Now, logger: slog.Default()}
}

// newStoreWedgeGuardFromEnv resolves the budget from the environment
// and builds the guard, failing loudly on an unparseable override.
func newStoreWedgeGuardFromEnv() (*storeWedgeGuard, error) {
	budget, err := storeWedgeBudget()
	if err != nil {
		return nil, err
	}
	return newStoreWedgeGuard(budget), nil
}

// success resets the failure streak; the loop calls it after any store
// call that completes without error.
func (g *storeWedgeGuard) success() {
	g.firstFailure = time.Time{}
	g.failures = 0
}

// fail records one failed store call. It returns nil while the streak
// is within budget (the loop keeps polling) and a terminal error once
// the loop must stop: immediately for a "locking protocol" error,
// which only clears when the conflicting process goes away, or when
// every call has failed continuously for longer than the budget. The
// terminal error names the condition, the elapsed duration, the last
// store error, and the read-only queue command that locates the wedging
// process. Each terminal verdict also emits one "store wedged"
// structured event -- fields op, kind (budget|protocol), elapsed, and
// failures are a stable interface soak dashboards count.
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

// emitWedged writes the guard's terminal telemetry event.
func (g *storeWedgeGuard) emitWedged(op, kind string, elapsed time.Duration) {
	g.logger.Error("store wedged",
		"op", op,
		"kind", kind,
		"elapsed", elapsed.Round(time.Second).String(),
		"failures", g.failures)
}
