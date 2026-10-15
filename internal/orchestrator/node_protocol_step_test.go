//go:build unix

package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/sdkserve"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

const stepPipeline = "node-protocol-step-fixture"

func init() {
	if os.Getenv(nodeProtocolEntryEnv) == "1" {
		sparkwing.Register[sparkwing.NoInputs](stepPipeline, func() sparkwing.Pipeline[sparkwing.NoInputs] { return stepFixturePipeline{} })
	}
}

type stepFixturePipeline struct{ sparkwing.Base }

func (stepFixturePipeline) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "compose", &composeJob{})
	source := sparkwing.Job(plan, "targets", func(context.Context) error { return nil })
	source.Generate(func(_ context.Context, expansion *sparkwing.Plan) error {
		sparkwing.JobFanOut(expansion, "tests", []string{"linux", "darwin"}, func(target string) (string, any) {
			return "test-" + target, func(ctx context.Context) error { sparkwing.Info(ctx, "testing %s", target); return nil }
		})
		return nil
	})
	source.Generate(func(context.Context, *sparkwing.Plan) error { panic("generator blew up") })
	sparkwing.Job(plan, "steps", stepsJob{}).OnFailure("steps-recover", func(ctx context.Context, f sparkwing.Failure) error {
		sparkwing.Info(ctx, "recovering from %s: %v", f.Stage, f.Err)
		return nil
	})
	return nil
}

type composeJob struct{ sparkwing.Produces[int] }

func (*composeJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	fetch := sparkwing.Step(w, "fetch", func(context.Context) (int, error) { return 41, nil })
	return sparkwing.Step(w, "use", func(ctx context.Context) (int, error) { return sparkwing.StepGet[int](ctx, fetch) + 1, nil }).Needs(fetch), nil
}

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
	sparkwing.Step(w, "wait", func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() })
	return nil, nil
}

func (s *entrySession) replies(n int) (map[string]nodeproto.Reply, []map[string]any) {
	s.t.Helper()
	replies := map[string]nodeproto.Reply{}
	var logs []map[string]any
	for len(replies) < n {
		reply, more := s.next()
		replies[reply.ID] = reply
		logs = append(logs, more...)
	}
	return replies, logs
}

func (s *entrySession) step(job, step string, settled []string, failure *nodeproto.JobFailure) (nodeproto.RunResult, nodeproto.Reply, []map[string]any) {
	s.t.Helper()
	reply, logs := s.call(nodeproto.Request{Op: nodeproto.OpRunStep, Job: job, Step: step, Attempt: 1, Settled: settled, Failure: failure})
	var result nodeproto.RunResult
	if reply.OK {
		if err := reply.Decode(&result); err != nil {
			s.t.Fatal(err)
		}
	}
	return result, reply, logs
}

func planSteps(t *testing.T, job string) *entrySession {
	t.Helper()
	s := startEntry(t, job)
	reply, _ := s.call(planRequestFor(stepPipeline, map[string]string{}))
	if !reply.OK {
		t.Fatalf("plan: %+v", reply)
	}
	return s
}

func TestNodeProtocolEntryRunsSteps(t *testing.T) {
	s := startEntry(t, "compose")
	_, reply, _ := s.step("compose", "fetch", []string{}, nil)
	wantCode(t, reply, apierr.NoPlan)
	reply, _ = s.call(planRequestFor(stepPipeline, map[string]string{}))
	if !reply.OK {
		t.Fatal(reply.Error)
	}
	if result, _, _ := s.step("compose", "fetch", []string{}, nil); result.Outcome != "success" || string(result.Output) != "41" {
		t.Fatalf("fetch: %+v", result)
	}
	if result, _, _ := s.step("compose", "use", []string{"fetch"}, nil); result.Outcome != "success" || string(result.Output) != "42" {
		t.Fatalf("use: %+v", result)
	}
	_, reply, _ = s.step("compose", "nope", []string{"fetch", "use"}, nil)
	wantCode(t, reply, apierr.UnknownStep)
	s.end()
}

func TestNodeProtocolEntryReturnsStepFailures(t *testing.T) {
	for _, tc := range []struct{ step, message string }{{"fail", "boom"}, {"explode", "kaboom"}} {
		t.Run(tc.step, func(t *testing.T) {
			s := planSteps(t, "steps")
			result, reply, _ := s.step("steps", tc.step, []string{}, nil)
			if !reply.OK || result.Outcome != "failed" || result.Error == nil || !strings.Contains(result.Error.Message, tc.message) {
				t.Fatalf("failure: %+v / %+v", reply, result)
			}
			s.end()
		})
	}
}

