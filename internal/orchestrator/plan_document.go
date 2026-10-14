package orchestrator

import (
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/sparkwingruntime"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// safety: leaves out requires and spec_hash, which the node protocol gives to
// the host.
func planDocument(plan *sparkwing.Plan, rc sparkwing.RunContext, reg *sparkwing.Registration) (nodeproto.PlanDoc, error) {
	snap, err := buildPlanSnapshot(plan, rc, planSnapshotMeta{Secrets: sparkwingruntime.ReflectSecretsField(reg)})
	if err != nil {
		return snap, err
	}
	if c := plan.CheckoutValue(); c != nil {
		depth := max(c.Depth, 1)
		if c.FullHistory {
			depth = 0
		}
		snap.Source = &nodeproto.PlanSource{Depth: depth, Tags: c.Tags, Submodules: c.Submodules, LFS: c.LFS}
	}
	return snap, nil
}
