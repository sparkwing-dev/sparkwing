package sparkwing

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/sdkserve"
)

type replayFixture struct{}

func (replayFixture) Plan(_ context.Context, plan *Plan, _ NoInputs, _ RunContext) error {
	source := Job(plan, "source", func(context.Context) error { return nil }).Env("SW_REPLAY_BODY", "source")
	source.Generate(func(_ context.Context, expanded *Plan) error {
		if os.Getenv("SW_REPLAY_BODY") != "outside" {
			return fmt.Errorf("body environment influenced generator declarations")
		}
		Job(expanded, "child", func(context.Context) error { return nil }).Generate(func(_ context.Context, nested *Plan) error {
			Job(nested, "leaf", func(context.Context) error {
				if os.Getenv("SW_REPLAY_BODY") != "leaf" {
					return fmt.Errorf("leaf did not receive body environment")
				}
				return nil
			}).Env("SW_REPLAY_BODY", "leaf")
			return nil
		})
		return nil
	})
	return nil
}

func init() {
	Register[NoInputs]("generated-replay-fixture", func() Pipeline[NoInputs] { return replayFixture{} })
}

func TestBoundGeneratedJobReplaysThroughProtocol(t *testing.T) {
	t.Setenv("SW_REPLAY_BODY", "outside")
	in, requests := io.Pipe()
	frames, out := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- sdkserve.Serve(context.Background(), in, out, sdkserve.Config{SDKVersion: "fixture", Operations: sdkOperations{}})
	}()
	t.Cleanup(func() {
		_ = requests.Close()
		_ = frames.Close()
		if err := <-done; err != nil {
			t.Errorf("serve: %v", err)
		}
	})
	reader := bufio.NewReader(frames)
	for _, request := range []nodeproto.Request{
		{ID: "hello", Op: nodeproto.OpHello, Protocol: 2,
			Engine: &nodeproto.EngineIdentity{Version: "fixture"}, Runner: &nodeproto.RunnerIdentity{Name: "local", Type: "local", Labels: []string{}},
			Binding: &nodeproto.ProcessBinding{RunID: "r", JobID: "leaf", Routes: nodeproto.ProcessRoutes{URL: "http://127.0.0.1:1", Bearer: "leaf-only"}}},
		{ID: "plan", Op: nodeproto.OpPlan, Pipeline: "generated-replay-fixture", Args: map[string]string{}, Run: &nodeproto.RunContext{RunID: "r", Pipeline: "generated-replay-fixture"}},
		{ID: "source", Op: nodeproto.OpEval, Closure: "source/generate/0", Input: json.RawMessage(`null`)},
		{ID: "child", Op: nodeproto.OpEval, Closure: "child/generate/0", Input: json.RawMessage(`null`)},
		{ID: "step", Op: nodeproto.OpRunStep, Job: "leaf", Step: "run", Attempt: 1},
	} {
		if err := json.NewEncoder(requests).Encode(request); err != nil {
			t.Fatal(err)
		}
		line, err := reader.ReadBytes('\n')
		if err != nil {
			t.Fatal(err)
		}
		var reply nodeproto.Reply
		if err := json.Unmarshal(line, &reply); err != nil {
			t.Fatal(err)
		}
		if reply.ID != request.ID || !reply.OK {
			t.Fatalf("%s reply: %s", request.ID, line)
		}
		if request.Op == nodeproto.OpEval {
			var result nodeproto.EvalResult
			if err := reply.Decode(&result); err != nil || result.Error != nil {
				t.Fatalf("%s result: %s (%v)", request.ID, line, err)
			}
		}
		if request.Op == nodeproto.OpRunStep {
			var result nodeproto.RunResult
			if err := reply.Decode(&result); err != nil || result.Outcome != "success" {
				t.Fatalf("leaf body: %s (%v)", line, err)
			}
		}
	}
}
