package controller

import (
	"context"
	"math"
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/localhostwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func (s *Server) localHostEndpoints() []Endpoint {
	engine := &EngineController{Store: s.store}
	return []Endpoint{
		{Method: "GET", Pattern: "/api/v1/runs/{run}/local-host/data-authority", Authentication: capability.AuthCapability, Decode: runSelectorTarget, ResolveTarget: engine.resolveRunSelector, Handle: s.localHostDataAuthority},
		{Method: "GET", Pattern: "/api/v1/runs/{run}/local-host/nodes/{job}/data-authority", Authentication: capability.AuthCapability, Decode: runSelectorTarget, ResolveTarget: engine.resolveRunSelector, Handle: s.localHostDataAuthority},
		{Method: "GET", Pattern: "/api/v1/runs/{run}/local-host/nodes/{job}/snapshot", Authentication: capability.AuthCapability, Decode: runSelectorTarget, ResolveTarget: engine.resolveRunSelector, Handle: s.localNodeSnapshot},
		{Method: "POST", Pattern: "/api/v1/runs/{run}/local-host/nodes/{job}/authentication", Authentication: capability.AuthCapability, Decode: runSelectorTarget, ResolveTarget: engine.resolveRunSelector, Handle: s.localHostAuthentication},
		{Method: "POST", Pattern: "/api/v1/runs/{run}/local-host/nodes/{job}/binding", Authentication: capability.AuthCapability, Decode: runSelectorTarget, ResolveTarget: engine.resolveRunSelector, Handle: s.localHostBinding},
		{Method: "GET", Pattern: "/api/v1/runs/{run}/local-host/snapshot", Authentication: capability.AuthCapability, Decode: runSelectorTarget, ResolveTarget: engine.resolveRunSelector, Handle: s.localHostSnapshot},
		{Method: "GET", Pattern: "/api/v1/runs/{run}/local-host/nodes/{job}", Authentication: capability.AuthCapability, Decode: runSelectorTarget, ResolveTarget: engine.resolveRunSelector, Handle: s.localHostNode},
		{Method: "POST", Pattern: "/api/v1/runs/{run}/local-host/transition", Authentication: capability.AuthCapability, Decode: engine.runResultTarget, ResolveTarget: engine.resolveRunSelector, Handle: s.localHostRunTransition},
		{Method: "POST", Pattern: "/api/v1/runs/{run}/local-host/finish", Authentication: capability.AuthCapability, Decode: engine.runResultTarget, ResolveTarget: engine.resolveRunSelector, Handle: s.localHostFinish},
		{Method: "POST", Pattern: "/api/v1/runs/{run}/local-host/nodes/{job}/transition", Authentication: capability.AuthCapability, Decode: engine.runResultTarget, ResolveTarget: engine.resolveRunSelector, Handle: s.localHostNodeTransition},
		{Method: "POST", Pattern: "/api/v1/runs/{run}/local-host/authentication", Authentication: capability.AuthCapability, Decode: runSelectorTarget, ResolveTarget: engine.resolveRunSelector, Handle: s.localHostAuthentication},
		{Method: "POST", Pattern: "/api/v1/runs/{run}/local-host/binding", Authentication: capability.AuthCapability, Decode: runSelectorTarget, ResolveTarget: engine.resolveRunSelector, Handle: s.localHostBinding},
	}
}

func (s *Server) localNodeSnapshot(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	tenant, err := s.store.ForTeam(r.Context(), store.Team(a.Target.Team))
	if err != nil {
		return err
	}
	snapshot, err := tenant.ReadSnapshot(r.Context(), a.Target.RunID)
	if err != nil {
		return engineLookupError(err)
	}
	for _, node := range snapshot.Nodes {
		if node.Ref.NodeID == a.Target.NodeID {
			snapshot.Nodes = []store.NodeRow{node}
			writeJSON(w, http.StatusOK, snapshot)
			return nil
		}
	}
	return engineLookupError(store.ErrNotFound)
}

func (s *Server) localHostSnapshot(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	tenant, err := s.store.ForTeam(r.Context(), store.Team(a.Target.Team))
	if err != nil {
		return err
	}
	ctx := store.WithClaimPlacement(r.Context(), store.ClaimPlacement{Preferences: []store.PlacementPreference{{Prefers: s.placement.defaultPrefers, Hold: s.placement.hold}, {}}, Liveness: s.placement.liveness})
	snapshot, err := tenant.ReadOwnerSnapshot(ctx, a.Principal.Authority, a.Target.RunID)
	if err != nil {
		return engineLookupError(err)
	}
	writeJSON(w, http.StatusOK, snapshot)
	return nil
}

func (s *Server) localHostNode(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	tenant, err := s.store.ForTeam(r.Context(), store.Team(a.Target.Team))
	if err != nil {
		return err
	}
	if a.Principal.Capability.Unit == capability.UnitTrigger {
		ctx := store.WithClaimPlacement(r.Context(), store.ClaimPlacement{Preferences: []store.PlacementPreference{{Prefers: s.placement.defaultPrefers, Hold: s.placement.hold}, {}}, Liveness: s.placement.liveness})
		snapshot, err := tenant.ReadOwnerSnapshot(ctx, a.Principal.Authority, a.Target.RunID)
		if err != nil {
			return engineLookupError(err)
		}
		for _, node := range snapshot.Nodes {
			if node.Ref.NodeID == a.Target.NodeID {
				writeJSON(w, http.StatusOK, node)
				return nil
			}
		}
		return engineLookupError(store.ErrNotFound)
	}
	node, err := tenant.ReadNode(r.Context(), store.NodeRef{RunID: a.Target.RunID, NodeID: a.Target.NodeID})
	if err != nil {
		return engineLookupError(err)
	}
	writeJSON(w, http.StatusOK, node)
	return nil
}

func (s *Server) localHostRunTransition(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	return s.localHostRunEdge(w, r, a, false)
}

func (s *Server) localHostFinish(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	return s.localHostRunEdge(w, r, a, true)
}

func (s *Server) localHostRunEdge(w http.ResponseWriter, r *http.Request, a capability.Authorized, finish bool) error {
	var body localhostwire.LocalRunTransition
	if err := decodeEngineRequest(r, &body); err != nil {
		return err
	}
	if !finish && (body.Cause != store.RunStart || body.To != store.RunRunning) {
		return invalidEngineBody("run transition requires start; terminal verdicts require atomic owner completion")
	}
	if a.Principal.Capability.Unit == capability.UnitPlan && (!finish || body.Cause != store.RunFinish || body.To != store.RunFailed && body.To != store.RunCancelled || body.OwnerPolicy != nil) {
		return invalidEngineBody("plan completion requires a preparation failure or cancellation")
	}
	tenant, err := s.store.ForTeam(r.Context(), store.Team(a.Target.Team))
	if err != nil {
		return err
	}
	change := store.RunTransition{RunID: a.Target.RunID, Actor: engineActor(a), To: body.To, Cause: body.Cause, Reason: body.Reason, MaskedError: body.MaskedError, OwnerPolicy: body.OwnerPolicy}
	var applied store.Applied
	if finish {
		applied, err = tenant.FinishRunAndUnit(r.Context(), store.FinishRunRequest{Transition: change, Unit: change.Actor.Owner})
	} else {
		applied, err = tenant.TransitionRun(r.Context(), change)
	}
	if err != nil {
		return engineLookupError(err)
	}
	writeJSON(w, http.StatusOK, applied)
	return nil
}

func (s *Server) localHostNodeTransition(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	var body localhostwire.LocalNodeTransition
	if err := decodeEngineRequest(r, &body); err != nil {
		return err
	}
	tenant, err := s.store.ForTeam(r.Context(), store.Team(a.Target.Team))
	if err != nil {
		return err
	}
	extra := body.Extra
	change := store.NodeTransition{
		Ref: store.NodeRef{RunID: a.Target.RunID, NodeID: a.Target.NodeID}, Actor: engineActor(a), To: body.To, Cause: body.Cause, SourceAttempt: body.SourceAttempt,
		Extra: store.TransitionExtra{OwnerPolicy: extra.OwnerPolicy, Result: extra.Result, Materialize: extra.Materialize, Approval: extra.Approval, RetryAt: extra.RetryAt},
	}
	if body.Cause == store.CauseStart && a.Principal.Capability.Unit == capability.UnitNode {
		if body.SourceAttempt == nil || *body.SourceAttempt < 0 || *body.SourceAttempt == math.MaxInt64 {
			return invalidEngineBody("start requires a valid captured source attempt")
		}
		profile, _, err := s.store.ReadClaimProfile(r.Context(), a.Principal.Authority)
		if err != nil {
			return err
		}
		change.Extra.Attempt = &store.NodeAttempt{
			Identity: runspec.AttemptIdentity{Team: a.Target.Team, RunID: a.Target.RunID, NodeID: a.Target.NodeID, Generation: a.Binding.Generation, Attempt: *body.SourceAttempt + 1},
			Holder:   a.Binding.Holder, CapabilityPrefix: a.Principal.Capability.Prefix, ExecutorName: profile.Name, ExecutorKind: profile.Class, ExecutorLocation: profile.Location,
		}
	}
	applied, err := tenant.TransitionNode(r.Context(), change)
	if err != nil {
		return engineLookupError(err)
	}
	writeJSON(w, http.StatusOK, applied)
	return nil
}

func (s *Server) localHostAuthentication(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	var body localhostwire.LocalAuthenticationRequest
	if err := decodeEngineRequest(r, &body); err != nil {
		return err
	}
	if body.Lookup.Class != capability.CredentialBearer || body.Lookup.Audience != capability.Controller || body.Lookup.Peer != (capability.Subject{}) {
		return localHostRefusal()
	}
	target, err := localHostTarget(a, body.Evidence.Target)
	if err != nil {
		return err
	}
	body.Evidence.Target = target
	principal, err := s.localHostAuthority(r.Context(), a, capability.AuthorityRef{}, body.Lookup.Digest)
	if err != nil {
		return err
	}
	snapshot, err := s.store.ReadAuthentication(r.Context(), body.Lookup, body.Evidence)
	if err != nil {
		return err
	}
	if len(snapshot.Authorities) == 0 || snapshot.Authorities[0].Principal.Authority != principal.Authority {
		return localHostRefusal()
	}
	response := localhostwire.LocalAuthentication{Snapshot: snapshot, Digests: make([][32]byte, len(snapshot.Authorities))}
	for i, authority := range snapshot.Authorities {
		response.Digests[i] = authority.Principal.Capability.Digest
	}
	writeJSON(w, http.StatusOK, response)
	return nil
}

func (s *Server) localHostBinding(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	var body localhostwire.LocalBindingRequest
	if err := decodeEngineRequest(r, &body); err != nil {
		return err
	}
	target, err := localHostTarget(a, body.Target)
	if err != nil {
		return err
	}
	principal, err := s.localHostAuthority(r.Context(), a, body.Authority, [32]byte{})
	if err != nil {
		return err
	}
	if err := s.store.CheckBinding(r.Context(), principal, body.Rule, target); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, localhostwire.LocalBindingChecked{Checked: true})
	return nil
}

