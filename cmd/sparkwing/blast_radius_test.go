package main

import (
	"errors"
	"testing"

	"github.com/sparkwing-dev/sparkwing/profile"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// IMP-015: enforce the dispatch gate from the wing CLI's
// perspective. The unit-level coverage (BlastRadius.String,
// BlastRadiusBlockedError contract) lives in pkg/sparkwing; here we
// pin the wing-level escape-flag and profile-auto-allow behavior.

// destructiveFinding is a fixture for "any pipeline with a
// destructive step" so the table tests below stay short.
func destructiveFinding() []blastRadiusFinding {
	return []blastRadiusFinding{
		{NodeID: "deploy", StepID: "destroy-eks", Marker: sparkwing.BlastRadiusDestructive},
	}
}

func prodFinding() []blastRadiusFinding {
	return []blastRadiusFinding{
		{NodeID: "migrate", StepID: "touch-prod-db", Marker: sparkwing.BlastRadiusAffectsProduction},
	}
}

func moneyFinding() []blastRadiusFinding {
	return []blastRadiusFinding{
		{NodeID: "stress", StepID: "spin-up-fleet", Marker: sparkwing.BlastRadiusCostsMoney},
	}
}

func TestEnforceBlastRadius_DestructiveBlocks(t *testing.T) {
	err := enforceBlastRadius("cluster-down", destructiveFinding(), wingFlags{}, nil)
	if err == nil {
		t.Fatal("enforceBlastRadius: want refusal, got nil")
	}
	var bre *sparkwing.BlastRadiusBlockedError
	if !errors.As(err, &bre) {
		t.Fatalf("expected *BlastRadiusBlockedError, got %T", err)
	}
	if bre.Marker != sparkwing.BlastRadiusDestructive {
		t.Errorf("Marker = %v, want destructive", bre.Marker)
	}
	if bre.StepID != "destroy-eks" {
		t.Errorf("StepID = %q, want destroy-eks", bre.StepID)
	}
}

func TestEnforceBlastRadius_AllowDestructivePasses(t *testing.T) {
	wf := wingFlags{allowDestructive: true}
	if err := enforceBlastRadius("cluster-down", destructiveFinding(), wf, nil); err != nil {
		t.Fatalf("--allow-destructive should pass: %v", err)
	}
}

func TestEnforceBlastRadius_DryRunBypassesEverything(t *testing.T) {
	// IMP-014 contract: --dry-run is the always-safe escape hatch
	// regardless of which marker is declared.
	cases := [][]blastRadiusFinding{
		destructiveFinding(),
		prodFinding(),
		moneyFinding(),
		{
			{Marker: sparkwing.BlastRadiusDestructive},
			{Marker: sparkwing.BlastRadiusAffectsProduction},
			{Marker: sparkwing.BlastRadiusCostsMoney},
		},
	}
	for i, findings := range cases {
		wf := wingFlags{dryRun: true}
		if err := enforceBlastRadius("any", findings, wf, nil); err != nil {
			t.Errorf("case %d: --dry-run should bypass gate: %v", i, err)
		}
	}
}

func TestEnforceBlastRadius_ProductionBlocks(t *testing.T) {
	err := enforceBlastRadius("migrate", prodFinding(), wingFlags{}, nil)
	if err == nil {
		t.Fatal("enforceBlastRadius: want refusal for production marker")
	}
	var bre *sparkwing.BlastRadiusBlockedError
	if !errors.As(err, &bre) {
		t.Fatalf("expected *BlastRadiusBlockedError, got %T", err)
	}
	if bre.Marker != sparkwing.BlastRadiusAffectsProduction {
		t.Errorf("Marker = %v, want production", bre.Marker)
	}
}

func TestEnforceBlastRadius_AllowProdPasses(t *testing.T) {
	wf := wingFlags{allowProd: true}
	if err := enforceBlastRadius("migrate", prodFinding(), wf, nil); err != nil {
		t.Fatalf("--allow-prod should pass: %v", err)
	}
}

func TestEnforceBlastRadius_AllowProdDoesNotAuthorizeDestructive(t *testing.T) {
	wf := wingFlags{allowProd: true}
	if err := enforceBlastRadius("cluster-down", destructiveFinding(), wf, nil); err == nil {
		t.Fatal("--allow-prod should NOT authorize destructive marker")
	}
}

func TestEnforceBlastRadius_MoneyBlocks(t *testing.T) {
	err := enforceBlastRadius("stress-test", moneyFinding(), wingFlags{}, nil)
	if err == nil {
		t.Fatal("enforceBlastRadius: want refusal for money marker")
	}
}

func TestEnforceBlastRadius_AllowMoneyPasses(t *testing.T) {
	wf := wingFlags{allowMoney: true}
	if err := enforceBlastRadius("stress-test", moneyFinding(), wf, nil); err != nil {
		t.Fatalf("--allow-money should pass: %v", err)
	}
}

func TestEnforceBlastRadius_ProfileAutoAllowDestructive(t *testing.T) {
	prof := &profile.Profile{
		Name:      "laptop",
		AutoAllow: profile.AutoAllow{Destructive: true},
	}
	if err := enforceBlastRadius("cluster-down", destructiveFinding(), wingFlags{}, prof); err != nil {
		t.Fatalf("profile auto_allow.destructive should pass: %v", err)
	}
}

func TestEnforceBlastRadius_ProfileAutoAllowDoesNotLeak(t *testing.T) {
	// auto_allow.destructive should NOT silently authorize the
	// other markers; each is per-marker so a "laptop allows destroy"
	// declaration doesn't accidentally bless prod-touching work.
	prof := &profile.Profile{
		Name:      "laptop",
		AutoAllow: profile.AutoAllow{Destructive: true},
	}
	if err := enforceBlastRadius("migrate", prodFinding(), wingFlags{}, prof); err == nil {
		t.Fatal("auto_allow.destructive should NOT authorize production marker")
	}
}

func TestEnforceBlastRadius_NoFindings(t *testing.T) {
	// Empty findings is the "no markers detected" state and must
	// pass cleanly so a cold cache or pre-IMP-015 binary doesn't
	// block dispatch.
	if err := enforceBlastRadius("plain", nil, wingFlags{}, nil); err != nil {
		t.Fatalf("no findings should pass: %v", err)
	}
}

func TestEnforceBlastRadius_FirstFindingSurfaces(t *testing.T) {
	// Two markers fire; only the first one without an --allow flag
	// is reported (the operator fixes one at a time).
	findings := []blastRadiusFinding{
		{NodeID: "n1", StepID: "step-a", Marker: sparkwing.BlastRadiusDestructive},
		{NodeID: "n2", StepID: "step-b", Marker: sparkwing.BlastRadiusAffectsProduction},
	}
	wf := wingFlags{allowDestructive: true} // skip the first
	err := enforceBlastRadius("multi", findings, wf, nil)
	var bre *sparkwing.BlastRadiusBlockedError
	if !errors.As(err, &bre) {
		t.Fatalf("expected *BlastRadiusBlockedError, got %T (%v)", err, err)
	}
	if bre.Marker != sparkwing.BlastRadiusAffectsProduction {
		t.Errorf("expected production refusal, got %v", bre.Marker)
	}
	if bre.StepID != "step-b" {
		t.Errorf("expected step-b refusal, got %q", bre.StepID)
	}
}

// TestLookupCachedBlastRadius_DegradesGracefully confirms the gate
// returns nil when no describe cache is present, matching IMP-011's
// degrade-gracefully shape.
func TestLookupCachedBlastRadius_DegradesGracefully(t *testing.T) {
	// Point at a directory with no cache; lookup must return nil
	// without panic so the dispatcher proceeds unblocked.
	tmp := t.TempDir()
	if got := lookupCachedBlastRadius(tmp, "any"); got != nil {
		t.Errorf("lookupCachedBlastRadius on missing cache = %v, want nil", got)
	}
}
