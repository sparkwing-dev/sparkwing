package orchestrator_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/fs"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/s3state"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type s3okPipe struct{ sparkwing.Base }

func (s3okPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	sparkwing.Job(plan, rc.Pipeline, func(ctx context.Context) error { return nil })
	return nil
}

var s3RegisterOnce sync.Once

func registerS3Pipelines(t *testing.T) {
	t.Helper()
	s3RegisterOnce.Do(func() {
		sparkwing.Register[sparkwing.NoInputs]("s3-ok",
			func() sparkwing.Pipeline[sparkwing.NoInputs] { return &s3okPipe{} })
	})
}

// TestRunLocal_S3State_DispatchesToS3Backends verifies that RunLocal
// recognizes a *s3state.Backend on opts.State and wires the S3Backends
// bundle (NDJSON state + log store + noop concurrency) instead of
// LocalBackends. The pipeline runs to terminal state and its run
// record is readable back from the underlying artifact store.
func TestRunLocal_S3State_DispatchesToS3Backends(t *testing.T) {
	registerS3Pipelines(t)
	paths := newPaths(t)

	stateRoot := t.TempDir()
	logsRoot := t.TempDir()

	art, err := fs.NewArtifactStore(stateRoot)
	if err != nil {
		t.Fatalf("fs.NewArtifactStore: %v", err)
	}
	logs, err := fs.NewLogStore(logsRoot)
	if err != nil {
		t.Fatalf("fs.NewLogStore: %v", err)
	}

	state := s3state.New(art, s3state.WithFlushInterval(20*time.Millisecond))

	res, err := orchestrator.RunLocal(context.Background(), paths, orchestrator.Options{
		Pipeline: "s3-ok",
		State:    state,
		LogStore: logs,
	})
	if err != nil {
		t.Fatalf("RunLocal: %v", err)
	}
	if res.Status != "success" {
		t.Fatalf("status = %q, want success (err=%v)", res.Status, res.Error)
	}

	reader := s3state.New(art)
	t.Cleanup(func() { _ = reader.Close() })
	got, err := reader.GetRun(context.Background(), res.RunID)
	if err != nil {
		t.Fatalf("reader GetRun: %v", err)
	}
	if got.Pipeline != "s3-ok" {
		t.Errorf("pipeline = %q, want s3-ok", got.Pipeline)
	}
	if got.Status == "" {
		t.Errorf("status was never persisted")
	}
}

// TestRunLocal_S3State_NoLogStore_Fails locks in the contract that
// Mode 2 requires a LogStore (the orchestrator can't dispatch without
// a log sink, and the SQLite-fallback path is not available in this
// dispatch arm).
func TestRunLocal_S3State_NoLogStore_Fails(t *testing.T) {
	registerS3Pipelines(t)
	paths := newPaths(t)

	art, err := fs.NewArtifactStore(t.TempDir())
	if err != nil {
		t.Fatalf("fs.NewArtifactStore: %v", err)
	}
	state := s3state.New(art)
	t.Cleanup(func() { _ = state.Close() })

	_, err = orchestrator.RunLocal(context.Background(), paths, orchestrator.Options{
		Pipeline: "s3-ok",
		State:    state,
	})
	if err == nil {
		t.Fatal("RunLocal succeeded without a LogStore; want error")
	}
}

// TestS3StateBackend_ListNotSupported_BubbleAsErrNotSupported makes
// sure GetLatestRun degrades cleanly when the artifact store cannot
// enumerate keys (some HTTP-only object stores).
func TestS3StateBackend_ListNotSupported_BubbleAsErrNotSupported(t *testing.T) {
	art := &noListArtifact{ArtifactStore: stubArtifact{}}
	state := s3state.New(art)
	t.Cleanup(func() { _ = state.Close() })

	_, err := state.GetLatestRun(context.Background(), "any", nil, 0)
	if !errors.Is(err, s3state.ErrNotSupported) {
		t.Fatalf("err = %v, want wraps ErrNotSupported", err)
	}
}

type stubArtifact struct{ storage.ArtifactStore }

type noListArtifact struct {
	storage.ArtifactStore
}

func (n *noListArtifact) List(_ context.Context, _ string) ([]string, error) {
	return nil, storage.ErrListNotSupported
}
