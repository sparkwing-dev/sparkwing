//go:build unix

package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/sdkserve"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

const stepPipeline = "node-protocol-step-fixture"

func init() {
	if os.Getenv(nodeProtocolEntryEnv) != "1" {
		return
	}
	sparkwing.Register[sparkwing.NoInputs](stepPipeline, func() sparkwing.Pipeline[sparkwing.NoInputs] {
		return stepFixturePipeline{}
	})
}

type stepFixturePipeline struct{ sparkwing.Base }

func (stepFixturePipeline) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "compose", &composeJob{})
	fanOut(plan)
	sparkwing.Job(plan, "steps", stepsJob{}).OnFailure("steps-recover", func(ctx context.Context, f sparkwing.Failure) error {
		sparkwing.Info(ctx, "recovering from %s: %v", f.Stage, f.Err)
		return nil
	})
	return nil
}

type composeJob struct {
	sparkwing.Base
	sparkwing.Produces[int]
}

func (*composeJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	fetch := sparkwing.Step(w, "fetch", func(context.Context) (int, error) { return 41, nil })
	return sparkwing.Step(w, "use", func(ctx context.Context) (int, error) {
		return sparkwing.StepGet[int](ctx, fetch) + 1, nil
	}).Needs(fetch), nil
}

// safety: each side closes its own channel and waits for the other's, so the
// pair finishes only when both bodies run at once; run one after the other,
// the first waits until end of input.
var leftIn, rightIn = make(chan struct{}), make(chan struct{})

