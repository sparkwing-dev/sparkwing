package controller

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/otelutil"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// LoopbackState is the run-state surface a [Loopback] serves. It is
// [storage.StateStore] plus the three wrapper-shaped methods the
// orchestrator's own state interface adds, so every state backend the
// orchestrator can assemble -- the SQLite adapter, the object-store
// adapter, the local mirror -- satisfies it without a shim of its own.
//
// Reads and writes a node process can make but not every backend can
// answer (triggers, the unredacted execution read) are optional
// interfaces asserted per request rather than methods here: requiring
// them would exclude the backends this type exists to serve.
type LoopbackState interface {
	storage.StateStore

	// AppendEvent mirrors store.AppendEvent without the sequence
	// number, which no state backend outside SQLite assigns.
	AppendEvent(ctx context.Context, runID, nodeID, kind string, payload []byte) error

	// GetNodeOutput returns a finished node's raw JSON output.
	GetNodeOutput(ctx context.Context, runID, nodeID string) ([]byte, error)

	// EnqueueTrigger spawns a new trigger; cycles are rejected with a
	// wrapped error mentioning "cycle".
	EnqueueTrigger(ctx context.Context, pipeline string, args map[string]string, parentRunID, parentNodeID, retryOf, source, user, repo, branch string) (runID string, err error)
}

// LoopbackConcurrency is the slice of the concurrency backend a node
// process reads: a parent blocked in RunAndAwait asks whether its
// child is queued for plan admission so its own timeout can be paused.
// Nothing a node does acquires or releases a slot -- the dispatcher
// resolved that before it spawned the process.
type LoopbackConcurrency interface {
	State(ctx context.Context, key string) (*store.ConcurrencyState, error)
}

type loopbackTriggerReader interface {
	GetTrigger(ctx context.Context, triggerID string) (*store.Trigger, error)
}

type loopbackChildTriggerFinder interface {
	FindSpawnedChildTriggerID(ctx context.Context, parentRunID, parentNodeID, pipeline string) (string, error)
}

type loopbackTriggerEnqueuerWithEnv interface {
	EnqueueTriggerWithEnv(ctx context.Context, pipeline string, args map[string]string, parentRunID, parentNodeID, retryOf, source, user, repo, branch string, triggerEnv map[string]string) (string, error)
}

type loopbackExecutionRunGetter interface {
	GetRunForExecution(ctx context.Context, runID string) (*store.Run, error)
}

// Loopback is the controller a run mounts for its own node processes
// when its state does not live in a SQLite database.
//
// A node process reaches run state through a controller client and
// nothing else: that is the one execution model, and it is what lets a
// pod and a locally spawned node run the same code. A run whose state
// is per-run NDJSON on a bucket has no controller to point a child at,
// and cannot grow one -- the real [Server] is a database server, with
// a tokens table for auth, SQL claim and reaper sweeps, receipts, and
// trend aggregation behind most of its routes.
//
// So this serves the node-facing subset over the state surface the run
// already holds. It lives in this package rather than beside its
// caller because the wire is the contract: the request and response
// types, the status codes, the scope gate, and the route patterns are
// the ones [Server] registers, taken by reference rather than copied,
// so a client cannot tell which of the two answered it.
//
// One instance belongs to one run and dies with it, which is why the
// bearer is held in memory: there is no tokens table on a bucket, and
// a credential that cannot outlive the process holding it needs no
// revocation.
//
// "Belongs to one run" is enforced, not just intended: every mutating
// route is gated on runID, so the bearer sitting in a node's
// environment writes to that node's run and no other. See [ownRun].
type Loopback struct {
	state         LoopbackState
	concurrency   LoopbackConcurrency
	artifactStore storage.ArtifactStore
	runID         string
	token         string
	logger        *slog.Logger
}

// NewLoopback binds a loopback controller to one run's state surface.
// runID is the only run its callers may write to; token is the bearer
// its node processes present, and an empty one serves unauthenticated,
// which is only ever right in a test.
//
// An empty runID fails closed: no path value matches it, so every
// mutating route answers not-found and the loopback is inert for
// writes.
func NewLoopback(state LoopbackState, runID, token string, logger *slog.Logger) *Loopback {
	if logger == nil {
		logger = slog.Default()
	}
	return &Loopback{state: state, runID: runID, token: token, logger: logger}
}

// WithConcurrency binds the run's concurrency backend so a node
// blocked on a child run can read plan-admission state. Without it
// that route reports the key as undeclared, which reads as "not
// queued" -- the answer for a run with no plan concurrency at all.
func (l *Loopback) WithConcurrency(c LoopbackConcurrency) *Loopback {
	l.concurrency = c
	return l
}

