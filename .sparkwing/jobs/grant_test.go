package jobs

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// safety: a Plan body is the exception and takes a bare context, because Plan
// purity is what these tests put under test.
func grantedCtx(ctx context.Context) context.Context {
	return sparkwing.Grant(ctx)
}
