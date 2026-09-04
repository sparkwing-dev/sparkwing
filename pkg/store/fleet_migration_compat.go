package store

import (
	"context"
	"fmt"
)

const (
	executorEnrollmentRequirement = "executor-enrollment-v1"
	executorOfferRequirement      = "executor-offer-arbitration-v1"
	agentLossRequirement          = "agent-loss-attempt-fencing-v1"
)

func legacyFleetStage(version int, listed []SchemaRequirement) (int, bool, error) {
	haveEnrollment := false
	haveOffers := false
	haveAgentLoss := false
	for _, requirement := range listed {
		switch requirement.Name {
		case executorEnrollmentRequirement:
			haveEnrollment = true
		case executorOfferRequirement:
			haveOffers = true
		case agentLossRequirement:
			haveAgentLoss = true
		}
	}
	if !haveEnrollment && !haveOffers && !haveAgentLoss {
		return 0, false, nil
	}

	recognized := version == 28 && haveEnrollment && !haveOffers && !haveAgentLoss ||
		version == 29 && haveEnrollment && haveOffers && !haveAgentLoss ||
		version == 30 && haveEnrollment && haveOffers && haveAgentLoss
	if !recognized {
		return 0, true, fmt.Errorf(
			"unrecognized unpublished Fleet schema lineage at v%d (enrollment=%t offers=%t agent-loss=%t)",
			version, haveEnrollment, haveOffers, haveAgentLoss,
		)
	}
	return version, true, nil
}

