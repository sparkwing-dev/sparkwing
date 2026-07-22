package orchestrator

import (
	"context"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func noopFingerprintJob(ctx context.Context) error { return nil }

func fingerprintPlan(capacity int) *sparkwing.Plan {
	group := sparkwing.NewConcurrencyGroup("db", sparkwing.ConcurrencyLimit{
		Capacity: capacity,
		Scope:    sparkwing.ScopeRun,
	})
	plan := sparkwing.NewPlan()
	a := sparkwing.Job(plan, "shard-a", noopFingerprintJob).Concurrency(group)
	sparkwing.Job(plan, "shard-b", noopFingerprintJob).Concurrency(group).Needs(a)
	return plan
}

func TestCapacityFingerprint_ChangesWithGroupCapacity(t *testing.T) {
	wide := capacityFingerprint(fingerprintPlan(4))
	narrow := capacityFingerprint(fingerprintPlan(1))
	if wide == narrow {
		t.Error("fingerprint unchanged by group capacity; a serialized pipeline would keep its old measured price")
	}
}

func TestCapacityFingerprint_StableForIdenticalDeclarations(t *testing.T) {
	if a, b := capacityFingerprint(fingerprintPlan(2)), capacityFingerprint(fingerprintPlan(2)); a != b {
		t.Errorf("fingerprint differs across identical plans: %q vs %q", a, b)
	}
}

func TestCapacityFingerprint_NormalizesZeroScopeAndPolicy(t *testing.T) {
	build := func(limit sparkwing.ConcurrencyLimit) *sparkwing.Plan {
		plan := sparkwing.NewPlan()
		sparkwing.Job(plan, "n", noopFingerprintJob).
			Concurrency(sparkwing.NewConcurrencyGroup("g", limit))
		return plan
	}
	zero := capacityFingerprint(build(sparkwing.ConcurrencyLimit{Capacity: 2}))
	explicit := capacityFingerprint(build(sparkwing.ConcurrencyLimit{
		Capacity: 2, Scope: sparkwing.ScopeGlobal, OnLimit: sparkwing.Queue,
	}))
	if zero != explicit {
		t.Error("zero-value scope/policy fingerprints differently than their explicit defaults")
	}
}

func TestCapacityFingerprint_IncludesPlanLevelGroups(t *testing.T) {
	build := func(gated bool) *sparkwing.Plan {
		plan := sparkwing.NewPlan()
		sparkwing.Job(plan, "n", noopFingerprintJob)
		if gated {
			plan.Concurrency(sparkwing.NewConcurrencyGroup("deploy", sparkwing.ConcurrencyLimit{Capacity: 1}))
		}
		return plan
	}
	if capacityFingerprint(build(false)) == capacityFingerprint(build(true)) {
		t.Error("plan-level concurrency gate does not change the fingerprint")
	}
}

// TestPlanTopologyHash_UnchangedByConcurrency confirms the topology hash
// stays declaration-blind, so a live run_plan record still matches a
// post-hoc receipt.
func TestPlanTopologyHash_UnchangedByConcurrency(t *testing.T) {
	if a, b := planTopologyHash(fingerprintPlan(4).Nodes()), planTopologyHash(fingerprintPlan(1).Nodes()); a != b {
		t.Errorf("topology hash moved with concurrency declarations: %q vs %q", a, b)
	}
}
