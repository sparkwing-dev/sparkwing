package orchestrator_test

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/fs"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/s3state"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// TestLocalBackends_ThreadsArtifact confirms LocalBackends carries the
// supplied artifact store onto Backends.Artifact and leaves it nil when
// none is supplied.
func TestLocalBackends_ThreadsArtifact(t *testing.T) {
	p := newPaths(t)
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

// TestS3Backends_ThreadsArtifact confirms S3Backends carries the
// supplied artifact store onto Backends.Artifact.
func TestS3Backends_ThreadsArtifact(t *testing.T) {
	logs, err := fs.NewLogStore(t.TempDir())
	if err != nil {
		t.Fatalf("fs.NewLogStore: %v", err)
	}
	cache, err := fs.NewArtifactStore(t.TempDir())
	if err != nil {
		t.Fatalf("fs.NewArtifactStore: %v", err)
	}
	state := s3state.New(cache)
	t.Cleanup(func() { _ = state.Close() })

	art := &noListArtifact{}
	if b := orchestrator.S3Backends(logs, state, art); b.Artifact != art {
		t.Errorf("Artifact = %v, want the supplied store", b.Artifact)
	}
}
