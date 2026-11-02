package store

import (
	"context"
	"errors"
	"strings"
	"time"
)

// GetRun fetches one of t's runs by id. A run of another team reads as
// [ErrNotFound], because a caller told the row exists but is not yours
// learns that the id is in use elsewhere.
func (t *Tenant) GetRun(ctx context.Context, runID string) (*Run, error) {
	row := t.s.queryRow(ctx, `
SELECT `+runColumns+`
  FROM runs WHERE team = ? AND id = ?`, string(t.team), runID)
	run, err := scanRun(row)
	if errors.Is(err, ErrNotFound) {
		return nil, notFound("run", runID)
	}
	if err != nil {
		return nil, err
	}
	if err := t.s.refreshRunPresentations(ctx, []*Run{run}); err != nil {
		return nil, err
	}
	return run, nil
}

// ListRuns returns t's runs, newest first, filtered by f.
func (t *Tenant) ListRuns(ctx context.Context, f RunFilter) ([]*Run, error) {
	if t.team == "" {
		return nil, ErrNoTeam
	}
	where, args, err := runFilterWhere(t.s.dialect, f)
	if err != nil {
		return nil, err
	}
	where = strings.Replace(where, " WHERE ", " AND ", 1)
	return t.s.listRuns(ctx, `SELECT `+runColumns+` FROM runs WHERE team = ?`+where, append([]any{t.team}, args...), f)
}

// RetryTreeMember is one run of a retry tree, carrying only what orders
// and pages the tree; callers load each page's runs by id.
type RetryTreeMember struct {
	ID        string
	Number    int64
	CreatedAt time.Time
}

// safety: every step of the walk, up and down, carries the team predicate, because retry_of is a bare id and a
// pointer that crosses teams would otherwise hand the caller the other team's run. UNION drops a row it already
// produced, so a retry_of cycle ends both walks with no depth limit to cut a long chain short.
const retryTreeSQL = `WITH RECURSIVE
 up(id, parent) AS (
  SELECT id, retry_of FROM runs WHERE team = ? AND id = ?
  UNION
  SELECT r.id, r.retry_of FROM runs r JOIN up ON r.id = up.parent WHERE r.team = ?
 ),
 tree(id) AS (
  SELECT id FROM up
  UNION
  SELECT r.id FROM runs r JOIN tree ON r.retry_of = tree.id WHERE r.team = ?
 )
SELECT id, number, created_at FROM runs
 WHERE team = ? AND id IN (SELECT id FROM tree)
 ORDER BY created_at, number, id`

// ListRunRetryTree returns every run of t's team in the retry tree runID
// belongs to, oldest first: the root found by walking retry_of upward, plus
// every descendant whose retry_of chain leads back to it. Siblings retried
// from one attempt both appear. A run of another team reads as no run.
// One statement walks retry_of up from runID, then down from every run
// that walk reached: each of those descends from the root, so the result
// is the root's tree, cycles included.
func (t *Tenant) ListRunRetryTree(ctx context.Context, runID string) (_ []RetryTreeMember, err error) {
	if runID == "" {
		return nil, nil
	}
	team := string(t.team)
	rows, err := t.s.query(ctx, retryTreeSQL, team, runID, team, team, team)
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
	var out []RetryTreeMember
	for rows.Next() {
		var m RetryTreeMember
		var created int64
		if err := rows.Scan(&m.ID, &m.Number, &created); err != nil {
			return nil, err
		}
		m.CreatedAt = time.Unix(0, created).UTC()
		out = append(out, m)
	}
	return out, rows.Err()
}
