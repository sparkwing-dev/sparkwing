package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// LegacyOutput is a node output still held inline in the nodes table.
type LegacyOutput struct {
	Team   Team
	RunID  string
	NodeID string
	Data   []byte
}

// Ref names the object a moved output takes.
func (o LegacyOutput) Ref() OutputRef {
	sum := sha256.Sum256(o.Data)
	return OutputRef{Key: MigratedOutputKey(o.RunID, o.NodeID), Size: int64(len(o.Data)), SHA256: hex.EncodeToString(sum[:])}
}

// MigratedOutputKey is the key an inline output moves to. It is fixed, so a
// move that is run again rewrites the same object.
func MigratedOutputKey(runID, nodeID string) string {
	return OutputKeyPrefix(runID, nodeID) + "migrated"
}

// LegacyOutputs lists up to limit inline outputs that have no output ref yet,
// ordered by run and node after the cursor. A non-zero retainedSince skips a
// run that finished before it, unless it is its pipeline's newest success.
func (s *Store) LegacyOutputs(ctx context.Context, afterRun, afterNode string, limit int, retainedSince time.Time) (out []LegacyOutput, err error) {
	query := `SELECT n.team, n.run_id, n.node_id, n.output_json FROM nodes n JOIN runs r ON r.id = n.run_id
 WHERE n.output_json IS NOT NULL AND length(n.output_json) > 0 AND (n.run_id > ? OR (n.run_id = ? AND n.node_id > ?))
   AND NOT EXISTS (SELECT 1 FROM node_outputs o WHERE o.team = n.team AND o.run_id = n.run_id AND o.node_id = n.node_id)`
	args := []any{afterRun, afterRun, afterNode}
	if !retainedSince.IsZero() {
		query += `
   AND (r.finished_at IS NULL OR r.finished_at > ? OR (r.status = 'success' AND r.finished_at = (
       SELECT MAX(r2.finished_at) FROM runs r2 WHERE r2.team = r.team AND r2.pipeline = r.pipeline AND r2.status = 'success')))`
		args = append(args, retainedSince.UnixNano())
	}
	query += ` ORDER BY n.run_id, n.node_id LIMIT ?`
	args = append(args, limit)
	rows, err := s.query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()
	for rows.Next() {
		var o LegacyOutput
		var team string
		if err := rows.Scan(&team, &o.RunID, &o.NodeID, &o.Data); err != nil {
			return nil, err
		}
		o.Team = Team(team)
		out = append(out, o)
	}
	return out, rows.Err()
}

// RecordMigratedOutput records an inline output the caller already wrote to
// the output store. It leaves a newer ref for the node in place, so a move
// run again, or run beside a live controller, changes nothing it finds done.
func (s *Store) RecordMigratedOutput(ctx context.Context, o LegacyOutput, provenance string, now time.Time) (err error) {
	ref := o.Ref()
	tx, err := s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackUnlessDone(tx, &err)
	if err := recordOutputObjectTx(ctx, tx, o.Team, o.RunID, ref, "migrate-outputs", provenance, false, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO node_outputs (team, run_id, node_id, attempt, key, size, sha256)
VALUES (?, ?, ?, 0, ?, ?, ?) ON CONFLICT (team, run_id, node_id) DO NOTHING`,
		string(o.Team), o.RunID, o.NodeID, ref.Key, ref.Size, ref.SHA256); err != nil {
		return err
	}
	return tx.Commit()
}

const metaKeyOutputsConverted = "outputs_converted"

// perf: a laptop moves its inline outputs once and records that it did, so
// the next open skips the scan.
func (s *Store) convertLegacyOutputs(ctx context.Context) error {
	var done string
	err := s.queryRow(ctx, `SELECT value FROM sparkwing_meta WHERE key = ?`, metaKeyOutputsConverted).Scan(&done)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	afterRun, afterNode := "", ""
	for {
		batch, err := s.LegacyOutputs(ctx, afterRun, afterNode, 500, time.Time{})
		if err != nil {
			return err
		}
		for _, o := range batch {
			ref := o.Ref()
			path := OutputPath(s.outputDir, ref.Key)
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				return err
			}
			if err := os.WriteFile(path, o.Data, 0o600); err != nil {
				return err
			}
			if err := s.RecordMigratedOutput(ctx, o, "local", time.Now()); err != nil {
				return fmt.Errorf("record output %s/%s: %w", o.RunID, o.NodeID, err)
			}
			afterRun, afterNode = o.RunID, o.NodeID
		}
		if len(batch) < 500 {
			break
		}
	}
	_, err = s.exec(ctx, `INSERT INTO sparkwing_meta (key, value, updated_at) VALUES (?, '1', ?)
ON CONFLICT (key) DO NOTHING`, metaKeyOutputsConverted, time.Now().UnixNano())
	return err
}
