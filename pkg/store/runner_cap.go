package store

import (
	"context"
	"database/sql"
	"log/slog"
	"math"
	"time"
)

// RunnerCap is the concurrent-runner ceiling one metered principal is held to
// and the paid credit that raised it.
//
// The ceiling scales with what the controller loaded recently, so a customer
// that has paid for capacity gets it while an account that has paid nothing
// cannot spawn a thousand pods. It is max_concurrent_runners plus one
// runner_scale_base for every runner_scale_step_credits of paid credit
// granted over [RunnerScaleWindow], held under runner_scale_ceiling, which
// defaults to max_global_runners. Every scaling setting is zero by default,
// which leaves max_concurrent_runners exactly as it was.
type RunnerCap struct {
	// Cap is the cloud runners this principal may hold at once. Zero means
	// max_concurrent_runners is unset, so nothing bounds the principal.
	Cap int64
	// RecentPaidMicro is the paid credit granted over [RunnerScaleWindow]
	// less the reversals of those grants, in micro-credits, which is the
	// figure the cap was derived from. It never reads below zero.
	RecentPaidMicro int64
}

type runnerCapEntry struct {
	limits  ComputeLimits
	epoch   uint64
	expires time.Time
	cap     RunnerCap
}

// safety: a reversal is matched to the payment it names rather than to its own
// date, so a refund settled after the window still takes back the payment that
// bought the cap, and a refund of a payment that has aged out changes nothing.
const recentPaidGrantsSQL = `SELECT COALESCE(SUM(g.amount_micro), 0) FROM credit_grants g
  WHERE (g.kind = ? AND g.created_at >= ?)
     OR (g.kind = ? AND g.reverses != '' AND EXISTS (
           SELECT 1 FROM credit_grants p
            WHERE p.kind = ? AND p.created_at >= ? AND p.reference != ''
              AND p.reference = g.reverses))`

func recentPaidGrantsMicro(ctx context.Context, q rowQuerier, now time.Time) (int64, error) {
	var micro int64
	since := now.Add(-RunnerScaleWindow).UnixNano()
	if err := q.QueryRowContext(ctx, recentPaidGrantsSQL,
		CreditGrantPaid, since, CreditGrantReversal, CreditGrantPaid, since).Scan(&micro); err != nil {
		return 0, err
	}
	if micro < 0 {
		return 0, nil
	}
	return micro, nil
}

type storeRowQuerier struct{ s *Store }

func (q storeRowQuerier) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return q.s.queryRow(ctx, query, args...)
}

// perf: a claim reads the ledger only when a scaling setting can move the
// cap.
func (l ComputeLimits) scales() bool {
	return l.ConcurrentRunners > 0 && (l.RunnerScaleStepCredits > 0 || l.RunnerScaleBase > 0)
}

func (l ComputeLimits) runnerCapFrom(paidMicro int64) int64 {
	if l.ConcurrentRunners <= 0 {
		return 0
	}
	base := l.ConcurrentRunners
	if l.RunnerScaleBase > 0 {
		base = l.RunnerScaleBase
	}
	allowed := base
	// safety: the step is divided into the paid credit rather than multiplied
	// up to micro-credits, so a setting an older binary stored past
	// RunnerScaleMaxStepCredits cannot wrap or divide by zero.
	if l.RunnerScaleStepCredits > 0 && paidMicro > 0 {
		allowed = addSteps(base, (paidMicro/MicroCreditsPerCredit)/l.RunnerScaleStepCredits)
	}
	ceiling := l.RunnerScaleCeiling
	if ceiling <= 0 {
		ceiling = l.GlobalRunners
	}
	if ceiling > 0 && allowed > ceiling {
		allowed = ceiling
	}
	// safety: scaling raises the static guard and never tightens it, so a
	// ceiling below max_concurrent_runners cannot refuse work the guard alone
	// would have allowed.
	if allowed < l.ConcurrentRunners {
		allowed = l.ConcurrentRunners
	}
	return allowed
}

