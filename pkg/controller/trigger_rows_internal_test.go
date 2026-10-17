package controller

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/runflags"
)

func TestTriggerInvocationRecordsDefaultBranchAndRunFlags(t *testing.T) {
	invocation := triggerRunInvocation(map[string]string{
		EnvDefaultBranch: "main", runflags.EnvDryRun: "1",
	})
	if got := invocation[runflags.DefaultBranchKey]; got != "main" {
		t.Errorf("default branch = %v, want main", got)
	}
	if !runflags.FromInvocation(invocation).DryRun {
		t.Errorf("invocation = %v, want the dry run recorded", invocation)
	}
}
