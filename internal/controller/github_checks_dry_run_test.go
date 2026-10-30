package controller

import "testing"

func TestGitHubChecksDistinguishDryRunFromSuccess(t *testing.T) {
	if phase := githubCheckPhaseFor("dry_run"); phase != githubCheckCompleted {
		t.Fatalf("dry-run check phase = %v", phase)
	}
	conclusion := githubCheckConclusion("dry_run")
	if conclusion != "neutral" || githubCheckTitle(conclusion) != "Dry run completed" {
		t.Fatalf("dry-run check = %q, %q", conclusion, githubCheckTitle(conclusion))
	}

}
