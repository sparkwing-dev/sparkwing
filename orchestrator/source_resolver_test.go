package orchestrator_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/orchestrator"
	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// envReadingSec demands TOKEN; populated via the resolver chosen by
// the orchestrator from sources.yaml.
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

func writeSparkwingDir(t *testing.T) (string, string) {
	t.Helper()
	// Isolate XDG so user file lookups land in tempdir.
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	if err := os.MkdirAll(filepath.Join(xdg, "sparkwing"), 0o755); err != nil {
		t.Fatalf("mkdir xdg: %v", err)
	}
	// Repo-side .sparkwing dir.
	repoDir := filepath.Join(t.TempDir(), ".sparkwing")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	return repoDir, xdg
}

func TestRun_PerTargetSourceWiring_EnvBackend(t *testing.T) {
	repoDir, _ := writeSparkwingDir(t)
	if err := os.WriteFile(filepath.Join(repoDir, "sources.yaml"), []byte(`
sources:
  shell-env:
    type: env
    prefix: SWTEST_
`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SWTEST_TOKEN", "from-env")

	capturedEnvSecret = ""
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{
		Pipeline:     "env-reading-pipe",
		SparkwingDir: repoDir,
		PipelineYAML: &pipelines.Pipeline{
			Name:       "env-reading-pipe",
			Entrypoint: "EnvReading",
			Targets: map[string]pipelines.Target{
				"dev": {Source: "shell-env"},
			},
		},
		Target: "dev",
	})
	if err != nil {
		t.Fatalf("RunLocal: %v", err)
	}
	if res.Status != "success" {
		t.Fatalf("status = %q (err=%v); want success", res.Status, res.Error)
	}
	if capturedEnvSecret != "from-env" {
		t.Errorf("step body saw Token = %q, want from-env (env-backed source)", capturedEnvSecret)
	}
}

func TestRun_NoSparkwingDir_FallsBackToOptionsSecretSource(t *testing.T) {
	// No SparkwingDir set; the orchestrator should fall back to the
	// existing Options.SecretSource path. We reuse the staticSource
	// fixture from pipeline_config_secrets_test.go.
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
		t.Errorf("step body saw Token = %q, want from-options-fallback (Options.SecretSource path)", capturedEnvSecret)
	}
}

func TestRun_SparkwingDirNoBinding_FallsBackToOptionsSecretSource(t *testing.T) {
	// SparkwingDir is set but sources.yaml has no entry for the
	// chosen target; orchestrator should fall back rather than
	// erroring.
	repoDir, _ := writeSparkwingDir(t)
	// Empty sources.yaml.
	if err := os.WriteFile(filepath.Join(repoDir, "sources.yaml"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	capturedEnvSecret = ""
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{
		Pipeline:     "env-reading-pipe",
		SparkwingDir: repoDir,
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

func TestRun_PlanSnapshotCarriesTargetAndConfig(t *testing.T) {
	repoDir, _ := writeSparkwingDir(t)
	if err := os.WriteFile(filepath.Join(repoDir, "sources.yaml"), []byte(`
sources:
  shell-env:
    type: env
    prefix: SWTEST_
`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SWTEST_TOKEN", "x")

	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{
		Pipeline:     "env-reading-pipe",
		SparkwingDir: repoDir,
		PipelineYAML: &pipelines.Pipeline{
			Name:       "env-reading-pipe",
			Entrypoint: "EnvReading",
			Secrets:    pipelines.SecretsField{{Name: "TOKEN", Required: true}},
			Targets: map[string]pipelines.Target{
				"dev": {Source: "shell-env"},
			},
		},
		Target: "dev",
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
	defer st.Close()
	run, err := st.GetRun(context.Background(), res.RunID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	snap := string(run.PlanSnapshot)
	if !contains(snap, `"target":"dev"`) {
		t.Errorf("snapshot missing target field:\n%s", snap)
	}
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
