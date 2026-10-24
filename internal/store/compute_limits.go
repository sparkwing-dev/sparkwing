package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
)

// Compute guards use zero for an unlimited operator ceiling.
const (
	// computeLimitConcurrentRunners caps the cloud runners one team holds
	// at once, counted across every token of the team.
	computeLimitConcurrentRunners = "max_concurrent_runners"
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
	// ComputeLimitRunsPerHour bounds intake per authenticated principal and team.
	ComputeLimitRunsPerHour = "max_runs_per_hour"
	// computeLimitGlobalNodesPerRun caps the nodes any run may carry,
	// whichever principal created it.
	computeLimitGlobalNodesPerRun = "max_global_nodes_per_run"
	// ComputeLimitGlobalRunsPerHour caps the runs the whole controller
	// creates in an hour, whichever principal created them.
	ComputeLimitGlobalRunsPerHour = "max_global_runs_per_hour"
	// ComputeLimitCronSeconds is the shortest interval a cloud schedule may
	// declare, in seconds.
	ComputeLimitCronSeconds = "min_cron_interval_seconds"
)

// failureComputeLimit is the failure reason on a node a compute guard stopped.
const failureComputeLimit = "compute_limit"

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

// ComputeUsage is what the guards are measuring right now: the cloud runners
// claimed, per principal and in total.
type ComputeUsage struct {
	Runners      int64
	ByPrincipal  map[string]int64
	AlarmReached bool
}

var computeLimitFields = map[string]func(*ComputeLimits) *int64{
	computeLimitConcurrentRunners: func(l *ComputeLimits) *int64 { return &l.ConcurrentRunners },
	ComputeLimitGlobalRunners:     func(l *ComputeLimits) *int64 { return &l.GlobalRunners },
	ComputeLimitRunnerAlarm:       func(l *ComputeLimits) *int64 { return &l.RunnerAlarm },
	ComputeLimitRunSeconds:        func(l *ComputeLimits) *int64 { return &l.RunSeconds },
	ComputeLimitNodesPerRun:       func(l *ComputeLimits) *int64 { return &l.NodesPerRun },
	ComputeLimitRunsPerHour:       func(l *ComputeLimits) *int64 { return &l.RunsPerHour },
	computeLimitGlobalNodesPerRun: func(l *ComputeLimits) *int64 { return &l.GlobalNodesPerRun },
	ComputeLimitGlobalRunsPerHour: func(l *ComputeLimits) *int64 { return &l.GlobalRunsPerHour },
	ComputeLimitCronSeconds:       func(l *ComputeLimits) *int64 { return &l.CronSeconds },
}

// ComputeLimitNames returns every guard name, sorted, which is the set
// [Operator.SetComputeLimits] accepts.
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
	return `SELECT key, value FROM settings WHERE key IN (` +
		strings.Join(keys, ",") + `)`, args
}()

