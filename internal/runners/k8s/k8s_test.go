package k8s

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capacity"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func jobEnv(t *testing.T, cfg Config) map[string]string {
	t.Helper()
	r := New(nil, nil, cfg, nil)
	job := r.buildJob("job-name", runner.Request{RunID: "run-1", NodeID: "node-1"},
		capacity.Resolution{Source: store.CostSourceDefault}, store.CPUClass{}, store.NodeClaimFence{})
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

func TestRunnerLabelsOwnNormalizedFallbackCapabilities(t *testing.T) {
	configured := []string{" cluster ", "", "cluster", "kubernetes"}
	r := New(nil, nil, Config{Labels: configured}, nil)
	configured[0] = "arch=arm64"

	if got, want := r.AdvertisedLabels(), []string{"cluster", "kubernetes"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("AdvertisedLabels() = %v, want %v", got, want)
	}
	got := r.AdvertisedLabels()
	got[0] = "mutated"
	if reread := r.AdvertisedLabels(); !reflect.DeepEqual(reread, []string{"cluster", "kubernetes"}) {
		t.Fatalf("AdvertisedLabels() shared mutable storage: %v", reread)
	}
}

func TestBuildJob_ReportsTheCapabilitiesUsedForFallbackEligibility(t *testing.T) {
	env := jobEnv(t, Config{Image: "img", Labels: []string{" cluster ", "", "cluster", "kubernetes"}})
	if got := env["SPARKWING_RUNNER_TYPE"]; got != "kubernetes" {
		t.Fatalf("SPARKWING_RUNNER_TYPE = %q, want kubernetes", got)
	}
	if got := env["SPARKWING_RUNNER_NAME"]; got != "job-name" {
		t.Fatalf("SPARKWING_RUNNER_NAME = %q, want job-name", got)
	}
	if got := env["SPARKWING_RUNNER_LABELS"]; got != "cluster,kubernetes" {
		t.Fatalf("SPARKWING_RUNNER_LABELS = %q, want cluster,kubernetes", got)
	}
	if strings.Contains(env["SPARKWING_RUNNER_LABELS"], "arch=") {
		t.Fatalf("SPARKWING_RUNNER_LABELS = %q, inherited an architecture the Job placement did not promise", env["SPARKWING_RUNNER_LABELS"])
	}
}

func TestBuildJob_DefaultsToNoFallbackCapabilities(t *testing.T) {
	r := New(nil, nil, Config{Image: "img"}, nil)
	if labels := r.AdvertisedLabels(); len(labels) != 0 {
		t.Fatalf("AdvertisedLabels() = %v, want none", labels)
	}
	env := jobEnv(t, Config{Image: "img"})
	if _, ok := env["SPARKWING_RUNNER_LABELS"]; ok {
		t.Fatalf("SPARKWING_RUNNER_LABELS = %q, want the empty default omitted", env["SPARKWING_RUNNER_LABELS"])
	}
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

func TestBuildJob_PointsPackageManagersAtTheDependencyProxy(t *testing.T) {
	env := jobEnv(t, Config{Image: "img", DependencyProxyURL: "http://cache.sparkwing.svc.cluster.local"})
	for key, want := range map[string]string{
		"GOPROXY":             "http://cache.sparkwing.svc.cluster.local/proxy/golang|https://proxy.golang.org,direct",
		"npm_config_registry": "http://cache.sparkwing.svc.cluster.local/proxy/npm",
		"PIP_INDEX_URL":       "http://cache.sparkwing.svc.cluster.local/proxy/pypi/simple/",
		"PIP_TRUSTED_HOST":    "cache.sparkwing.svc.cluster.local",
	} {
		if got := env[key]; got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}

func TestDependencyProxyEnv_NamesAreValidK8sEnvNames(t *testing.T) {
	for _, e := range dependencyProxyEnv("http://cache") {
		if errs := validation.IsEnvVarName(e.Name); len(errs) > 0 {
			t.Errorf("env name %q rejected by K8s validation: %v", e.Name, errs)
		}
	}
}

func TestBuildJob_OmitsDependencyProxyEnvWhenUnset(t *testing.T) {
	env := jobEnv(t, Config{Image: "img"})
	for _, key := range []string{"GOPROXY", "npm_config_registry", "PIP_INDEX_URL", "PIP_TRUSTED_HOST"} {
		if _, ok := env[key]; ok {
			t.Errorf("%s should be absent when DependencyProxyURL is empty", key)
		}
	}
}

func TestDependencyProxyEnv_URLJoin(t *testing.T) {
	for _, tc := range []struct {
		name        string
		base        string
		wantGoproxy string
		wantHost    string
	}{
		{
			name:        "trailing slash does not double up",
			base:        "http://cache/",
			wantGoproxy: "http://cache/proxy/golang|https://proxy.golang.org,direct",
			wantHost:    "cache",
		},
		{
			name:        "explicit port travels into the trusted host",
			base:        "http://cache:8090",
			wantGoproxy: "http://cache:8090/proxy/golang|https://proxy.golang.org,direct",
			wantHost:    "cache:8090",
		},
		{
			name:        "path prefix is preserved for an ingress-mounted cache",
			base:        "https://sparkwing.example.com/cache",
			wantGoproxy: "https://sparkwing.example.com/cache/proxy/golang|https://proxy.golang.org,direct",
			wantHost:    "sparkwing.example.com",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := map[string]string{}
			for _, e := range dependencyProxyEnv(tc.base) {
				got[e.Name] = e.Value
			}
			if got["GOPROXY"] != tc.wantGoproxy {
				t.Errorf("GOPROXY = %q, want %q", got["GOPROXY"], tc.wantGoproxy)
			}
			if got["PIP_TRUSTED_HOST"] != tc.wantHost {
				t.Errorf("PIP_TRUSTED_HOST = %q, want %q", got["PIP_TRUSTED_HOST"], tc.wantHost)
			}
		})
	}
}

func TestDependencyProxyEnv_RejectsUnusableBase(t *testing.T) {
	for _, base := range []string{"", "cache.sparkwing.svc", "http://", "://cache"} {
		if got := dependencyProxyEnv(base); got != nil {
			t.Errorf("dependencyProxyEnv(%q) = %#v, want nil", base, got)
		}
	}
}

func TestResolveDependencyProxy(t *testing.T) {
	for _, tc := range []struct {
		name     string
		explicit string
		cacheURL string
		want     string
	}{
		{name: "explicit wins", explicit: "http://mirror", cacheURL: "http://cache", want: "http://mirror"},
		{name: "off disables", explicit: "off", cacheURL: "http://cache", want: ""},
		{name: "off is case-insensitive", explicit: "OFF", cacheURL: "http://cache", want: ""},
		{name: "empty derives from the cache service", cacheURL: "http://cache", want: "http://cache"},
		{name: "no cache service, no proxy", want: ""},
		{name: "non-HTTP store is not a proxy", cacheURL: "s3://bucket/prefix", want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ResolveDependencyProxy(tc.explicit, tc.cacheURL); got != tc.want {
				t.Errorf("ResolveDependencyProxy(%q, %q) = %q, want %q", tc.explicit, tc.cacheURL, got, tc.want)
			}
		})
	}
}

