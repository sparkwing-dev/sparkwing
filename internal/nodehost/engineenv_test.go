package nodehost

import (
	"runtime"
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

func TestRunnerEnvNamesTheHostsRunner(t *testing.T) {
	for _, name := range []string{"SPARKWING_RUNNER_NAME", "SPARKWING_RUNNER_TYPE", "SPARKWING_RUNNER_LABELS"} {
		t.Setenv(name, "")
	}
	labels := "SPARKWING_RUNNER_LABELS=local,os=" + runtime.GOOS + ",arch=" + runtime.GOARCH
	local := []string{"SPARKWING_RUNNER_NAME=local", "SPARKWING_RUNNER_TYPE=local", labels}
	if got := runnerEnv(); !slices.Equal(got, local) {
		t.Errorf("local host env = %v, want %v", got, local)
	}
	t.Setenv("SPARKWING_RUNNER_NAME", "pool-a")
	t.Setenv("SPARKWING_RUNNER_TYPE", "kubernetes")
	if got, want := runnerEnv(), []string{"SPARKWING_RUNNER_NAME=pool-a", "SPARKWING_RUNNER_TYPE=kubernetes"}; !slices.Equal(got, want) {
		t.Errorf("pod host env = %v, want the Job's %v", got, want)
	}
}
