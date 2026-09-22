package store

import "context"

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

// CreateRetryWithRun writes a retry's trigger and pending run into the team
// that owns sourceRunID, in one transaction with the read that finds it. A
// retry is asked for by run id, so the source run's team is the only one
// that can own it; filing it anywhere else hands the rerun, its repository
// and its secrets to another team's runners. ErrNotFound when the source
// run does not exist.
func (s *Store) CreateRetryWithRun(ctx context.Context, sourceRunID string, trig Trigger, r Run) error {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackOrLog(tx)
	team, found, err := runOwnerTx(ctx, tx, sourceRunID)
	if err != nil {
		return err
	}
	if !found {
		return notFound("run", sourceRunID)
	}
	if err := createTriggerTx(ctx, tx, team, trig); err != nil {
		return err
	}
	if err := s.createRunTx(ctx, tx, team, r); err != nil {
		return err
	}
	return tx.Commit()
}
