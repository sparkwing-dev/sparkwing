package controller

import (
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func reconcileSourceForStore(source controllerwire.ReuseSource) store.MaterializeSource {
	result := store.MaterializeSource{Kind: store.SourcePolicy(source.Policy), Source: source.Attempt, Record: source.Record}
	if source.Result != nil {
		result.Result = &store.MaterializedResultSource{Ref: store.NodeRef{RunID: source.Result.RunID, NodeID: source.Result.JobID}, StateSeq: source.Result.StateSeq}
	}
	return result
}

func engineReuseSource(source store.MaterializeSource) controllerwire.ReuseSource {
	result := controllerwire.ReuseSource{Policy: string(source.Kind), Attempt: source.Source, Record: source.Record}
	if source.Result != nil {
		result.Result = &controllerwire.MaterializedResultSource{RunID: source.Result.Ref.RunID, JobID: source.Result.Ref.NodeID, StateSeq: source.Result.StateSeq}
	}
	return result
}

func engineReconciledClaim(c capability.Capability, result store.ReconciledNode, run store.RunRow) *controllerwire.ClaimedUnit {
	claim := &controllerwire.ClaimedUnit{Unit: c.Unit, Purpose: c.Purpose, Delivery: c.Delivery, UnitID: c.UnitID, RunID: result.Node.Ref.RunID, JobID: result.Node.Ref.NodeID, Binding: c.Binding, Capability: string(result.Bearer), Request: run.Request, Execution: &result.Node.Execution, LeaseExpiresAt: result.Claim.LeaseExpiresAt, OwnerDeadline: c.Binding.Deadline, Runner: nodeproto.RunnerIdentity{Name: result.Claim.Holder}}
	if c.Billing != nil {
		claim.Runner.Type = c.Billing.ExecutorClass
	}
	if run.Spec != nil {
		accepted := controllerwire.PlanAccepted{Spec: *run.Spec, Plan: run.Plan, PlanHash: runspec.Hash(run.PlanHash)}
		claim.Accepted = &accepted
		claim.Engine = &run.Spec.Resolved.Engine
	}
	return claim
}
