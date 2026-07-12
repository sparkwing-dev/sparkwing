// Package s3state implements storage.StateStore over an object store
// (S3, GCS, Azure Blob, or any backend exposing the
// storage.ArtifactStore interface). It is the data-plane storage for
// Mode 2 ("S3-only shared") in DESIGN-shared-state.md: runners
// serialize per-run state to runs/<runID>/state.ndjson with no
// database and no controller.
//
// Cross-runner coordination (dispatch claims, debug pauses, approvals,
// trigger enqueue, child-trigger lookup) is implemented in cas.go as
// discrete object-store records mutated through
// storage.ConditionalWriter compare-and-swap. When the artifact store
// does not support conditional writes, those methods return the
// package-level ErrNotSupported and callers fall back to Mode 3
// (Postgres) or Mode 4 (hosted controller).
//
// The on-disk format is line-delimited JSON envelopes of the shape
// {"kind":"...","data":{...}}. Reads parse the current blob and
// replay envelopes in order to reconstruct the in-memory snapshot;
// writes append envelopes to an in-memory log and re-PUT the whole
// blob on the next flush (object stores do not support portable
// append semantics).
package s3state

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// DefaultFlushInterval bounds how stale the on-disk state.ndjson can
// be relative to in-memory writes. Tuned to give the dashboard a
// sub-second freshness floor without bursting object-store PUTs.
const DefaultFlushInterval = 500 * time.Millisecond

// DefaultBufferThreshold triggers an early flush when pending
// envelopes since the last flush exceed this many bytes.
const DefaultBufferThreshold = 16 * 1024

// ErrNotSupported is the sentinel returned by methods that require
// cross-runner coordination Mode 2 deliberately omits. Callers check
// with errors.Is.
var ErrNotSupported = errors.New("s3state: operation not supported in S3-only mode")

// Envelope kinds. Exported so the dashboard's S3Backend reader can
// match against the same constants.
const (
	KindRun          = "run"
	KindNode         = "node"
	KindNodeStep     = "node_step"
	KindNodeStatus   = "node_status"
	KindMetricSample = "metric_sample"
	KindEvent        = "event"
)

// Backend serializes run records to runs/<runID>/state.ndjson in an
// object store. Safe for concurrent use by orchestrator goroutines on
// the same process.
type Backend struct {
	art           storage.ArtifactStore
	flushInterval time.Duration
	bufferLimit   int
	outbox        *Outbox

	mu   sync.Mutex
	runs map[string]*runState

	stopCh   chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
}

// Option tunes the backend at construction time.
type Option func(*Backend)

// WithFlushInterval overrides DefaultFlushInterval.
func WithFlushInterval(d time.Duration) Option {
	return func(b *Backend) {
		if d > 0 {
			b.flushInterval = d
		}
	}
}

// WithBufferThreshold overrides DefaultBufferThreshold.
func WithBufferThreshold(n int) Option {
	return func(b *Backend) {
		if n > 0 {
			b.bufferLimit = n
		}
	}
}

// WithOutbox attaches a local SQLite outbox that absorbs writes when
// the object store is unreachable, draining when connectivity
// returns. Pass nil (or omit) to disable.
func WithOutbox(o *Outbox) Option {
	return func(b *Backend) { b.outbox = o }
}

// New constructs a Backend over the given artifact store. Starts a
// background goroutine that periodically flushes dirty runs; call
// Close to stop it.
func New(art storage.ArtifactStore, opts ...Option) *Backend {
	b := &Backend{
		art:           art,
		flushInterval: DefaultFlushInterval,
		bufferLimit:   DefaultBufferThreshold,
		runs:          map[string]*runState{},
		stopCh:        make(chan struct{}),
	}
	for _, opt := range opts {
		opt(b)
	}
	b.wg.Add(1)
	go b.flushLoop()
	return b
}

var _ storage.StateStore = (*Backend)(nil)

