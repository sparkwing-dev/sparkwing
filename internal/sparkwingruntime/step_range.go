package sparkwingruntime

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func WithStepRange(ctx context.Context, startAt, stopAt string) context.Context {
	if startAt == "" && stopAt == "" {
		return ctx
	}
	return context.WithValue(ctx, sparkwing.RuntimePlumbing.Keys.StepRange, [2]string{startAt, stopAt})
}

func StepRangeFromContext(ctx context.Context) (startAt, stopAt string) {
	v, _ := ctx.Value(sparkwing.RuntimePlumbing.Keys.StepRange).([2]string)
	return v[0], v[1]
}
