package store

import (
	"context"
	"database/sql"
	"errors"
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
