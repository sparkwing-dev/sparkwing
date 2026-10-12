//go:build unix

package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

const runPipeline = "node-protocol-run-fixture"

type runFixtureInputs struct{}

type releaseOut struct {
	Release int `json:"release"`
}

type releaseJob struct {
	sparkwing.Produces[releaseOut]
}

func (releaseJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(w, "apply", func(ctx context.Context) (releaseOut, error) {
		sparkwing.Info(ctx, "applying release 42")
		return releaseOut{Release: 42}, nil
	}), nil
}

type steppedJob struct{}

func (steppedJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	sparkwing.Step(w, "lint", func(context.Context) error { return nil }).
		SkipIf(func(ctx context.Context) bool { return sparkwing.StepFromContext(ctx) == "lint" })
	return nil, nil
}

// safety: the gated body returns only once an eval of its first skip_if has
// run, so a run_node reply proves that eval ran while the body was running.
var (
	released    = make(chan struct{})
	releaseOnce sync.Once
)

type runFixturePipeline struct{ sparkwing.Base }

func (runFixturePipeline) Plan(_ context.Context, plan *sparkwing.Plan, _ runFixtureInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "release", releaseJob{})
	sparkwing.Job(plan, "fails", func(context.Context) error { return errors.New("exit status 3") })
	sparkwing.Job(plan, "panics", func(context.Context) error { panic("body blew up") })
	sparkwing.Job(plan, "deploy", func(context.Context) error { return nil }).
		OnFailure("recover", func(_ context.Context, f sparkwing.Failure) error {
			return fmt.Errorf("recovering from %s failure: %w", f.Stage, f.Err)
		})
	sparkwing.Job(plan, "gated", func(ctx context.Context) error {
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		select {
		case <-released:
			return nil
		case <-ctx.Done():
			return errors.New("no eval ran while the body was running")
		}
	}).
		SkipIf(func(context.Context) bool {
			releaseOnce.Do(func() { close(released) })
			return true
		}).
		SkipIf(func(context.Context) bool { panic("skip_if blew up") }).
		BeforeRun(func(context.Context) error { return errors.New("") }).
		Verify(func(ctx context.Context) error {
			sparkwing.Info(ctx, "verifying")
			return nil
		}).
		AfterRun(func(ctx context.Context, err error) { sparkwing.Info(ctx, "after_run saw %v", err) }).
		Memoize(func(context.Context) (sparkwing.CacheKey, error) { return "v1", nil })
	sparkwing.Job(plan, "nocache", func(context.Context) error { return nil }).
		Memoize(func(context.Context) (sparkwing.CacheKey, error) { return sparkwing.NoCache, nil })
	sparkwing.Job(plan, "keyfail", func(context.Context) error { return nil }).
		Memoize(func(context.Context) (sparkwing.CacheKey, error) { return "", errors.New("no lockfile") })
	sparkwing.Job(plan, "stepped", steppedJob{})
	return nil
}

func init() {
	if os.Getenv(nodeProtocolEntryEnv) == "1" {
		sparkwing.Register[runFixtureInputs](runPipeline, func() sparkwing.Pipeline[runFixtureInputs] {
			return runFixturePipeline{}
		})
	}
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
	s.ops[req.ID] = req.Op
	if _, err := s.req.Write(append(line, '\n')); err != nil {
		s.t.Fatal(err)
	}
	return req.ID
}

func (s *entrySession) next() (nodeproto.Reply, []map[string]any) {
	s.t.Helper()
	var logs []map[string]any
	for {
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
			switch op := s.ops[frame.Reply.ID]; op {
			case nodeproto.OpDescribe, nodeproto.OpPlan:
				s.doc.mustValidate(s.t, map[nodeproto.Op]string{nodeproto.OpPlan: "plan"}[op], jsonValue(s.t, frame.Reply.Result))
			case nodeproto.OpRunNode, nodeproto.OpRunStep:
				s.proto.mustValidate(s.t, "runResult", jsonValue(s.t, frame.Reply.Result))
			case nodeproto.OpEval:
				s.proto.mustValidate(s.t, "evalResult", jsonValue(s.t, frame.Reply.Result))
			default:
				s.t.Fatalf("reply %s answers no request", frame.Reply.ID)
			}
		}
		return frame.Reply, logs
	}
}

func startRunEntry(t *testing.T) *entrySession {
	t.Helper()
	s := startEntry(t)
	if reply, _ := s.call(planRequestFor(runPipeline, map[string]string{})); !reply.OK {
		t.Fatalf("plan = %+v", reply.Error)
	}
	return s
}

func runResult(t *testing.T, r nodeproto.Reply) nodeproto.RunResult {
	t.Helper()
	var res nodeproto.RunResult
	if err := r.Decode(&res); err != nil {
		t.Fatalf("reply %+v: %v", r, err)
	}
	return res
}

func evalResult(t *testing.T, r nodeproto.Reply) (string, *string) {
	t.Helper()
	var res nodeproto.EvalResult
	if err := r.Decode(&res); err != nil {
		t.Fatalf("reply %+v: %v", r, err)
	}
	return string(res.Value), res.Error
}

