package orchestrator_test

import (
	"context"
	"sync"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestLocalBackends_ThreadsArtifact(t *testing.T) {
	p := newPathsWithStore(t)
	if err := p.EnsureRoot(); err != nil {
		t.Fatalf("ensure root: %v", err)
	}
	st, err := store.Open(p.StateDB())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	art := &noListArtifact{}
	if b := orchestrator.LocalBackends(p, st, art); b.Artifact != art {
		t.Errorf("Artifact = %v, want the supplied store", b.Artifact)
	}
	if b := orchestrator.LocalBackends(p, st, nil); b.Artifact != nil {
		t.Errorf("Artifact = %v, want nil", b.Artifact)
	}
}

type noListArtifact struct {
	storage.ArtifactStore
}

func (n *noListArtifact) List(_ context.Context, _ string) ([]string, error) {
	return nil, storage.ErrListNotSupported
}

type captureLogger struct {
	mu      sync.Mutex
	records []sparkwing.LogRecord
}

func (c *captureLogger) Log(level, msg string) {
	c.Emit(sparkwing.LogRecord{Level: level, Msg: msg})
}

func (c *captureLogger) Emit(rec sparkwing.LogRecord) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.records = append(c.records, rec)
}

func (c *captureLogger) Snapshot() []sparkwing.LogRecord {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]sparkwing.LogRecord, len(c.records))
	copy(out, c.records)
	return out
}
