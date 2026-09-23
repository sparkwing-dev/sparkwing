package store

import (
	"context"
	"time"
)

// TestOnlyRecordCloudAttempt records one node attempt of runID on a cloud
// executor, which is what makes a run count as a cloud run.
func (s *Store) TestOnlyRecordCloudAttempt(ctx context.Context, team Team, runID, nodeID string) error {
	_, err := s.exec(ctx, `
INSERT INTO node_execution_attempts (
    lineage_root_run_id, run_id, node_id, attempt_ordinal, claim_generation,
    coordinator_id, membership_id, executor_kind, executor_id, executor_location,
    holder_id, reservation_id, started_at, team)
VALUES (?, ?, ?, 1, 1, 'coord', 'member', 'cloud', 'exec', 'cloud', 'holder', 'resv', ?, ?)`,
		runID, runID, nodeID, time.Now().UnixNano(), string(team))
	return err
}

// TestOnlySetTeamCreatedAt moves a team's creation time.
func (s *Store) TestOnlySetTeamCreatedAt(ctx context.Context, team Team, at time.Time) error {
	_, err := s.exec(ctx, `UPDATE teams SET created_at = ? WHERE name = ?`, at.UnixNano(), string(team))
	return err
}

// TestOnlyInsertAccount writes an account row created at the given time.
func (s *Store) TestOnlyInsertAccount(ctx context.Context, id string, at time.Time) error {
	_, err := s.exec(ctx, `
INSERT INTO accounts (id, email, created_at, updated_at) VALUES (?, ?, ?, ?)`,
		id, id+"@example.test", at.UnixNano(), at.UnixNano())
	return err
}
