package nodehost

import (
	"slices"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
)

func TestPlanningEnvironmentSeesTheDryRunSwitch(t *testing.T) {
	cfg := Config{Session: SessionConfig{Env: []string{"PATH=/bin"}, Validation: nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: noV2Secrets{}}, Engine: nodeproto.EngineIdentity{Version: "test"}, Runner: nodeproto.RunnerIdentity{Name: "local", Type: "local", Labels: []string{}}}}
	cfg.Run.DryRun = true
	planned, err := buildSessionEnvironment(cfg.Session, procenv.Plan, sdkRuntime(cfg.Run, cfg.Session.Runner))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(planned, "SPARKWING_DRY_RUN=true") || !slices.Contains(planned, "SPARKWING_RUNNER_NAME=local") {
		t.Errorf("plan env = %v, want the sealed dry-run switch and granted identity", planned)
	}
	if cfg.Session.Env[len(cfg.Session.Env)-1] != "PATH=/bin" {
		t.Error("planning changed the attempts' base environment")
	}
}
