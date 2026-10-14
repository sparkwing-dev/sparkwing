package sdkserve

import (
	"slices"
	"testing"
)

func TestHostRunnerReadsTheHostsVariables(t *testing.T) {
	t.Setenv("SPARKWING_RUNNER_NAME", "")
	t.Setenv("SPARKWING_RUNNER_TYPE", "")
	t.Setenv("SPARKWING_RUNNER_LABELS", "")
	if r := hostRunner(); r != nil {
		t.Fatalf("runner = %+v with no host variables, want nil", r)
	}
	t.Setenv("SPARKWING_RUNNER_NAME", "local")
	t.Setenv("SPARKWING_RUNNER_TYPE", "local")
	t.Setenv("SPARKWING_RUNNER_LABELS", "local,os=linux")
	r := hostRunner()
	if r == nil || r.Name != "local" || r.Type != "local" || !slices.Equal(r.Labels, []string{"local", "os=linux"}) {
		t.Fatalf("runner = %+v", r)
	}
}
