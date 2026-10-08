package orchestrator

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

func startTestLoopbackShim(t *testing.T) (*store.Store, *loopbackController) {
	t.Helper()
	paths := PathsAt(t.TempDir())
	st, err := teststore.Open(paths.StateDB())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	if err := st.CreateRun(ctx, store.Run{ID: "run-1", Pipeline: "demo", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "run-1", NodeID: "build", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	backends := LocalBackends(paths, st, nil)
	shim, err := startLoopbackShim(backends.State, backends.Concurrency, "run-1", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(shim.Close)
	return st, shim
}

func TestLoopbackShimServesALocalNodesBounceAndAttemptRoutes(t *testing.T) {
	st, shim := startTestLoopbackShim(t)
	c := client.NewWithToken(shim.url, nil, shim.token)
	ctx := context.Background()
	if b, err := c.PendingNodeBounce(ctx, "run-1", "build"); err != nil || b != nil {
		t.Fatalf("bounce poll = %+v, %v; want none pending", b, err)
	}
	if err := c.ConsumeNodeBounce(ctx, "run-1", "build", 1, store.BounceBounced); err == nil {
		t.Fatal("consumed a bounce request no backend holds")
	}
	start := store.ExecutionStart{AttemptOrdinal: 1, ExecutorKind: store.ExecutorKindLocal, ExecutorID: "laptop"}
	if err := c.AcknowledgeNodeExecutionStart(ctx, "run-1", "build", start); err != nil {
		t.Fatalf("execution-start on the shim: %v", err)
	}
	finish := store.ExecutionAttemptFinish{AttemptOrdinal: 1, Outcome: "success", ExecutorKind: store.ExecutorKindLocal}
	if err := c.FinishNodeExecutionAttempt(ctx, "run-1", "build", finish); err != nil {
		t.Fatalf("execution-finish on the shim: %v", err)
	}
	attempts, err := st.ListNodeExecutionAttempts(ctx, "run-1", "build")
	if err != nil || len(attempts) != 1 || attempts[0].FinishedAt == nil {
		t.Fatalf("recorded attempts = %+v, %v", attempts, err)
	}
}
