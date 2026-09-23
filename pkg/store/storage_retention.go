package store

import (
	"context"
	"fmt"
	"time"
)

// TeamRetentionExpiry is what one retention pass released for one team.
type TeamRetentionExpiry struct {
	Team  Team
	Runs  int64
	Bytes int64
}

// SeedStorageRetention writes days as the event and node-metric retention
// windows where the operator has written neither, and reports whether it
// wrote anything. A window the operator set, zero included, is kept.
func (s *Store) SeedStorageRetention(ctx context.Context, days int64) (bool, error) {
	if days <= 0 {
		return false, nil
	}
	seeded := false
	for _, key := range []string{metaKeyEventRetentionDays, metaKeyNodeMetricRetentionDays} {
		res, err := s.exec(ctx, `
INSERT INTO sparkwing_meta (key, value, updated_at) VALUES (?, ?, ?)
ON CONFLICT (key) DO NOTHING`, key, fmt.Sprint(days), time.Now().UnixNano())
		if err != nil {
			return seeded, err
		}
		if n, err := res.RowsAffected(); err == nil && n > 0 {
			seeded = true
		}
	}
	return seeded, nil
}

// ExpireRetainedRuns releases the stored bytes of every run that finished
// before the event retention window: its events and its usage row go. The
// window is measured from when the run finished, so a run that took longer
// than the window keeps its history for a whole window after it ends. A
// window of zero releases nothing, and a run still pending or running keeps
// everything.
func (s *Store) ExpireRetainedRuns(ctx context.Context, now time.Time) ([]TeamRetentionExpiry, error) {
	settings, err := s.StorageSettings(ctx)
	if err != nil || settings.EventRetentionDays <= 0 {
		return nil, err
	}
	cutoff := retentionCutoff(now, settings.EventRetentionDays)
	byTeam := map[Team]*TeamRetentionExpiry{}
	var order []Team
	for range retentionSweepMaxBatches {
		if err := ctx.Err(); err != nil {
			return collectExpiries(byTeam, order), err
		}
		batch, err := s.expiredRetainedRuns(ctx, cutoff)
		if err != nil {
			return collectExpiries(byTeam, order), err
		}
		for _, run := range batch {
			if _, err := s.expireRunStorage(ctx, run.principal, run.id); err != nil {
				return collectExpiries(byTeam, order), err
			}
			entry, ok := byTeam[run.team]
			if !ok {
				entry = &TeamRetentionExpiry{Team: run.team}
				byTeam[run.team] = entry
				order = append(order, run.team)
			}
			entry.Runs++
			entry.Bytes += run.bytes
		}
		if len(batch) < storageAllowanceSweepMaxRuns {
			break
		}
	}
	return collectExpiries(byTeam, order), nil
}

func collectExpiries(byTeam map[Team]*TeamRetentionExpiry, order []Team) []TeamRetentionExpiry {
	out := make([]TeamRetentionExpiry, 0, len(order))
	for _, team := range order {
		out = append(out, *byTeam[team])
	}
	return out
}

type expiredRun struct {
	team      Team
	principal string
	id        string
	bytes     int64
}

func (s *Store) expiredRetainedRuns(ctx context.Context, cutoff time.Time) (_ []expiredRun, err error) {
	rows, err := s.query(ctx, `
SELECT r.team, u.principal, u.run_id, u.bytes
  FROM storage_run_usage u JOIN runs r ON r.id = u.run_id
 WHERE r.`+runTerminalIn+` AND r.finished_at IS NOT NULL AND r.finished_at < ?
 ORDER BY r.finished_at ASC, u.run_id ASC
 LIMIT ?`, cutoff.UnixNano(), storageAllowanceSweepMaxRuns)
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
	var out []expiredRun
	for rows.Next() {
		var run expiredRun
		if err := rows.Scan(&run.team, &run.principal, &run.id, &run.bytes); err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	return out, rows.Err()
}
