package main

import (
	"errors"
	"reflect"
	"testing"

	"github.com/sparkwing-dev/sparkwing/profile"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func destructiveFinding() []stepRiskFinding {
	return []stepRiskFinding{
		{NodeID: "deploy", StepID: "destroy-eks", Labels: []string{"destructive"}},
	}
}

func prodFinding() []stepRiskFinding {
	return []stepRiskFinding{
		{NodeID: "migrate", StepID: "touch-prod-db", Labels: []string{"prod"}},
	}
}

func moneyFinding() []stepRiskFinding {
	return []stepRiskFinding{
		{NodeID: "stress", StepID: "spin-up-fleet", Labels: []string{"money"}},
	}
}

func TestEnforceRiskGate_DestructiveBlocks(t *testing.T) {
	err := enforceRiskGate("cluster-down", destructiveFinding(), runFlags{}, nil)
	if err == nil {
		t.Fatal("enforceRiskGate: want refusal, got nil")
	}
	var rbe *sparkwing.RiskBlockedError
	if !errors.As(err, &rbe) {
		t.Fatalf("expected *RiskBlockedError, got %T", err)
	}
	if !reflect.DeepEqual(rbe.MissingLabels, []string{"destructive"}) {
		t.Errorf("MissingLabels = %v, want [destructive]", rbe.MissingLabels)
	}
	if rbe.StepID != "destroy-eks" {
		t.Errorf("StepID = %q, want destroy-eks", rbe.StepID)
	}
}

func TestEnforceRiskGate_AllowDestructivePasses(t *testing.T) {
	wf := runFlags{allow: []string{"destructive"}}
	if err := enforceRiskGate("cluster-down", destructiveFinding(), wf, nil); err != nil {
		t.Fatalf("--sw-allow destructive should pass: %v", err)
	}
}

func TestEnforceRiskGate_DryRunBypassesEverything(t *testing.T) {
	cases := [][]stepRiskFinding{
		destructiveFinding(),
		prodFinding(),
		moneyFinding(),
		{
			{StepID: "a", Labels: []string{"destructive"}},
			{StepID: "b", Labels: []string{"prod"}},
			{StepID: "c", Labels: []string{"money"}},
		},
	}
	for i, findings := range cases {
		wf := runFlags{dryRun: true}
		if err := enforceRiskGate("any", findings, wf, nil); err != nil {
			t.Errorf("case %d: --sw-dry-run should bypass gate: %v", i, err)
		}
	}
}

func TestEnforceRiskGate_ProductionBlocks(t *testing.T) {
	err := enforceRiskGate("migrate", prodFinding(), runFlags{}, nil)
	if err == nil {
		t.Fatal("enforceRiskGate: want refusal for prod label")
	}
	var rbe *sparkwing.RiskBlockedError
	if !errors.As(err, &rbe) {
		t.Fatalf("expected *RiskBlockedError, got %T", err)
	}
	if !reflect.DeepEqual(rbe.MissingLabels, []string{"prod"}) {
		t.Errorf("MissingLabels = %v, want [prod]", rbe.MissingLabels)
	}
}

func TestEnforceRiskGate_AllowProdPasses(t *testing.T) {
	wf := runFlags{allow: []string{"prod"}}
	if err := enforceRiskGate("migrate", prodFinding(), wf, nil); err != nil {
		t.Fatalf("--sw-allow prod should pass: %v", err)
	}
}

func TestEnforceRiskGate_AllowProdDoesNotAuthorizeDestructive(t *testing.T) {
	wf := runFlags{allow: []string{"prod"}}
	if err := enforceRiskGate("cluster-down", destructiveFinding(), wf, nil); err == nil {
		t.Fatal("--sw-allow prod should NOT authorize destructive")
	}
}

func TestEnforceRiskGate_MoneyBlocks(t *testing.T) {
	if err := enforceRiskGate("stress-test", moneyFinding(), runFlags{}, nil); err == nil {
		t.Fatal("enforceRiskGate: want refusal for money label")
	}
}

func TestEnforceRiskGate_AllowMoneyPasses(t *testing.T) {
	wf := runFlags{allow: []string{"money"}}
	if err := enforceRiskGate("stress-test", moneyFinding(), wf, nil); err != nil {
		t.Fatalf("--sw-allow money should pass: %v", err)
	}
}

func TestEnforceRiskGate_ProfileAutoAllow(t *testing.T) {
	prof := &profile.Profile{
		Name:      "laptop",
		AutoAllow: []string{"destructive"},
	}
	if err := enforceRiskGate("cluster-down", destructiveFinding(), runFlags{}, prof); err != nil {
		t.Fatalf("profile auto_allow should pass: %v", err)
	}
}

func TestEnforceRiskGate_ProfileAutoAllowDoesNotLeak(t *testing.T) {
	prof := &profile.Profile{
		Name:      "laptop",
		AutoAllow: []string{"destructive"},
	}
	if err := enforceRiskGate("migrate", prodFinding(), runFlags{}, prof); err == nil {
		t.Fatal("auto_allow destructive should NOT authorize prod")
	}
}

func TestEnforceRiskGate_NoFindings(t *testing.T) {
	if err := enforceRiskGate("plain", nil, runFlags{}, nil); err != nil {
		t.Fatalf("no findings should pass: %v", err)
	}
}

// TestEnforceRiskGate_NamesEveryMissingLabel: two steps each declare
// a different label; neither is in --sw-allow. The error must name
// both labels so the operator can authorize them in one retry.
func TestEnforceRiskGate_NamesEveryMissingLabel(t *testing.T) {
	findings := []stepRiskFinding{
		{NodeID: "n1", StepID: "step-a", Labels: []string{"destructive"}},
		{NodeID: "n2", StepID: "step-b", Labels: []string{"prod"}},
	}
	err := enforceRiskGate("multi", findings, runFlags{}, nil)
	var rbe *sparkwing.RiskBlockedError
	if !errors.As(err, &rbe) {
		t.Fatalf("expected *RiskBlockedError, got %T (%v)", err, err)
	}
	want := []string{"destructive", "prod"}
	if !reflect.DeepEqual(rbe.MissingLabels, want) {
		t.Errorf("MissingLabels = %v, want %v", rbe.MissingLabels, want)
	}
}

// TestEnforceRiskGate_PartialAllow: --sw-allow covers some labels;
// the error names only the still-missing ones.
func TestEnforceRiskGate_PartialAllow(t *testing.T) {
	findings := []stepRiskFinding{
		{NodeID: "n1", StepID: "step-a", Labels: []string{"destructive", "prod"}},
	}
	wf := runFlags{allow: []string{"destructive"}}
	err := enforceRiskGate("partial", findings, wf, nil)
	var rbe *sparkwing.RiskBlockedError
	if !errors.As(err, &rbe) {
		t.Fatalf("expected *RiskBlockedError, got %T (%v)", err, err)
	}
	if !reflect.DeepEqual(rbe.MissingLabels, []string{"prod"}) {
		t.Errorf("MissingLabels = %v, want [prod]", rbe.MissingLabels)
	}
}

// TestEnforceRiskGate_AuthorDefinedLabel confirms arbitrary author
// labels participate in the gate without requiring SDK changes.
func TestEnforceRiskGate_AuthorDefinedLabel(t *testing.T) {
	findings := []stepRiskFinding{
		{StepID: "rotate", Labels: []string{"rotates-key"}},
	}
	if err := enforceRiskGate("rotation", findings, runFlags{}, nil); err == nil {
		t.Fatal("expected refusal for author-defined label")
	}
	wf := runFlags{allow: []string{"rotates-key"}}
	if err := enforceRiskGate("rotation", findings, wf, nil); err != nil {
		t.Fatalf("--sw-allow rotates-key should pass: %v", err)
	}
}

// TestLookupCachedRisks_DegradesGracefully confirms the gate
// returns nil when no describe cache is present.
func TestLookupCachedRisks_DegradesGracefully(t *testing.T) {
	tmp := t.TempDir()
	if got := lookupCachedRisks(tmp, "any"); got != nil {
		t.Errorf("lookupCachedRisks on missing cache = %v, want nil", got)
	}
}
