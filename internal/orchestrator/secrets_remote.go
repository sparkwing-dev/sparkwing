package orchestrator

import (
	"context"
	"errors"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// safety: only a run the controller dispatched (a cluster worker or a remote
// runner node) reads controller secrets; a local run reads this machine's.
func controllerSecretSource(ctx context.Context, c *client.Client, runID string) secrets.Source {
	return secrets.SourceFunc(func(name string) (string, bool, error) {
		sec, gerr := c.GetSecretForRun(ctx, name, runID)
		if gerr != nil {
			if errors.Is(gerr, store.ErrNotFound) {
				return "", false, secrets.ErrSecretMissing
			}
			return "", false, gerr
		}
		return sec.Value, sec.Masked, nil
	})
}
