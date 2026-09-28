package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

const (
	nodeKindPlan = "plan"
	nodeKindWork = "work"
)

const (
	outcomeSuccess           = "success"
	outcomeFailed            = "failed"
	outcomeSkipped           = "skipped"
	outcomeCancelled         = "cancelled"
	outcomeSuperseded        = "superseded"
	outcomeSatisfied         = "satisfied"
	outcomeCached            = "cached"
	outcomeSkippedConcurrent = "skipped-concurrent"
)

// safety: mirrors sparkwing.Outcome.OK, which the store cannot import; a
// dependent released on any other outcome would run past a failed upstream.
func outcomeOK(outcome string) bool {
	switch outcome {
	case outcomeSuccess, outcomeSatisfied, outcomeCached, outcomeSkipped, outcomeSkippedConcurrent:
		return true
	}
	return false
}

// safety: every v77 column is defaulted, so a binary predating it keeps writing
// nodes and runs, which it never marks as controller-dispatched.
var controllerDispatchNodeCols = map[string]string{
	"kind":              "TEXT NOT NULL DEFAULT ''",
	"spec_hash":         "TEXT NOT NULL DEFAULT ''",
	"on_failure_of":     "TEXT NOT NULL DEFAULT ''",
	"continue_on_error": "INTEGER NOT NULL DEFAULT 0",
	"optional":          "INTEGER NOT NULL DEFAULT 0",
	"retry_budget":      "INTEGER NOT NULL DEFAULT 0",
	"retry_backoff_ms":  "INTEGER NOT NULL DEFAULT 0",
	"failure_json":      "BLOB",
}

var controllerDispatchRunCols = map[string]string{
	"plan_accepted_generation": "INTEGER NOT NULL DEFAULT 0",
}

func applyControllerDispatchMigration(ctx context.Context, tx *storeTx, postgres bool) error {
	if !postgres {
		if err := ensureColumnsSQLite(ctx, tx, "nodes", controllerDispatchNodeCols); err != nil {
			return err
		}
		return ensureColumnsSQLite(ctx, tx, "runs", controllerDispatchRunCols)
	}
	pg := strings.NewReplacer("INTEGER", "BIGINT", "BLOB", "BYTEA")
	for table, cols := range map[string]map[string]string{
		"nodes": controllerDispatchNodeCols, "runs": controllerDispatchRunCols,
	} {
		converted := make(map[string]string, len(cols))
		for name, def := range cols {
			converted[name] = pg.Replace(def)
		}
		if err := addColumnsTx(ctx, tx, table, converted); err != nil {
			return err
		}
	}
	return nil
}

type settleNode struct {
	id, status, outcome, onFailureOf string
	deps                             []string
	continueOnError, optional        bool
	waiting                          bool
}

// safety: the caller holds the run row (lockDispatchRunTx) and calls this in the
// transaction that made the change, so no transition is visible without the
// releases, skips, cancels and run verdict it implies; with nothing due it
// writes nothing.
func settleTx(ctx context.Context, tx *storeTx, team Team, runID string, now time.Time) error {
	var status string
	err := tx.QueryRowContext(ctx, `SELECT status FROM runs WHERE team = ? AND id = ?`,
		string(team), runID).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return notFound("run", runID)
	}
	if err != nil {
		return err
	}
	if isTerminalRunStatus(status) {
		return nil
	}
	nodes, err := loadSettleNodesTx(ctx, tx, team, runID)
	if err != nil {
		return err
	}
	byID := make(map[string]*settleNode, len(nodes))
	for _, n := range nodes {
		byID[n.id] = n
	}
	var ready []string
	for changed := true; changed; {
		changed = false
		for _, n := range nodes {
			if !n.waiting {
				continue
			}
			verdict, reason := releaseVerdict(n, byID)
			switch verdict {
			case "":
				continue
			case nodeStatusPending:
				ready = append(ready, n.id)
			default:
				if err := finishUnrunNodeTx(ctx, tx, team, runID, n.id, verdict, reason, now); err != nil {
					return err
				}
				n.status, n.outcome = nodeStatusDone, verdict
				changed = true
			}
			n.waiting = false
		}
	}
	for _, id := range ready {
		if _, err := tx.ExecContext(ctx, `UPDATE nodes SET ready_at = ?, placement_hold_from = ?
 WHERE team = ? AND run_id = ? AND node_id = ? AND status = ? AND ready_at IS NULL AND claimed_by IS NULL`,
			now.UnixNano(), now.UnixNano(), string(team), runID, id, nodeStatusPending); err != nil {
			return err
		}
	}
	return finalizeSettledRunTx(ctx, tx, team, runID, nodes)
}

