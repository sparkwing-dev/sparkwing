package controller

import (
	"context"
	"errors"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// RunStartupSealing seals legacy plaintext in the background lifecycle owned by its caller.
// It returns after success, an unavailable key, or cancellation; operational failures retry.
func (s *Server) RunStartupSealing(ctx context.Context) {
	cipher, _ := s.secretsCipher.(store.SealedRotationCipher)
	if s.operator == nil {
		return
	}
	for backoff := time.Second; ctx.Err() == nil; backoff = min(backoff*2, time.Minute) {
		result, err := s.operator.SealPlaintextValues(ctx, cipher)
		outcome, reason := "success", "none"
		noKey := cipher == nil || errors.Is(err, secrets.ErrKeyRefused)
		if err != nil {
			outcome, reason = "failed", "store_error"
		}
		if noKey {
			outcome, reason = "skipped", "no_key"
		}
		if ctx.Err() != nil {
			outcome, reason = "cancelled", "cancelled"
		}
		auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		auditErr := s.operator.RecordStartupSealing(auditCtx, result, outcome)
		cancel()
		if auditErr != nil && outcome == "success" {
			outcome, reason = "failed", "audit_error"
		}
		s.logger.Info("startup plaintext sealing", "source", "startup", "outcome", outcome, "reason", reason, "sealed", result.PlaintextRunArguments, "already_sealed", result.AlreadySealed, "unreadable", len(result.Unreadable), "previous_key_only", result.PreviousKeyOnly, "audit_recorded", auditErr == nil)
		if noKey || (err == nil && auditErr == nil) {
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
