package protocolgo

import (
	"context"
	"fmt"
	"os"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type Observation struct {
	Runner *sparkwing.RunnerInfo `json:"runner"`
	Args   []string              `json:"args"`
	Env    []string              `json:"env"`
}

type probe struct {
	sparkwing.Base
	sparkwing.Produces[Observation]
	wait bool
}

func (p probe) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(w, "run", func(ctx context.Context) (Observation, error) {
		fmt.Fprintln(os.Stdout, "fixture stdout")
		fmt.Fprintln(os.Stderr, "fixture stderr")
		sparkwing.Info(ctx, "probe-started")
		if p.wait {
			<-ctx.Done()
			return Observation{}, ctx.Err()
		}
		return Observation{Runner: sparkwing.Runner(ctx), Args: os.Args, Env: os.Environ()}, nil
	}), nil
}
