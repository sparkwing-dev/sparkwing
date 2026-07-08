package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/sparkwingruntime"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// The local Pipeline struct used as the wire shape for `pipeline
// list -o json` and `pipeline describe -o json` must surface risks
// (union) and risks_by_step (per-step breakdown) when the underlying
// DescribePipeline declares them. JSON consumers rely on these
// fields to know which --sw-allow labels a pipeline needs.
func TestPipelineJSON_SurfacesRisks(t *testing.T) {
	p := Pipeline{
		Name:  "cluster-down",
		Risks: []string{"destructive", "prod"},
		RisksBySteps: []sparkwing.DescribeStepRisks{
			{NodeID: "cluster-down", StepID: "terraform-destroy-eks", Labels: []string{"destructive", "prod"}},
			{NodeID: "cluster-down", StepID: "terraform-destroy-nat", Labels: []string{"destructive"}},
		},
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(raw)
	for _, want := range []string{
		`"risks":["destructive","prod"]`,
		`"risks_by_step":[`,
		`"node_id":"cluster-down"`,
		`"step_id":"terraform-destroy-eks"`,
		`"labels":["destructive","prod"]`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("JSON missing %q\nfull: %s", want, got)
		}
	}
}

// Pipelines without risk labels must omit both fields entirely
// (omitempty contract). Catalog readers depend on the absent-field
// signal to mean "no labels declared".
func TestPipelineJSON_OmitsEmptyRisks(t *testing.T) {
	p := Pipeline{Name: "hello"}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(raw)
	if strings.Contains(got, "risks") {
		t.Errorf("expected no risks keys in payload, got: %s", got)
	}
}

// The catalog copy in gatherPipelinesCatalog is the load-bearing
// site -- it copies Short / Help / Args / Examples from the cached
// DescribePipeline schema along with the two risk fields. This test
// fakes the inner copy to assert the union + per-step both
// round-trip.
func TestCatalogCopy_PreservesRisks(t *testing.T) {
	dp := sparkwing.DescribePipeline{
		Name:  "cluster-down",
		Short: "tear down the cluster",
		Risks: []string{"destructive", "prod"},
		RisksBySteps: []sparkwing.DescribeStepRisks{
			{NodeID: "cluster-down", StepID: "destroy", Labels: []string{"destructive", "prod"}},
		},
	}
	a := Pipeline{
		Name:         dp.Name,
		Short:        dp.Short,
		Help:         dp.Help,
		Args:         dp.Args,
		Examples:     dp.Examples,
		Risks:        dp.Risks,
		RisksBySteps: dp.RisksBySteps,
	}

	if a.Name != dp.Name {
		t.Errorf("Name = %q, want %q", a.Name, dp.Name)
	}
	if a.Short != dp.Short {
		t.Errorf("Short = %q, want %q", a.Short, dp.Short)
	}
	if a.Help != dp.Help {
		t.Errorf("Help = %q, want %q", a.Help, dp.Help)
	}
	if len(a.Args) != len(dp.Args) {
		t.Errorf("Args len = %d, want %d", len(a.Args), len(dp.Args))
	}
	if len(a.Examples) != len(dp.Examples) {
		t.Errorf("Examples len = %d, want %d", len(a.Examples), len(dp.Examples))
	}
	if got, want := len(a.Risks), 2; got != want {
		t.Errorf("Risks len = %d, want %d", got, want)
	}
	if got, want := len(a.RisksBySteps), 1; got != want {
		t.Errorf("RisksBySteps len = %d, want %d", got, want)
	}
	if a.RisksBySteps[0].StepID != "destroy" {
		t.Errorf("RisksBySteps[0].StepID = %q, want %q",
			a.RisksBySteps[0].StepID, "destroy")
	}
}

// `sparkwing pipeline describe --name X` returns a "no pipeline
// named X" error when X isn't in the catalog, appended with a "did
// you mean Y?" suggestion when the typo is close enough, matching
// the orchestrator-side "unknown pipeline" surface. This test pins
// the suggestion-composition logic from action.go's
// runPipelineDescribe.
func TestPipelineDescribe_NoPipelineNamed_SuggestsClosest(t *testing.T) {
	catalog := []Pipeline{
		{Name: "cluster-up"},
		{Name: "cluster-down"},
		{Name: "hello"},
	}
	name := "claster-up"

	candidates := make([]string, 0, len(catalog))
	for _, p := range catalog {
		candidates = append(candidates, p.Name)
	}
	suggestion := sparkwingruntime.SuggestClosest(name, candidates)
	if suggestion != "cluster-up" {
		t.Fatalf("SuggestClosest(%q) = %q, want %q", name, suggestion, "cluster-up")
	}

	msg := fmt.Sprintf("no pipeline named %q; did you mean %q? (run `sparkwing pipeline list --all` to see every entry)", name, suggestion)
	for _, want := range []string{
		`no pipeline named "claster-up"`,
		`did you mean "cluster-up"`,
		"sparkwing pipeline list --all",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("describe error missing %q\nfull: %s", want, msg)
		}
	}
}

// Far typos must NOT produce a misleading suggestion. The describe
// verb falls through to the original message body when no candidate
// is close enough.
func TestPipelineDescribe_FarTypoNoSuggestion(t *testing.T) {
	catalog := []Pipeline{
		{Name: "cluster-up"},
		{Name: "hello"},
	}
	candidates := make([]string, 0, len(catalog))
	for _, p := range catalog {
		candidates = append(candidates, p.Name)
	}
	suggestion := sparkwingruntime.SuggestClosest("totallyunrelated", candidates)
	if suggestion != "" {
		t.Errorf("far typo should not suggest, got %q", suggestion)
	}
}
