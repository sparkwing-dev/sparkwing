package controller

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/localhostwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func (s *Server) localHostEndpoints() []controllerwire.Endpoint {
	engine := s.engineController
	return []controllerwire.Endpoint{
		{Method: "POST", Pattern: "/api/v1/runs/{run}/local-host/may-start", Authentication: capability.AuthCapability, Decode: runSelectorTarget, ResolveTarget: engine.resolveRunSelector, Handle: s.localHostCapacity},
		{Method: "POST", Pattern: "/api/v1/runs/{run}/local-host/release-evaluation", Authentication: capability.AuthCapability, Decode: runSelectorTarget, ResolveTarget: engine.resolveRunSelector, Handle: s.localHostReleaseEvaluation},
		{Method: "POST", Pattern: "/api/v1/runs/{run}/local-host/relinquish-start", Authentication: capability.AuthCapability, Decode: runSelectorTarget, ResolveTarget: engine.resolveRunSelector, Handle: s.localHostRelinquishStart},
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
	tenant, err := s.tenantForTeam(r.Context(), store.Team(a.Target.Team))
	if err != nil {
		return err
	}
	snapshot, err := tenant.ReadSnapshot(r.Context(), a.Target.RunID)
	if err != nil {
		return engineLookupError(err)
	}
	for _, node := range snapshot.Nodes {
		if node.Ref.NodeID == runspec.NodeID(a.Target.NodeID) {
			var job nodeproto.JobSpec
			if err := json.Unmarshal(node.Spec, &job); err != nil {
				return err
			}
			sources := map[string]blob.RecordKey{}
			for _, source := range snapshot.Nodes {
				if source.Ref == node.Ref || source.Status != runstate.StatusDone || source.ManifestRecord == nil || !slices.ContainsFunc(job.Consumes, func(edge nodeproto.ArtifactInput) bool { return runspec.NodeID(edge.Producer) == source.Ref.NodeID }) {
					continue
				}
				sources[string(source.Ref.NodeID)] = *source.ManifestRecord
			}
			ancestors := generatorSources(snapshot.Expansions, string(node.Ref.NodeID))
			snapshot.Nodes = slices.DeleteFunc(snapshot.Nodes, func(row runstate.NodeRow) bool { return row.Ref != node.Ref && !ancestors[string(row.Ref.NodeID)] })
			writeJSON(w, http.StatusOK, localhostwire.LocalNodeSnapshot{RunSnapshot: snapshot.ExecutionSnapshot(), ArtifactSources: sources})
			return nil
		}
	}
	return engineLookupError(store.ErrNotFound)
}

func generatorSources(expansions []runstate.AcceptedExpansionRow, id string) map[string]bool {
	parents := map[string]string{}
	for _, expansion := range expansions {
		var plan nodeproto.PlanDoc
		if json.Unmarshal(expansion.Body, &plan) != nil {
			continue
		}
		for _, job := range plan.Jobs {
			parents[job.ID] = string(expansion.Source.NodeID)
		}
	}
	sources := map[string]bool{}
	for parent, ok := parents[id]; ok && !sources[parent]; parent, ok = parents[parent] {
		sources[parent] = true
	}
	return sources
}

func (s *Server) localHostSnapshot(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	tenant, err := s.tenantForTeam(r.Context(), store.Team(a.Target.Team))
	if err != nil {
		return err
	}
	ctx := s.placementContext(r.Context())
	var snapshot store.RunSnapshot
	if a.Access.Credential == capability.CredentialLocalPeer {
		publication, err := tenant.ReadLocalPublication(ctx, a.Target.RunID)
		if err != nil {
			return engineLookupError(err)
		}
		writeJSON(w, http.StatusOK, runstate.LocalPublication{RunSnapshot: publication.RunSnapshot.ExecutionSnapshot(), Records: publication.Records, Results: publication.Results})
		return nil
	} else {
		snapshot, err = tenant.ReadOwnerSnapshot(ctx, a.Principal.Authority, a.Target.RunID)
	}
	if err != nil {
		return engineLookupError(err)
	}
	writeJSON(w, http.StatusOK, snapshot.ExecutionSnapshot())
	return nil
}

func (s *Server) localHostNode(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	tenant, err := s.tenantForTeam(r.Context(), store.Team(a.Target.Team))
	if err != nil {
		return err
	}
	if a.Principal.Capability.Unit == capability.UnitTrigger {
		ctx := s.placementContext(r.Context())
		node, err := tenant.ReadOwnerNode(ctx, a.Principal.Authority, runspec.NodeRef{RunID: runspec.RunID(a.Target.RunID), NodeID: runspec.NodeID(a.Target.NodeID)})
		if err != nil {
			return engineLookupError(err)
		}
		writeJSON(w, http.StatusOK, node)
		return nil
	}
	node, err := tenant.ReadNode(r.Context(), runspec.NodeRef{RunID: runspec.RunID(a.Target.RunID), NodeID: runspec.NodeID(a.Target.NodeID)})
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
	if finish {
		var complete localhostwire.LocalRunCompletion
		if err := decodeEngineRequest(r, &complete); err != nil {
			return err
		}
		body = localhostwire.LocalRunTransition{Cause: complete.Cause, Reason: complete.Reason, MaskedError: complete.MaskedError}
	} else if err := decodeEngineRequest(r, &body); err != nil {
		return err
	}
	if !finish && (body.Cause != runstate.RunStart || body.To != runstate.RunRunning) {
		return invalidEngineBody("run transition requires start; terminal verdicts require atomic owner completion")
	}
	if a.Principal.Capability.Unit == capability.UnitPlan && (!finish || body.Cause != runstate.RunFinish) {
		return invalidEngineBody("plan completion requires a preparation failure or cancellation")
	}
	tenant, err := s.tenantForTeam(r.Context(), store.Team(a.Target.Team))
	if err != nil {
		return err
	}
	change := runstate.RunTransition{RunID: runspec.RunID(a.Target.RunID), Actor: engineActor(a), To: body.To, Cause: body.Cause, Reason: body.Reason, MaskedError: body.MaskedError}
	var applied runstate.Applied
	if finish {
		applied, err = tenant.FinishRunAndUnit(r.Context(), runstate.FinishRunRequest{Transition: runstate.RunCompletion{RunID: change.RunID, Actor: change.Actor, Cause: change.Cause, Reason: change.Reason, MaskedError: change.MaskedError}, Unit: change.Actor.Owner})
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
	var body runstate.NodeCommandPayload
	if err := decodeEngineRequest(r, &body); err != nil {
		return err
	}
	tenant, err := s.tenantForTeam(r.Context(), store.Team(a.Target.Team))
	if err != nil {
		return err
	}
	change, err := store.NodeCommandFromPayload(body, runspec.NodeRef{RunID: runspec.RunID(a.Target.RunID), NodeID: runspec.NodeID(a.Target.NodeID)}, engineActor(a))
	if err != nil {
		return invalidEngineBody(err.Error())
	}
	if body.Cause == runstate.CauseStart && a.Principal.Capability.Unit == capability.UnitNode {
		if body.SourceAttempt == nil || *body.SourceAttempt < 0 || *body.SourceAttempt == math.MaxInt64 {
			return invalidEngineBody("start requires a valid captured source attempt")
		}
		profile, err := s.operator.ReadClaimProfile(r.Context(), a.Principal.Authority)
		if err != nil {
			return err
		}
		start := change.(store.StartNode)
		start.Attempt = &runstate.NodeAttempt{
			Identity: runspec.AttemptIdentity{Team: a.Target.Team, RunID: a.Target.RunID, NodeID: a.Target.NodeID, Generation: a.Binding.Generation, Attempt: int64(*body.SourceAttempt + 1)},
			Holder:   a.Binding.Holder, CapabilityPrefix: a.Principal.Capability.Prefix, ExecutorName: profile.Name, ExecutorKind: profile.Class, ExecutorLocation: profile.Location,
		}
		change = start
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
	snapshot, err := s.operator.ReadAuthentication(r.Context(), body.Lookup, body.Evidence)
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
	if err := s.operator.CheckBinding(r.Context(), principal, body.Rule, target); err != nil {
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
	tenant, err := s.tenantForTeam(ctx, store.Team(a.Target.Team))
	if err != nil {
		return capability.Principal{}, err
	}
	nodes, err := tenant.ReadOwnerNodes(ctx, owner.Authority, a.Target.RunID)
	if err != nil {
		return capability.Principal{}, engineLookupError(err)
	}
	for _, node := range nodes {
		if node.OwnerCapabilityPrefix != owner.Capability.Prefix || node.CapabilityPrefix == "" || ref.ID != "" && ref.ID != node.CapabilityPrefix {
			continue
		}
		principal, err := s.operator.ResolveAuthority(ctx, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: node.CapabilityPrefix})
		if err != nil {
			return capability.Principal{}, err
		}
		if localHostOwnsNode(owner, node, principal) && (ref == principal.Authority || digest != ([32]byte{}) && digest == principal.Capability.Digest) {
			return principal, nil
		}
	}
	return capability.Principal{}, localHostRefusal()
}

func localHostOwnsNode(owner capability.Principal, node runstate.NodeRow, child capability.Principal) bool {
	c, b := child.Capability, child.Capability.Binding
	return owner.Authority.Kind == capability.AuthorityCapability && owner.Capability.Prefix != "" &&
		owner.Capability.Unit == capability.UnitTrigger && owner.Capability.Binding.RunID != "" &&
		node.Ref.RunID == runspec.RunID(owner.Capability.Binding.RunID) && node.Ref.NodeID != "" && node.OwnerCapabilityPrefix == owner.Capability.Prefix &&
		node.CapabilityPrefix != "" && node.Generation > 0 && node.ClaimedBy == owner.Capability.Binding.Holder &&
		child.Authority == (capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: node.CapabilityPrefix}) &&
		child.Class == capability.CredentialBearer && c.Audience == capability.Controller && c.Team == owner.Capability.Team && c.Subject == owner.Capability.Subject &&
		c.Prefix == node.CapabilityPrefix && c.Unit == capability.UnitNode && c.Purpose == capability.PurposeWork &&
		b.Kind == capability.BindingNode && runspec.RunID(b.RunID) == node.Ref.RunID && runspec.NodeID(b.NodeID) == node.Ref.NodeID && runspec.ClaimGeneration(b.Generation) == node.Generation && b.Holder == node.ClaimedBy
}

func localHostRefusal() error {
	return &apierr.Error{Code: apierr.Forbidden, Message: "authority is outside the local host award"}
}
