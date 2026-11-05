package sparkwing

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/sdkserve"
)

type coresFixture struct{}

func (coresFixture) Plan(_ context.Context, plan *Plan, _ NoInputs, _ RunContext) error {
	Job(plan, "light", func(context.Context) error { return nil }).Cores(0.25)
	return nil
}

func init() {
	Register[NoInputs]("cores-fixture", func() Pipeline[NoInputs] { return coresFixture{} })
}

func TestCoreRequestsReachOnlyAHostThatNamesThem(t *testing.T) {
	for _, tc := range []struct {
		features []string
		want     *float64
	}{
		{nil, nil},
		{[]string{nodeproto.FeatureJobCores}, new(0.25)},
	} {
		doc := planOverProtocol(t, tc.features)
		got := doc.Jobs[0].Cores
		if (got == nil) != (tc.want == nil) || got != nil && *got != *tc.want {
			t.Fatalf("features %v: cores = %v, want %v", tc.features, got, tc.want)
		}
	}
}

func planOverProtocol(t *testing.T, features []string) nodeproto.PlanDoc {
	t.Helper()
	in, requests := io.Pipe()
	frames, out := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- sdkserve.Serve(context.Background(), in, out, sdkserve.Config{SDKVersion: "fixture", Operations: sdkOperations{}})
	}()
	defer func() {
		_ = requests.Close()
		_ = frames.Close()
		if err := <-done; err != nil {
			t.Errorf("serve: %v", err)
		}
	}()
	reader := bufio.NewReader(frames)
	var doc nodeproto.PlanDoc
	for _, request := range []nodeproto.Request{
		{
			ID: "hello", Op: nodeproto.OpHello, Protocol: 2, Features: features,
			Engine: &nodeproto.EngineIdentity{Version: "fixture"}, Runner: &nodeproto.RunnerIdentity{Name: "local", Type: "local", Labels: []string{}},
		},
		{ID: "plan", Op: nodeproto.OpPlan, Pipeline: "cores-fixture", Args: map[string]string{}, Run: &nodeproto.RunContext{RunID: "r", Pipeline: "cores-fixture"}},
	} {
		if err := json.NewEncoder(requests).Encode(request); err != nil {
			t.Fatal(err)
		}
		line, err := reader.ReadBytes('\n')
		if err != nil {
			t.Fatal(err)
		}
		var reply nodeproto.Reply
		if err := json.Unmarshal(line, &reply); err != nil || !reply.OK {
			t.Fatalf("%s reply: %s (%v)", request.ID, line, err)
		}
		if request.Op == nodeproto.OpPlan {
			if err := reply.Decode(&doc); err != nil {
				t.Fatal(err)
			}
		}
	}
	return doc
}
