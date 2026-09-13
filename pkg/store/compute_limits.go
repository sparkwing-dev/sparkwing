package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"
)

// Compute guards bound what an install can start before the credit ledger
// bills it. Each guard is one non-negative integer held in sparkwing_meta,
// and zero means unlimited, so an install that sets none behaves as it did
// before the guards existed.
//
// The runner guards count cloud runners, which are the claims a metered
// token holds: a node carries a charge window exactly while a metered claim
// is paying for it. The run, node, cron and rate guards count every run on
// the controller, because a bug that fans out ten thousand nodes costs the
// same whichever token claimed them.
const (
	// ComputeLimitConcurrentRunners caps the cloud runners one principal
	// holds at once. Each principal is measured on its own.
	ComputeLimitConcurrentRunners = "max_concurrent_runners"
	// ComputeLimitGlobalRunners caps the cloud runners the controller will
	// ever have claimed across every principal.
	ComputeLimitGlobalRunners = "max_global_runners"
	// ComputeLimitRunnerAlarm is the cloud runner count that logs a warning,
	// which is how an operator hears the global ceiling approaching before it
	// refuses anything.
	ComputeLimitRunnerAlarm = "runner_alarm"
	// ComputeLimitRunSeconds caps the wall-clock seconds a run may hold cloud
	// runners for. Past it a claim is refused and the next heartbeat cancels
	// the node.
	ComputeLimitRunSeconds = "max_run_seconds"
	// ComputeLimitNodesPerRun caps the nodes one run may hold, which is what
	// bounds a dynamic fan-out: its members are nodes of the run that
	// generated them.
	ComputeLimitNodesPerRun = "max_nodes_per_run"
	// ComputeLimitRunsPerHour caps the runs created in the hour before a new
	// one, which is what bounds a retry loop and a cron that fires too often.
	ComputeLimitRunsPerHour = "max_runs_per_hour"
	// ComputeLimitCronSeconds is the shortest interval a cloud schedule may
	// declare, in seconds.
	ComputeLimitCronSeconds = "min_cron_interval_seconds"
)

// EventKindComputeLimitBlocked records work a compute guard refused, so a run
// that is waiting or was cut short says which cap it met.
const EventKindComputeLimitBlocked = "compute_limit_blocked"

// FailureComputeLimit is the failure reason on a node a compute guard stopped.
const FailureComputeLimit = "compute_limit"

// ErrComputeLimit is returned when a compute guard refuses work. The refusal
// is a [ComputeLimitError], which names the cap and what was measured
// against it.
var ErrComputeLimit = errors.New("compute limit reached")

// ComputeLimitError refuses work a compute guard caps and names both sides of
// the comparison, so the caller reports which cap stopped it and by how much.
type ComputeLimitError struct {
	// Limit is one of the ComputeLimit names.
	Limit string
	// Cap is the configured ceiling.
	Cap int64
	// Observed is what was measured against the cap.
	Observed int64
	// Scope names what the measurement covered: a principal, a run, or the
	// whole controller.
	Scope string
}

func (e *ComputeLimitError) Error() string {
	scope := e.Scope
	if scope == "" {
		scope = "controller"
	}
	return fmt.Sprintf("compute limit %s reached: %s is at %d, cap %d",
		e.Limit, scope, e.Observed, e.Cap)
}

// Unwrap reports [ErrComputeLimit], so a caller matches the condition with
// errors.Is without knowing this type.
func (e *ComputeLimitError) Unwrap() error { return ErrComputeLimit }

// ComputeLimits is every compute guard the controller enforces. A zero field
// is an unlimited guard.
type ComputeLimits struct {
	ConcurrentRunners int64
	GlobalRunners     int64
	RunnerAlarm       int64
	RunSeconds        int64
	NodesPerRun       int64
	RunsPerHour       int64
	CronSeconds       int64
}

// ComputeUsage is what the guards are measuring right now: the cloud runners
// claimed, per principal and in total.
type ComputeUsage struct {
	Runners      int64
	ByPrincipal  map[string]int64
	AlarmReached bool
}

