package jobs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func advanceDeployMain(t *testing.T, f *fakeDeploy) string {
	t.Helper()
	deployGitIn(t, f.repo, "commit", "--quiet", "--allow-empty", "-m", "next")
	deployGitIn(t, f.repo, "push", "--quiet", "origin", "HEAD:main")
	return deployGitIn(t, f.repo, "rev-parse", "HEAD")
}

func TestDeployPromotesStagedAncestorWhileMainAdvances(t *testing.T) {
	f := newFakeDeploy(t)
	images := f.candidate()
	d := f.deployer(false)
	if _, _, err := d.deployTo(t.Context(), d.cfg.Staging, images); err != nil {
		t.Fatal(err)
	}
	if _, err := d.stagingGate(t.Context(), images); err != nil {
		t.Fatal(err)
	}
	advanceDeployMain(t, f)
	deployGitIn(t, f.repo, "checkout", "--quiet", "--detach", images.Commit)
	if _, err := f.deployer(false).promote(t.Context(), images); err != nil {
		t.Fatalf("staging-verified ancestor must reach production: %v", err)
	}
	if got := f.pinsOnMain("sparkwing"); got.Controller.Digest != images.Controller.Digest {
		t.Fatalf("production pins = %+v", got)
	}
}

func TestDeployCoalescesQueuedCommitsBeforeImageWork(t *testing.T) {
	f := newFakeDeploy(t)
	stale := f.head
	newest := advanceDeployMain(t, f)
	deployGitIn(t, f.repo, "checkout", "--quiet", "--detach", stale)
	d := f.deployer(false)
	if _, err := d.buildCandidate(t.Context(), "v0.68.4"); err == nil || !strings.Contains(err.Error(), "only main's tip deploys") {
		t.Fatalf("superseded queued build: %v", err)
	}
	if f.sessions != 0 || len(f.registry.published) != 0 || len(f.triggered("deploy-smoke")) != 0 {
		t.Fatal("a stale queue entry reached credentials, image publication or Cloud smoke")
	}
	deployGitIn(t, f.repo, "checkout", "--quiet", "--detach", newest)
	if _, err := f.deployer(false).buildCandidate(t.Context(), "v0.68.4"); err != nil {
		t.Fatalf("newest queued commit: %v", err)
	}
}

func TestDeployRejectsNewerProductionRevisionWithOlderVersion(t *testing.T) {
	f := newFakeDeploy(t)
	images := f.candidate()
	newer := advanceDeployMain(t, f)
	for _, pin := range []imagePin{f.old.Controller, f.old.Runner} {
		f.registry.revisions[pin.Repository+"@"+pin.Digest] = newer
	}
	before := deployGitIn(t, f.bare, "rev-parse", "main")
	if _, err := f.deployer(false).promote(t.Context(), images); err == nil || !strings.Contains(err.Error(), "production revision") {
		t.Fatalf("source downgrade: %v", err)
	}
	if deployGitIn(t, f.bare, "rev-parse", "main") != before {
		t.Fatal("a source downgrade reached GitOps")
	}
}

func TestDeployRejectsCommitOutsideMain(t *testing.T) {
	f := newFakeDeploy(t)
	if err := os.WriteFile(filepath.Join(f.repo, "SIDE"), []byte("side"), 0o644); err != nil {
		t.Fatal(err)
	}
	deployGitIn(t, f.repo, "add", "SIDE")
	deployGitIn(t, f.repo, "commit", "--quiet", "-m", "side")
	images := f.candidate()
	images.Commit = deployGitIn(t, f.repo, "rev-parse", "HEAD")
	before := deployGitIn(t, f.bare, "rev-parse", "main")
	if _, err := f.deployer(false).promote(t.Context(), images); err == nil {
		t.Fatal("a commit outside main reached production")
	}
	if deployGitIn(t, f.bare, "rev-parse", "main") != before {
		t.Fatal("a commit outside main reached GitOps")
	}
}
