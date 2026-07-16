package k8s

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capacity"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
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