func localHostTarget(a capability.Authorized, target capability.Target) (capability.Target, error) {
	if a.Principal.Capability.Unit == capability.UnitNode {
		if target.NodeID != "" && target.NodeID != a.Target.NodeID {
			return capability.Target{}, localHostRefusal()
		}
		target.NodeID = a.Target.NodeID
	}
	if target.Team != "" && target.Team != a.Target.Team || target.RunID != "" && target.RunID != a.Target.RunID {
		return capability.Target{}, localHostRefusal()
	}
	target.Team, target.RunID = a.Target.Team, a.Target.RunID
	return target, nil
}

func (s *Server) localHostAuthority(ctx context.Context, a capability.Authorized, ref capability.AuthorityRef, digest [32]byte) (capability.Principal, error) {
	owner := a.Principal
	if ref == owner.Authority || digest != ([32]byte{}) && digest == owner.Capability.Digest {
		return owner, nil
	}
	if owner.Capability.Unit != capability.UnitTrigger || ref.Kind != "" && ref.Kind != capability.AuthorityCapability {
		return capability.Principal{}, localHostRefusal()
	}
	tenant, err := s.store.ForTeam(ctx, store.Team(a.Target.Team))
	if err != nil {
		return capability.Principal{}, err
	}
	snapshot, err := tenant.ReadOwnerSnapshot(ctx, owner.Authority, a.Target.RunID)
	if err != nil {
		return capability.Principal{}, engineLookupError(err)
	}
	for _, node := range snapshot.Nodes {
		if node.OwnerCapabilityPrefix != owner.Capability.Prefix || node.CapabilityPrefix == "" || ref.ID != "" && ref.ID != node.CapabilityPrefix {
			continue
		}
		principal, err := s.store.ResolveAuthority(ctx, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: node.CapabilityPrefix})
		if err != nil {
			return capability.Principal{}, err
		}
		if localHostOwnsNode(owner, node, principal) && (ref == principal.Authority || digest != ([32]byte{}) && digest == principal.Capability.Digest) {
			return principal, nil
		}
	}
	return capability.Principal{}, localHostRefusal()
}

