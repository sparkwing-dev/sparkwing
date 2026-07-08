package orchestrator_test

import (
	"context"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/internal/profile"
	"github.com/sparkwing-dev/sparkwing/pkg/backends"
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

func TestRun_ProfileSecretsBackend_EnvType(t *testing.T) {
	t.Setenv("SWTEST_TOKEN", "from-env")

	capturedEnvSecret = ""
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{
		Pipeline: "env-reading-pipe",
		Profile: &profile.Profile{
			Name:    "test",
			Secrets: &backends.Spec{Type: backends.TypeEnv, Prefix: "SWTEST_"},
		},
	})
	if err != nil {
		t.Fatalf("RunLocal: %v", err)
	}
	if res.Status != "success" {
		t.Fatalf("status = %q (err=%v); want success", res.Status, res.Error)
	}
	if capturedEnvSecret != "from-env" {
		t.Errorf("step body saw Token = %q, want from-env", capturedEnvSecret)
	}
}

func TestRun_NoSecretsBackend_FallsBackToOptionsSecretSource(t *testing.T) {
	capturedEnvSecret = ""
	p := newPaths(t)
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
	t.Setenv("SWTEST_TOKEN", "x")

	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{
		Pipeline: "env-reading-pipe",
		PipelineYAML: &pipelines.Pipeline{
			Name:       "env-reading-pipe",
			Entrypoint: "EnvReading",
		},
		Profile: &profile.Profile{
			Name:    "test",
			Secrets: &backends.Spec{Type: backends.TypeEnv, Prefix: "SWTEST_"},
		},
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
