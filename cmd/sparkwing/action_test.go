package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/sparkwingruntime"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

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
