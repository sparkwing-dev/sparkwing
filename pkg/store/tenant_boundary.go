package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// OwnsRun reports whether id names a run or a trigger of t's team. A
// trigger counts because a webhook or cron trigger exists before the
// orchestrator creates its run under the same id, and a caller must not
// read or act on that trigger from another team in the gap.
func (t *Tenant) OwnsRun(ctx context.Context, id string) (bool, error) {
	var found int
	err := t.s.queryRow(ctx, `
SELECT 1 FROM runs WHERE team = ? AND id = ?
UNION ALL
SELECT 1 FROM triggers WHERE team = ? AND id = ?
LIMIT 1`, string(t.team), id, string(t.team), id).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// OwnedRunIDs returns the subset of ids that name one of t's runs.
func (t *Tenant) OwnedRunIDs(ctx context.Context, ids []string) (_ map[string]bool, err error) {
	owned := map[string]bool{}
	if len(ids) == 0 {
		return owned, nil
	}
	args := make([]any, 0, len(ids)+1)
	args = append(args, string(t.team))
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := t.s.query(ctx, `SELECT id FROM runs WHERE team = ? AND id IN (`+
		strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		owned[id] = true
	}
	return owned, rows.Err()
}
