package store_test

import (
	"context"

	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func seedPending(t *testing.T, s *store.Store, id string) {
	t.Helper()
	if err := s.CreateTrigger(context.Background(), store.Trigger{
		ID:        id,
		Pipeline:  "demo",
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("CreateTrigger: %v", err)
	}
}
