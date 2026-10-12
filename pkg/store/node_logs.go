package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

const nodeLogsTableSQL = `CREATE TABLE IF NOT EXISTS node_logs (
    team TEXT NOT NULL,
    run_id TEXT NOT NULL,
    node_id TEXT NOT NULL,
    attempt BIGINT NOT NULL,
    generation BIGINT NOT NULL,
    prefix TEXT NOT NULL,
    uploaded_through BIGINT NOT NULL DEFAULT 0,
    stored_bytes BIGINT NOT NULL DEFAULT 0,
    charged_bytes BIGINT NOT NULL DEFAULT 0,
    sealed_at BIGINT,
    created_at BIGINT NOT NULL,
    updated_at BIGINT NOT NULL,
    PRIMARY KEY (team, run_id, node_id, attempt, generation)
);
CREATE INDEX IF NOT EXISTS idx_node_logs_unsealed ON node_logs(updated_at) WHERE sealed_at IS NULL;`

func applyNodeLogsMigration(ctx context.Context, tx *storeTx) error {
	return execStatements(ctx, tx, nodeLogsTableSQL)
}

// NodeLog records where one node attempt's log segments live, recorded
// when the controller first issues upload URLs for the attempt, so a
// reader never has to guess the attempt's retention class or key.
type NodeLog struct {
	Team            Team
	RunID           string
	NodeID          string
	Attempt         int64
	Generation      int64
	Prefix          string
	UploadedThrough int64
	StoredBytes     int64
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

// NodeDone reports whether a node of the run has finished for good, and
// found false when the run has no such node.
func (s *Store) NodeDone(ctx context.Context, team Team, runID, nodeID string) (done, found bool, err error) {
	var status string
	err = s.queryRow(ctx, `SELECT status FROM nodes WHERE team = ? AND run_id = ? AND node_id = ?`,
		string(team), runID, nodeID).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, nil
	}
	return status == nodeStatusDone, err == nil, err
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

// RecordNodeLogProgress raises the attempt's uploaded-through segment and
// stored byte count to what its writer reports; neither ever goes down.
func (s *Store) RecordNodeLogProgress(ctx context.Context, l NodeLog, now time.Time) error {
	_, err := s.exec(ctx, `UPDATE node_logs
   SET uploaded_through = CASE WHEN uploaded_through > ? THEN uploaded_through ELSE ? END,
       stored_bytes = CASE WHEN stored_bytes > ? THEN stored_bytes ELSE ? END,
       updated_at = ?
 WHERE team = ? AND run_id = ? AND node_id = ? AND attempt = ? AND generation = ?`,
		l.UploadedThrough, l.UploadedThrough, l.StoredBytes, l.StoredBytes, now.UnixNano(),
		string(l.Team), l.RunID, l.NodeID, l.Attempt, l.Generation)
	return err
}

// ChargeNodeLog commits the attempt's stored bytes past what l records as
// charged to its team's log storage, and records charged, in one
// transaction. It reports false, changing nothing, when another request
// charged the attempt since l was read, so each delta is committed once; a
// commit the team's storage refuses leaves the attempt as it was, so no
// reader ever sees bytes charged that the team's count never took.
func (s *Store) ChargeNodeLog(ctx context.Context, l NodeLog, charged int64, now time.Time) (_ bool, err error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return false, err
	}
	defer rollbackUnlessDone(tx, &err)
	c := StorageCommit{Team: l.Team, Kind: StorageLogs, Bytes: charged - l.ChargedBytes, Now: now}
	if err := takeStorageSlotTx(ctx, tx, c.Team, c.Bytes, c.Now); err != nil {
		return false, err
	}
	if _, err := commitStorageTx(ctx, tx, c); err != nil {
		return false, err
	}
	res, err := tx.ExecContext(ctx, `UPDATE node_logs SET charged_bytes = ?
 WHERE team = ? AND run_id = ? AND node_id = ? AND attempt = ? AND generation = ? AND charged_bytes = ?`,
		charged, string(l.Team), l.RunID, l.NodeID, l.Attempt, l.Generation, l.ChargedBytes)
	if err != nil {
		return false, err
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return false, err
	}
	return true, tx.Commit()
}

// PrunableNodeLogs lists a team's log records of finished runs, least
// recently written first, which is the order a team past its storage
// share loses them in.
func (s *Store) PrunableNodeLogs(ctx context.Context, team Team, limit int) (_ []NodeLog, err error) {
	rows, err := s.query(ctx, `SELECT `+nodeLogColumnsOf("l")+` FROM node_logs l
  JOIN runs r ON r.team = l.team AND r.id = l.run_id
 WHERE l.team = ? AND r.status IN (?, ?, ?)
 ORDER BY l.updated_at LIMIT ?`, string(team), "success", runStatusFailed, runStatusCancelled, limit)
	if err != nil {
		return nil, err
	}
	return collectNodeLogs(rows)
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

// AbandonedNodeLogs lists unsealed attempts no writer will seal: the
// node finished or another claim generation holds it, nothing has been
// heard from the attempt since before cutoff, and a finished node
// finished before cutoff too. A deleted run's records are not listed;
// deleting the run deletes its logs.
func (s *Store) AbandonedNodeLogs(ctx context.Context, cutoff time.Time, limit int) (_ []NodeLog, err error) {
	rows, err := s.query(ctx, `SELECT `+nodeLogColumnsOf("l")+` FROM node_logs l
  JOIN nodes n ON n.team = l.team AND n.run_id = l.run_id AND n.node_id = l.node_id
 WHERE l.sealed_at IS NULL AND l.updated_at < ?
   AND ((n.status = ? AND COALESCE(n.finished_at, 0) < ?)
        OR (n.status != ? AND l.generation != n.claim_generation))
 ORDER BY l.updated_at LIMIT ?`,
		cutoff.UnixNano(), nodeStatusDone, cutoff.UnixNano(), nodeStatusDone, limit)
	if err != nil {
		return nil, err
	}
	return collectNodeLogs(rows)
}

// DeleteRunNodeLogs forgets a run's log records once their objects are
// gone.
func (s *Store) DeleteRunNodeLogs(ctx context.Context, team Team, runID string) error {
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

// LogSegmentsRequest asks the controller for upload URLs for a node
// attempt's log segments. Attempt and Generation echo the previous
// grant, so a writer whose attempt was superseded is refused; both are
// zero on the first request. UploadedThrough and StoredBytes report
// what the writer has stored so far.
type LogSegmentsRequest struct {
	Attempt         int64 `json:"attempt,omitempty"`
	Generation      int64 `json:"generation,omitempty"`
	Next            int   `json:"next"`
	Count           int   `json:"count"`
	UploadedThrough int   `json:"uploaded_through"`
	StoredBytes     int64 `json:"stored_bytes"`
}

// LogSegmentURL is one presigned PUT. N is the segment number; the
// manifest's URL carries zero.
type LogSegmentURL struct {
	N       int               `json:"n"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
}

// LogSegmentsGrant answers a [LogSegmentsRequest]: the attempt the
// writer writes, its key prefix, one URL per requested segment, and a
// fresh manifest URL, all valid until ExpiresAt.
type LogSegmentsGrant struct {
	Attempt     int64           `json:"attempt"`
	Generation  int64           `json:"generation"`
	Prefix      string          `json:"prefix"`
	URLs        []LogSegmentURL `json:"urls"`
	ManifestURL LogSegmentURL   `json:"manifest_url"`
	ExpiresAt   time.Time       `json:"expires_at"`
	// Watched reports that a viewer has the node's log open, so the
	// writer pushes its lines to the live view.
	Watched bool `json:"watched"`
}
