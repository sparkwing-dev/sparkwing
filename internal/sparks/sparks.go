package sparks

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/gotoolchain"
)

func ResolveAndWrite(ctx context.Context, sparkwingDir string, m *Manifest) (bool, error) {
	if m == nil || len(m.Libraries) == 0 {
		return false, nil
	}
	ctx = gotoolchain.WithSession(ctx, nil, nil)
	resolver := NewResolverFromEnv()
	resolver.Dir = sparkwingDir
	resolved, err := resolver.Resolve(ctx, m)
	if err != nil {
		return false, err
	}
	return WriteOverlay(ctx, sparkwingDir, resolved)
}
