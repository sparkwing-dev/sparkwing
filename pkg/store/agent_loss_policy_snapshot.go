package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"
)

type agentLossRetryNodeSource struct {
	SourceRunID string
	Record      nodeRecord
}

const agentLossRetryNodeSourceColumns = `source_run_id, node_id, deps_json, needs_labels_json, prefers_labels_json,
       requested_cores, requested_memory_bytes, requested_slots, attempts_consumed,
       required_coordinator_id, required_executor_location,
       avoid_coordinator_id, avoid_executor_kind, avoid_executor_id, avoid_until,
       policy_json, policy_hash, policy_version, body_protocol,
       supervisor_requirements_json, supervisor_requirements_hash,
       body_requirements_json, body_requirements_hash`

func scanAgentLossRetryNodeSource(row rowScanner, retryRunID string) (*agentLossRetryNodeSource, error) {
	var source agentLossRetryNodeSource
	var depsJSON, needsJSON, prefersJSON, policyJSON, supervisorJSON, bodyJSON []byte
	var policyHash, supervisorHash, bodyHash string
	var policyVersion, bodyProtocol int
	var avoidUntil sql.NullInt64
	source.Record.RunID = retryRunID
	if err := row.Scan(
		&source.SourceRunID, &source.Record.NodeID, &depsJSON, &needsJSON, &prefersJSON,
		&source.Record.RequestedCores, &source.Record.RequestedMemoryBytes, &source.Record.RequestedSlots, &source.Record.AttemptsConsumed,
		&source.Record.RequiredCoordinatorID, &source.Record.RequiredExecutorLocation,
		&source.Record.AvoidCoordinatorID, &source.Record.AvoidExecutorKind, &source.Record.AvoidExecutorID, &avoidUntil,
		&policyJSON, &policyHash, &policyVersion, &bodyProtocol,
		&supervisorJSON, &supervisorHash, &bodyJSON, &bodyHash,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	for name, item := range map[string]struct {
		raw []byte
		out *[]string
	}{
		"dependencies":   {depsJSON, &source.Record.Deps},
		"needs labels":   {needsJSON, &source.Record.NeedsLabels},
		"prefers labels": {prefersJSON, &source.Record.PrefersLabels},
	} {
		if len(item.raw) > 0 {
			if err := json.Unmarshal(item.raw, item.out); err != nil {
				return nil, fmt.Errorf("%w: retry source %s: %v", errExecutionPolicyInvalid, name, err)
			}
		}
	}
	if source.Record.RequestedSlots < 1 {
		return nil, fmt.Errorf("%w: retry source requested slots", errExecutionPolicyInvalid)
	}
	if avoidUntil.Valid {
		value := time.Unix(0, avoidUntil.Int64)
		source.Record.AvoidUntil = &value
	}
	if err := restoreNodeExecutionPolicySeal(&source.Record, policyJSON, policyHash, policyVersion, bodyProtocol,
		supervisorJSON, supervisorHash, bodyJSON, bodyHash); err != nil {
		return nil, err
	}
	return &source, nil
}

func loadAgentLossRetryNodeSourceTx(ctx context.Context, tx *storeTx, retryRunID, nodeID string) (*agentLossRetryNodeSource, error) {
	source, err := scanAgentLossRetryNodeSource(tx.QueryRowContext(ctx, `SELECT `+agentLossRetryNodeSourceColumns+`
	  FROM agent_loss_retry_node_sources WHERE retry_run_id = ? AND node_id = ?`+tx.forUpdate(), retryRunID, nodeID), retryRunID)
	if err != nil {
		return nil, err
	}
	var pipeline string
	if err := tx.QueryRowContext(ctx, `SELECT pipeline FROM runs WHERE id = ?`, retryRunID).Scan(&pipeline); err != nil {
		return nil, err
	}
	if err := validateNodeExecutionPolicy(&source.Record, pipeline); err != nil {
		return nil, err
	}
	return source, nil
}

func (s *Store) requiredAgentLossRetryNodeSourceTx(ctx context.Context, tx *storeTx, retryRunID, nodeID string) (*agentLossRetryNodeSource, bool, error) {
	var sourceRunID string
	err := tx.QueryRowContext(ctx, `SELECT source_run_id FROM agent_loss_retries WHERE run_id = ?`+tx.forUpdate(), retryRunID).Scan(&sourceRunID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	source, err := loadAgentLossRetryNodeSourceTx(ctx, tx, retryRunID, nodeID)
	if errors.Is(err, ErrNotFound) {
		var legacySource string
		legacyErr := tx.QueryRowContext(ctx, `SELECT source_run_id FROM agent_loss_retry_legacy_deny_all WHERE retry_run_id = ?`, retryRunID).Scan(&legacySource)
		if legacyErr == nil {
			if legacySource != sourceRunID {
				return nil, true, fmt.Errorf("%w: legacy retry source run mismatch", errExecutionPolicyInvalid)
			}
			return nil, true, fmt.Errorf("%w: legacy retry has no authoritative source; execution is deny-all", errExecutionPolicyInvalid)
		}
		if !errors.Is(legacyErr, sql.ErrNoRows) {
			return nil, true, legacyErr
		}
		return nil, true, fmt.Errorf("%w: missing durable retry source for %s/%s", errExecutionPolicyInvalid, retryRunID, nodeID)
	}
	if err != nil {
		return nil, true, err
	}
	if source.SourceRunID != sourceRunID {
		return nil, true, fmt.Errorf("%w: retry source run mismatch", errExecutionPolicyInvalid)
	}
	return source, true, nil
}

func snapshotAgentLossRetryNodesTx(ctx context.Context, tx *storeTx, retryRunID, sourceRunID string) error {
	rows, err := tx.QueryContext(ctx, `SELECT `+nodeSelectColumns+` FROM nodes WHERE run_id = ? ORDER BY seq`+tx.forUpdate(), sourceRunID)
	if err != nil {
		return err
	}
	var nodes []*nodeRecord
	for rows.Next() {
		node := &nodeRecord{}
		if err := scanNodeRow(rows, node); err != nil {
			_ = rows.Close()
			return err
		}
		nodes = append(nodes, node)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return err
	}
	if len(nodes) == 0 {
		return fmt.Errorf("%w: agent-loss retry source has no nodes", errExecutionPolicyInvalid)
	}
	for _, node := range nodes {
		if err := persistAgentLossRetryNodeSourceTx(ctx, tx, retryRunID, sourceRunID, node); err != nil {
			return err
		}
	}
	return nil
}

func persistAgentLossRetryNodeSourceTx(ctx context.Context, tx *storeTx, retryRunID, sourceRunID string, node *nodeRecord) error {
	depsJSON, _ := json.Marshal(node.Deps)
	needsJSON, _ := json.Marshal(node.NeedsLabels)
	prefersJSON, _ := json.Marshal(node.PrefersLabels)
	persisted, err := nodeExecutionPolicyPersistence(node)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_loss_retry_node_sources
    (retry_run_id, source_run_id, node_id, deps_json, needs_labels_json, prefers_labels_json,
     requested_cores, requested_memory_bytes, requested_slots, attempts_consumed,
     required_coordinator_id, required_executor_location,
     avoid_coordinator_id, avoid_executor_kind, avoid_executor_id, avoid_until,
     policy_json, policy_hash, policy_version, body_protocol,
     supervisor_requirements_json, supervisor_requirements_hash,
     body_requirements_json, body_requirements_hash)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (retry_run_id, node_id) DO NOTHING`,
		retryRunID, sourceRunID, node.NodeID, depsJSON, needsJSON, prefersJSON,
		node.RequestedCores, node.RequestedMemoryBytes, node.RequestedSlots, node.AttemptsConsumed,
		node.RequiredCoordinatorID, node.RequiredExecutorLocation,
		node.AvoidCoordinatorID, node.AvoidExecutorKind, node.AvoidExecutorID, nullableUnixNano(node.AvoidUntil),
		persisted.PolicyJSON, persisted.PolicyHash, persisted.PolicyVersion, persisted.BodyProtocol,
		persisted.SupervisorRequirementsJSON, persisted.SupervisorRequirementsHash,
		persisted.BodyRequirementsJSON, persisted.BodyRequirementsHash); err != nil {
		return err
	}
	stored, err := loadAgentLossRetryNodeSourceTx(ctx, tx, retryRunID, node.NodeID)
	if err != nil {
		return err
	}
	if !agentLossRetryNodeSourceMatches(stored, sourceRunID, node) {
		return fmt.Errorf("%w: retry source snapshot changed for node %s", errExecutionPolicyConflict, node.NodeID)
	}
	return nil
}

func agentLossRetryNodeSourceMatches(stored *agentLossRetryNodeSource, sourceRunID string, node *nodeRecord) bool {
	if stored == nil || stored.SourceRunID != sourceRunID || stored.Record.NodeID != node.NodeID ||
		!slices.Equal(stored.Record.Deps, node.Deps) || !slices.Equal(stored.Record.NeedsLabels, node.NeedsLabels) ||
		!slices.Equal(stored.Record.PrefersLabels, node.PrefersLabels) || stored.Record.RequestedCores != node.RequestedCores ||
		stored.Record.RequestedMemoryBytes != node.RequestedMemoryBytes || stored.Record.RequestedSlots != node.RequestedSlots ||
		stored.Record.AttemptsConsumed != node.AttemptsConsumed || stored.Record.RequiredCoordinatorID != node.RequiredCoordinatorID ||
		stored.Record.RequiredExecutorLocation != node.RequiredExecutorLocation ||
		stored.Record.AvoidCoordinatorID != node.AvoidCoordinatorID || stored.Record.AvoidExecutorKind != node.AvoidExecutorKind ||
		stored.Record.AvoidExecutorID != node.AvoidExecutorID || !equalOptionalTime(stored.Record.AvoidUntil, node.AvoidUntil) {
		return false
	}
	return nodeExecutionPoliciesEqual(&stored.Record, node)
}

func equalOptionalTime(left, right *time.Time) bool {
	return left == nil && right == nil || left != nil && right != nil && left.Equal(*right)
}

func nullableUnixNano(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UnixNano()
}

func agentLossRetrySourceMatchesMaterialized(source *agentLossRetryNodeSource, node *nodeRecord) bool {
	return agentLossRetryMaterializedMismatch(source, node) == ""
}

func agentLossRetryMaterializedMismatch(source *agentLossRetryNodeSource, node *nodeRecord) string {
	if source == nil || node == nil {
		return "missing node"
	}
	if !slices.Equal(source.Record.Deps, node.Deps) {
		return "dependencies"
	}
	if !slices.Equal(source.Record.NeedsLabels, node.NeedsLabels) {
		return "required labels"
	}
	if !slices.Equal(source.Record.PrefersLabels, node.PrefersLabels) {
		return "preferred labels"
	}
	if source.Record.RequestedCores != node.RequestedCores || source.Record.RequestedMemoryBytes != node.RequestedMemoryBytes ||
		source.Record.RequestedSlots != node.RequestedSlots {
		return "resource request"
	}
	if source.Record.RequiredCoordinatorID != node.RequiredCoordinatorID ||
		source.Record.RequiredExecutorLocation != node.RequiredExecutorLocation {
		return "required executor placement"
	}
	return ""
}

func backfillAgentLossRetryNodeSourcesTx(ctx context.Context, tx *storeTx) error {
	rows, err := tx.QueryContext(ctx, `SELECT run_id, source_run_id FROM agent_loss_retries ORDER BY run_id`+tx.forUpdate())
	if err != nil {
		return err
	}
	type retry struct{ id, source string }
	var retries []retry
	for rows.Next() {
		var item retry
		if err := rows.Scan(&item.id, &item.source); err != nil {
			_ = rows.Close()
			return err
		}
		retries = append(retries, item)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return err
	}
	for _, item := range retries {
		if err := snapshotAgentLossRetryNodesTx(ctx, tx, item.id, item.source); err != nil {
			if !errors.Is(err, errExecutionPolicyInvalid) && !errors.Is(err, errExecutionUpgradeRequired) {
				return err
			}
			if _, markerErr := tx.ExecContext(ctx, `INSERT INTO agent_loss_retry_legacy_deny_all
    (retry_run_id, source_run_id, reason) VALUES (?, ?, ?)
ON CONFLICT (retry_run_id) DO NOTHING`, item.id, item.source, err.Error()); markerErr != nil {
				return markerErr
			}
		}
	}
	return nil
}
