package charts

import (
	"slices"
	"strings"
	"testing"
)

func TestRunnerPoolTrustAndSubmitAllowListReachTheRunner(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: renders the runner chart three times; the fast class runs under -short")
	}
	defaults := runnerContainer(t, renderRunner(t)).Args
	if flag, ok := hasFlag(defaults, "--trust"); ok {
		t.Fatalf("default runner overrides the controller's trust default: %s", flag)
	}
	if flag, ok := hasFlag(defaults, "--submit-env-allow"); ok {
		t.Fatalf("default runner accepts submitted environment: %s", flag)
	}
	args := runnerContainer(t, renderRunner(t, "runner.trust=any", "runner.submitEnvAllow={BUILD_*,DEPLOY_TARGET}")).Args
	for _, want := range []string{"--trust=any", "--submit-env-allow=BUILD_*", "--submit-env-allow=DEPLOY_TARGET"} {
		if !slices.Contains(args, want) {
			t.Errorf("runner args %v lack %s", args, want)
		}
	}
	if out := helmRenderError(t, "./sparkwing-runner-bundle", "sparkwing", "runner.trust=main"); !strings.Contains(out, "runner.trust") {
		t.Fatalf("invalid trust class rendered: %s", out)
	}
}
