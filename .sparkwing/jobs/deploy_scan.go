package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type DeployScanArgs struct {
	Environment string `flag:"environment" default:"staging" enum:"staging,production" desc:"Deployed environment to scan."`
	Record      string `flag:"record" required:"true" desc:"Deployment identity JSON: environment, deploy_run and images."`
	DryRun      bool   `flag:"dry-run" desc:"Print scan actions without executing them."`
}

type DeployScan struct{ sparkwing.Base }

func (DeployScan) ShortHelp() string {
	return "Run a detached revision-bound full example scan and report Bitwing findings"
}

func (DeployScan) Help() string {
	return "Started as a detached child by deploy after each environment smoke, inheriting its verified repository and revision. Uses the deploy-kikd-prod desktop runner for the wrapper and hosts the full example on that same hardware with --runner here. A one-hour memoized admission receipt coalesces each environment and immutable revision, including failed scan attempts. Uses an independent per-environment concurrency group. Bitwing uses the runner user's PATH and existing ledger configuration. The source commit must match the supplied image identity. Reads DEPLOY_SCAN_STAGING_TOKEN or DEPLOY_SCAN_PRODUCTION_TOKEN from scoped Sparkwing secrets; it never assumes the deploy AWS role. Failures upsert a Bitwing finding, clean observations resolve it, and unavailable observations preserve earlier findings."
}

type deployScanSecrets struct {
	Staging    string `sw:"DEPLOY_SCAN_STAGING_TOKEN,optional"`
	Production string `sw:"DEPLOY_SCAN_PRODUCTION_TOKEN,optional"`
}

func (DeployScan) Secrets() any { return &deployScanSecrets{} }

func (DeployScan) Plan(_ context.Context, plan *sparkwing.Plan, in DeployScanArgs, rc sparkwing.RunContext) error {
	cfg := defaultDeployConfig()
	env := cfg.Staging
	switch in.Environment {
	case "", cfg.Staging.Name:
	case cfg.Production.Name:
		env = cfg.Production
	default:
		return fmt.Errorf("deploy-scan: unknown environment %q", in.Environment)
	}
	dry := in.DryRun || rc.DryRun
	var labels []string
	if !dry {
		plan.Concurrency(sparkwing.NewConcurrencyGroup("sparkwing-post-deploy-"+env.Name, sparkwing.ConcurrencyLimit{Capacity: 1}))
		labels = []string{"deploy-kikd-prod"}
	}
	sourceCommit := ""
	if rc.Git != nil {
		sourceCommit = rc.Git.SHA
	}
	admit := sparkwing.Job(plan, "admit", &deployScanAdmission{RunID: rc.RunID}).Requires(labels...).Timeout(time.Minute)
	if !dry {
		admit.Memo(func(context.Context) (sparkwing.MemoValue, error) {
			rec, err := decodeScanRecord(in.Record, env.Name, sourceCommit, false)
			if err != nil {
				return nil, err
			}
			return sparkwing.MemoKey(env.Name, rec.Images.Commit), nil
		}, sparkwing.MemoTTL(time.Hour))
	}
	owner := sparkwing.RefTo[string](admit)
	sparkwing.Job(plan, "scan", &deployScanJob{
		Job:         deployJob{Cfg: cfg, Dry: dry, RunID: rc.RunID, Name: "scan"},
		Environment: env, Record: in.Record, SourceCommit: sourceCommit,
	}).Needs(admit).Requires(labels...).Timeout(cfg.RunTimeout + 15*time.Minute).
		SkipIf(func(ctx context.Context) bool { return owner.Get(ctx) != rc.RunID })
	return nil
}

type deployScanAdmission struct {
	sparkwing.Base
	sparkwing.Produces[string]
	RunID string
}

func (j *deployScanAdmission) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(w, "claim", func(context.Context) (string, error) {
		return j.RunID, nil
	}), nil
}

func decodeScanRecord(raw, environment, sourceCommit string, dry bool) (deployScanRecord, error) {
	var rec deployScanRecord
	if err := json.Unmarshal([]byte(raw), &rec); err != nil {
		return rec, fmt.Errorf("deploy-scan: decode deployment record: %w", err)
	}
	if rec.Environment != environment {
		return rec, errors.New("deploy-scan: deployment record must match the selected environment")
	}
	if _, err := gitRevision(rec.Images.Commit); err != nil {
		return rec, err
	}
	if rec.DeployRun == "" || !rec.Images.sameDigests(rec.Images) {
		return rec, errors.New("deploy-scan: deployment run and both image digests are required")
	}
	if !dry && sourceCommit != rec.Images.Commit {
		return rec, errors.New("deploy-scan: pipeline source must match the deployed commit")
	}
	return rec, nil
}

