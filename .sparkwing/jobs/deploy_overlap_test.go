package jobs

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

func deploySourceClone(t *testing.T, f *fakeDeploy, revision string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "source")
	deployGitIn(t, filepath.Dir(dir), "clone", "--quiet", filepath.Join(filepath.Dir(f.repo), "source.git"), dir)
	deployGitIn(t, dir, "checkout", "--quiet", "--detach", revision)
	return dir
}

func TestDeployBuildAndStagingOverlapProduction(t *testing.T) {
	f := newFakeDeploy(t)
	images, err := f.deployer(false).buildCandidate(t.Context(), "v0.68.4")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.deployer(false).stage(t.Context(), images); err != nil {
		t.Fatal(err)
	}
	production := f.deployer(false)
	production.repoDir = deploySourceClone(t, f, images.Commit)
	prepared, resume := make(chan struct{}), make(chan struct{})
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(resume) }) })
	run := production.tools.run
	production.tools.run = func(ctx context.Context, c deployCommand) (string, error) {
		out, err := run(ctx, c)
		if err == nil && slices.Equal(c.Args, []string{"rev-parse", "HEAD"}) && strings.Contains(c.Dir, "gitops-production") {
			select {
			case <-prepared:
			default:
				close(prepared)
				select {
				case <-resume:
				case <-ctx.Done():
					return "", ctx.Err()
				}
			}
		}
		return out, err
	}
	finished := make(chan error, 1)
	go func() {
		_, err := production.promote(t.Context(), images)
		finished <- err
	}()
	select {
	case <-prepared:
	case err := <-finished:
		t.Fatalf("production stopped before overlap: %v", err)
	case <-t.Context().Done():
		t.Fatal(t.Context().Err())
	}
	newest := advanceDeployMain(t, f)
	next := f.deployer(false)
	next.repoDir = deploySourceClone(t, f, newest)
	nextImages, err := next.buildCandidate(t.Context(), "v0.68.5")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := next.stage(t.Context(), nextImages); err != nil {
		t.Fatalf("next candidate cannot stage during production: %v", err)
	}
	select {
	case err := <-finished:
		t.Fatalf("production did not remain active during the next staging gate: %v", err)
	default:
	}
	release.Do(func() { close(resume) })
	if err := <-finished; err != nil {
		t.Fatalf("production lost the concurrent staging GitOps writes: %v", err)
	}
	if pins := f.pinsOnMain("sparkwing-staging"); pins.Controller.Digest != nextImages.Controller.Digest || pins.Launcher.Digest != nextImages.Runner.Digest {
		t.Fatalf("production overwrote staging: %+v", pins)
	}
	if pins := f.pinsOnMain("sparkwing"); pins.Controller.Digest != images.Controller.Digest || pins.Launcher.Digest != images.Runner.Digest {
		t.Fatalf("production did not finish its staged ancestor: %+v", pins)
	}
}

func TestDeploySupersedesBuiltStagingWaiter(t *testing.T) {
	f := newFakeDeploy(t)
	waiting, err := f.deployer(false).buildCandidate(t.Context(), "v0.68.4")
	if err != nil {
		t.Fatal(err)
	}
	newest := advanceDeployMain(t, f)
	sessions, smokes := f.sessions, len(f.triggered("deploy-smoke"))
	before := deployGitIn(t, f.bare, "rev-parse", "main")
	if _, err := f.deployer(false).stage(t.Context(), waiting); err == nil || !strings.Contains(err.Error(), "only main's tip deploys") {
		t.Fatalf("stale built waiter entered staging: %v", err)
	}
	if f.sessions != sessions || len(f.triggered("deploy-smoke")) != smokes || deployGitIn(t, f.bare, "rev-parse", "main") != before {
		t.Fatal("stale staging waiter reached credentials, GitOps or Cloud smoke")
	}
	next := f.deployer(false)
	next.repoDir = deploySourceClone(t, f, newest)
	images, err := next.buildCandidate(t.Context(), "v0.68.5")
	if err != nil {
		t.Fatal(err)
	}
	if rec, err := next.stage(t.Context(), images); err != nil || rec.Images.Commit != newest {
		t.Fatalf("newest waiter did not pass staging: %+v, %v", rec, err)
	}
}

