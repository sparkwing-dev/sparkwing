package k8s

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capacity"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func jobEnv(t *testing.T, cfg Config) map[string]string {
	t.Helper()
	r := &Runner{cfg: cfg}
	job := r.buildJob("job-name", runner.Request{RunID: "run-1", NodeID: "node-1"}, capacity.Resolution{Source: store.CostSourceDefault})
	containers := job.Spec.Template.Spec.Containers
	if len(containers) != 1 {
		t.Fatalf("containers = %d, want 1", len(containers))
	}
	out := map[string]string{}
	for _, e := range containers[0].Env {
		out[e.Name] = e.Value
	}
	return out
}

func TestBuildJob_StampsArtifactStoreURLWhenSet(t *testing.T) {
	env := jobEnv(t, Config{Image: "img", ArtifactStoreURL: "s3://bucket/prefix"})
	if got := env["SPARKWING_CACHE_URL"]; got != "s3://bucket/prefix" {
		t.Fatalf("SPARKWING_CACHE_URL = %q, want s3://bucket/prefix", got)
	}
}

func TestBuildJob_OmitsArtifactStoreURLWhenEmpty(t *testing.T) {
	env := jobEnv(t, Config{Image: "img"})
	if _, ok := env["SPARKWING_CACHE_URL"]; ok {
		t.Fatalf("SPARKWING_CACHE_URL should be absent when ArtifactStoreURL is empty")
	}
}

func TestBuildJob_UsesWritableGoCachePaths(t *testing.T) {
	env := jobEnv(t, Config{Image: "img"})
	for key, want := range map[string]string{
		"HOME":       "/tmp",
		"GOCACHE":    "/tmp/go-build",
		"GOMODCACHE": "/tmp/go-mod",
	} {
		if got := env[key]; got != want {
			t.Fatalf("%s = %q, want %q", key, got, want)
		}
	}
}

func TestBuildJob_RunsNodeThroughRunnerBinary(t *testing.T) {
	r := &Runner{cfg: Config{Image: "img"}}
	job := r.buildJob("job-name", runner.Request{RunID: "run-1", NodeID: "node-1"}, capacity.Resolution{Source: store.CostSourceDefault})
	container := job.Spec.Template.Spec.Containers[0]
	if !reflect.DeepEqual(container.Command, []string{"sparkwing"}) {
		t.Fatalf("command = %#v, want sparkwing", container.Command)
	}
	if !reflect.DeepEqual(container.Args, []string{"run-node", "run-1", "node-1"}) {
		t.Fatalf("args = %#v, want run-node run-1 node-1", container.Args)
	}
}

func TestBuildJob_UsesRestrictedPodSecurityContext(t *testing.T) {
	r := &Runner{cfg: Config{Image: "img"}}
	job := r.buildJob("job-name", runner.Request{RunID: "run-1", NodeID: "node-1"}, capacity.Resolution{Source: store.CostSourceDefault})
	pod := job.Spec.Template.Spec
	if pod.SecurityContext == nil {
		t.Fatal("pod security context is nil")
	}
	if pod.SecurityContext.RunAsNonRoot == nil || !*pod.SecurityContext.RunAsNonRoot {
		t.Fatal("pod runAsNonRoot is not true")
	}
	if pod.SecurityContext.SeccompProfile == nil || pod.SecurityContext.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Fatalf("pod seccomp profile = %#v, want RuntimeDefault", pod.SecurityContext.SeccompProfile)
	}

	container := pod.Containers[0]
	if container.SecurityContext == nil {
		t.Fatal("container security context is nil")
	}
	if container.SecurityContext.AllowPrivilegeEscalation == nil || *container.SecurityContext.AllowPrivilegeEscalation {
		t.Fatal("container allowPrivilegeEscalation is not false")
	}
	if container.SecurityContext.RunAsNonRoot == nil || !*container.SecurityContext.RunAsNonRoot {
		t.Fatal("container runAsNonRoot is not true")
	}
	if container.SecurityContext.Capabilities == nil || len(container.SecurityContext.Capabilities.Drop) != 1 || container.SecurityContext.Capabilities.Drop[0] != "ALL" {
		t.Fatalf("container dropped capabilities = %#v, want [ALL]", container.SecurityContext.Capabilities)
	}
}

