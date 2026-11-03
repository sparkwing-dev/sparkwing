package jobs

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func treeGateRepo(t *testing.T) string {
	t.Helper()
	root := gateFixtureRepo(t)
	gitCommitAll(t, root, "base")
	t.Setenv("SPARKWING_ENGINE_VERSION", "v0.0.1")
	t.Setenv("SPARKWING_COMMIT", "")
	t.Setenv("SPARKWING_RUNNER_NAME", "workstation")
	t.Setenv("SPARKWING_RUN_ID", "run-1")
	t.Setenv("GIT_AUTHOR_NAME", "gate")
	t.Setenv("GIT_AUTHOR_EMAIL", "gate@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "gate")
	t.Setenv("GIT_COMMITTER_EMAIL", "gate@example.com")
	return root
}

func TestATreeThatPassedSkipsOnEveryBranchUntilItChanges(t *testing.T) {
	root := treeGateRepo(t)
	ctx := context.Background()
	if pass, reason := provenPass(ctx, gateAffected); pass != nil || !strings.Contains(reason, "no recorded") {
		t.Fatalf("fresh tree proven = %+v (%s), want a miss", pass, reason)
	}
	if err := recordTreePass(gateAffected)(ctx); err != nil {
		t.Fatal(err)
	}
	pass, reason := provenPass(ctx, gateAffected)
	if pass == nil {
		t.Fatalf("recorded tree not proven: %s", reason)
	}
	if pass.Runner != "workstation" || pass.RunID != "run-1" || pass.Commit == "" {
		t.Fatalf("record = %+v, want the runner, run and commit that proved it", pass)
	}
	if !skipWhenTreePassed(gateAffected)(ctx) {
		t.Fatal("a proven tree did not skip its gate job")
	}

	runTestGit(t, root, "checkout", "-q", "-b", "elsewhere")
	runTestGit(t, root, "-c", "commit.gpgsign=false", "commit", "-q", "--allow-empty", "-m", "same tree, new commit")
	if pass, reason := provenPass(ctx, gateAffected); pass == nil {
		t.Fatalf("a commit with the same tree on another branch lost the pass: %s", reason)
	}

	writeGoFile(t, filepath.Join(root, "internal", "sound.go"), "package internal\n\nfunc Sound() int { return 2 }\n")
	if pass, reason := provenPass(ctx, gateAffected); pass != nil || !strings.Contains(reason, "uncommitted") {
		t.Fatalf("dirty checkout proven = %+v (%s), want no key", pass, reason)
	}
	gitCommitAll(t, root, "new tree")
	if pass, _ := provenPass(ctx, gateAffected); pass != nil {
		t.Fatal("a changed tree inherited the old tree's pass")
	}
}

func TestAPassBindsTheEngineTheGateDefinitionAndTheMode(t *testing.T) {
	root := treeGateRepo(t)
	ctx := context.Background()
	if err := recordTreePass(gateAffected)(ctx); err != nil {
		t.Fatal(err)
	}
	if pass, _ := provenPass(ctx, gateFull); pass != nil {
		t.Fatal("an affected-test pass proved a full gate")
	}
	t.Setenv("SPARKWING_ENGINE_VERSION", "v0.0.2")
	if pass, reason := provenPass(ctx, gateAffected); pass != nil || !strings.Contains(reason, "none for gate") {
		t.Fatalf("another engine proven = %+v (%s), want a miss", pass, reason)
	}
	if err := recordTreePass(gateFull)(ctx); err != nil {
		t.Fatal(err)
	}
	if pass, _ := provenPass(ctx, gateAffected); pass == nil || pass.Mode != gateFull {
		t.Fatalf("a full pass did not prove the affected gate: %+v", pass)
	}

	before, err := currentGateKey(ctx)
	if err != nil {
		t.Fatal(err)
	}
	writeGoFile(t, filepath.Join(root, ".sparkwing", "jobs.go"), "package pipelines\n\nfunc Jobs() int { return 2 }\n")
	gitCommitAll(t, root, "change the gate")
	after, err := currentGateKey(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if before.Fingerprint == after.Fingerprint {
		t.Fatal("changing the pipeline module kept the gate fingerprint")
	}
}

func TestNothingIsRecordedForAnUnknownEngineOrAMovedHead(t *testing.T) {
	treeGateRepo(t)
	ctx := context.Background()
	t.Setenv("SPARKWING_COMMIT", strings.Repeat("0", 40))
	if err := recordTreePass(gateAffected)(ctx); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SPARKWING_ENGINE_VERSION", "")
	if err := recordTreePass(gateAffected)(ctx); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SPARKWING_ENGINE_VERSION", "v0.0.1")
	if pass, _ := provenPass(ctx, gateAffected); pass != nil {
		t.Fatalf("recorded a pass for a run whose HEAD moved or whose engine was unknown: %+v", pass)
	}
}

func TestGateModeFollowsTheFullFlag(t *testing.T) {
	for _, tc := range []struct {
		in   GateArgs
		want gateMode
	}{{GateArgs{}, gateAffected}, {GateArgs{Full: true}, gateFull}} {
		g := &Gate{}
		inspectPlan(t, func(plan *sparkwing.Plan) error {
			return g.Plan(t.Context(), plan, tc.in, sparkwing.RunContext{Pipeline: "gate"})
		})
		if g.mode != tc.want {
			t.Errorf("Plan(%+v) mode = %q, want %q", tc.in, g.mode, tc.want)
		}
	}
}
