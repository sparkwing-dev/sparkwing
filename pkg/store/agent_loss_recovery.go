package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/retryprovenance"
)

const (
	agentLossRetryDeadline = 24 * time.Hour
	agentLossAvoidWindow   = 15 * time.Second
	agentLossMaxBackoff    = 30 * time.Second
)

type AgentLossRecovery struct {
	RunID       string
	NodeID      string
	RetryRunID  string
	Started     bool
	Invocations int
}

func (s *Store) expirePendingAgentLossRetries(ctx context.Context, now time.Time) error {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.expirePendingAgentLossRetriesTx(ctx, tx, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) expirePendingAgentLossRetriesTx(ctx context.Context, tx *storeTx, now time.Time) error {
	rows, err := tx.QueryContext(ctx, `SELECT alr.run_id, alr.source_run_id, alr.cause_nodes_json, alr.deadline_at
  FROM agent_loss_retries alr
  JOIN triggers t ON t.id = alr.run_id
 WHERE t.status = ? AND alr.deadline_at <= ?`+s.forUpdate(), triggerStatusPending, now.UnixNano())
	if err != nil {
		return err
	}
	type expiredRetry struct {
		runID, sourceRunID string
		causes             []string
		deadline           int64
	}
	var expired []expiredRetry
	for rows.Next() {
		var item expiredRetry
		var causesJSON []byte
		if err := rows.Scan(&item.runID, &item.sourceRunID, &causesJSON, &item.deadline); err != nil {
			_ = rows.Close()
			return err
		}
		if err := json.Unmarshal(causesJSON, &item.causes); err != nil {
			_ = rows.Close()
			return err
		}
		expired = append(expired, item)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return err
	}
	for _, item := range expired {
		if _, err := tx.ExecContext(ctx, `UPDATE triggers SET status = ?, lease_expires_at = NULL
 WHERE id = ? AND status = ?`, triggerStatusDone, item.runID, triggerStatusPending); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE runs SET status = ?, error = ?, finished_at = ?
 WHERE id = ? AND status = ?`, runStatusFailed, "agent-loss retry deadline exceeded", now.UnixNano(), item.runID, runStatusPending); err != nil {
			return err
		}
		payload, _ := json.Marshal(map[string]any{
			"retry_run_id": item.runID, "deadline_at": time.Unix(0, item.deadline),
		})
		if _, err := appendEventTx(ctx, tx, item.runID, "", "agent_loss_retry_deadline_exceeded", payload, now); err != nil {
			return err
		}
		for _, nodeID := range item.causes {
			if _, err := appendEventTx(ctx, tx, item.sourceRunID, nodeID, "agent_loss_retry_deadline_exceeded", payload, now); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Store) loadAgentLossRetry(ctx context.Context, run *Run) error {
	var causesJSON []byte
	var availableAt, deadlineAt int64
	err := s.queryRow(ctx, `SELECT cause_nodes_json, available_at, deadline_at, retry_count
  FROM agent_loss_retries WHERE run_id = ?`, run.ID).Scan(
		&causesJSON, &availableAt, &deadlineAt, &run.AgentLossRetryCount)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(causesJSON, &run.RetryCauseNodeIDs); err != nil {
		return err
	}
	available := time.Unix(0, availableAt)
	deadline := time.Unix(0, deadlineAt)
	run.RetryAvailableAt = &available
	run.RetryDeadlineAt = &deadline
	return nil
}

type expiredAgentNode struct {
	runID, nodeID                                         string
	coordinatorID, executorKind, executorName, executorID string
	executorLocation                                      string
	membershipID, reservationID                           string
	requiredCoordinatorID, requiredLocation               string
	started                                               bool
	invocations                                           int
}

type agentLossPlan struct {
	Nodes []struct {
		ID        string `json:"id"`
		Modifiers *struct {
			Retry int `json:"retry,omitempty"`
		} `json:"modifiers,omitempty"`
	} `json:"nodes"`
}

func (s *Store) recoverExpiredNodeClaims(ctx context.Context) ([]AgentLossRecovery, error) {
	now := time.Now()
	tx, err := s.beginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockExecutorEligibilityTx(ctx, tx, false); err != nil {
		return nil, err
	}

	rows, err := tx.QueryContext(ctx, `SELECT run_id, node_id, coordinator_id, executor_kind, claim_worker_id, executor_id, executor_location,
	       claim_membership_id, reservation_id, required_coordinator_id, required_executor_location,
	       execution_started_at, attempts_consumed
 FROM nodes
 WHERE claimed_by IS NOT NULL AND lease_expires_at IS NOT NULL
   AND lease_expires_at < ? AND `+nodeNotDone+s.forUpdate(), now.UnixNano())
	if err != nil {
		return nil, err
	}
	byRun := map[string][]expiredAgentNode{}
	var runOrder []string
	for rows.Next() {
		var item expiredAgentNode
		var started sql.NullInt64
		if err := rows.Scan(&item.runID, &item.nodeID, &item.coordinatorID, &item.executorKind,
			&item.executorName, &item.executorID, &item.executorLocation, &item.membershipID, &item.reservationID,
			&item.requiredCoordinatorID, &item.requiredLocation, &started, &item.invocations); err != nil {
			_ = rows.Close()
			return nil, err
		}
		item.started = started.Valid
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

	var recovered []AgentLossRecovery
	for _, runID := range runOrder {
		items := byRun[runID]
		for _, item := range items {
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
       ready_at = NULL, offer_started_at = NULL, reservation_id = ''
 WHERE run_id = ? AND node_id = ? AND `+nodeNotDone,
				FailureAgentLost, now.UnixNano(), item.coordinatorID, item.executorKind,
				item.executorID, avoidUntil, runID, item.nodeID); err != nil {
				return nil, err
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM node_claim_offers WHERE run_id = ? AND node_id = ?`, runID, item.nodeID); err != nil {
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
			if _, err := appendEventTx(ctx, tx, runID, item.nodeID, "agent_lease_lost", payload, now); err != nil {
				return nil, err
			}
			recovered = append(recovered, AgentLossRecovery{
				RunID: runID, NodeID: item.nodeID, Started: item.started, Invocations: item.invocations,
			})
		}

		retryID, causes, decisions, err := s.createAgentLossRetryTx(ctx, tx, runID, items, now)
		if err != nil {
			return nil, err
		}
		if retryID != "" {
			var availableAt int64
			var retryCount int
			if err := tx.QueryRowContext(ctx, `SELECT available_at, retry_count FROM agent_loss_retries WHERE run_id = ?`, retryID).Scan(&availableAt, &retryCount); err != nil {
				return nil, err
			}
			for i := range recovered {
				if recovered[i].RunID == runID {
					for _, cause := range causes {
						if recovered[i].NodeID == cause {
							recovered[i].RetryRunID = retryID
							kind := "agent_loss_prestart_requeued"
							if recovered[i].Started {
								kind = "agent_loss_poststart_retry_scheduled"
							}
							payload, _ := json.Marshal(map[string]any{
								"retry_run_id": retryID, "available_at": time.Unix(0, availableAt),
								"retry_count": retryCount, "invocations": recovered[i].Invocations,
							})
							if _, err := appendEventTx(ctx, tx, runID, recovered[i].NodeID, kind, payload, now); err != nil {
								return nil, err
							}
						}
					}
				}
			}
			if _, err := tx.ExecContext(ctx, `UPDATE node_execution_attempts
   SET retry_run_id = ? WHERE run_id = ? AND node_id IN (`+placeholders(len(causes))+`)`,
				append([]any{retryID, runID}, stringsToAny(causes)...)...); err != nil {
				return nil, err
			}
			for _, item := range items {
				if containsString(causes, item.nodeID) {
					continue
				}
				reason := decisions[item.nodeID]
				kind := "agent_loss_retry_not_scheduled"
				if reason == "budget_exhausted" {
					kind = "agent_loss_retry_exhausted"
				}
				payload, _ := json.Marshal(map[string]any{
					"reason": reason, "execution_started": item.started, "invocations": item.invocations,
				})
				if _, err := appendEventTx(ctx, tx, runID, item.nodeID, kind, payload, now); err != nil {
					return nil, err
				}
			}
		} else {
			for _, item := range items {
				reason := decisions[item.nodeID]
				kind := "agent_loss_retry_not_scheduled"
				if reason == "budget_exhausted" {
					kind = "agent_loss_retry_exhausted"
				}
				payload, _ := json.Marshal(map[string]any{
					"reason": reason, "execution_started": item.started, "invocations": item.invocations,
				})
				if _, err := appendEventTx(ctx, tx, runID, item.nodeID, kind, payload, now); err != nil {
					return nil, err
				}
			}
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

func (s *Store) createAgentLossRetryTx(ctx context.Context, tx *storeTx, sourceRunID string, items []expiredAgentNode, now time.Time) (string, []string, map[string]string, error) {
	decisions := make(map[string]string, len(items))
	var pipeline, status, triggerSource, retriedAs, repoURL, gitSHA, githubOwner, githubRepo string
	var planJSON, invocationJSON []byte
	var definitionPlanHash string
	err := tx.QueryRowContext(ctx, `SELECT pipeline, status, trigger_source, retried_as, repo_url, git_sha,
	   github_owner, github_repo, plan_json, invocation_json,
	   COALESCE((SELECT plan_hash FROM run_definition_plans WHERE run_id = runs.id), '')
  FROM runs WHERE id = ?`+s.forUpdate(), sourceRunID).Scan(
		&pipeline, &status, &triggerSource, &retriedAs, &repoURL, &gitSHA,
		&githubOwner, &githubRepo, &planJSON, &invocationJSON, &definitionPlanHash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil, decisionsFor(items, "source_not_active"), nil
	}
	if err != nil {
		return "", nil, nil, err
	}
	if status != runStatusRunning {
		return "", nil, decisionsFor(items, "source_not_active"), nil
	}
	if len(planJSON) == 0 {
		return "", nil, decisionsFor(items, "missing_plan"), nil
	}
	var plan agentLossPlan
	if err := json.Unmarshal(planJSON, &plan); err != nil {
		return "", nil, decisionsFor(items, "invalid_plan"), nil
	}
	budgets := make(map[string]int, len(plan.Nodes))
	for _, node := range plan.Nodes {
		budget := 1
		if node.Modifiers != nil {
			budget += max(node.Modifiers.Retry, 0)
		}
		budgets[node.ID] = budget
	}
	var causes []string
	for _, item := range items {
		if item.requiredCoordinatorID == "" || item.requiredLocation != "local" && item.requiredLocation != "cloud" {
			decisions[item.nodeID] = "placement_unknown"
			continue
		}
		budget, ok := budgets[item.nodeID]
		if !ok {
			decisions[item.nodeID] = "node_missing_from_plan"
			continue
		}
		if !item.started || item.invocations < budget {
			causes = append(causes, item.nodeID)
			decisions[item.nodeID] = "retry_scheduled"
		} else {
			decisions[item.nodeID] = "budget_exhausted"
		}
	}
	if len(causes) == 0 {
		if len(budgets) == 0 {
			return "", nil, decisionsFor(items, "invalid_plan"), nil
		}
		return "", nil, decisions, nil
	}
	provenanceJSON, provenanceDecision := agentLossRetryProvenance(
		planJSON, definitionPlanHash, invocationJSON, repoURL, gitSHA, githubOwner, githubRepo, triggerSource,
	)
	if provenanceDecision != "" {
		for _, nodeID := range causes {
			decisions[nodeID] = provenanceDecision
		}
		return "", nil, decisions, nil
	}
	if retriedAs != "" {
		return s.mergeAgentLossRetryTx(ctx, tx, sourceRunID, retriedAs, causes, decisions)
	}

	rootRunID := sourceRunID
	retryCount := 0
	deadline := now.Add(agentLossRetryDeadline)
	var priorDeadline int64
	err = tx.QueryRowContext(ctx, `SELECT root_run_id, retry_count, deadline_at
  FROM agent_loss_retries WHERE run_id = ?`, sourceRunID).Scan(&rootRunID, &retryCount, &priorDeadline)
	if err == nil {
		deadline = time.Unix(0, priorDeadline)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return "", nil, nil, err
	}
	if !now.Before(deadline) {
		return "", nil, decisionsFor(items, "deadline_exceeded"), nil
	}
	retryCount++
	backoff := time.Second << min(retryCount-1, 4)
	backoff = min(backoff, agentLossMaxBackoff)
	availableAt := now.Add(backoff)
	if availableAt.After(deadline) {
		return "", nil, decisionsFor(items, "deadline_exceeded"), nil
	}
	retryID, err := newAgentLossRunID()
	if err != nil {
		return "", nil, nil, err
	}
	causesJSON, _ := json.Marshal(causes)
	if !strings.HasPrefix(triggerSource, "pipeline-working-tree@") {
		triggerSource = "retry"
	}

	res, err := tx.ExecContext(ctx, `UPDATE runs SET retried_as = ? WHERE id = ? AND retried_as = ''`, retryID, sourceRunID)
	if err != nil {
		return "", nil, nil, err
	}
	changed, err := res.RowsAffected()
	if err != nil {
		return "", nil, nil, err
	}
	if changed != 1 {
		return "", nil, decisionsFor(items, "already_retried"), nil
	}
	first := items[0]
	for _, item := range items {
		if item.nodeID == causes[0] {
			first = item
			break
		}
	}
	var retryAvoidUntil any
	if first.executorID != "" {
		retryAvoidUntil = now.Add(agentLossAvoidWindow).UnixNano()
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO runs
    (id, pipeline, status, trigger_source, git_branch, git_sha, args_json, plan_json,
     created_at, started_at, parent_run_id, repo, repo_url, github_owner, github_repo,
     retry_of, retry_source, retry_cause_node_id, retry_avoid_coordinator_id,
     retry_avoid_executor_kind, retry_avoid_executor_id, retry_avoid_until, invocation_json)
 SELECT ?, pipeline, ?, ?, git_branch, git_sha, args_json, plan_json,
        ?, ?, parent_run_id, repo, repo_url, github_owner, github_repo,
        id, ?, ?, ?, ?, ?, ?, invocation_json
   FROM runs WHERE id = ?`, retryID, runStatusPending, triggerSource,
		now.UnixNano(), now.UnixNano(), RetrySourceAuto, causes[0], first.coordinatorID,
		first.executorKind, first.executorID, retryAvoidUntil, sourceRunID); err != nil {
		return "", nil, nil, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO triggers
    (id, pipeline, args_json, trigger_source, trigger_user, trigger_env, git_branch, git_sha,
     status, created_at, parent_run_id, repo, repo_url, github_owner, github_repo,
     retry_of, retry_source, "full", available_at)
 SELECT ?, pipeline, args_json, ?, '', ?, git_branch, git_sha,
        ?, ?, parent_run_id, repo, repo_url, github_owner, github_repo,
        id, ?, 0, ?
   FROM runs WHERE id = ?`, retryID, triggerSource, provenanceJSON, triggerStatusPending,
		now.UnixNano(), RetrySourceAuto, availableAt.UnixNano(), sourceRunID); err != nil {
		return "", nil, nil, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_loss_retries
    (run_id, source_run_id, root_run_id, cause_nodes_json, available_at, deadline_at, retry_count)
VALUES (?, ?, ?, ?, ?, ?, ?)`, retryID, sourceRunID, rootRunID, causesJSON,
		availableAt.UnixNano(), deadline.UnixNano(), retryCount); err != nil {
		return "", nil, nil, err
	}
	if err := snapshotAgentLossRetryNodesTx(ctx, tx, retryID, sourceRunID); err != nil {
		return "", nil, nil, err
	}
	return retryID, causes, decisions, nil
}

func (s *Store) mergeAgentLossRetryTx(ctx context.Context, tx *storeTx, sourceRunID, retryRunID string, causes []string, decisions map[string]string) (string, []string, map[string]string, error) {
	var existingJSON []byte
	var triggerStatus, retryStatus string
	err := tx.QueryRowContext(ctx, `SELECT alr.cause_nodes_json, t.status, r.status
  FROM agent_loss_retries alr
  JOIN triggers t ON t.id = alr.run_id
  JOIN runs r ON r.id = alr.run_id
 WHERE alr.run_id = ? AND alr.source_run_id = ?`+s.forUpdate(), retryRunID, sourceRunID).Scan(
		&existingJSON, &triggerStatus, &retryStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil, decisionsForNodes(decisions, "already_retried"), nil
	}
	if err != nil {
		return "", nil, nil, err
	}
	if triggerStatus != triggerStatusPending || retryStatus != runStatusPending {
		return "", nil, decisionsForNodes(decisions, "retry_already_started"), nil
	}
	var existing []string
	if err := json.Unmarshal(existingJSON, &existing); err != nil {
		return "", nil, nil, err
	}
	merged := append([]string(nil), existing...)
	for _, nodeID := range causes {
		if !containsString(merged, nodeID) {
			merged = append(merged, nodeID)
		}
	}
	encoded, _ := json.Marshal(merged)
	if _, err := tx.ExecContext(ctx, `UPDATE agent_loss_retries SET cause_nodes_json = ? WHERE run_id = ?`, encoded, retryRunID); err != nil {
		return "", nil, nil, err
	}
	return retryRunID, causes, decisions, nil
}

func decisionsForNodes(in map[string]string, reason string) map[string]string {
	out := make(map[string]string, len(in))
	for nodeID := range in {
		out[nodeID] = reason
	}
	return out
}

func decisionsFor(items []expiredAgentNode, reason string) map[string]string {
	decisions := make(map[string]string, len(items))
	for _, item := range items {
		decisions[item.nodeID] = reason
	}
	return decisions
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func agentLossRetryProvenance(planJSON []byte, definitionPlanHash string, invocationJSON []byte, repoURL, gitSHA, githubOwner, githubRepo, triggerSource string) ([]byte, string) {
	var invocation map[string]any
	if len(invocationJSON) > 0 {
		if err := json.Unmarshal(invocationJSON, &invocation); err != nil {
			return nil, "invalid_provenance"
		}
	}
	provenance := map[string]string{}
	if inherited, ok := invocation["retry_provenance"].(map[string]any); ok {
		for _, key := range []string{"repo_dir", "repo_identity", "revision", "content_policy"} {
			provenance[key], _ = inherited[key].(string)
		}
	} else {
		provenance["repo_dir"], _ = invocation["cwd"].(string)
		provenance["repo_identity"] = repoURL
		provenance["revision"] = gitSHA
	}
	if definitionPlanHash == "" {
		sum := sha256.Sum256(planJSON)
		definitionPlanHash = "sha256:" + hex.EncodeToString(sum[:])
	}
	provenance["plan_hash"] = definitionPlanHash
	completeWorkspace := provenance["repo_dir"] != "" && provenance["repo_identity"] != "" && provenance["revision"] != ""
	if completeWorkspace {
		if !filepath.IsAbs(provenance["repo_dir"]) || !validAgentLossRevision(provenance["revision"]) {
			return nil, "invalid_provenance"
		}
		raw, _ := json.Marshal(map[string]string{
			retryprovenance.RepoDirKey:      provenance["repo_dir"],
			retryprovenance.RepoIdentityKey: provenance["repo_identity"],
			retryprovenance.RevisionKey:     provenance["revision"],
			retryprovenance.PlanHashKey:     provenance["plan_hash"],
		})
		return raw, ""
	}
	if strings.HasPrefix(triggerSource, "pipeline-working-tree@") || (repoURL == "" && (githubOwner == "" || githubRepo == "")) {
		return nil, "missing_provenance"
	}
	if !validAgentLossRevision(gitSHA) {
		return nil, "invalid_provenance"
	}
	raw, _ := json.Marshal(map[string]string{retryprovenance.PlanHashKey: provenance["plan_hash"]})
	return raw, ""
}

func validAgentLossRevision(revision string) bool {
	if len(revision) != 40 && len(revision) != 64 {
		return false
	}
	_, err := hex.DecodeString(revision)
	return err == nil
}

func newAgentLossRunID() (string, error) {
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", err
	}
	return fmt.Sprintf("run-%s-%s", time.Now().UTC().Format("20060102-150405"), hex.EncodeToString(suffix[:])), nil
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
