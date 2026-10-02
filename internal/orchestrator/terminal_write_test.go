package orchestrator

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/logbatch"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type terminalWriteState struct {
	localState
	durable  *bufferedLogStore
	failure  error
	commit   bool
	snapshot string
}

func (s *terminalWriteState) FinishNode(ctx context.Context, runID, nodeID, outcome, text string, output []byte) error {
	if outcome != string(sparkwing.Success) {
		return s.localState.FinishNode(ctx, runID, nodeID, outcome, text, output)
	}
	s.snapshot = s.durable.dump()
	if s.commit {
		if err := s.localState.FinishNode(ctx, runID, nodeID, outcome, text, output); err != nil {
			return err
		}
	}
	return s.failure
}

func TestNodeTerminalWriteMustBeAcknowledged(t *testing.T) {
	failure := errors.New("terminal acknowledgement unavailable")
	for _, tc := range []struct {
		name    string
		commit  bool
		failure error
	}{
		{"rejected", false, failure}, {"committed response lost", true, failure}, {"acknowledged", true, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, backends := terminalExecutionFixture(t)
			durable := &bufferedLogStore{}
			batcher := logbatch.New(durable, logbatch.WithFlushInterval(time.Hour), logbatch.WithBufferThreshold(1<<20))
			t.Cleanup(func() {
				if err := batcher.Close(); err != nil {
					t.Error(err)
				}
			})
			state := &terminalWriteState{localState: localState{st: st}, durable: durable, failure: tc.failure, commit: tc.commit}
			backends.State = state
			backends.Logs = NewLogStoreBackend(batcher, nil)
			node := sparkwing.Job(sparkwing.NewPlan(), "build", func(ctx context.Context) error { sparkwing.Info(ctx, "body reached completion"); return nil })
			result := NewNodeExecutor(backends).RunNode(t.Context(), runner.Request{RunID: "run", NodeID: "build", Node: node})
			want := sparkwing.Success
			if tc.failure != nil {
				want = sparkwing.Failed
			}
			if result.Outcome != want || !errors.Is(result.Err, tc.failure) || (tc.failure != nil && result.Output != nil) {
				t.Fatalf("terminal write result=%+v; want %s and %v", result, want, tc.failure)
			}
			for _, text := range []string{"body reached completion", `"node_end"`} {
				if !strings.Contains(state.snapshot, text) {
					t.Errorf("terminal write preceded durable %q: %s", text, state.snapshot)
				}
			}
			stored, err := st.GetNode(t.Context(), "run", "build")
			if tc.commit {
				want = sparkwing.Success
			}
			if err != nil || stored == nil || stored.Outcome != string(want) {
				t.Fatalf("stored node=%+v, %v; want %s", stored, err, want)
			}
			events, err := st.ListEventsAfter(t.Context(), "run", 0, 100)
			if err != nil {
				t.Fatal(err)
			}
			succeeded := false
			for _, event := range events {
				if event.Kind == "node_succeeded" {
					succeeded = true
				}
			}
			if succeeded != (tc.failure == nil) {
				t.Errorf("node_succeeded=%t after terminal error %v", succeeded, tc.failure)
			}
		})
	}
}

func terminalExecutionFixture(t *testing.T) (*store.Store, Backends) {
	t.Helper()
	paths := PathsAt(t.TempDir())
	if err := paths.EnsureRoot(); err != nil {
		t.Fatal(err)
	}
	st, err := teststore.Open(paths.StateDB())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.CreateRun(t.Context(), store.Run{ID: "run", Pipeline: "metrics", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(t.Context(), store.Node{RunID: "run", NodeID: "build", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	return st, LocalBackends(paths, st, nil)
}
