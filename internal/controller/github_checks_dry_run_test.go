package controller

import (
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func TestGitHubChecksDistinguishDryRunFromSuccess(t *testing.T) {
	if phase := githubCheckPhaseFor("dry_run"); phase != githubCheckCompleted {
		t.Fatalf("dry-run check phase = %v", phase)
	}
	conclusion := githubCheckConclusion("dry_run")
	if conclusion != "neutral" || githubCheckTitle(conclusion) != "Dry run completed" {
		t.Fatalf("dry-run check = %q, %q", conclusion, githubCheckTitle(conclusion))
	}
}

func TestGitHubCheckSummaryCountsMemoizedJobsByTheirOutcome(t *testing.T) {
	nodes := []*store.Node{{Outcome: "success"}, {Outcome: "memoized"}}
	summary := githubCheckSummary(nil, nodes, "success", "")
	if !strings.Contains(summary, "1 memoized, 1 success") || strings.Contains(summary, "other") {
		t.Fatalf("summary = %q, want the memoized job counted as memoized", summary)
	}
}
