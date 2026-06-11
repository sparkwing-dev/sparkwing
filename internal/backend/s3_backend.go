package backend

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/s3state"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// DefaultLiveTTL bounds how long the dashboard's parsed run snapshot
// is reused before it re-reads the object store. Live S3 runs append
// to state.ndjson over time; a non-zero TTL turns the cache into a
// poll-with-jitter freshness window. Set to zero to disable caching.
const DefaultLiveTTL = 2 * time.Second

// S3Backend serves the dashboard from runs/<id>/state.ndjson dumps in
// an ArtifactStore. State entries are cached with a short TTL so live
// runs (Mode 2 NDJSON appends) appear with sub-second latency without
// hammering the bucket on every request.
type S3Backend struct {
	store    storage.ArtifactStore
	logStore storage.LogStore // nil means logs render as empty
	liveTTL  time.Duration

	mu    sync.Mutex
	cache map[string]*cachedState

	caps Capabilities
}

type cachedState struct {
	state     *runState
	fetchedAt time.Time
}

type runState struct {
	run    *store.Run
	nodes  []*store.Node
	events []store.Event
}

// NewS3Backend binds an S3Backend to the given artifact store. logStore
// is optional. The artifact store must support List.
func NewS3Backend(art storage.ArtifactStore, logStore storage.LogStore) *S3Backend {
	return &S3Backend{
		store:    art,
		logStore: logStore,
		liveTTL:  DefaultLiveTTL,
		cache:    map[string]*cachedState{},
	}
}

// SetLiveTTL overrides DefaultLiveTTL. A zero or negative value
// disables caching (every read parses the object store fresh).
func (b *S3Backend) SetLiveTTL(d time.Duration) { b.liveTTL = d }

var _ Backend = (*S3Backend)(nil)

// SetCapabilities overrides the default S3-only capabilities body.
func (b *S3Backend) SetCapabilities(c Capabilities) { b.caps = c }

func (b *S3Backend) Capabilities(context.Context) (Capabilities, error) {
	if b.caps.Mode == "" {
		return Capabilities{
			Mode:     "s3-only",
			Storage:  CapabilitiesStorage{Artifacts: "s3", Logs: "s3", Runs: "s3"},
			Features: []string{"pipelines", "runs", "logs"},
			ReadOnly: true,
		}, nil
	}
	return b.caps, nil
}

// stateKey mirrors orchestrator.dumpRunState's output path.
func stateKey(runID string) string {
	return "runs/" + runID + "/state.ndjson"
}

func (b *S3Backend) ListRuns(ctx context.Context, f store.RunFilter) ([]*store.Run, error) {
	keys, err := b.store.List(ctx, "runs/")
	if err != nil {
		if errors.Is(err, storage.ErrListNotSupported) {
			return nil, fmt.Errorf("S3Backend.ListRuns: backend does not support enumeration")
		}
		return nil, err
	}
	var runs []*store.Run
	for _, k := range keys {
		runID, ok := runIDFromStateKey(k)
		if !ok {
			continue
		}
		st, err := b.loadState(ctx, runID)
		if err != nil {
			// One bad dump must not poison the whole list.
			continue
		}
		if st.run != nil {
			runs = append(runs, st.run)
		}
	}
	runs = applyRunFilter(runs, f)
	return runs, nil
}

func (b *S3Backend) GetRun(ctx context.Context, runID string) (*store.Run, error) {
	st, err := b.loadState(ctx, runID)
	if err != nil {
		return nil, err
	}
	if st.run == nil {
		return nil, store.ErrNotFound
	}
	return st.run, nil
}

func (b *S3Backend) ListNodes(ctx context.Context, runID string) ([]*store.Node, error) {
	st, err := b.loadState(ctx, runID)
	if err != nil {
		return nil, err
	}
	return st.nodes, nil
}

