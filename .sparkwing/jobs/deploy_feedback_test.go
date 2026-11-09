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
)

func TestDeployScanCreatesAndUpdatesFailureTickets(t *testing.T) {
	f := newFakeDeploy(t)
	d := f.deployer(false)
	promotions, comments := 0, 0
	var report []map[string]string
	d.tools.run = func(_ context.Context, c deployCommand) (string, error) {
		if filepath.Base(c.Name) == "sparkwing" {
			return `{"id":"example-1","status":"failed","jobs":[{"id":"race","outcome":"failed","error":"race detected"},{"id":"lint","outcome":"failed","error":"bad import"},{"id":"after","outcome":"skipped"}]}`, nil
		}
		switch c.Args[1] {
		case "record":
			raw, err := os.ReadFile(c.Args[slices.Index(c.Args, "--file")+1])
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(raw, &report); err != nil {
				t.Fatal(err)
			}
			return `{}`, nil
		case "list":
			ticket := ""
			if promotions == 2 {
				ticket = "BW-100"
			}
			var out strings.Builder
			for i, finding := range report {
				fmt.Fprintf(&out, "{\"id\":%d,\"fingerprint\":%q,\"ticket\":%q,\"detail\":%q}\n", i+1, finding["fingerprint"], ticket, finding["detail"])
			}
			return out.String() + `{"kind":"summary","truncated":false}`, nil
		case "promote":
			if !slices.Contains(c.Args, "--force") {
				t.Fatal("production failure waited for repeated breaches")
			}
			promotions++
			return `{}`, nil
		case "comment":
			comments++
			raw, err := os.ReadFile(c.Args[slices.Index(c.Args, "--body-file")+1])
			if err != nil || !strings.Contains(string(raw), "scan-2") || !strings.Contains(string(raw), "https://console.sparkwing.dev/runs?run=example-1") {
				t.Fatalf("ticket update lost run links: %s, %v", raw, err)
			}
			return `{}`, nil
		default:
			t.Fatalf("unexpected command: %s", c)
			return "", nil
		}
	}
	for _, scan := range []string{"scan-1", "scan-2"} {
		rec := deployScanRecord{Environment: "production", ScanRun: scan, DeployRun: "deploy-1", ExampleRun: "example-1", Images: f.candidate()}
		if err := d.recordScanFinding(t.Context(), rec, true, errors.New("example failed")); err != nil {
			t.Fatal(err)
		}
	}
	if len(report) != 2 || report[0]["fingerprint"] != "sparkwing-post-deploy-production/example/race" || report[1]["fingerprint"] != "sparkwing-post-deploy-production/example/lint" || promotions != 2 || comments != 2 {
		t.Fatalf("failure deduplication: report=%v promotions=%d comments=%d", report, promotions, comments)
	}
}

func TestDeployFlagsFailedProductionScanBeforeRollout(t *testing.T) {
	f := newFakeDeploy(t)
	d := f.deployer(false)
	flagged := false
	promotions := 0
	d.tools.say = func(_ context.Context, msg string) {
		if strings.Contains(msg, "run-prior-scan") && strings.Contains(msg, "failed") {
			flagged = true
		}
	}
	d.tools.run = func(ctx context.Context, c deployCommand) (string, error) {
		joined := strings.Join(c.Args, " ")
		if filepath.Base(c.Name) == "sparkwing" {
			if strings.HasPrefix(joined, "runs list") && strings.Contains(joined, "deploy-scan") {
				return `{"id":"run-prior-scan","status":"failed"}` + "\n" + `{"kind":"page"}`, nil
			}
			if strings.HasPrefix(joined, "runs status") && strings.Contains(joined, "run-prior-scan") {
				return `{"id":"run-prior-scan","status":"failed","inputs":{"args":{"environment":"production"}},"jobs":[{"id":"scan","outcome":"failed","reason":"integrity_failure","error":"installed engine differs"}]}`, nil
			}
		}
		if filepath.Base(c.Name) == "bitwing" {
			if c.Args[1] == "list" {
				return `{"id":1,"fingerprint":"sparkwing-post-deploy-production-execution/scan/integrity_failure"}` + "\n" + `{"kind":"summary"}`, nil
			}
			if c.Args[1] == "promote" {
				promotions++
			}
		}
		if filepath.Base(c.Name) == "git" && slices.Contains(c.Args, "clone") && strings.Contains(c.Args[len(c.Args)-1], "gitops-production") && (!flagged || promotions != 1) {
			t.Fatal("production pins changed before scan failure was flagged")
		}
		return f.run(ctx, c)
	}
	if _, err := d.promote(t.Context(), f.candidate()); err != nil {
		t.Fatal(err)
	}
	if !flagged || promotions != 1 {
		t.Fatal("failed pre-start scan was invisible to the next production deploy")
	}
}

func TestDeployScanFeedbackReadFailureWarnsAndAllowsRepair(t *testing.T) {
	f := newFakeDeploy(t)
	d := f.deployer(false)
	d.tools.run = func(ctx context.Context, c deployCommand) (string, error) {
		if filepath.Base(c.Name) == "sparkwing" && strings.HasPrefix(strings.Join(c.Args, " "), "runs list") {
			return "", errors.New("controller unavailable")
		}
		return f.run(ctx, c)
	}
	if _, err := d.promote(t.Context(), f.candidate()); err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(f.said, func(message string) bool {
		return strings.Contains(message, "WARNING: production scan feedback unavailable") && strings.Contains(message, "controller unavailable")
	}) {
		t.Fatalf("missing blind-observation warning: %v", f.said)
	}
}

func TestDeployScanFeedbackPagesPastStaging(t *testing.T) {
	f := newFakeDeploy(t)
	d := f.deployer(false)
	readProduction := false
	d.tools.run = func(ctx context.Context, c deployCommand) (string, error) {
		joined := strings.Join(c.Args, " ")
		if filepath.Base(c.Name) == "sparkwing" {
			if strings.HasPrefix(joined, "runs list") {
				if slices.Contains(c.Args, "--cursor") {
					return `{"id":"production-scan"}` + "\n" + `{"kind":"page"}`, nil
				}
				return `{"id":"staging-scan"}` + "\n" + `{"kind":"page","next_cursor":"older"}`, nil
			}
			if strings.HasPrefix(joined, "runs status") {
				environment := "staging"
				if c.Args[slices.Index(c.Args, "--run")+1] == "production-scan" {
					environment = "production"
					readProduction = true
				}
				return fmt.Sprintf(`{"id":%q,"status":"success","inputs":{"args":{"environment":%q}}}`, environment+"-scan", environment), nil
			}
		}
		return f.run(ctx, c)
	}
	if err := d.assumeRole(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := d.flagProductionScan(t.Context()); err != nil || !readProduction {
		t.Fatalf("latest production scan: read=%v error=%v", readProduction, err)
	}
}
