package store_test

import (
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

func TestDuplicateTriggerIDIsNotIdempotencyConflict(t *testing.T) {
	for _, key := range []string{"", "new-key"} {
		t.Run(key, func(t *testing.T) {
			s := storetest.New(t).Open(t)
			first := store.Trigger{ID: "same-id", Pipeline: "test", CreatedAt: time.Now()}
			if err := teststore.ForTeam(t, s, store.DefaultTeam).CreateTrigger(t.Context(), first); err != nil {
				t.Fatal(err)
			}
			first.IdempotencyKey = key
			err := teststore.ForTeam(t, s, store.DefaultTeam).CreateTrigger(t.Context(), first)
			if err == nil || errors.Is(err, store.ErrDuplicateIdempotencyKey) {
				t.Fatalf("duplicate primary id misclassified: %v", err)
			}
		})
	}
}
