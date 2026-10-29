package localcontroller

import (
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestStateReadinessDoesNotWaitForStoreWriter(t *testing.T) {
	var hold atomic.Bool
	entered, release := make(chan struct{}), make(chan struct{})
	// safety: the clock barrier is reached after acquiring the store's writer connection.
	owned, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"), teststore.WithClock(func() time.Time {
		if hold.CompareAndSwap(true, false) {
			close(entered)
			<-release
		}
		return time.Now()
	}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := owned.Close(); err != nil {
			t.Error(err)
		}
	})
	tenant, err := owned.ForTeam(t.Context(), store.DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}
	state, err := NewState(Binding{Store: owned, Tenant: tenant})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := state.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	hold.Store(true)
	finished := make(chan error, 1)
	go func() { _, err := owned.AsOperator().MaintainConcurrency(t.Context()); finished <- err }()
	defer func() {
		close(release)
		if err := <-finished; err != nil {
			t.Error(err)
		}
	}()
	<-entered
	if err := state.Ready(); err != nil {
		t.Fatal(err)
	}
}
