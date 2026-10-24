package store

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/runstate"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

// Display projects accepted classifications without changing the immutable seal.
func (r RunRow) Display() runspec.Display {
	if r.Spec == nil {
		return runspec.DisplayRequest(r.Request)
	}
	spec := *r.Spec
	spec.Resolved.SecretRegistration.Defaults = append([]runspec.SecretValue(nil), spec.Resolved.SecretRegistration.Defaults...)
	for _, expansion := range r.expansions {
		spec.Resolved.SecretRegistration.Defaults = append(spec.Resolved.SecretRegistration.Defaults, expansion.Registration.Defaults...)
	}
	return runspec.DisplaySpec(spec)
}

func (s *Store) refreshRunPresentations(ctx context.Context, runs []*Run) error {
	if len(runs) == 0 {
		return nil
	}
	tx, err := s.beginSnapshotReadTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackOrLog(tx)
	for _, run := range runs {
		row, err := readRunTx(ctx, tx, run.team, run.ID)
		if err != nil {
			return err
		}
		display := row.Display()
		run.Args = display.RequestedArgs
		if row.Spec != nil {
			run.Args = display.Args
		}
		run.Invocation = map[string]any{InvocationSecretArgsKey: display.SecretArgs}
	}
	return tx.Commit()
}

func (s *Store) refreshTriggerPresentations(ctx context.Context, triggers []*Trigger) error {
	runs := make([]*Run, len(triggers))
	for i, trigger := range triggers {
		runs[i] = &Run{team: trigger.Team, RunSummary: runstate.RunSummary{ID: trigger.ID}}
	}
	if err := s.refreshRunPresentations(ctx, runs); err != nil {
		return err
	}
	for i, trigger := range triggers {
		trigger.Args = runs[i].Args
	}
	return nil
}
