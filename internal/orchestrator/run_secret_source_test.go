package orchestrator_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type envReadingSec struct {
	Token string `sw:"TOKEN,required"`
}

type envReadingPipe struct{ sparkwing.Base }

var capturedEnvSecret string

func (envReadingPipe) Secrets() any { return &envReadingSec{} }

func (envReadingPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "read", func(ctx context.Context) error {
		sec := sparkwing.PipelineSecrets[envReadingSec](ctx)
		if sec != nil {
			capturedEnvSecret = sec.Token
		}
		return nil
	})
	return nil
}

func init() {
	register("env-reading-pipe", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &envReadingPipe{} })
}

func TestRun_NoSecretsBackend_FallsBackToOptionsSecretSource(t *testing.T) {
	capturedEnvSecret = ""
	p := newPathsWithStore(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{
		Pipeline:     "env-reading-pipe",
		SecretSource: staticSource{"TOKEN": "from-options-fallback"},
	})
	if err != nil {
		t.Fatalf("RunLocal: %v", err)
	}
	if res.Status != "success" {
		t.Fatalf("status = %q (err=%v); want success", res.Status, res.Error)
	}
	if capturedEnvSecret != "from-options-fallback" {
		t.Errorf("step body saw Token = %q, want from-options-fallback", capturedEnvSecret)
	}
}

func TestRun_PlanSnapshotCarriesPipelineYAML(t *testing.T) {
	p := newPathsWithStore(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{
		Pipeline: "env-reading-pipe",
		PipelineYAML: &pipelines.Pipeline{
			Name:       "env-reading-pipe",
			Entrypoint: "EnvReading",
		},
		SecretSource: staticSource{"TOKEN": "x"},
	})
	if err != nil {
		t.Fatalf("RunLocal: %v", err)
	}
	if res.Status != "success" {
		t.Fatalf("status = %q (err=%v); want success", res.Status, res.Error)
	}
	st, err := store.Open(p.StateDB())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer func() { _ = st.Close() }()
	run, err := st.GetRun(context.Background(), res.RunID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	snap := string(run.PlanSnapshot)
	if !contains(snap, `"name":"TOKEN"`) {
		t.Errorf("snapshot missing persisted SecretsField:\n%s", snap)
	}
}

func contains(s, sub string) bool { return len(s) >= len(sub) && (indexOf(s, sub) >= 0) }

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// A local run reads its secrets and state from this machine even when the
// shell exports a profile, a secrets profile and controller URLs that name a
// live controller.
func TestRun_LocalRunContactsNoControllerWhateverTheEnvironmentSays(t *testing.T) {
	var requests atomic.Int32
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer controller.Close()
	config := filepath.Join(t.TempDir(), "config.yaml")
	body := fmt.Sprintf("profiles:\n  remote:\n    controller: { url: %s, token: swu_test }\n", controller.URL)
	if err := os.WriteFile(config, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SPARKWING_CONFIG", config)
	t.Setenv("SPARKWING_PROFILE", "remote")
	t.Setenv("SPARKWING_SECRETS_PROFILE", "remote")
	t.Setenv("SPARKWING_CONTROLLER_URL", controller.URL)
	t.Setenv("SPARKWING_LOGS_URL", controller.URL)

	p := newPathsWithStore(t)
	seedLocalSecret(t, p, "TOKEN", "from-local-store")
	capturedEnvSecret = ""
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "env-reading-pipe"})
	if err != nil {
		t.Fatalf("RunLocal: %v", err)
	}
	if res.Status != "success" || capturedEnvSecret != "from-local-store" {
		t.Fatalf("status %q (err=%v), secret %q; want success with the local secret", res.Status, res.Error, capturedEnvSecret)
	}
	if n := requests.Load(); n != 0 {
		t.Fatalf("a local run sent %d requests to the controller", n)
	}
}
