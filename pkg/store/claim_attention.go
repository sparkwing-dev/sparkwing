package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

// safety: zero takes [match.DefaultClaimWait], so a run an older binary wrote
// waits exactly as long as any other.
var claimAttentionRunCols = map[string]string{
	"claim_wait_ns": "INTEGER NOT NULL DEFAULT 0",
}

var claimAttentionNodeCols = map[string]string{
	"attention_reason": "TEXT NOT NULL DEFAULT ''",
}

// safety: NULL is an agent that has not claimed since the column landed, which
// matches no selector until it does.
var agentLabelTokenCols = map[string]string{
	"advertised_labels_json": "BLOB",
}

func applyClaimAttentionMigration(ctx context.Context, tx *storeTx, postgres bool) error {
	add := ensureColumnsSQLite
	if postgres {
		add = addColumnsTx
	}
	for table, cols := range map[string]map[string]string{
		"runs": claimAttentionRunCols, "nodes": claimAttentionNodeCols, "tokens": agentLabelTokenCols,
	} {
		if err := add(ctx, tx, table, cols); err != nil {
			return err
		}
	}
	return nil
}

// safety: a claim hides the reason without writing the run row.
const runAttentionColumn = `COALESCE((SELECT n.attention_reason FROM nodes n
 WHERE n.run_id = runs.id AND n.ready_at IS NOT NULL AND n.claimed_by IS NULL
   AND n.` + nodeNotDone + ` AND n.attention_reason != ''
 ORDER BY n.node_id LIMIT 1), '')`

func planClaimWait(snapshot []byte) int64 {
	var plan struct {
		ClaimWaitMS int64 `json:"claim_wait_ms"`
	}
	if json.Unmarshal(snapshot, &plan) != nil || plan.ClaimWaitMS <= 0 {
		return 0
	}
	return (time.Duration(plan.ClaimWaitMS) * time.Millisecond).Nanoseconds()
}

