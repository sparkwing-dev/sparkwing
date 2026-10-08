package sparkwing

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/secretsource"
	"github.com/sparkwing-dev/sparkwing/pkg/backends"
)

// NewSecretResolverFromSpec builds a SecretResolver for the secrets
// surface from a backends.Spec. The returned resolver is uncached and
// unmasked; the orchestrator wraps the one it installs with the run's
// cache and log masker.
//
// Supported types on the secrets surface:
//
//   - controller: HTTPS GET against spec.URL + /api/v1/secrets/<name>
//     using spec.ResolvedToken() in the Authorization header.
//   - filesystem: reads a dotenv file at spec.Path. Keys without
//     values resolve to ErrSecretMissing rather than the empty string.
//   - env: looks up os.Getenv(spec.Prefix + name). An unset env var
//     resolves to ErrSecretMissing.
//   - none: every lookup fails, naming the missing backend.
func NewSecretResolverFromSpec(_ context.Context, spec backends.Spec) (SecretResolver, error) {
	r, err := secretsource.FromSpec(spec)
	if err != nil {
		return nil, err
	}
	return r, nil
}
