package sparkwingruntime

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func WithAdmission(ctx context.Context, a *sparkwing.Admission) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, sparkwing.RuntimePlumbing.Keys.Admission, a)
}
