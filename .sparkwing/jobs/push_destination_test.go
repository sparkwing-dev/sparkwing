package jobs

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestMainPushRefusesRewindToFailingAncestor(t *testing.T) {
	root := gateFixtureRepo(t)
	probe := filepath.Join(root, "internal", "probe_test.go")
	writeGoFile(t, probe, "package internal\nimport \"testing\"\nfunc TestProbe(t *testing.T) { t.Fatal(\"unchecked-topic-test\") }\n")
	gitCommitAll(t, root, "failing ancestor")
	ctx := newTestContext(t)
	broken, err := gitIn(root)(ctx, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	writeGoFile(t, probe, "package internal\nimport \"testing\"\nfunc TestProbe(t *testing.T) {}\n")
	gitCommitAll(t, root, "repair the test")
	good, err := gitIn(root)(ctx, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	runTestGit(t, root, "update-ref", "refs/remotes/origin/main", good)
	runTestGit(t, root, "switch", "--detach", broken)
	t.Setenv("SPARKWING_GATE_PUSH_REFS", "HEAD "+broken+" refs/heads/main "+good)
	err = runAffectedPushTests(ctx)
	if err == nil || !strings.Contains(err.Error(), "non-fast-forward") || !strings.Contains(err.Error(), good) || !strings.Contains(err.Error(), broken) {
		t.Fatalf("rewind to failing ancestor: %v; want non-fast-forward refusal naming both commits", err)
	}
}

func TestNewMainRunsAllPackagesAndCriticalTests(t *testing.T) {
	for _, localMain := range []bool{true, false} {
		name := "without local main"
		if localMain {
			name = "local main equals HEAD"
		}
		t.Run(name, func(t *testing.T) {
			root := gateFixtureRepo(t)
			writeGoFile(t, filepath.Join(root, "ordinary", "probe_test.go"), "package ordinary\nimport \"testing\"\nfunc TestProbe(t *testing.T) { t.Fatal(\"new main ordinary test\") }\n")
			writeGoFile(t, filepath.Join(root, "critical", "guard_test.go"), "package critical\nimport \"testing\"\n//sparkwing:critical\nfunc TestGuard(t *testing.T) { if testing.Short() { t.Skip(\"short\") }; t.Fatal(\"new main critical test\") }\n")
			gitCommitAll(t, root, "new destination source")
			ctx := newTestContext(t)
			head, err := gitIn(root)(ctx, "rev-parse", "HEAD")
			if err != nil {
				t.Fatal(err)
			}
			if localMain {
				runTestGit(t, root, "update-ref", "refs/remotes/origin/main", head)
			}
			t.Setenv("SPARKWING_GATE_PUSH_REFS", "refs/heads/topic "+head+" refs/heads/main "+strings.Repeat("0", 40))
			err = runAffectedPushTests(ctx)
			if err == nil || !strings.Contains(err.Error(), "new main ordinary test") || !strings.Contains(err.Error(), "new main critical test") {
				t.Fatalf("new main skipped ordinary or full critical tests: %v", err)
			}
		})
	}
}

func TestMainPushUsesAdvertisedBaseDespiteAdvancedLocalTracking(t *testing.T) {
	root := gateFixtureRepo(t)
	gitCommitAll(t, root, "remote base")
	ctx := newTestContext(t)
	base, err := gitIn(root)(ctx, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	writeGoFile(t, filepath.Join(root, "internal", "probe_test.go"), "package internal\nimport \"testing\"\nfunc TestProbe(t *testing.T) { t.Fatal(\"advertised main test\") }\n")
	gitCommitAll(t, root, "unchecked change")
	head, err := gitIn(root)(ctx, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	runTestGit(t, root, "update-ref", "refs/remotes/origin/main", head)
	t.Setenv("SPARKWING_GATE_PUSH_REFS", "refs/heads/topic "+head+" refs/heads/main "+base)
	if err := runAffectedPushTests(ctx); err == nil || !strings.Contains(err.Error(), "advertised main test") {
		t.Fatalf("advanced local tracking hid the advertised push range: %v", err)
	}
}
