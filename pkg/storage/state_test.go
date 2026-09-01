package storage_test

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestStateStore_StoreSatisfiesInterface(t *testing.T) {
	var _ storage.StateStore = (*store.Store)(nil)
}
