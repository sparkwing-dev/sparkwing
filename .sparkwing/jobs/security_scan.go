package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

const (
	gosecModule       = "github.com/securego/gosec/v2/cmd/gosec@v2.29.0"
	govulncheckModule = "golang.org/x/vuln/cmd/govulncheck@v1.4.0"
	gitleaksModule    = "github.com/zricethezav/gitleaks/v8@v8.30.1"
)

var gosecExcludedRules = []string{"G104", "G115", "G118", "G204", "G301", "G302", "G304", "G306", "G307"}

var gosecExcludedDirs = []string{"web", "testdata", "dist"}

// SecurityScanArgs are the inputs of the security-scan pipeline.
type SecurityScanArgs struct {
	ReportDir string `flag:"report-dir" desc:"Directory that receives gosec.sarif and gosec.json. Default: a fresh temporary directory named in the job log."`
	Strict    bool   `flag:"strict" desc:"Fail the gosec job when any high-severity, high-confidence finding remains. Off while the recorded backlog is being resolved; GitHub code scanning blocks new findings meanwhile."`
}

// SecurityScan runs the static security scanners against the repository.
type SecurityScan struct{ sparkwing.Base }

func (SecurityScan) ShortHelp() string {
	return "Security scanners: gosec (SARIF) + govulncheck source mode + gitleaks + npm audit"
}

func (SecurityScan) Help() string {
	return "Runs four independent scanners: gosec over the public module and the .sparkwing pipeline module, excluding the rules that describe how a CI tool works rather than a defect in it (files and subprocesses named by its inputs, cache directory permissions, and checks the lint gate already owns), writing gosec.json and a repo-relative gosec.sarif for GitHub code scanning; govulncheck in source mode over ./...; gitleaks over the available git history using the .gitleaks.toml allow-list; and `npm audit` over the dashboard's production dependencies. gosec reports without failing unless --strict is set; code scanning records its findings but does not make this pipeline fail. The other three scanners fail on any finding. The three Go-based scanners run through `go run` at pinned module versions; npm must be on PATH."
}

func (SecurityScan) Examples() []sparkwing.Example {
	return []sparkwing.Example{
		{Comment: "Run every scanner and print the gosec summary", Command: "sparkwing run security-scan"},
		{Comment: "Write the SARIF where a workflow can upload it", Command: "sparkwing run security-scan --report-dir=/tmp/security"},
		{Comment: "Fail on remaining high-severity, high-confidence gosec findings", Command: "sparkwing run security-scan --strict"},
	}
}

func (p *SecurityScan) Plan(_ context.Context, plan *sparkwing.Plan, in SecurityScanArgs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "gosec", func(ctx context.Context) error { return p.gosec(ctx, in) })
	sparkwing.Job(plan, "govulncheck", p.govulncheck)
	sparkwing.Job(plan, "gitleaks", p.gitleaks)
	sparkwing.Job(plan, "npm-audit", p.npmAudit)
	return nil
}

func (p *SecurityScan) gosec(ctx context.Context, in SecurityScanArgs) error {
	reportDir := in.ReportDir
	if reportDir == "" {
		dir, err := os.MkdirTemp("", "sparkwing-security-scan-*")
		if err != nil {
			return fmt.Errorf("create report directory: %w", err)
		}
		reportDir = dir
	}
	if err := os.MkdirAll(reportDir, 0o755); err != nil {
		return fmt.Errorf("create report directory: %w", err)
	}
	root := sparkwing.WorkDir()

	modules := []struct{ dir, report string }{
		{".", "gosec.json"},
		{".sparkwing", "gosec-pipelines.json"},
	}
	var issues []gosecIssue
	for _, module := range modules {
		found, err := runGosec(ctx, filepath.Join(root, module.dir), filepath.Join(reportDir, module.report))
		if err != nil {
			return fmt.Errorf("gosec %s: %w", module.dir, err)
		}
		issues = append(issues, found...)
	}

	sarif := gosecSARIF(root, issues)
	sarifPath := filepath.Join(reportDir, "gosec.sarif")
	data, err := json.MarshalIndent(sarif, "", "  ")
	if err != nil {
		return fmt.Errorf("encode sarif: %w", err)
	}
	if err := os.WriteFile(sarifPath, data, 0o644); err != nil {
		return fmt.Errorf("write sarif: %w", err)
	}

	summary := summarizeGosec(issues)
	sparkwing.Info(ctx, "gosec: %d finding(s); report in %s", len(issues), reportDir)
	for _, line := range summary.lines {
		sparkwing.Info(ctx, "  %s", line)
	}
	if in.Strict && summary.highHigh > 0 {
		return fmt.Errorf("gosec: %d high-severity, high-confidence finding(s) remain (see %s)", summary.highHigh, sarifPath)
	}
	return nil
}

