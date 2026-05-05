// Package client is the HTTP StateBackend implementation used by
// orchestrator pods to talk to the controller. Drop-in replacement
// for orchestrator.LocalBackends' State field:
//
//	c := client.New("http://controller:4344", nil)
//	backends := orchestrator.Backends{
//	    State: c,
//	    Logs:  ...,
//	    Locks: ...,
//	}
//	orchestrator.Run(ctx, backends, opts)
//
// Each method maps 1:1 to a controller endpoint. Wire format is the
// same JSON the server uses on store.Run / store.Node / store.CacheEntry.
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
	baseURL string       // e.g. "http://controller:4344"
	http    *http.Client // nil means use a default client
}

// New constructs a Client targeting the given controller base URL.
// A nil httpClient uses a default with a 30s timeout; pass a custom
// client to override retry/transport behavior.
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
// outgoing request carries `Authorization: Bearer <token>`. Use for
// laptop agents that talk to a controller with agent-token auth
// enabled. An empty token is equivalent to New.
func NewWithToken(baseURL string, httpClient *http.Client, token string) *Client {
	// When the caller supplies a pre-built client we trust their
	// transport stack (tests, custom retry wrappers, etc.) and only
	// layer on bearer auth. When we build the default, we start with
	// an otelhttp-wrapped transport so every outbound request
	// propagates W3C trace-context headers.
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
// token. Kept private: callers construct it via NewWithToken.
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

// ListRuns fetches recent runs filtered by f. Mirrors store.ListRuns
// semantics; filters are encoded as query params the controller
// understands.
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
// Controller unmarshals the optional reason + exit_code fields and
// persists them alongside the terminal state.
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
// last_heartbeat. Empty detail is valid and simply clears any prior
// detail while still refreshing the heartbeat.
func (c *Client) UpdateNodeActivity(ctx context.Context, runID, nodeID, detail string) error {
	path := fmt.Sprintf("/api/v1/runs/%s/nodes/%s/activity",
		url.PathEscape(runID), url.PathEscape(nodeID))
	return c.post(ctx, path,
		map[string]string{"detail": detail},
		http.StatusNoContent, nil)
}

// TouchNodeHeartbeat POSTs a zero-change heartbeat so the dashboard
// can distinguish a stalled runner from one still making progress.
// Called on a ticker while the node executes.
func (c *Client) TouchNodeHeartbeat(ctx context.Context, runID, nodeID string) error {
	path := fmt.Sprintf("/api/v1/runs/%s/nodes/%s/touch",
		url.PathEscape(runID), url.PathEscape(nodeID))
	return c.post(ctx, path, nil, http.StatusNoContent, nil)
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
// Drop-on-error is acceptable to the caller; controller responds
// 204 on success.
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

// HeartbeatTrigger extends the claim lease on a trigger the worker
// is currently processing and returns whether the operator has
// requested cancellation. ErrNotFound means the trigger was reaped
// (worker considered dead) or never existed -- worker should abort
// its in-flight run.
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
		// Older controllers (no cancel support) -- treat as not-cancelled.
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
	// ParentRunID, when set, threads cross-pipeline ancestry through
	// the trigger so the controller can reject cycles. Populated by
	// sparkwing.AwaitPipelineJob from its caller's run id; external
	// webhook callers leave this empty.
	ParentRunID string `json:"parent_run_id,omitempty"`
	// ParentNodeID identifies which node of the parent run did the
	// spawning. Stored on the trigger so a future retry of the parent
	// can locate the prior child by (parent_run_id, parent_node_id,
	// pipeline) and thread its run id into RetryOf below. Empty for
	// non-spawn triggers (webhook, CLI).
	ParentNodeID string `json:"parent_node_id,omitempty"`
	// RetryOf, when set, marks this trigger as a retry of the named
	// run. The orchestrator's skip-passed rehydration uses it to
	// seed outputs from the prior run's node rows.
	RetryOf string `json:"retry_of,omitempty"`
}

// TriggerMeta mirrors the controller's trigger block. Kept separate
// from store.Trigger because the wire contract is user-facing and
// shouldn't drift with internal schema changes.
type TriggerMeta struct {
	Source string            `json:"source,omitempty"`
	User   string            `json:"user,omitempty"`
	Env    map[string]string `json:"env,omitempty"`
}

// GitMeta is the optional git state attached to a trigger. The
// controller persists every populated field so the dashboard can
// render branch/commit/repo without re-fetching from GitHub. Mirrors
// the `git` block on the triggerReq server type — any field added
// here must be echoed on the server so it lands in the trigger row.
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
// Used by `sparkwing jobs retry` and any CLI-level dispatch path.
func (c *Client) CreateTrigger(ctx context.Context, req TriggerRequest) (*TriggerResponse, error) {
	var resp TriggerResponse
	if err := c.post(ctx, "/api/v1/triggers", req, http.StatusAccepted, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// FinishTrigger flips a trigger to 'done' after the worker's Run
// terminates. Without this, the reaper re-queues the trigger once
// the claim lease expires, and the next worker that claims fails
// setup on the UNIQUE(runs.id) constraint.
func (c *Client) FinishTrigger(ctx context.Context, triggerID string) error {
	path := fmt.Sprintf("/api/v1/triggers/%s/done", url.PathEscape(triggerID))
	return c.post(ctx, path, nil, http.StatusNoContent, nil)
}

// ListTriggers fetches triggers filtered by f. Mirrors
// store.ListTriggers semantics; filters are encoded as query params.
// Used by `sparkwing triggers list`.
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

// GetTrigger fetches a single trigger's full row. Used by
// fleet-worker's spawned child process to adopt an already-claimed
// trigger without re-claiming. Returns store.ErrNotFound on 404.
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

// CancelRun records an operator cancellation request. Idempotent;
// returns nil if the request was recorded (including for an
// already-cancelling run). ErrNotFound when the run doesn't exist.
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

// DeleteRun removes a run (and its nodes/events/trigger row) from
// the controller's state. Idempotent: missing ids return nil so
// `jobs prune` can run repeatedly. 5xx is surfaced as an error.
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
// queue is empty so the worker can back off without treating that as
// an error.
//
// Back-compat shim for single-repo callers; cross-repo workers should
// use ClaimTriggerFor so the controller filters the queue to names
// this worker can actually run.
func (c *Client) ClaimTrigger(ctx context.Context) (*store.Trigger, error) {
	return c.ClaimTriggerFor(ctx, nil, nil)
}

// ClaimTriggerFor is ClaimTrigger with optional pipeline and source
// subset filters. Pass the worker's known pipeline names (typically
// from sparkwing.Registered()) and/or the trigger_source values it
// handles; the controller returns only triggers that match both lists.
// Empty / nil on either axis means "accept any" for that axis.
//
// RUN-014a: trigger_sources lets workers declare their origin handling
// at claim time (e.g. ["github"] for the warm-runner trigger loop,
// ["manual","schedule"] for in-cluster workers) so the controller
// filters at claim rather than workers claiming and silently dropping.
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
// `pipeline`. Returns "" + nil error when no match. REG-018: pod-side
// pipelineAwaiter calls this on its parent's RetryOf to chain the new
// child to the prior spawn, when present.
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
// spawning a new pipeline run from inside another. Thin wrapper over
// CreateTrigger that constructs the wire TriggerRequest from the
// backend-neutral request struct. Returns the assigned run id or a
// wrapped error; "cycle" messages from the controller pass through
// verbatim so AwaitPipelineJob can surface them.
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
	// Cross-repo await: caller declares which repo the spawned
	// pipeline lives in. Without this the controller would inherit
	// the parent's repo + SHA, which silently builds the wrong code
	// when the awaited pipeline is registered in a different repo.
	if repo != "" {
		req.Git.Repo = repo
		req.Git.Branch = branch
		// Derive owner/repo for the dashboard. Trim once.
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

// --- Cross-pipeline refs (FOLLOWUPS #8a) ---

// GetLatestRun returns the newest run of pipeline whose status is in
// statuses (default "success" when nil) and whose finished_at is
// within maxAge (0 disables the bound). ErrNotFound when nothing
// matches -- treat that as a signal to fail the consuming node with
// a clear "no matching upstream" error.
//
// The SDK's PipelineRef[T].Get lives downstream of this call: it
// fetches the run, then follows up with GetNodeOutput for the
// targeted node id. Two round trips keeps the endpoint surface
// focused (run lookup vs output fetch) and reuses GetNodeOutput's
// existing auth on the wire.
//
// Method name mirrors orchestrator.StateBackend so the client is a
// drop-in resolver backend for cluster-mode pod runners.
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

// GetNode fetches one node. K8sRunner calls this after a Job pod
// terminates to read the outcome/output.
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
// ErrNotFound if the node doesn't exist. 409 is returned if the node
// exists but hasn't finished; callers (the HTTP ref resolver) should
// only call this for nodes whose dependency-completion signal has
// already fired.
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
// holderID. Returns (nil, nil) when the queue is empty so the pool's
// poll loop can back off without treating that as an error. Mirrors
// ClaimTrigger's shape.
//
// labels are the runner's advertised labels; the controller filters
// candidate nodes so a returned node's needs_labels is a subset of
// labels (AND semantics). Pass nil / empty to advertise no labels.
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
// Idempotent; the first call wins (stable FIFO ordering across retries).
func (c *Client) MarkNodeReady(ctx context.Context, runID, nodeID string) error {
	path := fmt.Sprintf("/api/v1/runs/%s/nodes/%s/mark-ready",
		url.PathEscape(runID), url.PathEscape(nodeID))
	return c.post(ctx, path, nil, http.StatusNoContent, nil)
}

// RevokeNodeReady atomically clears ready_at IFF the node is not
// currently claimed. Returns true if the revoke succeeded and the
// caller can proceed with a fallback path (K8sRunner). False means
// a warm runner claimed the node in the meantime -- let it finish.
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
// when the pool runner isn't the current claim holder (someone else
// took over, or the reaper released it).
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

// --- Debug pauses (REG-013) ---

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

// ListEventsAfter pulls events for a run with seq > afterSeq from
// the controller. Mirrors Store.ListEventsAfter; the web dashboard
// calls this in cluster mode so its SSE handler stays store-agnostic.
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

// --- Approvals (approval-gate primitive) ---

// CreateApproval requests a human decision on a gated node. Mirrors
// store.CreateApproval: the controller inserts an approvals row and
// flips the node's status to approval_pending atomically.
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
// ErrNotFound when the row doesn't exist -- the orchestrator's waiter
// uses that as a signal to abort its poll.
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

// ResolveApproval writes a resolution onto a pending row. approver is
// populated from the authenticated principal server-side; the value
// passed here is only respected for orchestrator-written timeouts and
// tests. 409 maps to store.ErrLockHeld (already resolved).
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
// Backs the top-nav dropdown and `sparkwing approvals list`.
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
// If out is non-nil, the response body is JSON-decoded into it.
// body may be nil (sends a bare POST).
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

// postRaw sends an octet-stream POST (used for plan snapshot bytes).
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
// when possible so callers see the server's reason, not "unexpected
// status 400".
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
