package orchestrator

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/sparkwingruntime"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// rehydratePipelineSecrets re-resolves the pipeline's declared
// secrets against the SecretResolver already installed on ctx (the
// pod's controller-backed source). The orchestrator persisted only
// the SecretsField list (names + required/optional flags); values
// never traveled with the snapshot.
//
// Returns nil, nil when the pipeline does not implement
// SecretsProvider and the snapshot carries no SecretsField entries.
// A required-secret miss propagates as an error -- by the time the
// pod runs this, the orchestrator-side fail-fast already passed, so
// a pod-side miss signals an environment drift between controller
// and runner that's worth failing loudly.
func rehydratePipelineSecrets(ctx context.Context, _ []byte, reg *sparkwing.Registration) (any, error) {
	if reg == nil {
		return nil, nil
	}
	return sparkwingruntime.ResolvePipelineSecrets(ctx, reg, nil)
}
