package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
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
	return "Started as a detached child after production smoke, inheriting its verified repository and revision. All scans share one concurrency slot, leaving the desktop runner's other slot for deploys. Only full scans marked for the same controller supply an hourly revision receipt, suppressing repeated scans even after failure. Superseded revisions stop before scanning. The full example and its children run on self-hosted runners labeled deploy-kikd-prod. Bitwing uses the runner user's PATH and existing ledger configuration. The source commit must match the supplied image identity. Reads DEPLOY_SCAN_STAGING_TOKEN or DEPLOY_SCAN_PRODUCTION_TOKEN from scoped Sparkwing secrets; it never assumes the deploy AWS role. Each failed example job upserts a finding. Bitwing promotes sustained findings to tickets, and later failures append run links to the linked ticket. Clean observations resolve unpromoted findings. Unavailable observations preserve earlier findings."
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
		plan.Concurrency(sparkwing.NewConcurrencyGroup("sparkwing-post-deploy", sparkwing.ConcurrencyLimit{Capacity: 1}))
		labels = []string{"deploy-kikd-prod"}
	}
	sourceCommit := ""
	if rc.Git != nil {
		sourceCommit = rc.Git.SHA
	}
	sparkwing.Job(plan, "scan", &deployScanJob{
		Job:         deployJob{Cfg: cfg, Dry: dry, RunID: rc.RunID, Name: "scan"},
		Environment: env, Record: in.Record, SourceCommit: sourceCommit,
	}).Inline().Requires(labels...).StartsChildren("example").Timeout(cfg.RunTimeout + 15*time.Minute)
	return nil
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
	coalesced := false
	defer func() {
		if coalesced {
			return
		}
		err = errors.Join(err, d.recordScanFinding(ctx, rec, completed, err))
		if !d.dry {
			sparkwing.Summary(ctx, fmt.Sprintf("%s post-deploy scan\n\nDeploy `%s`, commit `%s`, example `%s`\n\nController `%s`, runner `%s`\n\nElapsed: %s. Result: %v",
				env.Name, rec.DeployRun, images.Commit, rec.ExampleRun, images.Controller.Digest, images.Runner.Digest, d.tools.now().Sub(started), err))
		}
	}()
	if err = d.requireMainTip(ctx, images.Commit); err != nil {
		return rec, err
	}
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
	var priorFinished bool
	var priorStatus string
	if err = d.act(ctx, "run full example scan on our hardware", func() error {
		out, err := d.sw(ctx, "", "runs", "list", "--profile", profile, "--pipeline", "example", "--repo", "sparkwing-dev/sparkwing", "--sha", images.Commit, "--limit", "1", "-o", "json")
		if err != nil {
			return err
		}
		var prior struct {
			ID         string    `json:"id"`
			Kind       string    `json:"kind"`
			FinishedAt time.Time `json:"finished_at"`
		}
		if err := json.NewDecoder(strings.NewReader(out)).Decode(&prior); err != nil {
			return fmt.Errorf("decode previous example scan: %w", err)
		}
		if prior.ID == "" && prior.Kind != "page" {
			return errors.New("previous example scan listing returned no run or page")
		}
		if prior.ID != "" && (prior.FinishedAt.IsZero() || prior.FinishedAt.After(d.tools.now().Add(-time.Hour))) {
			out, err := d.sw(ctx, "", "runs", "status", "--run", prior.ID, "--profile", profile, "--exit-zero", "-o", "json")
			var receipt struct {
				ID, Status string
				Inputs     struct{ Args map[string]string }
			}
			if err == nil && json.Unmarshal([]byte(out), &receipt) == nil && receipt.ID == prior.ID && receipt.Inputs.Args["scan-receipt"] == env.ControllerURL {
				rec.ExampleRun = prior.ID
				coalesced = prior.FinishedAt.IsZero()
				priorFinished = !coalesced
				priorStatus = receipt.Status
				return nil
			}
		}
		rec.ExampleRun, err = d.tools.startChild(ctx, "example", map[string]string{"api-controller": env.ControllerURL, "post-deploy": "true", "scan-receipt": env.ControllerURL})
		if err != nil {
			return err
		}
		if rec.ExampleRun == "" {
			return errors.New("example scan returned no run ID")
		}
		return nil
	}); err != nil {
		return rec, err
	}
	if !d.dry {
		sparkwing.Summary(ctx, fmt.Sprintf("%s full example scan `%s` for deploy `%s`, commit `%s`, controller `%s`, runner `%s`. Promotion does not wait for this scan.",
			env.Name, rec.ExampleRun, rec.DeployRun, images.Commit, images.Controller.Digest, images.Runner.Digest))
	}
	if coalesced {
		d.tools.say(ctx, "coalesced into in-flight example "+rec.ExampleRun+"; findings await its observation")
		return rec, nil
	}
	err = d.act(ctx, "observe full example scan on "+profile, func() error {
		status := priorStatus
		if !priorFinished {
			runCtx, cancel := context.WithTimeout(ctx, d.cfg.RunTimeout)
			defer cancel()
			var err error
			status, err = d.tools.awaitChild(runCtx, rec.ExampleRun)
			if err != nil {
				return err
			}
		}
		switch status {
		case "success":
			completed = true
			return nil
		case "failed", "cancelled", "skipped", "superseded":
			completed = true
			return fmt.Errorf("example run %s ended %s", rec.ExampleRun, status)
		default:
			return fmt.Errorf("example run %s is still %s", rec.ExampleRun, status)
		}
	})
	return rec, err
}

