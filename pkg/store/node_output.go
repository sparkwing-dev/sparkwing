package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/limits"
)

// OutputRef names a node's output object: its key in the team's output store,
// its size and its SHA-256.
type OutputRef struct {
	Key    string `json:"key"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// ErrOutputLimit refuses an output past the per-output or per-run limit.
var ErrOutputLimit = errors.New("output limit exceeded")

// ErrOutputNotStored wraps any failure to store a node's output, so the
// node that produced it fails with the reason instead of finishing without
// the output its dependents read.
var ErrOutputNotStored = errors.New("the node's output was not stored")

// ErrOutputCorrupt refuses output bytes whose size or digest differs from
// what their commit recorded.
var ErrOutputCorrupt = errors.New("output bytes do not match their recorded sha256")

// OutputKeyPrefix is the key prefix under which every output object of one
// node lives; the final segment tells attempts apart.
func OutputKeyPrefix(runID, nodeID string) string {
	return "outputs/" + runID + "/" + nodeID + "/"
}

func runOutputPrefix(runID string) string { return "outputs/" + runID + "/" }

func likePrefix(prefix string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(prefix) + "%"
}

func (r *OutputRef) valid(runID, nodeID string) bool {
	return r != nil && strings.HasPrefix(r.Key, OutputKeyPrefix(runID, nodeID)) && validUploadKey(r.Key) &&
		r.Size > 0 && r.Size <= limits.NodeOutputBytes && validSHA256(r.SHA256)
}

// safety: the key must be this node's own attempt's and committed for this
// team with exactly this size and digest, so a report cannot name another
// node's object, an earlier attempt's, one still uploading, or one whose
// bytes differ from the claim.
func checkOutputRefTx(ctx context.Context, tx *storeTx, team Team, runID, nodeID string, attempt, generation int64, ref *OutputRef) error {
	if !ref.valid(runID, nodeID) {
		return fmt.Errorf("%w: output ref does not name this node's output", ErrInvalidInput)
	}
	if !strings.HasPrefix(ref.Key, outputAttemptPrefix(runID, nodeID, attempt, generation)) {
		return fmt.Errorf("%w: output %s is not this attempt's", ErrInvalidInput, ref.Key)
	}
	size, sha, err := lockLiveOutputTx(ctx, tx, team, ref.Key)
	if err != nil {
		return err
	}
	if size != ref.Size || sha != ref.SHA256 {
		return fmt.Errorf("%w: output %s does not match its committed size and digest", ErrInvalidInput, ref.Key)
	}
	return nil
}

func lockLiveOutputTx(ctx context.Context, tx *storeTx, team Team, key string) (size int64, sha string, err error) {
	rows, err := tx.QueryContext(ctx, `SELECT size, sha256 FROM data_objects WHERE team = ? AND key = ?`+tx.forShare(),
		string(team), key)
	if err != nil {
		return 0, "", err
	}
	found := false
	for rows.Next() {
		if err := rows.Scan(&size, &sha); err != nil {
			return 0, "", errors.Join(err, rows.Close())
		}
		found = true
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return 0, "", err
	}
	if !found {
		return 0, "", fmt.Errorf("%w: output %s is not committed", ErrInvalidInput, key)
	}
	return size, sha, nil
}

func writeNodeOutputTx(ctx context.Context, tx *storeTx, team Team, runID, nodeID string, attempt int64, ref *OutputRef) error {
	if ref == nil {
		return clearNodeOutputTx(ctx, tx, team, runID, nodeID)
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO node_outputs (team, run_id, node_id, attempt, key, size, sha256)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (team, run_id, node_id) DO UPDATE SET attempt = excluded.attempt, key = excluded.key,
    size = excluded.size, sha256 = excluded.sha256`,
		string(team), runID, nodeID, attempt, ref.Key, ref.Size, ref.SHA256)
	return err
}

func clearNodeOutputTx(ctx context.Context, tx *storeTx, team Team, runID, nodeID string) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM node_outputs WHERE team = ? AND run_id = ? AND node_id = ?`, string(team), runID, nodeID)
	return err
}

// safety: the caller holds the team's cache storage row, so two reservations
// for one run count one after the other.
func checkRunOutputRoomTx(ctx context.Context, tx *storeTx, team Team, runID string, size int64, now time.Time) error {
	if size > limits.NodeOutputBytes {
		return fmt.Errorf("%w: an output is %d bytes; the limit is %d (16 MiB)", ErrOutputLimit, size, limits.NodeOutputBytes)
	}
	pattern := likePrefix(runOutputPrefix(runID))
	var committed, pending int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(size), 0) FROM data_objects WHERE team = ? AND key LIKE ? ESCAPE '\'`,
		string(team), pattern).Scan(&committed); err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(size), 0) FROM uploads
 WHERE team = ? AND key LIKE ? ESCAPE '\' AND committed_at = 0 AND expires_at > ?`,
		string(team), pattern, now.UnixNano()).Scan(&pending); err != nil {
		return err
	}
	if committed+pending+size > limits.RunOutputBytes {
		return fmt.Errorf("%w: the run's outputs would reach %d bytes; the limit is %d (1 GiB)",
			ErrOutputLimit, committed+pending+size, limits.RunOutputBytes)
	}
	return nil
}

