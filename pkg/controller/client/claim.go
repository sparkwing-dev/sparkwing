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

// HeartbeatClaim renews the calling claim token's claim on runID/nodeID for
// lease. It reports whether the run is being cancelled, and returns
// [store.ErrLockHeld] once the claim is lost or its team cannot pay.
func (c *Client) HeartbeatClaim(ctx context.Context, runID, nodeID string, lease time.Duration) (cancel bool, err error) {
	var out struct {
		Cancel bool `json:"cancel"`
	}
	err = c.post(ctx, claimNodePath(runID, nodeID, "heartbeat"),
		map[string]int{"lease_secs": max(int(lease.Seconds()), 1)}, http.StatusOK, &out)
	return out.Cancel, err
}

// StartClaimExecution records that the calling claim is about to run pipeline
// code and returns the spec hash the run's accepted plan holds for the node.
// The controller issues the claim no source credential after it.
func (c *Client) StartClaimExecution(ctx context.Context, runID, nodeID string) (specHash string, err error) {
	var out struct {
		SpecHash string `json:"spec_hash"`
	}
	err = c.post(ctx, claimNodePath(runID, nodeID, "execution-start"), nil, http.StatusOK, &out)
	return out.SpecHash, err
}

// SubmitPlan submits a planning claim's plan document for runID.
func (c *Client) SubmitPlan(ctx context.Context, runID string, plan []byte) error {
	return c.post(ctx, "/api/v1/runs/"+url.PathEscape(runID)+"/plan", json.RawMessage(plan), http.StatusOK, nil)
}

// ReportAttempt reports the end of the calling claim's attempt at its node.
func (c *Client) ReportAttempt(ctx context.Context, runID, nodeID string, report store.AttemptReport) error {
	return c.post(ctx, claimNodePath(runID, nodeID, "attempt"), report, http.StatusOK, nil)
}

// ChildRun is one RunAndAwait call a controller-dispatched node makes.
// Ordinal numbers the call within the node's attempt, so a retry of the same
// call starts nothing new.
type ChildRun struct {
	Ordinal  int64             `json:"ordinal"`
	Pipeline string            `json:"pipeline"`
	Args     map[string]string `json:"args,omitempty"`
	Repo     string            `json:"repo,omitempty"`
	Branch   string            `json:"branch,omitempty"`
}

// EnqueueChildRun starts, or returns the already started, child run of runID
// the calling claim's call names.
func (c *Client) EnqueueChildRun(ctx context.Context, runID string, child ChildRun) (string, error) {
	var out struct {
		RunID string `json:"run_id"`
	}
	err := c.post(ctx, "/api/v1/runs/"+url.PathEscape(runID)+"/children", child, http.StatusAccepted, &out)
	return out.RunID, err
}

// GetChildRun reads childID, a child run runID started, in its redacted form.
func (c *Client) GetChildRun(ctx context.Context, runID, childID string) (*store.Run, error) {
	var run store.Run
	if err := c.getJSON(ctx, c.baseURL+childPath(runID, childID), &run); err != nil {
		return nil, err
	}
	return &run, nil
}

// GetChildNodeOutput reads the output of nodeID in childID, a child run
// runID started.
func (c *Client) GetChildNodeOutput(ctx context.Context, runID, childID, nodeID string) ([]byte, error) {
	var out json.RawMessage
	err := c.getJSON(ctx, c.baseURL+childPath(runID, childID)+"/nodes/"+url.PathEscape(nodeID)+"/output", &out)
	return out, err
}

// ClaimInput reads, with runID/nodeID's claim token, the output the node
// takes from another run through a reference its plan declares; the
// controller picks the run.
func (c *Client) ClaimInput(ctx context.Context, runID, nodeID string, req store.ClaimInputRequest) (store.ClaimInput, error) {
	var out store.ClaimInput
	body, err := json.Marshal(req)
	if err != nil {
		return out, err
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+claimNodePath(runID, nodeID, "claim/input"), bytes.NewReader(body))
	if err != nil {
		return out, err
	}
	hreq.Header.Set("Content-Type", "application/json")
	resp, err := c.do(hreq)
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return out, json.NewDecoder(resp.Body).Decode(&out)
	case http.StatusNotFound:
		return out, notFound(resp)
	default:
		return out, readHTTPError(resp)
	}
}

// LauncherSync tells the controller the Jobs a launcher holds, and hears what
// to do with each.
func (c *Client) LauncherSync(ctx context.Context, jobs []store.LaunchJob) ([]store.LaunchJobResult, error) {
	if jobs == nil {
		jobs = []store.LaunchJob{}
	}
	var out struct {
		Jobs []store.LaunchJobResult `json:"jobs"`
	}
	err := c.post(ctx, "/api/v1/launcher/sync", map[string]any{"jobs": jobs}, http.StatusOK, &out)
	return out.Jobs, err
}

func claimNodePath(runID, nodeID, verb string) string {
	return fmt.Sprintf("/api/v1/runs/%s/nodes/%s/%s", url.PathEscape(runID), url.PathEscape(nodeID), verb)
}

func childPath(runID, childID string) string {
	return "/api/v1/runs/" + url.PathEscape(runID) + "/children/" + url.PathEscape(childID)
}