func runGosec(ctx context.Context, dir, out string) ([]gosecIssue, error) {
	args := []string{
		"run", gosecModule,
		"-quiet", "-no-fail", "-exclude-generated",
		"-exclude=" + strings.Join(gosecExcludedRules, ","),
		"-fmt=json", "-out=" + out,
	}
	for _, d := range gosecExcludedDirs {
		args = append(args, "-exclude-dir="+d)
	}
	args = append(args, "./...")
	if _, err := sparkwing.Exec(ctx, "go", args...).Dir(dir).Env("GOWORK", "off").Run(); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(out)
	if err != nil {
		return nil, fmt.Errorf("read gosec report: %w", err)
	}
	var report gosecReport
	if err := json.Unmarshal(data, &report); err != nil {
		return nil, fmt.Errorf("decode gosec report %s: %w", out, err)
	}
	return report.Issues, nil
}

func (p *SecurityScan) govulncheck(ctx context.Context) error {
	if _, err := sparkwing.Exec(ctx, "go", "run", govulncheckModule, "./...").Env("GOWORK", "off").Run(); err != nil {
		return err
	}
	sparkwing.Info(ctx, "govulncheck: no reachable vulnerabilities")
	return nil
}

func (p *SecurityScan) gitleaks(ctx context.Context) error {
	if _, err := sparkwing.Exec(ctx, "go", "run", gitleaksModule, "git", "--no-banner", "--redact", "--exit-code=1", ".").Env("GOWORK", "off").Run(); err != nil {
		return err
	}
	sparkwing.Info(ctx, "gitleaks: no leaks in history")
	return nil
}

func (p *SecurityScan) npmAudit(ctx context.Context) error {
	if _, err := sparkwing.Bash(ctx, "npm --prefix web audit --omit=dev --audit-level=high").Run(); err != nil {
		return err
	}
	sparkwing.Info(ctx, "npm audit: no high or critical advisories in production dependencies")
	return nil
}

type gosecReport struct {
	Issues []gosecIssue `json:"Issues"`
}

type gosecIssue struct {
	Severity   string `json:"severity"`
	Confidence string `json:"confidence"`
	RuleID     string `json:"rule_id"`
	Details    string `json:"details"`
	File       string `json:"file"`
	Code       string `json:"code"`
	Line       string `json:"line"`
	Column     string `json:"column"`
	CWE        struct {
		ID  string `json:"id"`
		URL string `json:"url"`
	} `json:"cwe"`
}

type gosecSummary struct {
	highHigh int
	lines    []string
}

func summarizeGosec(issues []gosecIssue) gosecSummary {
	type key struct{ rule, severity, confidence string }
	counts := map[key]int{}
	var summary gosecSummary
	for _, issue := range issues {
		counts[key{issue.RuleID, issue.Severity, issue.Confidence}]++
		if issue.Severity == "HIGH" && issue.Confidence == "HIGH" {
			summary.highHigh++
		}
	}
	keys := make([]key, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if counts[keys[i]] != counts[keys[j]] {
			return counts[keys[i]] > counts[keys[j]]
		}
		return keys[i].rule < keys[j].rule
	})
	for _, k := range keys {
		summary.lines = append(summary.lines, fmt.Sprintf("%4d  %s  severity=%s confidence=%s", counts[k], k.rule, k.severity, k.confidence))
	}
	return summary
}

type sarifLog struct {
	Schema  string     `json:"$schema"`
	Version string     `json:"version"`
	Runs    []sarifRun `json:"runs"`
}

type sarifRun struct {
	Tool    sarifTool     `json:"tool"`
	Results []sarifResult `json:"results"`
}

type sarifTool struct {
	Driver sarifDriver `json:"driver"`
}

type sarifDriver struct {
	Name           string      `json:"name"`
	Version        string      `json:"version"`
	InformationURI string      `json:"informationUri"`
	Rules          []sarifRule `json:"rules"`
}

type sarifRule struct {
	ID               string          `json:"id"`
	Name             string          `json:"name"`
	ShortDescription sarifText       `json:"shortDescription"`
	FullDescription  sarifText       `json:"fullDescription"`
	Help             sarifText       `json:"help"`
	HelpURI          string          `json:"helpUri,omitempty"`
	Properties       sarifRuleProps  `json:"properties"`
	DefaultConfig    sarifRuleConfig `json:"defaultConfiguration"`
}

type sarifRuleProps struct {
	Tags             []string `json:"tags"`
	Precision        string   `json:"precision"`
	SecuritySeverity string   `json:"security-severity"`
}

