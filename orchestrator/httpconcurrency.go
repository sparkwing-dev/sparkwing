package orchestrator

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/sparkwing-dev/sparkwing/v2/controller/client"
	"github.com/sparkwing-dev/sparkwing/v2/orchestrator/store"
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
		Policy:        req.Policy,
		CacheKeyHash:  req.CacheKeyHash,
		CacheTTL:      req.CacheTTL,
		CancelTimeout: req.CancelTimeout,
		Lease:         req.Lease,
	})
	if err != nil {
		return store.AcquireSlotResponse{}, err
	}
	return store.AcquireSlotResponse{
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
	}, nil
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

func (h *HTTPConcurrency) ReleaseSlot(ctx context.Context, key, holderID, outcome, outputRef, cacheKeyHash string, ttl time.Duration) error {
	return h.client.ReleaseSlot(ctx, key, holderID, outcome, outputRef, cacheKeyHash, ttl)
}

// ResolveWaiter has no HTTP endpoint yet; in-pod orchestrators
// dispatch through the controller and don't wait locally.
func (h *HTTPConcurrency) ResolveWaiter(ctx context.Context, key, runID, nodeID, cacheKeyHash, leaderRunID, leaderNodeID string) (store.WaiterResolution, error) {
	_ = ctx
	_ = runID
	_ = nodeID
	_ = cacheKeyHash
	_ = leaderRunID
	_ = leaderNodeID
	return store.WaiterResolution{Status: store.WaiterStillWaiting}, fmt.Errorf("HTTPConcurrency.ResolveWaiter: not yet implemented on the wire; in-pod orchestrators should not wait locally (key=%s)", key)
}

// ForceReleaseSuperseded has no HTTP wire today; the controller's
// reaper sweeps superseded holders on lease expiry.
func (h *HTTPConcurrency) ForceReleaseSuperseded(ctx context.Context, key string) ([]store.ConcurrencyHolder, error) {
	_ = ctx
	_ = key
	return nil, nil
}
