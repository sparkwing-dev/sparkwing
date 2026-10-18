package controller

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// RunnerAdmissionPolicy checks server-owned software policy before a new award.
// Renewals do not call it because an existing runner must remain able to drain.
type RunnerAdmissionPolicy interface {
	CheckRunner(context.Context, string) error
}

// SignedRunnerAdmission uses the engine owner's verified index and runner policy.
type SignedRunnerAdmission struct {
	Resolver *engine.Resolver
}

// CheckRunner applies runner release policy for configured release builds.
func (p *SignedRunnerAdmission) CheckRunner(ctx context.Context, version string) error {
	if p.Resolver == nil {
		return &apierr.Error{Code: apierr.Unavailable, Message: "runner version policy is unavailable"}
	}
	now := time.Now().UTC()
	index, err := p.Resolver.Index(ctx, now)
	if err != nil {
		return err
	}
	_, err = engine.EvaluateRunner(index.Index, version, now)
	return err
}

// CheckRunner revalidates the claimant and selects version policy from its persisted subject.
func (s *EngineController) CheckRunner(ctx context.Context, authority capability.AuthorityRef, version string) (capability.Principal, error) {
	if s.Store == nil || s.RunnerPolicy == nil {
		return capability.Principal{}, &apierr.Error{Code: apierr.Unavailable, Message: "claim enrollment and runner policy are not configured"}
	}
	principal, err := s.Store.ResolveAuthority(ctx, authority)
	if err != nil {
		return capability.Principal{}, err
	}
	policy := s.RunnerPolicy
	if principal.Capability.Subject.Kind == capability.SubjectLocal && s.LocalRunnerPolicy != nil {
		policy = s.LocalRunnerPolicy
	}
	return principal, policy.CheckRunner(ctx, version)
}

func (s *EngineController) claim(ctx context.Context, authorized capability.Authorized, body controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
	if authorized.Principal.Authority.Kind != capability.AuthorityCapability || authorized.Principal.Authority.ID == "" || authorized.Principal.Authority.ID != authorized.Principal.Capability.Prefix {
		return controllerwire.ClaimResponse{}, invalidEngineBody("claim requires capability authority")
	}
	if body.RunnerVersion == "" {
		return controllerwire.ClaimResponse{}, invalidEngineBody("claim requires a runner version")
	}
	if len(body.Kinds) == 0 {
		return controllerwire.ClaimResponse{}, invalidEngineBody("claim requires unit kinds")
	}
	principal, err := s.CheckRunner(ctx, authorized.Principal.Authority, body.RunnerVersion)
	if err != nil {
		return controllerwire.ClaimResponse{}, err
	}
	profile, _, err := s.Store.ReadClaimProfile(ctx, principal.Authority)
	if err != nil {
		return controllerwire.ClaimResponse{}, err
	}
	target, err := engineClaimTarget(body)
	if err != nil {
		return controllerwire.ClaimResponse{}, err
	}
	if principal.Capability.Purpose == capability.PurposeHere || principal.Capability.Subject.Kind == capability.SubjectMachine && principal.Capability.Binding.Kind == capability.BindingNone {
		profile.OS, profile.Arch = body.OS, body.Arch
		profile.Capacity = match.Resources{Cores: body.AvailableCores, MemoryBytes: body.AvailableMemoryBytes}
		if body.Capacity != nil {
			profile.Capacity = *body.Capacity
		}
	}
	if principal.Capability.Purpose == capability.PurposeHere || principal.Capability.Subject.Kind == capability.SubjectMachine && principal.Capability.Binding.Kind == capability.BindingNone {
		if err := s.Store.RecordRunnerPoll(ctx, store.UnitDemand{CapabilityPrefix: principal.Capability.Prefix, Labels: body.Labels, OS: profile.OS, Arch: profile.Arch, Capacity: profile.Capacity, Available: match.Resources{Cores: body.AvailableCores, MemoryBytes: body.AvailableMemoryBytes}}); err != nil {
			return controllerwire.ClaimResponse{}, err
		}
	}
	claim, err := s.Store.ClaimUnit(ctx, store.UnitDemand{CapabilityPrefix: principal.Capability.Prefix, Kinds: body.Kinds, Labels: slices.Clone(body.Labels), Target: target, OS: profile.OS, Arch: profile.Arch, Capacity: profile.Capacity, Available: match.Resources{Cores: body.AvailableCores, MemoryBytes: body.AvailableMemoryBytes}})
	if target == nil && errors.Is(err, store.ErrNotFound) {
		observeEmptyClaim(principal.Capability)
		return controllerwire.ClaimResponse{}, nil
	}
	if err != nil {
		return controllerwire.ClaimResponse{}, err
	}
	unit, err := s.claimedUnit(ctx, claim)
	if err != nil {
		return controllerwire.ClaimResponse{}, err
	}
	return controllerwire.ClaimResponse{Claim: &unit}, nil
}

