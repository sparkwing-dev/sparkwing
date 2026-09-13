package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Compute guards bound what an install can start before the credit ledger
// bills it. Each guard is one non-negative integer held in sparkwing_meta,
// and zero means unlimited, so an install that sets none behaves as it did
// before the guards existed.
//
// A guard is measured per principal wherever the ticket's team is a principal:
// the runner guards count the cloud runners one principal holds, and the node
// and hourly guards count what one principal's runs carry, applying only when
// that principal holds a metered token. The two global guards are the
// operator's own ceiling and count every run on the controller, metered or
// not.
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
	// ComputeLimitNodesPerRun caps the nodes a metered principal's run may
	// carry, which is what bounds a dynamic fan-out.
	ComputeLimitNodesPerRun = "max_nodes_per_run"
	// ComputeLimitRunsPerHour caps the runs one metered principal creates in
	// an hour, which is what bounds a retry loop and a cron that fires too
	// often.
	ComputeLimitRunsPerHour = "max_runs_per_hour"
	// ComputeLimitGlobalNodesPerRun caps the nodes any run may carry,
	// whichever principal created it.
	ComputeLimitGlobalNodesPerRun = "max_global_nodes_per_run"
	// ComputeLimitGlobalRunsPerHour caps the runs the whole controller
	// creates in an hour, whichever principal created them.
	ComputeLimitGlobalRunsPerHour = "max_global_runs_per_hour"
	// ComputeLimitCronSeconds is the shortest interval a cloud schedule may
	// declare, in seconds.
	ComputeLimitCronSeconds = "min_cron_interval_seconds"
)

// safety: the per-principal guards measure the principal that created a run,
// and nothing else on the row records it.
var runsPrincipalCols = map[string]string{
	"created_principal": "TEXT NOT NULL DEFAULT ''",
}

// safety: the runner counts read every node with an open charge window and the
// hourly count reads one principal's recent runs, so both get an index rather
// than a table scan inside the claim transaction.
const computeGuardIndexes = `
CREATE INDEX IF NOT EXISTS idx_nodes_credit_active ON nodes(credit_charged_through);
CREATE INDEX IF NOT EXISTS idx_nodes_credit_principal ON nodes(claim_principal, credit_charged_through);
CREATE INDEX IF NOT EXISTS idx_runs_created ON runs(created_at);
CREATE INDEX IF NOT EXISTS idx_runs_principal_created ON runs(created_principal, created_at);`

func applyComputeGuardsMigrationSQLite(ctx context.Context, tx *storeTx) error {
	if err := ensureColumnsSQLite(ctx, tx, "runs", runsPrincipalCols); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, computeGuardIndexes)
	return err
}

func applyComputeGuardsMigrationPostgres(ctx context.Context, tx *storeTx) error {
	if err := addColumnsTx(ctx, tx, "runs", runsPrincipalCols); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, computeGuardIndexes)
	return err
}

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
	// Principal is the principal the measurement covered, empty for a guard
	// measured across the controller.
	Principal string
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
	GlobalNodesPerRun int64
	GlobalRunsPerHour int64
	CronSeconds       int64
}

// Any reports whether any guard is set, which is what lets a caller skip the
// work of measuring against guards nobody configured.
func (l ComputeLimits) Any() bool { return l != ComputeLimits{} }

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
	ComputeLimitGlobalNodesPerRun: func(l *ComputeLimits) *int64 { return &l.GlobalNodesPerRun },
	ComputeLimitGlobalRunsPerHour: func(l *ComputeLimits) *int64 { return &l.GlobalRunsPerHour },
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

// safety: one row per guard read one at a time is nine point-reads on the
// claim path, so every guard is read in this one statement instead.
var computeLimitsSQL, computeLimitKeyArgs = func() (string, []any) {
	names := ComputeLimitNames()
	args := make([]any, 0, len(names))
	keys := make([]string, 0, len(names))
	for _, name := range names {
		args = append(args, computeLimitKey(name))
		keys = append(keys, "?")
	}
	return `SELECT key, value FROM sparkwing_meta WHERE key IN (` +
		strings.Join(keys, ",") + `)`, args
}()

func scanComputeLimits(rows *sql.Rows) (ComputeLimits, error) {
	var out ComputeLimits
	for rows.Next() {
		var key, raw string
		if err := rows.Scan(&key, &raw); err != nil {
			return ComputeLimits{}, err
		}
		name := strings.TrimPrefix(key, "compute_limit_")
		field, ok := computeLimitFields[name]
		if !ok {
			continue
		}
		*field(&out) = parseCreditSetting(raw, 0)
	}
	return out, rows.Err()
}

