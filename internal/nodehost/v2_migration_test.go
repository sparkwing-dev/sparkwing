package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/pipelineentry"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestV2SessionRequiresGrantedIdentity(t *testing.T) {
	_, err := StartSession(t.Context(), SessionConfig{Entry: pipelineentry.Binary("/missing/sdk")})
	if err == nil || !strings.Contains(err.Error(), "granted runner") {
		t.Fatalf("missing identity: %v", err)
	}
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
	sess, err := StartSession(t.Context(), SessionConfig{
		Entry: pipelineentry.Command{executable, "-test.run=^TestV2SDKProcess$", "--"},
		Env:   []string{"SPARKWING_TEST_V2_SDK=1"}, Engine: nodeproto.EngineIdentity{Version: "v1.2.3"}, Runner: runner, Binding: binding,
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

type v2Admitter struct{ requests []wingwire.AdmissionRequest }

func (a *v2Admitter) Acquire(_ context.Context, req wingwire.AdmissionRequest, _ func(wingwire.Queued)) (Lease, error) {
	a.requests = append(a.requests, req)
	return &v2Lease{done: make(chan struct{})}, nil
}

type v2Lease struct{ done chan struct{} }

func (*v2Lease) Token() string                                               { return "lease" }
func (l *v2Lease) Release() error                                            { close(l.done); return nil }
func (l *v2Lease) Watch(func(wingwire.Evicted), func(wingwire.Cancel)) error { <-l.done; return nil }

func TestV2MultipleLocalConcurrencyGroupsShareResourceLease(t *testing.T) {
	daemon := &v2Admitter{}
	cores := 2.0
	node := &NodeRun{Run: &Run{ID: "r", Pipeline: "ci"}, Plan: nodeproto.JobSpec{
		ID: "build", Resources: &nodeproto.ResourceSpec{Cores: &cores},
		Concurrency: []nodeproto.ConcurrencySpec{{Group: "b", Scope: "run", Capacity: 1, Cost: 1, OnLimit: "queue"}, {Group: "a", Scope: "box", Capacity: 2, Cost: 1, OnLimit: "queue"}},
	}}
	admission := &Admission{Daemon: daemon, BoxID: func() string { return "machine" }}
	result := admission.admit(t.Context(), node, func(context.Context, *NodeRun) Result { return Result{Outcome: sparkwing.Success} })
	if result.Outcome != sparkwing.Success {
		t.Fatalf("admission: %+v", result)
	}
	if len(daemon.requests) != 1 {
		t.Fatalf("resource leases: %d", len(daemon.requests))
	}
	req := daemon.requests[0]
	if len(req.Semaphores) != 2 || req.Resources.Cores != cores || req.SemaphoresOnly {
		t.Fatalf("claims: %+v", req)
	}
	if req.Semaphores[0].Name != "box:machine:a" || req.Semaphores[1].Name != "run:r:b" {
		t.Fatalf("scope/order: %+v", req.Semaphores)
	}
}

func TestV2RetryBudgetCountsAdditionalAttempts(t *testing.T) {
	timeout, backoff := int64(100), int64(10)
	job := nodeproto.JobSpec{TimeoutMS: &timeout, Retry: &nodeproto.RetrySpec{Attempts: 2, BackoffMS: &backoff}}
	if got := AttemptsBudget(job); got != 330*time.Millisecond {
		t.Fatalf("budget = %v", got)
	}
}

func TestV2AttemptEnvironmentSeparatesStartupAndBody(t *testing.T) {
	got := attemptEnv([]string{"PATH=/bin", "SPARKWING_NODE_TOKEN=old", "SPARKWING_NODE_URL=old"}, map[string]nodeproto.EnvValue{"NODE_OPTIONS": {Value: "--max-old-space-size=2048", Startup: true}, "BODY": {Value: "later"}})
	if strings.Join(got, "\n") != "PATH=/bin\nNODE_OPTIONS=--max-old-space-size=2048" {
		t.Fatalf("attempt environment: %v", got)
	}
}