func engineClaimTarget(body controllerwire.ClaimRequest) (*store.UnitRef, error) {
	if body.RunID == "" {
		if body.JobID != "" {
			return nil, invalidEngineBody("named job requires a run")
		}
		return nil, nil
	}
	target := &store.UnitRef{RunID: body.RunID}
	switch {
	case body.JobID != "":
		target.Kind, target.NodeID = capability.UnitNode, body.JobID
	case slices.Contains(body.Kinds, capability.UnitTrigger) && !slices.Contains(body.Kinds, capability.UnitPlan):
		target.Kind = capability.UnitTrigger
	case slices.Contains(body.Kinds, capability.UnitPlan) && !slices.Contains(body.Kinds, capability.UnitTrigger):
		target.Kind = capability.UnitPlan
	default:
		return nil, invalidEngineBody("named owner claim requires a trigger or plan kind")
	}
	return target, nil
}

func (s *EngineController) claimedUnit(ctx context.Context, claim store.UnitClaim) (controllerwire.ClaimedUnit, error) {
	principal, err := s.Store.ResolveAuthority(ctx, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix})
	if err != nil {
		return controllerwire.ClaimedUnit{}, err
	}
	tenant, err := s.Store.ForTeam(ctx, claim.Team)
	if err != nil {
		return controllerwire.ClaimedUnit{}, err
	}
	run, err := tenant.ReadRun(ctx, claim.RunID)
	if err != nil {
		return controllerwire.ClaimedUnit{}, err
	}
	var node store.NodeRow
	if claim.Kind == capability.UnitNode {
		node, err = tenant.ReadNode(ctx, store.NodeRef{RunID: claim.RunID, NodeID: claim.NodeID})
		if err != nil {
			return controllerwire.ClaimedUnit{}, err
		}
	}
	unit := engineReconciledClaim(principal.Capability, store.ReconciledNode{Node: node, Claim: &claim.Award, Bearer: claim.Bearer}, run)
	unit.RunID, unit.JobID, unit.SourceGeneration = claim.RunID, claim.NodeID, claim.SourceGeneration
	switch claim.Kind {
	case capability.UnitPlan, capability.UnitEval:
		resources := run.Request.Request.Options.Resources
		if run.Spec != nil {
			resources = run.Spec.Resolved.Settings.Options.Resources
		}
		unit.Execution = &runspec.JobExecutionSettings{Resources: resources}
	case capability.UnitTrigger:
		unit.Execution = nil
	}
	unit.CloudResources, err = s.Store.CloudAwardResources(ctx, principal.Authority)
	if err != nil {
		return controllerwire.ClaimedUnit{}, err
	}
	if principal.Capability.Delivery == capability.DeliveryDirect && run.Request.Request.Origin != runspec.OriginLocal && len(run.Request.Request.Env.Names) > 0 {
		unit.SubmittedEnv, err = tenant.ReadSubmittedEnvironment(ctx, principal.Authority)
		if err != nil {
			return controllerwire.ClaimedUnit{}, err
		}
	}
	if parent := principal.Capability.Parent; parent != nil {
		p, err := s.Store.ResolveAuthority(ctx, *parent)
		if err != nil {
			return controllerwire.ClaimedUnit{}, err
		}
		unit.ParentCapabilityExpiry = p.Capability.ExpiresAt
	}
	return *unit, nil
}

func (s *EngineController) exchange(ctx context.Context, authorized capability.Authorized) (controllerwire.ExchangeResponse, error) {
	if s.Store == nil {
		return controllerwire.ExchangeResponse{}, missingEndpointPolicy()
	}
	ref := authorized.Principal.Authority
	if ref.Kind != capability.AuthorityCapability || ref.ID == "" || ref.ID != authorized.Principal.Capability.Prefix {
		return controllerwire.ExchangeResponse{}, invalidEngineBody("exchange requires bootstrap authority")
	}
	issued, err := s.Store.ExchangeCapability(ctx, capability.ExchangeSpec{BootstrapPrefix: ref.ID})
	if err != nil {
		return controllerwire.ExchangeResponse{}, err
	}
	c := issued.Capability
	snapshot, err := s.Store.ReadAuthentication(ctx, capability.CredentialLookup{Class: capability.CredentialBearer, Digest: c.Digest, Audience: capability.Controller}, capability.RequestEvidence{})
	if err != nil {
		return controllerwire.ExchangeResponse{}, err
	}
	claim := store.UnitClaim{Team: store.Team(c.Team), Kind: c.Unit, RunID: c.Binding.RunID, NodeID: c.Binding.NodeID, Bearer: []byte(issued.Raw), Award: store.ClaimAward{CapabilityPrefix: c.Prefix, Holder: c.Binding.Holder, Generation: c.Binding.Generation, LeaseExpiresAt: snapshot.Binding.LeaseExpiresAt}}
	if c.Unit == capability.UnitEval {
		if snapshot.Binding.Expansion == nil {
			return controllerwire.ExchangeResponse{}, invalidEngineBody("exchanged evaluation has no lease")
		}
		claim.SourceGeneration = c.Binding.Generation
		claim.Award.Generation = snapshot.Binding.Expansion.Lease.Generation
		claim.Award.LeaseExpiresAt = snapshot.Binding.Expansion.ExpiresAt
	}
	unit, err := s.claimedUnit(ctx, claim)
	if err != nil {
		return controllerwire.ExchangeResponse{}, err
	}
	return controllerwire.ExchangeResponse{Claim: unit}, nil
}
