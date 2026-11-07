package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
	"github.com/sparkwing-dev/sparkwing/sparkwing/sparkwingtest"
)

func TestDeployScanBindsRevisionAndReportsObservedOutcome(t *testing.T) {
	for _, envName := range []string{"staging", "production"} {
		for _, status := range []string{"success", "failed", "cancelled", "running", "unreachable"} {
			t.Run(envName+"/"+status, func(t *testing.T) {
				f := newFakeDeploy(t)
				d := f.deployer(false)
				d.tools.oidc = func(context.Context, string) (string, error) {
					t.Fatal("detached scans must not assume the deploy role")
					return "", nil
				}
				env := d.cfg.Staging
				if envName == "production" {
					env = d.cfg.Production
				}
				var report []map[string]string
				var findingArgs []string
				d.tools.run = func(ctx context.Context, c deployCommand) (string, error) {
					joined := strings.Join(c.Args, " ")
					if filepath.Base(c.Name) == "bitwing" {
						findingArgs = slices.Clone(c.Args)
						if index := slices.Index(c.Args, "--file"); index >= 0 {
							raw, err := os.ReadFile(c.Args[index+1])
							if err != nil {
								t.Fatal(err)
							}
							if err := json.Unmarshal(raw, &report); err != nil {
								t.Fatal(err)
							}
						}
						return "{}", nil
					}
					if filepath.Base(c.Name) == "sparkwing" && strings.HasPrefix(joined, "runs status") {
						if status == "unreachable" {
							return "", errors.New("controller unavailable")
						}
						if !strings.Contains(joined, "--exit-zero") {
							t.Fatal("scan must distinguish an observed failure from failed observation")
						}
						return fmt.Sprintf(`{"id":"run-example-%s-scan","status":%q}`, envName, status), nil
					}
					return f.run(ctx, c)
				}
				rec, err := d.postDeployScan(deployScanContext(t), env, f.candidate(), "run-original-deploy")
				if (err == nil) != (status == "success") {
					t.Fatalf("status %s: error %v", status, err)
				}
				if rec.DeployRun != "run-original-deploy" || rec.ScanRun != d.runID || !rec.Images.sameDigests(f.candidate()) {
					t.Fatalf("scan lost deployment identity: %+v", rec)
				}
				if !slices.ContainsFunc(f.calls, func(call string) bool {
					return strings.Contains(call, "pipeline trigger example") && strings.Contains(call, "--ref "+f.head) && strings.HasSuffix(call, "-- --api-controller "+env.ControllerURL)
				}) {
					t.Fatalf("scan is not revision-bound: %v", f.calls)
				}
				if !slices.Contains(findingArgs, "sparkwing-post-deploy-"+envName) {
					t.Fatalf("finding source: %v", findingArgs)
				}
				switch status {
				case "success":
					if report == nil || len(report) != 0 {
						t.Fatalf("success must resolve the source: %v", report)
					}
				case "failed", "cancelled":
					if len(report) != 1 || report[0]["fingerprint"] != "full-example" || !strings.Contains(report[0]["detail"], rec.ExampleRun) || !strings.Contains(report[0]["detail"], f.head) || !strings.Contains(report[0]["detail"], rec.Images.Controller.Digest) || !strings.Contains(report[0]["detail"], rec.Images.Runner.Digest) {
						t.Fatalf("failure lost fingerprint or evidence: %v", report)
					}
				default:
					if !slices.Contains(findingArgs, "--could-not-run") || slices.Contains(findingArgs, "--file") {
						t.Fatalf("blind observation must preserve previous findings: %v", findingArgs)
					}
				}
			})
		}
	}
}

func TestDeployScanFeedbackFailureFailsTheJob(t *testing.T) {
	f := newFakeDeploy(t)
	d := f.deployer(false)
	d.tools.run = func(ctx context.Context, c deployCommand) (string, error) {
		if filepath.Base(c.Name) == "bitwing" {
			return "", errors.New("ledger unavailable")
		}
		if filepath.Base(c.Name) == "sparkwing" && len(c.Args) > 1 && c.Args[0] == "runs" {
			return `{"id":"run-example-staging-scan","status":"failed"}`, nil
		}
		return f.run(ctx, c)
	}
	_, err := d.postDeployScan(deployScanContext(t), d.cfg.Staging, f.candidate(), "run-original-deploy")
	if err == nil || !strings.Contains(err.Error(), "ended failed") || !strings.Contains(err.Error(), "ledger unavailable") {
		t.Fatalf("scan or feedback failure was lost: %v", err)
	}
}

