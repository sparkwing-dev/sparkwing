package controller

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type acquireSlotReq struct {
	HolderID          string `json:"holder_id"`
	InheritedHolderID string `json:"inherited_holder_id,omitempty"`
	RunID             string `json:"run_id"`
	NodeID            string `json:"job_id,omitempty"`
	Max               int    `json:"max,omitempty"`
	Cost              int    `json:"cost,omitempty"`
	Policy            string `json:"policy,omitempty"`
	CacheKeyHash      string `json:"cache_key_hash,omitempty"`
	CacheTTLNS        int64  `json:"cache_ttl_ns,omitempty"`
	CancelTimeoutNS   int64  `json:"cancel_timeout_ns,omitempty"`
	LeaseSecs         int    `json:"lease_secs,omitempty"`
	BypassRead        bool   `json:"bypass_read,omitempty"`
}

type acquireSlotResp struct {
	Granted          bool                              `json:"granted"`
	Kind             string                            `json:"kind"`
	HolderID         string                            `json:"holder_id,omitempty"`
	LeaseExpiresAt   time.Time                         `json:"lease_expires_at,omitempty"`
	LeaderRunID      string                            `json:"leader_run_id,omitempty"`
	LeaderNodeID     string                            `json:"leader_job_id,omitempty"`
	OutputRef        string                            `json:"output_ref,omitempty"`
	OriginRunID      string                            `json:"origin_run_id,omitempty"`
	OriginNodeID     string                            `json:"origin_job_id,omitempty"`
	SupersededIDs    []string                          `json:"superseded_ids,omitempty"`
	PreviousCapacity int                               `json:"previous_capacity,omitempty"`
	DriftNote        string                            `json:"drift_note,omitempty"`
	Position         int                               `json:"position,omitempty"`
	QueueLength      int                               `json:"queue_length,omitempty"`
	Holders          []controllerwire.QueueGroupHolder `json:"holders,omitempty"`
}

func (s *Server) handleAcquireSlot(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	tenant, err := s.concurrencyStore(r.Context(), a.Target.Team)
	if err != nil {
		return err
	}
	key := a.Target.SlotKey
	var body acquireSlotReq
	if err := decodeConcurrencyRequest(r, &body); err != nil {
		return err
	}
	if body.HolderID == "" || body.RunID == "" {
		return invalidEngineBody("holder_id and run_id are required")
	}
	req := store.AcquireSlotRequest{
		Key:               key,
		HolderID:          body.HolderID,
		InheritedHolderID: body.InheritedHolderID,
		RunID:             body.RunID,
		NodeID:            body.NodeID,
		Capacity:          body.Max,
		Cost:              body.Cost,
		Policy:            body.Policy,
		CacheKeyHash:      body.CacheKeyHash,
		CacheTTL:          time.Duration(body.CacheTTLNS),
		CancelTimeout:     time.Duration(body.CancelTimeoutNS),
		Lease:             time.Duration(body.LeaseSecs) * time.Second,
		BypassRead:        body.BypassRead,
	}
	resp, err := tenant.AcquireAuthorizedSlot(r.Context(), a, req)
	if err != nil {
		return concurrencyError(err)
	}
	bodyOut := acquireSlotResp{
		Kind:             string(resp.Kind),
		HolderID:         resp.HolderID,
		LeaseExpiresAt:   resp.LeaseExpiresAt,
		LeaderRunID:      resp.LeaderRunID,
		LeaderNodeID:     resp.LeaderNodeID,
		OutputRef:        resp.OutputRef,
		OriginRunID:      resp.OriginRunID,
		OriginNodeID:     resp.OriginNodeID,
		SupersededIDs:    resp.SupersededIDs,
		PreviousCapacity: resp.PreviousCapacity,
		DriftNote:        resp.DriftNote,
		Position:         resp.Position,
		QueueLength:      resp.QueueLength,
	}
	for _, h := range resp.Holders {
		bodyOut.Holders = append(bodyOut.Holders, holderResp(h))
	}
	switch resp.Kind {
	case store.AcquireGranted, store.AcquireCached:
		bodyOut.Granted = true
		writeJSON(w, http.StatusOK, bodyOut)
	case store.AcquireQueued, store.AcquireCoalesced, store.AcquireCancellingOthers:
		writeJSON(w, http.StatusAccepted, bodyOut)
	case store.AcquireSkipped, store.AcquireFailed:
		writeJSON(w, http.StatusTooManyRequests, bodyOut)
	default:
		return fmt.Errorf("unknown concurrency result %q", resp.Kind)
	}
	return nil
}

