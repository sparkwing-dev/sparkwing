package controller

import (
	"context"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// CloudRetentionDays is the event and node-metric retention window a
// multi-team controller starts with when its operator has set none. Runs past
// it release their stored bytes, so a free team's event share is a window of
// history rather than a lifetime.
const CloudRetentionDays = 30

// safety: the defaults are written only where the operator has set nothing,
// so a window an operator chose, zero included, survives every restart.
func (s *Server) seedCloudRetention(ctx context.Context) {
	if !s.MultiTeam() {
		return
	}
	seeded, err := s.store.SeedStorageRetention(ctx, CloudRetentionDays)
	if err != nil {
		s.logger.Error("seeding the cloud retention window failed", "err", err)
		return
	}
	if seeded {
		s.logger.Info("seeded the cloud retention window", "event_retention_days", CloudRetentionDays,
			"node_metric_retention_days", CloudRetentionDays)
	}
}

func (s *Server) maintainTeamStorage(ctx context.Context, now time.Time) {
	expired, err := s.store.ExpireRetainedRuns(ctx, now)
	if err != nil {
		s.logger.Error("releasing runs past retention failed", "err", err)
	}
	for _, e := range expired {
		s.logger.Info("released storage past retention", "team", e.Team, "runs", e.Runs, "bytes", e.Bytes)
	}
	pruned, err := s.store.PruneSpentIdentity(ctx, now)
	if err != nil {
		s.logger.Error("pruning spent identity rows failed", "err", err)
	} else if pruned != (store.IdentityPrune{}) {
		s.logger.Info("pruned spent identity rows",
			"invitations", pruned.Invitations, "tokens", pruned.Tokens,
			"sessions", pruned.Sessions, "github_runner_credentials", pruned.GitHubRunnerCredentials)
	}
}
