package sparkwing_test

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type buildOut struct {
	Tag    string
	Digest string
}

type buildJob struct {
	sparkwing.Base
	sparkwing.Produces[buildOut]
}

func (b *buildJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(w, "run", func(context.Context) (buildOut, error) {
		return buildOut{Tag: "v1", Digest: "sha256:abc"}, nil
	}), nil
}

func jobFnNoop() func(context.Context) error {
	return func(context.Context) error { return nil }
}