func TestRunNode_MissingJobReturnsFailed(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if err := st.CreateRun(ctx, store.Run{ID: "run-1", Pipeline: "demo", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "run-1", NodeID: "build", Status: "running"}); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	srv := httptest.NewServer(controller.New(st, nil).Handler())
	defer srv.Close()

	kcli := fake.NewSimpleClientset()
	kcli.PrependReactor("get", "jobs", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewNotFound(schema.GroupResource{Group: "batch", Resource: "jobs"}, action.(k8stesting.GetAction).GetName())
	})
	r := New(kcli, client.New(srv.URL, nil), Config{
		Namespace:             "default",
		Image:                 "runner",
		ControllerURL:         srv.URL,
		PollInterval:          time.Millisecond,
		MissingJobGracePeriod: 5 * time.Millisecond,
	}, nil)

	res := r.RunNode(ctx, runner.Request{RunID: "run-1", NodeID: "build"})
	if res.Outcome != sparkwing.Failed {
		t.Fatalf("outcome = %s, want failed", res.Outcome)
	}
	if res.Err == nil || !strings.Contains(res.Err.Error(), "disappeared before reaching a terminal condition") {
		t.Fatalf("err = %v, want missing-job failure", res.Err)
	}
	n, err := st.GetNode(ctx, "run-1", "build")
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if n.Status != "done" || n.Outcome != string(sparkwing.Failed) {
		t.Fatalf("node terminal = status %q outcome %q, want done/failed", n.Status, n.Outcome)
	}
}

func TestRunNode_MissingJobFinalizesDoneNodeWithEmptyOutcome(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if err := st.CreateRun(ctx, store.Run{ID: "run-1", Pipeline: "demo", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "run-1", NodeID: "build", Status: "done"}); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	srv := httptest.NewServer(controller.New(st, nil).Handler())
	defer srv.Close()

	kcli := fake.NewSimpleClientset()
	kcli.PrependReactor("get", "jobs", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewNotFound(schema.GroupResource{Group: "batch", Resource: "jobs"}, action.(k8stesting.GetAction).GetName())
	})
	r := New(kcli, client.New(srv.URL, nil), Config{
		Namespace:             "default",
		Image:                 "runner",
		ControllerURL:         srv.URL,
		PollInterval:          time.Millisecond,
		MissingJobGracePeriod: time.Millisecond,
	}, nil)

	res := r.RunNode(ctx, runner.Request{RunID: "run-1", NodeID: "build"})
	if res.Outcome != sparkwing.Failed {
		t.Fatalf("outcome = %q, want failed", res.Outcome)
	}
	if res.Err == nil || !strings.Contains(res.Err.Error(), "disappeared before reaching a terminal condition") {
		t.Fatalf("err = %v, want missing-job failure", res.Err)
	}
}

func TestRunNode_MissingJobUsesTerminalNodeDuringGrace(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if err := st.CreateRun(ctx, store.Run{ID: "run-1", Pipeline: "demo", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "run-1", NodeID: "build", Status: "running"}); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	srv := httptest.NewServer(controller.New(st, nil).Handler())
	defer srv.Close()

	kcli := fake.NewSimpleClientset()
	kcli.PrependReactor("get", "jobs", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewNotFound(schema.GroupResource{Group: "batch", Resource: "jobs"}, action.(k8stesting.GetAction).GetName())
	})
	r := New(kcli, client.New(srv.URL, nil), Config{
		Namespace:             "default",
		Image:                 "runner",
		ControllerURL:         srv.URL,
		PollInterval:          time.Millisecond,
		MissingJobGracePeriod: 100 * time.Millisecond,
	}, nil)
	go func() {
		time.Sleep(5 * time.Millisecond)
		_ = st.FinishNode(ctx, "run-1", "build", string(sparkwing.Success), "", []byte(`{"ok":true}`))
	}()

	res := r.RunNode(ctx, runner.Request{RunID: "run-1", NodeID: "build"})
	if res.Outcome != sparkwing.Success {
		t.Fatalf("outcome = %s, want success from terminal node row (err=%v)", res.Outcome, res.Err)
	}
	output, ok := res.Output.([]byte)
	if !ok || string(output) != `{"ok":true}` {
		t.Fatalf("output = %#v, want terminal node output", res.Output)
	}
}

func TestRunNode_MissingJobReturnsLateTerminalNodeAfterGrace(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if err := st.CreateRun(ctx, store.Run{ID: "run-1", Pipeline: "demo", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "run-1", NodeID: "build", Status: "running"}); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	controllerHandler := controller.New(st, nil).Handler()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/nodes/build/finish") {
			if err := st.FinishNode(ctx, "run-1", "build", string(sparkwing.Success), "", []byte(`{"late":true}`)); err != nil {
				t.Errorf("FinishNode: %v", err)
			}
		}
		controllerHandler.ServeHTTP(w, r)
	}))
	defer srv.Close()

	kcli := fake.NewSimpleClientset()
	kcli.PrependReactor("get", "jobs", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewNotFound(schema.GroupResource{Group: "batch", Resource: "jobs"}, action.(k8stesting.GetAction).GetName())
	})
	r := New(kcli, client.New(srv.URL, nil), Config{
		Namespace:             "default",
		Image:                 "runner",
		ControllerURL:         srv.URL,
		PollInterval:          time.Millisecond,
		MissingJobGracePeriod: time.Millisecond,
	}, nil)

	res := r.RunNode(ctx, runner.Request{RunID: "run-1", NodeID: "build"})
	if res.Outcome != sparkwing.Success {
		t.Fatalf("outcome = %s, want late terminal success (err=%v)", res.Outcome, res.Err)
	}
	output, ok := res.Output.([]byte)
	if !ok || string(output) != `{"late":true}` {
		t.Fatalf("output = %#v, want late terminal node output", res.Output)
	}
}