func (s *Store) failStaleQueuedNodes(ctx context.Context, wait time.Duration) ([][2]string, error) {
	if wait <= 0 {
		return nil, nil
	}
	now := time.Now().UnixNano()
	tx, err := s.beginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer rollbackOrLog(tx)
	rows, err := tx.QueryContext(ctx, `SELECT n.run_id, n.node_id, n.attention_reason FROM nodes n JOIN runs r ON r.id = n.run_id
 WHERE n.ready_at IS NOT NULL AND n.claimed_by IS NULL AND n.`+nodeNotDone+`
   AND n.ready_at + CASE WHEN r.claim_wait_ns > 0 THEN r.claim_wait_ns ELSE ? END < ?`,
		wait.Nanoseconds(), now)
	if err != nil {
		return nil, err
	}
	var pairs [][2]string
	var reasons []string
	for rows.Next() {
		var rid, nid, reason string
		if err := rows.Scan(&rid, &nid, &reason); err != nil {
			return nil, errors.Join(err, rows.Close())
		}
		if reason == "" {
			reason = "no agent claimed this node before its claim wait ran out"
		}
		pairs = append(pairs, [2]string{rid, nid})
		reasons = append(reasons, "unclaimable: "+reason)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	for i, p := range pairs {
		if _, err := tx.ExecContext(ctx, `
UPDATE nodes
   SET `+nodeFailSet+`, error = ?, failure_reason = ?, finished_at = ?, ready_at = NULL
 WHERE run_id = ? AND node_id = ? AND claimed_by IS NULL AND `+nodeNotDone,
			reasons[i], FailureQueueTimeout, now, p[0], p[1]); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return pairs, nil
}

// RecordAgentLabels stores the labels the agent holding tokenPrefix advertised
// on its latest claim, which say what it could run while it is offline.
func (s *Store) RecordAgentLabels(ctx context.Context, tokenPrefix string, labels []string) error {
	raw, err := json.Marshal(match.SelfAsserted(labels))
	if err != nil {
		return err
	}
	_, err = s.exec(ctx, `UPDATE tokens SET advertised_labels_json = ? WHERE prefix = ?`, raw, tokenPrefix)
	return err
}

// RegisteredAgent is a live agent credential of a team, online or not.
type RegisteredAgent struct {
	Name, TokenPrefix string
	// Labels are what it last advertised plus any an enrollment granted.
	Labels   []string
	LastSeen time.Time
}

// ListRegisteredAgents lists team's unrevoked, unexpired agent credentials:
// runner tokens and enrolled executors.
func (s *Store) ListRegisteredAgents(ctx context.Context, team Team) ([]RegisteredAgent, error) {
	rows, err := s.query(ctx, `SELECT t.prefix, t.principal, t.advertised_labels_json, t.last_used_at,
       e.name, e.capabilities_json, e.last_seen
  FROM tokens t LEFT JOIN executors e ON e.token_prefix = t.prefix
 WHERE t.team = ? AND t.revoked_at IS NULL AND (t.expires_at IS NULL OR t.expires_at > ?)
   AND (t.kind = 'runner' OR e.name IS NOT NULL)
 ORDER BY t.prefix`, string(team), time.Now().UnixNano())
	if err != nil {
		return nil, err
	}
	defer closeRowsOrLog(rows)
	var out []RegisteredAgent
	for rows.Next() {
		var a RegisteredAgent
		var principal string
		var advertised, granted []byte
		var usedNS, seenNS sql.NullInt64
		var executor sql.NullString
		if err := rows.Scan(&a.TokenPrefix, &principal, &advertised, &usedNS, &executor, &granted, &seenNS); err != nil {
			return nil, err
		}
		a.Name = strings.TrimPrefix(principal, agentPrincipalPrefix)
		if executor.Valid {
			a.Name = executor.String
		}
		for _, raw := range [][]byte{advertised, granted} {
			var labels []string
			if len(raw) > 0 && json.Unmarshal(raw, &labels) == nil {
				a.Labels = append(a.Labels, match.SelfAsserted(labels)...)
			}
		}
		if last := max(usedNS.Int64, seenNS.Int64); last > 0 {
			a.LastSeen = time.Unix(0, last)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// WaitingNode is a ready node no agent holds.
type WaitingNode struct {
	RunID, NodeID string
	Team          Team
	Selector      []string
	Attention     string
}

// ListWaitingNodes lists the unpinned ready nodes that have waited since
// before readyBefore with no claim.
func (s *Store) ListWaitingNodes(ctx context.Context, readyBefore time.Time) ([]WaitingNode, error) {
	rows, err := s.query(ctx, `SELECT run_id, node_id, team, needs_labels, attention_reason FROM nodes
 WHERE ready_at IS NOT NULL AND ready_at < ? AND claimed_by IS NULL AND `+nodeNotDone+`
   AND required_coordinator_id = '' AND required_executor_location = ''
 ORDER BY run_id, node_id`, readyBefore.UnixNano())
	if err != nil {
		return nil, err
	}
	defer closeRowsOrLog(rows)
	var out []WaitingNode
	for rows.Next() {
		var n WaitingNode
		var team string
		var needs []byte
		if err := rows.Scan(&n.RunID, &n.NodeID, &team, &needs, &n.Attention); err != nil {
			return nil, err
		}
		n.Team = Team(team)
		decodeCandidateLabels(n.RunID, n.NodeID, needs, &n.Selector)
		out = append(out, n)
	}
	return out, rows.Err()
}

// SetNodeAttention records why a waiting node has no agent to claim it, or
// clears the record with an empty reason. A claimed node keeps none.
func (s *Store) SetNodeAttention(ctx context.Context, team Team, runID, nodeID, reason string) error {
	_, err := s.exec(ctx, `UPDATE nodes SET attention_reason = ?
 WHERE team = ? AND run_id = ? AND node_id = ? AND claimed_by IS NULL AND `+nodeNotDone, reason, string(team), runID, nodeID)
	return err
}
