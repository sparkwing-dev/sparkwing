package charts

import (
	"slices"
	"testing"
)

func TestRunnerSubmitAllowListReachesTheRunner(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: renders the runner chart twice; the fast class runs under -short")
	}
	defaults := runnerContainer(t, renderRunner(t)).Args
	if flag, ok := hasFlag(defaults, "--submit-env-allow"); ok {
		t.Fatalf("default runner accepts submitted environment: %s", flag)
	}
	args := runnerContainer(t, renderRunner(t, "runner.submitEnvAllow={BUILD_*,DEPLOY_TARGET}")).Args
	for _, want := range []string{"--submit-env-allow=BUILD_*", "--submit-env-allow=DEPLOY_TARGET"} {
		if !slices.Contains(args, want) {
			t.Errorf("runner args %v lack %s", args, want)
		}
	}
}
