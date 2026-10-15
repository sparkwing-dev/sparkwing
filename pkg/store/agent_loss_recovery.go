package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"
)

const agentLossAvoidWindow = 15 * time.Second

type AgentLossRecovery struct {
	RunID       string
	NodeID      string
	Started     bool
	Invocations int
}

type expiredAgentNode struct {
	runID, nodeID                                         string
	coordinatorID, executorKind, executorName, executorID string
	executorLocation                                      string
	membershipID, reservationID                           string
	requiredCoordinatorID, requiredLocation               string
	started                                               bool
	chargeWindowOpen                                      bool
	invocations                                           int
	leaseNS                                               int64
	tokenPrefix                                           string
}

func (s *Store) recoverExpiredNodeClaims(ctx context.Context) (out []AgentLossRecovery, err error) {
	now := time.Now()
	defer func() {
		for _, pair := range s.expireDispatchClaimsLogged(ctx, now) {
			out = append(out, AgentLossRecovery{RunID: pair[0], NodeID: pair[1], Started: true})
		}
	}()
	tx, err := s.beginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	// safety: the runs are locked before their nodes, in the order
	// lockTeamRunRowTx names; a claim that expires between the two reads, or
	// past this pass's batch, is left to the next pass.
	runIDs, err := expiredClaimRunsTx(ctx, tx, now)
	if err != nil || len(runIDs) == 0 {
		return nil, err
	}
	for _, runID := range runIDs {
		if err := lockRunRow(ctx, tx, runID); err != nil {
			return nil, err
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT run_id, node_id, coordinator_id, executor_kind, claim_worker_id, executor_id, executor_location,
	       claim_membership_id, reservation_id, required_coordinator_id, required_executor_location,
	       execution_started_at, attempts_consumed, credit_charged_through, lease_expires_at, claim_token_prefix
 FROM nodes
 WHERE claimed_by IS NOT NULL AND lease_expires_at IS NOT NULL
   AND lease_expires_at < ? AND kind = '' AND `+nodeNotDone+` AND run_id IN (`+placeholders(len(runIDs))+`)`+s.forUpdate(),
		append([]any{now.UnixNano()}, stringsToAny(runIDs)...)...)
	if err != nil {
		return nil, err
	}
	byRun := map[string][]expiredAgentNode{}
	var runOrder []string
	for rows.Next() {
		var item expiredAgentNode
		var started sql.NullInt64
		var chargeWindow int64
		if err := rows.Scan(&item.runID, &item.nodeID, &item.coordinatorID, &item.executorKind,
			&item.executorName, &item.executorID, &item.executorLocation, &item.membershipID, &item.reservationID,
			&item.requiredCoordinatorID, &item.requiredLocation, &started, &item.invocations, &chargeWindow,
			&item.leaseNS, &item.tokenPrefix); err != nil {
			_ = rows.Close()
			return nil, err
		}
		item.started = started.Valid
		item.chargeWindowOpen = chargeWindow != 0
		if _, ok := byRun[item.runID]; !ok {
			runOrder = append(runOrder, item.runID)
		}
		byRun[item.runID] = append(byRun[item.runID], item)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return nil, err
	}

	needsLedger := false
	for _, items := range byRun {
		for _, item := range items {
			needsLedger = needsLedger || item.chargeWindowOpen
		}
	}
	if needsLedger {
		if err := lockCreditLedgerTx(ctx, tx); err != nil {
			return nil, err
		}
	}

	var recovered []AgentLossRecovery
	for _, runID := range runOrder {
		items := byRun[runID]
		for _, item := range items {
			if item.chargeWindowOpen {
				if err := s.settleExpiredClaimTx(ctx, tx, expiredClaim{
					runID: item.runID, nodeID: item.nodeID, tokenPrefix: item.tokenPrefix, leaseNS: item.leaseNS,
				}, now.UnixNano()); err != nil {
					return nil, err
				}
			}
			var avoidUntil any
			if item.executorID != "" {
				avoidUntil = now.Add(agentLossAvoidWindow).UnixNano()
			}
			if _, err := tx.ExecContext(ctx, `UPDATE nodes
   SET `+nodeFailSet+`, error = 'runner heartbeat expired', failure_reason = ?, finished_at = ?,
       avoid_coordinator_id = ?, avoid_executor_kind = ?, avoid_executor_id = ?, avoid_until = ?,
       claimed_by = NULL, claim_principal = '', claim_token_prefix = '',
       claim_executor = '', claim_cores = 0, claim_memory_bytes = 0,
       claim_reservation = '', claim_slot = -1, lease_expires_at = NULL,
       ready_at = NULL, placement_hold_from = NULL, offer_started_at = NULL, reservation_id = '',
       credit_charged_through = 0
 WHERE run_id = ? AND node_id = ? AND `+nodeNotDone,
				FailureAgentLost, now.UnixNano(), item.coordinatorID, item.executorKind,
				item.executorID, avoidUntil, runID, item.nodeID); err != nil {
				return nil, err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE node_execution_attempts
   SET finished_at = COALESCE(finished_at, ?), outcome = CASE WHEN finished_at IS NULL THEN 'failed' ELSE outcome END,
       failure_reason = CASE WHEN finished_at IS NULL THEN ? ELSE failure_reason END
 WHERE run_id = ? AND node_id = ? AND finished_at IS NULL`,
				now.UnixNano(), FailureAgentLost, runID, item.nodeID); err != nil {
				return nil, err
			}
			event := executionAttributionEventFields(item.executorKind, item.executorName, item.executorLocation)
			event["execution_started"] = item.started
			event["invocations"] = item.invocations
			payload, _ := json.Marshal(event)
			if _, err := appendRunEventTx(ctx, tx, runID, item.nodeID, "agent_lease_lost", payload, now); err != nil {
				return nil, err
			}
			recovered = append(recovered, AgentLossRecovery{
				RunID: runID, NodeID: item.nodeID, Started: item.started, Invocations: item.invocations,
			})
		}

	}
	if len(recovered) == 0 {
		return nil, nil
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return recovered, nil
}

// safety: the runs a pass locks become one IN list of bind parameters, so a
// pass takes a bounded batch and the next pass continues where it stopped.
const maxExpiredClaimRunsPerPass = 500

func expiredClaimRunsTx(ctx context.Context, tx *storeTx, now time.Time) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT run_id FROM nodes
 WHERE claimed_by IS NOT NULL AND lease_expires_at IS NOT NULL
   AND lease_expires_at < ? AND kind = '' AND `+nodeNotDone+` ORDER BY run_id LIMIT ?`, now.UnixNano(), maxExpiredClaimRunsPerPass)
	if err != nil {
		return nil, err
	}
	defer closeRowsOrLog(rows)
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func placeholders(n int) string {
	if n < 1 {
		return ""
	}
	return strings.TrimRight(strings.Repeat("?,", n), ",")
}

func stringsToAny(values []string) []any {
	out := make([]any, len(values))
	for i := range values {
		out[i] = values[i]
	}
	return out
}