func TestBuildJob_DefaultsToIfNotPresentPullPolicy(t *testing.T) {
	r := &Runner{cfg: Config{Image: "img"}}
	job := r.buildJob("job-name", runner.Request{RunID: "run-1", NodeID: "node-1"},
		capacity.Resolution{Source: store.CostSourceDefault}, store.CPUClass{}, store.NodeClaimFence{})
	if got := job.Spec.Template.Spec.Containers[0].ImagePullPolicy; got != corev1.PullIfNotPresent {
		t.Fatalf("imagePullPolicy = %q, want IfNotPresent", got)
	}
}

func TestBuildJob_HonoursConfiguredPullPolicy(t *testing.T) {
	r := &Runner{cfg: Config{Image: "img", ImagePullPolicy: corev1.PullAlways}}
	job := r.buildJob("job-name", runner.Request{RunID: "run-1", NodeID: "node-1"},
		capacity.Resolution{Source: store.CostSourceDefault}, store.CPUClass{}, store.NodeClaimFence{})
	if got := job.Spec.Template.Spec.Containers[0].ImagePullPolicy; got != corev1.PullAlways {
		t.Fatalf("imagePullPolicy = %q, want Always", got)
	}
}

func TestParsePullPolicy(t *testing.T) {
	for in, want := range map[string]corev1.PullPolicy{
		"":              corev1.PullIfNotPresent,
		"IfNotPresent":  corev1.PullIfNotPresent,
		"ifnotpresent":  corev1.PullIfNotPresent,
		"Always":        corev1.PullAlways,
		"Never":         corev1.PullNever,
		" IfNotPresent": corev1.PullIfNotPresent,
	} {
		got, err := ParsePullPolicy(in)
		if err != nil || got != want {
			t.Errorf("ParsePullPolicy(%q) = (%q, %v), want (%q, nil)", in, got, err, want)
		}
	}
	for _, in := range []string{"always-ish", "IfPresent", "true"} {
		if _, err := ParsePullPolicy(in); err == nil {
			t.Errorf("ParsePullPolicy(%q) = nil error, want a rejection", in)
		}
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
	job := r.buildJob("job-name", runner.Request{RunID: "run-1", NodeID: "node-1"},
		capacity.Resolution{Source: store.CostSourceDefault}, store.CPUClass{}, store.NodeClaimFence{})
	container := job.Spec.Template.Spec.Containers[0]
	if !reflect.DeepEqual(container.Command, []string{"sparkwing-runner"}) {
		t.Fatalf("command = %#v, want sparkwing-runner", container.Command)
	}
	if JobBinary != "sparkwing-runner" {
		t.Fatalf("JobBinary = %q, want sparkwing-runner", JobBinary)
	}
	if !reflect.DeepEqual(container.Args, []string{"run-node", "run-1", "node-1"}) {
		t.Fatalf("args = %#v, want run-node run-1 node-1", container.Args)
	}
}

func TestBuildJob_UsesRestrictedPodSecurityContext(t *testing.T) {
	r := &Runner{cfg: Config{Image: "img"}}
	job := r.buildJob("job-name", runner.Request{RunID: "run-1", NodeID: "node-1"},
		capacity.Resolution{Source: store.CostSourceDefault}, store.CPUClass{}, store.NodeClaimFence{})
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
	if container.SecurityContext.ReadOnlyRootFilesystem == nil || !*container.SecurityContext.ReadOnlyRootFilesystem {
		t.Fatalf("container readOnlyRootFilesystem = %#v, want true", container.SecurityContext.ReadOnlyRootFilesystem)
	}
}

func TestBuildJob_MountsScratchOverEveryWritablePath(t *testing.T) {
	r := &Runner{cfg: Config{Image: "img"}}
	job := r.buildJob("job-name", runner.Request{RunID: "run-1", NodeID: "node-1"},
		capacity.Resolution{Source: store.CostSourceDefault}, store.CPUClass{}, store.NodeClaimFence{})
	pod := job.Spec.Template.Spec
	if len(pod.Volumes) != 1 || pod.Volumes[0].Name != scratchVolumeName || pod.Volumes[0].EmptyDir == nil {
		t.Fatalf("pod volumes = %#v, want one scratch emptyDir", pod.Volumes)
	}
	mounts := pod.Containers[0].VolumeMounts
	if len(mounts) != 1 || mounts[0].Name != scratchVolumeName || mounts[0].MountPath != "/tmp" {
		t.Fatalf("container volume mounts = %#v, want scratch at /tmp", mounts)
	}
	set := map[string]string{}
	for _, env := range pod.Containers[0].Env {
		set[env.Name] = env.Value
	}
	for _, name := range []string{"HOME", "GOCACHE", "GOMODCACHE", "SPARKWING_HOME"} {
		value, ok := set[name]
		if !ok {
			t.Fatalf("Job env has no %s, so the read-only root leaves its default unwritable: %#v", name, set)
		}
		if !strings.HasPrefix(value, "/tmp") {
			t.Fatalf("%s = %q, which the read-only root leaves unwritable", name, value)
		}
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
	if err := st.CreateNode(ctx, store.Node{RunID: "run-1", NodeID: "build", Status: "pending"}); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	if err := st.MarkNodeReady(ctx, "run-1", "build"); err != nil {
		t.Fatalf("MarkNodeReady: %v", err)
	}
	srv := httptest.NewServer(controller.New(st, nil).Handler())
	defer srv.Close()

	// safety: a done node refuses a named claim, so the node turns done with
	// no outcome only once the Job exists, which is the state a pod that died
	// between its two writes leaves.
	kcli := fake.NewSimpleClientset()
	kcli.PrependReactor("create", "jobs", func(k8stesting.Action) (bool, runtime.Object, error) {
		if err := st.SetNodeStatus(ctx, "run-1", "build", "done"); err != nil {
			t.Errorf("SetNodeStatus: %v", err)
		}
		return false, nil, nil
	})
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
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	setup := context.Background()
	if err := st.CreateRun(setup, store.Run{ID: "run-1", Pipeline: "demo", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := st.CreateNode(setup, store.Node{RunID: "run-1", NodeID: "build", Status: "running"}); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	// safety: opening and migrating the store under -race takes longer than
	// the budget, so the budget starts once setup is done and bounds only the
	// runner's handling of the missing Job.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	srv := httptest.NewServer(controller.New(st, nil).Handler())
	defer srv.Close()

	jobMissing := make(chan struct{}, 1)
	kcli := fake.NewSimpleClientset()
	kcli.PrependReactor("get", "jobs", func(action k8stesting.Action) (bool, runtime.Object, error) {
		select {
		case jobMissing <- struct{}{}:
		default:
		}
		return true, nil, apierrors.NewNotFound(schema.GroupResource{Group: "batch", Resource: "jobs"}, action.(k8stesting.GetAction).GetName())
	})
	r := New(kcli, client.New(srv.URL, nil), Config{
		Namespace:             "default",
		Image:                 "runner",
		ControllerURL:         srv.URL,
		PollInterval:          time.Millisecond,
		MissingJobGracePeriod: 100 * time.Millisecond,
	}, nil)
	finishErr := make(chan error, 1)
	go func() {
		select {
		case <-jobMissing:
			finishErr <- st.FinishNode(ctx, "run-1", "build", string(sparkwing.Success), "", []byte(`{"ok":true}`))
		case <-ctx.Done():
			finishErr <- ctx.Err()
		}
	}()

	res := r.RunNode(ctx, runner.Request{RunID: "run-1", NodeID: "build"})
	if err := <-finishErr; err != nil {
		t.Fatalf("finish terminal node: %v", err)
	}
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

var defaultsCfg = Config{
	CPURequest: "100m", CPULimit: "2", MemoryRequest: "128Mi", MemoryLimit: "2Gi",
}

func milli(q resource.Quantity) int64 { return q.MilliValue() }
func bytesOf(q resource.Quantity) int64 {
	v, _ := q.AsInt64()
	return v
}

func TestPodResources_ABilledClassIsTheWholePodShape(t *testing.T) {
	for _, tc := range []struct {
		name      string
		class     store.CPUClass
		res       capacity.Resolution
		cfg       Config
		wantCPU   int64
		wantBytes int64
	}{
		{
			name:      "the eight-core class",
			class:     store.CPUClass{Cores: 8, MemoryBytes: 32 << 30},
			res:       capacity.Resolution{Cores: 8, MemoryBytes: 8 << 30, Source: store.CostSourcePin},
			cfg:       defaultsCfg,
			wantCPU:   8000,
			wantBytes: 32 << 30,
		},
		{
			name:      "a class an operator ladder priced, not the default one",
			class:     store.CPUClass{Cores: 64, MemoryBytes: 256 << 30},
			res:       capacity.Resolution{Cores: 3, MemoryBytes: 1 << 30, Source: store.CostSourcePin},
			cfg:       defaultsCfg,
			wantCPU:   64000,
			wantBytes: 256 << 30,
		},
		{
			name:      "a ceiling never shrinks the class the claim billed",
			class:     store.CPUClass{Cores: 8, MemoryBytes: 32 << 30},
			res:       capacity.Resolution{Cores: 8, MemoryBytes: 32 << 30, Source: store.CostSourcePin},
			cfg:       ceilingCfg,
			wantCPU:   8000,
			wantBytes: 32 << 30,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rr := podResources(tc.res, tc.class, tc.cfg)
			if got := milli(rr.Requests[corev1.ResourceCPU]); got != tc.wantCPU {
				t.Errorf("cpu request = %dm, want %dm", got, tc.wantCPU)
			}
			if got := milli(rr.Limits[corev1.ResourceCPU]); got != tc.wantCPU {
				t.Errorf("cpu limit = %dm, want the request %dm", got, tc.wantCPU)
			}
			if got := bytesOf(rr.Requests[corev1.ResourceMemory]); got != tc.wantBytes {
				t.Errorf("mem request = %d, want %d", got, tc.wantBytes)
			}
			if got := bytesOf(rr.Limits[corev1.ResourceMemory]); got != tc.wantBytes {
				t.Errorf("mem limit = %d, want the request %d", got, tc.wantBytes)
			}
		})
	}
}

// A controller too old to price the node sends no class, and the pod keeps the
// shape it had before classes existed.
func TestPodResources_NoBilledClassKeepsTheBurstLimits(t *testing.T) {
	res := capacity.Resolution{Cores: 4, MemoryBytes: 8 << 30, Source: store.CostSourcePin}
	rr := podResources(res, store.CPUClass{}, defaultsCfg)
	if got := milli(rr.Requests[corev1.ResourceCPU]); got != 4000 {
		t.Errorf("cpu request = %dm, want 4000m", got)
	}
	if got := milli(rr.Limits[corev1.ResourceCPU]); got != 8000 {
		t.Errorf("cpu limit = %dm, want 8000m (2x request)", got)
	}
	if got := bytesOf(rr.Limits[corev1.ResourceMemory]); got != int64(float64(8<<30)*podMemoryLimitFactor) {
		t.Errorf("mem limit = %d, want %d (1.25x request)", got, int64(float64(8<<30)*podMemoryLimitFactor))
	}
}

// A customer billed for a class the operator's ceiling forbids is told so at
// once, rather than running smaller for the same price.
func TestCeilingUnderClass(t *testing.T) {
	for _, tc := range []struct {
		name  string
		cfg   Config
		class store.CPUClass
		want  string
	}{
		{name: "no class", cfg: ceilingCfg, class: store.CPUClass{}, want: ""},
		{name: "no ceiling", cfg: defaultsCfg, class: store.CPUClass{Cores: 64, MemoryBytes: 256 << 30}, want: ""},
		{
			name: "cpu ceiling under the class", cfg: ceilingCfg,
			class: store.CPUClass{Cores: 8, MemoryBytes: 32 << 30}, want: "cpu ceiling",
		},
		{
			name:  "memory ceiling under the class",
			cfg:   Config{MemoryCeiling: 2 << 30},
			class: store.CPUClass{Cores: 2, MemoryBytes: 8 << 30},
			want:  "memory",
		},
		{
			name:  "a ceiling above the class allows it",
			cfg:   Config{CPUCeiling: 16, MemoryCeiling: 64 << 30},
			class: store.CPUClass{Cores: 8, MemoryBytes: 32 << 30}, want: "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &Runner{cfg: tc.cfg}
			got := r.ceilingUnderClass(tc.class)
			if tc.want == "" {
				if got != "" {
					t.Fatalf("refusal = %q, want none", got)
				}
				return
			}
			if !strings.Contains(got, tc.want) {
				t.Fatalf("refusal = %q, want it to name %q", got, tc.want)
			}
		})
	}
}

var ceilingCfg = Config{
	CPURequest: "100m", CPULimit: "2", MemoryRequest: "128Mi", MemoryLimit: "2Gi",
	CPUCeiling: 2, MemoryCeiling: 2 << 30,
}

func TestPodResources_ClampsChargeToTheOperatorCeiling(t *testing.T) {
	cases := []struct {
		name       string
		res        capacity.Resolution
		cfg        Config
		wantCPUReq int64
		wantCPULim int64
		wantMemReq int64
		wantMemLim int64
	}{
		{
			name:       "pin over the cpu ceiling is capped, burst included",
			res:        capacity.Resolution{Cores: 64, MemoryBytes: 1 << 30, Source: store.CostSourcePin},
			cfg:        ceilingCfg,
			wantCPUReq: 2000,
			wantCPULim: 2000,
			wantMemReq: 1 << 30,
			wantMemLim: int64(float64(1<<30) * podMemoryLimitFactor),
		},
		{
			name:       "pin over the memory ceiling is capped, burst included",
			res:        capacity.Resolution{Cores: 1, MemoryBytes: 128 << 30, Source: store.CostSourcePin},
			cfg:        ceilingCfg,
			wantCPUReq: 1000,
			wantCPULim: int64(1000 * podCPULimitFactor),
			wantMemReq: 2 << 30,
			wantMemLim: 2 << 30,
		},
		{
			name:       "measured charge over the ceiling is capped too",
			res:        capacity.Resolution{Cores: 8, MemoryBytes: 16 << 30, Source: store.CostSourceMeasured},
			cfg:        ceilingCfg,
			wantCPUReq: 2000,
			wantCPULim: 2000,
			wantMemReq: 2 << 30,
			wantMemLim: 2 << 30,
		},
		{
			name:       "a charge under the ceiling keeps its full burst",
			res:        capacity.Resolution{Cores: 0.5, MemoryBytes: 1 << 30, Source: store.CostSourcePin},
			cfg:        ceilingCfg,
			wantCPUReq: 500,
			wantCPULim: 1000,
			wantMemReq: 1 << 30,
			wantMemLim: int64(float64(1<<30) * podMemoryLimitFactor),
		},
		{
			name:       "the unmeasured fallback size is capped by the ceiling as well",
			res:        capacity.Resolution{Cores: 8, Source: store.CostSourceDefault},
			cfg:        Config{CPURequest: "100m", CPULimit: "2", MemoryRequest: "128Mi", MemoryLimit: "2Gi", CPUCeiling: 1, MemoryCeiling: 1 << 30},
			wantCPUReq: 100,
			wantCPULim: 1000,
			wantMemReq: 128 << 20,
			wantMemLim: 1 << 30,
		},
		{
			name:       "no configured ceiling leaves the pin alone",
			res:        capacity.Resolution{Cores: 64, MemoryBytes: 128 << 30, Source: store.CostSourcePin},
			cfg:        defaultsCfg,
			wantCPUReq: 64000,
			wantCPULim: int64(64000 * podCPULimitFactor),
			wantMemReq: 128 << 30,
			wantMemLim: int64(float64(128<<30) * podMemoryLimitFactor),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := podResources(tc.res, store.CPUClass{}, tc.cfg)
			if got := milli(rr.Requests[corev1.ResourceCPU]); got != tc.wantCPUReq {
				t.Errorf("cpu request = %dm, want %dm", got, tc.wantCPUReq)
			}
			if got := milli(rr.Limits[corev1.ResourceCPU]); got != tc.wantCPULim {
				t.Errorf("cpu limit = %dm, want %dm", got, tc.wantCPULim)
			}
			if got := bytesOf(rr.Requests[corev1.ResourceMemory]); got != tc.wantMemReq {
				t.Errorf("mem request = %d, want %d", got, tc.wantMemReq)
			}
			if got := bytesOf(rr.Limits[corev1.ResourceMemory]); got != tc.wantMemLim {
				t.Errorf("mem limit = %d, want %d", got, tc.wantMemLim)
			}
		})
	}
}

func TestPodResources_FloorsATinyPinAtTheMeasuredCoreFloor(t *testing.T) {
	for _, cores := range []float64{0.0004, 1e-9, 0.05} {
		res := capacity.Resolution{Cores: cores, MemoryBytes: 1 << 30, Source: store.CostSourcePin}
		rr := podResources(res, store.CPUClass{}, defaultsCfg)
		want := int64(capacity.MeasuredCoreFloor * 1000)
		if got := milli(rr.Requests[corev1.ResourceCPU]); got != want {
			t.Errorf("pin %v cores: cpu request = %dm, want the %dm floor", cores, got, want)
		}
		if got := milli(rr.Limits[corev1.ResourceCPU]); got != int64(float64(want)*podCPULimitFactor) {
			t.Errorf("pin %v cores: cpu limit = %dm, want %dm", cores, got, int64(float64(want)*podCPULimitFactor))
		}
	}
}

func TestPodResources_CeilingUnderTheFloorStillWins(t *testing.T) {
	cfg := Config{CPURequest: "100m", MemoryRequest: "128Mi", CPUCeiling: 0.05}
	res := capacity.Resolution{Cores: 0.0004, MemoryBytes: 1 << 30, Source: store.CostSourcePin}
	rr := podResources(res, store.CPUClass{}, cfg)
	if got := milli(rr.Requests[corev1.ResourceCPU]); got != 50 {
		t.Errorf("cpu request = %dm, want the 50m ceiling, which outranks the core floor", got)
	}
}

func TestParseCeilingRejectsWhatItCannotEnforce(t *testing.T) {
	cpuCases := []struct {
		in      string
		want    float64
		wantErr bool
	}{
		{in: "", want: 0},
		{in: "8", want: 8},
		{in: "500m", want: 0.5},
		{in: "2 cores", wantErr: true},
		{in: "0", wantErr: true},
		{in: "-1", wantErr: true},
	}
	for _, tc := range cpuCases {
		got, err := ParseCPUCeiling(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("ParseCPUCeiling(%q) = %v, want an error", tc.in, got)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("ParseCPUCeiling(%q) = %v, %v, want %v", tc.in, got, err, tc.want)
		}
	}
	memCases := []struct {
		in      string
		want    int64
		wantErr bool
	}{
		{in: "", want: 0},
		{in: "2Gi", want: 2 << 30},
		{in: "2 GB", wantErr: true},
		{in: "0", wantErr: true},
		{in: "-1Gi", wantErr: true},
	}
	for _, tc := range memCases {
		got, err := ParseMemoryCeiling(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("ParseMemoryCeiling(%q) = %v, want an error", tc.in, got)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("ParseMemoryCeiling(%q) = %v, %v, want %v", tc.in, got, err, tc.want)
		}
	}
}

func TestPodResources_MeasuredPeaksDriveRequest(t *testing.T) {
	res := capacity.Resolution{Cores: 1.5, MemoryBytes: 3 << 30, Source: store.CostSourceMeasured}
	rr := podResources(res, store.CPUClass{}, defaultsCfg)
	if got := milli(rr.Requests[corev1.ResourceCPU]); got != 1500 {
		t.Errorf("cpu request = %dm, want 1500m", got)
	}
	if got := milli(rr.Limits[corev1.ResourceCPU]); got != 3000 {
		t.Errorf("cpu limit = %dm, want 3000m", got)
	}
}

func TestPodResources_DefaultTierFallsBackToConfig(t *testing.T) {
	res := capacity.Resolution{Cores: 8, Source: store.CostSourceDefault}
	rr := podResources(res, store.CPUClass{}, defaultsCfg)
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
	rr := podResources(res, store.CPUClass{}, defaultsCfg)
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

func TestResolveResources_AnnouncesTheClampOnBothChannels(t *testing.T) {
	ctx := context.Background()
	var mu sync.Mutex
	var events []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if strings.HasSuffix(req.URL.Path, "/events") {
			body, _ := io.ReadAll(req.Body)
			mu.Lock()
			events = append(events, string(body))
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	var logs bytes.Buffer
	r := &Runner{
		ctrl:   client.New(srv.URL, nil),
		cfg:    ceilingCfg,
		logger: slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn})),
	}
	plan := sparkwing.NewPlan()
	node := sparkwing.Job(plan, "build", func(context.Context) error { return nil }).
		Resources(sparkwing.Cores(64), sparkwing.MemoryGB(128))
	req := runner.Request{RunID: "run-1", Pipeline: "deploy", NodeID: "build", Node: node}

	if res := r.resolveResources(ctx, req); res.Cores != 64 {
		t.Fatalf("resolved cores = %v, want the unclamped charge; podResources owns the clamp", res.Cores)
	}
	line := logs.String()
	for _, want := range []string{"64.0 cores", "ceiling 2.0", "sized at 2.0 cores"} {
		if !strings.Contains(line, want) {
			t.Errorf("log = %q, want a line naming %q", line, want)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(events) != 1 || !strings.Contains(events[0], "resource_clamped") {
		t.Fatalf("run events = %v, want one resource_clamped warning", events)
	}
}

func TestResolveResources_StaysQuietUnderTheCeiling(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if strings.HasSuffix(req.URL.Path, "/events") {
			t.Errorf("unexpected run event for a charge under the ceiling")
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	var logs bytes.Buffer
	r := &Runner{
		ctrl:   client.New(srv.URL, nil),
		cfg:    ceilingCfg,
		logger: slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn})),
	}
	plan := sparkwing.NewPlan()
	node := sparkwing.Job(plan, "build", func(context.Context) error { return nil }).
		Resources(sparkwing.Cores(1), sparkwing.MemoryGB(1))
	r.resolveResources(ctx, runner.Request{RunID: "run-1", Pipeline: "deploy", NodeID: "build", Node: node})
	if logs.Len() != 0 {
		t.Fatalf("log = %q, want silence when the ceiling does not bite", logs.String())
	}
}

func TestBuildJob_MountsNoServiceAccountToken(t *testing.T) {
	r := &Runner{cfg: Config{Image: "img", ServiceAccountName: "runner-jobs"}}
	job := r.buildJob("job-name", runner.Request{RunID: "run-1", NodeID: "node-1"},
		capacity.Resolution{Source: store.CostSourceDefault}, store.CPUClass{}, store.NodeClaimFence{})
	pod := job.Spec.Template.Spec
	if pod.ServiceAccountName != "runner-jobs" {
		t.Fatalf("service account = %q, want runner-jobs", pod.ServiceAccountName)
	}
	if pod.AutomountServiceAccountToken == nil || *pod.AutomountServiceAccountToken {
		t.Fatalf("automountServiceAccountToken = %v, want false", pod.AutomountServiceAccountToken)
	}
}

// The pod runs the team's code, so it carries the run's grant and never the
// operator cache token, even when the dispatcher's own environment holds one.
func TestBuildJob_PassesTheRunGrantAndNeverTheCacheToken(t *testing.T) {
	t.Setenv("SPARKWING_CACHE_TOKEN", "operator-cache-token")
	t.Setenv("SPARKWING_CACHE_GRANT", "swcg1.run-grant")
	env := jobEnv(t, Config{Image: "img"})
	if got := env["SPARKWING_CACHE_GRANT"]; got != "swcg1.run-grant" {
		t.Fatalf("SPARKWING_CACHE_GRANT = %q, want the run's grant", got)
	}
	if got, ok := env["SPARKWING_CACHE_TOKEN"]; ok {
		t.Fatalf("SPARKWING_CACHE_TOKEN = %q reached the pod", got)
	}
	for name, value := range env {
		if value == "operator-cache-token" {
			t.Fatalf("%s carries the operator cache token into the pod", name)
		}
	}
}

func TestBuildJob_OmitsTheGrantWhenTheRunHasNone(t *testing.T) {
	t.Setenv("SPARKWING_CACHE_GRANT", "")
	if _, ok := jobEnv(t, Config{Image: "img"})["SPARKWING_CACHE_GRANT"]; ok {
		t.Fatal("SPARKWING_CACHE_GRANT should be absent when the run has none")
	}
}

func TestBuildJob_HandsThePodTheGitcacheURL(t *testing.T) {
	r := &Runner{cfg: Config{Image: "img", GitcacheURL: "http://cache.local"}}
	job := r.buildJob("job-name", runner.Request{RunID: "run-1", NodeID: "node-1"}, capacity.Resolution{}, store.CPUClass{}, store.NodeClaimFence{})
	var got string
	for _, e := range job.Spec.Template.Spec.Containers[0].Env {
		if e.Name == "SPARKWING_GITCACHE_URL" {
			got = e.Value
		}
	}
	if got != "http://cache.local" {
		t.Fatalf("SPARKWING_GITCACHE_URL = %q, want the configured gitcache", got)
	}
	bare := (&Runner{cfg: Config{Image: "img"}}).buildJob("job-name", runner.Request{RunID: "run-1", NodeID: "node-1"}, capacity.Resolution{}, store.CPUClass{}, store.NodeClaimFence{})
	for _, e := range bare.Spec.Template.Spec.Containers[0].Env {
		if e.Name == "SPARKWING_GITCACHE_URL" {
			t.Fatalf("an unset gitcache must not reach the pod, got %q", e.Value)
		}
	}
}

func fenceJobEnv(t *testing.T, fence store.NodeClaimFence) map[string]string {
	t.Helper()
	r := &Runner{cfg: Config{Image: "img"}}
	job := r.buildJob("job-name", runner.Request{RunID: "run-1", NodeID: "node-1"},
		capacity.Resolution{}, store.CPUClass{}, fence)
	out := map[string]string{}
	for _, e := range job.Spec.Template.Spec.Containers[0].Env {
		out[e.Name] = e.Value
	}
	return out
}

func TestBuildJob_HandsThePodTheClaimFence(t *testing.T) {
	env := fenceJobEnv(t, store.NodeClaimFence{
		HolderID: "k8s-job:sw-abc", MembershipID: "m-1",
		ReservationID: "res-1", ClaimGeneration: 7,
	})
	for name, want := range map[string]string{
		ClaimHolderEnv:       "k8s-job:sw-abc",
		ClaimGenerationEnv:   "7",
		ClaimMembershipEnv:   "m-1",
		ClaimReservationEnv:  "res-1",
		ClaimLeaseSecondsEnv: "600",
	} {
		if env[name] != want {
			t.Errorf("%s = %q, want %q", name, env[name], want)
		}
	}
}

func TestBuildJob_OmitsTheClaimFenceWhenNoClaimWasAwarded(t *testing.T) {
	env := fenceJobEnv(t, store.NodeClaimFence{})
	for _, name := range []string{ClaimHolderEnv, ClaimGenerationEnv, ClaimMembershipEnv, ClaimReservationEnv} {
		if _, ok := env[name]; ok {
			t.Errorf("%s is set on a Job that holds no claim", name)
		}
	}
}

func TestRunNode_ClaimsTheNodeBeforeItCreatesTheJob(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if err := st.CreateRun(ctx, store.Run{ID: "run-1", Pipeline: "demo", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "run-1", NodeID: "build", Status: "pending"}); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	srv := httptest.NewServer(controller.New(st, nil).Handler())
	defer srv.Close()

	kcli := fake.NewSimpleClientset()
	created := make(chan *batchv1.Job, 1)
	kcli.PrependReactor("create", "jobs", func(action k8stesting.Action) (bool, runtime.Object, error) {
		job := action.(k8stesting.CreateAction).GetObject().(*batchv1.Job)
		select {
		case created <- job:
		default:
		}
		return false, nil, nil
	})
	kcli.PrependReactor("get", "jobs", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, &batchv1.Job{Status: batchv1.JobStatus{Succeeded: 1}}, nil
	})
	r := New(kcli, client.New(srv.URL, nil), Config{
		Namespace: "default", Image: "runner", ControllerURL: srv.URL,
		PollInterval: time.Millisecond, MissingJobGracePeriod: time.Millisecond,
	}, nil)

	r.RunNode(ctx, runner.Request{RunID: "run-1", NodeID: "build"})

	n, err := st.GetNode(ctx, "run-1", "build")
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if n.ClaimedBy != "k8s-job:"+JobName("run-1", "build", 0) {
		t.Fatalf("claimed_by = %q, want the dispatcher's Job holder", n.ClaimedBy)
	}
	if n.ClaimGeneration < 1 {
		t.Fatalf("claim generation = %d, want a live fence", n.ClaimGeneration)
	}
	var job *batchv1.Job
	select {
	case job = <-created:
	default:
		t.Fatal("no Job was created")
	}
	env := map[string]string{}
	for _, e := range job.Spec.Template.Spec.Containers[0].Env {
		env[e.Name] = e.Value
	}
	if env[ClaimHolderEnv] != n.ClaimedBy {
		t.Fatalf("%s = %q, want the awarded holder", ClaimHolderEnv, env[ClaimHolderEnv])
	}
	if env[ClaimGenerationEnv] != strconv.FormatInt(n.ClaimGeneration, 10) {
		t.Fatalf("%s = %q, want generation %d", ClaimGenerationEnv, env[ClaimGenerationEnv], n.ClaimGeneration)
	}
}