// runState holds the in-memory log and derived snapshot for one run.
type runState struct {
	mu        sync.Mutex
	envelopes []envelope
	bufSize   int
	dirty     bool
	flushing  bool

	// Derived snapshot, kept up to date as envelopes are appended.
	run          *store.Run
	nodes        map[string]*store.Node
	steps        map[string]map[string]*store.NodeStep // nodeID -> stepID
	stepOrder    []stepKey                             // insertion order for ListNodeSteps
	events       []store.Event
	nextEventSeq int64
	metrics      map[string][]store.MetricSample
	loaded       bool // true once we've attempted a load-from-disk
}

type stepKey struct {
	nodeID string
	stepID string
}

type envelope struct {
	Kind string          `json:"kind"`
	Data json.RawMessage `json:"data"`
}

func newRunState() *runState {
	return &runState{
		nodes:        map[string]*store.Node{},
		steps:        map[string]map[string]*store.NodeStep{},
		nextEventSeq: 1,
		metrics:      map[string][]store.MetricSample{},
	}
}

// getRunState returns the entry for runID, lazily loading from disk
// on first access. If load=true and the run isn't already in memory,
// a Get + parse is attempted; storage.ErrNotFound is treated as a
// fresh run with an empty log.
func (b *Backend) getRunState(ctx context.Context, runID string, load bool) (*runState, error) {
	b.mu.Lock()
	rs, ok := b.runs[runID]
	if !ok {
		rs = newRunState()
		b.runs[runID] = rs
	}
	b.mu.Unlock()

	rs.mu.Lock()
	defer rs.mu.Unlock()
	if load && !rs.loaded {
		if err := b.loadLocked(ctx, runID, rs); err != nil {
			return nil, err
		}
		rs.loaded = true
	}
	return rs, nil
}

// loadLocked pulls the on-disk NDJSON for runID and replays it into
// rs. Called with rs.mu held.
func (b *Backend) loadLocked(ctx context.Context, runID string, rs *runState) error {
	rc, err := b.art.Get(ctx, stateKey(runID))
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil
		}
		return fmt.Errorf("s3state: load %s: %w", runID, err)
	}
	defer func() { _ = rc.Close() }()
	envs, err := parseEnvelopes(rc)
	if err != nil {
		return fmt.Errorf("s3state: parse %s: %w", runID, err)
	}
	for _, env := range envs {
		applyEnvelope(rs, env)
		rs.envelopes = append(rs.envelopes, env)
	}
	return nil
}

// appendEnvelope appends an envelope to the run's log, updates the
// derived snapshot, and marks the run dirty. If the pending buffer
// exceeds bufferLimit, an immediate flush is triggered synchronously
// (callers tolerate the latency in exchange for bounded memory).
func (b *Backend) appendEnvelope(ctx context.Context, runID string, env envelope) error {
	rs, err := b.getRunState(ctx, runID, true)
	if err != nil {
		return err
	}
	rs.mu.Lock()
	rs.envelopes = append(rs.envelopes, env)
	rs.bufSize += len(env.Data) + len(env.Kind) + 32
	rs.dirty = true
	applyEnvelope(rs, env)
	shouldFlush := rs.bufSize >= b.bufferLimit
	rs.mu.Unlock()
	if shouldFlush {
		return b.flushRun(ctx, runID)
	}
	return nil
}

// flushRun serializes the run's full envelope log and PUTs it.
// Idempotent; safe to call concurrently with appendEnvelope.
func (b *Backend) flushRun(ctx context.Context, runID string) error {
	b.mu.Lock()
	rs, ok := b.runs[runID]
	b.mu.Unlock()
	if !ok {
		return nil
	}
	rs.mu.Lock()
	if !rs.dirty || rs.flushing {
		rs.mu.Unlock()
		return nil
	}
	rs.flushing = true
	envs := make([]envelope, len(rs.envelopes))
	copy(envs, rs.envelopes)
	rs.mu.Unlock()

	body, err := encodeEnvelopes(envs)
	if err != nil {
		rs.mu.Lock()
		rs.flushing = false
		rs.mu.Unlock()
		return err
	}
	putErr := b.art.Put(ctx, stateKey(runID), bytes.NewReader(body))
	rs.mu.Lock()
	rs.flushing = false
	if putErr == nil {
		rs.dirty = false
		rs.bufSize = 0
	}
	rs.mu.Unlock()

	if putErr != nil && b.outbox != nil && isTransient(putErr) {
		return b.outbox.Stage(ctx, OutboxKindState, stateKey(runID), body)
	}
	return putErr
}

