package controller

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// Concurrency service. Callers declare their OnLimit policy on every
// acquire; the store enforces capacity per-key with latest-wins drift
// handling. All decisions happen inside one SQLite transaction.

type acquireSlotReq struct {
	HolderID        string `json:"holder_id"`
	RunID           string `json:"run_id"`
	NodeID          string `json:"node_id,omitempty"`
	Max             int    `json:"max,omitempty"`
	Cost            int    `json:"cost,omitempty"`
	Policy          string `json:"policy,omitempty"`
	CacheKeyHash    string `json:"cache_key_hash,omitempty"`
	CacheTTLNS      int64  `json:"cache_ttl_ns,omitempty"`
	CancelTimeoutNS int64  `json:"cancel_timeout_ns,omitempty"`
	LeaseSecs       int    `json:"lease_secs,omitempty"`
	BypassRead      bool   `json:"bypass_read,omitempty"`
}

type acquireSlotResp struct {
	Granted          bool      `json:"granted"`
	Kind             string    `json:"kind"`
	HolderID         string    `json:"holder_id,omitempty"`
	LeaseExpiresAt   time.Time `json:"lease_expires_at,omitempty"`
	LeaderRunID      string    `json:"leader_run_id,omitempty"`
	LeaderNodeID     string    `json:"leader_node_id,omitempty"`
	OutputRef        string    `json:"output_ref,omitempty"`
	OriginRunID      string    `json:"origin_run_id,omitempty"`
	OriginNodeID     string    `json:"origin_node_id,omitempty"`
	SupersededIDs    []string  `json:"superseded_ids,omitempty"`
	PreviousCapacity int       `json:"previous_capacity,omitempty"`
	DriftNote        string    `json:"drift_note,omitempty"`
	// Queue observability for AcquireQueued, mirroring the store.
	Position    int               `json:"position,omitempty"`
	QueueLength int               `json:"queue_length,omitempty"`
	Holders     []stateHolderResp `json:"holders,omitempty"`
}

// handleAcquireSlot resolves a single arrival. Status codes
// distinguish outcomes so clients branch without parsing body text:
//
//	200 -> AcquireGranted / AcquireCached
//	202 -> AcquireQueued / AcquireCoalesced / AcquireCancellingOthers
//	429 -> AcquireSkipped / AcquireFailed (terminal)
func (s *Server) handleAcquireSlot(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	var body acquireSlotReq
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if body.HolderID == "" || body.RunID == "" {
		writeError(w, http.StatusBadRequest, errors.New("holder_id and run_id are required"))
		return
	}
	req := store.AcquireSlotRequest{
		Key:           key,
		HolderID:      body.HolderID,
		RunID:         body.RunID,
		NodeID:        body.NodeID,
		Capacity:      body.Max,
		Cost:          body.Cost,
		Policy:        body.Policy,
		CacheKeyHash:  body.CacheKeyHash,
		CacheTTL:      time.Duration(body.CacheTTLNS),
		CancelTimeout: time.Duration(body.CancelTimeoutNS),
		Lease:         time.Duration(body.LeaseSecs) * time.Second,
		BypassRead:    body.BypassRead,
	}
	resp, err := s.store.AcquireConcurrencySlot(r.Context(), req)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
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
		bodyOut.Holders = append(bodyOut.Holders, stateHolderResp{
			HolderID: h.HolderID, RunID: h.RunID, NodeID: h.NodeID,
			ClaimedAt: h.ClaimedAt, LeaseExpiresAt: h.LeaseExpiresAt,
			Superseded: h.Superseded, Cost: h.Cost,
		})
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
		writeError(w, http.StatusInternalServerError, fmt.Errorf("unknown kind %q", resp.Kind))
	}
}

type heartbeatSlotReq struct {
	HolderID  string `json:"holder_id"`
	LeaseSecs int    `json:"lease_secs,omitempty"`
}