func TestBuildJob_BoundsAPodThatNeverFinishes(t *testing.T) {
	r := &Runner{cfg: Config{Image: "img"}}
	job := r.buildJob("job-name", runner.Request{RunID: "run-1", NodeID: "node-1"},
		capacity.Resolution{}, store.CPUClass{}, store.NodeClaimFence{})
	if job.Spec.ActiveDeadlineSeconds == nil {
		t.Fatal("the Job carries no ActiveDeadlineSeconds, so a wedged pod outlives its run")
	}
	if want := int64(DefaultJobActiveDeadline.Seconds()); *job.Spec.ActiveDeadlineSeconds != want {
		t.Fatalf("ActiveDeadlineSeconds = %d, want %d", *job.Spec.ActiveDeadlineSeconds, want)
	}

	tuned := &Runner{cfg: Config{Image: "img", JobActiveDeadline: 90 * time.Minute}}
	job = tuned.buildJob("job-name", runner.Request{RunID: "run-1", NodeID: "node-1"},
		capacity.Resolution{}, store.CPUClass{}, store.NodeClaimFence{})
	if want := int64((90 * time.Minute).Seconds()); *job.Spec.ActiveDeadlineSeconds != want {
		t.Fatalf("configured ActiveDeadlineSeconds = %d, want %d", *job.Spec.ActiveDeadlineSeconds, want)
	}
}