func TestDeployArgoAcceptsOnlyUnchangedEnvironmentDescendant(t *testing.T) {
	f := newFakeDeploy(t)
	d := f.deployer(false)
	images := f.candidate()
	if _, _, err := d.deployTo(t.Context(), d.cfg.Staging, images); err != nil {
		t.Fatal(err)
	}
	staged := deployGitIn(t, f.bare, "rev-parse", "main")
	f.commitValues("sparkwing", testValues(images.Version, images.Controller.Digest, images.Runner.Digest))
	if err := d.waitRollout(t.Context(), d.cfg.Staging, staged, deployPhases[1], images.Runner.Digest, nil); err != nil {
		t.Fatalf("other environment's descendant blocked Argo: %v", err)
	}
	f.commitValues("sparkwing-staging", testValues(f.old.Version, f.old.Controller.Digest, f.old.Runner.Digest))
	if matches, err := d.gitopsEnvironmentUnchanged(t.Context(), d.cfg.Staging, staged, deployGitIn(t, f.bare, "rev-parse", "main"), nil); err != nil || matches {
		t.Fatalf("changed environment accepted: %v, %v", matches, err)
	}
	if matches, err := d.gitopsEnvironmentUnchanged(t.Context(), d.cfg.Staging, staged, deployGitIn(t, f.bare, "rev-list", "--max-parents=0", "main"), nil); err != nil || matches {
		t.Fatalf("older revision accepted: %v, %v", matches, err)
	}
}

func TestDeployRollbackPreservesConcurrentStagingPush(t *testing.T) {
	f := newFakeDeploy(t)
	d := f.deployer(false)
	images := f.candidate()
	if _, _, err := d.deployTo(t.Context(), d.cfg.Staging, images); err != nil {
		t.Fatal(err)
	}
	f.onSmoke = func(string) error {
		return errors.New("production smoke failed")
	}
	run := d.tools.run
	pushes := 0
	d.tools.run = func(ctx context.Context, c deployCommand) (string, error) {
		if len(c.Args) > 0 && c.Args[0] == "push" && strings.Contains(c.Dir, "gitops-production") {
			pushes++
			if pushes == 3 {
				f.commitValues("sparkwing-staging", testValues("v0.68.5", testDigest("next-ctl"), testDigest("next-run")))
			}
		}
		return run(ctx, c)
	}
	if _, err := d.promote(t.Context(), images); err == nil || !strings.Contains(err.Error(), "rolled back") {
		t.Fatalf("production did not roll back: %v", err)
	}
	if pins := f.pinsOnMain("sparkwing-staging"); pins.Controller.Digest != testDigest("next-ctl") {
		t.Fatalf("rollback overwrote next staging: %+v", pins)
	}
	if pins := f.pinsOnMain("sparkwing"); pins.Controller.Digest != f.old.Controller.Digest || pins.Launcher.Digest != f.old.Runner.Digest {
		t.Fatalf("rollback did not restore production: %+v", pins)
	}
}

func TestDeployRechecksMainAfterConcurrentGitopsPush(t *testing.T) {
	f := newFakeDeploy(t)
	d := f.deployer(false)
	images := f.candidate()
	if _, err := d.stage(t.Context(), images); err != nil {
		t.Fatal(err)
	}
	run := d.tools.run
	rejected := false
	d.tools.run = func(ctx context.Context, c deployCommand) (string, error) {
		if !rejected && len(c.Args) > 0 && c.Args[0] == "push" && strings.Contains(c.Dir, "gitops-production") {
			f.commitValues("sparkwing-staging", testValues("v0.68.5", testDigest("next-ctl"), testDigest("next-run")))
			out, err := run(ctx, c)
			if err == nil {
				return out, errors.New("production push unexpectedly accepted the old GitOps head")
			}
			rejected = true
			deployGitIn(t, filepath.Join(filepath.Dir(f.repo), "source.git"), "update-ref", "refs/heads/main", f.old.Commit)
			return out, err
		}
		return run(ctx, c)
	}
	if _, err := d.promote(t.Context(), images); !rejected || err == nil || !strings.Contains(err.Error(), "refusing production GitOps push") {
		t.Fatalf("retry did not reject the force-removed source: rejected=%v, %v", rejected, err)
	}
	if pins := f.pinsOnMain("sparkwing"); pins.Controller.Digest != f.old.Controller.Digest || pins.Launcher.Digest != f.old.Runner.Digest {
		t.Fatalf("force-removed source reached production on retry: %+v", pins)
	}
}