func scanComputeLimits(rows rowCursor) (ComputeLimits, error) {
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
func (o *Operator) ComputeLimits(ctx context.Context) (_ ComputeLimits, err error) {
	rows, err := o.s.query(ctx, computeLimitsSQL, computeLimitKeyArgs...)
	if err != nil {
		return ComputeLimits{}, err
	}
	defer closeRowsInto(rows, &err)
	return scanComputeLimits(rows)
}

// ErrInvalidComputeLimitSetting reports a refused compute settings update.
var ErrInvalidComputeLimitSetting = errors.New("compute limits: invalid setting")

func validateComputeLimit(name string, value int64) error {
	if !ValidComputeLimit(name) {
		return fmt.Errorf("%w: unknown guard %q", ErrInvalidComputeLimitSetting, name)
	}
	if value < 0 {
		return fmt.Errorf("%w: %s must not be negative", ErrInvalidComputeLimitSetting, name)
	}
	return nil
}

// SetComputeLimits validates and writes every named guard in one transaction.
// A value of zero removes that ceiling.
func (o *Operator) SetComputeLimits(ctx context.Context, updates map[string]int64) (_ ComputeLimits, err error) {
	var out ComputeLimits
	err = o.s.inTx(ctx, func(tx *storeTx) error {
		var err error
		out, err = setComputeLimitsTx(ctx, tx, updates)
		return err
	})
	return out, err
}

func setComputeLimitsTx(ctx context.Context, tx *storeTx, updates map[string]int64) (ComputeLimits, error) {
	if len(updates) == 0 {
		return ComputeLimits{}, fmt.Errorf("%w: name at least one guard", ErrInvalidComputeLimitSetting)
	}
	for name, value := range updates {
		if err := validateComputeLimit(name, value); err != nil {
			return ComputeLimits{}, err
		}
	}
	// safety: Postgres locks metadata rows as it updates them, so overlapping
	// batches use one key order and cannot wait on each other in reverse.
	for _, name := range ComputeLimitNames() {
		value, ok := updates[name]
		if !ok {
			continue
		}
		if err := setCreditSettingTx(ctx, tx, computeLimitKey(name), formatCreditSetting(value)); err != nil {
			return ComputeLimits{}, err
		}
	}
	return computeLimitsTx(ctx, tx)
}

func computeLimitsTx(ctx context.Context, tx *storeTx) (_ ComputeLimits, err error) {
	rows, err := tx.queryContext(ctx, computeLimitsSQL, computeLimitKeyArgs...)
	if err != nil {
		return ComputeLimits{}, err
	}
	defer closeRowsInto(rows, &err)
	return scanComputeLimits(rows)
}

// ComputeUsage counts the cloud runners claimed now, in total and per
// principal, and reports whether the count reached the alarm.
func (o *Operator) ComputeUsage(ctx context.Context) (_ ComputeUsage, err error) {
	out := ComputeUsage{ByPrincipal: map[string]int64{}}
	rows, err := o.s.query(ctx, `SELECT c.principal, COUNT(*) FROM billing_awards a JOIN credit_charges c ON c.team=a.team AND c.capability_prefix=a.capability_prefix AND c.kind='reservation' WHERE a.metered=1 AND a.unit_kind='node' AND a.settled_at IS NULL GROUP BY c.principal`)
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
	limits, err := o.ComputeLimits(ctx)
	if err != nil {
		return ComputeUsage{}, err
	}
	out.AlarmReached = limits.RunnerAlarm > 0 && out.Runners >= limits.RunnerAlarm
	return out, nil
}

func enforceNodesPerRunTx(ctx context.Context, tx *storeTx, team Team, runID string) error {
	limits, err := computeLimitsTx(ctx, tx)
	if err != nil {
		return err
	}
	if limits.NodesPerRun <= 0 && limits.GlobalNodesPerRun <= 0 {
		return nil
	}
	principal, err := runPrincipalTx(ctx, tx, team, runID)
	if err != nil {
		return err
	}
	ceiling, guard := limits.GlobalNodesPerRun, computeLimitGlobalNodesPerRun
	if limits.NodesPerRun > 0 {
		metered, err := rowPresentTx(ctx, tx, `SELECT 1 FROM capabilities WHERE team=? AND bind_run=? AND issuance='award' AND unit_kind IN ('trigger','plan') AND metered=1 LIMIT 1`, team, runID)
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
	var nodes int64
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM nodes WHERE team = ? AND run_id = ?`, team, runID).Scan(&nodes); err != nil {
		return err
	}
	if nodes >= ceiling {
		refused := &ComputeLimitError{Limit: guard, Cap: ceiling, Observed: nodes, Scope: "run " + runID, Principal: principal}
		return errors.Join(refused, authorityError(apierr.PlanInvalid, refused.Error()))
	}

	return nil
}

func enforceRunsPerHourTx(ctx context.Context, tx *storeTx, team Team, runID string, principal capability.Subject, now time.Time) error {
	limits, err := computeLimitsTx(ctx, tx)
	if err != nil {
		return err
	}
	if limits.RunsPerHour <= 0 && limits.GlobalRunsPerHour <= 0 {
		return nil
	}
	if err := lockComputeGuardsTx(ctx, tx); err != nil {
		return err
	}
	owner, present, err := tx.operator.runOwnerTx(ctx, tx, runID)
	if err != nil {
		return err
	}
	if present {
		if owner != team {
			return fmt.Errorf("%w: run %s", errIDOwnedByAnotherTeam, runID)
		}
		createdBy, err := runPrincipalTx(ctx, tx, team, runID)
		if err != nil || createdBy != "" || principal.ID == "" {
			return err
		}
		return runsPerHourRefusal(ctx, tx, team, ComputeLimits{RunsPerHour: limits.RunsPerHour}, principal, now)
	}
	return runsPerHourRefusal(ctx, tx, team, limits, principal, now)
}

func runsPerHourRefusal(
	ctx context.Context, q *storeTx, team Team, limits ComputeLimits, principal capability.Subject, now time.Time,
) error {
	since := now.Add(-time.Hour).UnixNano()
	if limits.RunsPerHour > 0 && principal.ID != "" {
		kind, id := `json_extract(lineage_json, '$.admission_subject.kind')`, `json_extract(lineage_json, '$.admission_subject.id')`
		if q.dialect == DialectPostgres {
			kind, id = `convert_from(lineage_json,'UTF8')::jsonb->'admission_subject'->>'kind'`, `convert_from(lineage_json,'UTF8')::jsonb->'admission_subject'->>'id'`
		}
		var runs int64
		if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM runs WHERE team=? AND created_at>=? AND `+kind+`=? AND `+id+`=?`, team, since, principal.Kind, principal.ID).Scan(&runs); err != nil {
			return err
		}
		if runs >= limits.RunsPerHour {
			return &ComputeLimitError{Limit: ComputeLimitRunsPerHour, Cap: limits.RunsPerHour, Observed: runs, Scope: "principal " + principal.ID, Principal: principal.ID}
		}
	}

	if limits.GlobalRunsPerHour > 0 {
		return q.operator.globalRunsPerHourRefusal(ctx, q, limits.GlobalRunsPerHour, since)
	}
	return nil
}

