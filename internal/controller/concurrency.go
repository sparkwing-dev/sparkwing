package controller

import (
	"errors"
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/routepolicy"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/apierr"
)

func holderResp(h store.ConcurrencyHolder) controllerwire.QueueGroupHolder {
	resp := controllerwire.QueueGroupHolder{
		HolderID: h.HolderID, RunID: h.RunID, JobID: h.NodeID,
		ClaimedAt: h.ClaimedAt, LeaseExpiresAt: h.LeaseExpiresAt,
		Superseded: h.Superseded, Cost: h.Cost,
	}
	if !h.QueueArrivedAt.IsZero() {
		resp.QueueArrivedAt = &h.QueueArrivedAt
	}
	return resp
}

func (s *Server) handleConcurrencyState(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	tenant, err := s.concurrencyStore(r.Context(), a.Target.Team)
	if err != nil {
		return err
	}
	key := a.Target.SlotKey
	st, err := tenant.ReadAuthorizedGroup(r.Context(), a, key)
	if err != nil {
		return concurrencyError(err)
	}
	if st == nil || st.Key != key {
		return routepolicy.MissingEndpointPolicy()
	}
	resp := controllerwire.QueueGroup{
		Group: st.Key, Capacity: st.Capacity, EffectiveCapacity: st.EffectiveCapacity, UsedCost: st.UsedCost,
		Holders: []controllerwire.QueueGroupHolder{}, Waiters: []controllerwire.QueueGroupWaiter{},
	}
	for _, h := range st.Holders {
		resp.Holders = append(resp.Holders, holderResp(h))
	}
	for _, wt := range st.Waiters {
		resp.Waiters = append(resp.Waiters, controllerwire.QueueGroupWaiter{
			BlockingReason: wt.BlockingReason,
			RunID:          wt.RunID, JobID: wt.NodeID, ArrivedAt: wt.ArrivedAt,
			Policy: wt.Policy,
			Cost:   wt.Cost, Position: wt.Position + 1,
		})
	}
	writeJSON(w, http.StatusOK, resp)
	return nil
}

func concurrencyError(err error) error {
	switch {
	case errors.Is(err, store.ErrLockHeld):
		return &apierr.Error{Code: apierr.LockHeld, Message: "concurrency holder changed"}
	case errors.Is(err, store.ErrConcurrencySuperseded):
		return &apierr.Error{Code: apierr.Conflict, Message: "concurrency work superseded", Details: map[string]any{"reason": "concurrency_superseded"}}
	default:
		return engineLookupError(err)
	}
}
