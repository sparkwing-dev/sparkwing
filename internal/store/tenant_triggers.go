package store

import (
	"context"
	"database/sql"
	"errors"
)

// FindGitHubAppTrigger returns the trigger in t's team matching the
// signed material's digest and pipeline. An unsigned delivery header cannot select a run.
func (t *Tenant) FindGitHubAppTrigger(ctx context.Context, digest, pipeline string) (*Trigger, error) {
	replayKey := gitHubAppReplayKey(digest, pipeline)
	if replayKey == "" {
		return nil, notFound("trigger for webhook replay", replayKey)
	}
	var id string
	err := t.s.queryRow(ctx,
		`SELECT id FROM triggers
		  WHERE team = ?
		    AND webhook_replay_key != '' AND webhook_replay_key = ?`,
		string(t.team), replayKey).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, notFound("trigger for webhook replay", replayKey)
	}
	if err != nil {
		return nil, err
	}
	return t.GetTrigger(ctx, id)
}

// countPendingTriggers returns how many of t's triggers are waiting to be
// claimed. Another team's backlog is not counted, so it cannot shed t's
// submissions.
func (t *Tenant) countPendingTriggers(ctx context.Context) (int, error) {
	var n int
	if err := t.s.queryRow(ctx,
		`SELECT COUNT(*) FROM triggers WHERE team = ? AND status = ?`,
		string(t.team), triggerStatusPending).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}
