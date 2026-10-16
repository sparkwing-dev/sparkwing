package localcontroller

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func (c *nodeClaims) evaluate(ctx context.Context, input nodehost.EvaluationInput) (nodehost.ProcessGrant, error) {
	principal, err := c.current(ctx)
	if err != nil {
		return nodehost.ProcessGrant{}, err
	}
	b, p := principal.Capability.Binding, principal.Capability
	owner := p.Unit == capability.UnitTrigger && b.Kind == capability.BindingTrigger && b.NodeID == "" && b.Generation > 0 && b.Holder != "" && b.Attempt == 0 && b.Expansion == nil
	evaluator := p.Unit == capability.UnitEval && b.Kind == capability.BindingNode && b.Expansion != nil && b.Expansion.Kind == capability.UnitEval && b.Expansion.ID == p.UnitID && b.NodeID == input.NodeID && b.Generation == input.Generation
	if (!owner && !evaluator) || input.Accepted.RunID != b.RunID || input.NodeID == "" || input.SpecHash == "" {
		return nodehost.ProcessGrant{}, nodeClaimMismatch()
	}
	row, err := c.readNode(ctx, store.NodeRef{RunID: b.RunID, NodeID: input.NodeID})
	if err != nil {
		return nodehost.ProcessGrant{}, err
	}
	if row.Ref != (store.NodeRef{RunID: b.RunID, NodeID: input.NodeID}) || row.Generation != input.Generation || row.SpecHash != input.SpecHash {
		return nodehost.ProcessGrant{}, nodeClaimMismatch()
	}
	var job nodeproto.JobSpec
	if err := json.Unmarshal(row.Spec, &job); err != nil {
		return nodehost.ProcessGrant{}, err
	}
	if job.ID != input.NodeID || c.services == nil {
		return nodehost.ProcessGrant{}, nodeClaimMismatch()
	}
	services, err := c.services(ctx, principal, job)
	if err != nil {
		return nodehost.ProcessGrant{}, err
	}
	if services.RunID != b.RunID || services.Job.ID != job.ID || !completeServices(services) {
		return nodehost.ProcessGrant{}, errors.New("evaluation services are incomplete or belong to another job")
	}
	services.SourceBindings = evaluationBindings{readNode: c.readNode, source: row.Ref, generation: row.Generation, hash: row.SpecHash}
	return c.processes.Delegate(ctx, capability.CredentialLookup{Class: capability.CredentialBearer, Audience: capability.Controller, Digest: p.Digest}, capability.DelegateSpec{
		Parent: principal.Authority, Audience: capability.NodeHost, Grants: p.Grants, Binding: b, Repositories: p.Repositories, Data: p.Data, ExpiresAt: p.ExpiresAt,
	}, services)
}

type evaluationBindings struct {
	readNode   func(context.Context, store.NodeRef) (store.NodeRow, error)
	source     store.NodeRef
	generation int64
	hash       string
}

func (b evaluationBindings) CheckBinding(ctx context.Context, p capability.Principal, rule capability.BindingRule, target capability.Target) error {
	row, err := b.readNode(ctx, b.source)
	if err != nil {
		return err
	}
	if row.Ref != b.source || row.Generation != b.generation || row.SpecHash != b.hash || p.Capability.Binding.RunID != b.source.RunID {
		return nodeClaimMismatch()
	}
	if rule == capability.BindReadSet {
		var source nodeproto.JobSpec
		if err := json.Unmarshal(row.Spec, &source); err != nil {
			return err
		}
		if source.ID != b.source.NodeID || target.RunID != b.source.RunID {
			return apierr.Authorization(apierr.ReasonBinding, "output is outside the evaluated job read set")
		}
		if target.Pipeline != "" {
			declared := false
			for _, ref := range source.References {
				if ref.Pipeline == target.Pipeline && ref.Job != "" && ref.Job == target.NodeID {
					declared = true
				}
			}
			if !declared {
				return apierr.Authorization(apierr.ReasonBinding, "output is outside the evaluated job read set")
			}
		} else if p.Capability.Unit == capability.UnitEval {
			if target.NodeID != b.source.NodeID {
				return apierr.Authorization(apierr.ReasonBinding, "output is outside the evaluated job read set")
			}
		} else {
			output, err := b.readNode(ctx, store.NodeRef{RunID: b.source.RunID, NodeID: target.NodeID})
			if err != nil {
				if errors.Is(err, store.ErrNotFound) {
					return apierr.Authorization(apierr.ReasonBinding, "output is outside the evaluated job read set")
				}
				return err
			}
			var job nodeproto.JobSpec
			if err := json.Unmarshal(output.Spec, &job); err != nil {
				return err
			}
			if output.Ref != (store.NodeRef{RunID: b.source.RunID, NodeID: target.NodeID}) || job.ID != target.NodeID || !capability.ReadsJobOutput(source, job) {
				return apierr.Authorization(apierr.ReasonBinding, "output is outside the evaluated job read set")
			}
		}
	}
	return nil
}
