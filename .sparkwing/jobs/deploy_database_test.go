package jobs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
)

func TestDeploySnapshotsOnlySchemaChanges(t *testing.T) {
	for _, envName := range []string{"staging", "production"} {
		for _, changed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/changed=%t", envName, changed), func(t *testing.T) {
				f := newFakeDeploy(t)
				d := f.deployer(false)
				env := d.cfg.Staging
				if envName == "production" {
					env = d.cfg.Production
				}
				images := f.candidate()
				if changed {
					images.SchemaVersion++
					images.SchemaDigest = schemaBaselineDigestOf("new schema")
				}
				rec, _, err := d.deployTo(t.Context(), env, images)
				if err != nil {
					t.Fatal(err)
				}
				create := slices.IndexFunc(f.calls, func(call string) bool { return strings.Contains(call, "rds create-db-snapshot") })
				wait := slices.IndexFunc(f.calls, func(call string) bool { return strings.Contains(call, "rds wait db-snapshot-available") })
				push := slices.IndexFunc(f.calls, func(call string) bool { return strings.Contains(call, "git push") })
				if changed {
					if rec.Snapshot == "" || create < 0 || wait <= create || push <= wait || !strings.Contains(f.calls[create], "--db-instance-identifier "+env.DBInstance) || !strings.Contains(f.calls[wait], rec.Snapshot) {
						t.Fatalf("snapshot must be available before GitOps push: record=%+v calls=%v", rec, f.calls)
					}
				} else if rec.Snapshot != "" || create >= 0 || wait >= 0 || !slices.Contains(f.said, "no schema change; relying on RDS point-in-time recovery") {
					t.Fatalf("unchanged schema took a snapshot or missed PITR log: record=%+v calls=%v logs=%v", rec, f.calls, f.said)
				}
			})
		}
	}
}

func TestDeployBackupPreflightAndSnapshotFailuresStopBeforePush(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*fakeDeploy, *deployImages)
		want  string
	}{
		{"disabled", func(f *fakeDeploy, _ *deployImages) { f.backupRetention = 0 }, "enable automated backups"},
		{"describe denied", func(f *fakeDeploy, _ *deployImages) { f.rdsDescribeError = errors.New("AccessDenied") }, "rds:DescribeDBInstances"},
		{"create failed", func(f *fakeDeploy, i *deployImages) { i.SchemaVersion++; f.rdsCreateError = errors.New("AccessDenied") }, "rds:CreateDBSnapshot"},
		{"wait failed", func(f *fakeDeploy, i *deployImages) {
			i.SchemaVersion++
			f.rdsWaitError = errors.New("snapshot failed")
		}, "no rollout started"},
		{"schema downgrade", func(_ *fakeDeploy, i *deployImages) { i.SchemaVersion-- }, "forward migrations cannot be rolled back"},
		{"unknown candidate schema", func(_ *fakeDeploy, i *deployImages) { i.SchemaVersion = 0 }, "cannot compare schema versions"},
		{"unversioned baseline", func(_ *fakeDeploy, i *deployImages) { i.SchemaDigest = "other" }, "bump the schema version"},
		{"running image unpinned", func(f *fakeDeploy, _ *deployImages) { f.runningController = "example/controller:latest" }, "must be pinned by digest"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeDeploy(t)
			images := f.candidate()
			tc.setup(f, &images)
			before := deployGitIn(t, f.bare, "rev-parse", "main")
			_, err := f.deployer(false).promote(t.Context(), images)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %s", err, tc.want)
			}
			if after := deployGitIn(t, f.bare, "rev-parse", "main"); after != before {
				t.Fatal("failed preflight or snapshot changed GitOps")
			}
		})
	}
}

func TestDeployBackupPreflightChecksNoOpDeploy(t *testing.T) {
	f := newFakeDeploy(t)
	f.backupRetention = 0
	_, _, err := f.deployer(false).deployTo(t.Context(), f.config().Staging, f.old)
	if err == nil || !strings.Contains(err.Error(), "automated backups disabled") {
		t.Fatalf("no-op deploy with backups off: %v", err)
	}
}

func TestDeployComparesTheRunningControllerImage(t *testing.T) {
	f := newFakeDeploy(t)
	f.runningController = testRegistry + "/sparkwing-controller:v0.68.3@" + testDigest("running")
	// An untraced running image must not inherit the schema of the GitOps pin.
	_, _, err := f.deployer(false).deployTo(t.Context(), f.config().Staging, f.candidate())
	if err == nil || !strings.Contains(err.Error(), "Deploy once by hand") {
		t.Fatalf("untraced running controller = %v", err)
	}
}

func TestDeploySchemaUpgradeFailureKeepsImagesForRecovery(t *testing.T) {
	f := newFakeDeploy(t)
	images := f.candidate()
	images.SchemaVersion++
	f.failRuns["run-deploy-smoke-qa-team"] = true
	rec, err := f.deployer(false).promote(t.Context(), images)
	if err == nil || rec.Snapshot == "" || !strings.Contains(err.Error(), rec.Snapshot) || !strings.Contains(err.Error(), "automatic image rollback is unsafe") {
		t.Fatalf("schema-changing failure = %+v, %v", rec, err)
	}
	if f.pinsOnMain("sparkwing").Controller != images.Controller || slices.ContainsFunc(f.subjects(), func(s string) bool { return strings.HasPrefix(s, "Revert") }) {
		t.Fatal("schema-changing failure reverted images against a potentially upgraded store")
	}
}