var computeLimitFields = map[string]func(*ComputeLimits) *int64{
	ComputeLimitConcurrentRunners: func(l *ComputeLimits) *int64 { return &l.ConcurrentRunners },
	ComputeLimitGlobalRunners:     func(l *ComputeLimits) *int64 { return &l.GlobalRunners },
	ComputeLimitRunnerAlarm:       func(l *ComputeLimits) *int64 { return &l.RunnerAlarm },
	ComputeLimitRunSeconds:        func(l *ComputeLimits) *int64 { return &l.RunSeconds },
	ComputeLimitNodesPerRun:       func(l *ComputeLimits) *int64 { return &l.NodesPerRun },
	ComputeLimitRunsPerHour:       func(l *ComputeLimits) *int64 { return &l.RunsPerHour },
	ComputeLimitCronSeconds:       func(l *ComputeLimits) *int64 { return &l.CronSeconds },
}

// ComputeLimitNames returns every guard name, sorted, which is the set
// [Store.SetComputeLimit] accepts.
func ComputeLimitNames() []string {
	out := make([]string, 0, len(computeLimitFields))
	for name := range computeLimitFields {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// ValidComputeLimit reports whether name is a guard this store enforces.
func ValidComputeLimit(name string) bool {
	_, ok := computeLimitFields[name]
	return ok
}

// Value returns one guard by name, and false when the name is not a guard.
func (l ComputeLimits) Value(name string) (int64, bool) {
	field, ok := computeLimitFields[name]
	if !ok {
		return 0, false
	}
	return *field(&l), true
}

func computeLimitKey(name string) string { return "compute_limit_" + name }

// ComputeLimits reads every guard. A guard nothing set reads zero, which is
// unlimited.
func (s *Store) ComputeLimits(ctx context.Context) (ComputeLimits, error) {
	var out ComputeLimits
	for name, field := range computeLimitFields {
		v, err := s.creditSetting(ctx, computeLimitKey(name), 0)
		if err != nil {
			return ComputeLimits{}, err
		}
		*field(&out) = v
	}
	return out, nil
}

// SetComputeLimit sets one guard. A value of zero removes the ceiling.
func (s *Store) SetComputeLimit(ctx context.Context, name string, value int64) error {
	if !ValidComputeLimit(name) {
		return fmt.Errorf("compute limits: unknown guard %q", name)
	}
	if value < 0 {
		return errors.New("compute limits: a guard must not be negative")
	}
	return s.setCreditSetting(ctx, computeLimitKey(name), value)
}

func computeLimitsTx(ctx context.Context, tx *storeTx) (ComputeLimits, error) {
	var out ComputeLimits
	for name, field := range computeLimitFields {
		v, err := creditSettingTx(ctx, tx, computeLimitKey(name), 0)
		if err != nil {
			return ComputeLimits{}, err
		}
		*field(&out) = v
	}
	return out, nil
}

// ComputeUsage counts the cloud runners claimed now, in total and per
// principal, and reports whether the count reached the alarm.
func (s *Store) ComputeUsage(ctx context.Context) (_ ComputeUsage, err error) {
	out := ComputeUsage{ByPrincipal: map[string]int64{}}
	rows, err := s.query(ctx, `SELECT claim_principal, COUNT(*) FROM nodes
	  WHERE credit_charged_through > 0 GROUP BY claim_principal`)
	if err != nil {
		return ComputeUsage{}, err
	}
	defer closeRowsInto(rows, &err)
	for rows.Next() {
		var principal string
		var n int64
		if err := rows.Scan(&principal, &n); err != nil {
			return ComputeUsage{}, err
		}
		out.ByPrincipal[principal] = n
		out.Runners += n
	}
	if err := rows.Err(); err != nil {
		return ComputeUsage{}, err
	}
	limits, err := s.ComputeLimits(ctx)
	if err != nil {
		return ComputeUsage{}, err
	}
	out.AlarmReached = limits.RunnerAlarm > 0 && out.Runners >= limits.RunnerAlarm
	return out, nil
}

// safety: the guards run inside the claim's own transaction for the same
// reason the credit reservation does, so two runners polling at once cannot
// both read a count below the cap and both claim against it.
func enforceClaimComputeLimitsTx(
	ctx context.Context, tx *storeTx, claimant ClaimIdentity, runID string, now time.Time,
) error {
	limits, err := computeLimitsTx(ctx, tx)
	if err != nil {
		return err
	}
	if limits.GlobalRunners > 0 || limits.RunnerAlarm > 0 {
		var total int64
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM nodes WHERE credit_charged_through > 0`).Scan(&total); err != nil {
			return err
		}
		if limits.RunnerAlarm > 0 && total+1 >= limits.RunnerAlarm {
			slog.Warn("cloud runners reached the alarm count",
				"runners", total+1, "alarm", limits.RunnerAlarm, "ceiling", limits.GlobalRunners)
		}
		if limits.GlobalRunners > 0 && total >= limits.GlobalRunners {
			return &ComputeLimitError{
				Limit: ComputeLimitGlobalRunners, Cap: limits.GlobalRunners,
				Observed: total, Scope: "controller",
			}
		}
	}
	if limits.ConcurrentRunners > 0 {
		var held int64
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM nodes WHERE credit_charged_through > 0 AND claim_principal = ?`,
			claimant.Principal).Scan(&held); err != nil {
			return err
		}
		if held >= limits.ConcurrentRunners {
			return &ComputeLimitError{
				Limit: ComputeLimitConcurrentRunners, Cap: limits.ConcurrentRunners,
				Observed: held, Scope: "principal " + claimant.Principal,
			}
		}
	}
	if limits.RunSeconds > 0 {
		elapsed, err := runElapsedSecondsTx(ctx, tx, runID, now)
		if err != nil {
			return err
		}
		if elapsed > limits.RunSeconds {
			return &ComputeLimitError{
				Limit: ComputeLimitRunSeconds, Cap: limits.RunSeconds,
				Observed: elapsed, Scope: "run " + runID,
			}
		}
	}
	return nil
}

func runElapsedSecondsTx(ctx context.Context, tx *storeTx, runID string, now time.Time) (int64, error) {
	var started int64
	err := tx.QueryRowContext(ctx, `SELECT started_at FROM runs WHERE id = ?`, runID).Scan(&started)
	if errors.Is(err, sql.ErrNoRows) || started <= 0 {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	elapsed := now.UnixNano() - started
	if elapsed < 0 {
		return 0, nil
	}
	return elapsed / int64(time.Second), nil
}

// safety: the count is taken in the creating transaction, so a fan-out that
// adds members from several workers at once cannot pass the cap between the
// read and the insert.
func enforceNodesPerRunTx(ctx context.Context, tx *storeTx, runID string) error {
	ceiling, err := creditSettingTx(ctx, tx, computeLimitKey(ComputeLimitNodesPerRun), 0)
	if err != nil || ceiling <= 0 {
		return err
	}
	var nodes int64
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM nodes WHERE run_id = ?`, runID).Scan(&nodes); err != nil {
		return err
	}
	if nodes >= ceiling {
		return &ComputeLimitError{
			Limit: ComputeLimitNodesPerRun, Cap: ceiling, Observed: nodes, Scope: "run " + runID,
		}
	}
	return nil
}

func enforceRunsPerHourTx(ctx context.Context, tx *storeTx, now time.Time) error {
	ceiling, err := creditSettingTx(ctx, tx, computeLimitKey(ComputeLimitRunsPerHour), 0)
	if err != nil || ceiling <= 0 {
		return err
	}
	since := now.Add(-time.Hour).UnixNano()
	var runs int64
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM runs WHERE COALESCE(NULLIF(created_at, 0), started_at) >= ?`,
		since).Scan(&runs); err != nil {
		return err
	}
	if runs >= ceiling {
		return &ComputeLimitError{
			Limit: ComputeLimitRunsPerHour, Cap: ceiling, Observed: runs, Scope: "controller",
		}
	}
	return nil
}

// RunExceedsWallClock reports whether the run has held cloud runners past the
// wall-clock guard, which is what the heartbeat path reads to decide that a
// node must stop. It reports false when no guard is set.
func (s *Store) RunExceedsWallClock(ctx context.Context, runID string, now time.Time) (int64, bool, error) {
	ceiling, err := s.creditSetting(ctx, computeLimitKey(ComputeLimitRunSeconds), 0)
	if err != nil {
		return 0, false, err
	}
	if ceiling <= 0 {
		return 0, false, nil
	}
	var started int64
	err = s.queryRow(ctx, `SELECT started_at FROM runs WHERE id = ?`, runID).Scan(&started)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	if started <= 0 {
		return 0, false, nil
	}
	elapsed := (now.UnixNano() - started) / int64(time.Second)
	return ceiling, elapsed > ceiling, nil
}
