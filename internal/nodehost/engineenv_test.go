package nodehost

import (
	"slices"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

func TestPlanSessionSeesTheDryRunSwitch(t *testing.T) {
	cfg := Config{Session: SessionConfig{Env: []string{"PATH=/bin"}, Validation: nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: noV2Secrets{}}, Engine: nodeproto.EngineIdentity{Version: "test"}, Runner: nodeproto.RunnerIdentity{Name: "local", Type: "local", Labels: []string{}}}}
	cfg.Run.DryRun = true
	planned, err := planSession(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(planned.Env, "SPARKWING_DRY_RUN=true") || !slices.Contains(planned.Env, "SPARKWING_RUNNER_NAME=local") {
		t.Errorf("plan env = %v, want the sealed dry-run switch and granted identity", planned.Env)
	}
	if cfg.Session.Env[len(cfg.Session.Env)-1] != "PATH=/bin" {
		t.Error("planSession changed the attempts' base environment")
	}
}
