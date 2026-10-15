package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// NodeLogSealGrace is how long after a node attempt ends its writer may
// still upload the attempt's log and seal it, and how long the controller
// waits before it seals the attempt as abandoned.
const NodeLogSealGrace = 60 * time.Second

// NodeLog records where one node attempt's log segments live, recorded
// when the attempt's writer first asks the controller for it, so a
// reader never has to guess the attempt's retention class or key.
type NodeLog struct {
	Team       Team
	RunID      string
	NodeID     string
	Attempt    int64
	Generation int64
	Prefix     string
	// UploadedThrough is the highest segment number signed for upload.
	UploadedThrough int64
	// StoredBytes is the size of every segment signed for upload.
	StoredBytes int64
	// ChargedBytes is how much of StoredBytes is committed to the team's
	// storage.
	ChargedBytes int64
	// SealedAt is zero while the attempt has no manifest.
	SealedAt  time.Time
	CreatedAt time.Time
	UpdatedAt time.Time
}

// CurrentNodeAttempt returns the attempt ordinal and claim generation a
// node's running execution writes under: the attempt its next report
// will consume, under the claim that holds it now.
func (s *Store) CurrentNodeAttempt(ctx context.Context, team Team, runID, nodeID string) (attempt, generation int64, err error) {
	err = s.queryRow(ctx, `SELECT attempts_consumed, claim_generation FROM nodes WHERE team = ? AND run_id = ? AND node_id = ?`,
		string(team), runID, nodeID).Scan(&attempt, &generation)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, notFound("node", runID+"/"+nodeID)
	}
	return attempt + 1, generation, err
}

