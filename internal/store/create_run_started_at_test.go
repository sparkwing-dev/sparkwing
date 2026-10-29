package store_test

import (
	"context"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/runstate"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestScanRun_UndefinedStartedAtReadsBackUnset(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()

	if err := teststore.ForTeam(t, s, store.DefaultTeam).TestOnlySeedRun(ctx, store.Run{RunSummary: runstate.RunSummary{ID: "unstarted", Pipeline: "demo", Status: "pending"}}); err != nil {
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
