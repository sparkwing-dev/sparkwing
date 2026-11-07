package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type deployScanJob struct {
	sparkwing.Base
	Job         deployJob
	Environment deployEnv
	Deployment  sparkwing.Ref[envDeployment]
	sparkwing.Produces[deployScanRecord]
}

type deployScanRecord struct {
	Environment string       `json:"environment"`
	DeployRun   string       `json:"deploy_run"`
	Images      deployImages `json:"images"`
	ExampleRun  string       `json:"example_run"`
}

func (j *deployScanJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return deployStep(w, j.Job.Dry, "scan", func(ctx context.Context) (deployScanRecord, error) {
		d, done, err := j.Job.deployer()
		if err != nil {
			return deployScanRecord{}, err
		}
		defer done()
		return d.postDeployScan(ctx, j.Environment, j.Deployment.Get(ctx).Images)
	}), nil
}

func (d *deployer) postDeployScan(ctx context.Context, env deployEnv, images deployImages) (rec deployScanRecord, err error) {
	rec = deployScanRecord{Environment: env.Name, DeployRun: d.runID, Images: images}
	started := d.tools.now()
	completed := false
	defer func() {
		err = errors.Join(err, d.recordScanFinding(ctx, rec, completed, err))
		if !d.dry {
			sparkwing.Summary(ctx, fmt.Sprintf("%s post-deploy scan\n\nDeploy `%s`, commit `%s`, example `%s`\n\nController `%s`, runner `%s`\n\nElapsed: %s. Result: %v",
				env.Name, d.runID, images.Commit, rec.ExampleRun, images.Controller.Digest, images.Runner.Digest, d.tools.now().Sub(started), err))
		}
	}()
	if err = d.assumeRole(ctx); err != nil {
		return rec, err
	}
	if err = d.buildCLI(ctx); err != nil {
		return rec, err
	}
	profile := env.Name + "-scan"
	tokenParam := d.cfg.Params.StagingToken
	if env.Name == d.cfg.Production.Name {
		tokenParam = d.cfg.Params.SmokeTokenBase + d.cfg.SmokeTeams[0]
	}
	if err = d.addProfile(ctx, profile, env.ControllerURL, tokenParam); err != nil {
		return rec, err
	}
	if err = d.triggerRun(ctx, profile, "example", d.repoDir, images.Commit, &rec.ExampleRun, "--api-controller", env.ControllerURL); err != nil {
		return rec, err
	}
	if !d.dry {
		sparkwing.Summary(ctx, fmt.Sprintf("%s full example scan `%s` for deploy `%s`, commit `%s`, controller `%s`, runner `%s`. Promotion does not wait for this scan.",
			env.Name, rec.ExampleRun, d.runID, images.Commit, images.Controller.Digest, images.Runner.Digest))
	}
	err = d.act(ctx, "await full example scan on "+profile, func() error {
		out, followErr := d.sw(ctx, "", "runs", "status", "--run", rec.ExampleRun, "--profile", profile, "--follow", "--exit-zero", "--timeout", d.cfg.RunTimeout.String(), "-o", "json")
		if followErr != nil {
			return followErr
		}
		detail, decodeErr := exampleDecodeStatus(out)
		if decodeErr != nil {
			return decodeErr
		}
		if detail.ID != rec.ExampleRun {
			return fmt.Errorf("scan returned run %s, want %s", detail.ID, rec.ExampleRun)
		}
		switch detail.Status {
		case "success":
			completed = true
			return nil
		case "failed", "cancelled":
			completed = true
			return fmt.Errorf("example run %s ended %s", rec.ExampleRun, detail.Status)
		default:
			return fmt.Errorf("example run %s is still %s", rec.ExampleRun, detail.Status)
		}
	})
	return rec, err
}

func (d *deployer) recordScanFinding(ctx context.Context, rec deployScanRecord, completed bool, scanErr error) error {
	return d.act(ctx, "record "+rec.Environment+" post-deploy scan result in Bitwing", func() error {
		args := []string{"finding", "record", "--source", "sparkwing-post-deploy-" + rec.Environment, "-o", "json"}
		findings := []map[string]string{}
		if scanErr != nil {
			detail, err := json.Marshal(rec)
			if err != nil {
				return err
			}
			findings = append(findings, map[string]string{
				"fingerprint": "full-example",
				"severity":    "high",
				"title":       rec.Environment + " post-deploy example scan failed",
				"detail":      string(detail) + "\n" + scanErr.Error(),
			})
		}
		body, err := json.Marshal(findings)
		if err != nil {
			return err
		}
		if !completed {
			args = append(args, "--could-not-run", fmt.Sprintf("deploy %s, commit %s, example %s: %v", rec.DeployRun, rec.Images.Commit, rec.ExampleRun, scanErr))
		} else {
			file := filepath.Join(d.tmp, "scan-findings.json")
			if err := os.WriteFile(file, body, 0o600); err != nil {
				return err
			}
			args = append(args, "--file", file)
		}
		// safety: cancellation must not discard the scan's failure feedback.
		feedback, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		_, err = d.cmd(feedback, deployCommand{Name: "bitwing", Args: args})
		if err != nil {
			return fmt.Errorf("scan feedback failed; install bitwing in the deploy toolchain and check its ledger access: %w", err)
		}
		return nil
	})
}