func TestNodeProtocolEntryRunsNodes(t *testing.T) {
	s := startEntry(t)
	reply, _ := s.call(nodeproto.Request{Op: nodeproto.OpRunNode, Node: "release", Attempt: 1})
	wantCode(t, reply, nodeproto.CodeNoPlan)
	if reply, _ := s.call(planRequestFor(runPipeline, map[string]string{})); !reply.OK {
		t.Fatalf("plan = %+v", reply.Error)
	}

	reply, logs := s.call(nodeproto.Request{Op: nodeproto.OpRunNode, Node: "release", Attempt: 1})
	if res := runResult(t, reply); res.Outcome != nodeproto.OutcomeSuccess || string(res.Output) != `{"release":42}` {
		t.Errorf("release = %+v, want success with its output", res)
	}
	if !hasLog(logs, "release", "applying release 42") {
		t.Errorf("release logged %v, want its Info record naming the node", logs)
	}

	for node, want := range map[string]string{
		"fails":  "exit status 3",
		"panics": "body blew up",
	} {
		reply, _ := s.call(nodeproto.Request{Op: nodeproto.OpRunNode, Node: node, Attempt: 1})
		if res := runResult(t, reply); res.Outcome != nodeproto.OutcomeFailed || !strings.Contains(res.Error, want) || res.Output != nil {
			t.Errorf("%s = %+v, want outcome failed carrying %q", node, res, want)
		}
	}

	reply, _ = s.call(nodeproto.Request{
		Op: nodeproto.OpRunNode, Node: "recover", Attempt: 1,
		Failure: &nodeproto.Failure{Node: "deploy", Stage: "verify", Message: "health check: 503"},
	})
	if res := runResult(t, reply); !strings.HasSuffix(res.Error, "recovering from verify failure: health check: 503") {
		t.Errorf("recover = %+v, want the body to see the verify failure", res)
	}

	reply, _ = s.call(nodeproto.Request{Op: nodeproto.OpRunNode, Node: "nope", Attempt: 1})
	wantCode(t, reply, nodeproto.CodeUnknownNode)
	s.end()
}

func TestNodeProtocolEntryEvaluatesClosures(t *testing.T) {
	s := startRunEntry(t)
	afterRun := json.RawMessage(`{"outcome":"failed","error":"verify: health check: 503"}`)
	for _, tc := range []struct {
		closure string
		input   json.RawMessage
		value   string
		err     *string
	}{
		{closure: "gated/skip_if/0", value: "true"},
		{closure: "gated/skip_if/1", value: "null", err: ptr("skip_if closure of \"gated\" panicked: skip_if blew up")},
		{closure: "gated/before_run/0", value: "null", err: ptr("")},
		{closure: "gated/verify/0", value: "null"},
		{closure: "gated/after_run/0", input: afterRun, value: "null"},
		{closure: "gated/cache_key/0", value: `"v1"`},
		{closure: "nocache/cache_key/0", value: "null"},
		{closure: "keyfail/cache_key/0", value: "null", err: ptr("no lockfile")},
		{closure: "stepped/step/lint/skip_if/0", value: "true"},
	} {
		reply, logs := s.call(nodeproto.Request{Op: nodeproto.OpEval, Closure: tc.closure, Input: tc.input})
		value, errMsg := evalResult(t, reply)
		if value != tc.value || (errMsg == nil) != (tc.err == nil) || (errMsg != nil && *errMsg != *tc.err) {
			t.Errorf("%s = value %s error %v, want value %s error %v", tc.closure, value, deref(errMsg), tc.value, deref(tc.err))
		}
		switch tc.closure {
		case "gated/verify/0":
			if !hasLog(logs, "gated", "verifying") {
				t.Errorf("verify logged %v, want its record naming the node", logs)
			}
		case "gated/after_run/0":
			if !hasLog(logs, "gated", "after_run saw verify: health check: 503") {
				t.Errorf("after_run logged %v, want it to see the node's error", logs)
			}
		}
	}
	if reply, _ := s.call(nodeproto.Request{Op: nodeproto.OpEval, Closure: "gated/before_run/0"}); !strings.Contains(string(reply.Result), `"error":""`) {
		t.Errorf("empty error reply = %s, want an error field holding the empty string", reply.Result)
	}

	reply, _ := s.call(nodeproto.Request{Op: nodeproto.OpEval, Closure: "gated/after_run/0"})
	wantCode(t, reply, nodeproto.CodeBadRequest)
	reply, _ = s.call(nodeproto.Request{Op: nodeproto.OpEval, Closure: "gated/skip_if/9"})
	wantCode(t, reply, nodeproto.CodeUnknownClosure)
	s.end()
}

func TestNodeProtocolEntryEvaluatesWhileANodeRuns(t *testing.T) {
	s := startRunEntry(t)
	run := s.send(nodeproto.Request{Op: nodeproto.OpRunNode, Node: "gated", Attempt: 1})
	eval := s.send(nodeproto.Request{Op: nodeproto.OpEval, Closure: "gated/skip_if/0"})

	// safety: the closure releases the body, so the two replies race; the
	// node's success is what shows the eval ran while the body was running.
	replies := map[string]nodeproto.Reply{}
	for range 2 {
		r, _ := s.next()
		replies[r.ID] = r
	}
	if value, _ := evalResult(t, replies[eval]); value != "true" {
		t.Errorf("eval value = %s, want true", value)
	}
	if res := runResult(t, replies[run]); res.Outcome != nodeproto.OutcomeSuccess {
		t.Errorf("run_node = %+v, want the success of a body an eval released", res)
	}
	s.end()
}

func hasLog(logs []map[string]any, node, msg string) bool {
	for _, l := range logs {
		if l["node"] == node && l["msg"] == msg {
			return true
		}
	}
	return false
}

func deref(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}
