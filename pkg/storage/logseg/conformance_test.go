package logseg_test

import (
	"context"
	"io"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/conformance"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/logseg"
)

type segmentLogStore struct {
	t     *testing.T
	objs  *memObjects
	mu    sync.Mutex
	nodes map[string]*segmentNode
}

type segmentNode struct {
	sink  *logseg.Sink
	up    *memUploader
	clock *manualTimer
	cuts  int
}

func (s *segmentLogStore) prefix(runID, nodeID string) string {
	p, err := logseg.AttemptPrefix(logseg.ClassStandard, "acme", runID, nodeID, 1, 1)
	if err != nil {
		s.t.Fatal(err)
	}
	return p
}

func (s *segmentLogStore) Append(ctx context.Context, runID, nodeID string, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := runID + "\x00" + nodeID
	n := s.nodes[key]
	if n == nil {
		n = &segmentNode{up: newMemUploader(s.objs, s.prefix(runID, nodeID)), clock: &manualTimer{}}
		sink, err := logseg.NewSink(ctx, n.up, logseg.Options{
			Run: runID, Node: nodeID, SpoolDir: s.t.TempDir(),
			AfterFunc: n.clock.after,
		})
		if err != nil {
			return err
		}
		n.sink = sink
		s.nodes[key] = n
		s.t.Cleanup(func() { _, _ = sink.Close(ctx, logseg.End{}) })
	}
	if err := n.sink.Append(data, "", 0, 0); err != nil {
		return err
	}
	n.clock.fire()
	n.cuts++
	waitStored(s.t, n.up, n.cuts)
	return nil
}

func (s *segmentLogStore) Read(ctx context.Context, runID, nodeID string, opts storage.ReadOpts) ([]byte, error) {
	l, err := logseg.Open(ctx, s.objs, s.prefix(runID, nodeID))
	if err != nil {
		return nil, err
	}
	return l.Read(ctx, s.objs, opts)
}

func (s *segmentLogStore) ReadRun(ctx context.Context, runID string) ([]byte, error) {
	s.mu.Lock()
	var nodes []string
	for key := range s.nodes {
		if run, node, _ := strings.Cut(key, "\x00"); run == runID {
			nodes = append(nodes, node)
		}
	}
	s.mu.Unlock()
	sort.Strings(nodes)
	var out []byte
	for _, node := range nodes {
		body, err := s.Read(ctx, runID, node, storage.ReadOpts{})
		if err != nil {
			return nil, err
		}
		out = append(out, body...)
	}
	return out, nil
}

func (s *segmentLogStore) Stream(context.Context, string, string) (io.ReadCloser, error) {
	return nil, nil
}

func (s *segmentLogStore) DeleteRun(ctx context.Context, runID string) error {
	run, err := logseg.RunPrefix(logseg.ClassStandard, "acme", runID)
	if err != nil {
		return err
	}
	s.mu.Lock()
	for key, n := range s.nodes {
		if strings.HasPrefix(key, runID+"\x00") {
			_, _ = n.sink.Close(ctx, logseg.End{})
			delete(s.nodes, key)
		}
	}
	s.mu.Unlock()
	s.objs.deletePrefix(run)
	return nil
}

func TestSegmentLogConformance(t *testing.T) {
	conformance.TestLogStore(t, func() storage.LogStore {
		return &segmentLogStore{t: t, objs: newMemObjects(), nodes: map[string]*segmentNode{}}
	})
}
