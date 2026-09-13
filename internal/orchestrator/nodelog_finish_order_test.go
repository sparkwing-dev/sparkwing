package orchestrator

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/logbatch"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type bufferedLogStore struct {
	mu   sync.Mutex
	body bytes.Buffer
}

func (s *bufferedLogStore) Append(_ context.Context, _, _ string, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.body.Write(data)
	return nil
}

func (s *bufferedLogStore) Read(context.Context, string, string, storage.ReadOpts) ([]byte, error) {
	return nil, nil
}
func (s *bufferedLogStore) ReadRun(context.Context, string) ([]byte, error) { return nil, nil }
func (s *bufferedLogStore) Stream(context.Context, string, string) (io.ReadCloser, error) {
	return nil, nil
}
func (s *bufferedLogStore) DeleteRun(context.Context, string) error { return nil }

func (s *bufferedLogStore) dump() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.body.String()
}

var _ storage.LogStore = (*bufferedLogStore)(nil)

// safety: the snapshot is taken exactly as the node's status goes
// terminal, which is the ordering under test.
type finishSnapshotState struct {
	StateBackend
	durable  *bufferedLogStore
	snapshot string
	seen     bool
}

func (s *finishSnapshotState) capture() {
	if !s.seen {
		s.snapshot = s.durable.dump()
		s.seen = true
	}
}

func (s *finishSnapshotState) FinishNode(ctx context.Context, runID, nodeID, status, errMsg string, output []byte) error {
	s.capture()
	return s.StateBackend.FinishNode(ctx, runID, nodeID, status, errMsg, output)
}

func (s *finishSnapshotState) FinishNodeWithReason(ctx context.Context, runID, nodeID, status, errMsg string, output []byte, reason string, exitCode *int) error {
	s.capture()
	return s.StateBackend.FinishNodeWithReason(ctx, runID, nodeID, status, errMsg, output, reason, exitCode)
}

func TestNodeLog_IsCompleteBeforeAFailingNodeReportsTerminal(t *testing.T) {
	st := consumerTestStore(t, t.TempDir())
	const runID = "order-run"
	const nodeID = "order-node"
	if err := st.CreateRun(t.Context(), store.Run{ID: runID, Pipeline: "sample", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(t.Context(), store.Node{RunID: runID, NodeID: nodeID, Status: "pending"}); err != nil {
		t.Fatal(err)
	}

	durable := &bufferedLogStore{}
	// safety: an hour of interval and a megabyte of threshold mean nothing
	// reaches the store until something flushes the node on purpose.
	batcher := logbatch.New(durable,
		logbatch.WithFlushInterval(time.Hour),
		logbatch.WithBufferThreshold(1<<20))
	t.Cleanup(func() { _ = batcher.Close() })

	state := &finishSnapshotState{StateBackend: localState{st: st}, durable: durable}
	plan := sparkwing.NewPlan()
	boom := errors.New("node body failed")
	node := sparkwing.Job(plan, nodeID, func(ctx context.Context) error {
		sparkwing.Info(ctx, "a line the failure must not lose")
		return boom
	})

	res := NewNodeExecutor(Backends{State: state, Logs: NewLogStoreBackend(batcher, nil)}).
		RunNode(t.Context(), runner.Request{RunID: runID, NodeID: nodeID, Node: node})
	if res.Outcome != sparkwing.Failed {
		t.Fatalf("outcome = %s, want failed", res.Outcome)
	}
	if !state.seen {
		t.Fatal("the node never reported terminal")
	}
	for _, want := range []string{"a line the failure must not lose", `"node_end"`} {
		if !strings.Contains(state.snapshot, want) {
			t.Fatalf("durable log at the moment the node reported failure is missing %q:\n%s", want, state.snapshot)
		}
	}
}
