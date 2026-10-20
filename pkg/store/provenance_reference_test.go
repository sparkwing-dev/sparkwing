package store_test

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

// The reference model for sealed provenance, run through the real store in the
// suite's dialect; the rows live in provenance_reference_bridge_test.go.
func TestProvenanceReferenceModel(t *testing.T) {
	store.ProvenanceReferenceModel(t, func(t *testing.T) *store.Store { return storetest.Open(t) })
}

func TestProvenanceUnverifiedWritersShareNoScope(t *testing.T) {
	store.UnverifiedWritersShareNoScope(t, func(t *testing.T) *store.Store { return storetest.Open(t) })
}

func TestChildPipelinesMustBeDeclared(t *testing.T) {
	store.ChildPipelinesMustBeDeclared(t, func(t *testing.T) *store.Store { return storetest.Open(t) })
}
