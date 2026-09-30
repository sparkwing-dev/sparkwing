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
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Output limits, enforced when the bytes are reserved.
const (
	MaxOutputBytes    int64 = 64 << 20
	MaxRunOutputBytes int64 = 1 << 30
	// MaxUnpaidOutputBytes is the largest output a team with neither credits
	// nor a free slot may store; a larger one needs storage room.
	MaxUnpaidOutputBytes int64 = 1 << 20
)

// OutputRetention is how long a run's outputs outlive the run, matching its logs.
const OutputRetention = 30 * 24 * time.Hour

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

const nodeOutputTablesSQL = `CREATE TABLE IF NOT EXISTS node_outputs (
    team TEXT NOT NULL,
    run_id TEXT NOT NULL,
    node_id TEXT NOT NULL,
    attempt BIGINT NOT NULL,
    key TEXT NOT NULL,
    size BIGINT NOT NULL,
    sha256 TEXT NOT NULL,
    PRIMARY KEY (team, run_id, node_id)
);
CREATE TABLE IF NOT EXISTS output_runs (
    team TEXT NOT NULL,
    run_id TEXT NOT NULL,
    PRIMARY KEY (team, run_id)
);`

func applyNodeOutputMigration(ctx context.Context, tx *storeTx) error {
	return execStatements(ctx, tx, nodeOutputTablesSQL)
}

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
		r.Size > 0 && r.Size <= MaxOutputBytes && validSHA256(r.SHA256)
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
	var size int64
	var sha string
	err := tx.QueryRowContext(ctx, `SELECT size, sha256 FROM data_objects WHERE team = ? AND key = ? LIMIT 1`,
		string(team), ref.Key).Scan(&size, &sha)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: output %s is not committed", ErrInvalidInput, ref.Key)
	}
	if err != nil {
		return err
	}
	if size != ref.Size || sha != ref.SHA256 {
		return fmt.Errorf("%w: output %s does not match its committed size and digest", ErrInvalidInput, ref.Key)
	}
	return nil
}

func sourceOutputRefTx(ctx context.Context, tx *storeTx, team Team, runID, nodeID string) (*OutputRef, error) {
	var ref OutputRef
	err := tx.QueryRowContext(ctx, `SELECT key, size, sha256 FROM node_outputs WHERE team = ? AND run_id = ? AND node_id = ?`,
		string(team), runID, nodeID).Scan(&ref.Key, &ref.Size, &ref.SHA256)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &ref, nil
}

// FinishNodeCopyingOutput finishes a node whose output is the one srcRun's
// srcNode already stored, such as a cache hit's origin: the node's ref names
// that object instead of a second copy of its bytes.
func (s *Store) FinishNodeCopyingOutput(ctx context.Context, runID, nodeID, outcome, reason, srcRun, srcNode string) error {
	return s.finishNode(ctx, runID, nodeID, outcome, "", nil, &copySource{run: srcRun, node: srcNode}, reason, nil)
}

