package orchestrator

import (
	"context"
	"net/http"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// HTTPConcurrency satisfies ConcurrencyBackend via the controller's
// /api/v1/concurrency/* endpoints. In-process orchestrators use
// localConcurrency instead.
type HTTPConcurrency struct {
	client *client.Client
	lease  time.Duration
}

// NewHTTPConcurrency binds a backend to baseURL with optional bearer.
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
		HolderID:      req.HolderID,
		RunID:         req.RunID,
		NodeID:        req.NodeID,
		Max:           req.Capacity,
		Cost:          req.Cost,
		Policy:        req.Policy,
		CacheKeyHash:  req.CacheKeyHash,
		CacheTTL:      req.CacheTTL,
		CancelTimeout: req.CancelTimeout,
		Lease:         req.Lease,
		BypassRead:    req.BypassRead,
	})
	if err != nil {
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

// storeHolderFromClient is the single mapping from the controller's
// wire holder shape back into the store type, so a field added to one
// response path can't silently vanish from its siblings.
func storeHolderFromClient(key string, hd client.WaiterHolder) store.ConcurrencyHolder {
	return store.ConcurrencyHolder{
		Key: key, HolderID: hd.HolderID, RunID: hd.RunID, NodeID: hd.NodeID,
		ClaimedAt: hd.ClaimedAt, LeaseExpiresAt: hd.LeaseExpiresAt,
		Superseded: hd.Superseded, Cost: hd.Cost,
	}
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
	return &store.ConcurrencyHolder{
		Key:            key,
		HolderID:       resp.HolderID,
		RunID:          resp.RunID,
		NodeID:         resp.NodeID,
		ClaimedAt:      resp.ClaimedAt,
		LeaseExpiresAt: resp.LeaseExpiresAt,
		Superseded:     resp.Superseded,
		Cost:           resp.Cost,
	}, nil
}

func (h *HTTPConcurrency) ReleaseSlot(ctx context.Context, key, holderID, outcome, outputRef, cacheKeyHash string, ttl time.Duration) error {
	return h.client.ReleaseSlot(ctx, key, holderID, outcome, outputRef, cacheKeyHash, ttl)
}

// ResolveWaiter polls the controller's resolve endpoint so an in-pod
// orchestrator can wait on a queued/coalesced group slot and observe
// promotion, a cache hit, leader completion, or cancellation -- the
// same resolutions the in-process backend serves from the store.
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
	}
	for _, hd := range resp.Holders {
		res.Holders = append(res.Holders, storeHolderFromClient(key, hd))
	}
	return res, nil
}

// ForceReleaseSuperseded drops superseded holders via the controller so
// a stuck CancelOthers eviction can't block forward progress.
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

// CancelWaiter drops a parked waiter row via the controller so a
// QueueTimeout'd waiter won't later be promoted to a holder.
func (h *HTTPConcurrency) CancelWaiter(ctx context.Context, key, runID, nodeID string) (bool, error) {
	return h.client.CancelWaiter(ctx, key, runID, nodeID)
}