func TestNodeProtocolEntryRunsStepsConcurrently(t *testing.T) {
	s := planSteps(t, "steps")
	left := s.send(nodeproto.Request{Op: nodeproto.OpRunStep, Job: "steps", Step: "left", Attempt: 1, Settled: []string{}})
	right := s.send(nodeproto.Request{Op: nodeproto.OpRunStep, Job: "steps", Step: "right", Attempt: 1, Settled: []string{}})
	replies, logs := s.replies(2)
	for _, id := range []string{left, right} {
		var result nodeproto.RunResult
		if err := replies[id].Decode(&result); err != nil || result.Outcome != "success" {
			t.Fatalf("parallel result: %+v, %v", result, err)
		}
	}
	steps := map[any]bool{}
	for _, log := range logs {
		steps[log["step"]] = true
	}
	if !steps["left"] || !steps["right"] {
		t.Fatalf("parallel logs: %+v", logs)
	}
	s.end()
}

func TestNodeProtocolEntryCancelsAnActiveStep(t *testing.T) {
	s := planSteps(t, "steps")
	running := s.send(nodeproto.Request{Op: nodeproto.OpRunStep, Job: "steps", Step: "wait", Attempt: 1, Settled: []string{}})
	cancelled := s.send(nodeproto.Request{Op: nodeproto.OpCancel, Target: running})
	replies, _ := s.replies(2)
	if !replies[cancelled].OK {
		t.Fatal(replies[cancelled].Error)
	}
	var result nodeproto.RunResult
	if err := replies[running].Decode(&result); err != nil || result.Outcome != "failed" || result.Error == nil {
		t.Fatalf("cancelled step: %+v, %v", result, err)
	}
	s.end()
}

func TestNodeProtocolEntryPassesRecoveryFailure(t *testing.T) {
	s := planSteps(t, "steps-recover")
	result, _, logs := s.step("steps-recover", "run", []string{}, &nodeproto.JobFailure{Job: "steps", Stage: "verify", Error: &apierr.Error{Code: apierr.SDKError, Message: "503 from /ready"}})
	if result.Outcome != "success" || len(logs) != 1 || logs[0]["msg"] != "recovering from verify: 503 from /ready" || logs[0]["step"] != "run" {
		t.Fatalf("recovery: %+v / %+v", result, logs)
	}
	s.end()
}

func TestNodeProtocolEntryExpandsGenerators(t *testing.T) {
	s := planSteps(t, "targets")
	reply, _ := s.call(nodeproto.Request{Op: nodeproto.OpEval, Closure: "targets/generate/0", Input: json.RawMessage("null")})
	var result nodeproto.EvalResult
	if err := reply.Decode(&result); err != nil || result.Error != nil {
		t.Fatalf("generator: %+v, %v", result, err)
	}
	var expansion []nodeproto.JobSpec
	if err := json.Unmarshal(result.Value, &expansion); err != nil || len(expansion) != 2 {
		t.Fatalf("expansion: %s, %v", result.Value, err)
	}
	s.doc.mustValidate(t, "PlanDoc", nodeproto.PlanDoc{Jobs: expansion})
	reply, _ = s.call(nodeproto.Request{Op: nodeproto.OpEval, Closure: "targets/generate/1", Input: json.RawMessage("null")})
	if err := reply.Decode(&result); err != nil || result.Error == nil || !strings.Contains(result.Error.Message, "generator blew up") {
		t.Fatalf("panic: %+v, %v", result, err)
	}
	s.end()
}

func TestGeneratedStepsRunAfterExpansion(t *testing.T) {
	ops := sdkserve.DefaultOperations
	plan := sparkwing.NewPlan()
	if err := (stepFixturePipeline{}).Plan(t.Context(), plan, sparkwing.NoInputs{}, sparkwing.RunContext{}); err != nil {
		t.Fatal(err)
	}
	expanded, err := ops.Eval(t.Context(), plan, nodeproto.Request{Job: "targets", Closure: "targets/generate/0", Input: json.RawMessage("null")})
	if err != nil || expanded.Error != nil {
		t.Fatalf("expand: %+v, %v", expanded, err)
	}
	result, err := ops.RunStep(t.Context(), plan, nodeproto.Request{Job: "test-darwin", Step: "run", Attempt: 1, Settled: []string{}})
	if err != nil || result.Outcome != "success" {
		t.Fatalf("generated step: %+v, %v", result, err)
	}
}
