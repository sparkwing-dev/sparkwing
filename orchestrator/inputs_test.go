package orchestrator_test

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/sparkwing-dev/sparkwing/orchestrator"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// SDK-041: an end-to-end run drives a pipeline with typed Inputs and
// asserts a step body deep in the DAG sees the same values via
// sparkwing.Inputs[T](ctx) -- no closure threading, no struct field.

type inputsArgs struct {
	Service string `flag:"service"`
	Env     string `flag:"env" default:"staging"`
}

// inputsObservation captures what the step body saw, so the test can
// assert against it after RunLocal returns. Package-level so the
// pipeline factory closes over it.
var (
	inputsObservedMu sync.Mutex
	inputsObserved   *inputsArgs
)

type inputsPipe struct{ sparkwing.Base }

func (inputsPipe) Plan(_ context.Context, plan *sparkwing.Plan, in inputsArgs, rc sparkwing.RunContext) error {
	// Sanity-pin Plan-time view too: the value we get here should
	// equal what a step body retrieves via Inputs[T](ctx).
	planArgs := in
	sparkwing.Job(plan, "deploy", func(ctx context.Context) error {
		got := sparkwing.Inputs[inputsArgs](ctx)
		if got != planArgs {
			return fmt.Errorf("step body Inputs[inputsArgs] = %+v, want %+v (Plan-time value)", got, planArgs)
		}
		inputsObservedMu.Lock()
		defer inputsObservedMu.Unlock()
		v := got
		inputsObserved = &v
		return nil
	})
	return nil
}

func init() {
	// Custom Register because the package-shared `register` helper
	// only handles NoInputs.
	sparkwing.Register[inputsArgs]("orch-inputs", func() sparkwing.Pipeline[inputsArgs] {
		return &inputsPipe{}
	})
}

func TestInputs_StepBodySeesTypedInputs(t *testing.T) {
	inputsObservedMu.Lock()
	inputsObserved = nil
	inputsObservedMu.Unlock()

	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{
		Pipeline: "orch-inputs",
		Args: map[string]string{
			"service": "api",
			"env":     "prod",
		},
	})
	if err != nil {
		t.Fatalf("RunLocal: %v", err)
	}
	if res.Status != "success" {
		t.Fatalf("Status = %q, want success", res.Status)
	}
	inputsObservedMu.Lock()
	defer inputsObservedMu.Unlock()
	if inputsObserved == nil {
		t.Fatal("step body never recorded Inputs[T]")
	}
	if inputsObserved.Service != "api" || inputsObserved.Env != "prod" {
		t.Fatalf("step body saw %+v, want {Service:api Env:prod}", *inputsObserved)
	}
}
