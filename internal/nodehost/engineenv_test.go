package nodehost

import (
	"slices"
	"testing"
)

func TestPlanSessionSeesTheDryRunSwitch(t *testing.T) {
	cfg := Config{Session: SessionConfig{Env: []string{"PATH=/bin"}}}
	cfg.Run.DryRun = true
	if got := planSession(cfg).Env; !slices.Equal(got, []string{"PATH=/bin", "SPARKWING_DRY_RUN=1"}) {
		t.Errorf("plan env = %v, want SPARKWING_DRY_RUN=1 as its nodes get", got)
	}
	if cfg.Session.Env[len(cfg.Session.Env)-1] != "PATH=/bin" {
		t.Error("planSession changed the attempts' base environment")
	}
}
