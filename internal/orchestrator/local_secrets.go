package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"

	"github.com/sparkwing-dev/sparkwing/internal/localsecrets"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

// safety: a hosted run asks the daemon, which holds the key and serves the
// same rows as `sparkwing secrets` and the dashboard; a run no daemon hosts
// reads the machine's runs store itself, never the standalone store it
// records its own state in, because that is not where secrets are kept.
func localRunSecrets(ctx context.Context, paths Paths, opts Options, hosted Backends, sel hostedSelection) secrets.Source {
	if hosted.APISocket != "" {
		importClient := NewAPISocketClient(hosted.APISocket)
		defer importClient.CloseIdleConnections()
		if err := ImportLegacySecretsOverSocket(ctx, importClient); err != nil {
			fmt.Fprintf(os.Stderr, "sparkwing: %v\n", err)
		}
	} else {
		importLegacySecretsStandalone(ctx, paths, sel, opts.DryRun)
	}
	return localSecretsFor(ctx, paths, hosted, opts.RunID, opts.Pipeline)
}

func localSecretsFor(ctx context.Context, paths Paths, b Backends, runID, pipeline string) secrets.Source {
	if c, ok := b.State.(*client.Client); ok && b.APISocket != "" {
		return localsecrets.SocketSource(ctx, c, runID)
	}
	return localsecrets.StoreSource(paths.StateDB(), pipeline)
}

// safety: only a machine with no daemon at all lets a run write the shared
// runs store, because a daemon of another version may hold it at a schema
// this binary would migrate out from under it; that daemon imports the files
// itself once it runs this release.
func importLegacySecretsStandalone(ctx context.Context, paths Paths, sel hostedSelection, dryRun bool) {
	if dryRun || sel.standalone != standaloneNoDaemon {
		return
	}
	if err := ImportLegacySecretsInProcess(ctx, paths.StateDB()); err != nil {
		fmt.Fprintf(os.Stderr, "sparkwing: %v\n", err)
	}
}

// LocalAPIHealth is what the admission daemon reports about the controller
// API on its socket: Store is "ready", "absent" (no runs store yet, so no
// secrets either) or a fault, and Secrets is [APISecretsSealed] from a daemon
// that seals every secret it stores, empty from one that predates it.
type LocalAPIHealth struct {
	Store   string `json:"store"`
	Secrets string `json:"secrets"`
	// SecretsProblem is why the daemon refuses to seal or open a secret, such
	// as a store sealed under a key it was not given.
	SecretsProblem string `json:"secrets_problem,omitempty"`
}

// ReadLocalAPIHealth asks the daemon serving sock for its [LocalAPIHealth].
func ReadLocalAPIHealth(ctx context.Context, sock string) (LocalAPIHealth, error) {
	ctx, cancel := context.WithTimeout(ctx, hostedAPIProbeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, HostedAPIBaseURL+"/api/v1/health", nil)
	if err != nil {
		return LocalAPIHealth{}, err
	}
	probe := newAPIProbeClient(sock)
	defer probe.CloseIdleConnections()
	resp, err := probe.Do(req)
	if err != nil {
		return LocalAPIHealth{}, fmt.Errorf("controller API on %s: %w", sock, err)
	}
	defer func() { _ = resp.Body.Close() }()
	var health LocalAPIHealth
	if err := json.NewDecoder(resp.Body).Decode(&health); err != nil {
		return LocalAPIHealth{}, fmt.Errorf("%s did not answer GET /api/v1/health with a health report: %w", sock, err)
	}
	return health, nil
}