func bridgeLegacyFleetSQLite(ctx context.Context, s *Store, version int, listed []SchemaRequirement) error {
	stage, found, err := legacyFleetStage(version, listed)
	if err != nil || !found {
		return err
	}
	tx, err := s.beginTx(ctx)
	if err != nil {
		return fmt.Errorf("begin unpublished Fleet schema repair: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := validateLegacyFleetShape(ctx, tx, stage); err != nil {
		return err
	}
	complete, err := sqliteWave2InvariantsPresent(ctx, tx)
	if err != nil {
		return fmt.Errorf("inspect wave2 schema invariants: %w", err)
	}
	if !complete {
		if err := addNodeMetricsRunCascadeSQLite(ctx, tx); err != nil {
			return fmt.Errorf("repair wave2 node metrics invariant: %w", err)
		}
		if _, err := tx.ExecContext(ctx, concurrencyCacheOriginRunIndex); err != nil {
			return fmt.Errorf("repair wave2 concurrency index: %w", err)
		}
		if err := ensureColumnsSQLite(ctx, tx, "nodes", nodesOrderCols); err != nil {
			return fmt.Errorf("repair wave2 node order invariant: %w", err)
		}
		if _, err := tx.ExecContext(ctx, nodesOrderBackfillSQLite); err != nil {
			return fmt.Errorf("repair wave2 node order backfill: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit unpublished Fleet schema repair: %w", err)
	}
	return nil
}

func bridgeLegacyFleetPostgres(ctx context.Context, tx *storeTx, version int, listed []SchemaRequirement) error {
	stage, found, err := legacyFleetStage(version, listed)
	if err != nil || !found {
		return err
	}
	if err := validateLegacyFleetShape(ctx, tx, stage); err != nil {
		return err
	}
	complete, err := postgresWave2InvariantsPresent(ctx, tx)
	if err != nil {
		return fmt.Errorf("inspect wave2 schema invariants: %w", err)
	}
	if complete {
		return nil
	}
	if err := addNodeMetricsRunCascadePostgres(ctx, tx); err != nil {
		return fmt.Errorf("repair wave2 node metrics invariant: %w", err)
	}
	if _, err := tx.ExecContext(ctx, concurrencyCacheOriginRunIndex); err != nil {
		return fmt.Errorf("repair wave2 concurrency index: %w", err)
	}
	if err := addColumnsTx(ctx, tx, "nodes", nodesOrderCols); err != nil {
		return fmt.Errorf("repair wave2 node order invariant: %w", err)
	}
	return nil
}

func validateLegacyFleetShape(ctx context.Context, q migrationQueryExecer, stage int) error {
	probes := []string{
		`SELECT claim_executor, claim_cores, claim_memory_bytes, claim_reservation, claim_slot FROM nodes WHERE 1 = 0`,
		`SELECT name, token_prefix, kind, location, capabilities_json, max_concurrent, principal FROM executors WHERE 1 = 0`,
	}
	if stage >= 29 {
		probes = append(probes,
			`SELECT prefers_labels, requested_cores, requested_memory_bytes, requested_slots, offer_started_at, offer_priority_target, claim_base_priority, claim_priority, claim_worker_id, claim_executor_kind, claim_reservation_id FROM nodes WHERE 1 = 0`,
			`SELECT executor_id FROM executors WHERE 1 = 0`,
			`SELECT claim_token_prefix, claim_principal, holder_id, run_id, node_id, executor_name, membership_id, worker_id, executor_kind, reservation_id, resource_digest, slot, base_priority, effective_priority, offered_at, last_seen_at, lease_ns FROM node_claim_offers WHERE 1 = 0`,
		)
	}
	if stage >= 30 {
		probes = append(probes,
			`SELECT retry_cause_node_id, retry_avoid_coordinator_id, retry_avoid_executor_kind, retry_avoid_executor_id, retry_avoid_until FROM runs WHERE 1 = 0`,
			`SELECT coordinator_id, executor_kind, executor_id, execution_started_at, reservation_id, avoid_coordinator_id, avoid_executor_kind, avoid_executor_id, avoid_until, claim_generation, claim_membership_id, attempts_consumed, retry_root_run_id, executor_location, required_coordinator_id, required_executor_location FROM nodes WHERE 1 = 0`,
			`SELECT available_at FROM triggers WHERE 1 = 0`,
			`SELECT run_id, source_run_id, root_run_id, cause_nodes_json, available_at, deadline_at, retry_count FROM agent_loss_retries WHERE 1 = 0`,
			`SELECT lineage_root_run_id, run_id, node_id, attempt_ordinal, claim_generation, coordinator_id, membership_id, executor_kind, executor_name, executor_id, executor_location, holder_id, reservation_id, started_at, finished_at, outcome, failure_reason, retry_run_id FROM node_execution_attempts WHERE 1 = 0`,
			`SELECT run_id, plan_hash FROM run_definition_plans WHERE 1 = 0`,
		)
	}
	for _, probe := range probes {
		rows, err := q.QueryContext(ctx, probe)
		if err != nil {
			return fmt.Errorf("unrecognized or corrupt unpublished Fleet schema v%d: %w", stage, err)
		}
		if err := rows.Close(); err != nil {
			return fmt.Errorf("validate unpublished Fleet schema v%d: %w", stage, err)
		}
	}
	return nil
}

func sqliteWave2InvariantsPresent(ctx context.Context, q migrationQueryExecer) (bool, error) {
	cascade, err := nodeMetricsRunCascadeSQLiteValid(ctx, q)
	if err != nil {
		return false, err
	}
	const query = `SELECT
    (SELECT COUNT(*) FROM sqlite_master
      WHERE type = 'index' AND name = 'idx_concurrency_cache_origin_run'),
    (SELECT COUNT(*) FROM pragma_table_info('nodes') WHERE name = 'seq')`
	rest, err := twoMigrationInvariantsPresent(ctx, q, query)
	return cascade && rest, err
}

func postgresWave2InvariantsPresent(ctx context.Context, q migrationQueryExecer) (bool, error) {
	cascade, err := nodeMetricsRunCascadePostgresValid(ctx, q)
	if err != nil {
		return false, err
	}
	const query = `SELECT
    (SELECT COUNT(*) FROM pg_indexes
      WHERE schemaname = current_schema()
        AND tablename = 'concurrency_cache'
        AND indexname = 'idx_concurrency_cache_origin_run'),
    (SELECT COUNT(*) FROM information_schema.columns
      WHERE table_schema = current_schema()
        AND table_name = 'nodes' AND column_name = 'seq')`
	rest, err := twoMigrationInvariantsPresent(ctx, q, query)
	return cascade && rest, err
}

func twoMigrationInvariantsPresent(ctx context.Context, q migrationQueryExecer, query string) (bool, error) {
	rows, err := q.QueryContext(ctx, query)
	if err != nil {
		return false, err
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return false, err
		}
		return false, fmt.Errorf("invariant query returned no row")
	}
	var first, second int
	if err := rows.Scan(&first, &second); err != nil {
		return false, err
	}
	return first > 0 && second > 0, rows.Err()
}