func (o *Operator) globalRunsPerHourRefusal(ctx context.Context, q *storeTx, limit, since int64) error {
	var runs int64
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM runs WHERE created_at>=?`, since).Scan(&runs); err != nil {
		return err
	}
	if runs >= limit {
		return &ComputeLimitError{Limit: ComputeLimitGlobalRunsPerHour, Cap: limit, Observed: runs, Scope: "controller"}
	}
	return nil
}

// enforceChildLimitsTx bounds a child's depth and its root's descendants.
// The caller's per-team intake lock serializes the count.
func enforceChildLimitsTx(ctx context.Context, tx *storeTx, team Team, root string, depth int) error {
	if depth > limits.MaxChildDepth {
		return &apierr.Error{Code: apierr.Conflict, Message: fmt.Sprintf("child runs nest at most %d levels deep, counting the root run", limits.MaxChildDepth), Details: map[string]any{"limit": "max_child_depth", "cap": limits.MaxChildDepth}}
	}
	var created, ceiling int64
	if err := tx.QueryRowContext(ctx, `SELECT r.created_at,t.max_children_per_root FROM runs r JOIN teams t ON t.name=r.team WHERE r.team=? AND r.id=?`, team, root).Scan(&created, &ceiling); err != nil {
		return err
	}
	expression := `json_extract(lineage_json, '$.root_run_id')`
	if tx.dialect == DialectPostgres {
		expression = `convert_from(lineage_json,'UTF8')::jsonb->>'root_run_id'`
	}
	var children int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM runs WHERE team=? AND created_at>=? AND COALESCE(parent_run_id,'')<>'' AND retry_of='' AND `+expression+`=?`, team, created, root).Scan(&children); err != nil {
		return err
	}
	if children >= ceiling {
		maximum := strconv.FormatInt(limits.MaxChildrenPerRoot, 10)
		for i := len(maximum) - 3; i > 0; i -= 3 {
			maximum = maximum[:i] + "," + maximum[i:]
		}
		return &apierr.Error{Code: apierr.Conflict, Message: fmt.Sprintf("This team allows %d child runs per root run (maximum %s). Raise the team's child-run limit or split the work into separate runs.", ceiling, maximum), Details: map[string]any{"limit": "max_children_per_root", "cap": ceiling, "observed": children}}
	}
	return nil
}

type rowQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) rowScanner
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

func runPrincipalTx(ctx context.Context, tx *storeTx, team Team, runID string) (string, error) {
	var principal string
	err := tx.QueryRowContext(ctx,
		`SELECT created_principal FROM runs WHERE team = ? AND id = ?`, string(team), runID).Scan(&principal)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return principal, nil
}

// safety: the create-side guards count runs and nodes, which no claim reads, so
// they serialize on a key of their own rather than the ledger's.
func lockComputeGuardsTx(ctx context.Context, tx *storeTx) error {
	if tx.dialect != DialectPostgres {
		return nil
	}
	_, err := tx.execContext(ctx, `SELECT pg_advisory_xact_lock(hashtext(?))`, "sparkwing/compute-guards")
	return err
}
