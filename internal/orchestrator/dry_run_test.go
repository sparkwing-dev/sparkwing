package orchestrator

import "testing"

func TestDryRunFromEnvironmentProjection(t *testing.T) {
	for _, test := range []struct {
		value string
		want  bool
	}{{"true", true}, {"false", false}, {"1", true}, {"", false}} {
		t.Run(test.value, func(t *testing.T) {
			t.Setenv(envDryRun, test.value)
			if got := dryRunFromEnv(); got != test.want {
				t.Fatalf("inherited dry run = %t, want %t", got, test.want)
			}
		})
	}
}