// WithArtifactStore exposes /api/v1/artifacts/{key}, matching what the
// SQLite loopback wires so a node whose cache surface resolves to this
// controller can stage inputs through it.
func (l *Loopback) WithArtifactStore(a storage.ArtifactStore) *Loopback {
	l.artifactStore = a
	return l
}

// Handler returns the HTTP router. The route patterns and their scope
// gates mirror [Server.Handler] exactly; a request this router does not
// recognize is one a node process never makes.
//
// Mutating routes carrying a run id are additionally wrapped in
// [Loopback.ownRun]. Reads are not: a cross-pipeline ref, a
// RunAndAwait poll, and the retry-lineage lookup all read runs that are
// legitimately not this one.
func (l *Loopback) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.Handle("POST /api/v1/runs", requireScope(ScopeAdmin, http.HandlerFunc(l.handleCreateRun)))
	mux.Handle("GET /api/v1/runs/{id}", requireScope(ScopeRunsRead, http.HandlerFunc(l.handleGetRun)))
	mux.Handle("POST /api/v1/runs/{id}/finish", requireScope(ScopeAdmin, l.ownRun(l.handleFinishRun)))
	mux.Handle("POST /api/v1/runs/{id}/plan", requireScope(ScopeAdmin, l.ownRun(l.handleUpdatePlanSnapshot)))
	mux.Handle("POST /api/v1/runs/{id}/heartbeat", requireScope(ScopeNodesClaim, l.ownRun(l.handleTouchRunHeartbeat)))
	mux.Handle("POST /api/v1/runs/{id}/events", requireScope(ScopeAdmin, l.ownRun(l.handleAppendEvent)))
	mux.Handle("GET /api/v1/runs/{id}/steps", requireScope(ScopeRunsRead, http.HandlerFunc(l.handleListNodeSteps)))
	mux.Handle("GET /api/v1/pipelines/{name}/latest", requireScope(ScopeRunsRead, http.HandlerFunc(l.handlePipelineLatest)))

	mux.Handle("POST /api/v1/runs/{id}/nodes", requireScope(ScopeAdmin, l.ownRun(l.handleCreateNode)))
	mux.Handle("GET /api/v1/runs/{id}/nodes/{nodeID}", requireScope(ScopeNodesClaim, http.HandlerFunc(l.handleGetNode)))
	mux.Handle("GET /api/v1/runs/{id}/nodes/{nodeID}/output", requireScope(ScopeNodesClaim, http.HandlerFunc(l.handleGetNodeOutput)))
	mux.Handle("POST /api/v1/runs/{id}/nodes/{nodeID}/start", requireScope(ScopeAdmin, l.ownRun(l.handleStartNode)))
	mux.Handle("POST /api/v1/runs/{id}/nodes/{nodeID}/finish", requireScope(ScopeAdmin, l.ownRun(l.handleFinishNode)))
	mux.Handle("POST /api/v1/runs/{id}/nodes/{nodeID}/deps", requireScope(ScopeAdmin, l.ownRun(l.handleUpdateNodeDeps)))
	mux.Handle("POST /api/v1/runs/{id}/nodes/{nodeID}/status", requireScope(ScopeAdmin, l.ownRun(l.handleSetNodeStatus)))
	mux.Handle("POST /api/v1/runs/{id}/nodes/{nodeID}/activity", requireScope(ScopeNodesClaim, l.ownRun(l.handleUpdateNodeActivity)))
	mux.Handle("POST /api/v1/runs/{id}/nodes/{nodeID}/touch", requireScope(ScopeNodesClaim, l.ownRun(l.handleTouchNodeHeartbeat)))
	mux.Handle("POST /api/v1/runs/{id}/nodes/{nodeID}/annotations", requireScope(ScopeNodesClaim, l.ownRun(l.handleAppendNodeAnnotation)))
	mux.Handle("POST /api/v1/runs/{id}/nodes/{nodeID}/summary", requireScope(ScopeNodesClaim, l.ownRun(l.handleSetNodeSummary)))
	mux.Handle("POST /api/v1/runs/{id}/nodes/{nodeID}/artifact-manifest", requireScope(ScopeNodesClaim, l.ownRun(l.handleSetNodeArtifactManifest)))
	mux.Handle("POST /api/v1/runs/{id}/nodes/{nodeID}/metrics", requireScope(ScopeNodesClaim, l.ownRun(l.handleAddNodeMetric)))
	mux.Handle("POST /api/v1/runs/{id}/nodes/{nodeID}/dispatch", requireScope(ScopeNodesClaim, l.ownRun(l.handleWriteNodeDispatch)))
	mux.Handle("GET /api/v1/runs/{id}/nodes/{nodeID}/dispatch", requireScope(ScopeRunsRead, http.HandlerFunc(l.handleGetNodeDispatch)))
	mux.Handle("GET /api/v1/runs/{id}/nodes/{nodeID}/dispatches", requireScope(ScopeRunsRead, http.HandlerFunc(l.handleListNodeDispatches)))

	mux.Handle("POST /api/v1/runs/{id}/nodes/{nodeID}/steps/start", requireScope(ScopeNodesClaim, l.ownRun(l.handleStartNodeStep)))
	mux.Handle("POST /api/v1/runs/{id}/nodes/{nodeID}/steps/finish", requireScope(ScopeNodesClaim, l.ownRun(l.handleFinishNodeStep)))
	mux.Handle("POST /api/v1/runs/{id}/nodes/{nodeID}/steps/skip", requireScope(ScopeNodesClaim, l.ownRun(l.handleSkipNodeStep)))
	mux.Handle("POST /api/v1/runs/{id}/nodes/{nodeID}/steps/annotations", requireScope(ScopeNodesClaim, l.ownRun(l.handleAppendStepAnnotation)))
	mux.Handle("POST /api/v1/runs/{id}/nodes/{nodeID}/steps/summary", requireScope(ScopeNodesClaim, l.ownRun(l.handleSetStepSummary)))

	mux.Handle("POST /api/v1/runs/{id}/debug-pauses", requireScope(ScopeAdmin, l.ownRun(l.handleCreateDebugPause)))
	mux.Handle("GET /api/v1/runs/{id}/debug-pauses", requireScope(ScopeRunsRead, http.HandlerFunc(l.handleListDebugPauses)))
	mux.Handle("GET /api/v1/runs/{id}/paused", requireScope(ScopeRunsRead, http.HandlerFunc(l.handleListDebugPauses)))
	mux.Handle("GET /api/v1/runs/{id}/nodes/{nodeID}/debug-pause", requireScope(ScopeRunsRead, http.HandlerFunc(l.handleGetActiveDebugPause)))
	mux.Handle("POST /api/v1/runs/{id}/nodes/{nodeID}/release", requireScope(ScopeRunsWrite, l.ownRun(l.handleReleaseDebugPause)))

	mux.Handle("POST /api/v1/runs/{id}/approvals/{nodeID}/request", requireScope(ScopeAdmin, l.ownRun(l.handleRequestApproval)))
	mux.Handle("POST /api/v1/runs/{id}/approvals/{nodeID}", requireScope(ScopeApprovalsWrite, l.ownRun(l.handleResolveApproval)))
	mux.Handle("GET /api/v1/runs/{id}/approvals/{nodeID}", requireScope(ScopeRunsRead, http.HandlerFunc(l.handleGetApproval)))

	mux.Handle("GET /api/v1/approvals/pending", requireScope(ScopeRunsRead, http.HandlerFunc(l.handleListPendingApprovals)))

	mux.Handle("POST /api/v1/triggers", requireScope(ScopeRunsWrite, http.HandlerFunc(l.handleTrigger)))
	// hack: static segment prevents {id} from consuming "spawned-child" as a trigger ID.
	mux.Handle("GET /api/v1/triggers/spawned-child", requireScope(ScopeTriggersRead, http.HandlerFunc(l.handleFindSpawnedChildTrigger)))
	mux.Handle("GET /api/v1/triggers/{id}", requireScope(ScopeTriggersRead, http.HandlerFunc(l.handleGetTrigger)))

	mux.Handle("GET /api/v1/concurrency/{key}/state", requireScope(ScopeRunsRead, http.HandlerFunc(l.handleConcurrencyState)))

	if l.artifactStore != nil {
		mux.Handle("GET /api/v1/artifacts/{key}", requireScope(ScopeRunsRead, http.HandlerFunc(l.handleArtifactGet)))
	}

	router := http.NewServeMux()
	router.HandleFunc("GET /api/v1/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	router.Handle("/", l.authenticate(mux))
	// safety: preserve the server wrapper order while the Warn-level loopback
	// logger suppresses per-request Info lines from node state writes.
	return otelutil.WrapHandler("sparkwing-controller", withRequestLog(router, l.logger))
}