func loadSettleNodesTx(ctx context.Context, tx *storeTx, team Team, runID string) ([]*settleNode, error) {
	rows, err := tx.QueryContext(ctx, `SELECT node_id, status, outcome, deps_json, on_failure_of,
       continue_on_error, optional, ready_at IS NULL AND claimed_by IS NULL
  FROM nodes WHERE team = ? AND run_id = ? ORDER BY seq, node_id`, string(team), runID)
	if err != nil {
		return nil, err
	}
	defer closeRowsOrLog(rows)
	var out []*settleNode
	for rows.Next() {
		n := &settleNode{}
		var deps []byte
		var unreleased bool
		if err := rows.Scan(&n.id, &n.status, &n.outcome, &deps, &n.onFailureOf,
			&n.continueOnError, &n.optional, &unreleased); err != nil {
			return nil, err
		}
		if len(deps) > 0 {
			if err := json.Unmarshal(deps, &n.deps); err != nil {
				return nil, fmt.Errorf("node %s/%s deps: %w", runID, n.id, err)
			}
		}
		n.waiting = n.status == nodeStatusPending && unreleased
		out = append(out, n)
	}
	return out, rows.Err()
}

// safety: returns "" while any upstream is still running, so a node is decided
// once, from terminal upstreams only. A retrying upstream is pending, not
// terminal, which is what keeps its dependents waiting between attempts.
func releaseVerdict(n *settleNode, byID map[string]*settleNode) (string, string) {
	upstream := n.deps
	if n.onFailureOf != "" {
		upstream = append(slices.Clone(n.deps), n.onFailureOf)
	}
	for _, id := range upstream {
		if dep, ok := byID[id]; !ok || dep.status != nodeStatusDone {
			return "", ""
		}
	}
	if n.onFailureOf != "" {
		if parent := byID[n.onFailureOf]; parent.outcome != outcomeFailed {
			return outcomeSkipped, fmt.Sprintf("parent %q did not fail (outcome=%s)", parent.id, parent.outcome)
		}
	}
	for _, id := range n.deps {
		if dep := byID[id]; !outcomeOK(dep.outcome) && !dep.continueOnError {
			return outcomeCancelled, "upstream-failed"
		}
	}
	return nodeStatusPending, ""
}

func finishUnrunNodeTx(ctx context.Context, tx *storeTx, team Team, runID, nodeID, outcome, reason string, now time.Time) error {
	_, err := tx.ExecContext(ctx, `UPDATE nodes SET status = ?, outcome = ?, error = ?, finished_at = ?
 WHERE team = ? AND run_id = ? AND node_id = ? AND status = ?`,
		nodeStatusDone, outcome, reason, now.UnixNano(), string(team), runID, nodeID, nodeStatusPending)
	return err
}

func finalizeSettledRunTx(ctx context.Context, tx *storeTx, team Team, runID string, nodes []*settleNode) error {
	if len(nodes) == 0 {
		return nil
	}
	var failed, cancelled, superseded []string
	for _, n := range nodes {
		if n.status != nodeStatusDone {
			return nil
		}
		if n.optional || outcomeOK(n.outcome) {
			continue
		}
		switch n.outcome {
		case outcomeSuperseded:
			superseded = append(superseded, n.id)
		case outcomeCancelled:
			cancelled = append(cancelled, n.id)
		default:
			failed = append(failed, n.id)
		}
	}
	status, msg := "success", ""
	switch {
	case len(failed) > 0 || len(cancelled) > 0:
		status = runStatusFailed
		var parts []string
		if len(failed) > 0 {
			parts = append(parts, "failed: "+strings.Join(failed, ", "))
		}
		if len(cancelled) > 0 {
			parts = append(parts, "cancelled: "+strings.Join(cancelled, ", "))
		}
		msg = strings.Join(parts, "; ")
	case len(superseded) > 0:
		status, msg = runStatusCancelled, "superseded: "+strings.Join(superseded, ", ")
	}
	return finishRunOnceTx(ctx, tx, runID, status, msg, team)
}

// safety: every dispatch transition locks the run row before any node row, so
// siblings finishing at once serialize on the run instead of deadlocking; the
// shared eligibility lock comes first because the credit ledger, taken last,
// must follow it everywhere.
func lockDispatchRunTx(ctx context.Context, tx *storeTx, team Team, runID string) error {
	if err := lockExecutorEligibilityTx(ctx, tx, false); err != nil {
		return err
	}
	var id string
	err := tx.QueryRowContext(ctx, `SELECT id FROM runs WHERE team = ? AND id = ?`+tx.forNoKeyUpdate(),
		string(team), runID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return notFound("run", runID)
	}
	return err
}
