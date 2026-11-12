package jobs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestQueuedReviewUsesPinnedBaseWithoutTrackingRef(t *testing.T) {
	fx, root := gitEncodingReviewFixture(t, map[string]string{"change.go": "package change\n// before\n"}, map[string]string{"change.go": "package change\n// after\n"})
	base := strings.TrimSpace(gitOutput(t, root, "rev-parse", "origin/main"))
	runTestGit(t, root, "update-ref", "-d", "refs/remotes/origin/main")
	rec, _, err := runReview(newTestContext(t), fx.deps, testPolicy(t), reviewInputs{branch: "work", brief: "Review the queued change", base: base})
	if err != nil {
		t.Fatal(err)
	}
	if rec.Base != base || len(fx.def.calls) != 1 || !strings.Contains(fx.def.calls[0].prompt, "+// after") {
		t.Fatalf("wrong queued base or omitted change: %+v", rec)
	}
}

func TestQueuedReviewUsesTheLandingCheckoutInsteadOfAnIsolatedSnapshot(t *testing.T) {
	isolateTestHome(t)
	policy, err := os.ReadFile(filepath.Join(sourceTreeRoot(), ".review.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	owner := t.TempDir()
	gitInit(t, owner)
	for path, body := range map[string]string{".review.yaml": string(policy), "AGENTS.md": "# Fixture\n", ".sparkwing/go.mod": "module fixture\n"} {
		writeGoFile(t, filepath.Join(owner, path), body)
	}
	gitCommitAll(t, owner, "landing tree")
	base := strings.TrimSpace(gitOutput(t, owner, "rev-parse", "HEAD"))
	runTestGit(t, owner, "update-ref", "refs/remotes/origin/main", base)
	snapshot := t.TempDir()
	gitInit(t, snapshot)
	for path, body := range map[string]string{".review.yaml": string(policy), "AGENTS.md": "# Snapshot\n", ".sparkwing/go.mod": "module snapshot\n"} {
		writeGoFile(t, filepath.Join(snapshot, path), body)
	}
	gitCommitAll(t, snapshot, "isolated input")
	t.Chdir(snapshot)
	job := reviewJob{branch: "queued", args: ReviewArgs{Brief: "Review the landing tree", base: base, checkout: owner}}
	rec, err := job.run(newTestContext(t))
	if err != nil {
		t.Fatal(err)
	}
	if rec.Base != base || rec.Commit != base || rec.Result != resultPass {
		t.Fatalf("reviewed another checkout: %+v", rec)
	}
	common := strings.TrimSpace(gitOutput(t, owner, "rev-parse", "--path-format=absolute", "--git-common-dir"))
	if _, err := os.Stat(reviewRecordPath(filepath.Join(common, "sparkwing-review"), "queued")); err != nil {
		t.Fatalf("review record not retained in landing checkout: %v", err)
	}
}

func TestReviewChangedBaseCannotReuseAnUnchangedTreeVerdict(t *testing.T) {
	fx, root := gitEncodingReviewFixture(t, map[string]string{"change.go": "package change\n// before\n"}, map[string]string{"change.go": "package change\n// after\n"})
	head := strings.TrimSpace(gitOutput(t, root, "rev-parse", "HEAD"))
	base := strings.TrimSpace(gitOutput(t, root, "rev-parse", "origin/main"))
	ctx := newTestContext(t)
	if _, _, err := runReview(ctx, fx.deps, testPolicy(t), reviewInputs{branch: "work", brief: "Review selected base", base: head}); err != nil {
		t.Fatal(err)
	}
	if len(fx.def.calls) != 0 {
		t.Fatal("empty diff invoked the model")
	}
	if _, _, err := runReview(ctx, fx.deps, testPolicy(t), reviewInputs{branch: "work", brief: "Review selected base", base: base}); err != nil {
		t.Fatal(err)
	}
	if len(fx.def.calls) != 1 || !strings.Contains(fx.def.calls[0].prompt, "+// after") {
		t.Fatal("changed base reused a verdict without inspecting its newly included patch")
	}
}
