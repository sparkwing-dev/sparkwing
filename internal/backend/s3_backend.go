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
	"strings"
	"sync"

	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
)

// S3Backend serves the dashboard from runs/<id>/state.ndjson dumps in
// an ArtifactStore. State files are written once at run-finish time so
// the in-memory cache is invalidation-free.
type S3Backend struct {
	store    storage.ArtifactStore
	logStore storage.LogStore // nil means logs render as empty

	mu    sync.Mutex
	cache map[string]*runState

	caps Capabilities
}

type runState struct {
	run   *store.Run
	nodes []*store.Node
}

// NewS3Backend binds an S3Backend to the given artifact store. logStore
// is optional. The artifact store must support List.
func NewS3Backend(art storage.ArtifactStore, logStore storage.LogStore) *S3Backend {
	return &S3Backend{
		store:    art,
		logStore: logStore,
		cache:    map[string]*runState{},
	}
}

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

// ListEventsAfter returns no events: state.ndjson captures the terminal
// record set, not the runtime event log.
func (b *S3Backend) ListEventsAfter(context.Context, string, int64, int) ([]store.Event, error) {
	return nil, nil
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

// loadState fetches + parses runs/<id>/state.ndjson, cached by runID.
func (b *S3Backend) loadState(ctx context.Context, runID string) (*runState, error) {
	b.mu.Lock()
	if s, ok := b.cache[runID]; ok {
		b.mu.Unlock()
		return s, nil
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
	b.cache[runID] = st
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

// parseStateNDJSON decodes the dump format the orchestrator writes:
//
//	{"kind":"run","data":{...}}
//	{"kind":"node","data":{...}}
//
// Record order is not enforced; kind alone determines placement.
func parseStateNDJSON(rc io.Reader) (*runState, error) {
	type envelope struct {
		Kind string          `json:"kind"`
		Data json.RawMessage `json:"data"`
	}
	st := &runState{}
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
			st.nodes = append(st.nodes, node)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return st, nil
}

// runIDFromStateKey returns ("abc", true) for "runs/abc/state.ndjson"
// and ("", false) for anything else under runs/.
func runIDFromStateKey(key string) (string, bool) {
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