func (b *Backend) flushLoop() {
	defer b.wg.Done()
	t := time.NewTicker(b.flushInterval)
	defer t.Stop()
	for {
		select {
		case <-b.stopCh:
			return
		case <-t.C:
			b.flushAllDirty()
		}
	}
}

func (b *Backend) flushAllDirty() {
	b.mu.Lock()
	ids := make([]string, 0, len(b.runs))
	for id, rs := range b.runs {
		rs.mu.Lock()
		if rs.dirty && !rs.flushing {
			ids = append(ids, id)
		}
		rs.mu.Unlock()
	}
	b.mu.Unlock()
	for _, id := range ids {
		_ = b.flushRun(context.Background(), id)
	}
}

// Close stops the background flush goroutine and synchronously
// flushes any runs still marked dirty.
func (b *Backend) Close() error {
	b.stopOnce.Do(func() { close(b.stopCh) })
	b.wg.Wait()
	b.flushAllDirty()
	if b.outbox != nil {
		_ = b.outbox.Close()
	}
	return nil
}

// stateKey is the object-store key for one run's state log. Matches
// the path orchestrator.DumpRunState writes today so a Mode 2 reader
// can also pick up legacy completed-run dumps.
func stateKey(runID string) string { return "runs/" + runID + "/state.ndjson" }

// encodeEnvelopes serializes envs as NDJSON. One line per envelope.
func encodeEnvelopes(envs []envelope) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, env := range envs {
		if err := enc.Encode(env); err != nil {
			return nil, err
		}
	}
	return buf.Bytes(), nil
}

// parseEnvelopes scans NDJSON from r and returns the envelope log.
// Blank lines are tolerated. Buffer size is sized to accommodate
// planSnapshot blobs inlined into the run record.
func parseEnvelopes(r io.Reader) ([]envelope, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 1<<20), 16<<20)
	var out []envelope
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var env envelope
		if err := json.Unmarshal(line, &env); err != nil {
			return nil, err
		}
		out = append(out, env)
	}
	return out, scanner.Err()
}

// applyEnvelope replays one envelope into the derived snapshot. Last
// write wins for status/summary fields; annotations and metrics
// accumulate. Called with rs.mu held.
func applyEnvelope(rs *runState, env envelope) {
	switch env.Kind {
	case KindRun:
		var r store.Run
		if err := json.Unmarshal(env.Data, &r); err == nil {
			rs.run = &r
		}
	case KindNode:
		var n store.Node
		if err := json.Unmarshal(env.Data, &n); err == nil {
			rs.nodes[n.NodeID] = &n
		}
	case KindNodeStep:
		var s store.NodeStep
		if err := json.Unmarshal(env.Data, &s); err == nil {
			byNode, ok := rs.steps[s.NodeID]
			if !ok {
				byNode = map[string]*store.NodeStep{}
				rs.steps[s.NodeID] = byNode
			}
			if _, exists := byNode[s.StepID]; !exists {
				rs.stepOrder = append(rs.stepOrder, stepKey{s.NodeID, s.StepID})
			}
			byNode[s.StepID] = &s
		}
	case KindNodeStatus:
		var payload struct {
			NodeID       string `json:"node_id"`
			Status       string `json:"status"`
			StatusDetail string `json:"status_detail,omitempty"`
			LastHeartNS  int64  `json:"last_heartbeat,omitempty"`
		}
		if err := json.Unmarshal(env.Data, &payload); err == nil {
			n, ok := rs.nodes[payload.NodeID]
			if !ok {
				n = &store.Node{NodeID: payload.NodeID}
				rs.nodes[payload.NodeID] = n
			}
			if payload.Status != "" {
				n.Status = payload.Status
			}
			if payload.StatusDetail != "" {
				n.StatusDetail = payload.StatusDetail
			}
			if payload.LastHeartNS != 0 {
				t := time.Unix(0, payload.LastHeartNS)
				n.LastHeartbeat = &t
			}
		}
	case KindMetricSample:
		var payload struct {
			NodeID string             `json:"node_id"`
			Sample store.MetricSample `json:"sample"`
		}
		if err := json.Unmarshal(env.Data, &payload); err == nil {
			rs.metrics[payload.NodeID] = append(rs.metrics[payload.NodeID], payload.Sample)
		}
	case KindEvent:
		var e store.Event
		if err := json.Unmarshal(env.Data, &e); err == nil {
			rs.events = append(rs.events, e)
			if e.Seq >= rs.nextEventSeq {
				rs.nextEventSeq = e.Seq + 1
			}
		}
	}
}