// RecordLocalOutput records an output object the caller already wrote to its
// own output store, for a store with no upload in between: a laptop's local
// runs. It bypasses reservation, so a controller never calls it.
func (s *Store) RecordLocalOutput(ctx context.Context, runID, nodeID string, ref OutputRef, now time.Time) (err error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackUnlessDone(tx, &err)
	var team string
	if err := tx.QueryRowContext(ctx, `SELECT team FROM nodes WHERE run_id = ? AND node_id = ?`, runID, nodeID).Scan(&team); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return notFound("node", runID+"/"+nodeID)
		}
		return err
	}
	if !ref.valid(runID, nodeID) {
		return fmt.Errorf("%w: output ref does not name this node's output", ErrInvalidInput)
	}
	if err := recordOutputObjectTx(ctx, tx, Team(team), runID, ref, "local", "local", true, now); err != nil {
		return err
	}
	return tx.Commit()
}

func recordOutputObjectTx(ctx context.Context, tx *storeTx, team Team, runID string, ref OutputRef,
	principal, provenance string, checkLimit bool, now time.Time,
) error {
	if _, _, err := lockTeamStorageTx(ctx, tx, team, StorageCache, now); err != nil {
		return err
	}
	if checkLimit {
		if err := checkRunOutputRoomTx(ctx, tx, team, runID, ref.Size, now); err != nil {
			return err
		}
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO data_objects (team, key, store, size, sha256, principal, provenance, committed_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT (team, key, provenance) DO NOTHING`,
		string(team), ref.Key, string(StorageCache), ref.Size, ref.SHA256, principal, provenance, now.UnixNano())
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE team_storage SET used_bytes = used_bytes + ?, committed_bytes = committed_bytes + ?, updated_at = ?
 WHERE team = ? AND store = ?`, ref.Size, ref.Size, now.UnixNano(), string(team), string(StorageCache))
	return err
}

// NodeOutputObject returns the committed object behind a node's output, or
// [ErrNotFound] when the node recorded none.
func (s *Store) NodeOutputObject(ctx context.Context, team Team, runID, nodeID string) (Upload, error) {
	var u Upload
	var kind string
	var committed int64
	err := s.queryRow(ctx, `SELECT o.key, d.store, d.size, d.sha256, d.principal, d.provenance, d.committed_at
  FROM node_outputs o JOIN data_objects d ON d.team = o.team AND d.key = o.key
 WHERE o.team = ? AND o.run_id = ? AND o.node_id = ?
 ORDER BY CASE d.provenance WHEN 'cloud' THEN 0 ELSE 1 END LIMIT 1`, string(NormalizeTeam(team)), runID, nodeID).Scan(
		&u.Key, &kind, &u.Size, &u.SHA256, &u.Principal, &u.Provenance, &committed)
	if errors.Is(err, sql.ErrNoRows) {
		return Upload{}, ErrNotFound
	}
	if err != nil {
		return Upload{}, err
	}
	u.Team, u.Kind, u.RunID, u.CommittedAt = NormalizeTeam(team), StorageKind(kind), runID, time.Unix(0, committed)
	return u, nil
}

// OutputDir is the directory under which this store keeps output bytes it
// is handed directly: beside a SQLite database, and empty for Postgres
// unless [Store.SetOutputDir] names one.
func (s *Store) OutputDir() string { return s.outputDir }

// SetOutputDir names the directory [Store.OutputDir] reports.
func (s *Store) SetOutputDir(dir string) { s.outputDir = dir }

// OutputPath is where an output object lives under an output directory.
func OutputPath(dir, key string) string { return filepath.Join(dir, filepath.FromSlash(key)) }

// NewOutputKey returns a fresh key for one attempt's output of a node. The
// attempt ordinal and claim generation lead the final segment, so a report
// can be held to its own attempt's object.
func NewOutputKey(runID, nodeID string, attempt, generation int64) (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return outputAttemptPrefix(runID, nodeID, attempt, generation) + hex.EncodeToString(b[:]), nil
}

func outputAttemptPrefix(runID, nodeID string, attempt, generation int64) string {
	return OutputKeyPrefix(runID, nodeID) + fmt.Sprintf("a%d-g%d-", attempt, generation)
}

// NodeOutputKey returns a fresh key for the output of a node's current
// attempt.
func (s *Store) NodeOutputKey(ctx context.Context, runID, nodeID string) (string, error) {
	var consumed, generation int64
	err := s.queryRow(ctx, `SELECT attempts_consumed, claim_generation FROM nodes WHERE run_id = ? AND node_id = ?`,
		runID, nodeID).Scan(&consumed, &generation)
	if errors.Is(err, sql.ErrNoRows) {
		return "", notFound("node", runID+"/"+nodeID)
	}
	if err != nil {
		return "", err
	}
	return NewOutputKey(runID, nodeID, consumed+1, generation)
}

// ErrNoOutputDir refuses output bytes a store has nowhere to keep.
var ErrNoOutputDir = errors.New("store: this store keeps no output bytes; upload the output instead")

func (s *Store) writeLocalOutput(ctx context.Context, runID, nodeID string, data []byte) (*OutputRef, error) {
	if len(data) == 0 {
		return nil, nil
	}
	if s.outputDir == "" {
		return nil, ErrNoOutputDir
	}
	if int64(len(data)) > limits.NodeOutputBytes {
		return nil, fmt.Errorf("%w: an output is %d bytes; the limit is %d (16 MiB)", ErrOutputLimit, len(data), limits.NodeOutputBytes)
	}
	key, err := s.NodeOutputKey(ctx, runID, nodeID)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	ref := &OutputRef{Key: key, Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}
	path := OutputPath(s.outputDir, key)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return nil, err
	}
	if err := s.RecordLocalOutput(ctx, runID, nodeID, *ref, time.Now()); err != nil {
		return nil, errors.Join(err, os.Remove(path))
	}
	return ref, nil
}

