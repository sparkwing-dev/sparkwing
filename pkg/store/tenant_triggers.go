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
	return t.s.GetTrigger(ctx, id)
}

// FindTriggerByWebhookReplay returns the trigger in t's team a refused
// webhook delivery collided with, as [Store.FindTriggerByWebhookReplay]
// does. The delivery id is an unsigned header, so a delivery signed for
// one team naming another team's delivery id reads as not found rather
// than as that team's run.
func (t *Tenant) FindTriggerByWebhookReplay(ctx context.Context, replayKey, delivery string) (*Trigger, error) {
	if replayKey == "" && delivery == "" {
		return nil, notFound("trigger for webhook delivery", delivery)
	}
	var id string
	err := t.s.queryRow(ctx,
		`SELECT id FROM triggers
		  WHERE team = ?
		    AND ((webhook_replay_key != '' AND webhook_replay_key = ?)
		      OR (webhook_delivery != '' AND webhook_delivery = ?))
		  ORDER BY created_at LIMIT 1`,
		string(t.team), replayKey, delivery).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, notFound("trigger for webhook delivery", delivery)
	}
	if err != nil {
		return nil, err
	}
	return t.s.GetTrigger(ctx, id)
}
