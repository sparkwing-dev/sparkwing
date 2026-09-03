package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// ListPendingTriggersForParent returns the ids of triggers a parent run
// spawned that no consumer has claimed yet, oldest first. Mirrors
// store.ListPendingTriggersForParent.
func (c *Client) ListPendingTriggersForParent(ctx context.Context, parentRunID string) ([]string, error) {
	u := fmt.Sprintf("%s/api/v1/runs/%s/pending-triggers", c.baseURL, url.PathEscape(parentRunID))
	var body struct {
		TriggerIDs []string `json:"trigger_ids"`
	}
	if err := c.getJSON(ctx, u, &body); err != nil {
		return nil, err
	}
	return body.TriggerIDs, nil
}

// ClaimSpecificTrigger claims one named trigger for lease. Returns
// store.ErrNotFound when the trigger is gone or another consumer holds it,
// which is the signal to try the next candidate.
func (c *Client) ClaimSpecificTrigger(ctx context.Context, id string, lease time.Duration) (*store.Trigger, error) {
	path := fmt.Sprintf("/api/v1/triggers/%s/claim", url.PathEscape(id))
	req, err := c.jsonRequest(ctx, http.MethodPost, c.baseURL+path,
		claimSpecificTriggerBody{LeaseNanos: lease.Nanoseconds()})
	if err != nil {
		return nil, err
	}
	resp, err := c.do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		var tr store.Trigger
		if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
			return nil, err
		}
		return &tr, nil
	case http.StatusNotFound:
		return nil, notFound(resp)
	default:
		return nil, readHTTPError(resp)
	}
}

type claimSpecificTriggerBody struct {
	LeaseNanos int64 `json:"lease_nanos,omitempty"`
}

// RecordProfileObservation folds one run's measurement into the
// (pipeline, node) capacity profile. Empty nodeID targets the pipeline
// rollup. Mirrors store.RecordProfileObservation.
func (c *Client) RecordProfileObservation(ctx context.Context, pipeline, nodeID string, obs store.ProfileObservation) error {
	return c.postProfile(ctx, pipeline, nodeID, "observations", profileObservationBody{
		DurationNanos:    obs.Duration.Nanoseconds(),
		PeakCores:        obs.PeakCores,
		PeakMemoryBytes:  obs.PeakMemoryBytes,
		SustainedCores:   obs.SustainedCores,
		CPUMeasured:      obs.CPUMeasured,
		PlanHash:         obs.PlanHash,
		Contended:        obs.Contended,
		FloorCores:       obs.FloorCores,
		FloorMemoryBytes: obs.FloorMemoryBytes,
	})
}

type profileObservationBody struct {
	DurationNanos    int64   `json:"duration_nanos,omitempty"`
	PeakCores        float64 `json:"peak_cores,omitempty"`
	PeakMemoryBytes  int64   `json:"peak_memory_bytes,omitempty"`
	SustainedCores   float64 `json:"sustained_cores,omitempty"`
	CPUMeasured      bool    `json:"cpu_measured,omitempty"`
	PlanHash         string  `json:"plan_hash,omitempty"`
	Contended        bool    `json:"contended,omitempty"`
	FloorCores       float64 `json:"floor_cores,omitempty"`
	FloorMemoryBytes int64   `json:"floor_memory_bytes,omitempty"`
}

// RecordContention marks that a run of this pipeline was throttled by host
// contention. Mirrors store.RecordContention.
func (c *Client) RecordContention(ctx context.Context, pipeline string) error {
	return c.postProfile(ctx, pipeline, "", "contention", nil)
}

// RecordWaitObservation folds one run's admission queue wait into the
// pipeline's profile. Mirrors store.RecordWaitObservation.
func (c *Client) RecordWaitObservation(ctx context.Context, pipeline string, wait time.Duration) error {
	return c.postProfile(ctx, pipeline, "", "waits", waitObservationBody{WaitNanos: wait.Nanoseconds()})
}

type waitObservationBody struct {
	WaitNanos int64 `json:"wait_nanos"`
}