func encodeEnvelope(kind string, data any) (envelope, error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return envelope{}, err
	}
	return envelope{Kind: kind, Data: raw}, nil
}

func (b *Backend) CreateRun(ctx context.Context, r store.Run) error {
	env, err := encodeEnvelope(KindRun, r)
	if err != nil {
		return err
	}
	return b.appendEnvelope(ctx, r.ID, env)
}

func (b *Backend) FinishRun(ctx context.Context, runID, status, errMsg string) error {
	rs, err := b.getRunState(ctx, runID, true)
	if err != nil {
		return err
	}
	rs.mu.Lock()
	var run store.Run
	if rs.run != nil {
		run = *rs.run
	} else {
		run = store.Run{ID: runID}
	}
	run.Status = status
	run.Error = errMsg
	now := time.Now().UTC()
	run.FinishedAt = &now
	rs.mu.Unlock()

	env, err := encodeEnvelope(KindRun, run)
	if err != nil {
		return err
	}
	if err := b.appendEnvelope(ctx, runID, env); err != nil {
		return err
	}
	return b.flushRun(ctx, runID)
}

func (b *Backend) UpdatePlanSnapshot(ctx context.Context, runID string, snapshot []byte) error {
	rs, err := b.getRunState(ctx, runID, true)
	if err != nil {
		return err
	}
	rs.mu.Lock()
	var run store.Run
	if rs.run != nil {
		run = *rs.run
	} else {
		run = store.Run{ID: runID}
	}
	run.PlanSnapshot = snapshot
	rs.mu.Unlock()
	env, err := encodeEnvelope(KindRun, run)
	if err != nil {
		return err
	}
	return b.appendEnvelope(ctx, runID, env)
}

