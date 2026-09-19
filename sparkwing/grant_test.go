package sparkwing_test

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func grantedCtx(ctx context.Context) context.Context {
	return sparkwing.Grant(ctx)
}
