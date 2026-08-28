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
	"github.com/sparkwing-dev/sparkwing/pkg/backends"
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

func TestClusterPodRoundTrip_RemoteControllerSource(t *testing.T) {
	reg := ensurePodRTPipe(t)

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

	src := backends.Spec{Type: backends.TypeController, URL: srv.URL, Token: wantToken}
	resolver, err := sparkwing.NewSecretResolverFromSpec(context.Background(), src)
	if err != nil {
		t.Fatalf("build resolver: %v", err)
	}
	ctx := sparkwing.WithSecretResolver(context.Background(), resolver)

	gotSec, err := rehydratePipelineSecrets(ctx, nil, reg)
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

	if hits["DEPLOY_TOKEN"] == 0 {
		t.Errorf("controller never queried for DEPLOY_TOKEN")
	}
}

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

func TestClusterPodRoundTrip_AuthFailureSurfacesAsError(t *testing.T) {
	reg := ensurePodRTPipe(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	src := backends.Spec{Type: backends.TypeController, URL: srv.URL, Token: "bad-token"}
	resolver, err := sparkwing.NewSecretResolverFromSpec(context.Background(), src)
	if err != nil {
		t.Fatalf("resolver: %v", err)
	}
	ctx := sparkwing.WithSecretResolver(context.Background(), resolver)
	_, err = rehydratePipelineSecrets(ctx, nil, reg)
	if err == nil {
		t.Fatal("expected auth-error to propagate")
	}
	if !strings.Contains(err.Error(), "401") && !strings.Contains(err.Error(), "Unauthorized") {
		t.Errorf("expected 401/Unauthorized error, got %v", err)
	}
}
