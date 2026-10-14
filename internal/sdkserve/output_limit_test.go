package sdkserve_test

import (
	"context"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

const outputLimitPipeline = "output-limit"

// safety: each string's JSON form adds two quotes, putting these at and one byte past the limit.
var (
	exactOutput = strings.Repeat("x", nodeproto.MaxOutputBytes-2)
	overOutput  = strings.Repeat("x", nodeproto.MaxOutputBytes-1)
)

type outputLimitJob struct {
	sparkwing.Base
	sparkwing.Produces[string]
}

func (*outputLimitJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	sparkwing.Step(w, "over", func(context.Context) (string, error) { return overOutput, nil })
	return sparkwing.Step(w, "exact", func(context.Context) (string, error) { return exactOutput, nil }), nil
}

type outputLimitFixture struct{ sparkwing.Base }

func (outputLimitFixture) Plan(_ context.Context, p *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(p, "big", &outputLimitJob{})
	return nil
}

func init() {
	sparkwing.Register[sparkwing.NoInputs](outputLimitPipeline, func() sparkwing.Pipeline[sparkwing.NoInputs] { return outputLimitFixture{} })
}

func TestStepOutputLimitHoldsForEveryStep(t *testing.T) {
	s := serve(t, nil)
	if r := s.call(nodeproto.RequestOld{Op: nodeproto.OpPlanOld, Pipeline: outputLimitPipeline, Args: map[string]string{}, Run: &nodeproto.RunContextOld{RunID: "limit"}}); !r.OK {
		t.Fatal(r.Error)
	}
	step := func(id string) nodeproto.RunResultOld {
		t.Helper()
		r := s.call(nodeproto.RequestOld{Op: nodeproto.OpRunStepOld, Node: "big", Step: id, Attempt: 1})
		if !r.OK {
			t.Fatalf("step %s: failed reply %+v", id, r.Error)
		}
		var rr nodeproto.RunResultOld
		if err := r.Decode(&rr); err != nil {
			t.Fatal(err)
		}
		return rr
	}
	if rr := step("over"); rr.Outcome != nodeproto.OutcomeFailed || !strings.Contains(rr.Error, "16 MiB limit") {
		t.Fatalf("an intermediate output over the limit = %s %q, want failed naming the limit", rr.Outcome, rr.Error)
	}
	rr := step("exact")
	if rr.Outcome != nodeproto.OutcomeSuccess || len(rr.Output) != nodeproto.MaxOutputBytes {
		t.Fatalf("a result of exactly the limit = %s with %d bytes (%q), want success", rr.Outcome, len(rr.Output), rr.Error)
	}
	if err := rr.Validate(); err != nil {
		t.Fatalf("the host refuses an output at the limit: %v", err)
	}
	if err := (nodeproto.RunResultOld{Outcome: nodeproto.OutcomeSuccess, Output: []byte(`"` + overOutput + `"`)}).Validate(); err == nil {
		t.Fatal("the host accepts an output over the limit")
	}
}