func TestBuildJob_LetsTheNodeTimeoutFireBeforeKubernetesKillsThePod(t *testing.T) {
	plan := sparkwing.NewPlan()
	node := sparkwing.Job(plan, "slow", func(context.Context) error { return nil }).Timeout(time.Hour)
	r := &Runner{cfg: Config{Image: "img"}}
	job := r.buildJob("job-name", runner.Request{RunID: "run-1", NodeID: "slow", Node: node},
		capacity.Resolution{}, store.CPUClass{}, store.NodeClaimFence{})
	if want := int64((time.Hour + jobDeadlineSlack).Seconds()); *job.Spec.ActiveDeadlineSeconds != want {
		t.Fatalf("ActiveDeadlineSeconds = %d, want the node's timeout plus slack (%d)",
			*job.Spec.ActiveDeadlineSeconds, want)
	}
}

func TestBuildJob_ADeclaredTimeoutCannotOutliveTheCeiling(t *testing.T) {
	plan := sparkwing.NewPlan()
	node := sparkwing.Job(plan, "forever", func(context.Context) error { return nil }).Timeout(30 * 24 * time.Hour)
	req := runner.Request{RunID: "run-1", NodeID: "forever", Node: node}
	r := &Runner{cfg: Config{Image: "img"}}
	job := r.buildJob("job-name", req, capacity.Resolution{}, store.CPUClass{}, store.NodeClaimFence{})
	if want := int64(MaxDeclaredJobActiveDeadline.Seconds()); *job.Spec.ActiveDeadlineSeconds != want {
		t.Fatalf("ActiveDeadlineSeconds = %d, want the %s ceiling (%d)",
			*job.Spec.ActiveDeadlineSeconds, MaxDeclaredJobActiveDeadline, want)
	}
	operator := &Runner{cfg: Config{Image: "img", JobActiveDeadline: 48 * time.Hour}}
	job = operator.buildJob("job-name", req, capacity.Resolution{}, store.CPUClass{}, store.NodeClaimFence{})
	if want := int64((48 * time.Hour).Seconds()); *job.Spec.ActiveDeadlineSeconds != want {
		t.Fatalf("ActiveDeadlineSeconds = %d, want the operator's longer deadline (%d)",
			*job.Spec.ActiveDeadlineSeconds, want)
	}
}

