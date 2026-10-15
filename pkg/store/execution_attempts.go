package store

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode"
)

const maxExecutionExecutorNameLen = 128

var executorNameEscape = regexp.MustCompile(`\x1b(?:\[[0-?]*[ -/]*[@-~]|\][^\x07]*(?:\x07|\x1b\\)|.)`)

func safeExecutorName(name string) string {
	name = executorNameEscape.ReplaceAllString(name, "")
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, name))
}

type ExecutionAttempt struct {
	RunID             string     `json:"run_id"`
	NodeID            string     `json:"node_id,omitempty"`
	Attempt           int        `json:"attempt"`
	ClaimGeneration   int64      `json:"claim_generation,omitempty"`
	CoordinatorID     string     `json:"-"`
	MembershipID      string     `json:"-"`
	ExecutorKind      string     `json:"executor_kind,omitempty"`
	ExecutorName      string     `json:"executor_name,omitempty"`
	ExecutorID        string     `json:"-"`
	ExecutorLocation  string     `json:"location,omitempty"`
	ExecutionSite     string     `json:"execution_site,omitempty"`
	ExecutionSiteName string     `json:"execution_site_name,omitempty"`
	HolderID          string     `json:"-"`
	ReservationID     string     `json:"-"`
	StartedAt         time.Time  `json:"started_at"`
	FinishedAt        *time.Time `json:"finished_at,omitempty"`
	Outcome           string     `json:"outcome,omitempty"`
	FailureReason     string     `json:"failure_reason,omitempty"`
	RetryRunID        string     `json:"retry_run_id,omitempty"`
}

// ListNodeExecutionAttempts reads this tenant's persisted job attempts.
func (t *Tenant) ListNodeExecutionAttempts(ctx context.Context, runID, nodeID string) (_ []ExecutionAttempt, err error) {
	tx, err := t.s.beginSnapshotReadTx(ctx)
	if err != nil {
		return nil, err
	}
	defer rollbackOrLog(tx)
	if _, err := readNodeTx(ctx, tx, t.team, NodeRef{runID, nodeID}); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT attempt,generation,holder,executor_kind,executor_name,executor_location,started_at,finished_at,outcome,failure_reason FROM node_attempts WHERE team=? AND run_id=? AND node_id=? ORDER BY attempt`, t.team, runID, nodeID)
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
	out := []ExecutionAttempt{}
	for rows.Next() {
		item := ExecutionAttempt{RunID: runID, NodeID: nodeID}
		var started int64
		var finished sql.NullInt64
		if err := rows.Scan(&item.Attempt, &item.ClaimGeneration, &item.HolderID, &item.ExecutorKind, &item.ExecutorName, &item.ExecutorLocation, &started, &finished, &item.Outcome, &item.FailureReason); err != nil {
			return nil, err
		}
		item.StartedAt, item.FinishedAt = time.Unix(0, started).UTC(), stateTime(finished)
		out = append(out, item)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	return out, tx.Commit()
}
