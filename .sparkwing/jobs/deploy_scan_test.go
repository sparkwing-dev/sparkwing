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
				rec, err := d.postDeployScan(t.Context(), env, f.candidate())
				if (err == nil) != (status == "success") {
					t.Fatalf("status %s: error %v", status, err)
				}
				if rec.DeployRun != d.runID || !rec.Images.sameDigests(f.candidate()) {
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
	_, err := d.postDeployScan(t.Context(), d.cfg.Staging, f.candidate())
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
	if _, err := d.postDeployScan(t.Context(), d.cfg.Staging, f.candidate()); err != nil {
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
	f.oidcDown = true
	d := f.deployer(false)
	var args []string
	d.tools.run = func(_ context.Context, c deployCommand) (string, error) {
		if filepath.Base(c.Name) != "bitwing" {
			t.Fatalf("scan with no credentials executed %s", c)
		}
		args = c.Args
		return "{}", nil
	}
	if _, err := d.postDeployScan(t.Context(), d.cfg.Staging, f.candidate()); err == nil || !slices.Contains(args, "--could-not-run") || slices.Contains(args, "--file") {
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