func TestParseJobDeadline(t *testing.T) {
	for _, tc := range []struct {
		in      string
		want    time.Duration
		wantErr bool
	}{
		{in: "", want: 0},
		{in: "6h", want: 6 * time.Hour},
		{in: " 90m ", want: 90 * time.Minute},
		{in: "30s", wantErr: true},
		{in: "soon", wantErr: true},
	} {
		got, err := ParseJobDeadline(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("ParseJobDeadline(%q) = %s, want an error", tc.in, got)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("ParseJobDeadline(%q) = %s, %v; want %s", tc.in, got, err, tc.want)
		}
	}
}

// A Job Kubernetes killed at its deadline must not read like the unfenced-write
// defect: the pod is gone, so the Job condition is the only evidence.
func TestRunNode_DeadlineKillIsReportedAsItsOwnFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if err := st.CreateRun(ctx, store.Run{ID: "run-1", Pipeline: "demo", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "run-1", NodeID: "build", Status: "pending"}); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	srv := httptest.NewServer(controller.New(st, nil).Handler())
	defer srv.Close()

	deadline := int64(3600)
	kcli := fake.NewSimpleClientset()
	kcli.PrependReactor("get", "jobs", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, &batchv1.Job{
			Spec: batchv1.JobSpec{ActiveDeadlineSeconds: &deadline},
			Status: batchv1.JobStatus{Conditions: []batchv1.JobCondition{{
				Type: batchv1.JobFailed, Status: corev1.ConditionTrue,
				Reason: DeadlineExceededReason,
			}}},
		}, nil
	})
	r := New(kcli, client.New(srv.URL, nil), Config{
		Namespace: "default", Image: "runner", ControllerURL: srv.URL,
		PollInterval: time.Millisecond, MissingJobGracePeriod: time.Millisecond,
	}, nil)

	res := r.RunNode(ctx, runner.Request{RunID: "run-1", NodeID: "build"})
	if res.Outcome != sparkwing.Failed {
		t.Fatalf("outcome = %q, want failed", res.Outcome)
	}
	n, err := st.GetNode(ctx, "run-1", "build")
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if n.FailureReason != store.FailureTimeout {
		t.Fatalf("failure_reason = %q, want %q", n.FailureReason, store.FailureTimeout)
	}
	if !strings.Contains(n.Error, "killed at its active deadline (1h0m0s)") {
		t.Fatalf("node error = %q, want the deadline named", n.Error)
	}
	if strings.Contains(n.Error, "exited without writing terminal state") {
		t.Fatal("a deadline kill still reads as the missing-terminal-state failure")
	}
}

