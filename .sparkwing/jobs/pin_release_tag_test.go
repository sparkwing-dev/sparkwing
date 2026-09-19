package jobs

import (
	"context"
	"path/filepath"
	"testing"
)

func TestAutoBumpLeavesThePinAloneAtTheTagBeingReleased(t *testing.T) {
	repo := seedReleaseRepo(t)
	gitRun(t, repo, "tag", "v0.2.0")

	bumped, err := autoBumpSparkwingPinIfStale(grantedCtx(context.Background()), repo)
	if err != nil {
		t.Fatalf("autoBumpSparkwingPinIfStale at the release tag: %v", err)
	}
	if bumped != "" {
		t.Fatalf("the pin was bumped to %s while verifying the tag that carries it; a release cannot pin itself", bumped)
	}
	if out := gitRun(t, repo, "status", "--porcelain"); out != "" {
		t.Errorf("verifying a release tag left the tree dirty:\n%s", out)
	}
}

func TestAutoBumpStillMovesThePinWhenTheTagIsBehindHead(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 0.2s of real work; the fast class runs under -short")
	}
	repo := seedReleaseRepo(t)
	gitRun(t, repo, "tag", "v0.2.0")
	writeFile(t, filepath.Join(repo, "doc.go"), "package sparkwing\n\n// a later commit\n")
	gitRun(t, repo, "add", "doc.go")
	gitRun(t, repo, "commit", "-m", "move past the tag")

	bumped, err := autoBumpSparkwingPinIfStale(grantedCtx(context.Background()), repo)
	if err != nil {
		t.Fatalf("autoBumpSparkwingPinIfStale past the release tag: %v", err)
	}
	if bumped != "v0.2.0" {
		t.Fatalf("the pin moved to %q, want v0.2.0; a tag behind HEAD is a release the pin must catch up to", bumped)
	}
}
