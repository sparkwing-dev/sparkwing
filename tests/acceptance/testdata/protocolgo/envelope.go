package protocolgo

import (
	"context"
	"errors"
	"sync"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

const EnvelopeName = "protocol-go-envelope"

type EnvelopePipeline struct{}

func (EnvelopePipeline) Plan(_ context.Context, plan *sparkwing.Plan, in Inputs, _ sparkwing.RunContext) error {
	release := make(chan struct{})
	var once sync.Once
	failure := func() error {
		if in.Name == "fail" {
			return errors.New("fixture closure failed")
		}
		return nil
	}
	job := sparkwing.Job(plan, "envelope", envelopeWork{release: release, failure: failure})
	job.SkipIf(func(context.Context) bool {
		if err := failure(); err != nil {
			panic(err)
		}
		return true
	})
	job.BeforeRun(func(context.Context) error {
		once.Do(func() { close(release) })
		return failure()
	})
	job.Verify(func(context.Context) error { return failure() })
	job.AfterRun(func(context.Context, error) {
		if err := failure(); err != nil {
			panic(err)
		}
	})
	job.Memo(func(ctx context.Context) (sparkwing.MemoValue, error) {
		if in.Name == "cancel" {
			sparkwing.Info(ctx, "memo-waiting")
			<-ctx.Done()
			return "", ctx.Err()
		}
		if in.Name == "no-memo" {
			return sparkwing.NoMemo, nil
		}
		return "fixture-memo-key", failure()
	})
	job.Generate(func(_ context.Context, expansion *sparkwing.Plan) error {
		if err := failure(); err != nil {
			return err
		}
		sparkwing.Job(expansion, "generated", func(context.Context) error { return nil })
		return nil
	})
	return nil
}

type envelopeWork struct {
	sparkwing.Base
	release <-chan struct{}
	failure func() error
}

func (work envelopeWork) Work(steps *sparkwing.Work) (*sparkwing.WorkStep, error) {
	sparkwing.Step(steps, "body", func(context.Context) error { return work.failure() })
	sparkwing.Step(steps, "sibling", func(ctx context.Context) error {
		sparkwing.Info(ctx, "sibling-waiting")
		select {
		case <-work.release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	return nil, nil
}
