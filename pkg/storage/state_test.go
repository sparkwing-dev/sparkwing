package storage_test

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// TestStateStore_StoreSatisfiesInterface fails to compile if
// *store.Store ever drifts away from the storage.StateStore method
// set. The pkg-scope assertion in state.go already catches this; the
// test exists so the contract is exercised from an external package
// (the assertion otherwise checks only that storage's own view of
// *store.Store satisfies the interface, which is by definition true
// where both names are visible from one file).
func TestStateStore_StoreSatisfiesInterface(t *testing.T) {
	var _ storage.StateStore = (*store.Store)(nil)
}
