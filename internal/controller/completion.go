package controller

import (
	"context"
	"errors"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// ReconcileRunEffects delivers committed effects.
func (s *Server) ReconcileRunEffects(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	op := s.operator
	profiles, err := op.DueProfileFolds(ctx, 100)
	if err != nil {
		return err
	}
	checks, err := op.DueGitHubChecks(ctx, 100)
	if err != nil {
		return err
	}
	effects := append(profiles, checks...)
	teams := make([]store.Team, 0, len(effects))
	for _, effect := range effects {
		teams = append(teams, effect.Team)
	}
	return op.EachTeam(ctx, teams, func(tenant *store.Tenant) error {
		for _, effect := range effects {
			if effect.Team != tenant.Team() {
				continue
			}
			delivery := store.EffectDelivery{RunID: effect.RunID, Effect: effect.Effect, Revision: effect.DesiredRev}
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
	})
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
			return s.githubAppUnavailable()
		}
		ctx, cancel := context.WithTimeout(ctx, githubCheckTimeout)
		defer cancel()
		reporter := githubCheckReporter{client: s.githubApp.client, resolveTenant: s.resolveTenant, operator: s.operator, dashboardURL: s.dashboardURL}
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
	_, err = tenant.ApplyProfileFold(ctx, delivery, observations)
	return err
}
