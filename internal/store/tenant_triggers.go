package store

import (
	"context"
	"database/sql"
	"errors"
)

// FindTriggerByWebhookReplay returns the trigger in t's team matching the
// signed material's replay key. An unsigned delivery header cannot select a run.
func (t *Tenant) FindTriggerByWebhookReplay(ctx context.Context, replayKey string) (*Trigger, error) {
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

// CountPendingTriggers returns how many of t's triggers are waiting to be
// claimed. Another team's backlog is not counted, so it cannot shed t's
// submissions.
func (t *Tenant) CountPendingTriggers(ctx context.Context) (int, error) {
	var n int
	if err := t.s.queryRow(ctx,
		`SELECT COUNT(*) FROM triggers WHERE team = ? AND status = ?`,
		string(t.team), triggerStatusPending).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}
