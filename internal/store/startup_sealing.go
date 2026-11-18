package store

import "context"

type auditStartupSealing struct {
	Source          string `json:"source"`
	Outcome         string `json:"outcome"`
	Sealed          int    `json:"sealed"`
	AlreadySealed   int    `json:"already_sealed"`
	Unreadable      int    `json:"unreadable"`
	PreviousKeyOnly int    `json:"previous_key_only"`
}

func (auditStartupSealing) isAuditPayload() {}

// RecordStartupSealing records sanitized counts for a controller sealing attempt.
func (o *Operator) RecordStartupSealing(ctx context.Context, result SealedRotation, outcome string) error {
	return o.s.inTx(ctx, func(tx *storeTx) error {
		return o.recordAudit(ctx, tx, auditContext{Actor: auditActor{system: auditController}}, auditStartupSealing{
			Source: "startup", Outcome: outcome, Sealed: result.PlaintextRunArguments, AlreadySealed: result.AlreadySealed, Unreadable: len(result.Unreadable), PreviousKeyOnly: result.PreviousKeyOnly,
		})
	})
}
