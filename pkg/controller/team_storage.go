package controller

import (
	"context"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// CloudRetentionDays is the run retention default for a multi-team controller.
// An explicit operator value, including zero, takes precedence.
const CloudRetentionDays = 90

// safety: the defaults are written only where the operator has set nothing,
// so a window an operator chose, zero included, survives every restart.
func (s *Server) seedCloudRetention(ctx context.Context) {
	if !s.MultiTeam() {
		return
	}
	seeded, err := s.store.AsOperator().SeedRunRetention(ctx, CloudRetentionDays)
	if err != nil {
		s.logger.Error("seeding the cloud retention window failed", "err", err)
		return
	}
	if seeded {
		s.logger.Info("seeded the cloud retention window", "run_retention_days", CloudRetentionDays)
	}
}

func (s *Server) maintainTeamStorage(ctx context.Context, now time.Time) {
	pruned, err := s.store.PruneSpentIdentity(ctx, now)
	if err != nil {
		s.logger.Error("pruning spent identity rows failed", "err", err)
	} else if pruned != (store.IdentityPrune{}) {
		s.logger.Info("pruned spent identity rows",
			"invitations", pruned.Invitations, "tokens", pruned.Tokens,
			"sessions", pruned.Sessions, "github_runner_credentials", pruned.GitHubRunnerCredentials)
	}
}
