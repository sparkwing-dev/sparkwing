package orchestrator_test

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
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
