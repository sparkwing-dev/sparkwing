package fs_test

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/conformance"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/fs"
)

// TestConformance_ArtifactStore runs the cross-implementation
// contract suite from pkg/storage/conformance against the fs
// backend. Add new conformance subtests there to extend coverage
// for every implementation at once.
func TestConformance_ArtifactStore(t *testing.T) {
	conformance.TestArtifactStore(t, func() storage.ArtifactStore {
		s, err := fs.NewArtifactStore(t.TempDir())
		if err != nil {
			t.Fatalf("NewArtifactStore: %v", err)
		}
		return s
	})
}

// TestConformance_ConditionalWriter runs the optional CAS-capability
// suite against the fs backend.
func TestConformance_ConditionalWriter(t *testing.T) {
	conformance.TestConditionalWriter(t, func() storage.ArtifactStore {
		s, err := fs.NewArtifactStore(t.TempDir())
		if err != nil {
			t.Fatalf("NewArtifactStore: %v", err)
		}
		return s
	})
}

// TestConformance_LogStore wires the LogStore suite against the fs
// backend.
func TestConformance_LogStore(t *testing.T) {
	conformance.TestLogStore(t, func() storage.LogStore {
		s, err := fs.NewLogStore(t.TempDir())
		if err != nil {
			t.Fatalf("NewLogStore: %v", err)
		}
		return s
	})
}
