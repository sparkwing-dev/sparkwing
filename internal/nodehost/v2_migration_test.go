package nodehost

import (
	"encoding/json"
	"errors"
	"os"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/pipelineentry"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestV2SessionRequiresGrantedIdentity(t *testing.T) {
	for _, missing := range []string{"engine", "name", "type", "labels"} {
		t.Run(missing, func(t *testing.T) {
			cfg := SessionConfig{Entry: pipelineentry.Binary("/missing/sdk"), Engine: nodeproto.EngineIdentity{Version: "test"}, Runner: nodeproto.RunnerIdentity{Name: "local", Type: "local", Labels: []string{}}}
			switch missing {
			case "engine":
				cfg.Engine.Version = ""
			case "name":
				cfg.Runner.Name = ""
			case "type":
				cfg.Runner.Type = ""
			case "labels":
				cfg.Runner.Labels = nil
			}
			_, err := startConfiguredProcess(t.Context(), cfg)
			if err == nil || err.Error() != "sdk process needs the selected engine version and granted runner identity" {
				t.Fatalf("missing %s identity: %v", missing, err)
			}
		})
	}
}

func FuzzV2SessionRefusesIncompleteIdentity(f *testing.F) {
	f.Add("test", "runner", "cloud", uint8(0))
	f.Fuzz(func(t *testing.T, engine, name, kind string, missing uint8) {
		cfg := SessionConfig{Entry: pipelineentry.Binary("/missing/sdk"), Engine: nodeproto.EngineIdentity{Version: engine}, Runner: nodeproto.RunnerIdentity{Name: name, Type: kind, Labels: []string{}}}
		switch missing % 4 {
		case 0:
			cfg.Engine.Version = ""
		case 1:
			cfg.Runner.Name = ""
		case 2:
			cfg.Runner.Type = ""
		case 3:
			cfg.Runner.Labels = nil
		}
		_, err := startConfiguredProcess(t.Context(), cfg)
		if err == nil || err.Error() != "sdk process needs the selected engine version and granted runner identity" {
			t.Fatalf("incomplete identity reached process startup: %v", err)
		}
	})
}

func TestV2SessionHelloKeepsAuthorityOffEnvironment(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses Unix protocol descriptors")
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	binding := &nodeproto.ProcessBinding{RunID: "r", JobID: "build", Routes: nodeproto.ProcessRoutes{URL: "http://127.0.0.1:1", Bearer: "test-route-authority"}}
	runner := nodeproto.RunnerIdentity{Name: "granted-runner", Type: "cloud", Labels: []string{"linux"}}
	sess, err := startConfiguredProcess(t.Context(), SessionConfig{
		Entry: pipelineentry.Command{executable, "-test.run=^TestV2SDKProcess$", "--"},
		Env:   []string{"SPARKWING_TEST_V2_SDK=1"}, Engine: nodeproto.EngineIdentity{Version: "v1.2.3"}, Runner: runner, Binding: binding, Validation: nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: noV2Secrets{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	var got nodeproto.Request
	if err := ask(t.Context(), sess, nodeproto.Request{Op: nodeproto.OpDescribe}, &got); err != nil {
		t.Fatal(err)
	}
	if got.Op != nodeproto.OpHello || got.Engine.Version != "v1.2.3" || got.Runner.Name != runner.Name || got.Binding.Routes.Bearer != binding.Routes.Bearer {
		t.Fatalf("wrong hello: %+v", got)
	}
}

func TestV2SDKProcess(t *testing.T) {
	if os.Getenv("SPARKWING_TEST_V2_SDK") != "1" {
		return
	}
	requests := nodeproto.NewReader(os.NewFile(3, "requests"))
	frames := nodeproto.NewWriter(os.NewFile(4, "frames"))
	hello, err := requests.NextRequest()
	if err != nil || hello.Op != nodeproto.OpHello {
		os.Exit(21)
	}
	for _, text := range append(os.Environ(), os.Args...) {
		if strings.Contains(text, hello.Binding.Routes.Bearer) {
			os.Exit(22)
		}
	}
	result, _ := json.Marshal(nodeproto.HelloResult{Protocol: 2, SDK: nodeproto.SDKIdentity{Language: "go", Version: "v1.2.3"}, Features: []string{}})
	if frames.Send(nodeproto.Reply{ID: hello.ID, OK: true, Result: result}) != nil {
		os.Exit(23)
	}
	for {
		req, err := requests.NextRequest()
		if err != nil {
			os.Exit(0)
		}
		result, _ := json.Marshal(hello)
		if frames.Send(nodeproto.Reply{ID: req.ID, OK: true, Result: result}) != nil {
			os.Exit(24)
		}
	}
}

type noV2Secrets struct{}

func (noV2Secrets) ContainsSecret(string) bool { return false }

func TestV2DriftChecksDeclarationFields(t *testing.T) {
	accepted := nodeproto.JobSpec{ID: "build", Steps: []nodeproto.StepSpec{{ID: "run"}}, ResultStep: "run"}
	raw, err := json.Marshal(nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{accepted}})
	if err != nil {
		t.Fatal(err)
	}
	if err := checkDrift(raw, accepted, nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: noV2Secrets{}}); err != nil {
		t.Fatal(err)
	}
	changed := accepted
	changed.Optional = true
	var refusal *apierr.Error
	if err := checkDrift(raw, changed, nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: noV2Secrets{}}); !errors.As(err, &refusal) || refusal.Code != apierr.PlanDrift {
		t.Fatalf("changed declaration: %v", err)
	}
}