// safety: an operator can set a step small enough that the multiplication
// overflows, and a negative cap would refuse every claim.
func addSteps(base, steps int64) int64 {
	if steps <= 0 {
		return base
	}
	if steps > (math.MaxInt64-base)/base {
		return math.MaxInt64
	}
	return base + base*steps
}

// RunnerCapFor reports the concurrent-runner cap every metered principal is
// held to, deriving it from the paid grants of the last [RunnerScaleWindow]
// and caching the result for a minute. A grant clears the cache, so credit
// loaded or reversed takes effect on the next claim rather than a minute
// later.
func (s *Store) RunnerCapFor(ctx context.Context, now time.Time) (RunnerCap, error) {
	limits, err := s.ComputeLimits(ctx)
	if err != nil {
		return RunnerCap{}, err
	}
	return s.runnerCap(ctx, storeRowQuerier{s}, limits, now)
}

func (s *Store) runnerCap(
	ctx context.Context, q rowQuerier, limits ComputeLimits, now time.Time,
) (RunnerCap, error) {
	if !limits.scales() {
		return RunnerCap{Cap: limits.ConcurrentRunners}, nil
	}
	if limits.RunnerScaleStepCredits <= 0 {
		return RunnerCap{Cap: limits.runnerCapFrom(0)}, nil
	}
	cached, epoch, ok := s.cachedRunnerCap(limits, now)
	if ok {
		return cached, nil
	}
	paid, err := recentPaidGrantsMicro(ctx, q, now)
	if err != nil {
		return RunnerCap{}, err
	}
	out := RunnerCap{Cap: limits.runnerCapFrom(paid), RecentPaidMicro: paid}
	s.storeRunnerCap(limits, now, epoch, out)
	return out, nil
}

// safety: the epoch the derivation started under travels back to the write, so
// a grant that lands while the ledger is being read leaves an entry the next
// lookup rejects rather than one that outlives the payment it missed.
func (s *Store) cachedRunnerCap(limits ComputeLimits, now time.Time) (RunnerCap, uint64, bool) {
	s.runnerCapMu.Lock()
	defer s.runnerCapMu.Unlock()
	epoch := s.runnerCapEpoch
	entry := s.runnerCapCache
	if entry.epoch != epoch || entry.limits != limits || !now.Before(entry.expires) {
		return RunnerCap{}, epoch, false
	}
	return entry.cap, epoch, true
}

func (s *Store) storeRunnerCap(limits ComputeLimits, now time.Time, epoch uint64, derived RunnerCap) {
	s.runnerCapMu.Lock()
	defer s.runnerCapMu.Unlock()
	s.runnerCapCache = runnerCapEntry{
		limits: limits, epoch: epoch, expires: now.Add(runnerCapTTL), cap: derived,
	}
}

// safety: a ledger the derivation cannot read must not reach a runner as a
// database error, so the claim meets the static guard it would have been
// measured against and the read failure reaches the operator through the log.
func runnerCapReadRefusal(err error, limits ComputeLimits, principal string) error {
	slog.Warn("compute limits: reading recent paid grants failed; holding the static cap",
		"principal", principal, "cap", limits.ConcurrentRunners, "err", err)
	return &ComputeLimitError{
		Limit: ComputeLimitConcurrentRunners, Cap: limits.ConcurrentRunners,
		Observed: limits.ConcurrentRunners, Scope: "principal " + principal, Principal: principal,
	}
}

// safety: a reversal must not leave a runner holding the cap its payment
// bought, so every ledger write retires the derivation taken before it rather
// than waiting out the minute.
func (s *Store) invalidateRunnerCap() {
	s.runnerCapMu.Lock()
	defer s.runnerCapMu.Unlock()
	s.runnerCapEpoch++
	s.runnerCapCache = runnerCapEntry{}
}
