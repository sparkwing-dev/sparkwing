package controller_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/sdkserve"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type dependencyOutputPipeline struct{}

type dependencyOutputJob struct {
	sparkwing.Produces[childWeather]
	producer sparkwing.Ref[childWeather]
}

func (job dependencyOutputJob) Work(work *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(work, "read", func(ctx context.Context) (childWeather, error) {
		return job.producer.Get(ctx), nil
	}), nil
}

func (dependencyOutputPipeline) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	producer := sparkwing.Job(plan, "stamp", childWeatherStamp{})
	sparkwing.Job(plan, "job", dependencyOutputJob{producer: sparkwing.RefTo[childWeather](producer)}).Needs(producer)
	return nil
}

func init() {
	sparkwing.Register[sparkwing.NoInputs]("dependency-output-fixture", func() sparkwing.Pipeline[sparkwing.NoInputs] { return dependencyOutputPipeline{} })
}

func proveGoDependencyOutput(t *testing.T, binding nodeproto.ProcessBinding) {
	t.Helper()
	input, requests := io.Pipe()
	replies, output := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- sdkserve.Serve(t.Context(), input, output, sdkserve.Config{SDKVersion: "test"}) }()
	t.Cleanup(func() {
		_ = requests.Close()
		_ = replies.Close()
		if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
			t.Error(err)
		}
	})
	encode, decode := json.NewEncoder(requests), json.NewDecoder(replies)
	for _, request := range []nodeproto.Request{
		{ID: "hello", Op: nodeproto.OpHello, Protocol: nodeproto.Version, Engine: &nodeproto.EngineIdentity{Version: "test"}, Runner: &nodeproto.RunnerIdentity{Name: "local", Type: "local", Labels: []string{}}, Binding: &binding},
		{ID: "plan", Op: nodeproto.OpPlan, Pipeline: "dependency-output-fixture", Args: map[string]string{}, Run: &nodeproto.RunContext{RunID: "run", Pipeline: "dependency-output-fixture"}},
		{ID: "read", Op: nodeproto.OpRunStep, Job: "job", Step: "read", Attempt: 1, Settled: []string{}},
	} {
		if err := encode.Encode(request); err != nil {
			t.Fatal(err)
		}
		var reply nodeproto.Reply
		for reply.ID == "" {
			if err := decode.Decode(&reply); err != nil {
				t.Fatal(err)
			}
		}
		if !reply.OK || reply.ID != request.ID {
			t.Fatalf("Go SDK %s: %+v", request.ID, reply)
		}
		if request.ID == "read" {
			var result nodeproto.RunResult
			if err := json.Unmarshal(reply.Result, &result); err != nil || result.Outcome != "success" || string(result.Output) != `{"weather":"clear","degrees":72}` {
				t.Fatalf("Go Ref.Get: %+v %v", result, err)
			}
		}
	}
}
