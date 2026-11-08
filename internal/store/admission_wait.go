package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

func fillAdmissionWaitsTx(ctx context.Context, tx *storeTx, team Team, snapshot *RunSnapshot) (err error) {
	waiting := map[string]int{}
	for i, node := range snapshot.Nodes {
		if node.Status == runstate.StatusPending && node.ClaimedBy != "" && node.StatusDetail == "" {
			waiting[string(node.Ref.NodeID)] = i
		}
	}
	if len(waiting) == 0 || snapshot.Run.FinishedAt != nil {
		return nil
	}
	rows, err := tx.queryContext(ctx, `SELECT node_id,kind,payload FROM events WHERE team=? AND run_id=? AND seq IN
 (SELECT MAX(seq) FROM events WHERE team=? AND run_id=? AND kind IN (?,?) GROUP BY node_id)`, team, snapshot.Run.ID, team, snapshot.Run.ID, runstate.EventAdmissionQueued, runstate.EventAdmissionGranted)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var node, kind string
		var raw []byte
		if err := rows.Scan(&node, &kind, &raw); err != nil {
			return err
		}
		i, ok := waiting[node]
		if !ok || kind != runstate.EventAdmissionQueued {
			continue
		}
		var payload struct {
			Reason   string `json:"blocking_reason"`
			Key      string `json:"key"`
			Position int    `json:"position"`
		}
		if err := json.Unmarshal(raw, &payload); err != nil {
			return err
		}
		reason := payload.Reason
		if reason == "" {
			reason = fmt.Sprintf("queue position %d, waiting on %s", payload.Position, payload.Key)
		}
		snapshot.Nodes[i].StatusDetail = "waiting for local CPU admission: " + reason
	}
	return rows.Err()
}