// GetNodeOutput reads a node's output bytes from this store's output
// directory, checking them against the recorded digest. A node with no
// output returns nil.
func (s *Store) GetNodeOutput(ctx context.Context, runID, nodeID string) ([]byte, error) {
	n, err := s.defaultTenant().GetNode(ctx, runID, nodeID)
	if err != nil {
		return nil, err
	}
	if n.OutputRef == nil {
		return nil, nil
	}
	if s.outputDir == "" {
		return nil, ErrNoOutputDir
	}
	return ReadOutputFile(s.outputDir, *n.OutputRef)
}

// ReadOutputFile reads ref from an output directory and verifies its digest.
// A missing file reads as [ErrNotFound].
func ReadOutputFile(dir string, ref OutputRef) (data []byte, err error) {
	if ref.Size > limits.NodeOutputBytes {
		return nil, ErrOutputLimit
	}
	if !validUploadKey(ref.Key) || !strings.HasPrefix(ref.Key, "outputs/") {
		return nil, fmt.Errorf("%w: invalid output key", ErrInvalidInput)
	}
	file, err := os.Open(OutputPath(dir, ref.Key))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	defer func() {
		err = errors.Join(err, file.Close())
		if err != nil {
			data = nil
		}
	}()
	data, err = io.ReadAll(io.LimitReader(file, limits.NodeOutputBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limits.NodeOutputBytes {
		return nil, ErrOutputLimit
	}
	sum := sha256.Sum256(data)
	if int64(len(data)) != ref.Size || hex.EncodeToString(sum[:]) != ref.SHA256 {
		return nil, fmt.Errorf("output %s: %w", ref.Key, ErrOutputCorrupt)
	}
	return data, nil
}

// OutputUploadGrant tells a node where to PUT exactly its declared output
// bytes before it commits the upload and names Key in its report.
type OutputUploadGrant struct {
	UploadID  string            `json:"upload_id"`
	Key       string            `json:"key"`
	URL       string            `json:"url"`
	Headers   map[string]string `json:"headers,omitempty"`
	ExpiresAt time.Time         `json:"expires_at"`
}

// OutputReadGrant is a short-lived URL for one node's output and the digest
// its bytes must match. An empty URL means the node recorded no output.
type OutputReadGrant struct {
	URL     string    `json:"url,omitempty"`
	SHA256  string    `json:"sha256,omitempty"`
	Size    int64     `json:"size,omitempty"`
	Expires time.Time `json:"expires,omitzero"`
	// SourceRunID names the run the output was read from.
	SourceRunID string `json:"source_run_id,omitempty"`
}
