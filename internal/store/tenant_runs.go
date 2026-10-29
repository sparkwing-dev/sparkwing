package store

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strings"
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

// ListRunRetryTree returns every run of t's team in the retry tree runID
// belongs to, oldest first: the root found by walking retry_of upward, plus
// every descendant whose retry_of chain leads back to it. Siblings retried
// from one attempt both appear. A run of another team reads as no run.
//
// safety: every step of the walk, up and down, carries the team predicate,
// because retry_of is a bare id and a pointer that crosses teams would
// otherwise hand the caller the other team's run.
func (t *Tenant) ListRunRetryTree(ctx context.Context, runID string) ([]*Run, error) {
	if runID == "" {
		return nil, nil
	}
	visited := make(map[string]bool)
	rootID := ""
	next := runID
	for !visited[next] {
		visited[next] = true
		var parent string
		err := t.s.queryRow(ctx,
			`SELECT retry_of FROM runs WHERE team = ? AND id = ?`, string(t.team), next).Scan(&parent)
		if errors.Is(err, sql.ErrNoRows) {
			break
		}
		if err != nil {
			return nil, err
		}
		rootID = next
		if parent == "" || parent == next {
			break
		}
		next = parent
	}
	if rootID == "" {
		return nil, nil
	}
	root, err := t.GetRun(ctx, rootID)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	collected := map[string]*Run{rootID: root}
	frontier := []string{rootID}
	for len(frontier) > 0 {
		var below []string
		for _, id := range frontier {
			children, err := t.retriesOf(ctx, id)
			if err != nil {
				return nil, err
			}
			for _, r := range children {
				if _, dup := collected[r.ID]; dup {
					continue
				}
				collected[r.ID] = r
				below = append(below, r.ID)
			}
		}
		frontier = below
	}
	out := make([]*Run, 0, len(collected))
	for _, r := range collected {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].Number < out[j].Number
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out, nil
}

func (t *Tenant) retriesOf(ctx context.Context, id string) (_ []*Run, err error) {
	rows, err := t.s.query(ctx,
		`SELECT `+runColumns+` FROM runs WHERE team = ? AND retry_of = ?`, string(t.team), id)
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
	var out []*Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	if err := t.s.refreshRunPresentations(ctx, out); err != nil {
		return nil, err
	}
	return out, nil
}
