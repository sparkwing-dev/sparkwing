package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
)

func (t *Tenant) populateNodeOutput(ctx context.Context, n *Node) error {
	if len(n.outputRecord) == 0 {
		return nil
	}
	var key blob.RecordKey
	if err := json.Unmarshal(n.outputRecord, &key); err != nil {
		return err
	}
	if key.Team != string(t.team) || key.Kind != blob.KindOutput || key.Scope.Job != n.NodeID {
		return blob.ErrIntegrity
	}
	scope, err := blob.RecordNamespace(key)
	if err != nil {
		return err
	}
	err = t.s.queryRow(ctx, `SELECT digest FROM blob_records
 WHERE team=? AND kind=? AND scope=? AND name=? AND run_id=? AND node_id=?`,
		t.team, key.Kind, scope, key.Name, n.RunID, n.NodeID).Scan(&n.OutputDigest)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	return err
}
