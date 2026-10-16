package store_test

import (
	"context"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

// Rows written before the zero start time was substituted read back as
// an unset time rather than a 1754 date, the way created_at already does.
func TestScanRun_UndefinedStartedAtReadsBackUnset(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()

	if err := teststore.ForTeam(t, s, store.DefaultTeam).TestOnlySeedRun(ctx, store.Run{ID: "unstarted", Pipeline: "demo", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	got, err := teststore.ForTeam(t, s, store.DefaultTeam).GetRun(ctx, "unstarted")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if !got.StartedAt.IsZero() {
		t.Fatalf("StartedAt = %s, want the zero time", got.StartedAt)
	}
}