func TestDeployScanDryRunHasNoEffects(t *testing.T) {
	f := newFakeDeploy(t)
	d := f.deployer(true)
	d.tools.run = func(_ context.Context, c deployCommand) (string, error) {
		t.Fatalf("dry-run executed %s", c)
		return "", nil
	}
	if _, err := d.postDeployScan(deployScanContext(t), d.cfg.Staging, f.candidate(), "run-original-deploy"); err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(f.said, func(line string) bool {
		return strings.Contains(line, "record staging post-deploy scan result in Bitwing")
	}) {
		t.Fatalf("dry-run omitted feedback: %v", f.said)
	}
}

func TestDeployRefreshPrecedesEachRolloutAndRefusesMissingPermission(t *testing.T) {
	f := newFakeDeploy(t)
	d := f.deployer(false)
	_, _, err := d.deployTo(t.Context(), d.cfg.Staging, f.candidate())
	if err != nil {
		t.Fatal(err)
	}
	refreshes := 0
	for index, call := range f.calls {
		if !strings.Contains(call, "annotate application") {
			continue
		}
		refreshes++
		if !strings.Contains(call, "argocd.argoproj.io/refresh=hard --overwrite") || index+1 >= len(f.calls) || !strings.Contains(f.calls[index+1], "get application") {
			t.Fatalf("refresh did not precede Argo observation: %v", f.calls)
		}
	}
	if refreshes != 2 {
		t.Fatalf("got %d refreshes, want both ordered phases", refreshes)
	}
	d.tools.run = func(_ context.Context, c deployCommand) (string, error) {
		return "", errors.New("Forbidden")
	}
	if err := d.waitRollout(t.Context(), d.cfg.Staging, f.head, deployPhases[0], f.candidate().Controller.Digest); err == nil || !strings.Contains(err.Error(), "patch permission") {
		t.Fatalf("missing refresh permission was hidden: %v", err)
	}
}

func TestDeploySmokeAssertsOutputsAndDeclaresChild(t *testing.T) {
	childCalls := 0
	host := newTestHost(t, sparkwingtest.Options{
		Outputs: map[string]json.RawMessage{"greet": []byte(`"example-output-v1"`)},
		RunPipeline: func(_ context.Context, pipeline string, _ map[string]string) (json.RawMessage, error) {
			childCalls++
			if pipeline != "weather-report" {
				t.Fatalf("unexpected child %q", pipeline)
			}
			return json.Marshal(WeatherOut{Forecast: "sunny", TempF: 65, At: time.Unix(1, 0)})
		},
	})
	plan := sparkwing.NewPlan()
	if err := (DeploySmoke{}).Plan(t.Context(), plan, DeploySmokeInputs{Child: true}, sparkwing.RunContext{}); err != nil {
		t.Fatal(err)
	}
	for _, step := range []string{"predecessor", "assert"} {
		if _, err := host.RunStep(t.Context(), plan, "after", step); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := host.RunStep(t.Context(), plan, "child", "run"); err != nil {
		t.Fatal(err)
	}
	if childCalls != 1 {
		t.Fatalf("got %d child runs, want one", childCalls)
	}
	doc, err := host.Plan(t.Context(), "DeploySmoke", map[string]string{"child": "true"})
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Jobs) != 3 {
		t.Fatalf("smoke jobs: %+v", doc.Jobs)
	}
	for _, job := range doc.Jobs {
		if job.ID == "greet" && job.Inline {
			t.Fatal("greet must exercise a Cloud work job")
		}
		if job.ID == "child" && (len(job.Children) != 1 || job.Children[0].Pipeline != "weather-report") {
			t.Fatalf("child declaration: %+v", job.Children)
		}
	}
}