// A pod no node will take must say so, because the alternative is silence
// until the Job's wall-clock deadline hours later.
func TestObserveUnschedulable_ReportsTheSchedulersMessage(t *testing.T) {
	const jobName = "sw-run-1-build"
	pod := func(phase corev1.PodPhase, conditions ...corev1.PodCondition) *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "pod-1",
				Namespace: "sparkwing",
				Labels:    map[string]string{"batch.kubernetes.io/job-name": jobName},
			},
			Status: corev1.PodStatus{Phase: phase, Conditions: conditions},
		}
	}
	unschedulable := corev1.PodCondition{
		Type: corev1.PodScheduled, Status: corev1.ConditionFalse,
		Reason: corev1.PodReasonUnschedulable, Message: "0/3 nodes are available: insufficient cpu",
	}
	for _, tc := range []struct {
		name string
		pod  *corev1.Pod
		want string
	}{
		{name: "no pod yet", pod: nil, want: ""},
		{name: "pending but schedulable", pod: pod(corev1.PodPending), want: ""},
		{name: "already running", pod: pod(corev1.PodRunning, unschedulable), want: ""},
		{
			name: "pending and unschedulable", pod: pod(corev1.PodPending, unschedulable),
			want: "0/3 nodes are available: insufficient cpu",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var kcli *fake.Clientset
			if tc.pod == nil {
				kcli = fake.NewSimpleClientset()
			} else {
				kcli = fake.NewSimpleClientset(tc.pod)
			}
			r := New(kcli, nil, Config{Namespace: "sparkwing"}, slog.Default())
			if got := r.observeUnschedulable(context.Background(), jobName); got != tc.want {
				t.Fatalf("observeUnschedulable = %q, want %q", got, tc.want)
			}
		})
	}
}

