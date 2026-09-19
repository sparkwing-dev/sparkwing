package git

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/sparkwing/planguard"
)

func grantedCtx(ctx context.Context) context.Context {
	return planguard.Grant(ctx)
}