type heartbeatSlotResp struct {
	LeaseExpiresAt   time.Time `json:"lease_expires_at"`
	CancelledByNewer bool      `json:"cancelled_by_newer"`
}

// handleHeartbeatSlot extends the holder's lease and reports whether
// the slot has been marked superseded since the last heartbeat (a
// CancelOthers arrival tripped it).
func (s *Server) handleHeartbeatSlot(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	var body heartbeatSlotReq
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if body.HolderID == "" {
		writeError(w, http.StatusBadRequest, errors.New("holder_id is required"))
		return
	}
	lease := time.Duration(body.LeaseSecs) * time.Second
	expires, superseded, err := s.store.HeartbeatConcurrencySlot(r.Context(), key, body.HolderID, lease)
	if err != nil {
		if errors.Is(err, store.ErrLockHeld) {
			writeError(w, http.StatusConflict, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, heartbeatSlotResp{
		LeaseExpiresAt:   expires,
		CancelledByNewer: superseded,
	})
}

type releaseSlotReq struct {
	HolderID     string `json:"holder_id"`
	Outcome      string `json:"outcome"`
	OutputRef    string `json:"output_ref,omitempty"`
	CacheKeyHash string `json:"cache_key_hash,omitempty"`
	CacheTTLNS   int64  `json:"cache_ttl_ns,omitempty"`
}

// handleReleaseSlot drops the holder row, optionally writes a cache
// entry on success, and resolves any coalesce followers + promotes
// the next FIFO waiter. Returns 204 whether or not a row was removed
// so idempotent release paths don't have to handle 404.
func (s *Server) handleReleaseSlot(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	var body releaseSlotReq
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if body.HolderID == "" {
		writeError(w, http.StatusBadRequest, errors.New("holder_id is required"))
		return
	}
	// One atomic transaction so a mid-handler crash leaves no
	// stranded waiters.
	_, _, _, err := s.store.ReleaseAndNotify(
		r.Context(), key, body.HolderID, body.Outcome,
		body.OutputRef, body.CacheKeyHash,
		time.Duration(body.CacheTTLNS), store.DefaultConcurrencyLease,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type stateResp struct {
	Key      string `json:"key"`
	Capacity int    `json:"capacity"`
	// EffectiveCapacity is the most-restrictive minimum actually
	// enforced; UsedCost is the summed cost of active holders. Available
	// budget is EffectiveCapacity - UsedCost.
	EffectiveCapacity int               `json:"effective_capacity"`
	UsedCost          int               `json:"used_cost"`
	Holders           []stateHolderResp `json:"holders"`
	Waiters           []stateWaiterResp `json:"waiters"`
}

type stateHolderResp struct {
	HolderID       string    `json:"holder_id"`
	RunID          string    `json:"run_id"`
	NodeID         string    `json:"node_id,omitempty"`
	ClaimedAt      time.Time `json:"claimed_at"`
	LeaseExpiresAt time.Time `json:"lease_expires_at"`
	Superseded     bool      `json:"superseded"`
	Cost           int       `json:"cost,omitempty"`
}

type stateWaiterResp struct {
	RunID         string    `json:"run_id"`
	NodeID        string    `json:"node_id,omitempty"`
	ArrivedAt     time.Time `json:"arrived_at"`
	Policy        string    `json:"policy"`
	CacheKeyHash  string    `json:"cache_key_hash,omitempty"`
	LeaderRunID   string    `json:"leader_run_id,omitempty"`
	LeaderNodeID  string    `json:"leader_node_id,omitempty"`
	CancelTimeout string    `json:"cancel_timeout,omitempty"`
	Cost          int       `json:"cost,omitempty"`
	// Position is the queue-policy waiter's 0-based rank in arrival
	// order (0 == next in line).
	Position int `json:"position"`
}

// handleConcurrencyState returns the current capacity + holders +
// waiters for a key. 404 when the key has never been declared.
func (s *Server) handleConcurrencyState(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	st, err := s.store.GetConcurrencyState(r.Context(), key)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	resp := stateResp{
		Key: st.Key, Capacity: st.Capacity,
		EffectiveCapacity: st.EffectiveCapacity, UsedCost: st.UsedCost,
	}
	for _, h := range st.Holders {
		resp.Holders = append(resp.Holders, stateHolderResp{
			HolderID: h.HolderID, RunID: h.RunID, NodeID: h.NodeID,
			ClaimedAt: h.ClaimedAt, LeaseExpiresAt: h.LeaseExpiresAt,
			Superseded: h.Superseded, Cost: h.Cost,
		})
	}
	for _, wt := range st.Waiters {
		var ct string
		if wt.CancelTimeout > 0 {
			ct = wt.CancelTimeout.String()
		}
		resp.Waiters = append(resp.Waiters, stateWaiterResp{
			RunID: wt.RunID, NodeID: wt.NodeID, ArrivedAt: wt.ArrivedAt,
			Policy: wt.Policy, CacheKeyHash: wt.CacheKeyHash,
			LeaderRunID: wt.LeaderRunID, LeaderNodeID: wt.LeaderNodeID,
			CancelTimeout: ct, Cost: wt.Cost, Position: wt.Position,
		})
	}
	writeJSON(w, http.StatusOK, resp)
}

type resolveWaiterResp struct {
	Status             string            `json:"status"`
	HolderID           string            `json:"holder_id,omitempty"`
	HolderLeaseExpires time.Time         `json:"holder_lease_expires,omitempty"`
	OutputRef          string            `json:"output_ref,omitempty"`
	OriginRunID        string            `json:"origin_run_id,omitempty"`
	OriginNodeID       string            `json:"origin_node_id,omitempty"`
	LeaderRunID        string            `json:"leader_run_id,omitempty"`
	LeaderNodeID       string            `json:"leader_node_id,omitempty"`
	LeaderOutcome      string            `json:"leader_outcome,omitempty"`
	Position           int               `json:"position,omitempty"`
	Holders            []stateHolderResp `json:"holders,omitempty"`
}

// handleResolveWaiter is the polling read a waiting in-pod orchestrator
// uses to learn whether it was promoted, cached, coalesced behind a
// finished leader, or cancelled. Mirrors store.ResolveWaiter; the
// orchestrator's waitThenRun loop drives it once the controller backend
// returns a queued/coalesced/cancelling acquire.
func (s *Server) handleResolveWaiter(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	q := r.URL.Query()
	runID := q.Get("run_id")
	if key == "" || runID == "" {
		writeError(w, http.StatusBadRequest, errors.New("key and run_id are required"))
		return
	}
	res, err := s.store.ResolveWaiter(
		r.Context(), key, runID, q.Get("node_id"),
		q.Get("cache_key_hash"), q.Get("leader_run_id"), q.Get("leader_node_id"),
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	out := resolveWaiterResp{
		Status:             string(res.Status),
		HolderID:           res.HolderID,
		HolderLeaseExpires: res.HolderLeaseExpires,
		OutputRef:          res.OutputRef,
		OriginRunID:        res.OriginRunID,
		OriginNodeID:       res.OriginNodeID,
		LeaderRunID:        res.LeaderRunID,
		LeaderNodeID:       res.LeaderNodeID,
		LeaderOutcome:      res.LeaderOutcome,
		Position:           res.Position,
	}
	for _, h := range res.Holders {
		out.Holders = append(out.Holders, stateHolderResp{
			HolderID: h.HolderID, RunID: h.RunID, NodeID: h.NodeID,
			ClaimedAt: h.ClaimedAt, LeaseExpiresAt: h.LeaseExpiresAt,
			Superseded: h.Superseded,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

type cancelWaiterReq struct {
	RunID  string `json:"run_id"`
	NodeID string `json:"node_id,omitempty"`
}

type cancelWaiterResp struct {
	Cancelled bool `json:"cancelled"`
}

// handleCancelWaiter drops a parked waiter row so a waiter that gave up
// (QueueTimeout) can't later be promoted to a holder.
func (s *Server) handleCancelWaiter(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	var body cancelWaiterReq
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if body.RunID == "" {
		writeError(w, http.StatusBadRequest, errors.New("run_id is required"))
		return
	}
	cancelled, err := s.store.CancelWaiter(r.Context(), key, body.RunID, body.NodeID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, cancelWaiterResp{Cancelled: cancelled})
}

type forceReleaseResp struct {
	Dropped []stateHolderResp `json:"dropped,omitempty"`
}

// handleForceRelease drops superseded holders (a CancelOthers eviction
// whose evicted holders won't terminate) and promotes the next
// waiters, bounding forward progress.
func (s *Server) handleForceRelease(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	dropped, err := s.store.ForceReleaseSupersededHolders(r.Context(), key)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if len(dropped) > 0 {
		if _, err := s.store.PromoteNextWaiters(r.Context(), key, store.DefaultConcurrencyLease); err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Errorf("force-release: promote: %w", err))
			return
		}
	}
	var out forceReleaseResp
	for _, h := range dropped {
		out.Dropped = append(out.Dropped, stateHolderResp{
			HolderID: h.HolderID, RunID: h.RunID, NodeID: h.NodeID,
			ClaimedAt: h.ClaimedAt, LeaseExpiresAt: h.LeaseExpiresAt,
			Superseded: h.Superseded,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// handleWaiterNotify is an SSE stream that surfaces resolution events
// for a given (run_id, node_id) waiter. Polls the concurrency state at
// 250ms cadence and emits exactly one terminal event per stream, then
// closes. Every event payload carries key + run_id, plus node_id when
// one was supplied on the query string.
//
//	event: ready         data: {"key":"...","run_id":"...","node_id":"..."}
//	event: superseded    data: {"key":"...","run_id":"...","node_id":"..."}
//	event: stream_end    data: {"reason":"max_wait"|"key_not_found", ...}
//
// 30 minutes is the fail-safe per-stream cap; the stream also closes
// when the client disconnects.
func (s *Server) handleWaiterNotify(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	runID := r.URL.Query().Get("run_id")
	nodeID := r.URL.Query().Get("node_id")
	if key == "" || runID == "" {
		writeError(w, http.StatusBadRequest, errors.New("key and run_id are required"))
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, errors.New("streaming not supported"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(": open\n\n"))
	flusher.Flush()

	ctx := r.Context()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	maxWait := time.NewTimer(30 * time.Minute)
	defer maxWait.Stop()

	emit := func(event string, payload map[string]string) bool {
		payload["key"] = key
		payload["run_id"] = runID
		if nodeID != "" {
			payload["node_id"] = nodeID
		}
		b, _ := json.Marshal(payload)
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-maxWait.C:
			emit("stream_end", map[string]string{"reason": "max_wait"})
			return
		case <-ticker.C:
		}

		st, err := s.store.GetConcurrencyState(ctx, key)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				emit("stream_end", map[string]string{"reason": "key_not_found"})
				return
			}
			return
		}
		if hasHolder(st, runID, nodeID) {
			emit("ready", map[string]string{})
			return
		}
		if !hasWaiter(st, runID, nodeID) {
			emit("superseded", map[string]string{})
			return
		}
	}
}

func hasHolder(st *store.ConcurrencyState, runID, nodeID string) bool {
	for _, h := range st.Holders {
		if h.RunID == runID && h.NodeID == nodeID && !h.Superseded {
			return true
		}
	}
	return false
}

func hasWaiter(st *store.ConcurrencyState, runID, nodeID string) bool {
	for _, wt := range st.Waiters {
		if wt.RunID == runID && wt.NodeID == nodeID {
			return true
		}
	}
	return false
}
