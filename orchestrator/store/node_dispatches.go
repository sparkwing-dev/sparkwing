package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// MaxNodeDispatchEnvelope caps input_envelope_json; over-cap rows
// store {"version":1,"truncated":true,...}. Replay must reject truncated.
const MaxNodeDispatchEnvelope = 4 << 20 // 4 MiB

// NodeDispatch is one dispatch-frame snapshot.
type NodeDispatch struct {
	RunID            string    `json:"run_id"`
	NodeID           string    `json:"node_id"`
	Seq              int       `json:"seq"`
	DispatchedAt     time.Time `json:"dispatched_at"`
	CodeVersion      string    `json:"code_version,omitempty"`
	BinaryHash       string    `json:"binary_hash,omitempty"`
	RunnerLabels     []byte    `json:"runner_labels,omitempty"` // JSON []string
	EnvJSON          []byte    `json:"env_json,omitempty"`      // JSON map[string]string
	Workdir          string    `json:"workdir,omitempty"`
	InputEnvelope    []byte    `json:"input_envelope_json,omitempty"`
	InputSizeBytes   int64     `json:"input_size_bytes"`
	SecretRedactions int       `json:"secret_redactions"`
}

// WriteNodeDispatch persists a snapshot; Seq<0 = auto-assign.
// Over-cap envelopes become truncation stubs (size preserved).
func (s *Store) WriteNodeDispatch(ctx context.Context, d NodeDispatch) error {
	if d.RunID == "" || d.NodeID == "" {
		return fmt.Errorf("WriteNodeDispatch: run_id and node_id required")
	}
	envelope := d.InputEnvelope
	origSize := int64(len(envelope))
	if origSize > MaxNodeDispatchEnvelope {
		envelope = fmt.Appendf(nil,
			`{"version":1,"truncated":true,"reason":"size","original_size":%d}`,
			origSize)
	}
	if d.DispatchedAt.IsZero() {
		d.DispatchedAt = time.Now()
	}
	seq := d.Seq
	if seq < 0 {
		if err := s.db.QueryRowContext(ctx, `
			SELECT COALESCE(MAX(seq), -1) + 1
			  FROM node_dispatches
			 WHERE run_id = ? AND node_id = ?
		`, d.RunID, d.NodeID).Scan(&seq); err != nil {
			return fmt.Errorf("assign next seq: %w", err)
		}
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO node_dispatches (
			run_id, node_id, seq, dispatched_at,
			code_version, binary_hash, runner_labels, env_json,
			workdir, input_envelope_json, input_size_bytes, secret_redactions
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		d.RunID, d.NodeID, seq, d.DispatchedAt.UnixNano(),
		d.CodeVersion, d.BinaryHash, d.RunnerLabels, d.EnvJSON,
		d.Workdir, envelope, origSize, d.SecretRedactions,
	)
	return err
}

// GetNodeDispatch returns the snapshot at seq; seq<0 = latest.
func (s *Store) GetNodeDispatch(ctx context.Context, runID, nodeID string, seq int) (*NodeDispatch, error) {
	const cols = `run_id, node_id, seq, dispatched_at,
	              code_version, binary_hash, runner_labels, env_json,
	              workdir, input_envelope_json, input_size_bytes, secret_redactions`
	var row *sql.Row
	if seq < 0 {
		row = s.db.QueryRowContext(ctx, `
			SELECT `+cols+`
			  FROM node_dispatches
			 WHERE run_id = ? AND node_id = ?
			 ORDER BY seq DESC
			 LIMIT 1
		`, runID, nodeID)
	} else {
		row = s.db.QueryRowContext(ctx, `
			SELECT `+cols+`
			  FROM node_dispatches
			 WHERE run_id = ? AND node_id = ? AND seq = ?
		`, runID, nodeID, seq)
	}
	d, err := scanNodeDispatch(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return d, nil
}

// ListNodeDispatches returns all snapshots oldest-first.
func (s *Store) ListNodeDispatches(ctx context.Context, runID, nodeID string) ([]*NodeDispatch, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT run_id, node_id, seq, dispatched_at,
		       code_version, binary_hash, runner_labels, env_json,
		       workdir, input_envelope_json, input_size_bytes, secret_redactions
		  FROM node_dispatches
		 WHERE run_id = ? AND node_id = ?
		 ORDER BY seq ASC
	`, runID, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*NodeDispatch
	for rows.Next() {
		d, err := scanNodeDispatch(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// scanNodeDispatch accepts row.Scan or rows.Scan.
func scanNodeDispatch(scan func(...any) error) (*NodeDispatch, error) {
	d := &NodeDispatch{}
	var dispatchedNS int64
	if err := scan(
		&d.RunID, &d.NodeID, &d.Seq, &dispatchedNS,
		&d.CodeVersion, &d.BinaryHash, &d.RunnerLabels, &d.EnvJSON,
		&d.Workdir, &d.InputEnvelope, &d.InputSizeBytes, &d.SecretRedactions,
	); err != nil {
		return nil, err
	}
	d.DispatchedAt = time.Unix(0, dispatchedNS)
	return d, nil
}