func (c *Client) postProfile(ctx context.Context, pipeline, nodeID, leaf string, body any) error {
	path := fmt.Sprintf("/api/v1/pipelines/%s/profile/%s", url.PathEscape(pipeline), leaf)
	if nodeID != "" {
		path += "?node=" + url.QueryEscape(nodeID)
	}
	return c.post(ctx, path, body, http.StatusNoContent, nil)
}

// AddNodeUsage folds one finished process's accounting into a node's row.
// Mirrors store.AddNodeUsage.
func (c *Client) AddNodeUsage(ctx context.Context, runID, nodeID string, u store.NodeUsage) error {
	path := fmt.Sprintf("/api/v1/runs/%s/nodes/%s/usage",
		url.PathEscape(runID), url.PathEscape(nodeID))
	return c.post(ctx, path, nodeUsageBody{
		CPUTimeNanos: u.CPUTime.Nanoseconds(),
		MaxRSSBytes:  u.MaxRSSBytes,
		WallNanos:    u.Wall.Nanoseconds(),
	}, http.StatusNoContent, nil)
}

type nodeUsageBody struct {
	CPUTimeNanos int64 `json:"cpu_time_nanos,omitempty"`
	MaxRSSBytes  int64 `json:"max_rss_bytes,omitempty"`
	WallNanos    int64 `json:"wall_nanos,omitempty"`
}

// ListNodeMetrics returns a node's resource samples oldest-first. Mirrors
// store.ListNodeMetrics.
func (c *Client) ListNodeMetrics(ctx context.Context, runID, nodeID string) ([]store.MetricSample, error) {
	u := fmt.Sprintf("%s/api/v1/runs/%s/nodes/%s/metrics", c.baseURL,
		url.PathEscape(runID), url.PathEscape(nodeID))
	var body struct {
		Points []struct {
			TS            string `json:"ts"`
			CPUMillicores int64  `json:"cpu_millicores"`
			MemoryBytes   int64  `json:"memory_bytes"`
			CPUTimeNanos  int64  `json:"cpu_time_nanos"`
		} `json:"points"`
	}
	if err := c.getJSON(ctx, u, &body); err != nil {
		return nil, err
	}
	out := make([]store.MetricSample, 0, len(body.Points))
	for _, p := range body.Points {
		ts, err := time.Parse(time.RFC3339Nano, p.TS)
		if err != nil {
			return nil, fmt.Errorf("metric sample ts %q: %w", p.TS, err)
		}
		out = append(out, store.MetricSample{
			TS:            ts,
			CPUMillicores: p.CPUMillicores,
			MemoryBytes:   p.MemoryBytes,
			CPUTime:       time.Duration(p.CPUTimeNanos),
		})
	}
	return out, nil
}

// ReconcileOrphanedLocalRuns finishes runs left "running" by a process that
// died, and returns how many it closed. threshold is the idle age a run must
// exceed; the controller refuses a non-positive one.
func (c *Client) ReconcileOrphanedLocalRuns(ctx context.Context, threshold time.Duration) (int, error) {
	var out reconcileOrphansBody
	if err := c.post(ctx, "/api/v1/maintenance/reconcile-orphans",
		reconcileOrphansRequest{ThresholdNanos: threshold.Nanoseconds()}, http.StatusOK, &out); err != nil {
		return 0, err
	}
	return out.Reconciled, nil
}

type reconcileOrphansRequest struct {
	ThresholdNanos int64 `json:"threshold_nanos,omitempty"`
}

type reconcileOrphansBody struct {
	Reconciled int `json:"reconciled"`
}

func (c *Client) getJSON(ctx context.Context, u string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	resp, err := c.do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return json.NewDecoder(resp.Body).Decode(out)
	case http.StatusNotFound:
		return notFound(resp)
	default:
		return readHTTPError(resp)
	}
}

func (c *Client) jsonRequest(ctx context.Context, method, u string, body any) (*http.Request, error) {
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal body: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return req, nil
}
