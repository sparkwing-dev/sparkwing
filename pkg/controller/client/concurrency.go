package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// AcquireSlotRequest mirrors the controller's acquireSlotReq JSON
// shape.
type AcquireSlotRequest struct {
	HolderID      string
	RunID         string
	NodeID        string
	Max           int
	Cost          int
	Policy        string
	CacheKeyHash  string
	CacheTTL      time.Duration
	CancelTimeout time.Duration
	Lease         time.Duration
	BypassRead    bool
}

// AcquireSlotResponse surfaces the controller response so the caller
// can branch on Kind. "granted"/"cached" map to 200 with Granted=true;
// 202 covers Queued/Coalesced/CancellingOthers; 429 Skipped/Failed.
type AcquireSlotResponse struct {
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
	// Queue observability for a queued arrival, mirroring the store.
	Position    int            `json:"position,omitempty"`
	QueueLength int            `json:"queue_length,omitempty"`
	Holders     []WaiterHolder `json:"holders,omitempty"`
}

// AcquireSlot requests a concurrency slot. The server performs the
// cache lookup, capacity check, and waiter-row insertion in a single
// transaction; the response describes the outcome.
func (c *Client) AcquireSlot(ctx context.Context, key string, req AcquireSlotRequest) (*AcquireSlotResponse, error) {
	body := map[string]any{
		"holder_id": req.HolderID,
		"run_id":    req.RunID,
	}
	if req.NodeID != "" {
		body["node_id"] = req.NodeID
	}
	if req.Max > 0 {
		body["max"] = req.Max
	}
	if req.Cost > 0 {
		body["cost"] = req.Cost
	}
	if req.Policy != "" {
		body["policy"] = req.Policy
	}
	if req.CacheKeyHash != "" {
		body["cache_key_hash"] = req.CacheKeyHash
	}
	if req.CacheTTL > 0 {
		body["cache_ttl_ns"] = int64(req.CacheTTL)
	}
	if req.CancelTimeout > 0 {
		body["cancel_timeout_ns"] = int64(req.CancelTimeout)
	}
	if req.Lease > 0 {
		body["lease_secs"] = int(req.Lease.Seconds())
	}
	if req.BypassRead {
		body["bypass_read"] = true
	}
	buf, _ := json.Marshal(body)

	u := fmt.Sprintf("%s/api/v1/concurrency/%s/acquire", c.baseURL, url.PathEscape(key))
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK, http.StatusAccepted, http.StatusTooManyRequests:
		var out AcquireSlotResponse
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return nil, err
		}
		return &out, nil
	default:
		return nil, readHTTPError(resp)
	}
}

// HeartbeatSlotResponse carries the new lease expiry and a flag the
// runner uses to short-circuit to its cancel path.
type HeartbeatSlotResponse struct {
	LeaseExpiresAt   time.Time `json:"lease_expires_at"`
	CancelledByNewer bool      `json:"cancelled_by_newer"`
}

// HeartbeatSlot extends the holder's lease. CancelledByNewer=true
// indicates a CancelOthers arrival has superseded this holder.
func (c *Client) HeartbeatSlot(ctx context.Context, key, holderID string, lease time.Duration) (*HeartbeatSlotResponse, error) {
	body := map[string]any{"holder_id": holderID}
	if lease > 0 {
		body["lease_secs"] = int(lease.Seconds())
	}
	buf, _ := json.Marshal(body)
	u := fmt.Sprintf("%s/api/v1/concurrency/%s/heartbeat", c.baseURL, url.PathEscape(key))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readHTTPError(resp)
	}
	var out HeartbeatSlotResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

// WaiterHolder is the minimal holder shape a resolved waiter needs to
// refresh its "N ahead, held by X" display.
type WaiterHolder struct {
	HolderID       string    `json:"holder_id"`
	RunID          string    `json:"run_id"`
	NodeID         string    `json:"node_id,omitempty"`
	ClaimedAt      time.Time `json:"claimed_at"`
	LeaseExpiresAt time.Time `json:"lease_expires_at"`
	Superseded     bool      `json:"superseded"`
}

