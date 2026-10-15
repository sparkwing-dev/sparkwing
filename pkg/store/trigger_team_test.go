package store_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

// A trigger's idempotency key and signed replay digest are
// unique within its team, so one team holding a value neither refuses another
// team the same value nor tells it the value is taken. A repeat inside one
// team is still refused.
func TestCreateTriggerKeysReplayAndIdempotencyPerTeam(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	if err := st.AsOperator().CreateTeam(ctx, "team-b"); err != nil {
		t.Fatal(err)
	}
	operator, err := st.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}
	tenantB, err := st.ForTeam(ctx, "team-b")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		set  func(*store.Trigger)
		want error
	}{
		{"idempotency key", func(tr *store.Trigger) { tr.IdempotencyKey = "k-1" }, store.ErrDuplicateIdempotencyKey},
		{"replay key", func(tr *store.Trigger) { tr.WebhookReplayKey = "r-1" }, store.ErrDuplicateWebhookDelivery},
	} {
		t.Run(tc.name, func(t *testing.T) {
			trig := func(id string) store.Trigger {
				tr := store.Trigger{ID: id, Pipeline: "build", CreatedAt: time.Now()}
				tc.set(&tr)
				return tr
			}
			prefix := strings.ReplaceAll(tc.name, " ", "-")
			if err := operator.CreateTrigger(ctx, trig(prefix+"-a")); err != nil {
				t.Fatal(err)
			}
			if err := tenantB.CreateTrigger(ctx, trig(prefix+"-b")); err != nil {
				t.Fatalf("team B reusing the operator's %s = %v, want accepted", tc.name, err)
			}
			if err := tenantB.CreateTrigger(ctx, trig(prefix+"-c")); !errors.Is(err, tc.want) {
				t.Fatalf("team B repeating its own %s = %v, want %v", tc.name, err, tc.want)
			}
		})
	}
}
