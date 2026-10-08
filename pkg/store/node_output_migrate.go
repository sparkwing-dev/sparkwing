package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
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
	// Recorded is the moved object the node's ref names, nil before the move.
	Recorded *OutputRef
}

// Ref names the object a moved output takes.
func (o LegacyOutput) Ref() OutputRef {
	sum := sha256.Sum256(o.Data)
	return OutputRef{Key: MigratedOutputKey(o.RunID, o.NodeID), Size: int64(len(o.Data)), SHA256: hex.EncodeToString(sum[:])}
}

// Moved reports whether the node's ref already names exactly these bytes.
func (o LegacyOutput) Moved() bool { return o.Recorded != nil && *o.Recorded == o.Ref() }

// MigratedOutputKey is the key an inline output moves to. It is fixed, so a
// move that is run again rewrites the same object.
func MigratedOutputKey(runID, nodeID string) string {
	return OutputKeyPrefix(runID, nodeID) + "migrated"
}

// LegacyOutputs lists up to limit inline outputs ordered by run and node
// after the cursor: those with no ref yet, and those whose ref is their own
// moved object, which may since have been rewritten inline. A non-zero
// retainedSince skips a run that finished before it, unless it is its
// pipeline's newest success.
func (s *Store) LegacyOutputs(ctx context.Context, afterRun, afterNode string, limit int, retainedSince time.Time) (out []LegacyOutput, err error) {
	query := `SELECT n.team, n.run_id, n.node_id, n.output_json, o.key, o.size, o.sha256
  FROM nodes n JOIN runs r ON r.id = n.run_id
  LEFT JOIN node_outputs o ON o.team = n.team AND o.run_id = n.run_id AND o.node_id = n.node_id
 WHERE n.output_json IS NOT NULL AND length(n.output_json) > 0 AND (n.run_id > ? OR (n.run_id = ? AND n.node_id > ?))
   AND (o.key IS NULL OR o.key = 'outputs/' || n.run_id || '/' || n.node_id || '/migrated')`
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
		var key, sha sql.NullString
		var size sql.NullInt64
		if err := rows.Scan(&team, &o.RunID, &o.NodeID, &o.Data, &key, &size, &sha); err != nil {
			return nil, err
		}
		o.Team = Team(team)
		if key.Valid {
			o.Recorded = &OutputRef{Key: key.String, Size: size.Int64, SHA256: sha.String}
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// RecordMigratedOutput records an inline output the caller already wrote to
// the output store under its moved key. It replaces only a ref that names
// that same moved key, so a newer ref a live controller wrote stays in place.
func (s *Store) RecordMigratedOutput(ctx context.Context, o LegacyOutput, provenance string, now time.Time) (err error) {
	ref := o.Ref()
	tx, err := s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackUnlessDone(tx, &err)
	var prior int64
	err = tx.QueryRowContext(ctx, `SELECT size FROM data_objects WHERE team = ? AND key = ? AND provenance = ?`,
		string(o.Team), ref.Key, provenance).Scan(&prior)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if err := recordOutputObjectTx(ctx, tx, o.Team, o.RunID, ref, "migrate-outputs", provenance, false, now); err != nil {
			return err
		}
	case err != nil:
		return err
	default:
		if _, _, err := lockTeamStorageTx(ctx, tx, o.Team, StorageCache, now); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE data_objects SET size = ?, sha256 = ?, committed_at = ?
 WHERE team = ? AND key = ? AND provenance = ?`, ref.Size, ref.SHA256, now.UnixNano(), string(o.Team), ref.Key, provenance); err != nil {
			return err
		}
		// safety: the committed total moves with the count, so a storage pass
		// that took its marks before this rewrite keeps the change.
		if _, err := tx.ExecContext(ctx, `UPDATE team_storage SET used_bytes = CASE WHEN used_bytes + ? > 0 THEN used_bytes + ? ELSE 0 END,
       committed_bytes = committed_bytes + ?, updated_at = ? WHERE team = ? AND store = ?`,
			ref.Size-prior, ref.Size-prior, ref.Size-prior, now.UnixNano(), string(o.Team), string(StorageCache)); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO node_outputs (team, run_id, node_id, attempt, key, size, sha256)
VALUES (?, ?, ?, 0, ?, ?, ?)
ON CONFLICT (team, run_id, node_id) DO UPDATE SET size = excluded.size, sha256 = excluded.sha256
 WHERE node_outputs.key = excluded.key`,
		string(o.Team), o.RunID, o.NodeID, ref.Key, ref.Size, ref.SHA256); err != nil {
		return err
	}
	return tx.Commit()
}

// MoveStats counts what one [Store.MoveLegacyOutputs] moved.
type MoveStats struct {
	Outputs int64
	Bytes   int64
}

// MoveLegacyOutputs moves every inline output write puts into the output
// store, recording each ref only after write returns. It repeats whole
// passes until one records nothing, so an output written inline while it
// ran, behind its cursor, is caught by the next pass; a writer still writing
// inline after the last pass is not, which is why the final run happens
// with inline writers stopped. With verifyMoved, write also sees outputs
// already moved, so it can check their bytes.
func (s *Store) MoveLegacyOutputs(ctx context.Context, retainedSince time.Time, batch int, provenance string, verifyMoved bool,
	write func(context.Context, LegacyOutput) error,
) (MoveStats, error) {
	var total MoveStats
	for {
		recorded := int64(0)
		afterRun, afterNode := "", ""
		for {
			outputs, err := s.LegacyOutputs(ctx, afterRun, afterNode, batch, retainedSince)
			if err != nil {
				return total, err
			}
			for _, o := range outputs {
				afterRun, afterNode = o.RunID, o.NodeID
				if o.Moved() && !verifyMoved {
					continue
				}
				if err := write(ctx, o); err != nil {
					return total, fmt.Errorf("write output %s/%s: %w", o.RunID, o.NodeID, err)
				}
				if o.Moved() {
					continue
				}
				if err := s.RecordMigratedOutput(ctx, o, provenance, time.Now()); err != nil {
					return total, fmt.Errorf("record output %s/%s: %w", o.RunID, o.NodeID, err)
				}
				recorded++
				total.Outputs++
				total.Bytes += int64(len(o.Data))
			}
			if len(outputs) < batch {
				break
			}
		}
		if recorded == 0 {
			return total, nil
		}
	}
}

// WriteOutputFileDurably leaves exactly data at path: an existing file that
// already matches is synced and kept, and anything else is replaced through a
// synced temporary file, so a crash leaves either the old file or the whole
// new one.
func WriteOutputFileDurably(path string, data []byte) error {
	dir := filepath.Dir(path)
	if matched, err := syncIfMatches(path, data); err != nil {
		return err
	} else if matched {
		return syncDir(dir)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".output-*")
	if err != nil {
		return err
	}
	_, err = tmp.Write(data)
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = renameOutputFile(tmp.Name(), path)
	}
	if err != nil {
		return errors.Join(err, os.Remove(tmp.Name()))
	}
	return syncDir(dir)
}

