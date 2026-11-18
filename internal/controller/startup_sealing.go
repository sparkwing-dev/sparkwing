package controller

import (
	"context"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// RunStartupSealing seals legacy plaintext in the background lifecycle owned by its caller.
// It returns after a successful pass or cancellation; operational failures retry.
func (s *Server) RunStartupSealing(ctx context.Context) {
	cipher, _ := s.secretsCipher.(store.SealedRotationCipher)
	if s.operator == nil {
		return
	}
	for backoff := time.Second; ctx.Err() == nil; backoff = min(backoff*2, time.Minute) {
		result, err := s.operator.SealPlaintextValues(ctx, cipher)
		outcome := "success"
		if err != nil {
			outcome = "failed"
		}
		if ctx.Err() != nil {
			outcome = "cancelled"
		}
		auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		auditErr := s.operator.RecordStartupSealing(auditCtx, result, outcome)
		cancel()
		if auditErr != nil && outcome == "success" {
			outcome = "failed"
		}
		s.logger.Info("startup plaintext sealing", "source", "startup", "outcome", outcome, "sealed", result.PlaintextRunArguments, "already_sealed", result.AlreadySealed, "unreadable", len(result.Unreadable), "previous_key_only", result.PreviousKeyOnly, "audit_recorded", auditErr == nil)
		if err == nil && auditErr == nil {
			return
		}
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
