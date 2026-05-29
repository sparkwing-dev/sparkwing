package orchestrator

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/sparkwingruntime"
	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
	"github.com/sparkwing-dev/sparkwing/pkg/sources"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type podRTCfg struct {
	ImageRepo string `sw:"image_repo"`
	Replicas  int    `sw:"replicas" default:"1"`
}
type podRTSec struct {
	DeployToken string `sw:"DEPLOY_TOKEN,required"`
	SlackHook   string `sw:"SLACK_HOOK,optional"`
}
type podRTPipe struct{ sparkwing.Base }

func (podRTPipe) Config() any  { return &podRTCfg{} }
func (podRTPipe) Secrets() any { return &podRTSec{} }
func (podRTPipe) Plan(_ context.Context, _ *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	return nil
}

func ensurePodRTPipe(t *testing.T) *sparkwing.Registration {
	t.Helper()
	if reg, ok := sparkwing.Lookup("pod-rt-pipe"); ok {
		return reg
	}
	sparkwing.Register[sparkwing.NoInputs]("pod-rt-pipe",
		func() sparkwing.Pipeline[sparkwing.NoInputs] { return &podRTPipe{} })
	reg, _ := sparkwing.Lookup("pod-rt-pipe")
	return reg
}

// TestClusterPodRoundTrip stitches the orchestrator-side snapshot
// emission with the pod-side rehydrate path through a fake
// controller-backed source. Mirrors what happens when a cluster
// worker claims a remote-controller-bound run: the snapshot ships
// declarations only, never values, and the pod re-resolves secrets
// against the controller it talks to in its own boot.
func TestClusterPodRoundTrip_RemoteControllerSource(t *testing.T) {
	reg := ensurePodRTPipe(t)

	// 1. Fake controller serving /api/v1/secrets/<name>. Auth header
	// is checked so a missing/wrong token surfaces as 401, matching
	// the production controller's contract.
	const wantToken = "pod-rt-token"
	hits := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+wantToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/api/v1/secrets/")
		name, _ = url.PathUnescape(name)
		hits[name]++
		switch name {
		case "DEPLOY_TOKEN":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"value":  "rotated-pod-side",
				"masked": true,
			})
		case "SLACK_HOOK":
			w.WriteHeader(http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	// 2. Build the snapshot the orchestrator would persist. The pod
	// receives only secret declarations -- never values.
	snap, err := json.Marshal(planSnapshot{
		Pipeline: "pod-rt-pipe",
		RunID:    "run-pod-rt",
		Secrets: pipelines.SecretsField{
			{Name: "DEPLOY_TOKEN", Required: true},
			{Name: "SLACK_HOOK", Optional: true},
		},
	})
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}

	// 3. Pod-side: build the resolver the cluster worker would
	// install for a remote-controller source binding. The SDK
	// factory handles the http wiring; we just supply the URL+token
	// via the profile-lookup callback.
	src := sources.Source{
		Name: "pod-controller", Type: sources.TypeProfile, Profile: "pod-profile",
	}
	resolver, err := sparkwing.NewSecretResolverFromSource(context.Background(), src,
		func(_ string) (string, string, error) { return srv.URL, wantToken, nil })
	if err != nil {
		t.Fatalf("build resolver: %v", err)
	}
	ctx := sparkwing.WithSecretResolver(context.Background(), resolver)

	// 4. Re-resolve secrets against the controller-backed resolver.
	// DEPLOY_TOKEN must come back with the value the fake server
	// served; SLACK_HOOK is optional so a 404 doesn't fail the run.
	gotSec, err := rehydratePipelineSecrets(ctx, snap, reg)
	if err != nil {
		t.Fatalf("rehydrate secrets: %v", err)
	}
	s := gotSec.(*podRTSec)
	if s.DeployToken != "rotated-pod-side" {
		t.Errorf("DEPLOY_TOKEN = %q, want rotated-pod-side", s.DeployToken)
	}
	if s.SlackHook != "" {
		t.Errorf("SLACK_HOOK should be empty (optional, 404'd), got %q", s.SlackHook)
	}

	// 6. Confirm the controller was actually hit. The snapshot
	// shipped names only -- values came over the wire at run time.
	if hits["DEPLOY_TOKEN"] == 0 {
		t.Errorf("controller never queried for DEPLOY_TOKEN")
	}
}

// TestClusterPodRoundTrip_RunnerInfoVisibleOnPod asserts that the
// pod-side install picks up the runner identity the cluster trigger
// loop stamps via SPARKWING_RUNNER_* env vars, so adapters branching
// on sparkwing.Runner(ctx).HasLabel(...) take the non-local path on
// the pod.
func TestClusterPodRoundTrip_RunnerInfoVisibleOnPod(t *testing.T) {
	t.Setenv("SPARKWING_RUNNER_NAME", "warm-pool-a")
	t.Setenv("SPARKWING_RUNNER_TYPE", "kubernetes")
	t.Setenv("SPARKWING_RUNNER_LABELS", "kubernetes,os=linux,cloud-linux")

	info := podRunnerInfo()
	if info == nil {
		t.Fatal("podRunnerInfo nil")
	}
	ctx := sparkwingruntime.WithRunner(context.Background(), info)

	r := sparkwing.Runner(ctx)
	if r == nil {
		t.Fatal("Runner(ctx) nil after install")
	}
	if r.HasLabel("local") {
		t.Errorf("pod adapter would take local path; labels = %v", r.Labels)
	}
	if !r.HasLabel("kubernetes") {
		t.Errorf("pod adapter would miss kubernetes path; labels = %v", r.Labels)
	}
	if r.Name != "warm-pool-a" || r.Type != "kubernetes" {
		t.Errorf("identity wrong: %+v", r)
	}
}

// TestClusterPodRoundTrip_AuthFailureSurfacesAsError pins the
// controller's 401 contract end-to-end: the pod-side rehydrate
// surfaces the auth failure rather than silently substituting an
// empty value into the required field.
func TestClusterPodRoundTrip_AuthFailureSurfacesAsError(t *testing.T) {
	reg := ensurePodRTPipe(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	src := sources.Source{
		Name: "pod-controller", Type: sources.TypeProfile, Profile: "pod-profile",
	}
	resolver, err := sparkwing.NewSecretResolverFromSource(context.Background(), src,
		func(_ string) (string, string, error) { return srv.URL, "bad-token", nil })
	if err != nil {
		t.Fatalf("resolver: %v", err)
	}
	ctx := sparkwing.WithSecretResolver(context.Background(), resolver)
	snap, _ := json.Marshal(planSnapshot{
		Secrets: pipelines.SecretsField{{Name: "DEPLOY_TOKEN", Required: true}},
	})
	_, err = rehydratePipelineSecrets(ctx, snap, reg)
	if err == nil {
		t.Fatal("expected auth-error to propagate")
	}
	if !strings.Contains(err.Error(), "401") && !strings.Contains(err.Error(), "Unauthorized") {
		t.Errorf("expected 401/Unauthorized error, got %v", err)
	}
}