type deployScanJob struct {
	sparkwing.Base
	Job          deployJob
	Environment  deployEnv
	Record       string
	SourceCommit string
	sparkwing.Produces[deployScanRecord]
}

type deployScanRecord struct {
	Environment string       `json:"environment"`
	ScanRun     string       `json:"scan_run,omitempty"`
	DeployRun   string       `json:"deploy_run"`
	Images      deployImages `json:"images"`
	ExampleRun  string       `json:"example_run"`
}

func (j *deployScanJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return deployStep(w, j.Job.Dry, "scan", func(ctx context.Context) (deployScanRecord, error) {
		rec, err := decodeScanRecord(j.Record, j.Environment.Name, j.SourceCommit, j.Job.Dry)
		if err != nil {
			return rec, err
		}
		d, done, err := j.Job.deployer()
		if err != nil {
			return deployScanRecord{}, err
		}
		defer done()
		return d.postDeployScan(ctx, j.Environment, rec.Images, rec.DeployRun)
	}), nil
}

func (d *deployer) postDeployScan(ctx context.Context, env deployEnv, images deployImages, deployRun string) (rec deployScanRecord, err error) {
	rec = deployScanRecord{Environment: env.Name, DeployRun: deployRun, ScanRun: d.runID, Images: images}
	started := d.tools.now()
	completed := false
	defer func() {
		err = errors.Join(err, d.recordScanFinding(ctx, rec, completed, err))
		if !d.dry {
			sparkwing.Summary(ctx, fmt.Sprintf("%s post-deploy scan\n\nDeploy `%s`, commit `%s`, example `%s`\n\nController `%s`, runner `%s`\n\nElapsed: %s. Result: %v",
				env.Name, rec.DeployRun, images.Commit, rec.ExampleRun, images.Controller.Digest, images.Runner.Digest, d.tools.now().Sub(started), err))
		}
	}()
	if err = d.buildCLI(ctx); err != nil {
		return rec, err
	}
	profile := env.Name + "-scan"
	secretName := "DEPLOY_SCAN_STAGING_TOKEN"
	if env.Name == d.cfg.Production.Name {
		secretName = "DEPLOY_SCAN_PRODUCTION_TOKEN"
	}
	if err = d.act(ctx, "configure fixture profile from "+secretName, func() error {
		token, err := sparkwing.Secret(ctx, secretName)
		if err != nil {
			return err
		}
		return d.profileToken(ctx, profile, env.ControllerURL, token)
	}); err != nil {
		return rec, err
	}
	if err = d.triggerRun(ctx, profile, "here", "example", d.repoDir, images.Commit, &rec.ExampleRun, "--cli", d.cliBin()); err != nil {
		return rec, err
	}
	if !d.dry {
		sparkwing.Summary(ctx, fmt.Sprintf("%s full example scan `%s` for deploy `%s`, commit `%s`, controller `%s`, runner `%s`. Promotion does not wait for this scan.",
			env.Name, rec.ExampleRun, rec.DeployRun, images.Commit, images.Controller.Digest, images.Runner.Digest))
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
		path, err := d.tools.lookPath("bitwing")
		if err != nil {
			return fmt.Errorf("scan feedback failed; bitwing must be on the runner user's PATH: %w", err)
		}
		_, err = d.tools.run(feedback, deployCommand{Name: path, Args: args})
		if err != nil {
			return fmt.Errorf("scan feedback failed; check the runner user's Bitwing ledger access: %w", err)
		}
		return nil
	})
}

func (d *deployer) submitScan(ctx context.Context, env deployEnv, images deployImages) (string, error) {
	rec := deployScanRecord{Environment: env.Name, DeployRun: d.runID, Images: images}
	body, err := json.Marshal(rec)
	if err != nil {
		return "", err
	}
	var runID string
	err = d.act(ctx, "submit detached "+env.Name+" deploy-scan at "+images.Commit, func() error {
		var err error
		runID, err = d.tools.startChild(ctx, "deploy-scan", map[string]string{"environment": env.Name, "record": string(body)})
		if err != nil {
			return err
		}
		if runID == "" {
			return errors.New("child start returned no scan run ID")
		}
		link := "https://console.sparkwing.dev/runs?run=" + url.QueryEscape(runID)
		sparkwing.Summary(ctx, fmt.Sprintf("%s post-deploy scan: [%s](%s)\n\nDeploy `%s`, commit `%s`, controller `%s`, runner `%s`. Submitted without waiting; failures appear on the linked scan run and in Bitwing.",
			env.Name, runID, link, d.runID, images.Commit, images.Controller.Digest, images.Runner.Digest))
		d.tools.say(ctx, "detached deploy-scan: "+link)
		return nil
	})
	return runID, err
}

func init() {
	sparkwing.Register("DeployScan", func() sparkwing.Pipeline[DeployScanArgs] { return &DeployScan{} })
}