func classJob(t *testing.T, cfg Config, cores int64) *batchv1.Job {
	t.Helper()
	class := store.CPUClass{Cores: cores, MemoryBytes: store.CPUClassMemoryBytes(cores)}
	return (&Runner{cfg: cfg}).buildJob("job-name",
		runner.Request{RunID: "run-1", NodeID: "node-1"},
		capacity.Resolution{Source: store.CostSourceDefault}, class, store.NodeClaimFence{})
}

func bandToleration(pod corev1.PodSpec) *corev1.Toleration {
	for i, tol := range pod.Tolerations {
		if tol.Key == "sparkwing.dev/cpu-band" {
			return &pod.Tolerations[i]
		}
	}
	return nil
}

func TestBuildJob_PlacesEachClassOnItsCPUBand(t *testing.T) {
	for _, tc := range []struct {
		cores int64
		band  string
	}{
		{cores: 2, band: ""},
		{cores: 4, band: "small"},
		{cores: 8, band: "small"},
	} {
		t.Run(strconv.FormatInt(tc.cores, 10), func(t *testing.T) {
			job := classJob(t, Config{Image: "img"}, tc.cores)
			pod := job.Spec.Template.Spec

			if tc.band == "" {
				if pod.NodeSelector != nil {
					t.Fatalf("nodeSelector = %v, want none for a warm-pool class", pod.NodeSelector)
				}
				if tol := bandToleration(pod); tol != nil {
					t.Fatalf("toleration = %#v, want none for a warm-pool class", tol)
				}
				if pod.Affinity.NodeAffinity != nil {
					t.Fatalf("node affinity = %#v, want none for a warm-pool class", pod.Affinity.NodeAffinity)
				}
				return
			}

			if got := pod.NodeSelector["sparkwing.dev/cpu-band"]; got != tc.band {
				t.Fatalf("nodeSelector band = %q, want %q", got, tc.band)
			}
			tol := bandToleration(pod)
			if tol == nil {
				t.Fatalf("tolerations = %#v, want one for the %s band taint", pod.Tolerations, tc.band)
			}
			if tol.Value != tc.band || tol.Effect != corev1.TaintEffectNoSchedule ||
				tol.Operator != corev1.TolerationOpEqual {
				t.Fatalf("toleration = %#v, want %s NoSchedule on Equal", tol, tc.band)
			}
			if pod.Affinity.NodeAffinity != nil {
				t.Fatalf("node affinity = %#v, want none: one pool serves every class", pod.Affinity.NodeAffinity)
			}
		})
	}
}

