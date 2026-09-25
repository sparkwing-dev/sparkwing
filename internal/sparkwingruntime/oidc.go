package sparkwingruntime

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// WithOIDCTokenSource installs the source sparkwing.OIDCToken reads.
func WithOIDCTokenSource(ctx context.Context, src sparkwing.OIDCTokenSource) context.Context {
	return context.WithValue(ctx, sparkwing.RuntimePlumbing.Keys.OIDCTokenSource, src)
}