// ListEventsAfter returns events with seq > afterSeq. Mode 2 state
// dumps interleave event envelopes alongside run/node rows; older
// final-only dumps carry no events and yield an empty slice.
func (b *S3Backend) ListEventsAfter(ctx context.Context, runID string, afterSeq int64, limit int) ([]store.Event, error) {
	st, err := b.loadState(ctx, runID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	if limit <= 0 {
		limit = 500
	}
	out := make([]store.Event, 0, len(st.events))
	for _, e := range st.events {
		if e.Seq <= afterSeq {
			continue
		}
		out = append(out, e)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (b *S3Backend) ReadNodeLog(ctx context.Context, runID, nodeID string, opts ReadOpts) ([]byte, error) {
	if b.logStore == nil {
		return nil, nil
	}
	return b.logStore.Read(ctx, runID, nodeID, toStorageReadOpts(opts))
}

func (b *S3Backend) StreamNodeLog(ctx context.Context, runID, nodeID string) (io.ReadCloser, error) {
	if b.logStore == nil {
		return nil, nil
	}
	return b.logStore.Stream(ctx, runID, nodeID)
}

// loadState fetches + parses runs/<id>/state.ndjson with a short TTL
// cache so live Mode 2 runs (NDJSON appended over time) appear in the
// dashboard within liveTTL of an update.
func (b *S3Backend) loadState(ctx context.Context, runID string) (*runState, error) {
	b.mu.Lock()
	if entry, ok := b.cache[runID]; ok {
		if b.liveTTL <= 0 || time.Since(entry.fetchedAt) < b.liveTTL {
			b.mu.Unlock()
			return entry.state, nil
		}
	}
	b.mu.Unlock()

	rc, err := b.store.Get(ctx, stateKey(runID))
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, store.ErrNotFound
		}
		return nil, err
	}
	defer rc.Close()
	st, err := parseStateNDJSON(rc)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", stateKey(runID), err)
	}

	b.mu.Lock()
	b.cache[runID] = &cachedState{state: st, fetchedAt: time.Now()}
	// Drop-on-overflow safety valve; not a true LRU.
	if len(b.cache) > 1024 {
		for k := range b.cache {
			delete(b.cache, k)
			if len(b.cache) <= 1024 {
				break
			}
		}
	}
	b.mu.Unlock()
	return st, nil
}

// parseStateNDJSON decodes the dump format the orchestrator writes
// and the s3state backend appends to over the lifetime of a run.
// Replay semantics: last-write-wins for run/node records (Mode 2
// re-PUTs the whole envelope log on each flush), accumulating for
// events.
func parseStateNDJSON(rc io.Reader) (*runState, error) {
	type envelope struct {
		Kind string          `json:"kind"`
		Data json.RawMessage `json:"data"`
	}
	st := &runState{}
	nodesByID := map[string]*store.Node{}
	var nodeOrder []string
	scanner := bufio.NewScanner(rc)
	// state.ndjson can carry long PlanSnapshot blobs inlined into the
	// run record; default 64K bufio limit is not enough.
	buf := make([]byte, 0, 1<<20)
	scanner.Buffer(buf, 16<<20)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var env envelope
		if err := json.Unmarshal(line, &env); err != nil {
			return nil, err
		}
		switch env.Kind {
		case "run":
			run := &store.Run{}
			if err := json.Unmarshal(env.Data, run); err != nil {
				return nil, err
			}
			st.run = run
		case "node":
			node := &store.Node{}
			if err := json.Unmarshal(env.Data, node); err != nil {
				return nil, err
			}
			if _, seen := nodesByID[node.NodeID]; !seen {
				nodeOrder = append(nodeOrder, node.NodeID)
			}
			nodesByID[node.NodeID] = node
		case "event":
			var e store.Event
			if err := json.Unmarshal(env.Data, &e); err != nil {
				return nil, err
			}
			st.events = append(st.events, e)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	for _, id := range nodeOrder {
		st.nodes = append(st.nodes, nodesByID[id])
	}
	return st, nil
}

// runIDFromStateKey parses state keys through the layout owner's
// single definition in pkg/storage/s3state.
func runIDFromStateKey(key string) (string, bool) {
	return s3state.RunIDFromStateKey(key)
}

// applyRunFilter mirrors store.ListRuns' SQL semantics in memory:
// filter, sort newest-first, then limit.
func applyRunFilter(runs []*store.Run, f store.RunFilter) []*store.Run {
	pipelineSet := toSet(f.Pipelines)
	statusSet := toSet(f.Statuses)
	out := runs[:0]
	for _, r := range runs {
		if r == nil {
			continue
		}
		if len(pipelineSet) > 0 && !pipelineSet[r.Pipeline] {
			continue
		}
		if len(statusSet) > 0 && !statusSet[r.Status] {
			continue
		}
		if !f.Since.IsZero() && r.StartedAt.Before(f.Since) {
			continue
		}
		if f.ParentRunID != "" && r.ParentRunID != f.ParentRunID {
			continue
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].StartedAt.After(out[j].StartedAt)
	})
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func toSet(values []string) map[string]bool {
	if len(values) == 0 {
		return nil
	}
	m := make(map[string]bool, len(values))
	for _, v := range values {
		m[v] = true
	}
	return m
}