func localHostOwnsNode(owner capability.Principal, node store.NodeRow, child capability.Principal) bool {
	c, b := child.Capability, child.Capability.Binding
	return owner.Authority.Kind == capability.AuthorityCapability && owner.Capability.Prefix != "" &&
		owner.Capability.Unit == capability.UnitTrigger && owner.Capability.Binding.RunID != "" &&
		node.Ref.RunID == owner.Capability.Binding.RunID && node.Ref.NodeID != "" && node.OwnerCapabilityPrefix == owner.Capability.Prefix &&
		node.CapabilityPrefix != "" && node.Generation > 0 && node.ClaimedBy == owner.Capability.Binding.Holder &&
		child.Authority == (capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: node.CapabilityPrefix}) &&
		child.Class == capability.CredentialBearer && c.Audience == capability.Controller && c.Team == owner.Capability.Team && c.Subject == owner.Capability.Subject &&
		c.Prefix == node.CapabilityPrefix && c.Unit == capability.UnitNode && c.Purpose == capability.PurposeWork &&
		b.Kind == capability.BindingNode && b.RunID == node.Ref.RunID && b.NodeID == node.Ref.NodeID && b.Generation == node.Generation && b.Holder == node.ClaimedBy
}

func localHostRefusal() error {
	return &apierr.Error{Code: apierr.Forbidden, Message: "authority is outside the local host award"}
}
