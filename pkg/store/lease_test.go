package store_test

import (
	"context"

	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

func seedPending(t *testing.T, s *store.Store, id string) {
	t.Helper()
	if err := teststore.ForTeam(t, s, store.DefaultTeam).TestOnlySeedTrigger(context.Background(), store.Trigger{
		ID:        id,
		Pipeline:  "demo",
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("CreateTrigger: %v", err)
	}
}