func meet(ctx context.Context, mine, theirs chan struct{}) error {
	close(mine)
	select {
	case <-theirs:
		sparkwing.Info(ctx, "met")
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type stepsJob struct{}

func (stepsJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	sparkwing.Step(w, "fail", func(context.Context) error { return errors.New("boom") })
	sparkwing.Step(w, "explode", func(context.Context) error { panic("kaboom") })
	sparkwing.Step(w, "left", func(ctx context.Context) error { return meet(ctx, leftIn, rightIn) })
	sparkwing.Step(w, "right", func(ctx context.Context) error { return meet(ctx, rightIn, leftIn) })
	return nil, nil
}

func (s *entrySession) replies(n int) (map[string]nodeproto.Reply, []map[string]any) {
	s.t.Helper()
	got := map[string]nodeproto.Reply{}
	var logs []map[string]any
	for len(got) < n {
		reply, more := s.next()
		got[reply.ID] = reply
		logs = append(logs, more...)
	}
	return got, logs
}

func (s *entrySession) step(node, step string, failure *nodeproto.Failure) (nodeproto.RunResult, nodeproto.Reply, []map[string]any) {
	s.t.Helper()
	reply, logs := s.call(nodeproto.Request{Op: nodeproto.OpRunStep, Node: node, Step: step, Attempt: 1, Failure: failure})
	var res nodeproto.RunResult
	if reply.OK {
		if err := reply.Decode(&res); err != nil {
			s.t.Fatal(err)
		}
	}
	return res, reply, logs
}

func TestNodeProtocolEntryRunsSteps(t *testing.T) {
	s := startEntry(t)
	_, reply, _ := s.step("compose", "fetch", nil)
	wantCode(t, reply, nodeproto.CodeNoPlan)
	if reply, _ := s.call(planRequestFor(stepPipeline, map[string]string{})); !reply.OK {
		t.Fatalf("plan = %+v", reply)
	}

	if res, _, _ := s.step("compose", "fetch", nil); res.Outcome != nodeproto.OutcomeSuccess || res.Output != nil {
		t.Errorf("fetch = %+v, want success without output, since only the result step carries one", res)
	}
	if res, _, _ := s.step("compose", "use", nil); res.Outcome != nodeproto.OutcomeSuccess || string(res.Output) != "42" {
		t.Errorf("use = %+v, want output 42 from fetch's 41 read through StepGet", res)
	}

	if res, _, _ := s.step("steps", "fail", nil); res.Outcome != nodeproto.OutcomeFailed || res.Error != "boom" {
		t.Errorf("fail = %+v, want outcome failed with the body's error", res)
	}
	if res, _, _ := s.step("steps", "explode", nil); res.Outcome != nodeproto.OutcomeFailed || !strings.Contains(res.Error, "kaboom") {
		t.Errorf("explode = %+v, want outcome failed carrying the panic", res)
	}

	_, reply, _ = s.step("nope", "run", nil)
	wantCode(t, reply, nodeproto.CodeUnknownNode)
	_, reply, _ = s.step("steps", "nope", nil)
	wantCode(t, reply, nodeproto.CodeUnknownStep)

	left := s.send(nodeproto.Request{Op: nodeproto.OpRunStep, Node: "steps", Step: "left", Attempt: 1})
	right := s.send(nodeproto.Request{Op: nodeproto.OpRunStep, Node: "steps", Step: "right", Attempt: 1})
	got, logs := s.replies(2)
	for _, id := range []string{left, right} {
		var res nodeproto.RunResult
		if err := got[id].Decode(&res); err != nil || res.Outcome != nodeproto.OutcomeSuccess {
			t.Errorf("parallel step reply %s = %+v (%v), want success", id, res, err)
		}
	}
	steps := map[any]bool{}
	for _, rec := range logs {
		steps[rec["step"]] = true
	}
	if !steps["left"] || !steps["right"] {
		t.Errorf("parallel step logs %v, want each tagged with its own step", logs)
	}

	res, _, logs := s.step("steps-recover", "run", &nodeproto.Failure{Node: "steps", Stage: "verify", Message: "503 from /ready"})
	if res.Outcome != nodeproto.OutcomeSuccess || len(logs) != 1 || logs[0]["msg"] != "recovering from verify: 503 from /ready" || logs[0]["step"] != "run" {
		t.Errorf("recovery = %+v with logs %v, want the failure passed to its body", res, logs)
	}
	s.end()
}

type targetsJob struct {
	sparkwing.Base
	sparkwing.Produces[[]string]
}

func (*targetsJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(w, "list", func(context.Context) ([]string, error) { return []string{"linux", "darwin"}, nil }), nil
}

func fanOut(plan *sparkwing.Plan) {
	targets := sparkwing.Job(plan, "targets", &targetsJob{})
	sparkwing.JobFanOutDynamic[string](plan, "tests", targets, func(os string) (string, any) {
		return "test-" + os, func(ctx context.Context) error {
			sparkwing.Info(ctx, "testing %s", os)
			return nil
		}
	})
}

func TestExpandReportsAPanickingGenerator(t *testing.T) {
	plan := sparkwing.NewPlan()
	fanOut(plan)
	planned := &sdkserve.Planned{Plan: plan, Run: sparkwing.RunContext{RunID: "run-1"}}
	res, err := sdkserve.Expand(context.Background(), sdkserve.Config{PlanDoc: planDocument}, planned, "targets/generator/0")
	if err != nil || res.Error == nil || !strings.Contains(*res.Error, "panicked") {
		t.Errorf("expand without a resolver = %+v, %v, want the generator's panic as the closure's error", res, err)
	}
}

func TestNodeProtocolEntryExpandsGenerators(t *testing.T) {
	s := startEntry(t)
	if reply, _ := s.call(planRequestFor(stepPipeline, map[string]string{})); !reply.OK {
		t.Fatalf("plan = %+v", reply)
	}
	reply, _ := s.call(nodeproto.Request{Op: nodeproto.OpRunNode, Node: "targets", Attempt: 1})
	if res := runResult(t, reply); res.Outcome != nodeproto.OutcomeSuccess {
		t.Fatalf("targets = %+v", res)
	}

	reply, _ = s.call(nodeproto.Request{Op: nodeproto.OpEval, Closure: "targets/generator/0"})
	value, errMsg := evalResult(t, reply)
	var nodes []any
	if err := json.Unmarshal([]byte(value), &nodes); err != nil || errMsg != nil || len(nodes) != 2 {
		t.Fatalf("generator = value %s error %v, want two node documents", value, deref(errMsg))
	}
	for _, n := range nodes {
		s.doc.mustValidate(t, "node", n)
	}

	res, _, logs := s.step("test-darwin", "run", nil)
	if res.Outcome != nodeproto.OutcomeSuccess || !hasLog(logs, "test-darwin", "testing darwin") {
		t.Errorf("generated step = %+v with logs %v, want it to run in the process that generated it", res, logs)
	}
	reply, _ = s.call(nodeproto.Request{Op: nodeproto.OpEval, Closure: "targets/generator/0"})
	wantCode(t, reply, nodeproto.CodeBadRequest)
	s.end()
}
