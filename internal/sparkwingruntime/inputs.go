package sparkwingruntime

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func WithInputs(ctx context.Context, in any) context.Context {
	return context.WithValue(ctx, sparkwing.RuntimePlumbing.Keys.Inputs, in)
}
