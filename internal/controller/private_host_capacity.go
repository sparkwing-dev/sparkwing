package controller

import (
	"errors"
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/localhostwire"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func (s *Server) localHostCapacity(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	var body localhostwire.LocalCapacityRequest
	if err := decodeEngineRequest(r, &body); err != nil {
		return err
	}
	if body.Binding.RunID != a.Target.RunID {
		return localHostRefusal()
	}
	tenant, err := s.tenantForTeam(r.Context(), store.Team(a.Target.Team))
	if err != nil {
		return err
	}
	actor := engineActor(a)
	var result any
	switch r.Pattern {
	case "POST /api/v1/runs/{run}/local-host/capacity/adopt":
		var adoption store.CapacityAdoption
		adoption, err = tenant.AdoptCapacity(r.Context(), body.Key, body.HolderID, body.Binding, actor)
		// A missing entry is a fresh arrival, just as on direct-store admission.
		if errors.Is(err, store.ErrNotFound) {
			adoption, err = store.CapacityAdoption{Kind: store.AcquireAgain}, nil
		}
		result = adoption
	case "POST /api/v1/runs/{run}/local-host/capacity/acquire":
		if body.Acquire == nil || body.Acquire.Key != body.Key || body.Acquire.HolderID != body.HolderID {
			return invalidEngineBody("capacity acquisition requires its selected group and holder")
		}
		result, err = tenant.AcquireCapacity(r.Context(), *body.Acquire, body.Binding, actor)
	case "POST /api/v1/runs/{run}/local-host/capacity/resolve":
		result, err = tenant.ResolveCapacityWaiter(r.Context(), body.Key, body.Binding, actor)
	default:
		return invalidEngineBody("unknown capacity operation")
	}
	if err != nil {
		return engineLookupError(err)
	}
	writeJSON(w, http.StatusOK, result)
	return nil
}
