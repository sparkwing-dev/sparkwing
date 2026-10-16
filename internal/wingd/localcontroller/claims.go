package localcontroller

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type nodeClaims struct {
	auth      capability.Authenticator
	bindings  capability.BindingChecker
	masker    *secrets.Masker
	current   func(context.Context) (capability.Principal, error)
	readNode  func(context.Context, store.NodeRef) (store.NodeRow, error)
	processes *nodehost.NativeProcesses
	services  func(context.Context, capability.Principal, nodeproto.JobSpec) (nodehost.ProcessServices, error)
	mu        sync.Mutex
	nodes     map[string]nodeClaim
}

type nodeClaim struct {
	raw        string
	prefix     string
	generation int64
}

func (c *nodeClaims) adopt(ctx context.Context, result store.ReconciledNode) error {
	owner, err := c.current(ctx)
	if err != nil {
		return err
	}
	row, claim := result.Node, result.Claim
	if result.Kind != store.Execute || claim == nil || len(result.Bearer) == 0 || row.OwnerCapabilityPrefix != owner.Capability.Prefix || row.Ref.RunID != owner.Capability.Binding.RunID || row.Ref.NodeID == "" || row.Generation != claim.Generation || row.CapabilityPrefix != claim.CapabilityPrefix || row.ClaimedBy != claim.Holder {
		return nodeClaimMismatch()
	}
	award := nodeClaim{raw: string(result.Bearer), prefix: row.CapabilityPrefix, generation: row.Generation}
	if _, err := c.authenticate(ctx, owner, award, row.Ref.NodeID, row.Attempt); err != nil {
		return err
	}
	c.masker.Register(award.raw)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.nodes == nil {
		c.nodes = make(map[string]nodeClaim)
	}
	c.nodes[row.Ref.NodeID] = award
	return nil
}

func (c *nodeClaims) principal(ctx context.Context, node string, generation, attempt int64) (capability.Principal, error) {
	owner, err := c.current(ctx)
	if err != nil {
		return capability.Principal{}, err
	}
	fence := owner.Capability.Binding
	if fence.Kind == capability.BindingNode && fence.NodeID == node && fence.Generation == generation && (fence.Attempt == 0 || fence.Attempt == attempt) {
		return owner, nil
	}
	c.mu.Lock()
	award, ok := c.nodes[node]
	c.mu.Unlock()
	if !ok || generation != award.generation {
		return capability.Principal{}, nodeClaimMismatch()
	}
	return c.authenticate(ctx, owner, award, node, attempt)
}

func (c *nodeClaims) authenticate(ctx context.Context, owner capability.Principal, award nodeClaim, node string, attempt int64) (capability.Principal, error) {
	route, ok := capability.LookupRoute("POST", "/api/v1/runs/{run}/jobs/{job}/claim/input")
	if !ok {
		return capability.Principal{}, errors.New("local node input policy is unavailable")
	}
	target := capability.Target{Team: owner.Capability.Team, RunID: owner.Capability.Binding.RunID, NodeID: node}
	authenticated, err := c.auth.Authenticate(ctx, capability.Presentation{Class: capability.CredentialBearer, Raw: award.raw, Evidence: capability.RequestEvidence{Target: target}}, route)
	if err != nil {
		return capability.Principal{}, err
	}
	authorized, err := capability.Authorize(ctx, c.bindings, route, authenticated, target)
	if err != nil {
		return capability.Principal{}, err
	}
	p := authorized.Principal
	b := p.Capability.Binding
	if p.Authority != (capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: award.prefix}) || p.Capability.Team != target.Team || p.Capability.Subject != owner.Capability.Subject || p.Capability.Unit != capability.UnitNode || p.Capability.Purpose != capability.PurposeWork || b.Kind != capability.BindingNode || b.RunID != target.RunID || b.NodeID != node || b.Generation != award.generation || b.Attempt != 0 && b.Attempt != attempt || b.Holder != owner.Capability.Binding.Holder {
		return capability.Principal{}, nodeClaimMismatch()
	}
	return p, nil
}

func nodeClaimMismatch() error {
	return &apierr.Error{Code: apierr.LockHeld, Message: "local process requires its independently awarded current node authority"}
}

func (c *nodeClaims) delegate(ctx context.Context, node string, generation, attempt int64) (nodehost.ProcessGrant, error) {
	principal, err := c.principal(ctx, node, generation, attempt)
	if err != nil {
		return nodehost.ProcessGrant{}, err
	}
	fence := principal.Capability.Binding
	row, err := c.readNode(ctx, store.NodeRef{RunID: fence.RunID, NodeID: node})
	if err != nil {
		return nodehost.ProcessGrant{}, err
	}
	if row.Ref != (store.NodeRef{RunID: fence.RunID, NodeID: node}) || row.Generation != generation || row.Attempt != attempt || row.CapabilityPrefix != principal.Capability.Prefix || row.ClaimedBy != fence.Holder {
		return nodehost.ProcessGrant{}, nodeClaimMismatch()
	}
	var job nodeproto.JobSpec
	if err := json.Unmarshal(row.Spec, &job); err != nil {
		return nodehost.ProcessGrant{}, err
	}
	fence.Attempt = attempt
	data := principal.Capability.Data
	if data != nil {
		copy := *data
		copy.Attempt = attempt
		data = &copy
	}
	lookup := capability.CredentialLookup{Class: capability.CredentialBearer, Audience: capability.Controller, Digest: principal.Capability.Digest}
	if c.services == nil {
		return nodehost.ProcessGrant{}, errors.New("local process services are unavailable")
	}
	services, err := c.services(ctx, principal, job)
	if err != nil {
		return nodehost.ProcessGrant{}, err
	}
	if services.RunID != row.Ref.RunID || services.Job.ID != job.ID || !completeServices(services) {
		return nodehost.ProcessGrant{}, errors.New("local process services are incomplete or belong to another job")
	}
	return c.processes.Delegate(ctx, lookup, capability.DelegateSpec{
		Parent: principal.Authority, Audience: capability.NodeHost, Grants: principal.Capability.Grants, Binding: fence,
		Repositories: principal.Capability.Repositories, Data: data, ExpiresAt: principal.Capability.ExpiresAt,
	}, services)
}

func completeServices(s nodehost.ProcessServices) bool {
	return s.Secret != nil && s.Output != nil && s.Latest != nil && s.OIDC != nil && s.Annotation != nil && s.Summary != nil && s.StartChild != nil && s.AwaitChild != nil
}
