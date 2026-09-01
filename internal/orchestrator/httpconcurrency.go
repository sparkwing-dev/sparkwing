package orchestrator

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type HTTPConcurrency struct {
	client *client.Client
	lease  time.Duration
}

func NewHTTPConcurrency(baseURL string, httpClient *http.Client, token string, lease time.Duration) *HTTPConcurrency {
	if lease <= 0 {
		lease = store.DefaultConcurrencyLease
	}
	return &HTTPConcurrency{
		client: client.NewWithToken(baseURL, httpClient, token),
		lease:  lease,
	}
}

func (h *HTTPConcurrency) AcquireSlot(ctx context.Context, req store.AcquireSlotRequest) (store.AcquireSlotResponse, error) {
	if req.Lease <= 0 {
		req.Lease = h.lease
	}
	resp, err := h.client.AcquireSlot(ctx, req.Key, client.AcquireSlotRequest{
		HolderID:          req.HolderID,
		InheritedHolderID: req.InheritedHolderID,
		RunID:             req.RunID,
		NodeID:            req.NodeID,
		Max:               req.Capacity,
		Cost:              req.Cost,
		Policy:            req.Policy,
		CacheKeyHash:      req.CacheKeyHash,
		CacheTTL:          req.CacheTTL,
		CancelTimeout:     req.CancelTimeout,
		Lease:             req.Lease,
		BypassRead:        req.BypassRead,
	})
	if err != nil {
		if strings.Contains(err.Error(), store.ErrConcurrencySuperseded.Error()) {
			return store.AcquireSlotResponse{}, store.ErrConcurrencySuperseded
		}
		return store.AcquireSlotResponse{}, err
	}
	out := store.AcquireSlotResponse{
		Kind:             store.AcquireKind(resp.Kind),
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
	for _, hd := range resp.Holders {
		out.Holders = append(out.Holders, storeHolderFromClient(req.Key, hd))
	}
	return out, nil
}

func storeHolderFromClient(key string, hd client.WaiterHolder) store.ConcurrencyHolder {
	holder := store.ConcurrencyHolder{
		Key: key, HolderID: hd.HolderID, RunID: hd.RunID, NodeID: hd.NodeID,
		ClaimedAt: hd.ClaimedAt, LeaseExpiresAt: hd.LeaseExpiresAt,
		Superseded: hd.Superseded, Cost: hd.Cost,
	}
	if hd.QueueArrivedAt != nil {
		holder.QueueArrivedAt = *hd.QueueArrivedAt
	}
	return holder
}

func (h *HTTPConcurrency) State(ctx context.Context, key string) (*store.ConcurrencyState, error) {
	resp, err := h.client.ConcurrencyState(ctx, key)
	if err != nil {
		return nil, err
	}
	out := &store.ConcurrencyState{
		Key:               resp.Key,
		Capacity:          resp.Capacity,
		EffectiveCapacity: resp.EffectiveCapacity,
		UsedCost:          resp.UsedCost,
	}
	for _, holder := range resp.Holders {
		out.Holders = append(out.Holders, storeHolderFromClient(key, holder))
	}
	for _, waiter := range resp.Waiters {
		out.Waiters = append(out.Waiters, store.ConcurrencyWaiter{
			Key:           key,
			RunID:         waiter.RunID,
			NodeID:        waiter.NodeID,
			ArrivedAt:     waiter.ArrivedAt,
			Policy:        waiter.Policy,
			CacheKeyHash:  waiter.CacheKeyHash,
			LeaderRunID:   waiter.LeaderRunID,
			LeaderNodeID:  waiter.LeaderNodeID,
			Cost:          waiter.Cost,
			Position:      waiter.Position,
			CancelTimeout: waiter.CancelTimeoutDuration(),
		})
	}
	return out, nil
}

func (h *HTTPConcurrency) HeartbeatSlot(ctx context.Context, key, holderID string, lease time.Duration) (time.Time, bool, error) {
	if lease <= 0 {
		lease = h.lease
	}
	resp, err := h.client.HeartbeatSlot(ctx, key, holderID, lease)
	if err != nil {
		return time.Time{}, false, err
	}
	return resp.LeaseExpiresAt, resp.CancelledByNewer, nil
}

func (h *HTTPConcurrency) ObserveSlot(ctx context.Context, key, holderID string) (*store.ConcurrencyHolder, error) {
	resp, err := h.client.ObserveSlot(ctx, key, holderID)
	if err != nil {
		return nil, err
	}
	holder := storeHolderFromClient(key, *resp)
	return &holder, nil
}

func (h *HTTPConcurrency) ReleaseSlot(ctx context.Context, key, holderID, outcome, outputRef, cacheKeyHash string, ttl time.Duration) error {
	return h.client.ReleaseSlot(ctx, key, holderID, outcome, outputRef, cacheKeyHash, ttl)
}

func (h *HTTPConcurrency) ResolveWaiter(ctx context.Context, key, runID, nodeID, cacheKeyHash, leaderRunID, leaderNodeID string, bypassRead bool) (store.WaiterResolution, error) {
	resp, err := h.client.ResolveWaiter(ctx, key, runID, nodeID, cacheKeyHash, leaderRunID, leaderNodeID, bypassRead)
	if err != nil {
		return store.WaiterResolution{}, err
	}
	res := store.WaiterResolution{
		Status:              store.WaiterStatus(resp.Status),
		HolderID:            resp.HolderID,
		HolderLeaseExpires:  resp.HolderLeaseExpires,
		OutputRef:           resp.OutputRef,
		OriginRunID:         resp.OriginRunID,
		OriginNodeID:        resp.OriginNodeID,
		LeaderRunID:         resp.LeaderRunID,
		LeaderNodeID:        resp.LeaderNodeID,
		LeaderOutcome:       resp.LeaderOutcome,
		LeaderFailureReason: resp.LeaderFailureReason,
		Position:            resp.Position,
		QueueLength:         resp.QueueLength,
	}
	for _, hd := range resp.Holders {
		res.Holders = append(res.Holders, storeHolderFromClient(key, hd))
	}
	return res, nil
}

func (h *HTTPConcurrency) ForceReleaseSuperseded(ctx context.Context, key string) ([]store.ConcurrencyHolder, error) {
	dropped, err := h.client.ForceReleaseSuperseded(ctx, key)
	if err != nil {
		return nil, err
	}
	out := make([]store.ConcurrencyHolder, 0, len(dropped))
	for _, hd := range dropped {
		out = append(out, storeHolderFromClient(key, hd))
	}
	return out, nil
}

func (h *HTTPConcurrency) CancelWaiter(ctx context.Context, key, runID, nodeID string) (bool, error) {
	return h.client.CancelWaiter(ctx, key, runID, nodeID)
}
