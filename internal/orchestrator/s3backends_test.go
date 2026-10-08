package orchestrator_test

import (
	"context"
	"errors"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/s3state"
)

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
