package nodehost

import (
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"slices"
	"testing"
)

func TestPlanSessionSeesTheDryRunSwitch(t *testing.T) {
	cfg := Config{Session: SessionConfig{Env: []string{"PATH=/bin"}, Validation: nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: noV2Secrets{}}, Engine: nodeproto.EngineIdentity{Version: "test"}, Runner: nodeproto.RunnerIdentity{Name: "local", Type: "local", Labels: []string{}}}}
	cfg.Run.DryRun = true
	if got := planSession(cfg).Env; !slices.Equal(got, []string{"PATH=/bin", "SPARKWING_DRY_RUN=1"}) {
		t.Errorf("plan env = %v, want SPARKWING_DRY_RUN=1 as its nodes get", got)
	}
	if cfg.Session.Env[len(cfg.Session.Env)-1] != "PATH=/bin" {
		t.Error("planSession changed the attempts' base environment")
	}
}