func (b *Backend) GetRun(ctx context.Context, runID string) (*store.Run, error) {
	rs, err := b.getRunState(ctx, runID, true)
	if err != nil {
		return nil, err
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.run == nil {
		return nil, store.ErrNotFound
	}
	clone := *rs.run
	return &clone, nil
}

// GetLatestRun scans every runs/<id>/state.ndjson key in the
// artifact store, loads the run envelope, and returns the
// most-recent match. Latency scales with the number of runs in the
// bucket; this is the deliberate tradeoff for not running a
// database. Returns store.ErrNotFound when no run matches.
func (b *Backend) GetLatestRun(ctx context.Context, pipeline string, statuses []string, maxAge time.Duration) (*store.Run, error) {
	keys, err := b.art.List(ctx, "runs/")
	if err != nil {
		if errors.Is(err, storage.ErrListNotSupported) {
			return nil, fmt.Errorf("%w: GetLatestRun needs ArtifactStore.List", ErrNotSupported)
		}
		return nil, err
	}
	statusSet := map[string]bool{}
	for _, s := range statuses {
		statusSet[s] = true
	}
	cutoff := time.Time{}
	if maxAge > 0 {
		cutoff = time.Now().Add(-maxAge)
	}
	var best *store.Run
	for _, k := range keys {
		runID, ok := RunIDFromStateKey(k)
		if !ok {
			continue
		}
		r, gerr := b.GetRun(ctx, runID)
		if gerr != nil {
			continue
		}
		if pipeline != "" && r.Pipeline != pipeline {
			continue
		}
		if len(statusSet) > 0 && !statusSet[r.Status] {
			continue
		}
		if !cutoff.IsZero() && r.StartedAt.Before(cutoff) {
			continue
		}
		if best == nil || r.StartedAt.After(best.StartedAt) {
			best = r
		}
	}
	if best == nil {
		return nil, store.ErrNotFound
	}
	return best, nil
}

func (b *Backend) CreateNode(ctx context.Context, n store.Node) error {
	if n.Status == "" {
		n.Status = "pending"
	}
	env, err := encodeEnvelope(KindNode, n)
	if err != nil {
		return err
	}
	return b.appendEnvelope(ctx, n.RunID, env)
}

func (b *Backend) StartNode(ctx context.Context, runID, nodeID string) error {
	return b.mutateNode(ctx, runID, nodeID, func(n *store.Node) {
		n.Status = "running"
		now := time.Now().UTC()
		n.StartedAt = &now
	})
}

func (b *Backend) FinishNode(ctx context.Context, runID, nodeID, outcome, errMsg string, output []byte) error {
	return b.FinishNodeWithReason(ctx, runID, nodeID, outcome, errMsg, output, store.FailureUnknown, nil)
}

func (b *Backend) FinishNodeWithReason(ctx context.Context, runID, nodeID, outcome, errMsg string, output []byte, reason string, exitCode *int) error {
	return b.mutateNode(ctx, runID, nodeID, func(n *store.Node) {
		n.Status = "done"
		n.Outcome = outcome
		n.Error = errMsg
		n.Output = output
		now := time.Now().UTC()
		n.FinishedAt = &now
		n.FailureReason = reason
		n.ExitCode = exitCode
	})
}

func (b *Backend) UpdateNodeDeps(ctx context.Context, runID, nodeID string, deps []string) error {
	return b.mutateNode(ctx, runID, nodeID, func(n *store.Node) { n.Deps = deps })
}

func (b *Backend) UpdateNodeActivity(ctx context.Context, runID, nodeID, detail string) error {
	return b.mutateNode(ctx, runID, nodeID, func(n *store.Node) {
		n.StatusDetail = detail
		now := time.Now().UTC()
		n.LastHeartbeat = &now
	})
}

func (b *Backend) SetNodeStatus(ctx context.Context, runID, nodeID, status string) error {
	return b.mutateNode(ctx, runID, nodeID, func(n *store.Node) { n.Status = status })
}

func (b *Backend) SetNodeArtifactManifest(ctx context.Context, runID, nodeID, manifestDigest string) error {
	return b.mutateNode(ctx, runID, nodeID, func(n *store.Node) { n.ArtifactManifest = manifestDigest })
}

func (b *Backend) GetNode(ctx context.Context, runID, nodeID string) (*store.Node, error) {
	rs, err := b.getRunState(ctx, runID, true)
	if err != nil {
		return nil, err
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	n, ok := rs.nodes[nodeID]
	if !ok {
		return nil, store.ErrNotFound
	}
	clone := *n
	return &clone, nil
}

func (b *Backend) TouchNodeHeartbeat(ctx context.Context, runID, nodeID string) error {
	return b.mutateNode(ctx, runID, nodeID, func(n *store.Node) {
		now := time.Now().UTC()
		n.LastHeartbeat = &now
	})
}

// TouchRunHeartbeat is a no-op in S3 mode. The controller-side orphan
// reaper that consumes last_heartbeat_at runs against a SQL store
// the laptop owns directly; S3 mode reconciles orphans via per-node
// heartbeats on the laptop instead.
func (b *Backend) TouchRunHeartbeat(_ context.Context, _ string) error { return nil }

func (b *Backend) AppendNodeAnnotation(ctx context.Context, runID, nodeID, msg string) error {
	return b.mutateNode(ctx, runID, nodeID, func(n *store.Node) {
		n.Annotations = append(n.Annotations, msg)
	})
}

func (b *Backend) SetNodeSummary(ctx context.Context, runID, nodeID, md string) error {
	return b.mutateNode(ctx, runID, nodeID, func(n *store.Node) { n.Summary = md })
}

// mutateNode reads the current node snapshot, applies f, and appends
// a new KindNode envelope reflecting the mutation. Creates a stub
// row if the node doesn't exist yet (mirrors *store.Store's UPDATE-
// without-error behavior on missing rows during reorders).
func (b *Backend) mutateNode(ctx context.Context, runID, nodeID string, f func(*store.Node)) error {
	rs, err := b.getRunState(ctx, runID, true)
	if err != nil {
		return err
	}
	rs.mu.Lock()
	n, ok := rs.nodes[nodeID]
	if !ok {
		n = &store.Node{RunID: runID, NodeID: nodeID}
	}
	clone := *n
	if clone.Annotations != nil {
		clone.Annotations = append([]string(nil), n.Annotations...)
	}
	if clone.Deps != nil {
		clone.Deps = append([]string(nil), n.Deps...)
	}
	rs.mu.Unlock()
	f(&clone)
	env, err := encodeEnvelope(KindNode, clone)
	if err != nil {
		return err
	}
	return b.appendEnvelope(ctx, runID, env)
}

func (b *Backend) StartNodeStep(ctx context.Context, runID, nodeID, stepID string) error {
	return b.mutateStep(ctx, runID, nodeID, stepID, func(s *store.NodeStep) {
		if s.StartedAt == nil {
			now := time.Now().UTC()
			s.StartedAt = &now
			s.Status = "running"
		}
	})
}

func (b *Backend) FinishNodeStep(ctx context.Context, runID, nodeID, stepID, status string) error {
	return b.mutateStep(ctx, runID, nodeID, stepID, func(s *store.NodeStep) {
		s.Status = status
		now := time.Now().UTC()
		if s.StartedAt == nil {
			s.StartedAt = &now
		}
		s.FinishedAt = &now
	})
}

func (b *Backend) SkipNodeStep(ctx context.Context, runID, nodeID, stepID string) error {
	return b.mutateStep(ctx, runID, nodeID, stepID, func(s *store.NodeStep) {
		now := time.Now().UTC()
		s.Status = "skipped"
		s.StartedAt = &now
		s.FinishedAt = &now
	})
}

func (b *Backend) AppendStepAnnotation(ctx context.Context, runID, nodeID, stepID, msg string) error {
	return b.mutateStep(ctx, runID, nodeID, stepID, func(s *store.NodeStep) {
		s.Annotations = append(s.Annotations, msg)
	})
}

func (b *Backend) SetStepSummary(ctx context.Context, runID, nodeID, stepID, md string) error {
	return b.mutateStep(ctx, runID, nodeID, stepID, func(s *store.NodeStep) { s.Summary = md })
}

func (b *Backend) ListNodeSteps(ctx context.Context, runID string) ([]*store.NodeStep, error) {
	rs, err := b.getRunState(ctx, runID, true)
	if err != nil {
		return nil, err
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	out := make([]*store.NodeStep, 0, len(rs.stepOrder))
	for _, k := range rs.stepOrder {
		if byNode, ok := rs.steps[k.nodeID]; ok {
			if s, ok := byNode[k.stepID]; ok {
				clone := *s
				out = append(out, &clone)
			}
		}
	}
	return out, nil
}

func (b *Backend) mutateStep(ctx context.Context, runID, nodeID, stepID string, f func(*store.NodeStep)) error {
	rs, err := b.getRunState(ctx, runID, true)
	if err != nil {
		return err
	}
	rs.mu.Lock()
	byNode, ok := rs.steps[nodeID]
	if !ok {
		byNode = map[string]*store.NodeStep{}
	}
	s, ok := byNode[stepID]
	if !ok {
		s = &store.NodeStep{RunID: runID, NodeID: nodeID, StepID: stepID}
	}
	clone := *s
	if clone.Annotations != nil {
		clone.Annotations = append([]string(nil), s.Annotations...)
	}
	rs.mu.Unlock()
	f(&clone)
	env, err := encodeEnvelope(KindNodeStep, clone)
	if err != nil {
		return err
	}
	return b.appendEnvelope(ctx, runID, env)
}

func (b *Backend) AddNodeMetricSample(ctx context.Context, runID, nodeID string, sample store.MetricSample) error {
	payload := struct {
		NodeID string             `json:"node_id"`
		Sample store.MetricSample `json:"sample"`
	}{NodeID: nodeID, Sample: sample}
	env, err := encodeEnvelope(KindMetricSample, payload)
	if err != nil {
		return err
	}
	return b.appendEnvelope(ctx, runID, env)
}

// AppendEvent serializes payload as an event envelope. Seq is
// per-run monotonic, including after a backend restart and reload.
func (b *Backend) AppendEvent(ctx context.Context, runID, nodeID, kind string, payload []byte) error {
	rs, err := b.getRunState(ctx, runID, true)
	if err != nil {
		return err
	}
	rs.mu.Lock()
	seq := rs.nextEventSeq
	rs.nextEventSeq++
	e := store.Event{
		RunID:   runID,
		Seq:     seq,
		NodeID:  nodeID,
		Kind:    kind,
		TS:      time.Now().UTC(),
		Payload: payload,
	}
	env, err := encodeEnvelope(KindEvent, e)
	if err != nil {
		rs.mu.Unlock()
		return err
	}
	rs.envelopes = append(rs.envelopes, env)
	rs.bufSize += len(env.Data) + len(env.Kind) + 32
	rs.dirty = true
	applyEnvelope(rs, env)
	shouldFlush := rs.bufSize >= b.bufferLimit
	rs.mu.Unlock()
	if shouldFlush {
		return b.flushRun(ctx, runID)
	}
	return nil
}

func (b *Backend) ListEventsAfter(ctx context.Context, runID string, afterSeq int64, limit int) ([]store.Event, error) {
	if limit <= 0 {
		limit = 500
	}
	rs, err := b.getRunState(ctx, runID, true)
	if err != nil {
		return nil, err
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	out := make([]store.Event, 0, min(limit, len(rs.events)))
	for _, event := range rs.events {
		if event.Seq <= afterSeq {
			continue
		}
		out = append(out, event)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

// GetNodeOutput returns the finished node's raw output bytes.
func (b *Backend) GetNodeOutput(ctx context.Context, runID, nodeID string) ([]byte, error) {
	n, err := b.GetNode(ctx, runID, nodeID)
	if err != nil {
		return nil, err
	}
	return n.Output, nil
}

// RunIDFromStateKey extracts "abc" from "runs/abc/state.ndjson",
// returning ("", false) for keys with a different shape. It is the
// single definition of the state-key layout this backend writes;
// every consumer that lists the bucket parses keys through it.
func RunIDFromStateKey(key string) (string, bool) {
	const prefix = "runs/"
	const suffix = "/state.ndjson"
	if !strings.HasPrefix(key, prefix) || !strings.HasSuffix(key, suffix) {
		return "", false
	}
	mid := key[len(prefix) : len(key)-len(suffix)]
	if mid == "" || strings.Contains(mid, "/") {
		return "", false
	}
	return mid, true
}

// isTransient classifies an error as worth retrying via the outbox.
// Context deadlines, network timeouts, and connection-refused are
// transient; HTTP 4xx (auth, policy) bubble up unchanged so the user
// sees the configuration problem.
func isTransient(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	msg := err.Error()
	for _, marker := range []string{
		"connection refused",
		"i/o timeout",
		"no such host",
		"connection reset",
		"EOF",
	} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}
