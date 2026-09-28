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
   AND COALESCE(n.placement_hold_from, n.ready_at) + CASE WHEN r.claim_wait_ns > 0 THEN r.claim_wait_ns ELSE ? END < ?`,
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
	// Profile is what an enrollment grants, or else what the agent advertised
	// on its latest claim.
	Profile match.Profile
	// Enrolled marks an enrolled executor, whose LastSeen is its heartbeat.
	Enrolled bool
	LastSeen time.Time
}

// ListRegisteredAgents lists team's unrevoked, unexpired agent credentials:
// runner tokens and enrolled executors.
func (s *Store) ListRegisteredAgents(ctx context.Context, team Team) ([]RegisteredAgent, error) {
	rows, err := s.query(ctx, `SELECT t.prefix, t.principal, t.advertised_labels_json, t.last_used_at,
       e.name, e.location, e.capabilities_json, e.accept_repos_json, e.budget_cores, e.budget_memory_bytes, e.last_seen
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
		var advertised, caps, accept []byte
		var usedNS, seenNS sql.NullInt64
		var name, location sql.NullString
		var cores sql.NullFloat64
		var memory sql.NullInt64
		if err := rows.Scan(&a.TokenPrefix, &principal, &advertised, &usedNS,
			&name, &location, &caps, &accept, &cores, &memory, &seenNS); err != nil {
			return nil, err
		}
		if name.Valid {
			e := Executor{
				Name: name.String, Location: location.String,
				Budget: ExecutorResource{Cores: cores.Float64, MemoryBytes: memory.Int64},
			}
			e.decodeGrants(caps, accept)
			a.Name, a.Profile, a.Enrolled = e.Name, e.profile(), true
			a.LastSeen = time.Unix(0, seenNS.Int64)
			out = append(out, a)
			continue
		}
		a.Name = strings.TrimPrefix(principal, agentPrincipalPrefix)
		a.Profile = match.Profile{Name: a.Name, Class: match.ClassAgent}
		if len(advertised) > 0 && json.Unmarshal(advertised, &a.Profile.Labels) == nil {
			a.Profile.Labels = match.SelfAsserted(a.Profile.Labels)
		}
		if usedNS.Int64 > 0 {
			a.LastSeen = time.Unix(0, usedNS.Int64)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// WaitingNode is a ready node no agent holds, with what a claim of it checks.
type WaitingNode struct {
	RunID, NodeID string
	Team          Team
	Selector      []string
	Repo          match.Repository
	Request       match.Resources
	Attention     string
}

// ListWaitingNodes lists up to limit unpinned nodes, ordered by run and node
// after the key after, that first became ready before readyBefore and that no
// agent has claimed.
func (s *Store) ListWaitingNodes(ctx context.Context, readyBefore time.Time, after [2]string, limit int) ([]WaitingNode, error) {
	rows, err := s.query(ctx, `SELECT n.run_id, n.node_id, n.team, n.needs_labels, n.attention_reason,
       n.requested_cores, n.requested_memory_bytes, tr.repo_url, tr.github_owner, tr.github_repo, tr.trigger_env
  FROM nodes n LEFT JOIN triggers tr ON tr.id = n.run_id AND tr.team = n.team
 WHERE n.ready_at IS NOT NULL AND COALESCE(n.placement_hold_from, n.ready_at) < ?
   AND n.claimed_by IS NULL AND n.`+nodeNotDone+`
   AND n.required_coordinator_id = '' AND n.required_executor_location = ''
   AND (n.run_id > ? OR (n.run_id = ? AND n.node_id > ?))
 ORDER BY n.run_id, n.node_id LIMIT ?`, readyBefore.UnixNano(), after[0], after[0], after[1], limit)
	if err != nil {
		return nil, err
	}
	defer closeRowsOrLog(rows)
	var out []WaitingNode
	for rows.Next() {
		var n WaitingNode
		var team string
		var needs, env []byte
		var url, owner, repo sql.NullString
		if err := rows.Scan(&n.RunID, &n.NodeID, &team, &needs, &n.Attention,
			&n.Request.Cores, &n.Request.MemoryBytes, &url, &owner, &repo, &env); err != nil {
			return nil, err
		}
		n.Team = Team(team)
		decodeCandidateLabels(n.RunID, n.NodeID, needs, &n.Selector)
		if url.Valid {
			n.Repo = triggerRunRepository(&Trigger{RepoURL: url.String, GithubOwner: owner.String, GithubRepo: repo.String}, env)
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// NodeAttention is why one waiting node has no agent to claim it; an empty
// Reason clears the record.
type NodeAttention struct {
	Team          Team
	RunID, NodeID string
	Reason        string
}

// SetNodeAttention records the reasons. A node claimed since the sweep read
// it keeps none.
//
// safety: each reason is its own autocommitted single-row statement, so the
// sweep never holds one node's lock while it waits for another's, and run
// settlement, which locks a run's nodes in its own order, cannot deadlock
// against it. A reason is advisory, so one lost to an error is rewritten on
// the next pass.
func (s *Store) SetNodeAttention(ctx context.Context, updates []NodeAttention) error {
	var errs []error
	for _, u := range updates {
		if _, err := s.exec(ctx, `UPDATE nodes SET attention_reason = ?
 WHERE team = ? AND run_id = ? AND node_id = ? AND claimed_by IS NULL AND `+nodeNotDone,
			u.Reason, string(u.Team), u.RunID, u.NodeID); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