func (d *deployer) recordScanFinding(ctx context.Context, rec deployScanRecord, completed bool, scanErr error) error {
	return d.act(ctx, "record "+rec.Environment+" post-deploy scan result in Bitwing", func() error {
		source := "sparkwing-post-deploy-" + rec.Environment
		args := []string{"finding", "record", "--source", source, "-o", "json"}
		findings := []map[string]string{}
		if scanErr != nil {
			detail, err := json.Marshal(rec)
			if err != nil {
				return err
			}
			finding := map[string]string{
				"fingerprint": source + "/full-example",
				"severity":    "high",
				"title":       rec.Environment + " post-deploy example scan failed",
				"detail":      string(detail) + "\n" + scanErr.Error() + scanRunLinks(rec),
			}
			if completed && rec.ExampleRun != "" {
				enrichment, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
				out, err := d.sw(enrichment, "", "runs", "status", "--run", rec.ExampleRun, "--profile", rec.Environment+"-scan", "--exit-zero", "-o", "json")
				cancel()
				var observation deployScanObservation
				if err == nil {
					err = json.Unmarshal([]byte(out), &observation)
				}
				if err == nil && observation.ID != rec.ExampleRun {
					err = fmt.Errorf("example observation returned %s, want %s", observation.ID, rec.ExampleRun)
				}
				if err != nil {
					finding["detail"] += "\nJob details unavailable: " + err.Error()
					d.tools.say(ctx, "WARNING: failed example job details unavailable; recording the scan failure: "+err.Error())
				} else {
					for _, job := range observation.Jobs {
						if job.Outcome == "failed" {
							findings = append(findings, map[string]string{"fingerprint": source + "/example/" + job.ID, "severity": "high", "title": rec.Environment + " post-deploy example: " + job.ID + " failed", "detail": finding["detail"] + "\n" + job.Error})
						}
					}
				}
			}
			if len(findings) == 0 {
				findings = append(findings, finding)
			}
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
		// safety: optional enrichment and caller cancellation cannot consume the publication budget.
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
		if !completed {
			return nil
		}
		return d.scanTickets(feedback, path, source, findings)
	})
}

func scanRunLinks(rec deployScanRecord) string {
	var links string
	for _, id := range []string{rec.DeployRun, rec.ScanRun, rec.ExampleRun} {
		if id != "" {
			links += "\nhttps://console.sparkwing.dev/runs?run=" + url.QueryEscape(id)
		}
	}
	return links
}

func (d *deployer) scanTickets(ctx context.Context, path, source string, report []map[string]string) error {
	if len(report) == 0 {
		return nil
	}
	out, err := d.tools.run(ctx, deployCommand{Name: path, Args: []string{"finding", "list", "--source", source, "--limit", "0", "-o", "json"}, Quiet: true})
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(strings.NewReader(out))
	for {
		var row struct {
			ID         int64 `json:"id"`
			Promotable bool  `json:"promotable"`
		}
		if err := decoder.Decode(&row); errors.Is(err, io.EOF) {
			return nil
		} else if err != nil {
			return err
		}
		if row.ID == 0 {
			continue
		}
		out, err := d.tools.run(ctx, deployCommand{Name: path, Args: []string{"finding", "show", "--id", fmt.Sprint(row.ID), "-o", "json"}, Quiet: true})
		if err != nil {
			return err
		}
		var finding struct {
			ID          int64  `json:"id"`
			Fingerprint string `json:"fingerprint"`
			Ticket      string `json:"ticket"`
		}
		if err := json.Unmarshal([]byte(out), &finding); err != nil {
			return err
		}
		if finding.ID != row.ID {
			return fmt.Errorf("finding detail returned %d, want %d", finding.ID, row.ID)
		}
		for _, current := range report {
			if finding.ID == 0 || finding.Fingerprint != current["fingerprint"] {
				continue
			}
			if finding.Ticket == "" && !row.Promotable {
				continue
			}
			args := []string{"finding", "promote", "--id", fmt.Sprint(finding.ID), "--repo", "sparkwing", "-o", "json"}
			if finding.Ticket != "" {
				file := filepath.Join(d.tmp, "scan-ticket-comment.txt")
				if err := os.WriteFile(file, []byte(current["detail"]), 0o600); err != nil {
					return err
				}
				args = []string{"ticket", "comment", "--id", finding.Ticket, "--body-file", file, "-o", "json"}
			}
			if _, err := d.tools.run(ctx, deployCommand{Name: path, Args: args}); err != nil {
				return err
			}
		}
	}
}

func (d *deployer) submitScan(ctx context.Context, env deployEnv, images deployImages) (string, error) {
	if env.Name != d.cfg.Production.Name {
		return "", nil
	}
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