func TestDeployScanCannotStartPreservesFindings(t *testing.T) {
	f := newFakeDeploy(t)
	d := f.deployer(false)
	var args []string
	d.tools.run = func(_ context.Context, c deployCommand) (string, error) {
		if filepath.Base(c.Name) == "bitwing" {
			args = c.Args
		} else if filepath.Base(c.Name) != "go" {
			t.Fatalf("scan with no fixture token executed %s", c)
		}
		return "{}", nil
	}
	host := newTestHost(t, sparkwingtest.Options{})
	if _, err := d.postDeployScan(host.Context(t.Context()), d.cfg.Staging, f.candidate(), "run-original-deploy"); err == nil || !slices.Contains(args, "--could-not-run") || slices.Contains(args, "--file") {
		t.Fatalf("failed setup must not clear earlier findings: args %v, error %v", args, err)
	}
}

func TestDeployScanFeedbackOutlivesCancellation(t *testing.T) {
	f := newFakeDeploy(t)
	d := f.deployer(false)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	called := false
	d.tools.run = func(ctx context.Context, c deployCommand) (string, error) {
		called = true
		if ctx.Err() != nil || filepath.Base(c.Name) != "bitwing" {
			t.Fatalf("feedback inherited cancellation: command %s, error %v", c, ctx.Err())
		}
		return "{}", nil
	}
	if err := d.recordScanFinding(ctx, deployScanRecord{Environment: "staging"}, true, errors.New("scan failed")); err != nil || !called {
		t.Fatalf("failure feedback: called %t, error %v", called, err)
	}
}

func deployScanContext(t *testing.T) context.Context {
	t.Helper()
	host := newTestHost(t, sparkwingtest.Options{Secrets: map[string]sparkwingtest.Secret{
		"DEPLOY_SCAN_STAGING_TOKEN":    {Value: "staging-fixture-token", Masked: true},
		"DEPLOY_SCAN_PRODUCTION_TOKEN": {Value: "production-fixture-token", Masked: true},
	}})
	return host.Context(t.Context())
}