// NodeLogSettled reports whether a node of the run will write no more
// log: its run is over, or the node is done with an outcome other than
// failed. Only a failure can be retried, so a failed node's log stays
// open until the run ends. found is false when the run has no such node.
func (s *Store) NodeLogSettled(ctx context.Context, team Team, runID, nodeID string) (settled, found bool, err error) {
	var status, outcome, runStatus string
	err = s.queryRow(ctx, `SELECT n.status, n.outcome, r.status
  FROM nodes n JOIN runs r ON r.team = n.team AND r.id = n.run_id
 WHERE n.team = ? AND n.run_id = ? AND n.node_id = ?`,
		string(team), runID, nodeID).Scan(&status, &outcome, &runStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	switch runStatus {
	case "success", runStatusFailed, runStatusCancelled:
		return true, true, nil
	}
	return status == nodeStatusDone && outcome != outcomeFailed, true, nil
}

// OpenNodeLog records prefix as the attempt's log location unless the
// attempt already has one, and returns the attempt's record. The first
// prefix wins, so a retried request never moves an attempt's log.
func (s *Store) OpenNodeLog(ctx context.Context, l NodeLog, now time.Time) (NodeLog, error) {
	if _, err := s.exec(ctx, `INSERT INTO node_logs
    (team, run_id, node_id, attempt, generation, prefix, created_at, updated_at)
    VALUES (?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT (team, run_id, node_id, attempt, generation) DO NOTHING`,
		string(l.Team), l.RunID, l.NodeID, l.Attempt, l.Generation, l.Prefix, now.UnixNano(), now.UnixNano()); err != nil {
		return NodeLog{}, err
	}
	return scanNodeLog(s.queryRow(ctx, `SELECT `+nodeLogColumns+` FROM node_logs
 WHERE team = ? AND run_id = ? AND node_id = ? AND attempt = ? AND generation = ?`,
		string(l.Team), l.RunID, l.NodeID, l.Attempt, l.Generation))
}

// NodeLogSegment is one segment of a node attempt's log as the controller
// signed its upload: the exact size and digests the upload URL binds.
type NodeLogSegment struct {
	N           int
	StoredBytes int64
	// MD5 is base64, as the Content-MD5 header carries it.
	MD5 string
	// SHA256 is hex, as the manifest carries it.
	SHA256 string
}

var (
	// ErrNodeLogSealed refuses a segment for an attempt whose log has a
	// manifest.
	ErrNodeLogSealed = errors.New("store: the node log is sealed")
	// ErrNodeLogSegmentConflict refuses a segment number already signed
	// for other bytes.
	ErrNodeLogSegmentConflict = errors.New("store: the log segment was signed for other bytes")
	// ErrNodeLogCap refuses a segment that would take the attempt's log
	// past its byte cap.
	ErrNodeLogCap = errors.New("store: the node log reached its byte cap")
)

// SignNodeLogSegment records seg as segment seg.N of l's attempt and adds
// its size to the attempt's stored bytes, committing it to the team's log
// storage when charge is set, in one transaction. Signing a number again
// for the same size and digests changes nothing and charges nothing. It
// refuses other bytes under a signed number with
// [ErrNodeLogSegmentConflict], a segment past capBytes with [ErrNodeLogCap]
// and a sealed attempt with [ErrNodeLogSealed]; a refused storage commit
// records nothing, so a retry is refused again.
func (s *Store) SignNodeLogSegment(ctx context.Context, l NodeLog, seg NodeLogSegment, capBytes int64, charge bool, now time.Time) (err error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackUnlessDone(tx, &err)
	key := []any{string(l.Team), l.RunID, l.NodeID, l.Attempt, l.Generation}
	var sealed sql.NullInt64
	var stored int64
	err = tx.QueryRowContext(ctx, `SELECT sealed_at, stored_bytes FROM node_logs
 WHERE team = ? AND run_id = ? AND node_id = ? AND attempt = ? AND generation = ?`+tx.forUpdate(), key...).Scan(&sealed, &stored)
	if errors.Is(err, sql.ErrNoRows) {
		return notFound("node log", l.RunID+"/"+l.NodeID)
	}
	if err != nil {
		return err
	}
	if sealed.Valid {
		return ErrNodeLogSealed
	}
	prior := NodeLogSegment{N: seg.N}
	err = tx.QueryRowContext(ctx, `SELECT stored_bytes, md5, sha256 FROM node_log_segments
 WHERE team = ? AND run_id = ? AND node_id = ? AND attempt = ? AND generation = ? AND n = ?`,
		append(key, seg.N)...).Scan(&prior.StoredBytes, &prior.MD5, &prior.SHA256)
	switch {
	case err == nil && prior != seg:
		return ErrNodeLogSegmentConflict
	case err == nil:
		return tx.Commit()
	case !errors.Is(err, sql.ErrNoRows):
		return err
	}
	if stored+seg.StoredBytes > capBytes {
		return ErrNodeLogCap
	}
	var charged int64
	if charge {
		charged = seg.StoredBytes
		c := StorageCommit{Team: l.Team, Kind: StorageLogs, Bytes: charged, Now: now}
		if err := takeStorageSlotTx(ctx, tx, c.Team, c.Bytes, c.Now); err != nil {
			return err
		}
		if _, err := commitStorageTx(ctx, tx, c); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO node_log_segments
    (team, run_id, node_id, attempt, generation, n, stored_bytes, md5, sha256)
    VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, append(key, seg.N, seg.StoredBytes, seg.MD5, seg.SHA256)...); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE node_logs
   SET uploaded_through = CASE WHEN uploaded_through > ? THEN uploaded_through ELSE ? END,
       stored_bytes = stored_bytes + ?, charged_bytes = charged_bytes + ?, updated_at = ?
 WHERE team = ? AND run_id = ? AND node_id = ? AND attempt = ? AND generation = ?`,
		append([]any{seg.N, seg.N, seg.StoredBytes, charged, now.UnixNano()}, key...)...); err != nil {
		return err
	}
	return tx.Commit()
}

// NodeLogSegments returns the segments signed for l's attempt, by number.
func (s *Store) NodeLogSegments(ctx context.Context, l NodeLog) (_ []NodeLogSegment, err error) {
	rows, err := s.query(ctx, `SELECT n, stored_bytes, md5, sha256 FROM node_log_segments
 WHERE team = ? AND run_id = ? AND node_id = ? AND attempt = ? AND generation = ? ORDER BY n`,
		string(l.Team), l.RunID, l.NodeID, l.Attempt, l.Generation)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()
	var out []NodeLogSegment
	for rows.Next() {
		var seg NodeLogSegment
		if err := rows.Scan(&seg.N, &seg.StoredBytes, &seg.MD5, &seg.SHA256); err != nil {
			return nil, err
		}
		out = append(out, seg)
	}
	return out, rows.Err()
}

// MarkNodeLogSealed records that the attempt's manifest exists, with
// the bytes its segments hold.
func (s *Store) MarkNodeLogSealed(ctx context.Context, l NodeLog, storedBytes int64, now time.Time) error {
	_, err := s.exec(ctx, `UPDATE node_logs SET sealed_at = ?, stored_bytes = ?, updated_at = ?
 WHERE team = ? AND run_id = ? AND node_id = ? AND attempt = ? AND generation = ? AND sealed_at IS NULL`,
		now.UnixNano(), storedBytes, now.UnixNano(),
		string(l.Team), l.RunID, l.NodeID, l.Attempt, l.Generation)
	return err
}

// GetNodeLog returns one attempt's log record, or ErrNotFound.
func (s *Store) GetNodeLog(ctx context.Context, team Team, runID, nodeID string, attempt, generation int64) (NodeLog, error) {
	l, err := scanNodeLog(s.queryRow(ctx, `SELECT `+nodeLogColumns+` FROM node_logs
 WHERE team = ? AND run_id = ? AND node_id = ? AND attempt = ? AND generation = ?`,
		string(team), runID, nodeID, attempt, generation))
	if errors.Is(err, sql.ErrNoRows) {
		return NodeLog{}, notFound("node log", runID+"/"+nodeID)
	}
	return l, err
}

// LatestNodeLog returns the newest attempt's log record for a node, or
// ErrNotFound when no attempt of the node wrote segments.
func (s *Store) LatestNodeLog(ctx context.Context, team Team, runID, nodeID string) (NodeLog, error) {
	l, err := scanNodeLog(s.queryRow(ctx, `SELECT `+nodeLogColumns+` FROM node_logs
 WHERE team = ? AND run_id = ? AND node_id = ?
 ORDER BY attempt DESC, generation DESC LIMIT 1`, string(team), runID, nodeID))
	if errors.Is(err, sql.ErrNoRows) {
		return NodeLog{}, notFound("node log", runID+"/"+nodeID)
	}
	return l, err
}

// RunNodeLogs returns every log record of a run, oldest attempt first
// within each node.
func (s *Store) RunNodeLogs(ctx context.Context, team Team, runID string) (_ []NodeLog, err error) {
	rows, err := s.query(ctx, `SELECT `+nodeLogColumns+` FROM node_logs
 WHERE team = ? AND run_id = ? ORDER BY node_id, attempt, generation`, string(team), runID)
	if err != nil {
		return nil, err
	}
	return collectNodeLogs(rows)
}

// DeleteRunNodeLogs forgets a run's log records once their objects are
// gone.
func (s *Store) DeleteRunNodeLogs(ctx context.Context, team Team, runID string) error {
	if _, err := s.exec(ctx, `DELETE FROM node_log_segments WHERE team = ? AND run_id = ?`, string(team), runID); err != nil {
		return err
	}
	_, err := s.exec(ctx, `DELETE FROM node_logs WHERE team = ? AND run_id = ?`, string(team), runID)
	return err
}

const nodeLogColumns = `team, run_id, node_id, attempt, generation, prefix, uploaded_through, stored_bytes, charged_bytes, sealed_at, created_at, updated_at`

func nodeLogColumnsOf(alias string) string {
	return alias + ".team, " + alias + ".run_id, " + alias + ".node_id, " + alias + ".attempt, " + alias + ".generation, " +
		alias + ".prefix, " + alias + ".uploaded_through, " + alias + ".stored_bytes, " +
		alias + ".charged_bytes, " + alias + ".sealed_at, " +
		alias + ".created_at, " + alias + ".updated_at"
}

func scanNodeLog(row rowScanner) (NodeLog, error) {
	var l NodeLog
	var team string
	var sealed sql.NullInt64
	var created, updated int64
	if err := row.Scan(&team, &l.RunID, &l.NodeID, &l.Attempt, &l.Generation, &l.Prefix,
		&l.UploadedThrough, &l.StoredBytes, &l.ChargedBytes, &sealed, &created, &updated); err != nil {
		return NodeLog{}, err
	}
	l.Team = Team(team)
	if sealed.Valid {
		l.SealedAt = time.Unix(0, sealed.Int64).UTC()
	}
	l.CreatedAt, l.UpdatedAt = time.Unix(0, created).UTC(), time.Unix(0, updated).UTC()
	return l, nil
}

func collectNodeLogs(rows *sql.Rows) (out []NodeLog, err error) {
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()
	for rows.Next() {
		l, err := scanNodeLog(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// LiveLogOffsetHeader names the byte offset in a node's whole log at
// which a live log append begins. A writer that keeps its log as
// segments sends it, so a reader can join the live view to the segments.
const LiveLogOffsetHeader = "X-Sparkwing-Log-Offset"

// LiveLogAttemptHeader names, beside [LiveLogOffsetHeader], the attempt
// whose bytes the offsets count, as "a<attempt>-g<generation>". Each
// attempt numbers its log from zero, so a new attempt starts the live
// view over.
const LiveLogAttemptHeader = "X-Sparkwing-Log-Attempt"

// LiveLogAttempt is the [LiveLogAttemptHeader] value for one attempt.
func LiveLogAttempt(attempt, generation int64) string {
	return fmt.Sprintf("a%d-g%d", attempt, generation)
}

// LogWatchHeader is the request header that, sent as "1" on a node claim
// or run heartbeat, asks the controller to answer which of the run's
// nodes a viewer is watching. A heartbeat without it is answered 204.
const LogWatchHeader = "X-Sparkwing-Log-Watch"

// LogSegmentRequest asks the controller to sign the upload of one sealed
// segment of a node attempt's log, for exactly its size and digests. N
// zero signs nothing and names the attempt the writer writes. Attempt and
// Generation echo that answer, so a writer whose attempt was superseded
// is refused; both are zero on the first request.
type LogSegmentRequest struct {
	Attempt    int64 `json:"attempt,omitempty"`
	Generation int64 `json:"generation,omitempty"`
	N          int   `json:"n"`
	// Size is the stored (compressed) segment's length in bytes.
	Size int64 `json:"size,omitempty"`
	// MD5 is the stored segment's MD5, base64, as Content-MD5 carries it.
	MD5 string `json:"md5,omitempty"`
	// SHA256 is the stored segment's SHA-256, hex, as the manifest lists it.
	SHA256 string `json:"sha256,omitempty"`
}

// LogSegmentGrant answers a [LogSegmentRequest]: the attempt the writer
// writes, its key prefix, and for a segment one presigned PUT that sends
// Headers and accepts only the signed bytes until ExpiresAt.
type LogSegmentGrant struct {
	Attempt    int64             `json:"attempt"`
	Generation int64             `json:"generation"`
	Prefix     string            `json:"prefix"`
	URL        string            `json:"url,omitempty"`
	Headers    map[string]string `json:"headers,omitempty"`
	ExpiresAt  time.Time         `json:"expires_at,omitzero"`
	// Watched reports that a viewer has the node's log open, so the
	// writer pushes its lines to the live view.
	Watched bool `json:"watched"`
}