// WaiterResolution mirrors the controller's resolveWaiterResp.
type WaiterResolution struct {
	Status              string         `json:"status"`
	HolderID            string         `json:"holder_id,omitempty"`
	HolderLeaseExpires  time.Time      `json:"holder_lease_expires,omitempty"`
	OutputRef           string         `json:"output_ref,omitempty"`
	OriginRunID         string         `json:"origin_run_id,omitempty"`
	OriginNodeID        string         `json:"origin_node_id,omitempty"`
	LeaderRunID         string         `json:"leader_run_id,omitempty"`
	LeaderNodeID        string         `json:"leader_node_id,omitempty"`
	LeaderOutcome       string         `json:"leader_outcome,omitempty"`
	LeaderFailureReason string         `json:"leader_failure_reason,omitempty"`
	Position            int            `json:"position,omitempty"`
	Holders             []WaiterHolder `json:"holders,omitempty"`
}

// ResolveWaiter polls the controller for a parked waiter's resolution
// (promoted / cached / leader-finished / cancelled / still-waiting).
func (c *Client) ResolveWaiter(ctx context.Context, key, runID, nodeID, cacheKeyHash, leaderRunID, leaderNodeID string, bypassRead bool) (*WaiterResolution, error) {
	q := url.Values{}
	q.Set("run_id", runID)
	if nodeID != "" {
		q.Set("node_id", nodeID)
	}
	if cacheKeyHash != "" {
		q.Set("cache_key_hash", cacheKeyHash)
	}
	if leaderRunID != "" {
		q.Set("leader_run_id", leaderRunID)
	}
	if leaderNodeID != "" {
		q.Set("leader_node_id", leaderNodeID)
	}
	if bypassRead {
		q.Set("bypass_read", "true")
	}
	u := fmt.Sprintf("%s/api/v1/concurrency/%s/resolve?%s", c.baseURL, url.PathEscape(key), q.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readHTTPError(resp)
	}
	var out WaiterResolution
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CancelWaiter drops a parked waiter row; reports whether one matched.
func (c *Client) CancelWaiter(ctx context.Context, key, runID, nodeID string) (bool, error) {
	body := map[string]any{"run_id": runID}
	if nodeID != "" {
		body["node_id"] = nodeID
	}
	buf, _ := json.Marshal(body)
	u := fmt.Sprintf("%s/api/v1/concurrency/%s/cancel-waiter", c.baseURL, url.PathEscape(key))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(buf))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, readHTTPError(resp)
	}
	var out struct {
		Cancelled bool `json:"cancelled"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return false, err
	}
	return out.Cancelled, nil
}

// ForceReleaseSuperseded drops superseded holders and promotes the next
// waiters. Returns the dropped holders.
func (c *Client) ForceReleaseSuperseded(ctx context.Context, key string) ([]WaiterHolder, error) {
	u := fmt.Sprintf("%s/api/v1/concurrency/%s/force-release", c.baseURL, url.PathEscape(key))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readHTTPError(resp)
	}
	var out struct {
		Dropped []WaiterHolder `json:"dropped"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out.Dropped, nil
}

// ReleaseSlot drops the holder row and optionally stores a cache
// entry when outcome=="success" and cacheKeyHash is non-empty.
func (c *Client) ReleaseSlot(ctx context.Context, key, holderID, outcome, outputRef, cacheKeyHash string, ttl time.Duration) error {
	body := map[string]any{
		"holder_id": holderID,
		"outcome":   outcome,
	}
	if outputRef != "" {
		body["output_ref"] = outputRef
	}
	if cacheKeyHash != "" {
		body["cache_key_hash"] = cacheKeyHash
	}
	if ttl > 0 {
		body["cache_ttl_ns"] = int64(ttl)
	}
	buf, _ := json.Marshal(body)
	u := fmt.Sprintf("%s/api/v1/concurrency/%s/release", c.baseURL, url.PathEscape(key))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return readHTTPError(resp)
	}
	return nil
}
