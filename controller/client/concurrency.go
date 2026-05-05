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
// shape. Clients build one per arrival.
type AcquireSlotRequest struct {
	HolderID      string
	RunID         string
	NodeID        string
	Max           int
	Policy        string
	CacheKeyHash  string
	CacheTTL      time.Duration
	CancelTimeout time.Duration
	Lease         time.Duration
}

// AcquireSlotResponse surfaces the full controller response so the
// caller can branch on Kind. "granted" and "cached" both map to
// 200 OK with Granted=true; everything else is either 202 Accepted
// (Queued/Coalesced/CancellingOthers) or 429 (Skipped/Failed).
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
}

// AcquireSlot requests a concurrency slot under the RUN-015 unified
// primitive. The server performs the cache lookup, capacity check,
// and waiter-row insertion in a single transaction; the response
// describes the outcome so the caller can branch.
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

// HeartbeatSlotResponse carries the new lease expiry and the
// supersede flag the runner uses to short-circuit to its cancel path.
type HeartbeatSlotResponse struct {
	LeaseExpiresAt   time.Time `json:"lease_expires_at"`
	CancelledByNewer bool      `json:"cancelled_by_newer"`
}

// HeartbeatSlot extends the holder's lease. A CancelledByNewer=true
// return indicates that a CancelOthers arrival has marked this
// holder superseded; callers should abort.
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

// ReleaseSlot drops the holder row and optionally stores a cache
// entry when outcome == "success" and cacheKeyHash is non-empty.
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
