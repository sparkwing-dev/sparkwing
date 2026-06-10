package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSeverity_BlocksMediumAndAbove(t *testing.T) {
	cases := map[Severity]bool{
		SevBlocker: true,
		SevHigh:    true,
		SevMedium:  true,
		SevLow:     false,
	}
	for sev, want := range cases {
		if got := sev.blocks(); got != want {
			t.Errorf("%s.blocks() = %v, want %v", sev, got, want)
		}
	}
}

func TestBlocking_FiltersToGatingFindings(t *testing.T) {
	in := []Finding{
		{Agent: "a", Severity: SevLow, Claim: "nit"},
		{Agent: "b", Severity: SevMedium, Claim: "real"},
		{Agent: "c", Severity: SevBlocker, Claim: "bad"},
	}
	got := blocking(in)
	if len(got) != 2 {
		t.Fatalf("blocking() returned %d findings, want 2", len(got))
	}
	for _, f := range got {
		if f.Severity == SevLow {
			t.Errorf("low-severity finding leaked into blocking set: %+v", f)
		}
	}
}

func TestSortFindings_MostSevereFirst(t *testing.T) {
	in := []Finding{
		{Agent: "z", Severity: SevLow, Claim: "x"},
		{Agent: "a", Severity: SevBlocker, Claim: "x"},
		{Agent: "m", Severity: SevMedium, Claim: "x"},
	}
	sortFindings(in)
	if in[0].Severity != SevBlocker || in[2].Severity != SevLow {
		t.Errorf("not sorted by severity: %v", []Severity{in[0].Severity, in[1].Severity, in[2].Severity})
	}
}

func TestParsePayload_ToleratesFencesAndProse(t *testing.T) {
	cases := []string{
		`{"findings":[{"file":"a.go","severity":"high","claim":"boom"}]}`,
		"```json\n{\"findings\":[{\"file\":\"a.go\",\"severity\":\"high\",\"claim\":\"boom\"}]}\n```",
		"Here you go:\n{\"findings\":[{\"file\":\"a.go\",\"severity\":\"high\",\"claim\":\"boom\"}]}",
	}
	for i, c := range cases {
		p, err := parsePayload(c)
		if err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
		if len(p.Findings) != 1 || p.Findings[0].File != "a.go" {
			t.Errorf("case %d: parsed %+v", i, p.Findings)
		}
	}
}

func TestParsePayload_RejectsGarbage(t *testing.T) {
	if _, err := parsePayload("no json here"); err == nil {
		t.Error("expected error on garbage input")
	}
}

func TestWriteBucket_RoundTrips(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "findings.json")
	in := []Finding{{Agent: "a", File: "x.go", Severity: SevHigh, Claim: "boom"}}
	if err := writeBucket(path, in); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []Finding
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Claim != "boom" {
		t.Errorf("round-trip mismatch: %+v", out)
	}
}

func TestReport_GroupsAndMarksSeverity(t *testing.T) {
	r := report([]Finding{
		{Agent: "correctness-reviewer", File: "x.go", Line: 4, Severity: SevBlocker, Claim: "nil deref"},
	})
	if !strings.Contains(r, "correctness-reviewer") || !strings.Contains(r, "[blocker]") || !strings.Contains(r, "x.go:4") {
		t.Errorf("report missing expected content:\n%s", r)
	}
}

func TestAgents_PersonasAndRulesResolve(t *testing.T) {
	if len(agents) != 10 {
		t.Errorf("expected 10 reviewers, got %d", len(agents))
	}
	for _, a := range agents {
		sp, err := a.systemPrompt()
		if err != nil {
			t.Errorf("%s: systemPrompt: %v", a.Name, err)
		}
		if len(sp) == 0 {
			t.Errorf("%s: empty system prompt", a.Name)
		}
		if a.Rules != "" && !strings.Contains(sp, "The rules you enforce") {
			t.Errorf("%s: rules not inlined", a.Name)
		}
	}
}
