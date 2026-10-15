package controller

import (
	"net/http"
	"slices"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func (s *Server) claimEndpoints() []Endpoint {
	e := s.engineController
	if e == nil {
		e = &EngineController{Store: s.store}
	}
	return []Endpoint{
		{Method: "POST", Pattern: "/api/v1/runners/claim", Authentication: capability.AuthCapability, Decode: e.claimTarget, Handle: s.claimHandler(e)},
		{Method: "POST", Pattern: "/api/v1/launcher/claim", Authentication: capability.AuthCapability, Decode: e.claimTarget, Handle: s.claimHandler(e)},
		{Method: "POST", Pattern: "/api/v1/launcher/sync", Authentication: capability.AuthCapability, Decode: launcherSyncTarget, Handle: e.handleSyncLauncher},
		{Method: "POST", Pattern: "/api/v1/runs/{run}/claim", Authentication: capability.AuthCapability, Decode: e.claimTarget, Handle: s.claimHandler(e)},
		{Method: "POST", Pattern: "/api/v1/claims/exchange", Authentication: capability.AuthCapability, Decode: authorityTarget, ResolveTarget: ownUnitTarget, Handle: e.handleExchange},
		{Method: "POST", Pattern: "/api/v1/claims/{claim}/heartbeat", Authentication: capability.AuthCapability, Decode: claimIDTarget, ResolveTarget: ownUnitTarget, Handle: e.handleRenewal},
		{Method: "POST", Pattern: "/api/v1/runs/{run}/jobs/{job}/reconcile", Authentication: capability.AuthCapability, Decode: e.runTarget, Handle: func(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
			ctx := store.WithClaimPlacement(r.Context(), store.ClaimPlacement{DefaultPrefers: s.placement.defaultPrefers, Hold: s.placement.hold, Liveness: s.placement.liveness})
			return e.handleReconcile(w, r.WithContext(ctx), a)
		}},
	}
}

func launcherSyncTarget(*http.Request) (capability.RequestEvidence, error) {
	return capability.RequestEvidence{Target: capability.Target{ClaimKinds: []capability.UnitKind{capability.UnitPlan, capability.UnitEval, capability.UnitNode}}}, nil
}

func claimIDTarget(r *http.Request) (capability.RequestEvidence, error) {
	return capability.RequestEvidence{Target: capability.Target{ClaimID: r.PathValue("claim")}}, nil
}

func ownUnitTarget(p capability.Principal, target capability.Target) (capability.Target, error) {
	c, b := p.Capability, p.Capability.Binding
	target.Team, target.RunID, target.NodeID = c.Team, b.RunID, b.NodeID
	target.Unit, target.UnitID, target.UnitGeneration, target.UnitHolder = c.Unit, c.UnitID, b.Generation, b.Holder
	if lease := b.Expansion; lease != nil {
		target.UnitGeneration, target.UnitHolder = lease.Generation, lease.Holder
		target.UnitSourceNodeID, target.UnitSourceGeneration = b.NodeID, b.Generation
	}
	return target, nil
}

func (s *EngineController) claimTarget(r *http.Request) (capability.RequestEvidence, error) {
	var evidence capability.RequestEvidence
	body, err := decodeClaimRequest(r)
	if err != nil {
		return evidence, err
	}
	evidence.Target = capability.Target{RunID: body.RunID, NodeID: body.JobID, ClaimKinds: slices.Clone(body.Kinds)}
	if r.PathValue("run") != "" {
		selected, err := s.runTarget(r)
		if err != nil {
			return evidence, err
		}
		selected.Target.ClaimKinds = evidence.Target.ClaimKinds
		return selected, nil
	}
	return evidence, nil
}

func decodeClaimRequest(r *http.Request) (controllerwire.ClaimRequest, error) {
	var body controllerwire.ClaimRequest
	if err := decodeEngineRequest(r, &body); err != nil {
		return body, err
	}
	if run := r.PathValue("run"); run != "" {
		if body.RunID != "" && body.RunID != run || body.JobID != "" || len(body.Kinds) != 1 || body.Kinds[0] != capability.UnitTrigger {
			return body, invalidEngineBody("named trigger claim must select its path run and trigger kind")
		}
		body.RunID = run
	}
	if _, err := engineClaimTarget(body); err != nil {
		return body, err
	}
	return body, nil
}

func (s *Server) claimHandler(engine *EngineController) func(http.ResponseWriter, *http.Request, capability.Authorized) error {
	return func(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
		body, err := decodeClaimRequest(r)
		if err != nil {
			return err
		}
		key, ok := s.admitRunnerRequest(w, r, a, budgetClassClaim)
		if !ok {
			return nil
		}
		ctx := store.WithClaimPlacement(r.Context(), store.ClaimPlacement{DefaultPrefers: s.placement.defaultPrefers, Hold: s.placement.hold, Liveness: s.placement.liveness})
		response, err := engine.Claim(ctx, a, body)
		if response.Claim != nil {
			s.recordQueueActivity(time.Now())
		} else {
			if limiter := s.requestBudget.limiter(budgetClassClaim); limiter != nil {
				limiter.Penalize(key, time.Now())
			}
			s.writeClaimPollAdvice(w)
		}
		if err != nil {
			return engineLookupError(err)
		}
		if response.Claim == nil {
			if record, ok := r.Context().Value(auditCtxKey{}).(*auditRecord); ok {
				record.emptyPoll = true
			}
		}
		writeJSON(w, http.StatusOK, response)
		return nil
	}
}

func (s *EngineController) handleExchange(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	var body struct{}
	if err := decodeEngineRequest(r, &body); err != nil {
		return err
	}
	response, err := s.Exchange(r.Context(), a)
	if err != nil {
		return engineLookupError(err)
	}
	writeJSON(w, http.StatusOK, response)
	return nil
}

func (s *EngineController) handleReconcile(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	var body controllerwire.ReconcileRequest
	if err := decodeEngineRequest(r, &body); err != nil {
		return err
	}
	response, err := s.ReconcileJob(r.Context(), a, body)
	if err != nil {
		return engineLookupError(err)
	}
	writeJSON(w, http.StatusOK, response)
	return nil
}

func (s *EngineController) handleSyncLauncher(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	var body controllerwire.LauncherSyncRequest
	if err := decodeEngineRequest(r, &body); err != nil {
		return err
	}
	response, err := s.SyncLauncher(r.Context(), a, body)
	if err != nil {
		return engineLookupError(err)
	}
	writeJSON(w, http.StatusOK, response)
	return nil
}
