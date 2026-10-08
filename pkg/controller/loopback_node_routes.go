package controller

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type loopbackBounces interface {
	PendingNodeBounce(ctx context.Context, runID, nodeID string) (*store.NodeBounce, error)
	ConsumeNodeBounce(ctx context.Context, runID, nodeID string, seq int64, outcome string) error
}

type loopbackExecutionAttempts interface {
	AcknowledgeNodeExecutionStart(ctx context.Context, runID, nodeID string, start store.ExecutionStart) error
	FinishNodeExecutionAttempt(ctx context.Context, runID, nodeID string, finish store.ExecutionAttemptFinish) error
}

// safety: a node process acquires slots for the nodes it spawns, which its dispatcher never
// saw, so the shim serves them from the run's own concurrency backend.
type loopbackSlots interface {
	AcquireSlot(ctx context.Context, req store.AcquireSlotRequest) (store.AcquireSlotResponse, error)
	ObserveSlot(ctx context.Context, key, holderID string) (*store.ConcurrencyHolder, error)
	HeartbeatSlot(ctx context.Context, key, holderID string, lease time.Duration) (time.Time, bool, error)
	ReleaseSlot(ctx context.Context, key, holderID, outcome, outputRef, cacheKeyHash string, ttl time.Duration) error
	ResolveWaiter(ctx context.Context, key, runID, nodeID, cacheKeyHash, leaderRunID, leaderNodeID string, bypassRead bool) (store.WaiterResolution, error)
	CancelWaiter(ctx context.Context, key, runID, nodeID string) (bool, error)
	ForceReleaseSuperseded(ctx context.Context, key string) ([]store.ConcurrencyHolder, error)
}

// safety: a backend that cannot hold a bounce request has none pending, so the poll answers
// 204 and a consume answers 404, which a runner reads as already settled.
func (l *Loopback) handlePendingNodeBounce(w http.ResponseWriter, r *http.Request) {
	bounces, ok := l.state.(loopbackBounces)
	if !ok {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	b, err := bounces.PendingNodeBounce(r.Context(), r.PathValue("id"), r.PathValue("nodeID"))
	if err != nil {
		writeStateError(w, err)
		return
	}
	if b == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, b)
}