type sarifRuleConfig struct {
	Level string `json:"level"`
}

type sarifText struct {
	Text string `json:"text"`
}

type sarifResult struct {
	RuleID     string            `json:"ruleId"`
	RuleIndex  int               `json:"ruleIndex"`
	Level      string            `json:"level"`
	Message    sarifText         `json:"message"`
	Locations  []sarifLocation   `json:"locations"`
	Properties map[string]string `json:"properties"`
}

type sarifLocation struct {
	PhysicalLocation sarifPhysicalLocation `json:"physicalLocation"`
}

type sarifPhysicalLocation struct {
	ArtifactLocation sarifArtifactLocation `json:"artifactLocation"`
	Region           sarifRegion           `json:"region"`
}

type sarifArtifactLocation struct {
	URI       string `json:"uri"`
	URIBaseID string `json:"uriBaseId"`
}

type sarifRegion struct {
	StartLine   int        `json:"startLine"`
	StartColumn int        `json:"startColumn,omitempty"`
	Snippet     *sarifText `json:"snippet,omitempty"`
}

// hack: gosec's own SARIF writer emits bare file names that code scanning
// cannot map back to the repository, so the JSON report is converted here.
func gosecSARIF(root string, issues []gosecIssue) sarifLog {
	ruleIndex := map[string]int{}
	var rules []sarifRule
	var results []sarifResult
	for _, issue := range issues {
		idx, ok := ruleIndex[issue.RuleID]
		if !ok {
			idx = len(rules)
			ruleIndex[issue.RuleID] = idx
			rules = append(rules, sarifRule{
				ID:               issue.RuleID,
				Name:             issue.RuleID,
				ShortDescription: sarifText{Text: issue.Details},
				FullDescription:  sarifText{Text: issue.Details},
				Help:             sarifText{Text: fmt.Sprintf("%s\nSeverity: %s\nConfidence: %s\n", issue.Details, issue.Severity, issue.Confidence)},
				HelpURI:          issue.CWE.URL,
				Properties: sarifRuleProps{
					Tags:             []string{"security", issue.Severity},
					Precision:        strings.ToLower(issue.Confidence),
					SecuritySeverity: sarifSecuritySeverity(issue.Severity),
				},
				DefaultConfig: sarifRuleConfig{Level: sarifLevel(issue.Severity)},
			})
		}
		rel, err := filepath.Rel(root, issue.File)
		if err != nil {
			rel = issue.File
		}
		region := sarifRegion{StartLine: firstInt(issue.Line), StartColumn: firstInt(issue.Column)}
		if issue.Code != "" {
			region.Snippet = &sarifText{Text: issue.Code}
		}
		results = append(results, sarifResult{
			RuleID:    issue.RuleID,
			RuleIndex: idx,
			Level:     sarifLevel(issue.Severity),
			Message:   sarifText{Text: issue.Details},
			Locations: []sarifLocation{{PhysicalLocation: sarifPhysicalLocation{
				ArtifactLocation: sarifArtifactLocation{URI: filepath.ToSlash(rel), URIBaseID: "%SRCROOT%"},
				Region:           region,
			}}},
			Properties: map[string]string{
				"severity":   issue.Severity,
				"confidence": issue.Confidence,
				"cwe":        issue.CWE.ID,
			},
		})
	}
	if rules == nil {
		rules = []sarifRule{}
	}
	if results == nil {
		results = []sarifResult{}
	}
	return sarifLog{
		Schema:  "https://json.schemastore.org/sarif-2.1.0.json",
		Version: "2.1.0",
		Runs: []sarifRun{{
			Tool: sarifTool{Driver: sarifDriver{
				Name:           "gosec",
				Version:        strings.TrimPrefix(gosecModule[strings.LastIndex(gosecModule, "@")+1:], "v"),
				InformationURI: "https://github.com/securego/gosec",
				Rules:          rules,
			}},
			Results: results,
		}},
	}
}

func sarifLevel(severity string) string {
	switch severity {
	case "HIGH":
		return "error"
	case "MEDIUM":
		return "warning"
	default:
		return "note"
	}
}

func sarifSecuritySeverity(severity string) string {
	switch severity {
	case "HIGH":
		return "8.0"
	case "MEDIUM":
		return "5.0"
	default:
		return "2.0"
	}
}

func firstInt(s string) int {
	s, _, _ = strings.Cut(s, "-")
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0
	}
	return n
}

func init() {
	sparkwing.Register[SecurityScanArgs]("security-scan", func() sparkwing.Pipeline[SecurityScanArgs] { return &SecurityScan{} })
}