// safety: a file found from an earlier run may still sit only in the page
// cache, so it is synced before anything records it done, and its digest is
// read back from the synced file.
func syncIfMatches(path string, data []byte) (bool, error) {
	f, err := openOutputForSync(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer func() { _ = f.Close() }()
	if err := f.Sync(); err != nil {
		return false, err
	}
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return false, err
	}
	want := sha256.Sum256(data)
	return n == int64(len(data)) && string(h.Sum(nil)) == string(want[:]), nil
}

const metaKeyOutputsConverted = "outputs_converted"

// safety: a laptop's inline bytes stay until a later release drops the
// column, so each open until the move is recorded done checks every moved
// file against them and rewrites a bad one; a crash anywhere in the move
// loses no output.
func (s *Store) convertLegacyOutputs(ctx context.Context) error {
	var done string
	err := s.queryRow(ctx, `SELECT value FROM sparkwing_meta WHERE key = ?`, metaKeyOutputsConverted).Scan(&done)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if _, err := s.MoveLegacyOutputs(ctx, time.Time{}, 500, "local", true, func(_ context.Context, o LegacyOutput) error {
		return WriteOutputFileDurably(OutputPath(s.outputDir, o.Ref().Key), o.Data)
	}); err != nil {
		return err
	}
	_, err = s.exec(ctx, `INSERT INTO sparkwing_meta (key, value, updated_at) VALUES (?, '1', ?)
ON CONFLICT (key) DO NOTHING`, metaKeyOutputsConverted, time.Now().UnixNano())
	return err
}
