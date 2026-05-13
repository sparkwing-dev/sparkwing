// Package client is the HTTP StateBackend implementation used by
// orchestrator pods to talk to the controller.
//
//	c := client.New("http://controller:4344", nil)
//	backends := orchestrator.Backends{State: c, Logs: ..., Locks: ...}
//	orchestrator.Run(ctx, backends, opts)
//
// Each method maps 1:1 to a controller endpoint.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
	"github.com/sparkwing-dev/sparkwing/otelutil"
)

// Client implements orchestrator.StateBackend over HTTP.
type Client struct {
	baseURL string
	http    *http.Client
}

// New constructs a Client targeting the given controller base URL.
// A nil httpClient uses a default with a 30s timeout.
func New(baseURL string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{
			Timeout:   30 * time.Second,
			Transport: otelutil.WrapTransport(nil),
		}
	}
	return &Client{baseURL: baseURL, http: httpClient}
}

// NewWithToken is New plus a shared-secret bearer token. Every
// outgoing request carries `Authorization: Bearer <token>`. Empty
// token is equivalent to New.
func NewWithToken(baseURL string, httpClient *http.Client, token string) *Client {
	ownsClient := httpClient == nil
	if ownsClient {
		httpClient = &http.Client{
			Timeout:   30 * time.Second,
			Transport: otelutil.WrapTransport(nil),
		}
	}
	if token != "" {
		base := httpClient.Transport
		if base == nil {
			base = http.DefaultTransport
		}
		httpClient = &http.Client{
			Timeout:       httpClient.Timeout,
			CheckRedirect: httpClient.CheckRedirect,
			Jar:           httpClient.Jar,
			Transport:     &bearerTransport{base: base, token: token},
		}
	}
	return &Client{baseURL: baseURL, http: httpClient}
}

// bearerTransport decorates outgoing requests with a fixed bearer
// token.
type bearerTransport struct {
	base  http.RoundTripper
	token string
}

func (t *bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(req)
}

// --- Runs ---

func (c *Client) CreateRun(ctx context.Context, r store.Run) error {
	return c.post(ctx, "/api/v1/runs", r, http.StatusCreated, nil)
}

