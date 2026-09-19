package jobs

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestSleepStepGatesTheRangeWhenNothingIsStaged(t *testing.T) {
	root := commentFixtureRepo(t)
	writeGoFile(t, filepath.Join(root, "internal", "committed_test.go"),
		"package internal\n\nfunc committed() int { return 2 }\n")
	gitCommitAll(t, root, "work the branch already carries")

	command, scope, err := sleepCheckCommand(context.Background())
	if err != nil {
		t.Fatalf("sleepCheckCommand: %v", err)
	}
	if !strings.Contains(command, "./internal/sleepcheck -base ") {
		t.Errorf("command = %q, want the range mode against the baseline", command)
	}
	if !strings.Contains(scope, gateBaselineRef) || !strings.Contains(scope, "untracked test file") {
		t.Errorf("scope = %q, does not name the baseline and the untracked files the step gated", scope)
	}
}

func TestSleepStepCountsOnlyTheStagedTestFiles(t *testing.T) {
	root := commentFixtureRepo(t)
	writeGoFile(t, filepath.Join(root, "internal", "pending_test.go"),
		"package internal\n\nfunc pending() int { return 3 }\n")
	writeGoFile(t, filepath.Join(root, "internal", "product.go"),
		"package internal\n\nfunc product() int { return 4 }\n")
	gitAddAll(t, root)

	command, scope, err := sleepCheckCommand(context.Background())
	if err != nil {
		t.Fatalf("sleepCheckCommand: %v", err)
	}
	if !strings.Contains(command, "./internal/sleepcheck -staged .") {
		t.Errorf("command = %q, want the staged mode a commit hook needs", command)
	}
	if !strings.Contains(scope, "1 staged test file(s)") {
		t.Errorf("scope = %q, counts files this rule never judges", scope)
	}
}