// defaultsCfg is the conservative fallback the cold-start tier and any
// unset dimension resolve to.
var defaultsCfg = Config{
	CPURequest: "100m", CPULimit: "2", MemoryRequest: "128Mi", MemoryLimit: "2Gi",
}

func milli(q resource.Quantity) int64 { return q.MilliValue() }
func bytesOf(q resource.Quantity) int64 {
	v, _ := q.AsInt64()
	return v
}

func TestPodResources_PinDrivesRequestAndPolicyLimit(t *testing.T) {
	res := capacity.Resolution{Cores: 4, MemoryBytes: 8 << 30, Source: store.CostSourcePin}
	rr := podResources(res, defaultsCfg)
	if got := milli(rr.Requests[corev1.ResourceCPU]); got != 4000 {
		t.Errorf("cpu request = %dm, want 4000m", got)
	}
	if got := milli(rr.Limits[corev1.ResourceCPU]); got != 8000 {
		t.Errorf("cpu limit = %dm, want 8000m (2x request)", got)
	}
	if got := bytesOf(rr.Requests[corev1.ResourceMemory]); got != 8<<30 {
		t.Errorf("mem request = %d, want %d", got, int64(8<<30))
	}
	if got := bytesOf(rr.Limits[corev1.ResourceMemory]); got != int64(float64(8<<30)*podMemoryLimitFactor) {
		t.Errorf("mem limit = %d, want %d (1.25x request)", got, int64(float64(8<<30)*podMemoryLimitFactor))
	}
}

func TestPodResources_MeasuredPeaksDriveRequest(t *testing.T) {
	res := capacity.Resolution{Cores: 1.5, MemoryBytes: 3 << 30, Source: store.CostSourceMeasured}
	rr := podResources(res, defaultsCfg)
	if got := milli(rr.Requests[corev1.ResourceCPU]); got != 1500 {
		t.Errorf("cpu request = %dm, want 1500m", got)
	}
	if got := milli(rr.Limits[corev1.ResourceCPU]); got != 3000 {
		t.Errorf("cpu limit = %dm, want 3000m", got)
	}
}

func TestPodResources_DefaultTierFallsBackToConfig(t *testing.T) {
	res := capacity.Resolution{Cores: 8, Source: store.CostSourceDefault}
	rr := podResources(res, defaultsCfg)
	if got := milli(rr.Requests[corev1.ResourceCPU]); got != 100 {
		t.Errorf("default cpu request = %dm, want 100m (config, not half-machine)", got)
	}
	if got := milli(rr.Limits[corev1.ResourceCPU]); got != 2000 {
		t.Errorf("default cpu limit = %dm, want 2000m (config)", got)
	}
	if got := bytesOf(rr.Requests[corev1.ResourceMemory]); got != 128<<20 {
		t.Errorf("default mem request = %d, want %d", got, int64(128<<20))
	}
}

func TestPodResources_PinCoresOnlyFallsBackForMemory(t *testing.T) {
	res := capacity.Resolution{Cores: 2, Source: store.CostSourcePin}
	rr := podResources(res, defaultsCfg)
	if got := milli(rr.Requests[corev1.ResourceCPU]); got != 2000 {
		t.Errorf("cpu request = %dm, want 2000m", got)
	}
	if got := bytesOf(rr.Requests[corev1.ResourceMemory]); got != 128<<20 {
		t.Errorf("mem request should fall back to config default: got %d want %d", got, int64(128<<20))
	}
}

func TestResolveResources_ClearsControllerPinWhenNodeDeclaresNone(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()
	if err := st.RecordProfileObservation(ctx, "deploy", "build", store.ProfileObservation{
		Duration: time.Minute, PeakCores: 1.2, PeakMemoryBytes: 1 << 30, CPUMeasured: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertProfilePin(ctx, "deploy", "build", 0.25, 0); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(controller.New(st, nil).Handler())
	defer srv.Close()

	plan := sparkwing.NewPlan()
	node := sparkwing.Job(plan, "build", func(context.Context) error { return nil })
	k8sRunner := &Runner{ctrl: client.New(srv.URL, nil), cfg: defaultsCfg}
	_ = k8sRunner.resolveResources(ctx, runner.Request{Pipeline: "deploy", NodeID: "build", Node: node})

	profile, err := st.GetPipelineProfile(ctx, "deploy", "build")
	if err != nil || profile == nil {
		t.Fatalf("profile missing: %v", err)
	}
	if profile.PinnedCores != 0 || profile.PinnedMemoryBytes != 0 {
		t.Fatalf("controller pin = %.2f cores/%d bytes, want cleared after undeclared node", profile.PinnedCores, profile.PinnedMemoryBytes)
	}
}
