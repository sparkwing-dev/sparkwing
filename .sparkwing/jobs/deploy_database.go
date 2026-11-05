package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
)

func (d *deployer) checkAutomatedBackups(ctx context.Context, env deployEnv) error {
	return d.act(ctx, "verify RDS automated backups are enabled for "+env.DBInstance, func() error {
		if env.DBInstance == "" {
			return fmt.Errorf("%s has no target RDS instance configured", env.Name)
		}
		out, err := d.cmd(ctx, deployCommand{Env: d.env(), Quiet: true, Name: "aws", Args: []string{
			"rds", "describe-db-instances", "--db-instance-identifier", env.DBInstance, "--output", "json",
		}})
		if err != nil {
			return fmt.Errorf("read RDS backup retention for %s (requires rds:DescribeDBInstances): %w", env.DBInstance, err)
		}
		var response struct {
			DBInstances []struct {
				DBInstanceIdentifier  string
				BackupRetentionPeriod int
			}
		}
		if err := json.Unmarshal([]byte(out), &response); err != nil {
			return fmt.Errorf("decode RDS backup retention: %w", err)
		}
		if len(response.DBInstances) != 1 || response.DBInstances[0].DBInstanceIdentifier != env.DBInstance {
			return fmt.Errorf("RDS describe returned no unique target instance %s", env.DBInstance)
		}
		if response.DBInstances[0].BackupRetentionPeriod <= 0 {
			return fmt.Errorf("RDS instance %s has automated backups disabled (BackupRetentionPeriod <= 0); enable automated backups before deploying so point-in-time recovery is available", env.DBInstance)
		}
		return nil
	})
}

func (d *deployer) snapshotSchemaChange(ctx context.Context, env deployEnv, images deployImages) (string, error) {
	image, err := d.kubectl(ctx, "-n", env.Namespace, "get", "deployment", env.ControllerDeployment,
		"-o", "jsonpath={.spec.template.spec.containers[0].image}")
	if err != nil {
		return "", err
	}
	pin, err := name.NewDigest(strings.TrimSpace(image))
	if err != nil {
		return "", fmt.Errorf("%s controller image must be pinned by digest: %w", env.Name, err)
	}
	deployed := imagePin{Repository: pin.Context().Name(), Digest: pin.DigestStr()}
	// safety: the revision label is authoritative. A tag is only a fallback for older images.
	ref, _, _ := strings.Cut(strings.TrimSpace(image), "@")
	if colon := strings.LastIndex(ref, ":"); colon > strings.LastIndex(ref, "/") {
		deployed.Tag = ref[colon+1:]
	}
	version, digest, err := d.deployedStoreSchema(ctx, env, deployed)
	if err != nil {
		return "", err
	}
	if version <= 0 || images.SchemaVersion <= 0 {
		return "", fmt.Errorf("cannot compare schema versions: deployed %d, candidate %d", version, images.SchemaVersion)
	}
	if version == images.SchemaVersion {
		if digest != images.SchemaDigest {
			return "", fmt.Errorf("%s schema version %d has a different baseline from the candidate; bump the schema version and provide a forward migration before deploying", env.Name, version)
		}
		d.tools.say(ctx, "no schema change; relying on RDS point-in-time recovery")
		return "", nil
	}
	if version > images.SchemaVersion {
		return "", fmt.Errorf("%s runs schema %d, newer than candidate schema %d; forward migrations cannot be rolled back by deploying an older image", env.Name, version, images.SchemaVersion)
	}
	snapshot := fmt.Sprintf("sparkwing-deploy-%s-%d", env.Name, d.tools.now().UnixNano())
	d.tools.say(ctx, fmt.Sprintf("schema %d -> %d: create RDS snapshot %s before rollout", version, images.SchemaVersion, snapshot))
	if _, err := d.cmd(ctx, deployCommand{Env: d.env(), Quiet: true, Name: "aws", Args: []string{
		"rds", "create-db-snapshot", "--db-instance-identifier", env.DBInstance, "--db-snapshot-identifier", snapshot, "--output", "json",
	}}); err != nil {
		return snapshot, fmt.Errorf("create pre-migration RDS snapshot %s (requires rds:CreateDBSnapshot): %w", snapshot, err)
	}
	d.tools.say(ctx, "wait for RDS snapshot "+snapshot+" to be available before rollout")
	waitCtx, cancel := context.WithTimeout(ctx, deploySnapshotBudget)
	defer cancel()
	_, err = d.cmd(waitCtx, deployCommand{Env: d.env(), Quiet: true, Name: "aws", Args: []string{
		"rds", "wait", "db-snapshot-available", "--db-instance-identifier", env.DBInstance, "--db-snapshot-identifier", snapshot,
	}})
	if err != nil {
		return snapshot, fmt.Errorf("RDS snapshot %s is not ready; no rollout started (requires rds:DescribeDBSnapshots): %w", snapshot, err)
	}
	// safety: a snapshot wait can consume half the role's one-hour session.
	if err := d.refreshRole(ctx); err != nil {
		return snapshot, err
	}
	return snapshot, nil
}