func TestBuildJob_MergesTheBandWithTheOperatorsOwnPlacement(t *testing.T) {
	cfg := Config{
		Image:        "img",
		NodeSelector: map[string]string{"pool": "runners"},
		Tolerations: []corev1.Toleration{{
			Key: "dedicated", Operator: corev1.TolerationOpEqual,
			Value: "sparkwing", Effect: corev1.TaintEffectNoSchedule,
		}},
	}
	pod := classJob(t, cfg, 8).Spec.Template.Spec
	want := map[string]string{"pool": "runners", "sparkwing.dev/cpu-band": "small"}
	if !reflect.DeepEqual(pod.NodeSelector, want) {
		t.Fatalf("nodeSelector = %v, want %v", pod.NodeSelector, want)
	}
	if len(pod.Tolerations) != 2 || pod.Tolerations[0].Key != "dedicated" {
		t.Fatalf("tolerations = %#v, want the operator's kept beside the band's", pod.Tolerations)
	}
	if cfg.NodeSelector["sparkwing.dev/cpu-band"] != "" || len(cfg.Tolerations) != 1 {
		t.Fatalf("buildJob wrote back into the operator's config: %#v", cfg)
	}
}

func TestBuildJob_LetsTheOperatorOverrideTheBand(t *testing.T) {
	cfg := Config{
		Image:        "img",
		NodeSelector: map[string]string{"sparkwing.dev/cpu-band": "house"},
		Tolerations: []corev1.Toleration{{
			Key: "sparkwing.dev/cpu-band", Operator: corev1.TolerationOpExists,
		}},
	}
	pod := classJob(t, cfg, 8).Spec.Template.Spec
	if got := pod.NodeSelector["sparkwing.dev/cpu-band"]; got != "house" {
		t.Fatalf("nodeSelector band = %q, want the operator's own value", got)
	}
	if len(pod.Tolerations) != 1 || pod.Tolerations[0].Operator != corev1.TolerationOpExists {
		t.Fatalf("tolerations = %#v, want only the operator's own entry", pod.Tolerations)
	}
}

func TestBuildJob_ToleratesThePoolItSelects(t *testing.T) {
	cfg := Config{
		Image:        "img",
		NodeSelector: map[string]string{"sparkwing.dev/cpu-band": "house"},
	}
	job := classJob(t, cfg, 8)
	pod := job.Spec.Template.Spec
	tol := bandToleration(pod)
	if tol == nil {
		t.Fatalf("tolerations = %#v, want one for the pool the selector names", pod.Tolerations)
	}
	if tol.Value != "house" {
		t.Fatalf("toleration value = %q, want house: a pod that selects the operator's pool "+
			"and tolerates another value never schedules", tol.Value)
	}
}