func TestDeployBuildsUntaggedMainWithoutARelease(t *testing.T) {
	f := newFakeDeploy(t)
	deployGitIn(t, f.repo, "tag", "v0.68.3")
	deployGitIn(t, f.repo, "commit", "--quiet", "--allow-empty", "-m", "main change")
	f.head = deployGitIn(t, f.repo, "rev-parse", "HEAD")
	d := f.deployer(false)
	images, err := d.buildCandidate(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	want := "v0.68.3-1-g" + f.head[:12]
	if images.Version != want || images.SchemaVersion != 30 || len(f.registry.published) != 1 {
		t.Fatalf("untagged main build = %+v, publications=%v", images, f.registry.published)
	}
	if tags := deployGitIn(t, f.repo, "tag", "--points-at", "HEAD"); tags != "" {
		t.Fatalf("deploy requires a release tag at HEAD: %s", tags)
	}
	if !slices.ContainsFunc(f.calls, func(call string) bool { return strings.Contains(call, "bin/build-web.sh --reuse") }) {
		t.Fatal("deploy did not request validated dashboard reuse")
	}
	_, err = d.buildCandidate(t.Context(), "")
	if err != nil || len(f.registry.published) != 1 {
		t.Fatalf("same main commit should reuse published images: %v, %v", f.registry.published, err)
	}
}

func TestDeployRepositoryPushStopsAfterStaging(t *testing.T) {
	file, err := os.Open(filepath.Join("..", "sparkwing.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	cfg, err := pipelines.Parse(file)
	if err != nil {
		t.Fatal(err)
	}
	entry := cfg.Find("deploy")
	if entry == nil || entry.Args["stop-after"] != "staging" || entry.On.Push == nil || !slices.Equal(entry.On.Push.Branches, []string{"main"}) {
		t.Fatalf("push deploy configuration = %+v", entry)
	}
}

func TestDeployMalformedBackupResponseFailsClosed(t *testing.T) {
	for _, body := range []string{`{`, `{}`, `{"DBInstances":[]}`, `{"DBInstances":[{"DBInstanceIdentifier":"other","BackupRetentionPeriod":7}]}`, `{"DBInstances":[{"DBInstanceIdentifier":"kikd-prod-sparkwing-postgres"}]}`} {
		t.Run(body, func(t *testing.T) {
			f := newFakeDeploy(t)
			d := f.deployer(false)
			d.tools.run = func(ctx context.Context, cmd deployCommand) (string, error) {
				if strings.Contains(cmd.String(), "rds describe-db-instances") {
					return body, nil
				}
				return f.run(ctx, cmd)
			}
			if err := d.checkAutomatedBackups(t.Context(), d.cfg.Staging); err == nil {
				t.Fatalf("accepted malformed backup response %s", body)
			}
		})
	}
}

func TestDeployStagingGateStartsBothRunsBeforeWaiting(t *testing.T) {
	f := newFakeDeploy(t)
	if _, err := f.deployer(false).stagingGate(t.Context(), f.candidate()); err != nil {
		t.Fatal(err)
	}
	firstWait := slices.IndexFunc(f.calls, func(call string) bool { return strings.Contains(call, "runs status") })
	reference := slices.IndexFunc(f.calls, func(call string) bool { return strings.Contains(call, "pipeline trigger hello") })
	if reference < 0 || firstWait <= reference {
		t.Fatalf("staging probes must overlap: %v", f.calls)
	}
}

func TestDeployTracesReleasedImageWithoutRevisionLabel(t *testing.T) {
	f := newFakeDeploy(t)
	deployGitIn(t, f.repo, "tag", "v0.68.3")
	f.registry.revisions = map[string]string{}
	if _, _, err := f.deployer(false).deployTo(t.Context(), f.config().Staging, f.candidate()); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(f.said, "no schema change; relying on RDS point-in-time recovery") {
		t.Fatalf("did not trace deployed release tag: %v", f.said)
	}
}

func TestDeployRenewsRoleAfterSnapshotWait(t *testing.T) {
	f := newFakeDeploy(t)
	d := f.deployer(false)
	d.tools.run = func(ctx context.Context, cmd deployCommand) (string, error) {
		out, err := f.run(ctx, cmd)
		if strings.Contains(cmd.String(), "rds wait db-snapshot-available") {
			f.mu.Lock()
			f.expired = f.sessions
			f.mu.Unlock()
		}
		return out, err
	}
	images := f.candidate()
	images.SchemaVersion++
	if _, _, err := d.deployTo(t.Context(), d.cfg.Staging, images); err != nil {
		t.Fatalf("rollout after snapshot wait must have fresh credentials: %v", err)
	}
}