func (l *Loopback) authenticate(next http.Handler) http.Handler {
	if l.token == "" {
		return next
	}
	principal := &Principal{Name: "loopback", Kind: "service", Scopes: []string{ScopeAdmin}}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := extractBearer(r)
		if err != nil {
			writeAuthError(w, http.StatusUnauthorized, authErrorBody{
				Code:    "unauthenticated",
				Message: err.Error(),
			})
			return
		}
		if subtle.ConstantTimeCompare([]byte(raw), []byte(l.token)) != 1 {
			writeAuthError(w, http.StatusUnauthorized, authErrorBody{
				Code:    "unauthenticated",
				Message: "token not recognized",
			})
			return
		}
		p := *principal
		p.Authed = time.Now().UTC()
		next.ServeHTTP(w, r.WithContext(contextWithPrincipal(r.Context(), &p)))
	})
}

func (l *Loopback) ownRun(h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id := r.PathValue("id"); id != l.runID {
			writeError(w, http.StatusNotFound,
				fmt.Errorf("run %s: %w on the loopback controller for run %s", id, store.ErrNotFound, l.runID))
			return
		}
		h(w, r)
	})
}

func (l *Loopback) handleCreateRun(w http.ResponseWriter, r *http.Request) {
	var body store.Run
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	// safety: this route carries the run id in the body, so [Loopback.ownRun]
	// cannot gate it; the check is the same one, done here.
	if body.ID != l.runID {
		writeError(w, http.StatusNotFound,
			fmt.Errorf("run %s: %w on the loopback controller for run %s", body.ID, store.ErrNotFound, l.runID))
		return
	}
	if err := l.state.CreateRun(r.Context(), body); err != nil {
		writeStateError(w, err)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func (l *Loopback) handleGetRun(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	run, err := l.getRun(r, runID)
	if err != nil {
		writeStateError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, runForResponse(r, run, loopbackSecretValuesAllowed))
}

func (l *Loopback) getRun(r *http.Request, runID string) (*store.Run, error) {
	if includeHas(r.URL.Query().Get("include"), store.IncludeSecretValues) {
		if ex, ok := l.state.(loopbackExecutionRunGetter); ok {
			return ex.GetRunForExecution(r.Context(), runID)
		}
	}
	return l.state.GetRun(r.Context(), runID)
}

func (l *Loopback) handleFinishRun(w http.ResponseWriter, r *http.Request) {
	var body finishRunReq
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := l.state.FinishRun(r.Context(), r.PathValue("id"), body.Status, body.Error); err != nil {
		writeStateError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (l *Loopback) handleUpdatePlanSnapshot(w http.ResponseWriter, r *http.Request) {
	defer func() { _ = r.Body.Close() }()
	snapshot, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := l.state.UpdatePlanSnapshot(r.Context(), r.PathValue("id"), snapshot); err != nil {
		writeStateError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (l *Loopback) handleTouchRunHeartbeat(w http.ResponseWriter, r *http.Request) {
	if err := l.state.TouchRunHeartbeat(r.Context(), r.PathValue("id")); err != nil {
		writeStateError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (l *Loopback) handleAppendEvent(w http.ResponseWriter, r *http.Request) {
	var body appendEventReq
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if body.Kind == "" {
		writeError(w, http.StatusBadRequest, errors.New("kind is required"))
		return
	}
	if err := l.state.AppendEvent(r.Context(), r.PathValue("id"), body.NodeID, body.Kind, body.Payload); err != nil {
		writeStateError(w, err)
		return
	}
	// safety: the sequence number is SQLite's; a backend that assigns none
	// answers zero rather than inventing one, and no client reads it.
	writeJSON(w, http.StatusOK, appendEventResp{})
}

func (l *Loopback) handlePipelineLatest(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, errors.New("pipeline name required"))
		return
	}
	q := r.URL.Query()
	statuses := splitCSV(q.Get("status"))
	if len(statuses) == 0 {
		statuses = []string{"success"}
	}
	var maxAge time.Duration
	if v := q.Get("max_age"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("max_age: %w", err))
			return
		}
		if d < 0 {
			writeError(w, http.StatusBadRequest, errors.New("max_age must be >= 0"))
			return
		}
		maxAge = d
	}
	run, err := l.state.GetLatestRun(r.Context(), name, statuses, maxAge)
	if err != nil {
		writeStateError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, store.RedactedRun(run))
}

func (l *Loopback) handleCreateNode(w http.ResponseWriter, r *http.Request) {
	var body store.Node
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	body.RunID = r.PathValue("id")
	if body.NodeID == "" || body.Status == "" {
		writeError(w, http.StatusBadRequest, errors.New("node id and status are required"))
		return
	}
	if err := l.state.CreateNode(r.Context(), body); err != nil {
		writeStateError(w, err)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func (l *Loopback) handleGetNode(w http.ResponseWriter, r *http.Request) {
	n, err := l.state.GetNode(r.Context(), r.PathValue("id"), r.PathValue("nodeID"))
	if err != nil {
		writeStateError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, n)
}

func (l *Loopback) handleGetNodeOutput(w http.ResponseWriter, r *http.Request) {
	runID, nodeID := r.PathValue("id"), r.PathValue("nodeID")
	n, err := l.state.GetNode(r.Context(), runID, nodeID)
	if err != nil {
		writeStateError(w, err)
		return
	}
	if n.Status != "done" {
		writeError(w, http.StatusConflict, fmt.Errorf("node %s/%s not finished (status=%s)", runID, nodeID, n.Status))
		return
	}
	out, err := l.state.GetNodeOutput(r.Context(), runID, nodeID)
	if err != nil {
		writeStateError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if len(out) > 0 {
		_, _ = w.Write(out)
	} else {
		_, _ = w.Write([]byte("null"))
	}
}

func (l *Loopback) handleStartNode(w http.ResponseWriter, r *http.Request) {
	if err := l.state.StartNode(r.Context(), r.PathValue("id"), r.PathValue("nodeID")); err != nil {
		writeStateError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (l *Loopback) handleFinishNode(w http.ResponseWriter, r *http.Request) {
	var body finishNodeReq
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if body.Outcome == "" {
		writeError(w, http.StatusBadRequest, errors.New("outcome is required"))
		return
	}
	if err := l.state.FinishNodeWithReason(r.Context(), r.PathValue("id"), r.PathValue("nodeID"),
		body.Outcome, body.Error, body.Output, body.FailureReason, body.ExitCode); err != nil {
		writeStateError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (l *Loopback) handleUpdateNodeDeps(w http.ResponseWriter, r *http.Request) {
	var body updateDepsReq
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := l.state.UpdateNodeDeps(r.Context(), r.PathValue("id"), r.PathValue("nodeID"), body.Deps); err != nil {
		writeStateError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (l *Loopback) handleSetNodeStatus(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Status string `json:"status"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if body.Status == "" {
		writeError(w, http.StatusBadRequest, errors.New("status is required"))
		return
	}
	if err := l.state.SetNodeStatus(r.Context(), r.PathValue("id"), r.PathValue("nodeID"), body.Status); err != nil {
		writeStateError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (l *Loopback) handleUpdateNodeActivity(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Detail string `json:"detail"`
	}
	_ = decodeJSON(r, &body)
	if err := l.state.UpdateNodeActivity(r.Context(), r.PathValue("id"), r.PathValue("nodeID"), body.Detail); err != nil {
		writeStateError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (l *Loopback) handleTouchNodeHeartbeat(w http.ResponseWriter, r *http.Request) {
	if err := l.state.TouchNodeHeartbeat(r.Context(), r.PathValue("id"), r.PathValue("nodeID")); err != nil {
		writeStateError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (l *Loopback) handleAppendNodeAnnotation(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Message string `json:"message"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := l.state.AppendNodeAnnotation(r.Context(), r.PathValue("id"), r.PathValue("nodeID"), body.Message); err != nil {
		writeStateError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (l *Loopback) handleSetNodeSummary(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Markdown string `json:"markdown"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := l.state.SetNodeSummary(r.Context(), r.PathValue("id"), r.PathValue("nodeID"), body.Markdown); err != nil {
		writeStateError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (l *Loopback) handleSetNodeArtifactManifest(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ManifestDigest string `json:"manifest_digest"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := l.state.SetNodeArtifactManifest(r.Context(), r.PathValue("id"), r.PathValue("nodeID"), body.ManifestDigest); err != nil {
		writeStateError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (l *Loopback) handleAddNodeMetric(w http.ResponseWriter, r *http.Request) {
	var body metricSample
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	ts := time.Now()
	if body.TS != "" {
		if parsed, err := time.Parse(time.RFC3339Nano, body.TS); err == nil {
			ts = parsed
		}
	}
	if err := l.state.AddNodeMetricSample(r.Context(), r.PathValue("id"), r.PathValue("nodeID"), store.MetricSample{
		TS:            ts,
		CPUMillicores: body.CPUMillicores,
		MemoryBytes:   body.MemoryBytes,
		CPUTime:       time.Duration(body.CPUTimeNanos),
	}); err != nil {
		writeStateError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (l *Loopback) handleWriteNodeDispatch(w http.ResponseWriter, r *http.Request) {
	var d store.NodeDispatch
	if err := decodeJSON(r, &d); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	d.RunID = r.PathValue("id")
	d.NodeID = r.PathValue("nodeID")
	if err := l.state.WriteNodeDispatch(r.Context(), d); err != nil {
		writeStateError(w, err)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func (l *Loopback) handleGetNodeDispatch(w http.ResponseWriter, r *http.Request) {
	seq := -1
	if v := r.URL.Query().Get("seq"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("invalid seq: %w", err))
			return
		}
		seq = n
	}
	d, err := l.state.GetNodeDispatch(r.Context(), r.PathValue("id"), r.PathValue("nodeID"), seq)
	if err != nil {
		writeStateError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dispatchForResponse(r, d))
}

func (l *Loopback) handleListNodeDispatches(w http.ResponseWriter, r *http.Request) {
	out, err := l.state.ListNodeDispatches(r.Context(), r.PathValue("id"), r.PathValue("nodeID"))
	if err != nil {
		writeStateError(w, err)
		return
	}
	if out == nil {
		out = []*store.NodeDispatch{}
	}
	writeJSON(w, http.StatusOK, dispatchesForResponse(r, out))
}

func (l *Loopback) handleStartNodeStep(w http.ResponseWriter, r *http.Request) {
	var body struct {
		StepID string `json:"step_id"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if body.StepID == "" {
		writeError(w, http.StatusBadRequest, errors.New("step_id is required"))
		return
	}
	if err := l.state.StartNodeStep(r.Context(), r.PathValue("id"), r.PathValue("nodeID"), body.StepID); err != nil {
		writeStateError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (l *Loopback) handleFinishNodeStep(w http.ResponseWriter, r *http.Request) {
	var body struct {
		StepID string `json:"step_id"`
		Status string `json:"status"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if body.StepID == "" {
		writeError(w, http.StatusBadRequest, errors.New("step_id is required"))
		return
	}
	if body.Status != store.StepPassed && body.Status != store.StepFailed {
		writeError(w, http.StatusBadRequest, errors.New("status must be passed or failed"))
		return
	}
	if err := l.state.FinishNodeStep(r.Context(), r.PathValue("id"), r.PathValue("nodeID"), body.StepID, body.Status); err != nil {
		writeStateError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (l *Loopback) handleSkipNodeStep(w http.ResponseWriter, r *http.Request) {
	var body struct {
		StepID string `json:"step_id"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if body.StepID == "" {
		writeError(w, http.StatusBadRequest, errors.New("step_id is required"))
		return
	}
	if err := l.state.SkipNodeStep(r.Context(), r.PathValue("id"), r.PathValue("nodeID"), body.StepID); err != nil {
		writeStateError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (l *Loopback) handleAppendStepAnnotation(w http.ResponseWriter, r *http.Request) {
	var body struct {
		StepID  string `json:"step_id"`
		Message string `json:"message"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if body.StepID == "" {
		writeError(w, http.StatusBadRequest, errors.New("step_id is required"))
		return
	}
	if err := l.state.AppendStepAnnotation(r.Context(), r.PathValue("id"), r.PathValue("nodeID"), body.StepID, body.Message); err != nil {
		writeStateError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (l *Loopback) handleSetStepSummary(w http.ResponseWriter, r *http.Request) {
	var body struct {
		StepID   string `json:"step_id"`
		Markdown string `json:"markdown"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if body.StepID == "" {
		writeError(w, http.StatusBadRequest, errors.New("step_id is required"))
		return
	}
	if err := l.state.SetStepSummary(r.Context(), r.PathValue("id"), r.PathValue("nodeID"), body.StepID, body.Markdown); err != nil {
		writeStateError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (l *Loopback) handleListNodeSteps(w http.ResponseWriter, r *http.Request) {
	steps, err := l.state.ListNodeSteps(r.Context(), r.PathValue("id"))
	if err != nil {
		writeStateError(w, err)
		return
	}
	if steps == nil {
		steps = []*store.NodeStep{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"steps": steps})
}

func (l *Loopback) handleCreateDebugPause(w http.ResponseWriter, r *http.Request) {
	var body store.DebugPause
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	body.RunID = r.PathValue("id")
	if body.NodeID == "" || body.Reason == "" {
		writeError(w, http.StatusBadRequest, errors.New("node_id and reason are required"))
		return
	}
	if err := l.state.CreateDebugPause(r.Context(), body); err != nil {
		writeStateError(w, err)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func (l *Loopback) handleListDebugPauses(w http.ResponseWriter, r *http.Request) {
	pauses, err := l.state.ListDebugPauses(r.Context(), r.PathValue("id"))
	if err != nil {
		writeStateError(w, err)
		return
	}
	if pauses == nil {
		pauses = []*store.DebugPause{}
	}
	writeJSON(w, http.StatusOK, pauses)
}

func (l *Loopback) handleGetActiveDebugPause(w http.ResponseWriter, r *http.Request) {
	p, err := l.state.GetActiveDebugPause(r.Context(), r.PathValue("id"), r.PathValue("nodeID"))
	if err != nil {
		writeStateError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (l *Loopback) handleReleaseDebugPause(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ReleaseKind string `json:"release_kind"`
	}
	_ = decodeJSON(r, &body)
	if body.ReleaseKind == "" {
		body.ReleaseKind = store.PauseReleaseManual
	}
	if err := l.state.ReleaseDebugPause(r.Context(), r.PathValue("id"), r.PathValue("nodeID"),
		auditPrincipal(r), body.ReleaseKind); err != nil {
		writeStateError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (l *Loopback) handleRequestApproval(w http.ResponseWriter, r *http.Request) {
	runID, nodeID := r.PathValue("id"), r.PathValue("nodeID")
	var body requestApprovalReq
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if runID == "" || nodeID == "" {
		writeError(w, http.StatusBadRequest, errors.New("run id and node id required"))
		return
	}
	onTimeout := body.OnTimeout
	switch onTimeout {
	case "", store.ApprovalOnTimeoutFail, store.ApprovalOnTimeoutDeny, store.ApprovalOnTimeoutApprove:
	default:
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid on_timeout: %q", onTimeout))
		return
	}
	if onTimeout == "" {
		onTimeout = store.ApprovalOnTimeoutFail
	}
	if err := l.state.CreateApproval(r.Context(), store.Approval{
		RunID:       runID,
		NodeID:      nodeID,
		RequestedAt: time.Now(),
		Message:     body.Message,
		TimeoutMS:   body.TimeoutMS,
		OnTimeout:   onTimeout,
	}); err != nil {
		writeStateError(w, err)
		return
	}
	payload, _ := json.Marshal(map[string]any{
		"message":    body.Message,
		"timeout_ms": body.TimeoutMS,
	})
	_ = l.state.AppendEvent(r.Context(), runID, nodeID, "approval_requested", payload)
	w.WriteHeader(http.StatusCreated)
}

func (l *Loopback) handleResolveApproval(w http.ResponseWriter, r *http.Request) {
	runID, nodeID := r.PathValue("id"), r.PathValue("nodeID")
	var body resolveApprovalReq
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	switch body.Resolution {
	case store.ApprovalResolutionApproved,
		store.ApprovalResolutionDenied,
		store.ApprovalResolutionTimedOut:
	default:
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid resolution: %q", body.Resolution))
		return
	}
	// safety: the loopback's principal is the run itself, not a person, so
	// the body's approver is the only identity there is -- the reverse of
	// the real controller, where a named principal must beat a spoofable
	// body field.
	approver := body.Approver
	if approver == "" {
		approver = "unknown"
	}
	got, err := l.state.ResolveApproval(r.Context(), runID, nodeID, body.Resolution, approver, body.Comment)
	if err != nil {
		if errors.Is(err, store.ErrLockHeld) {
			writeError(w, http.StatusConflict, errors.New("approval already resolved"))
			return
		}
		writeStateError(w, err)
		return
	}
	payload, _ := json.Marshal(map[string]any{
		"resolution": got.Resolution,
		"approver":   got.Approver,
		"comment":    got.Comment,
	})
	_ = l.state.AppendEvent(r.Context(), runID, nodeID, "approval_resolved", payload)
	writeJSON(w, http.StatusOK, got)
}

func (l *Loopback) handleGetApproval(w http.ResponseWriter, r *http.Request) {
	a, err := l.state.GetApproval(r.Context(), r.PathValue("id"), r.PathValue("nodeID"))
	if err != nil {
		writeStateError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, a)
}

func (l *Loopback) handleListPendingApprovals(w http.ResponseWriter, r *http.Request) {
	rows, err := l.state.ListPendingApprovals(r.Context())
	if err != nil {
		writeStateError(w, err)
		return
	}
	if rows == nil {
		rows = []*store.Approval{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"approvals": rows})
}

func (l *Loopback) handleTrigger(w http.ResponseWriter, r *http.Request) {
	var body triggerReq
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if body.Pipeline == "" {
		writeError(w, http.StatusBadRequest, errors.New("pipeline is required"))
		return
	}
	if body.Trigger.Source == "" {
		writeError(w, http.StatusBadRequest, errors.New("trigger.source is required"))
		return
	}
	// safety: the spawning run is always the caller's own -- the awaiter
	// passes its own id -- so a parent naming anything else is a node
	// forging lineage onto a run it does not own.
	if body.ParentRunID != "" && body.ParentRunID != l.runID {
		writeError(w, http.StatusNotFound,
			fmt.Errorf("run %s: %w on the loopback controller for run %s", body.ParentRunID, store.ErrNotFound, l.runID))
		return
	}
	env := sanitizeTriggerEnv(body.Trigger.Env)

	var runID string
	var err error
	if withEnv, ok := l.state.(loopbackTriggerEnqueuerWithEnv); ok {
		runID, err = withEnv.EnqueueTriggerWithEnv(r.Context(), body.Pipeline, body.Args,
			body.ParentRunID, body.ParentNodeID, body.RetryOf,
			body.Trigger.Source, body.Trigger.User, body.Git.Repo, body.Git.Branch, env)
	} else if len(env) > 0 {
		writeError(w, http.StatusBadRequest, errors.New("state backend cannot persist trigger env"))
		return
	} else {
		runID, err = l.state.EnqueueTrigger(r.Context(), body.Pipeline, body.Args,
			body.ParentRunID, body.ParentNodeID, body.RetryOf,
			body.Trigger.Source, body.Trigger.User, body.Git.Repo, body.Git.Branch)
	}
	if err != nil {
		writeError(w, triggerErrorStatus(err), err)
		return
	}
	writeJSON(w, http.StatusAccepted, triggerResp{RunID: runID, Status: "dispatched"})
}

func triggerErrorStatus(err error) int {
	switch {
	case errors.Is(err, storage.ErrNotSupported):
		return http.StatusNotImplemented
	// safety: both enqueues report a rejected cycle as a wrapped message
	// rather than a sentinel, and the await path branches on the word.
	case strings.Contains(err.Error(), "cycle"):
		return http.StatusConflict
	default:
		return http.StatusInternalServerError
	}
}

func (l *Loopback) handleGetTrigger(w http.ResponseWriter, r *http.Request) {
	// safety: mirrored state has no trigger row, and the node path already
	// treats not-found as no trigger.
	reader, ok := l.state.(loopbackTriggerReader)
	if !ok {
		writeError(w, http.StatusNotFound, store.ErrNotFound)
		return
	}
	tg, err := reader.GetTrigger(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, storage.ErrNotSupported) {
			writeError(w, http.StatusNotFound, store.ErrNotFound)
			return
		}
		writeStateError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, tg)
}

func (l *Loopback) handleFindSpawnedChildTrigger(w http.ResponseWriter, r *http.Request) {
	finder, ok := l.state.(loopbackChildTriggerFinder)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]string{"run_id": ""})
		return
	}
	q := r.URL.Query()
	id, err := finder.FindSpawnedChildTriggerID(r.Context(),
		q.Get("parent_run_id"), q.Get("parent_node_id"), q.Get("pipeline"))
	if err != nil {
		writeStateError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"run_id": id})
}

func (l *Loopback) handleConcurrencyState(w http.ResponseWriter, r *http.Request) {
	if l.concurrency == nil {
		writeError(w, http.StatusNotFound, store.ErrNotFound)
		return
	}
	st, err := l.concurrency.State(r.Context(), r.PathValue("key"))
	if err != nil {
		writeStateError(w, err)
		return
	}
	resp := stateResp{
		Key: st.Key, Capacity: st.Capacity,
		EffectiveCapacity: st.EffectiveCapacity, UsedCost: st.UsedCost,
	}
	for _, h := range st.Holders {
		resp.Holders = append(resp.Holders, holderResp(h))
	}
	for _, wt := range st.Waiters {
		var ct string
		if wt.CancelTimeout > 0 {
			ct = wt.CancelTimeout.String()
		}
		resp.Waiters = append(resp.Waiters, stateWaiterResp{
			RunID: wt.RunID, NodeID: wt.NodeID, ArrivedAt: wt.ArrivedAt,
			Policy: wt.Policy, CacheKeyHash: wt.CacheKeyHash,
			LeaderRunID: wt.LeaderRunID, LeaderNodeID: wt.LeaderNodeID,
			CancelTimeout: ct, Cost: wt.Cost, Position: wt.Position,
		})
	}
	writeJSON(w, http.StatusOK, resp)
}

func (l *Loopback) handleArtifactGet(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if key == "" {
		http.Error(w, "missing key", http.StatusBadRequest)
		return
	}
	if !safeArtifactKey(key) {
		http.Error(w, "invalid key", http.StatusBadRequest)
		return
	}
	rc, err := l.artifactStore.Get(r.Context(), key)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer func() { _ = rc.Close() }()
	w.Header().Set("Content-Type", "application/octet-stream")
	_, _ = io.Copy(w, rc)
}

func writeStateError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrSecretInputHash):
		writeError(w, http.StatusBadRequest, err)
	case errors.Is(err, store.ErrNotFound), errors.Is(err, storage.ErrNotFound):
		writeError(w, http.StatusNotFound, err)
	case errors.Is(err, storage.ErrNotSupported):
		writeError(w, http.StatusNotImplemented, err)
	default:
		writeError(w, http.StatusInternalServerError, err)
	}
}