type heartbeatSlotReq struct {
	HolderID  string `json:"holder_id"`
	LeaseSecs int    `json:"lease_secs,omitempty"`
}

type heartbeatSlotResp struct {
	LeaseExpiresAt   time.Time `json:"lease_expires_at"`
	CancelledByNewer bool      `json:"cancelled_by_newer"`
}

func (s *Server) handleHeartbeatSlot(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	tenant, err := s.concurrencyStore(r.Context(), a.Target.Team)
	if err != nil {
		return err
	}
	key := a.Target.SlotKey
	var body heartbeatSlotReq
	if err := decodeConcurrencyRequest(r, &body); err != nil {
		return err
	}
	if body.HolderID == "" {
		return invalidEngineBody("holder_id is required")
	}
	lease := time.Duration(body.LeaseSecs) * time.Second
	expires, superseded, err := tenant.HeartbeatAuthorizedSlot(r.Context(), a, key, body.HolderID, lease)
	if err != nil {
		return concurrencyError(err)
	}
	writeJSON(w, http.StatusOK, heartbeatSlotResp{
		LeaseExpiresAt:   expires,
		CancelledByNewer: superseded,
	})
	return nil
}

func (s *Server) handleObserveSlot(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	tenant, err := s.concurrencyStore(r.Context(), a.Target.Team)
	if err != nil {
		return err
	}
	key := a.Target.SlotKey
	holderID := r.URL.Query().Get("holder_id")
	if holderID == "" {
		return invalidEngineBody("holder_id is required")
	}
	holder, err := tenant.ReadAuthorizedHolder(r.Context(), a, key, holderID)
	if err != nil {
		return concurrencyError(err)
	}
	writeJSON(w, http.StatusOK, holderResp(holder))
	return nil
}

type releaseSlotReq struct {
	HolderID     string `json:"holder_id"`
	Outcome      string `json:"outcome"`
	OutputRef    string `json:"output_ref,omitempty"`
	CacheKeyHash string `json:"cache_key_hash,omitempty"`
	CacheTTLNS   int64  `json:"cache_ttl_ns,omitempty"`
}

func (s *Server) handleReleaseSlot(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	tenant, err := s.concurrencyStore(r.Context(), a.Target.Team)
	if err != nil {
		return err
	}
	key := a.Target.SlotKey
	var body releaseSlotReq
	if err := decodeConcurrencyRequest(r, &body); err != nil {
		return err
	}
	if body.HolderID == "" {
		return invalidEngineBody("holder_id is required")
	}
	err = tenant.ReleaseAuthorizedSlot(r.Context(), a, key, body.HolderID, body.Outcome,
		body.OutputRef, body.CacheKeyHash, time.Duration(body.CacheTTLNS))
	if err != nil {
		return concurrencyError(err)
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

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
		return missingEndpointPolicy()
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
			RunID: wt.RunID, JobID: wt.NodeID, ArrivedAt: wt.ArrivedAt,
			Policy: wt.Policy, CacheKeyHash: wt.CacheKeyHash,
			LeaderRunID: wt.LeaderRunID, LeaderJobID: wt.LeaderNodeID,
			CancelTimeout: wt.CancelTimeout, Cost: wt.Cost, Position: wt.Position + 1,
		})
	}
	writeJSON(w, http.StatusOK, resp)
	return nil
}