// ListRuns fetches recent runs filtered by f. Mirrors store.ListRuns.
func (c *Client) ListRuns(ctx context.Context, f store.RunFilter) ([]*store.Run, error) {
	q := url.Values{}
	if len(f.Pipelines) > 0 {
		q.Set("pipeline", strings.Join(f.Pipelines, ","))
	}
	if len(f.Statuses) > 0 {
		q.Set("status", strings.Join(f.Statuses, ","))
	}
	if !f.Since.IsZero() {
		q.Set("since", time.Since(f.Since).String())
	}
	if f.Limit > 0 {
		q.Set("limit", fmt.Sprintf("%d", f.Limit))
	}
	u := c.baseURL + "/api/v1/runs"
	if enc := q.Encode(); enc != "" {
		u += "?" + enc
	}
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
	var body struct {
		Runs []*store.Run `json:"runs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	return body.Runs, nil
}

func (c *Client) GetRun(ctx context.Context, runID string) (*store.Run, error) {
	u := fmt.Sprintf("%s/api/v1/runs/%s", c.baseURL, url.PathEscape(runID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		var run store.Run
		if err := json.NewDecoder(resp.Body).Decode(&run); err != nil {
			return nil, err
		}
		return &run, nil
	case http.StatusNotFound:
		return nil, store.ErrNotFound
	default:
		return nil, readHTTPError(resp)
	}
}

// GetRunReceipt fetches the receipt JSON for a run. Returned as raw
// bytes so callers can pretty-print or pipe without round-trip
// re-encoding (the receipt's hashes commit to canonical bytes).
func (c *Client) GetRunReceipt(ctx context.Context, runID string) ([]byte, error) {
	u := fmt.Sprintf("%s/api/v1/runs/%s/receipt", c.baseURL, url.PathEscape(runID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return io.ReadAll(resp.Body)
	case http.StatusNotFound:
		return nil, store.ErrNotFound
	default:
		return nil, readHTTPError(resp)
	}
}

func (c *Client) ListNodes(ctx context.Context, runID string) ([]*store.Node, error) {
	u := fmt.Sprintf("%s/api/v1/runs/%s/nodes", c.baseURL, url.PathEscape(runID))
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
	var body struct {
		Nodes []*store.Node `json:"nodes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	return body.Nodes, nil
}

func (c *Client) FinishRun(ctx context.Context, runID, status, errMsg string) error {
	path := fmt.Sprintf("/api/v1/runs/%s/finish", url.PathEscape(runID))
	return c.post(ctx, path,
		map[string]string{"status": status, "error": errMsg},
		http.StatusNoContent, nil)
}

func (c *Client) UpdatePlanSnapshot(ctx context.Context, runID string, snapshot []byte) error {
	path := fmt.Sprintf("/api/v1/runs/%s/plan", url.PathEscape(runID))
	return c.postRaw(ctx, path, snapshot, http.StatusNoContent)
}

// --- Nodes ---

func (c *Client) CreateNode(ctx context.Context, n store.Node) error {
	path := fmt.Sprintf("/api/v1/runs/%s/nodes", url.PathEscape(n.RunID))
	return c.post(ctx, path, n, http.StatusCreated, nil)
}

func (c *Client) StartNode(ctx context.Context, runID, nodeID string) error {
	path := fmt.Sprintf("/api/v1/runs/%s/nodes/%s/start",
		url.PathEscape(runID), url.PathEscape(nodeID))
	return c.post(ctx, path, nil, http.StatusNoContent, nil)
}

func (c *Client) FinishNode(ctx context.Context, runID, nodeID, outcome, errMsg string, output []byte) error {
	return c.FinishNodeWithReason(ctx, runID, nodeID, outcome, errMsg, output, "", nil)
}

// FinishNodeWithReason is the wire equivalent of the store method.
func (c *Client) FinishNodeWithReason(ctx context.Context, runID, nodeID, outcome, errMsg string, output []byte, reason string, exitCode *int) error {
	path := fmt.Sprintf("/api/v1/runs/%s/nodes/%s/finish",
		url.PathEscape(runID), url.PathEscape(nodeID))
	body := map[string]any{
		"outcome": outcome,
		"error":   errMsg,
		"output":  output,
	}
	if reason != "" {
		body["failure_reason"] = reason
	}
	if exitCode != nil {
		body["exit_code"] = *exitCode
	}
	return c.post(ctx, path, body, http.StatusNoContent, nil)
}

func (c *Client) UpdateNodeDeps(ctx context.Context, runID, nodeID string, deps []string) error {
	path := fmt.Sprintf("/api/v1/runs/%s/nodes/%s/deps",
		url.PathEscape(runID), url.PathEscape(nodeID))
	return c.post(ctx, path,
		map[string]any{"deps": deps},
		http.StatusNoContent, nil)
}

// UpdateNodeActivity POSTs a status_detail update that also bumps
// last_heartbeat. Empty detail clears the prior detail.
func (c *Client) UpdateNodeActivity(ctx context.Context, runID, nodeID, detail string) error {
	path := fmt.Sprintf("/api/v1/runs/%s/nodes/%s/activity",
		url.PathEscape(runID), url.PathEscape(nodeID))
	return c.post(ctx, path,
		map[string]string{"detail": detail},
		http.StatusNoContent, nil)
}

// TouchNodeHeartbeat POSTs a zero-change heartbeat so the dashboard
// can distinguish a stalled runner from one still making progress.
func (c *Client) TouchNodeHeartbeat(ctx context.Context, runID, nodeID string) error {
	path := fmt.Sprintf("/api/v1/runs/%s/nodes/%s/touch",
		url.PathEscape(runID), url.PathEscape(nodeID))
	return c.post(ctx, path, nil, http.StatusNoContent, nil)
}

// AppendNodeAnnotation POSTs a single persistent summary string to
// append to the node's annotations list. Driven by sparkwing.Annotate.
func (c *Client) AppendNodeAnnotation(ctx context.Context, runID, nodeID, msg string) error {
	path := fmt.Sprintf("/api/v1/runs/%s/nodes/%s/annotations",
		url.PathEscape(runID), url.PathEscape(nodeID))
	return c.post(ctx, path,
		map[string]string{"message": msg},
		http.StatusNoContent, nil)
}

// SetNodeSummary POSTs the latest markdown run summary for the node.
// Driven by sparkwing.Summary() emitted outside any step body. The
// server overwrites the previous value.
func (c *Client) SetNodeSummary(ctx context.Context, runID, nodeID, md string) error {
	path := fmt.Sprintf("/api/v1/runs/%s/nodes/%s/summary",
		url.PathEscape(runID), url.PathEscape(nodeID))
	return c.post(ctx, path,
		map[string]string{"markdown": md},
		http.StatusNoContent, nil)
}

// StartNodeStep POSTs the running transition for one step. Body
// carries the step id; the server stamps started_at server-side.
func (c *Client) StartNodeStep(ctx context.Context, runID, nodeID, stepID string) error {
	path := fmt.Sprintf("/api/v1/runs/%s/nodes/%s/steps/start",
		url.PathEscape(runID), url.PathEscape(nodeID))
	return c.post(ctx, path,
		map[string]string{"step_id": stepID},
		http.StatusNoContent, nil)
}

// FinishNodeStep POSTs the terminal status (passed | failed) and
// finished_at stamp for one step.
func (c *Client) FinishNodeStep(ctx context.Context, runID, nodeID, stepID, status string) error {
	path := fmt.Sprintf("/api/v1/runs/%s/nodes/%s/steps/finish",
		url.PathEscape(runID), url.PathEscape(nodeID))
	return c.post(ctx, path,
		map[string]string{"step_id": stepID, "status": status},
		http.StatusNoContent, nil)
}

// SkipNodeStep records a step that was skipped before it ran (skipIf
// guard fired, dry-run with no body, etc.). Server inserts a single
// terminal row with started_at == finished_at.
func (c *Client) SkipNodeStep(ctx context.Context, runID, nodeID, stepID string) error {
	path := fmt.Sprintf("/api/v1/runs/%s/nodes/%s/steps/skip",
		url.PathEscape(runID), url.PathEscape(nodeID))
	return c.post(ctx, path,
		map[string]string{"step_id": stepID},
		http.StatusNoContent, nil)
}

// AppendStepAnnotation POSTs one persistent summary string onto a
// step's annotations list. Mirrors AppendNodeAnnotation but scoped
// to a single step inside a node's inner Work DAG.
func (c *Client) AppendStepAnnotation(ctx context.Context, runID, nodeID, stepID, msg string) error {
	path := fmt.Sprintf("/api/v1/runs/%s/nodes/%s/steps/annotations",
		url.PathEscape(runID), url.PathEscape(nodeID))
	return c.post(ctx, path,
		map[string]string{"step_id": stepID, "message": msg},
		http.StatusNoContent, nil)
}

// SetStepSummary POSTs the latest markdown run summary for a step.
// Driven by sparkwing.Summary() emitted inside a step body. The
// server overwrites the previous value.
func (c *Client) SetStepSummary(ctx context.Context, runID, nodeID, stepID, md string) error {
	path := fmt.Sprintf("/api/v1/runs/%s/nodes/%s/steps/summary",
		url.PathEscape(runID), url.PathEscape(nodeID))
	return c.post(ctx, path,
		map[string]string{"step_id": stepID, "markdown": md},
		http.StatusNoContent, nil)
}

// ListNodeSteps fetches every step row for the run, across nodes.
// The wire shape matches the local store (per-step status + started/
// finished timestamps); the dashboard's nodes endpoint joins this in
// when serving ?include=nodes.
func (c *Client) ListNodeSteps(ctx context.Context, runID string) ([]*store.NodeStep, error) {
	u := fmt.Sprintf("%s/api/v1/runs/%s/steps", c.baseURL, url.PathEscape(runID))
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
	var body struct {
		Steps []*store.NodeStep `json:"steps"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	return body.Steps, nil
}

// --- Events ---

func (c *Client) AppendEvent(ctx context.Context, runID, nodeID, kind string, payload []byte) error {
	path := fmt.Sprintf("/api/v1/runs/%s/events", url.PathEscape(runID))
	return c.post(ctx, path,
		map[string]any{"node_id": nodeID, "kind": kind, "payload": payload},
		http.StatusOK, nil)
}

// --- Metrics ---

// AddNodeMetricSample POSTs a single resource sample for a node.
func (c *Client) AddNodeMetricSample(ctx context.Context, runID, nodeID string, sample store.MetricSample) error {
	path := fmt.Sprintf("/api/v1/runs/%s/nodes/%s/metrics",
		url.PathEscape(runID), url.PathEscape(nodeID))
	body := map[string]any{
		"ts":             sample.TS.UTC().Format("2006-01-02T15:04:05.000000000Z07:00"),
		"cpu_millicores": sample.CPUMillicores,
		"memory_bytes":   sample.MemoryBytes,
	}
	return c.post(ctx, path, body, http.StatusNoContent, nil)
}

// --- Triggers ---

// HeartbeatStatus is the structured response from a heartbeat call.
type HeartbeatStatus struct {
	CancelRequested bool `json:"cancel_requested"`
}

// HeartbeatTrigger extends the claim lease on a trigger and returns
// whether cancellation has been requested. ErrNotFound means the
// trigger was reaped or never existed; the worker should abort.
func (c *Client) HeartbeatTrigger(ctx context.Context, id string) (*HeartbeatStatus, error) {
	path := fmt.Sprintf("/api/v1/triggers/%s/heartbeat", url.PathEscape(id))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		var status HeartbeatStatus
		if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
			return nil, err
		}
		return &status, nil
	case http.StatusNoContent:
		// Older controllers without cancel support: treat as not-cancelled.
		return &HeartbeatStatus{}, nil
	case http.StatusNotFound:
		return nil, store.ErrNotFound
	default:
		return nil, readHTTPError(resp)
	}
}

// TriggerRequest is the body of POST /api/v1/triggers.
type TriggerRequest struct {
	Pipeline string            `json:"pipeline"`
	Args     map[string]string `json:"args,omitempty"`
	Trigger  TriggerMeta       `json:"trigger"`
	Git      GitMeta           `json:"git"`
	// ParentRunID threads cross-pipeline ancestry so the controller
	// can reject cycles.
	ParentRunID string `json:"parent_run_id,omitempty"`
	// ParentNodeID identifies which node of the parent run did the
	// spawning, so a parent retry can locate the prior child by
	// (parent_run_id, parent_node_id, pipeline).
	ParentNodeID string `json:"parent_node_id,omitempty"`
	// RetryOf marks this trigger as a retry of the named run; used by
	// skip-passed rehydration to seed outputs from the prior run.
	RetryOf string `json:"retry_of,omitempty"`
}

// TriggerMeta mirrors the controller's trigger block. Kept separate
// from store.Trigger because the wire contract shouldn't drift with
// internal schema changes.
type TriggerMeta struct {
	Source string            `json:"source,omitempty"`
	User   string            `json:"user,omitempty"`
	Env    map[string]string `json:"env,omitempty"`
}

// GitMeta is the optional git state attached to a trigger. Any field
// added here must be echoed on the server's `git` block so it lands
// in the trigger row.
type GitMeta struct {
	Branch      string `json:"branch,omitempty"`
	SHA         string `json:"sha,omitempty"`
	Repo        string `json:"repo,omitempty"`
	RepoURL     string `json:"repo_url,omitempty"`
	GithubOwner string `json:"github_owner,omitempty"`
	GithubRepo  string `json:"github_repo,omitempty"`
}

// TriggerResponse is the 202 body from a successful trigger post.
type TriggerResponse struct {
	RunID  string `json:"run_id"`
	Status string `json:"status"`
}

// CreateTrigger posts a new trigger and returns the assigned run id.
func (c *Client) CreateTrigger(ctx context.Context, req TriggerRequest) (*TriggerResponse, error) {
	var resp TriggerResponse
	if err := c.post(ctx, "/api/v1/triggers", req, http.StatusAccepted, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// FinishTrigger flips a trigger to 'done' after the worker's Run
// terminates. Without this the reaper re-queues the trigger and the
// next claim fails on the UNIQUE(runs.id) constraint.
func (c *Client) FinishTrigger(ctx context.Context, triggerID string) error {
	path := fmt.Sprintf("/api/v1/triggers/%s/done", url.PathEscape(triggerID))
	return c.post(ctx, path, nil, http.StatusNoContent, nil)
}

// ListTriggers fetches triggers filtered by f. Mirrors
// store.ListTriggers.
func (c *Client) ListTriggers(ctx context.Context, f store.TriggerFilter) ([]*store.Trigger, error) {
	q := url.Values{}
	if len(f.Statuses) > 0 {
		q.Set("status", strings.Join(f.Statuses, ","))
	}
	if len(f.Pipelines) > 0 {
		q.Set("pipeline", strings.Join(f.Pipelines, ","))
	}
	if f.Repo != "" {
		q.Set("repo", f.Repo)
	}
	if f.Limit > 0 {
		q.Set("limit", fmt.Sprintf("%d", f.Limit))
	}
	u := c.baseURL + "/api/v1/triggers"
	if enc := q.Encode(); enc != "" {
		u += "?" + enc
	}
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
	var body struct {
		Triggers []*store.Trigger `json:"triggers"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	return body.Triggers, nil
}

// GetTrigger fetches a single trigger's full row. Returns
// store.ErrNotFound on 404.
func (c *Client) GetTrigger(ctx context.Context, triggerID string) (*store.Trigger, error) {
	path := fmt.Sprintf("/api/v1/triggers/%s", url.PathEscape(triggerID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
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
		return nil, store.ErrNotFound
	default:
		return nil, readHTTPError(resp)
	}
}

// CancelRun records an operator cancellation request. Idempotent.
// ErrNotFound when the run doesn't exist.
func (c *Client) CancelRun(ctx context.Context, runID string) error {
	path := fmt.Sprintf("/api/v1/runs/%s/cancel", url.PathEscape(runID))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNoContent:
		return nil
	case http.StatusNotFound:
		return store.ErrNotFound
	default:
		return readHTTPError(resp)
	}
}

// DeleteRun removes a run (and its nodes/events/trigger row).
// Idempotent: missing ids return nil.
func (c *Client) DeleteRun(ctx context.Context, runID string) error {
	u := fmt.Sprintf("%s/api/v1/runs/%s", c.baseURL, url.PathEscape(runID))
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, u, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNoContent, http.StatusNotFound:
		return nil
	default:
		return readHTTPError(resp)
	}
}

// ClaimTrigger asks the controller for the next pending trigger,
// atomically flipping it to 'claimed'. Returns (nil, nil) when the
// queue is empty.
func (c *Client) ClaimTrigger(ctx context.Context) (*store.Trigger, error) {
	return c.ClaimTriggerFor(ctx, nil, nil)
}

// ClaimTriggerFor is ClaimTrigger with optional pipeline and
// trigger_source filters. The controller returns only triggers that
// match both lists. Empty/nil on either axis means "accept any".
func (c *Client) ClaimTriggerFor(ctx context.Context, pipelines []string, sources []string) (*store.Trigger, error) {
	var body io.Reader
	if len(pipelines) > 0 || len(sources) > 0 {
		req := map[string]any{}
		if len(pipelines) > 0 {
			req["pipelines"] = pipelines
		}
		if len(sources) > 0 {
			req["trigger_sources"] = sources
		}
		buf, _ := json.Marshal(req)
		body = bytes.NewReader(buf)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/api/v1/triggers/claim", body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		httpReq.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		var t store.Trigger
		if err := json.NewDecoder(resp.Body).Decode(&t); err != nil {
			return nil, err
		}
		return &t, nil
	case http.StatusNoContent:
		return nil, nil
	default:
		return nil, readHTTPError(resp)
	}
}

// FindSpawnedChildTriggerID asks the controller for the most-recent
// child trigger spawned at (parentRunID, parentNodeID) targeting
// pipeline. Returns "" + nil error when no match.
func (c *Client) FindSpawnedChildTriggerID(ctx context.Context, parentRunID, parentNodeID, pipeline string) (string, error) {
	if parentRunID == "" || parentNodeID == "" || pipeline == "" {
		return "", nil
	}
	q := url.Values{}
	q.Set("parent_run_id", parentRunID)
	q.Set("parent_node_id", parentNodeID)
	q.Set("pipeline", pipeline)
	u := c.baseURL + "/api/v1/triggers/spawned-child?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", readHTTPError(resp)
	}
	var body struct {
		RunID string `json:"run_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", err
	}
	return body.RunID, nil
}

// EnqueueTrigger matches orchestrator.StateBackend's shape for
// spawning a new pipeline run from inside another. Cycle errors from
// the controller pass through verbatim.
func (c *Client) EnqueueTrigger(ctx context.Context, pipeline string, args map[string]string, parentRunID, parentNodeID, retryOf, source, user, repo, branch string) (string, error) {
	req := TriggerRequest{
		Pipeline:     pipeline,
		Args:         args,
		ParentRunID:  parentRunID,
		ParentNodeID: parentNodeID,
		RetryOf:      retryOf,
		Trigger: TriggerMeta{
			Source: source,
			User:   user,
		},
	}
	// Cross-repo await: without this the controller inherits parent's
	// repo+SHA and builds the wrong code for awaited pipelines in
	// different repos.
	if repo != "" {
		req.Git.Repo = repo
		req.Git.Branch = branch
		if i := indexByte(repo, '/'); i > 0 && i < len(repo)-1 {
			req.Git.GithubOwner = repo[:i]
			req.Git.GithubRepo = repo[i+1:]
		}
	}
	resp, err := c.CreateTrigger(ctx, req)
	if err != nil {
		return "", err
	}
	return resp.RunID, nil
}

func indexByte(s string, b byte) int {
	for i := range len(s) {
		if s[i] == b {
			return i
		}
	}
	return -1
}

// --- Cross-pipeline refs ---

// GetLatestRun returns the newest run of pipeline whose status is in
// statuses (default "success" when nil) and whose finished_at is
// within maxAge (0 disables the bound). ErrNotFound when nothing
// matches.
func (c *Client) GetLatestRun(ctx context.Context, pipeline string, statuses []string, maxAge time.Duration) (*store.Run, error) {
	if pipeline == "" {
		return nil, errors.New("GetPipelineLatest: pipeline is required")
	}
	q := url.Values{}
	if len(statuses) > 0 {
		q.Set("status", strings.Join(statuses, ","))
	}
	if maxAge > 0 {
		q.Set("max_age", maxAge.String())
	}
	u := fmt.Sprintf("%s/api/v1/pipelines/%s/latest", c.baseURL, url.PathEscape(pipeline))
	if enc := q.Encode(); enc != "" {
		u += "?" + enc
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		var run store.Run
		if err := json.NewDecoder(resp.Body).Decode(&run); err != nil {
			return nil, err
		}
		return &run, nil
	case http.StatusNotFound:
		return nil, store.ErrNotFound
	default:
		return nil, readHTTPError(resp)
	}
}

// --- Cluster-mode: node reads ---

// GetNode fetches one node.
func (c *Client) GetNode(ctx context.Context, runID, nodeID string) (*store.Node, error) {
	u := fmt.Sprintf("%s/api/v1/runs/%s/nodes/%s",
		c.baseURL, url.PathEscape(runID), url.PathEscape(nodeID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		var n store.Node
		if err := json.NewDecoder(resp.Body).Decode(&n); err != nil {
			return nil, err
		}
		return &n, nil
	case http.StatusNotFound:
		return nil, store.ErrNotFound
	default:
		return nil, readHTTPError(resp)
	}
}

// GetNodeOutput returns the raw JSON output of a finished node, or
// ErrNotFound if the node doesn't exist. 409 if it exists but hasn't
// finished.
func (c *Client) GetNodeOutput(ctx context.Context, runID, nodeID string) ([]byte, error) {
	u := fmt.Sprintf("%s/api/v1/runs/%s/nodes/%s/output",
		c.baseURL, url.PathEscape(runID), url.PathEscape(nodeID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return io.ReadAll(resp.Body)
	case http.StatusNotFound:
		return nil, store.ErrNotFound
	default:
		return nil, readHTTPError(resp)
	}
}

// --- Cluster-mode: node claim for warm runner pool ---

// ClaimNode atomically claims the oldest ready, unclaimed node for
// holderID. Returns (nil, nil) when the queue is empty.
//
// The controller filters candidates so a returned node's needs_labels
// is a subset of labels (AND semantics).
func (c *Client) ClaimNode(ctx context.Context, holderID string, labels []string, lease time.Duration) (*store.Node, error) {
	body := map[string]any{"holder_id": holderID}
	if lease > 0 {
		secs := int(lease.Seconds())
		if secs <= 0 {
			secs = 1
		}
		body["lease_secs"] = secs
	}
	if len(labels) > 0 {
		body["labels"] = labels
	}
	buf, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/api/v1/nodes/claim", bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		var n store.Node
		if err := json.NewDecoder(resp.Body).Decode(&n); err != nil {
			return nil, err
		}
		return &n, nil
	case http.StatusNoContent:
		return nil, nil
	default:
		return nil, readHTTPError(resp)
	}
}

// MarkNodeReady sets ready_at on a node so pool runners can claim it.
// Idempotent; first call wins (stable FIFO ordering).
func (c *Client) MarkNodeReady(ctx context.Context, runID, nodeID string) error {
	path := fmt.Sprintf("/api/v1/runs/%s/nodes/%s/mark-ready",
		url.PathEscape(runID), url.PathEscape(nodeID))
	return c.post(ctx, path, nil, http.StatusNoContent, nil)
}

// RevokeNodeReady atomically clears ready_at IFF the node is not
// currently claimed. Returns true on success; false means a warm
// runner claimed the node in the meantime.
func (c *Client) RevokeNodeReady(ctx context.Context, runID, nodeID string) (bool, error) {
	path := fmt.Sprintf("/api/v1/runs/%s/nodes/%s/revoke-ready",
		url.PathEscape(runID), url.PathEscape(nodeID))
	var resp struct {
		Revoked bool `json:"revoked"`
	}
	if err := c.post(ctx, path, nil, http.StatusOK, &resp); err != nil {
		return false, err
	}
	return resp.Revoked, nil
}

// HeartbeatNodeClaim extends the claim lease for holderID. ErrLockHeld
// when the caller isn't the current claim holder.
func (c *Client) HeartbeatNodeClaim(ctx context.Context, runID, nodeID, holderID string, lease time.Duration) error {
	path := fmt.Sprintf("/api/v1/runs/%s/nodes/%s/heartbeat",
		url.PathEscape(runID), url.PathEscape(nodeID))
	body := map[string]any{"holder_id": holderID}
	if lease > 0 {
		secs := int(lease.Seconds())
		if secs <= 0 {
			secs = 1
		}
		body["lease_secs"] = secs
	}
	buf, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+path, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNoContent:
		return nil
	case http.StatusConflict:
		return store.ErrLockHeld
	default:
		return readHTTPError(resp)
	}
}

// --- Debug pauses ---

func (c *Client) CreateDebugPause(ctx context.Context, p store.DebugPause) error {
	path := fmt.Sprintf("/api/v1/runs/%s/debug-pauses", url.PathEscape(p.RunID))
	return c.post(ctx, path, p, http.StatusCreated, nil)
}

func (c *Client) GetActiveDebugPause(ctx context.Context, runID, nodeID string) (*store.DebugPause, error) {
	u := fmt.Sprintf("%s/api/v1/runs/%s/nodes/%s/debug-pause",
		c.baseURL, url.PathEscape(runID), url.PathEscape(nodeID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, store.ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return nil, readHTTPError(resp)
	}
	var out store.DebugPause
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) ReleaseDebugPause(ctx context.Context, runID, nodeID, releasedBy, kind string) error {
	path := fmt.Sprintf("/api/v1/runs/%s/nodes/%s/release",
		url.PathEscape(runID), url.PathEscape(nodeID))
	return c.post(ctx, path,
		map[string]string{"released_by": releasedBy, "release_kind": kind},
		http.StatusNoContent, nil)
}

// ListEventsAfter pulls events for a run with seq > afterSeq.
// Mirrors Store.ListEventsAfter.
func (c *Client) ListEventsAfter(ctx context.Context, runID string, afterSeq int64, limit int) ([]store.Event, error) {
	if limit <= 0 {
		limit = 500
	}
	u := fmt.Sprintf("%s/api/v1/runs/%s/events?after=%d&limit=%d",
		c.baseURL, url.PathEscape(runID), afterSeq, limit)
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
	var out []store.Event
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) ListDebugPauses(ctx context.Context, runID string) ([]*store.DebugPause, error) {
	u := fmt.Sprintf("%s/api/v1/runs/%s/debug-pauses", c.baseURL, url.PathEscape(runID))
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
	var out []*store.DebugPause
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) SetNodeStatus(ctx context.Context, runID, nodeID, status string) error {
	path := fmt.Sprintf("/api/v1/runs/%s/nodes/%s/status",
		url.PathEscape(runID), url.PathEscape(nodeID))
	return c.post(ctx, path,
		map[string]string{"status": status},
		http.StatusNoContent, nil)
}

// --- Approvals ---

// CreateApproval requests a human decision on a gated node. The
// controller inserts an approvals row and flips the node's status to
// approval_pending atomically.
func (c *Client) CreateApproval(ctx context.Context, a store.Approval) error {
	path := fmt.Sprintf("/api/v1/runs/%s/approvals/%s/request",
		url.PathEscape(a.RunID), url.PathEscape(a.NodeID))
	body := map[string]any{
		"message":    a.Message,
		"timeout_ms": a.TimeoutMS,
		"on_timeout": a.OnTimeout,
	}
	return c.post(ctx, path, body, http.StatusCreated, nil)
}

// GetApproval fetches a single approval row, pending or resolved.
// ErrNotFound when the row doesn't exist.
func (c *Client) GetApproval(ctx context.Context, runID, nodeID string) (*store.Approval, error) {
	u := fmt.Sprintf("%s/api/v1/runs/%s/approvals/%s",
		c.baseURL, url.PathEscape(runID), url.PathEscape(nodeID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		var a store.Approval
		if err := json.NewDecoder(resp.Body).Decode(&a); err != nil {
			return nil, err
		}
		return &a, nil
	case http.StatusNotFound:
		return nil, store.ErrNotFound
	default:
		return nil, readHTTPError(resp)
	}
}

// ResolveApproval writes a resolution onto a pending row. The server
// populates approver from the authenticated principal; the parameter
// is only respected for orchestrator-written timeouts and tests. 409
// maps to store.ErrLockHeld (already resolved).
func (c *Client) ResolveApproval(ctx context.Context, runID, nodeID, resolution, approver, comment string) (*store.Approval, error) {
	path := fmt.Sprintf("/api/v1/runs/%s/approvals/%s",
		url.PathEscape(runID), url.PathEscape(nodeID))
	body := map[string]any{
		"resolution": resolution,
		"comment":    comment,
	}
	if approver != "" {
		body["approver"] = approver
	}
	buf, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		var out store.Approval
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return nil, err
		}
		return &out, nil
	case http.StatusNotFound:
		return nil, store.ErrNotFound
	case http.StatusConflict:
		return nil, store.ErrLockHeld
	default:
		return nil, readHTTPError(resp)
	}
}

// ListApprovalsForRun returns every approval row for a run.
func (c *Client) ListApprovalsForRun(ctx context.Context, runID string) ([]*store.Approval, error) {
	u := fmt.Sprintf("%s/api/v1/runs/%s/approvals", c.baseURL, url.PathEscape(runID))
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
	var body struct {
		Approvals []*store.Approval `json:"approvals"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	return body.Approvals, nil
}

// ListPendingApprovals returns every unresolved approval, oldest-first.
func (c *Client) ListPendingApprovals(ctx context.Context) ([]*store.Approval, error) {
	u := c.baseURL + "/api/v1/approvals/pending"
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
	var body struct {
		Approvals []*store.Approval `json:"approvals"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	return body.Approvals, nil
}

// --- low-level helpers ---

// post marshals body as JSON, POSTs to path, and checks the status.
// If out is non-nil, the response is JSON-decoded into it. body may
// be nil.
func (c *Client) post(ctx context.Context, path string, body any, wantStatus int, out any) error {
	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal body: %w", err)
		}
		reader = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != wantStatus {
		return readHTTPError(resp)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

// postRaw sends an octet-stream POST.
func (c *Client) postRaw(ctx context.Context, path string, body []byte, wantStatus int) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != wantStatus {
		return readHTTPError(resp)
	}
	return nil
}

// readHTTPError unpacks a non-2xx response into a typed error.
// Controller returns `{"error": "msg"}` on failures; extract that
// so callers see the server's reason.
func readHTTPError(resp *http.Response) error {
	body, _ := io.ReadAll(resp.Body)
	var payload struct {
		Error string `json:"error"`
	}
	if len(body) > 0 && json.Unmarshal(body, &payload) == nil && payload.Error != "" {
		return fmt.Errorf("controller %d: %s", resp.StatusCode, payload.Error)
	}
	if len(body) > 0 {
		return fmt.Errorf("controller %d: %s", resp.StatusCode, bytes.TrimSpace(body))
	}
	return errors.New(resp.Status)
}
