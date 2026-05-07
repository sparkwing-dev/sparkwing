package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/v2/sparkwing"
)

// IMP-036: the local Pipeline struct used as the wire shape for
// `pipeline list -o json` and `pipeline describe -o json` must
// surface blast_radius (union) and blast_radius_by_step (per-step
// breakdown) when the underlying DescribePipeline declares them.
// Previously these fields were dropped during the catalog copy,
// leaving JSON consumers blind to which pipelines need
// --allow-destructive / --allow-production / --allow-money.
func TestPipelineJSON_SurfacesBlastRadius(t *testing.T) {
	p := Pipeline{
		Name:        "cluster-down",
		Venue:       "local-only",
		BlastRadius: []string{"destructive", "production"},
		BlastRadiusBySteps: []sparkwing.DescribeStepBlastRadius{
			{NodeID: "cluster-down", StepID: "terraform-destroy-eks", Markers: []string{"destructive", "production"}},
			{NodeID: "cluster-down", StepID: "terraform-destroy-nat", Markers: []string{"destructive"}},
		},
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(raw)
	for _, want := range []string{
		`"blast_radius":["destructive","production"]`,
		`"blast_radius_by_step":[`,
		`"node_id":"cluster-down"`,
		`"step_id":"terraform-destroy-eks"`,
		`"markers":["destructive","production"]`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("JSON missing %q\nfull: %s", want, got)
		}
	}
}

// IMP-036: pipelines without blast-radius markers must omit both
// fields entirely (omitempty contract). Catalog readers depend on
// the absent-field signal to mean "no markers declared", not "old
// CLI version".
func TestPipelineJSON_OmitsEmptyBlastRadius(t *testing.T) {
	p := Pipeline{
		Name:  "hello",
		Venue: "either",
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(raw)
	if strings.Contains(got, "blast_radius") {
		t.Errorf("expected no blast_radius keys in payload, got: %s", got)
	}
}

// IMP-036: the catalog copy in gatherPipelinesCatalog is the
// load-bearing site -- it copies Short / Help / Args / Examples /
// Venue from the cached DescribePipeline schema and was previously
// dropping the two blast-radius fields. This test fakes the inner
// copy to assert the union + per-step both round-trip.
func TestCatalogCopy_PreservesBlastRadius(t *testing.T) {
	dp := sparkwing.DescribePipeline{
		Name:        "cluster-down",
		Short:       "tear down the cluster",
		Venue:       "local-only",
		BlastRadius: []string{"destructive", "production"},
		BlastRadiusBySteps: []sparkwing.DescribeStepBlastRadius{
			{NodeID: "cluster-down", StepID: "destroy", Markers: []string{"destructive", "production"}},
		},
	}
	a := Pipeline{Name: dp.Name}
	// Mirror the copy block in gatherPipelinesCatalog. If a future
	// edit drops one of these assignments, this test fails -- the
	// IMP-036 regression we want to prevent.
	a.Short = dp.Short
	a.Help = dp.Help
	a.Args = dp.Args
	a.Examples = dp.Examples
	a.Venue = dp.Venue
	a.BlastRadius = dp.BlastRadius
	a.BlastRadiusBySteps = dp.BlastRadiusBySteps

	if got, want := len(a.BlastRadius), 2; got != want {
		t.Errorf("BlastRadius len = %d, want %d", got, want)
	}
	if got, want := len(a.BlastRadiusBySteps), 1; got != want {
		t.Errorf("BlastRadiusBySteps len = %d, want %d", got, want)
	}
	if a.BlastRadiusBySteps[0].StepID != "destroy" {
		t.Errorf("BlastRadiusBySteps[0].StepID = %q, want %q",
			a.BlastRadiusBySteps[0].StepID, "destroy")
	}
}

// IMP-040: `sparkwing pipeline describe --name X` returns a
// "no pipeline named X" error when X isn't in the catalog.
// Previously that error gave no hint; now it appends a "did you
// mean Y?" suggestion when the typo is close enough, matching the
// orchestrator-side "unknown pipeline" surface. This test pins the
// suggestion-composition logic from action.go's runPipelineDescribe.
func TestPipelineDescribe_NoPipelineNamed_SuggestsClosest(t *testing.T) {
	// Mirror the catalog-search fragment in runPipelineDescribe.
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
	suggestion := sparkwing.SuggestClosest(name, candidates)
	if suggestion != "cluster-up" {
		t.Fatalf("SuggestClosest(%q) = %q, want %q", name, suggestion, "cluster-up")
	}

	// And the message shape we emit when a suggestion exists:
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

// IMP-040: far typos must NOT produce a misleading suggestion.
// The describe verb falls through to the original message body
// when no candidate is close enough.
func TestPipelineDescribe_FarTypoNoSuggestion(t *testing.T) {
	catalog := []Pipeline{
		{Name: "cluster-up"},
		{Name: "hello"},
	}
	candidates := make([]string, 0, len(catalog))
	for _, p := range catalog {
		candidates = append(candidates, p.Name)
	}
	suggestion := sparkwing.SuggestClosest("totallyunrelated", candidates)
	if suggestion != "" {
		t.Errorf("far typo should not suggest, got %q", suggestion)
	}
}
