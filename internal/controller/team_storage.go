package controller

import "context"

// cloudRetentionDays is the run retention default for a multi-team controller.
// An explicit operator value, including zero, takes precedence.
const cloudRetentionDays = 90

// safety: the defaults are written only where the operator has set nothing,
// so a window an operator chose, zero included, survives every restart.
func (s *Server) seedCloudRetention(ctx context.Context) {
	if !s.deployment.MultiTeam() {
		return
	}
	seeded, err := s.operator.SeedRunRetention(ctx, cloudRetentionDays)
	if err != nil {
		s.logger.Error("seeding the cloud retention window failed", "err", err)
		return
	}
	if seeded {
		s.logger.Info("seeded the cloud retention window", "run_retention_days", cloudRetentionDays)
	}
}
