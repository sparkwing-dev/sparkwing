package controller

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/runflags"
)

// safety: a GitHub App trigger's default branch reaches the run row, where a controller-dispatched run's planning
// claim reads it for git:branch=default guards.
func TestTriggerRows_RecordTheDefaultBranchAndRunFlags(t *testing.T) {
	_, run := triggerRows(triggerIntake{RunID: "run-1", Pipeline: "ship", Env: map[string]string{
		EnvDefaultBranch: "main", runflags.EnvDryRun: "1",
	}})
	if got := run.Invocation[runflags.DefaultBranchKey]; got != "main" {
		t.Errorf("default branch = %v, want main", got)
	}
	if !runflags.FromInvocation(run.Invocation).DryRun {
		t.Errorf("invocation = %v, want the dry run recorded", run.Invocation)
	}
}