func TestV2AfterRunPreservesCanonicalFailure(t *testing.T) {
	failure := &apierr.Error{Code: apierr.DeadlineExceeded, Message: "attempt deadline", Details: map[string]any{"job": "build"}}
	got := afterRunArgOf(Result{Outcome: sparkwing.Failed, Err: failure})
	if got.Outcome != "failed" || got.Error != failure {
		t.Fatalf("failure lost: %+v", got)
	}
	if got := afterRunArgOf(Result{Outcome: sparkwing.Success}); got.Outcome != "success" || got.Error != nil {
		t.Fatalf("success: %+v", got)
	}
}

func TestV2RetryBudgetCountsAdditionalAttempts(t *testing.T) {
	timeout, backoff := int64(100), int64(10)
	job := nodeproto.JobSpec{TimeoutMS: &timeout, Retry: &nodeproto.RetrySpec{Attempts: 2, BackoffMS: &backoff}, Steps: []nodeproto.StepSpec{{ID: "body"}}}
	if got := AttemptsBudget(job); got != 330*time.Millisecond {
		t.Fatalf("budget = %v", got)
	}
}

func TestV2AttemptEnvironmentSeparatesStartupAndBody(t *testing.T) {
	base := SessionConfig{Env: []string{"PATH=/bin", "SPARKWING_NODE_TOKEN=old", "SPARKWING_NODE_URL=old", "DOCKER_HOST=tcp://captured:2375", "SPARKWING_RUNNER_NAME=forged"}, Runner: nodeproto.RunnerIdentity{Name: "granted", Type: "agent", Labels: []string{}}}
	node := &NodeRun{Run: &Run{ID: "run"}, Plan: nodeproto.JobSpec{ID: "build", Env: map[string]nodeproto.EnvValue{"NODE_OPTIONS": {Value: "--max-old-space-size=2048"}, "GOMAXPROCS": {Value: "2"}, "BODY": {Value: "later"}, "DOCKER_HOST": {Value: "tcp://declared:2375"}}}}
	startup, cleanup, err := attemptEnvironments(base, node)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range []string{"PATH=/bin", "NODE_OPTIONS=--max-old-space-size=2048", "GOMAXPROCS=2", "DOCKER_HOST=tcp://captured:2375", "SPARKWING_RUNNER_NAME=granted"} {
		if !slices.Contains(startup, entry) {
			t.Fatalf("startup omitted %s: %v", entry, startup)
		}
	}
	for _, entry := range []string{"NODE_OPTIONS=--max-old-space-size=2048", "GOMAXPROCS=2", "DOCKER_HOST=tcp://declared:2375", "BODY=later", "SPARKWING_RUNNER_NAME=granted"} {
		if !slices.Contains(cleanup, entry) {
			t.Fatalf("cleanup omitted %s: %v", entry, cleanup)
		}
	}
	all := strings.Join(slices.Concat(startup, cleanup), "\n")
	if slices.Contains(startup, "BODY=later") || slices.Contains(cleanup, "DOCKER_HOST=tcp://captured:2375") || strings.Contains(all, "=old") || strings.Contains(all, "=forged") {
		t.Fatal("environment mixed startup/body values or inherited host authority")
	}
	node.Plan.Env["SPARKWING_RUNNER_NAME"] = nodeproto.EnvValue{Value: "forged"}
	if startup, cleanup, err := attemptEnvironments(base, node); err == nil || startup != nil || cleanup != nil {
		t.Fatal("invalid declaration produced a partial environment")
	}
}