func TestDeployScanPipelineSeparatesConcurrencyAndPinsSource(t *testing.T) {
	f := newFakeDeploy(t)
	host := newTestHost(t, sparkwingtest.Options{Run: sparkwing.RunContext{
		Git: sparkwing.NewGit(f.repo, f.head, "main", "main", "sparkwing-dev/sparkwing", "https://github.com/sparkwing-dev/sparkwing"),
	}})
	for _, environment := range []string{"staging", "production"} {
		rec := deployScanRecord{Environment: environment, DeployRun: "run-original-deploy", Images: f.candidate()}
		body, err := json.Marshal(rec)
		if err != nil {
			t.Fatal(err)
		}
		doc, err := host.Plan(t.Context(), "DeployScan", map[string]string{"environment": environment, "record": string(body)})
		if err != nil {
			t.Fatal(err)
		}
		if len(doc.Jobs) != 1 || doc.Jobs[0].ID != "scan" || !slices.Equal(doc.Jobs[0].Requires, []string{"deploy-kikd-prod"}) || len(doc.Concurrency) != 1 || doc.Concurrency[0].Group != "sparkwing-post-deploy-"+environment {
			t.Fatalf("scan must use its own concurrency group: %+v", doc)
		}
		rec.Images.Commit = strings.Repeat("a", 40)
		body, err = json.Marshal(rec)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := decodeScanRecord(string(body), environment, f.head, false); err == nil || !strings.Contains(err.Error(), "source must match") {
			t.Fatalf("accepted scan from another revision: %v", err)
		}
	}
	deploy, err := host.Plan(t.Context(), "Deploy", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(deploy.Concurrency) != 1 || deploy.Concurrency[0].Group != "sparkwing-cloud-deploy" || len(deploy.Jobs) != 4 {
		t.Fatalf("deploy must have its own slot and no scan jobs: %+v", deploy)
	}
}

func TestDeploySubmitsScanWithoutAwaitingAndLinksItsReceipt(t *testing.T) {
	f := newFakeDeploy(t)
	d := f.deployer(false)
	host := newTestHost(t, sparkwingtest.Options{})
	ctx := host.Context(t.Context())
	if err := d.assumeRole(ctx); err != nil {
		t.Fatal(err)
	}
	if err := d.buildCLI(ctx); err != nil {
		t.Fatal(err)
	}
	submitted := false
	d.tools.run = func(ctx context.Context, c deployCommand) (string, error) {
		if len(c.Args) > 2 && c.Args[0] == "pipeline" && c.Args[2] == "deploy-scan" {
			submitted = true
			var rec deployScanRecord
			if i := slices.Index(c.Args, "--record"); i < 0 {
				t.Fatal("submission lost deployment identity")
			} else if err := json.Unmarshal([]byte(c.Args[i+1]), &rec); err != nil {
				t.Fatal(err)
			}
			if rec.DeployRun != d.runID || rec.Images.Commit != f.head || !rec.Images.sameDigests(f.candidate()) || !slices.Contains(c.Args, "--detach") || !slices.Contains(c.Args, "label=deploy-kikd-prod") || !slices.Contains(c.Args, f.head) {
				t.Fatalf("unbound or attached submission: %+v, %v", rec, c.Args)
			}
			return `{"run_id":"run-detached-scan","status":"queued"}`, nil
		}
		if len(c.Args) > 0 && c.Args[0] == "runs" {
			t.Fatal("deploy must not await scan completion")
		}
		return f.run(ctx, c)
	}
	var run string
	plan := sparkwing.NewPlan()
	sparkwing.Job(plan, "submit", func(ctx context.Context) error {
		var err error
		run, err = d.submitScan(ctx, d.cfg.Staging, f.candidate())
		return err
	})
	_, err := host.RunStep(t.Context(), plan, "submit", "run")
	if err != nil || run != "run-detached-scan" || !submitted {
		t.Fatalf("detached submission = %q, %v", run, err)
	}
	if !slices.ContainsFunc(host.Records(), func(record sparkwing.LogRecord) bool {
		return strings.Contains(record.Msg, "[run-detached-scan](https://console.sparkwing.dev/runs?run=run-detached-scan)")
	}) {
		t.Fatalf("deploy summary did not link its scan: %+v", host.Records())
	}
}

func TestDeployScanRejectsIncompleteIdentity(t *testing.T) {
	f := newFakeDeploy(t)
	for _, mutate := range []func(*deployScanRecord){
		func(rec *deployScanRecord) { rec.Environment = "unknown" },
		func(rec *deployScanRecord) { rec.DeployRun = "" },
		func(rec *deployScanRecord) { rec.Images.Commit = "main" },
		func(rec *deployScanRecord) { rec.Images.Controller.Digest = "" },
		func(rec *deployScanRecord) { rec.Images.Runner.Digest = "" },
	} {
		rec := deployScanRecord{Environment: "staging", DeployRun: "run-original-deploy", Images: f.candidate()}
		mutate(&rec)
		body, err := json.Marshal(rec)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := decodeScanRecord(string(body), "staging", f.head, false); err == nil {
			t.Fatalf("accepted incomplete identity: %s", body)
		}
	}
}

func TestDeployScanFeedbackUsesTheRunnerUsersInstallAndLedger(t *testing.T) {
	f := newFakeDeploy(t)
	d := f.deployer(false)
	d.cfg.ToolDir = filepath.Join(t.TempDir(), "absent-deploy-toolchain")
	userBinary := filepath.Join(t.TempDir(), "user-bin", "bitwing")
	lookedUp := false
	d.tools.lookPath = func(name string) (string, error) {
		if name != "bitwing" {
			t.Fatalf("looked up %q instead of bitwing", name)
		}
		lookedUp = true
		return userBinary, nil
	}
	called := false
	d.tools.run = func(_ context.Context, c deployCommand) (string, error) {
		called = true
		if c.Name != userBinary || len(c.Env) != 0 {
			t.Fatalf("feedback must use the user installation and inherit its environment: %+v", c)
		}
		return "{}", nil
	}
	if err := d.recordScanFinding(t.Context(), deployScanRecord{Environment: "staging"}, true, errors.New("scan failed")); err != nil || !called || !lookedUp {
		t.Fatalf("user feedback failed: called %t, looked up %t, error %v", called, lookedUp, err)
	}
}

func TestDeployScanFeedbackReportsMissingUserInstall(t *testing.T) {
	f := newFakeDeploy(t)
	d := f.deployer(false)
	d.tools.lookPath = func(string) (string, error) { return "", errors.New("not found") }
	d.tools.run = func(_ context.Context, c deployCommand) (string, error) {
		t.Fatalf("executed a missing installation: %s", c)
		return "", nil
	}
	if err := d.recordScanFinding(t.Context(), deployScanRecord{Environment: "staging"}, true, errors.New("scan failed")); err == nil || !strings.Contains(err.Error(), "runner user's PATH") {
		t.Fatalf("missing installation was hidden: %v", err)
	}
}
