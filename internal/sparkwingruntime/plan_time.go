package sparkwingruntime

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/sparkwing/planguard"
)

func GuardPlanTime(ctx context.Context, what string) {
	planguard.Guard(ctx, what)
}

func IsPlanTime(ctx context.Context) bool {
	return planguard.Active(ctx)
}
