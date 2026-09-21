package sparkwingruntime

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func WithAdmission(ctx context.Context, admission sparkwing.Admission) context.Context {
	return context.WithValue(ctx, sparkwing.RuntimePlumbing.Keys.Admission, admission)
}
