package acceptance_test

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
	"github.com/sparkwing-dev/sparkwing/sparkwing/sparkwingtest"
)

type interfaceOutputJob struct {
	sparkwing.Base
	sparkwing.Produces[any]
	value any
}

func (j interfaceOutputJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	source := sparkwing.Step(w, "source", func(context.Context) (any, error) { return j.value, nil })
	return sparkwing.Step(w, "result", func(ctx context.Context) (any, error) {
		return sparkwing.StepGet[any](ctx, source), nil
	}).Needs(source), nil
}

func testGoSDKInterfaceOutputs(t *testing.T) {
	for _, test := range []struct {
		name  string
		value any
	}{
		{name: "null"},
		{name: "object", value: map[string]any{"answer": float64(42)}},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan := sparkwing.NewPlan()
			job := sparkwing.Job(plan, "producer", interfaceOutputJob{value: test.value})
			ref := sparkwing.RefTo[any](job)
			host := sparkwingtest.New(sparkwingtest.Options{})
			t.Cleanup(func() { _ = host.Close() })
			var raw json.RawMessage
			for _, step := range []string{"source", "result"} {
				var err error
				raw, err = host.RunStep(t.Context(), plan, "producer", step)
				if err != nil {
					t.Fatalf("%s: %v", step, err)
				}
				var value any
				if err := json.Unmarshal(raw, &value); err != nil || !reflect.DeepEqual(value, test.value) {
					t.Fatalf("%s output = %s (%v), want %#v", step, raw, err, test.value)
				}
			}
			reader := sparkwingtest.New(sparkwingtest.Options{Outputs: map[string]json.RawMessage{"producer": raw}})
			t.Cleanup(func() { _ = reader.Close() })
			if got := ref.Get(reader.Context(t.Context())); !reflect.DeepEqual(got, test.value) {
				t.Fatalf("reference output = %#v, want %#v", got, test.value)
			}
		})
	}
}
