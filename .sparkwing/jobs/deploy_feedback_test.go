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
			var out strings.Builder
			for i := range report {
				fmt.Fprintf(&out, "{\"id\":%d,\"severity\":\"high\",\"status\":\"active\",\"kind\":\"defect\",\"title\":\"scan failed\",\"promotable\":true}\n", i+1)
			}
			return out.String() + `{"kind":"summary","truncated":false}`, nil
		case "show":
			ticket := ""
			if promotions == 2 {
				ticket = "fixture-ticket"
			}
			for i, finding := range report {
				if fmt.Sprint(i+1) == c.Args[slices.Index(c.Args, "--id")+1] {
					return fmt.Sprintf("{\"id\":%d,\"fingerprint\":%q,\"ticket\":%q,\"detail\":%q}", i+1, finding["fingerprint"], ticket, finding["detail"]), nil
				}
			}
			t.Fatalf("unexpected finding detail: %s", c)
			return "", nil
		case "promote":
			if slices.Contains(c.Args, "--force") {
				t.Fatal("automatic promotion bypassed Bitwing policy")
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
				return `{"id":1,"severity":"high","promotable":true}` + "\n" + `{"kind":"summary"}`, nil
			}
			if c.Args[1] == "show" {
				return `{"id":1,"fingerprint":"sparkwing-post-deploy-production-execution/scan/integrity_failure"}`, nil
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

func TestDeployScanPlanningFailureCreatesTicket(t *testing.T) {
	body, err := os.ReadFile("testdata/production_scan_planning_failure.json")
	if err != nil {
		t.Fatal(err)
	}
	var observed deployScanObservation
	if err := json.Unmarshal(body, &observed); err != nil {
		t.Fatal(err)
	}
	if observed.Inputs.RequestedArgs.Environment != "***" {
		t.Fatal("fixture did not exercise real request redaction")
	}

	f := newFakeDeploy(t)
	d := f.deployer(false)
	var report []map[string]string
	promoted := false
	d.tools.run = func(ctx context.Context, c deployCommand) (string, error) {
		if filepath.Base(c.Name) == "sparkwing" {
			if c.Args[1] == "list" {
				return `{"id":"planning-scan"}` + "\n" + `{"kind":"page"}`, nil
			}
			if c.Args[1] == "status" {
				return string(body), nil
			}
		}
		if filepath.Base(c.Name) == "bitwing" {
			switch c.Args[1] {
			case "record":
				body, err := os.ReadFile(c.Args[slices.Index(c.Args, "--file")+1])
				if err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(body, &report); err != nil {
					t.Fatal(err)
				}
			case "list":
				return `{"id":1,"severity":"high","promotable":true}`, nil
			case "show":
				return fmt.Sprintf(`{"id":1,"fingerprint":%q}`, report[0]["fingerprint"]), nil
			case "promote":
				promoted = true
			}
			return `{}`, nil
		}
		return f.run(ctx, c)
	}
	if err := d.assumeRole(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := d.flagProductionScan(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(report) != 1 || !promoted || !strings.Contains(report[0]["detail"], "invalid plan") || !strings.Contains(report[0]["detail"], "parent-deploy") || !slices.ContainsFunc(f.said, func(msg string) bool {
		return strings.Contains(msg, "WARNING:") && strings.Contains(msg, "planning-scan")
	}) {
		t.Fatalf("planning failure disappeared: report=%v promoted=%v messages=%v", report, promoted, f.said)
	}
}

func TestDeployScanKeepsFailureWhenEnrichmentUnavailable(t *testing.T) {
	for _, response := range []string{"unavailable", "invalid-json", `{"id":"wrong-example"}`} {
		t.Run(response, func(t *testing.T) {
			f := newFakeDeploy(t)
			d := f.deployer(false)
			var report []map[string]string
			promoted := false
			d.tools.run = func(_ context.Context, c deployCommand) (string, error) {
				if filepath.Base(c.Name) == "sparkwing" {
					if response == "unavailable" {
						return "", errors.New("controller unavailable")
					}
					return response, nil
				}
				switch c.Args[1] {
				case "record":
					body, err := os.ReadFile(c.Args[slices.Index(c.Args, "--file")+1])
					if err != nil {
						t.Fatal(err)
					}
					if err := json.Unmarshal(body, &report); err != nil {
						t.Fatal(err)
					}
				case "list":
					return `{"id":1,"severity":"high","promotable":true}`, nil
				case "show":
					return `{"id":1,"fingerprint":"sparkwing-post-deploy-production/full-example"}`, nil
				case "promote":
					promoted = true
				}
				return `{}`, nil
			}
			rec := deployScanRecord{Environment: "production", ScanRun: "scan-1", DeployRun: "deploy-1", ExampleRun: "example-1", Images: f.candidate()}
			if err := d.recordScanFinding(t.Context(), rec, true, errors.New("known example failure")); err != nil {
				t.Fatal(err)
			}
			if len(report) != 1 || !promoted || !strings.Contains(report[0]["detail"], "known example failure") || !strings.Contains(report[0]["detail"], "example-1") {
				t.Fatalf("known failure lost to optional enrichment: %v, promoted=%v", report, promoted)
			}
		})
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

func TestDeployScanRespectsFindingPromotionPolicy(t *testing.T) {
	f := newFakeDeploy(t)
	d := f.deployer(false)
	observation, promotions, comments := 0, 0, 0
	report := []map[string]string{{"fingerprint": "failure", "detail": "scan evidence"}}
	d.tools.run = func(_ context.Context, c deployCommand) (string, error) {
		switch c.Args[1] {
		case "list":
			return fmt.Sprintf(`{"id":1,"promotable":%t}`, observation >= 3), nil
		case "show":
			ticket := ""
			if promotions > 0 {
				ticket = "fixture-ticket"
			}
			return fmt.Sprintf(`{"id":1,"fingerprint":"failure","ticket":%q}`, ticket), nil
		case "promote":
			if observation < 3 || slices.Contains(c.Args, "--force") {
				t.Fatal("automatic check bypassed Bitwing sustain policy")
			}
			promotions++
		case "comment":
			comments++
		}
		return `{}`, nil
	}
	for observation = 1; observation <= 4; observation++ {
		if err := d.scanTickets(t.Context(), "bitwing", "source", report); err != nil {
			t.Fatal(err)
		}
	}
	if promotions != 1 || comments != 1 {
		t.Fatalf("promotions=%d comments=%d", promotions, comments)
	}
}

func TestDeployScanKeepsPublicationBudgetAfterEnrichment(t *testing.T) {
	f := newFakeDeploy(t)
	d := f.deployer(false)
	var enrichment context.Context
	recorded := false
	d.tools.run = func(ctx context.Context, c deployCommand) (string, error) {
		if filepath.Base(c.Name) == "sparkwing" {
			enrichment = ctx
			return "", context.DeadlineExceeded
		}
		if c.Args[1] == "record" {
			if enrichment == nil || enrichment.Err() == nil {
				t.Fatal("optional enrichment still owns publication lifetime")
			}
			if ctx.Err() != nil {
				t.Fatalf("enrichment deadline consumed publication budget: %v", ctx.Err())
			}
			early, _ := enrichment.Deadline()
			later, _ := ctx.Deadline()
			if !later.After(early) {
				t.Fatal("publication did not receive its own deadline")
			}
			recorded = true
		}
		return `{}`, nil
	}
	rec := deployScanRecord{Environment: "production", ExampleRun: "example-1"}
	if err := d.recordScanFinding(t.Context(), rec, true, errors.New("known failure")); err != nil {
		t.Fatal(err)
	}
	if !recorded {
		t.Fatal("known failure was not recorded")
	}
}

func TestDeployScanExecutionRecoveryResetsFindings(t *testing.T) {
	f := newFakeDeploy(t)
	d := f.deployer(false)
	status := "failed"
	var sizes []int
	d.tools.run = func(ctx context.Context, c deployCommand) (string, error) {
		if filepath.Base(c.Name) == "sparkwing" && c.Args[0] == "runs" {
			if c.Args[1] == "list" {
				return `{"id":"scan-1"}`, nil
			}
			return fmt.Sprintf(`{"id":"scan-1","status":%q,"inputs":{"args":{"environment":"production"}},"jobs":[{"id":"scan","attempt":0,"outcome":%q,"reason":"integrity_failure"}]}`, status, status), nil
		}
		if filepath.Base(c.Name) == "bitwing" {
			if c.Args[1] == "record" {
				if c.Args[slices.Index(c.Args, "--source")+1] != "sparkwing-post-deploy-production-execution" {
					t.Fatal("wrong execution finding source")
				}
				body, err := os.ReadFile(c.Args[slices.Index(c.Args, "--file")+1])
				if err != nil {
					t.Fatal(err)
				}
				var report []map[string]string
				if err := json.Unmarshal(body, &report); err != nil {
					t.Fatal(err)
				}
				sizes = append(sizes, len(report))
			}
			return "", nil
		}
		return f.run(ctx, c)
	}
	if err := d.assumeRole(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, current := range []string{"failed", "success", "failed", "success", "failed"} {
		status = current
		if err := d.flagProductionScan(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if !slices.Equal(sizes, []int{1, 0, 1, 0, 1}) {
		t.Fatalf("recovery did not reset execution findings: %v", sizes)
	}
}
