package backend

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/s3state"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

const DefaultLiveTTL = 2 * time.Second

type S3Backend struct {
	store    storage.ArtifactStore
	logStore storage.LogStore
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

func NewS3Backend(art storage.ArtifactStore, logStore storage.LogStore) *S3Backend {
	return &S3Backend{
		store:    art,
		logStore: logStore,
		liveTTL:  DefaultLiveTTL,
		cache:    map[string]*cachedState{},
	}
}

func (b *S3Backend) SetLiveTTL(d time.Duration) { b.liveTTL = d }

var _ Backend = (*S3Backend)(nil)

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
			continue
		}
		if st.run != nil {
			runs = append(runs, st.run)
		}
	}
	return store.FilterRuns(runs, f)
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

// GetNodeOutput reads a node's output object beside its run's state.
func (b *S3Backend) GetNodeOutput(ctx context.Context, runID, nodeID string) ([]byte, error) {
	st, err := b.loadState(ctx, runID)
	if err != nil {
		return nil, err
	}
	for _, n := range st.nodes {
		if n.NodeID == nodeID {
			if n.OutputRef == nil {
				return nil, nil
			}
			return s3state.ReadNodeOutput(ctx, b.store, runID, *n.OutputRef)
		}
	}
	return nil, store.ErrNotFound
}

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

func parseStateNDJSON(rc io.Reader) (*runState, error) {
	type envelope struct {
		Kind string          `json:"kind"`
		Data json.RawMessage `json:"data"`
	}
	st := &runState{}
	nodesByID := map[string]*store.Node{}
	var nodeOrder []string
	scanner := bufio.NewScanner(rc)
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

func runIDFromStateKey(key string) (string, bool) {
	return s3state.RunIDFromStateKey(key)
}
