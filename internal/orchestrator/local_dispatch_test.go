package orchestrator

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

func newInternalPaths(t *testing.T) Paths {
	t.Helper()
	root := t.TempDir()
	p := PathsAt(root)
	if err := p.EnsureRoot(); err != nil {
		t.Fatalf("ensure root: %v", err)
	}
	return p
}

func quietTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestSetupLocalExecution_CanceledContextDoesNotMintToken(t *testing.T) {
	paths := newInternalPaths(t)
	st, err := teststore.Open(paths.StateDB())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	execution, err := setupLocalExecution(ctx, paths, &Options{State: st, RunID: "canceled"}, LocalBackends(paths, st, nil), t.TempDir(), quietTestLogger())
	if execution != nil {
		execution.cleanup()
		t.Fatal("canceled setup returned an execution")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("setup error = %v; want context cancellation", err)
	}
	tokens, err := st.ListTokens("", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 0 {
		t.Fatalf("canceled setup persisted %d tokens", len(tokens))
	}
}
