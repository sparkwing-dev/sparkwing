package orchestrator

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestInProcessRunnerMarkFailedPersistsAfterContextCancel(t *testing.T) {
	home := t.TempDir()
	paths := PathsAt(home)
	if err := paths.EnsureRoot(); err != nil {
		t.Fatalf("ensure root: %v", err)
	}
	st, err := store.Open(paths.StateDB())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = st.Close() }()

	ctx := context.Background()
	if err := st.CreateRun(ctx, store.Run{
		ID:        "run-cancelled-terminal-write",
		Pipeline:  "test",
		Status:    "running",
		StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := st.CreateNode(ctx, store.Node{
		RunID:  "run-cancelled-terminal-write",
		NodeID: "node",
		Status: "pending",
	}); err != nil {
		t.Fatalf("create node: %v", err)
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	r := &InProcessRunner{backends: LocalBackends(paths, st, nil)}
	r.markFailed(cancelled, "run-cancelled-terminal-write", "node", errors.New("local admission failed"))

	node, err := st.GetNode(ctx, "run-cancelled-terminal-write", "node")
	if err != nil {
		t.Fatalf("get node: %v", err)
	}
	if node.Outcome != string(sparkwing.Failed) {
		t.Fatalf("node outcome = %q, want failed", node.Outcome)
	}
	if node.Error != "local admission failed" {
		t.Fatalf("node error = %q, want local admission failed", node.Error)
	}
}
