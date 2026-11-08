package jobs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
	"github.com/sparkwing-dev/sparkwing/sparkwing/sparkwingtest"
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
	for _, phase := range deployPhases {
		for _, evidence := range []string{"newer", "missing", "equal"} {
			t.Run(phase.name+"/"+evidence, func(t *testing.T) {
				f := newFakeDeploy(t)
				images := f.candidate()
				revision := ""
				switch evidence {
				case "newer":
					revision = advanceDeployMain(t, f)
				case "equal":
					revision = images.Commit
				}
				pin := phase.want(f.old)
				f.registry.revisions[pin.Repository+"@"+pin.Digest] = revision
				before := deployGitIn(t, f.bare, "rev-parse", "main")
				if _, err := f.deployer(false).promote(t.Context(), images); err == nil || !strings.Contains(err.Error(), "production revision") {
					t.Fatalf("invalid production source evidence: %v", err)
				}
				if deployGitIn(t, f.bare, "rev-parse", "main") != before {
					t.Fatal("invalid production source evidence reached GitOps")
				}
			})
		}
	}
}

func TestDeployBurstOfLandsConvergesOnNewestWaitingCommit(t *testing.T) {
	f := newFakeDeploy(t)
	active, err := f.deployer(false).buildCandidate(t.Context(), "v0.68.4")
	if err != nil {
		t.Fatal(err)
	}
	var waiting []string
	for range 4 {
		waiting = append(waiting, advanceDeployMain(t, f))
	}
	newest := waiting[len(waiting)-1]
	var gated []string
	finish := func(images deployImages) {
		t.Helper()
		d := f.deployer(false)
		if _, _, err := d.deployTo(t.Context(), d.cfg.Staging, images); err != nil {
			t.Fatal(err)
		}
		rec, err := f.deployer(false).stagingGate(t.Context(), images)
		if err != nil {
			t.Fatal(err)
		}
		gated = append(gated, rec.Images.Commit)
		raw, err := json.Marshal(rec)
		if err != nil {
			t.Fatal(err)
		}
		ctx := newTestHost(t, sparkwingtest.Options{Outputs: map[string]json.RawMessage{"staging-gate": raw}}).Context(t.Context())
		if err := requireStagingPass(ctx, images, sparkwing.Ref[stagingGateRecord]{JobID: "staging-gate"}); err != nil {
			t.Fatal(err)
		}
		if _, err := f.deployer(false).promote(t.Context(), images); err != nil {
			t.Fatal(err)
		}
	}
	deployGitIn(t, f.repo, "checkout", "--quiet", "--detach", active.Commit)
	finish(active)
	for _, commit := range waiting {
		deployGitIn(t, f.repo, "checkout", "--quiet", "--detach", commit)
		sessions, published, smokes := f.sessions, len(f.registry.published), len(f.triggered("deploy-smoke"))
		images, err := f.deployer(false).buildCandidate(t.Context(), "v0.68.5")
		if commit != newest {
			if err == nil || !strings.Contains(err.Error(), "only main's tip deploys") {
				t.Fatalf("stale waiter %s was not rejected at admission: %v", commit, err)
			}
			if f.sessions != sessions || len(f.registry.published) != published || len(f.triggered("deploy-smoke")) != smokes {
				t.Fatal("stale waiter reached credentials, image work or Cloud smoke")
			}
			continue
		}
		if err != nil || images.Commit != newest {
			t.Fatalf("newest waiter failed admission: %+v, %v", images, err)
		}
		finish(images)
	}
	if !slices.Equal(gated, []string{active.Commit, newest}) || len(f.registry.published) != 2 || len(f.triggered("deploy-smoke")) != 4 {
		t.Fatalf("burst did not coalesce: gates=%v, publications=%d, smokes=%v", gated, len(f.registry.published), f.triggered("deploy-smoke"))
	}
	pins := f.pinsOnMain("sparkwing")
	for _, pin := range []imagePin{pins.Controller, pins.Launcher} {
		if revision := f.registry.revisions[pin.Repository+"@"+pin.Digest]; revision != newest {
			t.Fatalf("production ended at %s, want newest %s", revision, newest)
		}
	}
}

func TestDeployProductionRetryFinishesOnlyMissingPins(t *testing.T) {
	f := newFakeDeploy(t)
	images := f.candidate()
	f.registry.revisions[images.Controller.Repository+"@"+images.Controller.Digest] = images.Commit
	f.registry.revisions[images.Runner.Repository+"@"+images.Runner.Digest] = images.Commit
	values, err := pinValues(testValues(f.old.Version, f.old.Controller.Digest, f.old.Runner.Digest), deployPhaseController, images.Controller)
	if err != nil {
		t.Fatal(err)
	}
	f.commitValues("sparkwing", values)
	rec, err := f.deployer(false).promote(t.Context(), images)
	if err != nil || len(rec.Changes) != 1 || rec.Changes[0].Phase != deployPhaseLauncher {
		t.Fatalf("retry did not finish the launcher alone: %+v, %v", rec, err)
	}
	rec, err = f.deployer(false).promote(t.Context(), images)
	if err != nil || len(rec.Changes) != 0 {
		t.Fatalf("retry changed an already deployed candidate: %+v, %v", rec, err)
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
