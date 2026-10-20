package controller

import (
	"context"
	"errors"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// ReconcileRunEffects delivers committed effects under the completion lease.
func (s *Server) ReconcileRunEffects(ctx context.Context) (err error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	op := s.store.AsOperator()
	lease, err := op.AcquireSingletonLease(ctx, "completion", s.leaseHolder, time.Minute)
	if errors.Is(err, store.ErrLockHeld) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, op.ReleaseSingletonLease(context.WithoutCancel(ctx), lease)) }()
	profiles, err := op.DueProfileFolds(ctx, lease, 100)
	if err != nil {
		return err
	}
	checks, err := op.DueGitHubChecks(ctx, lease, 100)
	if err != nil {
		return err
	}
	for _, effect := range append(profiles, checks...) {
		tenant, err := s.store.ForTeam(ctx, effect.Team)
		if err != nil {
			return err
		}
		delivery := store.EffectDelivery{RunID: effect.RunID, Effect: effect.Effect, Revision: effect.DesiredRev, Lease: lease}
		if err := s.deliverRunEffect(ctx, tenant, effect, delivery); err != nil {
			var recordErr error
			if errors.Is(err, errInvalidGitHubCompletion) {
				recordErr = tenant.RejectRunEffect(ctx, delivery)
			} else {
				recordErr = tenant.RetryRunEffect(ctx, delivery, effect.ObservedAt.Add(time.Second*time.Duration(1<<min(effect.Attempts, 6))))
			}
			if recordErr != nil {
				return errors.Join(err, recordErr)
			}
			s.logger.Error("deliver run effect", "effect", effect.Effect, "team", effect.Team, "run_id", effect.RunID, "err", err)
		}
	}
	return nil
}

func (s *Server) deliverRunEffect(ctx context.Context, tenant *store.Tenant, effect store.DesiredRunEffect, delivery store.EffectDelivery) error {
	if effect.Effect == store.EffectProfileFold {
		return s.deliverProfileFold(ctx, tenant, delivery)
	}
	update, relevant, err := s.githubAppCheckUpdate(ctx, tenant, effect)
	if err != nil {
		return err
	}
	if relevant {
		if s.githubApp == nil {
			return errors.New("GitHub App is unavailable for completion")
		}
		ctx, cancel := context.WithTimeout(ctx, githubStatusTimeout)
		defer cancel()
		reporter := githubCheckReporter{client: s.githubApp.client, store: s.store, dashboardURL: s.dashboardURL, missingChecks: func() { s.warnMissingChecks(update.installation) }}
		if err := reporter.deliver(ctx, update); err != nil {
			return err
		}
	}
	return tenant.AcknowledgeRunEffect(ctx, delivery)
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