func (l *Loopback) handleConsumeNodeBounce(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Seq     int64  `json:"seq"`
		Outcome string `json:"outcome"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	bounces, ok := l.state.(loopbackBounces)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("bounce request %d: %w", body.Seq, store.ErrNotFound))
		return
	}
	if body.Outcome == "" {
		body.Outcome = store.BounceBounced
	}
	if err := bounces.ConsumeNodeBounce(r.Context(), r.PathValue("id"), r.PathValue("nodeID"), body.Seq, body.Outcome); err != nil {
		writeStateError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (l *Loopback) executionAttempts(w http.ResponseWriter) (loopbackExecutionAttempts, bool) {
	attempts, ok := l.state.(loopbackExecutionAttempts)
	if !ok {
		writeError(w, http.StatusNotImplemented,
			fmt.Errorf("%w: this run's state backend records no execution attempts", storage.ErrNotSupported))
	}
	return attempts, ok
}

func (l *Loopback) handleAcknowledgeNodeExecutionStart(w http.ResponseWriter, r *http.Request) {
	attempts, ok := l.executionAttempts(w)
	if !ok {
		return
	}
	var body store.ExecutionStart
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if body.AttemptOrdinal < 1 {
		writeError(w, http.StatusBadRequest, errors.New("attempt_ordinal is required"))
		return
	}
	if err := attempts.AcknowledgeNodeExecutionStart(r.Context(), r.PathValue("id"), r.PathValue("nodeID"), body); err != nil {
		writeExecutionAttemptError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (l *Loopback) handleFinishNodeExecutionAttempt(w http.ResponseWriter, r *http.Request) {
	attempts, ok := l.executionAttempts(w)
	if !ok {
		return
	}
	var body store.ExecutionAttemptFinish
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if body.AttemptOrdinal < 1 || !validExecutionAttemptResult(body.Outcome, body.FailureReason) {
		writeError(w, http.StatusBadRequest, errors.New("attempt_ordinal and a valid outcome are required"))
		return
	}
	if err := attempts.FinishNodeExecutionAttempt(r.Context(), r.PathValue("id"), r.PathValue("nodeID"), body); err != nil {
		writeExecutionAttemptError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeExecutionAttemptError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrLockHeld) {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeStateError(w, err)
}

func (l *Loopback) slots(w http.ResponseWriter) (loopbackSlots, bool) {
	slots, ok := l.concurrency.(loopbackSlots)
	if !ok {
		writeError(w, http.StatusNotImplemented,
			fmt.Errorf("%w: this run has no concurrency backend that grants slots", storage.ErrNotSupported))
	}
	return slots, ok
}

// safety: a holder id is <run>/<node>, so this run's bearer moves only this run's holders.
func (l *Loopback) ownHolder(w http.ResponseWriter, holderID string) bool {
	if !strings.HasPrefix(holderID, l.runID+"/") {
		writeError(w, http.StatusNotFound,
			fmt.Errorf("holder %s: %w on the loopback controller for run %s", holderID, store.ErrNotFound, l.runID))
		return false
	}
	return true
}

func (l *Loopback) ownSlotRun(w http.ResponseWriter, runID string) bool {
	if runID != l.runID {
		writeError(w, http.StatusNotFound,
			fmt.Errorf("run %s: %w on the loopback controller for run %s", runID, store.ErrNotFound, l.runID))
		return false
	}
	return true
}

func (l *Loopback) handleAcquireSlot(w http.ResponseWriter, r *http.Request) {
	slots, ok := l.slots(w)
	if !ok {
		return
	}
	var body acquireSlotReq
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if body.HolderID == "" || body.RunID == "" {
		writeError(w, http.StatusBadRequest, errors.New("holder_id and run_id are required"))
		return
	}
	if !l.ownSlotRun(w, body.RunID) || !l.ownHolder(w, body.HolderID) {
		return
	}
	resp, err := slots.AcquireSlot(r.Context(), store.AcquireSlotRequest{
		Key: r.PathValue("key"), HolderID: body.HolderID, InheritedHolderID: body.InheritedHolderID,
		RunID: body.RunID, NodeID: body.NodeID, Capacity: body.Max, Cost: body.Cost, Policy: body.Policy,
		CacheKeyHash: body.CacheKeyHash, CacheTTL: time.Duration(body.CacheTTLNS),
		CancelTimeout: time.Duration(body.CancelTimeoutNS), Lease: time.Duration(body.LeaseSecs) * time.Second,
		BypassRead: body.BypassRead,
	})
	if err != nil {
		if errors.Is(err, store.ErrConcurrencySuperseded) {
			writeError(w, http.StatusConflict, err)
			return
		}
		writeStateError(w, err)
		return
	}
	writeAcquireSlot(w, resp)
}

func writeAcquireSlot(w http.ResponseWriter, resp store.AcquireSlotResponse) {
	out := acquireSlotResp{
		Kind: string(resp.Kind), HolderID: resp.HolderID, LeaseExpiresAt: resp.LeaseExpiresAt,
		LeaderRunID: resp.LeaderRunID, LeaderNodeID: resp.LeaderNodeID, OutputRef: resp.OutputRef,
		OriginRunID: resp.OriginRunID, OriginNodeID: resp.OriginNodeID, SupersededIDs: resp.SupersededIDs,
		PreviousCapacity: resp.PreviousCapacity, DriftNote: resp.DriftNote,
		Position: resp.Position, QueueLength: resp.QueueLength,
	}
	for _, h := range resp.Holders {
		out.Holders = append(out.Holders, holderResp(h))
	}
	switch resp.Kind {
	case store.AcquireGranted, store.AcquireCached:
		out.Granted = true
		writeJSON(w, http.StatusOK, out)
	case store.AcquireQueued, store.AcquireCoalesced, store.AcquireCancellingOthers:
		writeJSON(w, http.StatusAccepted, out)
	case store.AcquireSkipped, store.AcquireFailed:
		writeJSON(w, http.StatusTooManyRequests, out)
	default:
		writeError(w, http.StatusInternalServerError, fmt.Errorf("unknown kind %q", resp.Kind))
	}
}

func (l *Loopback) handleHeartbeatSlot(w http.ResponseWriter, r *http.Request) {
	slots, ok := l.slots(w)
	if !ok {
		return
	}
	var body heartbeatSlotReq
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !l.ownHolder(w, body.HolderID) {
		return
	}
	expires, superseded, err := slots.HeartbeatSlot(r.Context(), r.PathValue("key"), body.HolderID,
		time.Duration(body.LeaseSecs)*time.Second)
	if err != nil {
		writeExecutionAttemptError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, heartbeatSlotResp{LeaseExpiresAt: expires, CancelledByNewer: superseded})
}

func (l *Loopback) handleObserveSlot(w http.ResponseWriter, r *http.Request) {
	slots, ok := l.slots(w)
	if !ok {
		return
	}
	holderID := r.URL.Query().Get("holder_id")
	if !l.ownHolder(w, holderID) {
		return
	}
	holder, err := slots.ObserveSlot(r.Context(), r.PathValue("key"), holderID)
	if err != nil {
		writeStateError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, holderResp(*holder))
}

func (l *Loopback) handleReleaseSlot(w http.ResponseWriter, r *http.Request) {
	slots, ok := l.slots(w)
	if !ok {
		return
	}
	var body releaseSlotReq
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !l.ownHolder(w, body.HolderID) {
		return
	}
	if err := slots.ReleaseSlot(r.Context(), r.PathValue("key"), body.HolderID, body.Outcome,
		body.OutputRef, body.CacheKeyHash, time.Duration(body.CacheTTLNS)); err != nil {
		writeStateError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (l *Loopback) handleResolveWaiter(w http.ResponseWriter, r *http.Request) {
	slots, ok := l.slots(w)
	if !ok {
		return
	}
	q := r.URL.Query()
	if !l.ownSlotRun(w, q.Get("run_id")) {
		return
	}
	res, err := slots.ResolveWaiter(r.Context(), r.PathValue("key"), q.Get("run_id"), q.Get("node_id"),
		q.Get("cache_key_hash"), q.Get("leader_run_id"), q.Get("leader_node_id"), q.Get("bypass_read") == "true")
	if err != nil {
		writeStateError(w, err)
		return
	}
	out := resolveWaiterResp{
		Status: string(res.Status), HolderID: res.HolderID, HolderLeaseExpires: res.HolderLeaseExpires,
		OutputRef: res.OutputRef, OriginRunID: res.OriginRunID, OriginNodeID: res.OriginNodeID,
		LeaderRunID: res.LeaderRunID, LeaderNodeID: res.LeaderNodeID, LeaderOutcome: res.LeaderOutcome,
		LeaderFailureReason: res.LeaderFailureReason, Position: res.Position, QueueLength: res.QueueLength,
	}
	for _, h := range res.Holders {
		out.Holders = append(out.Holders, holderResp(h))
	}
	writeJSON(w, http.StatusOK, out)
}

func (l *Loopback) handleCancelWaiter(w http.ResponseWriter, r *http.Request) {
	slots, ok := l.slots(w)
	if !ok {
		return
	}
	var body cancelWaiterReq
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !l.ownSlotRun(w, body.RunID) {
		return
	}
	cancelled, err := slots.CancelWaiter(r.Context(), r.PathValue("key"), body.RunID, body.NodeID)
	if err != nil {
		writeStateError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, cancelWaiterResp{Cancelled: cancelled})
}

func (l *Loopback) handleForceRelease(w http.ResponseWriter, r *http.Request) {
	slots, ok := l.slots(w)
	if !ok {
		return
	}
	dropped, err := slots.ForceReleaseSuperseded(r.Context(), r.PathValue("key"))
	if err != nil {
		writeStateError(w, err)
		return
	}
	var out forceReleaseResp
	for _, h := range dropped {
		out.Dropped = append(out.Dropped, holderResp(h))
	}
	writeJSON(w, http.StatusOK, out)
}
