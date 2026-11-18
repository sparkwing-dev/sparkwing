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
	var pendingNoKey *store.SealedRotation
	for backoff := time.Second; ctx.Err() == nil; backoff = min(backoff*2, time.Minute) {
		var result store.SealedRotation
		var err error
		if pendingNoKey != nil {
			result = *pendingNoKey
		} else {
			result, err = s.operator.SealPlaintextValues(ctx, cipher)
		}
		outcome, reason := "success", "none"
		noKey := pendingNoKey != nil || cipher == nil || errors.Is(err, secrets.ErrKeyRefused)
		if err != nil {
			outcome, reason = "failed", "store_error"
		}
		if noKey {
			outcome, reason = "skipped", "no_key"
			pendingNoKey = &result
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
		if !noKey || auditErr == nil {
			s.logger.Info("startup plaintext sealing", "source", "startup", "outcome", outcome, "reason", reason, "sealed", result.PlaintextRunArguments, "already_sealed", result.AlreadySealed, "unreadable", len(result.Unreadable), "previous_key_only", result.PreviousKeyOnly, "audit_recorded", auditErr == nil)
		}
		if auditErr == nil && (noKey || err == nil) {
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