// ComputeLimits reads every guard. A guard nothing set reads zero, which is
// unlimited.
func (s *Store) ComputeLimits(ctx context.Context) (_ ComputeLimits, err error) {
	rows, err := s.query(ctx, computeLimitsSQL, computeLimitKeyArgs...)
	if err != nil {
		return ComputeLimits{}, err
	}
	defer closeRowsInto(rows, &err)
	return scanComputeLimits(rows)
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

func computeLimitsTx(ctx context.Context, tx *storeTx) (_ ComputeLimits, err error) {
	rows, err := tx.QueryContext(ctx, computeLimitsSQL, computeLimitKeyArgs...)
	if err != nil {
		return ComputeLimits{}, err
	}
	defer closeRowsInto(rows, &err)
	return scanComputeLimits(rows)
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

// ComputeAlarmState reports the cloud runners claimed now against the alarm,
// which is how a caller logs the crossing once rather than on every claim. It
// returns a zero alarm and counts nothing when no alarm is set.
func (s *Store) ComputeAlarmState(ctx context.Context) (runners, alarm int64, err error) {
	alarm, err = s.creditSetting(ctx, computeLimitKey(ComputeLimitRunnerAlarm), 0)
	if err != nil || alarm <= 0 {
		return 0, 0, err
	}
	if err := s.queryRow(ctx,
		`SELECT COUNT(*) FROM nodes WHERE credit_charged_through > 0`).Scan(&runners); err != nil {
		return 0, 0, err
	}
	return runners, alarm, nil
}

// safety: the guards run inside the claim's own transaction, under the ledger
// lock the reservation already holds, so two runners polling at once cannot
// both read a count below the cap and both claim against it.
func enforceClaimComputeLimitsTx(
	ctx context.Context, tx *storeTx, limits ComputeLimits, claimant ClaimIdentity, runID string, now time.Time,
) error {
	if limits.GlobalRunners > 0 {
		var total int64
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM nodes WHERE credit_charged_through > 0`).Scan(&total); err != nil {
			return err
		}
		if total >= limits.GlobalRunners {
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
				Observed: held, Scope: "principal " + claimant.Principal, Principal: claimant.Principal,
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
				Observed: elapsed, Scope: "run " + runID, Principal: claimant.Principal,
			}
		}
	}
	return nil
}

func runElapsedSecondsTx(ctx context.Context, tx *storeTx, runID string, now time.Time) (int64, error) {
	var started int64
	err := tx.QueryRowContext(ctx, `SELECT started_at FROM runs WHERE id = ?`, runID).Scan(&started)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if started <= 0 {
		return 0, nil
	}
	elapsed := now.UnixNano() - started
	if elapsed < 0 {
		return 0, nil
	}
	return elapsed / int64(time.Second), nil
}

// safety: a re-created row is the caller guaranteeing the row exists before it
// writes a terminal status, so it is measured against nothing; only a row the
// statement would actually insert counts.
func enforceNodesPerRunTx(ctx context.Context, tx *storeTx, runID, nodeID string) error {
	limits, err := computeLimitsTx(ctx, tx)
	if err != nil {
		return err
	}
	if limits.NodesPerRun <= 0 && limits.GlobalNodesPerRun <= 0 {
		return nil
	}
	present, err := rowPresentTx(ctx, tx,
		`SELECT 1 FROM nodes WHERE run_id = ? AND node_id = ?`, runID, nodeID)
	if err != nil || present {
		return err
	}
	principal, err := runPrincipalTx(ctx, tx, runID)
	if err != nil {
		return err
	}
	ceiling, guard := limits.GlobalNodesPerRun, ComputeLimitGlobalNodesPerRun
	if limits.NodesPerRun > 0 {
		metered, err := principalMetered(ctx, tx, principal)
		if err != nil {
			return err
		}
		if metered && (ceiling == 0 || limits.NodesPerRun < ceiling) {
			ceiling, guard = limits.NodesPerRun, ComputeLimitNodesPerRun
		}
	}
	if ceiling <= 0 {
		return nil
	}
	if err := lockComputeGuardsTx(ctx, tx); err != nil {
		return err
	}
	var nodes int64
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM nodes WHERE run_id = ?`, runID).Scan(&nodes); err != nil {
		return err
	}
	if nodes >= ceiling {
		return &ComputeLimitError{
			Limit: guard, Cap: ceiling, Observed: nodes,
			Scope: "run " + runID, Principal: principal,
		}
	}
	return nil
}

func enforceRunsPerHourTx(ctx context.Context, tx *storeTx, runID, principal string, now time.Time) error {
	limits, err := computeLimitsTx(ctx, tx)
	if err != nil {
		return err
	}
	if limits.RunsPerHour <= 0 && limits.GlobalRunsPerHour <= 0 {
		return nil
	}
	present, err := rowPresentTx(ctx, tx, `SELECT 1 FROM runs WHERE id = ?`, runID)
	if err != nil || present {
		return err
	}
	if err := lockComputeGuardsTx(ctx, tx); err != nil {
		return err
	}
	return runsPerHourRefusal(ctx, tx, limits, principal, now)
}

func runsPerHourRefusal(
	ctx context.Context, q rowQuerier, limits ComputeLimits, principal string, now time.Time,
) error {
	since := now.Add(-time.Hour).UnixNano()
	if limits.RunsPerHour > 0 {
		metered, err := principalMetered(ctx, q, principal)
		if err != nil {
			return err
		}
		if metered {
			var runs int64
			if err := q.QueryRowContext(ctx,
				`SELECT COUNT(*) FROM runs WHERE created_principal = ? AND created_at >= ?`,
				principal, since).Scan(&runs); err != nil {
				return err
			}
			if runs >= limits.RunsPerHour {
				return &ComputeLimitError{
					Limit: ComputeLimitRunsPerHour, Cap: limits.RunsPerHour, Observed: runs,
					Scope: "principal " + principal, Principal: principal,
				}
			}
		}
	}
	if limits.GlobalRunsPerHour > 0 {
		var runs int64
		if err := q.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM runs WHERE created_at >= ?`, since).Scan(&runs); err != nil {
			return err
		}
		if runs >= limits.GlobalRunsPerHour {
			return &ComputeLimitError{
				Limit: ComputeLimitGlobalRunsPerHour, Cap: limits.GlobalRunsPerHour,
				Observed: runs, Scope: "controller",
			}
		}
	}
	return nil
}

type rowQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func rowPresentTx(ctx context.Context, q rowQuerier, query string, args ...any) (bool, error) {
	var one int
	err := q.QueryRowContext(ctx, query, args...).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func runPrincipalTx(ctx context.Context, tx *storeTx, runID string) (string, error) {
	var principal string
	err := tx.QueryRowContext(ctx,
		`SELECT created_principal FROM runs WHERE id = ?`, runID).Scan(&principal)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return principal, nil
}

// safety: metering follows the operator's token marker, so a principal holding
// no metered token is local work the per-principal guards leave alone.
func principalMetered(ctx context.Context, q rowQuerier, principal string) (bool, error) {
	if principal == "" {
		return false, nil
	}
	return rowPresentTx(ctx, q,
		`SELECT 1 FROM tokens WHERE principal = ? AND metered != 0 LIMIT 1`, principal)
}

// safety: the create-side guards count runs and nodes, which no claim reads, so
// they serialize on a key of their own rather than the ledger's. A caller that
// also takes the executor eligibility lock takes that one first, which is the
// store's one lock order: executor, then a guard or ledger key.
func lockComputeGuardsTx(ctx context.Context, tx *storeTx) error {
	if tx.dialect != DialectPostgres {
		return nil
	}
	_, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext(?))`, "sparkwing/compute-guards")
	return err
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

// OldestWaitingReadyNodeForPrincipal names the ready node a claim by this
// principal would have been given, so a refusal is recorded against a run that
// principal owns rather than one it cannot see. It returns empty strings when
// that principal has nothing waiting.
func (s *Store) OldestWaitingReadyNodeForPrincipal(
	ctx context.Context, principal string,
) (runID, nodeID string, err error) {
	if principal == "" {
		return "", "", nil
	}
	err = s.queryRow(ctx, `SELECT run_id, node_id FROM nodes
	  WHERE ready_at IS NOT NULL AND claimed_by IS NULL AND `+nodeNotDone+`
	    AND run_id IN (SELECT id FROM runs WHERE created_principal = ?)
	  ORDER BY ready_at ASC LIMIT 1`, principal).Scan(&runID, &nodeID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", nil
	}
	if err != nil {
		return "", "", err
	}
	return runID, nodeID, nil
}

type creatingPrincipalKey struct{}

// WithCreatingPrincipal names the authenticated principal creating a run, so
// the per-principal guards measure the caller rather than the controller. A
// context without one creates runs that belong to no principal, which is what
// local work does.
func WithCreatingPrincipal(ctx context.Context, principal string) context.Context {
	return context.WithValue(ctx, creatingPrincipalKey{}, principal)
}

func creatingPrincipal(ctx context.Context) string {
	principal, _ := ctx.Value(creatingPrincipalKey{}).(string)
	return principal
}
