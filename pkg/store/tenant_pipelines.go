package store

import (
	"context"
	"database/sql"
	"sort"
	"time"
)

// PipelineSummary is one pipeline a team has run, queued or scheduled, with
// its newest run when it has one.
type PipelineSummary struct {
	Name string `json:"name"`
	// LastActivityAt is the newest of the pipeline's last run start and last
	// trigger; zero for a pipeline known only from a schedule.
	LastActivityAt time.Time `json:"last_activity_at,omitzero"`
	// LastRunID, LastStatus, LastRunAt and LastFinishedAt describe the newest
	// run by start time, and are empty for a pipeline that has not run.
	LastRunID      string     `json:"last_run_id,omitempty"`
	LastStatus     string     `json:"last_status,omitempty"`
	LastRunAt      *time.Time `json:"last_run_at,omitempty"`
	LastFinishedAt *time.Time `json:"last_finished_at,omitempty"`
}

// ListPipelines returns up to limit of t's pipelines, most recently active
// first, then by name. A pipeline counts once it has a run, a trigger or a
// schedule in t's team.
func (t *Tenant) ListPipelines(ctx context.Context, limit int) ([]PipelineSummary, error) {
	if limit <= 0 {
		return []PipelineSummary{}, nil
	}
	byName := map[string]*PipelineSummary{}
	if err := t.latestRunPerPipeline(ctx, limit, byName); err != nil {
		return nil, err
	}
	if err := t.latestTriggerPerPipeline(ctx, limit, byName); err != nil {
		return nil, err
	}
	if err := t.scheduledPipelines(ctx, limit, byName); err != nil {
		return nil, err
	}
	out := make([]PipelineSummary, 0, len(byName))
	for _, p := range byName {
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].LastActivityAt.Equal(out[j].LastActivityAt) {
			return out[i].LastActivityAt.After(out[j].LastActivityAt)
		}
		return out[i].Name < out[j].Name
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// safety: each source is cut to limit before the merge. That is exact, since
// a pipeline in the merged top limit is in the top limit of the source that
// gave it its newest time.
func (t *Tenant) latestRunPerPipeline(ctx context.Context, limit int, byName map[string]*PipelineSummary) (err error) {
	rows, err := t.s.query(ctx, `
SELECT r.pipeline, r.id, r.status, r.started_at, r.finished_at
  FROM runs r
  JOIN (SELECT pipeline, MAX(started_at) AS latest
          FROM runs
         WHERE team = ?
         GROUP BY pipeline
         ORDER BY latest DESC, pipeline
         LIMIT ?) m
    ON r.pipeline = m.pipeline AND r.started_at = m.latest
 WHERE r.team = ?
 ORDER BY r.started_at DESC, r.id DESC`, string(t.team), limit, string(t.team))
	if err != nil {
		return err
	}
	defer closeRowsInto(rows, &err)
	for rows.Next() {
		var name, id, status string
		var started int64
		var finished sql.NullInt64
		if err := rows.Scan(&name, &id, &status, &started, &finished); err != nil {
			return err
		}
		if _, seen := byName[name]; seen {
			continue
		}
		at := time.Unix(0, started).UTC()
		p := &PipelineSummary{Name: name, LastActivityAt: at, LastRunID: id, LastStatus: status, LastRunAt: &at}
		if finished.Valid {
			f := time.Unix(0, finished.Int64).UTC()
			p.LastFinishedAt = &f
		}
		byName[name] = p
	}
	return rows.Err()
}

func (t *Tenant) latestTriggerPerPipeline(ctx context.Context, limit int, byName map[string]*PipelineSummary) (err error) {
	rows, err := t.s.query(ctx, `
SELECT pipeline, MAX(created_at) AS latest
  FROM triggers
 WHERE team = ?
 GROUP BY pipeline
 ORDER BY latest DESC, pipeline
 LIMIT ?`, string(t.team), limit)
	if err != nil {
		return err
	}
	defer closeRowsInto(rows, &err)
	for rows.Next() {
		var name string
		var created int64
		if err := rows.Scan(&name, &created); err != nil {
			return err
		}
		at := time.Unix(0, created).UTC()
		if p, ok := byName[name]; ok {
			if at.After(p.LastActivityAt) {
				p.LastActivityAt = at
			}
			continue
		}
		byName[name] = &PipelineSummary{Name: name, LastActivityAt: at}
	}
	return rows.Err()
}

func (t *Tenant) scheduledPipelines(ctx context.Context, limit int, byName map[string]*PipelineSummary) (err error) {
	rows, err := t.s.query(ctx, `
SELECT DISTINCT pipeline
  FROM cron_schedules
 WHERE team = ? AND declared = 1
 ORDER BY pipeline
 LIMIT ?`, string(t.team), limit)
	if err != nil {
		return err
	}
	defer closeRowsInto(rows, &err)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return err
		}
		if _, ok := byName[name]; !ok {
			byName[name] = &PipelineSummary{Name: name}
		}
	}
	return rows.Err()
}
