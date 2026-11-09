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
	"github.com/sparkwing-dev/sparkwing/sparkwingtest"
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
					if filepath.Base(c.Name) == "bitwing" && len(c.Args) > 1 && c.Args[1] == "record" {
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
						return fmt.Sprintf(`{"id":"run-example-%s-scan","status":%q}`, envName, status), nil
					}
					return f.run(ctx, c)
				}
				d.tools.awaitChild = func(context.Context, string) (string, error) {
					if status == "unreachable" {
						return "", errors.New("controller unavailable")
					}
					return status, nil
				}
				rec, err := d.postDeployScan(deployScanContext(t), env, f.candidate(), "run-original-deploy")
				if (err == nil) != (status == "success") {
					t.Fatalf("status %s: error %v", status, err)
				}
				if rec.DeployRun != "run-original-deploy" || rec.ScanRun != d.runID || !rec.Images.sameDigests(f.candidate()) {
					t.Fatalf("scan lost deployment identity: %+v", rec)
				}
				if !slices.ContainsFunc(f.calls, func(call string) bool {
					return strings.HasPrefix(call, "child start example ") && strings.Contains(call, env.ControllerURL) && strings.Contains(call, `"post-deploy":"true"`)
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
					if len(report) != 1 || report[0]["fingerprint"] != "sparkwing-post-deploy-"+envName+"/full-example" || !strings.Contains(report[0]["detail"], rec.ExampleRun) || !strings.Contains(report[0]["detail"], f.head) || !strings.Contains(report[0]["detail"], rec.Images.Controller.Digest) || !strings.Contains(report[0]["detail"], rec.Images.Runner.Digest) {
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
		if filepath.Base(c.Name) == "sparkwing" && len(c.Args) > 1 && c.Args[0] == "runs" && c.Args[1] == "status" {
			return `{"id":"run-example-staging-scan","status":"failed"}`, nil
		}
		return f.run(ctx, c)
	}
	d.tools.awaitChild = func(context.Context, string) (string, error) { return "failed", nil }
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
	if err := d.waitRollout(t.Context(), d.cfg.Staging, f.head, deployPhases[0], f.candidate().Controller.Digest, nil); err == nil || !strings.Contains(err.Error(), "patch permission") {
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
	d.tools.run = func(ctx context.Context, c deployCommand) (string, error) {
		if filepath.Base(c.Name) == "git" {
			return f.run(ctx, c)
		}
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
		if len(doc.Jobs) != 1 || !doc.Jobs[0].Inline || !slices.Equal(doc.Jobs[0].Requires, []string{"deploy-kikd-prod"}) || len(doc.Concurrency) != 1 || doc.Concurrency[0].Group != "sparkwing-post-deploy" || *doc.Concurrency[0].Capacity != 1 {
			t.Fatalf("scans must share one slot across environments: %+v", doc)
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
	for _, job := range deploy.Jobs {
		if (job.ID == "staging-gate" || job.ID == "production") && (len(job.Children) != 1 || job.Children[0].Pipeline != "deploy-scan") {
			t.Fatalf("deploy job %s must declare scan children: %+v", job.ID, job.Children)
		}
		if len(job.Concurrency) != 1 || strings.HasPrefix(job.Concurrency[0].Group, "sparkwing-post-deploy") {
			t.Fatalf("deploy stage %s shares scan capacity: %+v", job.ID, job.Concurrency)
		}
	}
	if len(deploy.Concurrency) != 0 || len(deploy.Jobs) != 3 {
		t.Fatalf("deploy must pipeline its stages without scan jobs: %+v", deploy)
	}
}

func TestDeploySubmitsScanWithoutAwaitingAndLinksItsReceipt(t *testing.T) {
	f := newFakeDeploy(t)
	d := f.deployer(false)
	host := newTestHost(t, sparkwingtest.Options{StartChild: func(_ context.Context, pipeline string, args map[string]string) (string, error) {
		var rec deployScanRecord
		if err := json.Unmarshal([]byte(args["record"]), &rec); err != nil {
			t.Fatal(err)
		}
		if pipeline != "deploy-scan" || rec.DeployRun != d.runID || rec.Images.Commit != f.head || !rec.Images.sameDigests(f.candidate()) || args["environment"] != "production" {
			t.Fatalf("unbound submission: %s, %+v, %v", pipeline, rec, args)
		}
		return "run-detached-scan", nil
	}})
	d.tools.startChild = sparkwing.StartChild
	d.tools.run = func(_ context.Context, c deployCommand) (string, error) {
		t.Fatalf("child submission must not use a token or CLI: %s", c)
		return "", nil
	}
	var run string
	plan := sparkwing.NewPlan()
	sparkwing.Job(plan, "submit", func(ctx context.Context) error {
		var err error
		run, err = d.submitScan(ctx, d.cfg.Production, f.candidate())
		return err
	}).StartsChildren("deploy-scan")
	_, err := host.RunStep(t.Context(), plan, "submit", "run")
	if err != nil || run != "run-detached-scan" {
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

func TestDeployScanRunsExampleOnItsOwnHardware(t *testing.T) {
	host := newTestHost(t, sparkwingtest.Options{})
	doc, err := host.Plan(t.Context(), "Example", map[string]string{"post-deploy": "true", "api-controller": defaultDeployConfig().Production.ControllerURL})
	if err != nil {
		t.Fatal(err)
	}
	for _, job := range doc.Jobs {
		if job.Approval != nil {
			continue
		}
		if !slices.Equal(job.Requires, []string{"deploy-kikd-prod"}) || len(job.Concurrency) != 1 || job.Concurrency[0].Group != "sparkwing-post-deploy-work" || *job.Concurrency[0].Capacity != 1 {
			t.Errorf("scan job %s can consume a second slot or leave our hardware: %+v", job.ID, job)
		}
	}
}

func TestDeployScanReusesRunningAndRecentFailedExamples(t *testing.T) {
	for _, status := range []string{"running", "failed", "success", "old"} {
		t.Run(status, func(t *testing.T) {
			f := newFakeDeploy(t)
			d := f.deployer(false)
			finished := d.tools.now().Add(-time.Minute)
			if status == "running" {
				finished = time.Time{}
			} else if status == "old" {
				finished = d.tools.now().Add(-2 * time.Hour)
			}
			d.tools.run = func(ctx context.Context, c deployCommand) (string, error) {
				if filepath.Base(c.Name) == "sparkwing" && len(c.Args) > 1 && c.Args[0] == "runs" {
					if c.Args[1] == "list" {
						if !strings.Contains(strings.Join(c.Args, " "), "--pipeline example --repo sparkwing-dev/sparkwing --sha "+f.head+" --limit 1") {
							t.Fatalf("hourly receipt lost its repository or revision: %s", c)
						}
						return fmt.Sprintf(`{"id":"run-existing","status":%q,"finished_at":%q}`, status, finished.Format(time.RFC3339Nano)), nil
					}
					outcome := "success"
					if status == "failed" {
						outcome = "failed"
					}
					if outcome == "failed" && !slices.Contains(c.Args, "--exit-zero") {
						return "", errors.New("completed run failed")
					}
					return fmt.Sprintf(`{"id":"%s","status":%q,"inputs":{"proof_eligible":true,"args":{"post-deploy":"true","api-controller":%q}}}`, c.Args[3], outcome, d.cfg.Production.ControllerURL), nil
				}
				return f.run(ctx, c)
			}
			if status != "old" {
				d.tools.startChild = func(context.Context, string, map[string]string) (string, error) {
					t.Fatal("repeated scan started another full example")
					return "", nil
				}
				d.tools.awaitChild = func(context.Context, string) (string, error) {
					t.Fatal("coalesced scan waited with another slot")
					return "", nil
				}
			}
			rec, err := d.postDeployScan(deployScanContext(t), d.cfg.Production, f.candidate(), "run-deploy")
			if status == "failed" && (err == nil || !strings.Contains(err.Error(), "ended failed")) {
				t.Fatalf("lost completed failure outcome: %v", err)
			}
			if (err != nil) != (status == "failed") || (rec.ExampleRun == "run-existing") != (status != "old") {
				t.Fatalf("reused example = %q, %v", rec.ExampleRun, err)
			}
		})
	}
}

func TestDeployScanSupersededRevisionStopsBeforeBuild(t *testing.T) {
	f := newFakeDeploy(t)
	images := f.candidate()
	deployGitIn(t, f.repo, "commit", "--quiet", "--allow-empty", "-m", "newer source")
	deployGitIn(t, f.repo, "push", "--quiet", "origin", "HEAD:main")
	d := f.deployer(false)
	if _, err := d.postDeployScan(deployScanContext(t), d.cfg.Production, images, "run-deploy"); err == nil || !strings.Contains(err.Error(), "only main's tip") {
		t.Fatalf("superseded scan = %v", err)
	}
	for _, call := range f.calls {
		if strings.Contains(call, "pipeline trigger") || strings.HasPrefix(call, "go ") {
			t.Fatalf("superseded scan did expensive work: %s", call)
		}
	}
}

func TestDeployStagingLeavesTheFullScanForProduction(t *testing.T) {
	f := newFakeDeploy(t)
	d := f.deployer(false)
	d.tools.startChild = func(context.Context, string, map[string]string) (string, error) {
		t.Fatal("staging submitted a second full scan")
		return "", nil
	}
	if run, err := d.submitScan(t.Context(), d.cfg.Staging, f.candidate()); err != nil || run != "" {
		t.Fatalf("staging scan = %q, %v", run, err)
	}
}

func TestDeployScanUsesNativeChildSubmissionAndAwait(t *testing.T) {
	f := newFakeDeploy(t)
	d := f.deployer(false)
	started, awaited := false, false
	host := newTestHost(t, sparkwingtest.Options{
		Secrets: map[string]sparkwingtest.Secret{"DEPLOY_SCAN_PRODUCTION_TOKEN": {Value: "fixture-token", Masked: true}},
		StartChild: func(_ context.Context, pipeline string, args map[string]string) (string, error) {
			started = true
			if pipeline != "example" || args["post-deploy"] != "true" || args["api-controller"] != d.cfg.Production.ControllerURL {
				t.Fatalf("scan child request = %s %v", pipeline, args)
			}
			return "native-example", nil
		},
		AwaitChild: func(_ context.Context, run string) (string, error) {
			awaited = true
			if !started || run != "native-example" {
				t.Fatalf("await = %q, started = %v", run, started)
			}
			return "success", nil
		},
	})
	d.tools.startChild, d.tools.awaitChild = sparkwing.StartChild, sparkwing.AwaitChild
	plan := sparkwing.NewPlan()
	sparkwing.Job(plan, "scan", func(ctx context.Context) error {
		rec, err := d.postDeployScan(ctx, d.cfg.Production, f.candidate(), "deploy")
		if err == nil && rec.ExampleRun != "native-example" {
			t.Errorf("scan run = %q", rec.ExampleRun)
		}
		return err
	}).StartsChildren("example")
	_, err := host.RunStep(t.Context(), plan, "scan", "run")
	if err != nil || !started || !awaited {
		t.Fatalf("native scan: started=%v awaited=%v error=%v", started, awaited, err)
	}
}

func TestDeployScanDeclaresNativeExampleChild(t *testing.T) {
	f := newFakeDeploy(t)
	rec, err := json.Marshal(deployScanRecord{Environment: "production", DeployRun: "deploy", Images: f.candidate()})
	if err != nil {
		t.Fatal(err)
	}
	host := newTestHost(t, sparkwingtest.Options{})
	doc, err := host.Plan(t.Context(), "DeployScan", map[string]string{"environment": "production", "record": string(rec)})
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Jobs[0].Children) != 1 || doc.Jobs[0].Children[0].Pipeline != "example" {
		t.Fatalf("scan must declare its native child: %+v", doc.Jobs[0])
	}
}

func TestDeployScanSerializesGeneratedWorkAndOwnedProbeJobs(t *testing.T) {
	host := newTestHost(t, sparkwingtest.Options{Outputs: map[string]json.RawMessage{"output-source": []byte(`"example-output-v1"`)}})
	plan := sparkwing.NewPlan()
	if err := (&Example{}).Plan(t.Context(), plan, ExampleInputs{PostDeploy: true, APIController: defaultDeployConfig().Production.ControllerURL}, sparkwing.RunContext{}); err != nil {
		t.Fatal(err)
	}
	doc, err := host.Plan(t.Context(), "Example", map[string]string{"post-deploy": "true", "api-controller": defaultDeployConfig().Production.ControllerURL})
	if err != nil {
		t.Fatal(err)
	}
	for _, job := range doc.Jobs {
		if job.ID != "output-source" {
			continue
		}
		raw, err := host.Eval(t.Context(), plan, job.ID, job.Closures.Generate[0], []byte(`"example-output-v1"`))
		if err != nil {
			t.Fatal(err)
		}
		var expanded struct{ Jobs []jobDocument }
		if err := json.Unmarshal(raw, &expanded); err != nil {
			t.Fatal(err)
		}
		if len(expanded.Jobs) != 1 || len(expanded.Jobs[0].Concurrency) != 1 || expanded.Jobs[0].Concurrency[0].Group != "sparkwing-post-deploy-work" || !slices.Equal(expanded.Jobs[0].Requires, []string{"deploy-kikd-prod"}) {
			t.Fatalf("generated scan escaped admission: %s", raw)
		}
	}
	probe, err := host.Plan(t.Context(), "ExampleProbe", map[string]string{"token": "scan", "post-deploy": "true"})
	if err != nil {
		t.Fatal(err)
	}
	for _, job := range probe.Jobs {
		if !slices.Equal(job.Requires, []string{"deploy-kikd-prod"}) || len(job.Concurrency) != 1 || job.Concurrency[0].Group != "sparkwing-post-deploy-probes" || *job.Concurrency[0].Capacity != 1 {
			t.Errorf("probe job escaped scan capacity: %+v", job)
		}
	}
	weather, err := host.Plan(t.Context(), "WeatherReport", map[string]string{"post-deploy": "true"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(weather.Jobs[0].Requires, []string{"deploy-kikd-prod"}) || len(weather.Jobs[0].Concurrency) != 0 {
		t.Fatalf("weather child must use owned hardware without taking its parent's group: %+v", weather.Jobs[0])
	}
}

func TestDeployScanLifecycleProbeUsesNativeSubmissionBeforeCleanup(t *testing.T) {
	calls := 0
	host := newTestHost(t, sparkwingtest.Options{StartChild: func(_ context.Context, pipeline string, args map[string]string) (string, error) {
		calls++
		if pipeline != "example-probe" || args["post-deploy"] != "true" || args["token"] != "probe" || args["long"] != "true" {
			t.Fatalf("probe child = %s %v", pipeline, args)
		}
		return "native-probe", nil
	}})
	_, err := exampleStart(host.Context(t.Context()), ExampleInputs{PostDeploy: true, CLI: "true", APIProfile: "scan"}, "probe", "--long")
	if calls != 1 || err == nil || !strings.Contains(err.Error(), "fake host does not execute cleanups") {
		t.Fatalf("native probe calls=%d error=%v", calls, err)
	}
}
