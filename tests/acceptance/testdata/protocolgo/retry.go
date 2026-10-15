package protocolgo

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type retryProbe struct {
	sparkwing.Base
	sparkwing.Produces[int]
	calls int
}

func (p *retryProbe) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	source := sparkwing.Step(w, "source", func(context.Context) (int, error) {
		p.calls++
		return p.calls, nil
	})
	return sparkwing.Step(w, "result", func(ctx context.Context) (int, error) {
		return sparkwing.StepGet[int](ctx, source), nil
	}).Needs(source), nil
}
