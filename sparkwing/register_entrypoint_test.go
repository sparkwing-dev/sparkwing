package sparkwing

import (
	"context"
	"testing"
)

type entrypointArgs struct {
	Replicas int `flag:"replicas"`
}
type entrypointPipe struct{ Base }

func (entrypointPipe) Plan(_ context.Context, plan *Plan, _ entrypointArgs, _ RunContext) error {
	Job(plan, "noop", func(context.Context) error { return nil })
	return nil
}

func TestRegisterEntrypointIsAvailableToTheHost(t *testing.T) {
	RegisterEntrypoint[entrypointArgs]("EntrypointTestPipe", func() Pipeline[entrypointArgs] { return entrypointPipe{} })
	reg, exists := lookup("EntrypointTestPipe")
	if !exists {
		t.Fatal("entrypoint factory is absent")
	}
	plan, err := reg.Invoke(context.Background(), map[string]string{}, RunContext{Pipeline: "project-alias"})
	if err != nil {
		t.Fatal(err)
	}
	if got := planDocument(plan); len(got.Jobs) != 1 || got.Jobs[0].ID != "noop" {
		t.Fatalf("entrypoint plan: %+v", got)
	}
}
