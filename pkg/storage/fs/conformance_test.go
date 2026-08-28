package fs_test

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/conformance"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/fs"
)

func TestConformance_ArtifactStore(t *testing.T) {
	conformance.TestArtifactStore(t, func() storage.ArtifactStore {
		s, err := fs.NewArtifactStore(t.TempDir())
		if err != nil {
			t.Fatalf("NewArtifactStore: %v", err)
		}
		return s
	})
}

func TestConformance_ConditionalWriter(t *testing.T) {
	conformance.TestConditionalWriter(t, func() storage.ArtifactStore {
		s, err := fs.NewArtifactStore(t.TempDir())
		if err != nil {
			t.Fatalf("NewArtifactStore: %v", err)
		}
		return s
	})
}

func TestConformance_LogStore(t *testing.T) {
	conformance.TestLogStore(t, func() storage.LogStore {
		s, err := fs.NewLogStore(t.TempDir())
		if err != nil {
			t.Fatalf("NewLogStore: %v", err)
		}
		return s
	})
}