type copySource struct{ run, node string }

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
	if size > MaxOutputBytes {
		return fmt.Errorf("%w: an output is %d bytes; the limit is %d (64 MiB)", ErrOutputLimit, size, MaxOutputBytes)
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
	if committed+pending+size > MaxRunOutputBytes {
		return fmt.Errorf("%w: the run's outputs would reach %d bytes; the limit is %d (1 GiB)",
			ErrOutputLimit, committed+pending+size, MaxRunOutputBytes)
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO output_runs (team, run_id) VALUES (?, ?) ON CONFLICT (team, run_id) DO NOTHING`,
		string(team), runID)
	return err
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

// safety: an object stored without an upload still counts toward its run's
// limit, when checked, and its team's cache storage, as an uploaded one does,
// so retention's decrement finds what it removes counted.
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
	} else if _, err := tx.ExecContext(ctx, `INSERT INTO output_runs (team, run_id) VALUES (?, ?) ON CONFLICT (team, run_id) DO NOTHING`,
		string(team), runID); err != nil {
		return err
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

// NodeIsAncestor reports whether ancestor is a transitive dependency of node
// within one run.
func (s *Store) NodeIsAncestor(ctx context.Context, team Team, runID, ancestor, node string) (_ bool, err error) {
	rows, err := s.query(ctx, `SELECT node_id, deps_json FROM nodes WHERE team = ? AND run_id = ?`, string(team), runID)
	if err != nil {
		return false, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()
	deps := map[string][]string{}
	for rows.Next() {
		var id string
		var raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			return false, err
		}
		var list []string
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &list); err != nil {
				return false, fmt.Errorf("node %s deps: %w", id, err)
			}
		}
		deps[id] = list
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	seen := map[string]bool{node: true}
	queue := []string{node}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, d := range deps[cur] {
			if d == ancestor {
				return true, nil
			}
			if !seen[d] {
				seen[d] = true
				queue = append(queue, d)
			}
		}
	}
	return false, nil
}

// ExpiredOutputRun is a finished run whose outputs have passed retention.
type ExpiredOutputRun struct {
	Team  Team
	RunID string
}

// ExpiredOutputRuns lists runs that finished more than [OutputRetention]
// before now and hold outputs. The newest successful run of each pipeline is
// kept, so a cross-pipeline ref to it still resolves.
func (s *Store) ExpiredOutputRuns(ctx context.Context, now time.Time, limit int) (out []ExpiredOutputRun, err error) {
	rows, err := s.query(ctx, `SELECT o.team, o.run_id FROM output_runs o
  JOIN runs r ON r.id = o.run_id
 WHERE r.finished_at IS NOT NULL AND r.finished_at <= ?
   AND NOT (r.status = 'success' AND r.finished_at = (
       SELECT MAX(r2.finished_at) FROM runs r2
        WHERE r2.team = r.team AND r2.pipeline = r.pipeline AND r2.status = 'success'))
 ORDER BY r.finished_at LIMIT ?`, now.Add(-OutputRetention).UnixNano(), limit)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()
	for rows.Next() {
		var e ExpiredOutputRun
		var team string
		if err := rows.Scan(&team, &e.RunID); err != nil {
			return nil, err
		}
		e.Team = Team(team)
		out = append(out, e)
	}
	return out, rows.Err()
}

// DeleteRunOutputs drops a run's output rows once its objects are deleted.
// A ref to one reads as absent from then on.
func (s *Store) DeleteRunOutputs(ctx context.Context, team Team, runID string) (err error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackUnlessDone(tx, &err)
	pattern := likePrefix(runOutputPrefix(runID))
	var freed int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(size), 0) FROM data_objects WHERE team = ? AND key LIKE ? ESCAPE '\'`,
		string(team), pattern).Scan(&freed); err != nil {
		return err
	}
	// safety: a filesystem store has no bucket listing to correct the count,
	// so the bytes retention removes leave the team's storage here.
	now := time.Now()
	if _, _, err := lockTeamStorageTx(ctx, tx, team, StorageCache, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE team_storage SET used_bytes = CASE WHEN used_bytes > ? THEN used_bytes - ? ELSE 0 END,
       updated_at = ? WHERE team = ? AND store = ?`, freed, freed, now.UnixNano(), string(team), string(StorageCache)); err != nil {
		return err
	}
	// safety: a cache entry whose origin's output is gone reads as a miss
	// rather than as a hit with nothing to hand over.
	for _, q := range []string{
		`DELETE FROM node_outputs WHERE team = ? AND run_id = ?`,
		`DELETE FROM output_runs WHERE team = ? AND run_id = ?`,
		`DELETE FROM concurrency_cache WHERE team = ? AND origin_run_id = ?`,
	} {
		if _, err := tx.ExecContext(ctx, q, string(team), runID); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM data_objects WHERE team = ? AND key LIKE ? ESCAPE '\'`, string(team), pattern); err != nil {
		return err
	}
	return tx.Commit()
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
	if int64(len(data)) > MaxOutputBytes {
		return nil, fmt.Errorf("%w: an output is %d bytes; the limit is %d (64 MiB)", ErrOutputLimit, len(data), MaxOutputBytes)
	}
	key, err := s.NodeOutputKey(ctx, runID, nodeID)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	ref := &OutputRef{Key: key, Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}
	path := OutputPath(s.outputDir, key)
	if err := WriteOutputFileDurably(path, data); err != nil {
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
	n, err := s.GetNode(ctx, runID, nodeID)
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
func ReadOutputFile(dir string, ref OutputRef) ([]byte, error) {
	if !validUploadKey(ref.Key) || !strings.HasPrefix(ref.Key, "outputs/") {
		return nil, fmt.Errorf("%w: invalid output key", ErrInvalidInput)
	}
	data, err := os.ReadFile(OutputPath(dir, ref.Key))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	if int64(len(data)) != ref.Size || hex.EncodeToString(sum[:]) != ref.SHA256 {
		return nil, fmt.Errorf("output %s does not match its recorded digest", ref.Key)
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
