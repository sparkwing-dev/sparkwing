package protocolgo

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

const Name = "protocol-go-acceptance"

type Inputs struct {
	Name string `flag:"name" required:"true"`
}

type Output struct {
	Value string `json:"value"`
}

type Pipeline struct{}

func (Pipeline) Plan(_ context.Context, plan *sparkwing.Plan, in Inputs, run sparkwing.RunContext) error {
	if in.Name == "alias" {
		sparkwing.Job(plan, "invoked-"+run.Pipeline, func(context.Context) error { return nil })
	}
	producer := sparkwing.Job(plan, "producer", producer{value: in.Name})
	sparkwing.Job(plan, "consumer", consumer{Source: sparkwing.RefTo[Output](producer)})
	sparkwing.Job(plan, "secret", func(ctx context.Context) error {
		secret, err := sparkwing.Secret(ctx, "TOKEN")
		if err != nil || secret != "fixture-secret" {
			return fmt.Errorf("secret lookup: %w", err)
		}
		plain, err := sparkwing.Variable(ctx, "PLAIN")
		if err != nil || plain != "fixture-plain" {
			return fmt.Errorf("plain lookup: %w", err)
		}
		return nil
	})
	sparkwing.Job(plan, "failure", func(context.Context) error {
		return errors.New("fixture failure")
	})
	sparkwing.Job(plan, "probe", probe{wait: in.Name == "cancel"}).
		SkipIf(func(context.Context) bool { return false })
	sparkwing.Job(plan, "retry", &retryProbe{}).Retry(1)
	return nil
}

type producer struct {
	sparkwing.Base
	sparkwing.Produces[Output]
	value string
}

func (j producer) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(w, "run", func(context.Context) (Output, error) {
		return Output{Value: j.value}, nil
	}), nil
}

type consumer struct {
	sparkwing.Base
	sparkwing.Produces[Output]
	Source sparkwing.Ref[Output]
}

func (j consumer) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(w, "run", func(ctx context.Context) (Output, error) {
		return Output{Value: j.Source.Get(ctx).Value + "-consumed"}, nil
	}), nil
}

var registerOnce sync.Once

func Register() {
	registerOnce.Do(func() {
		sparkwing.Register[Inputs](Name, func() sparkwing.Pipeline[Inputs] { return Pipeline{} })
	})
}
