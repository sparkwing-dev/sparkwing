package orchestrator

import (
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

func maskEventPayload(masker *secrets.Masker, payload []byte) []byte {
	return masker.MaskJSON(payload)
}
