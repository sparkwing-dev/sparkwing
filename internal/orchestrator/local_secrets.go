package orchestrator

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/localsecrets"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

// safety: a hosted run asks the daemon, which holds the key and serves the
// same rows as `sparkwing secrets` and the dashboard; a run no daemon hosts
// reads the machine's runs store itself, never the standalone store it
// records its own state in, because that is not where secrets are kept.
func localSecretsFor(ctx context.Context, paths Paths, b Backends, runID, pipeline string) secrets.Source {
	if c, ok := b.State.(*client.Client); ok && b.APISocket != "" {
		return localsecrets.SocketSource(ctx, c, runID)
	}
	return localsecrets.StoreSource(paths.StateDB(), pipeline)
}