type resolveWaiterResp struct {
	Status              string                            `json:"status"`
	HolderID            string                            `json:"holder_id,omitempty"`
	HolderLeaseExpires  time.Time                         `json:"holder_lease_expires,omitempty"`
	OutputRef           string                            `json:"output_ref,omitempty"`
	OriginRunID         string                            `json:"origin_run_id,omitempty"`
	OriginNodeID        string                            `json:"origin_job_id,omitempty"`
	LeaderRunID         string                            `json:"leader_run_id,omitempty"`
	LeaderNodeID        string                            `json:"leader_job_id,omitempty"`
	LeaderOutcome       string                            `json:"leader_outcome,omitempty"`
	LeaderFailureReason string                            `json:"leader_failure_reason,omitempty"`
	Position            int                               `json:"position,omitempty"`
	QueueLength         int                               `json:"queue_length,omitempty"`
	Holders             []controllerwire.QueueGroupHolder `json:"holders,omitempty"`
}

func (s *Server) handleResolveWaiter(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	tenant, err := s.concurrencyStore(r.Context(), a.Target.Team)
	if err != nil {
		return err
	}
	key := a.Target.SlotKey
	q := r.URL.Query()
	runID := q.Get("run_id")
	if key == "" || runID == "" {
		return invalidEngineBody("group and run_id are required")
	}
	res, err := tenant.ResolveAuthorizedWaiter(
		r.Context(), a, key, runID, q.Get("job_id"),
		q.Get("cache_key_hash"), q.Get("leader_run_id"), q.Get("leader_job_id"),
		q.Get("bypass_read") == "true",
	)
	if err != nil {
		return concurrencyError(err)
	}
	out := resolveWaiterResp{
		Status:              string(res.Status),
		HolderID:            res.HolderID,
		HolderLeaseExpires:  res.HolderLeaseExpires,
		OutputRef:           res.OutputRef,
		OriginRunID:         res.OriginRunID,
		OriginNodeID:        res.OriginNodeID,
		LeaderRunID:         res.LeaderRunID,
		LeaderNodeID:        res.LeaderNodeID,
		LeaderOutcome:       res.LeaderOutcome,
		LeaderFailureReason: res.LeaderFailureReason,
		Position:            res.Position,
		QueueLength:         res.QueueLength,
	}
	for _, h := range res.Holders {
		out.Holders = append(out.Holders, holderResp(h))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

type cancelWaiterReq struct {
	RunID  string `json:"run_id"`
	NodeID string `json:"job_id,omitempty"`
}

type cancelWaiterResp struct {
	Cancelled bool `json:"cancelled"`
}

func (s *Server) handleCancelWaiter(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	tenant, err := s.concurrencyStore(r.Context(), a.Target.Team)
	if err != nil {
		return err
	}
	key := a.Target.SlotKey
	var body cancelWaiterReq
	if err := decodeConcurrencyRequest(r, &body); err != nil {
		return err
	}
	if body.RunID == "" {
		return invalidEngineBody("run_id is required")
	}
	cancelled, err := tenant.CancelAuthorizedWaiter(r.Context(), a, key, store.NodeRef{RunID: body.RunID, NodeID: body.NodeID})
	if err != nil {
		return concurrencyError(err)
	}
	writeJSON(w, http.StatusOK, cancelWaiterResp{Cancelled: cancelled})
	return nil
}

type forceReleaseResp struct {
	Dropped []controllerwire.QueueGroupHolder `json:"dropped,omitempty"`
}

func (s *Server) handleForceRelease(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	tenant, err := s.concurrencyStore(r.Context(), a.Target.Team)
	if err != nil {
		return err
	}
	key := a.Target.SlotKey
	dropped, err := tenant.ForceReleaseAuthorizedSlots(r.Context(), a, key)
	if err != nil {
		return concurrencyError(err)
	}
	var out forceReleaseResp
	for _, h := range dropped {
		out.Dropped = append(out.Dropped, holderResp(h))
	}
	writeJSON(w, http.StatusOK, out)
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
