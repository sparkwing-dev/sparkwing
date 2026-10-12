//go:build unix

package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/sdkserve"
	"github.com/sparkwing-dev/sparkwing/internal/sparkwingruntime"
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

func (s *entrySession) send(req nodeproto.Request) string {
	s.t.Helper()
	s.nextID++
	req.ID = fmt.Sprint(s.nextID)
	line, err := json.Marshal(req)
	if err != nil {
		s.t.Fatal(err)
	}
	s.proto.mustValidate(s.t, "request", jsonValue(s.t, line))
	if _, err := s.req.Write(append(line, '\n')); err != nil {
		s.t.Fatal(err)
	}
	return req.ID
}

// safety: replies may arrive in any order, so this reads frames until n
// replies are in, checking each run result against the schema.
func (s *entrySession) replies(n int) (map[string]nodeproto.Reply, []map[string]any) {
	s.t.Helper()
	got := map[string]nodeproto.Reply{}
	var logs []map[string]any
	for len(got) < n {
		raw, err := s.frames.ReadLine()
		if err != nil {
			s.t.Fatalf("frame pipe: %v; stdout: %s", err, s.stdout.String())
		}
		frame, err := nodeproto.ParseFrame(raw)
		if err != nil {
			s.t.Fatalf("frame %s: %v", raw, err)
		}
		v := jsonValue(s.t, raw)
		if frame.Kind == nodeproto.KindLog {
			s.record.mustValidate(s.t, "", v)
			logs = append(logs, v.(map[string]any))
			continue
		}
		s.proto.mustValidate(s.t, "reply", v)
		if frame.Reply.OK {
			s.proto.mustValidate(s.t, "runResult", jsonValue(s.t, frame.Reply.Result))
		}
		got[frame.Reply.ID] = frame.Reply
	}
	return got, logs
}

func (s *entrySession) step(node, step string, failure *nodeproto.Failure) (nodeproto.RunResult, nodeproto.Reply, []map[string]any) {
	s.t.Helper()
	id := s.send(nodeproto.Request{Op: nodeproto.OpRunStep, Node: node, Step: step, Attempt: 1, Failure: failure})
	got, logs := s.replies(1)
	var res nodeproto.RunResult
	if reply := got[id]; reply.OK {
		if err := reply.Decode(&res); err != nil {
			s.t.Fatal(err)
		}
	}
	return res, got[id], logs
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
	return sparkwing.Step(w, "list", func(context.Context) ([]string, error) { return nil, nil }), nil
}

func TestExpandAnswersGeneratedNodeDocuments(t *testing.T) {
	plan := sparkwing.NewPlan()
	targets := sparkwing.Job(plan, "targets", &targetsJob{})
	sparkwing.JobFanOutDynamic[string](plan, "tests", targets, func(os string) (string, any) {
		return "test-" + os, func(context.Context) error { return nil }
	})
	rc := sparkwing.RunContext{RunID: "run-1", Pipeline: "fanout"}
	doc, err := planDocument(plan, rc, nil)
	if err != nil {
		t.Fatal(err)
	}
	var gen string
	for _, n := range doc.Nodes {
		if n.ID == "targets" && n.Closures != nil && len(n.Closures.Generator) == 1 {
			gen = n.Closures.Generator[0]
		}
	}
	if gen == "" {
		t.Fatalf("plan %+v lists no generator on targets", doc.Nodes)
	}
	planned := &sdkserve.Planned{Plan: plan, Run: rc, Doc: doc}
	cfg := sdkserve.Config{PlanDoc: planDocument}

	if _, err := sdkserve.Expand(context.Background(), cfg, planned, "targets/skip_if/0"); !hasCode(err, nodeproto.CodeUnknownClosure) {
		t.Errorf("expand of a non-generator = %v, want unknown_closure", err)
	}

	ctx := sparkwingruntime.WithJSONResolver(context.Background(), func(id string) ([]byte, bool) {
		return []byte(`["linux","darwin"]`), id == "targets"
	})
	res, err := sdkserve.Expand(ctx, cfg, planned, gen)
	if err != nil || res.Error != nil {
		t.Fatalf("expand = %+v, %v", res, err)
	}
	schema := loadProtocolSchema(t, "describe.schema.json")
	var nodes []any
	if err := json.Unmarshal(res.Value, &nodes); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, n := range nodes {
		schema.mustValidate(t, "node", n)
		node := n.(map[string]any)
		ids = append(ids, node["id"].(string))
		if deps, _ := node["deps"].([]any); len(deps) != 1 || deps[0] != "targets" {
			t.Errorf("generated node %v does not depend on its source", node)
		}
	}
	if strings.Join(ids, ",") != "test-linux,test-darwin" {
		t.Errorf("generated ids %v, want test-linux and test-darwin", ids)
	}
	if plan.Job("test-linux") == nil {
		t.Error("the generated node is not in the process's plan for later requests")
	}

	if _, err := sdkserve.Expand(ctx, cfg, planned, gen); !hasCode(err, nodeproto.CodeBadRequest) {
		t.Errorf("second expand = %v, want bad_request", err)
	}
}

func TestExpandReportsAPanickingGenerator(t *testing.T) {
	plan := sparkwing.NewPlan()
	targets := sparkwing.Job(plan, "targets", &targetsJob{})
	sparkwing.JobFanOutDynamic[string](plan, "tests", targets, func(os string) (string, any) {
		return "test-" + os, func(context.Context) error { return nil }
	})
	planned := &sdkserve.Planned{Plan: plan, Run: sparkwing.RunContext{RunID: "run-1"}}
	res, err := sdkserve.Expand(context.Background(), sdkserve.Config{PlanDoc: planDocument}, planned, "targets/generator/0")
	if err != nil || res.Error == nil || !strings.Contains(*res.Error, "panicked") {
		t.Errorf("expand without a resolver = %+v, %v, want the generator's panic as the closure's error", res, err)
	}
}

func hasCode(err error, code nodeproto.ErrorCode) bool {
	var perr *nodeproto.Error
	return errors.As(err, &perr) && perr.Code == code
}
