package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/limits"
)

// OutputRef names a node's output object: its key in the team's output store,
// its size and its SHA-256.
type OutputRef struct {
	Key    string `json:"key"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// OutputKeyPrefix is the key prefix under which every output object of one
// node lives; the final segment tells attempts apart.
func OutputKeyPrefix(runID, nodeID string) string {
	return "outputs/" + runID + "/" + nodeID + "/"
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

func outputAttemptPrefix(runID, nodeID string, attempt, generation int64) string {
	return OutputKeyPrefix(runID, nodeID) + fmt.Sprintf("a%d-g%d-", attempt, generation)
}
