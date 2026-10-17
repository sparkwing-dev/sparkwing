package controller

import (
	"context"
	"errors"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// ReconcileProfileFolds delivers due terminal profile work under the completion lease.
func (s *Server) ReconcileProfileFolds(ctx context.Context) (err error) {
	op := s.store.AsOperator()
	lease, err := op.AcquireSingletonLease(ctx, "completion", s.leaseHolder, time.Minute)
	if errors.Is(err, store.ErrLockHeld) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, op.ReleaseSingletonLease(context.WithoutCancel(ctx), lease)) }()
	effects, err := op.DueProfileFolds(ctx, lease, 100)
	if err != nil {
		return err
	}
	for _, effect := range effects {
		tenant, err := s.store.ForTeam(ctx, effect.Team)
		if err != nil {
			return err
		}
		delivery := store.EffectDelivery{RunID: effect.RunID, Effect: effect.Effect, Revision: effect.DesiredRev, Lease: lease}
		if err := s.deliverProfileFold(ctx, tenant, delivery); err != nil {
			if retryErr := tenant.RetryRunEffect(ctx, delivery, time.Now().Add(time.Second*time.Duration(1<<min(effect.Attempts, 6)))); retryErr != nil {
				return errors.Join(err, retryErr)
			}
			s.logger.Error("fold run profile", "team", effect.Team, "run_id", effect.RunID, "err", err)
		}
	}
	return nil
}

func (s *Server) deliverProfileFold(ctx context.Context, tenant *store.Tenant, delivery store.EffectDelivery) error {
	run, err := tenant.GetRun(ctx, delivery.RunID)
	if err != nil {
		return err
	}
	observations, err := runProfileObservations(ctx, tenant, run)
	if err != nil {
		return err
	}
	applied, err := tenant.ApplyProfileFold(ctx, delivery, observations)
	if err != nil {
		return err
	}
	if applied {
		for node := range observations {
			s.emitNodeDrift(ctx, tenant, run, node)
		}
	}
	return nil
}
