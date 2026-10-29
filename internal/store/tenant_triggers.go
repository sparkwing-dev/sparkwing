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
