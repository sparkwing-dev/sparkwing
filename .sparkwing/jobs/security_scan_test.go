package jobs

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSecurityScanStrictFlagDescription(t *testing.T) {
	field, ok := reflect.TypeFor[SecurityScanArgs]().FieldByName("Strict")
	if !ok {
		t.Fatal("SecurityScanArgs.Strict is missing")
	}
	want := "Fail the gosec job when any high-severity, high-confidence finding remains. Gosec findings are report-only unless this flag is set."
	if got := field.Tag.Get("desc"); got != want {
		t.Fatalf("strict description = %q, want %q", got, want)
	}
}

func TestGosecSARIFUsesRepoRelativePaths(t *testing.T) {
	root := filepath.Join("/", "work", "sparkwing")
	issues := []gosecIssue{
		{
			Severity: "HIGH", Confidence: "HIGH", RuleID: "G703", Details: "Path traversal via taint analysis",
			File: filepath.Join(root, "internal", "cache", "gitcache.go"), Line: "157", Column: "9", Code: "157: f, err := os.Open(p)",
		},
		{
			Severity: "MEDIUM", Confidence: "HIGH", RuleID: "G705", Details: "XSS via taint analysis",
			File: filepath.Join(root, ".sparkwing", "jobs", "x.go"), Line: "10-12", Column: "1",
		},
		{
			Severity: "LOW", Confidence: "LOW", RuleID: "G706", Details: "Log injection",
			File: filepath.Join(root, "pkg", "logs", "server.go"), Line: "3",
		},
	}
	log := gosecSARIF(root, issues)

	if log.Version != "2.1.0" || len(log.Runs) != 1 {
		t.Fatalf("unexpected sarif envelope: %+v", log)
	}
	run := log.Runs[0]
	if run.Tool.Driver.Name != "gosec" || run.Tool.Driver.Version != "2.29.0" {
		t.Fatalf("driver = %+v", run.Tool.Driver)
	}
	if len(run.Tool.Driver.Rules) != 3 || len(run.Results) != 3 {
		t.Fatalf("rules=%d results=%d", len(run.Tool.Driver.Rules), len(run.Results))
	}

	want := []struct {
		uri   string
		level string
		line  int
		col   int
	}{
		{"internal/cache/gitcache.go", "error", 157, 9},
		{".sparkwing/jobs/x.go", "warning", 10, 1},
		{"pkg/logs/server.go", "note", 3, 0},
	}
	for i, w := range want {
		got := run.Results[i]
		loc := got.Locations[0].PhysicalLocation
		if loc.ArtifactLocation.URI != w.uri || loc.ArtifactLocation.URIBaseID != "%SRCROOT%" {
			t.Errorf("result %d uri = %+v, want %s", i, loc.ArtifactLocation, w.uri)
		}
		if got.Level != w.level || loc.Region.StartLine != w.line || loc.Region.StartColumn != w.col {
			t.Errorf("result %d level/region = %s %+v, want %s %d:%d", i, got.Level, loc.Region, w.level, w.line, w.col)
		}
		if got.RuleIndex != i || run.Tool.Driver.Rules[got.RuleIndex].ID != got.RuleID {
			t.Errorf("result %d ruleIndex %d does not resolve to %s", i, got.RuleIndex, got.RuleID)
		}
	}
	if run.Results[0].Locations[0].PhysicalLocation.Region.Snippet == nil {
		t.Errorf("snippet dropped for result 0")
	}
	if run.Results[2].Locations[0].PhysicalLocation.Region.Snippet != nil {
		t.Errorf("empty code produced a snippet")
	}
	rule := run.Tool.Driver.Rules[0]
	if rule.Properties.SecuritySeverity != "8.0" || rule.Properties.Precision != "high" || rule.DefaultConfig.Level != "error" {
		t.Errorf("rule properties = %+v %+v", rule.Properties, rule.DefaultConfig)
	}

	data, err := json.Marshal(log)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"security-severity":"8.0"`) {
		t.Errorf("security-severity missing from encoded sarif")
	}
}

func TestGosecSARIFEmptyReportEncodesArrays(t *testing.T) {
	data, err := json.Marshal(gosecSARIF("/root", nil))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"rules":[]`, `"results":[]`} {
		if !strings.Contains(string(data), want) {
			t.Errorf("encoded sarif lacks %s: %s", want, data)
		}
	}
}

func TestSummarizeGosecCountsHighHigh(t *testing.T) {
	issues := []gosecIssue{
		{Severity: "HIGH", Confidence: "HIGH", RuleID: "G703"},
		{Severity: "HIGH", Confidence: "HIGH", RuleID: "G703"},
		{Severity: "HIGH", Confidence: "MEDIUM", RuleID: "G115"},
		{Severity: "MEDIUM", Confidence: "HIGH", RuleID: "G705"},
	}
	s := summarizeGosec(issues)
	if s.highHigh != 2 {
		t.Errorf("highHigh = %d, want 2", s.highHigh)
	}
	if len(s.lines) != 3 || !strings.HasPrefix(strings.TrimSpace(s.lines[0]), "2  G703") {
		t.Errorf("summary lines = %q", s.lines)
	}
}

func TestFirstInt(t *testing.T) {
	for in, want := range map[string]int{"12": 12, "10-14": 10, "": 0, "x": 0, " 7 ": 7} {
		if got := firstInt(in); got != want {
			t.Errorf("firstInt(%q) = %d, want %d", in, got, want)
		}
	}
}
