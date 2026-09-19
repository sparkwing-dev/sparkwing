package sparkwing

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/sparkwing/planguard"
)

// Grant returns ctx marked as deliberately used outside a pipeline run,
// so side-effect helpers such as [Bash] and the git and docker helpers
// allow it. Reach for it in a tool or a test that calls an SDK helper
// with no run around it.
//
// Inside a callback, thread the context the callback was handed instead.
// Applied to a sealed context, Grant leaves it sealed.
func Grant(ctx context.Context) context.Context {
	return planguard.Grant(ctx)
}
