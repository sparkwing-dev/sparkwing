package store

import (
	"context"
	"database/sql"
	"errors"
)

// CreateTrigger writes a pending trigger into t's team.
func (t *Tenant) CreateTrigger(ctx context.Context, trig Trigger) error {
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackOrLog(tx)
	if err := createTriggerTx(ctx, tx, t.team, trig); err != nil {
		return err
	}
	return tx.Commit()
}

// CreateTriggerWithRun writes a trigger and the pending run it names into
// t's team in one transaction, so a guard that refuses the run leaves no
// trigger behind for a worker to claim. It maps the same duplicate-key
// errors [Tenant.CreateTrigger] does.
func (t *Tenant) CreateTriggerWithRun(ctx context.Context, trig Trigger, r Run) error {
	return t.createTriggerWithRun(ctx, trig, r, nil)
}

func (t *Tenant) createTriggerWithRun(ctx context.Context, trig Trigger, r Run, automation *GitHubAppAutomation) error {
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackOrLog(tx)
	if err := createTriggerTx(ctx, tx, t.team, trig); err != nil {
		return err
	}
	if err := t.s.createRunTx(ctx, tx, t.team, r); err != nil {
		return err
	}
	if automation != nil {
		if err := t.checkGitHubAppAutomationTx(ctx, tx, *automation); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// FindTriggerByIdempotencyKey returns the trigger in t's team that
// already claimed key for pipeline, or ErrNotFound. The unique index the
// key races on spans every team, so a key another team holds loses the
// insert and still reads as not found here rather than as their trigger.
func (t *Tenant) FindTriggerByIdempotencyKey(ctx context.Context, pipeline, key string) (*Trigger, error) {
	if key == "" || pipeline == "" {
		return nil, notFound("trigger for idempotency key", key)
	}
	var id string
	err := t.s.queryRow(ctx,
		`SELECT id FROM triggers WHERE team = ? AND pipeline = ? AND idempotency_key = ?`,
		string(t.team), pipeline, key).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, notFound("trigger for idempotency key", key)
	}
	if err != nil {
		return nil, err
	}
	return t.GetTrigger(ctx, id)
}

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
