package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func TestFailedRunClosesTriggerAsFailed(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	const id = "failed-pipeline"
	if err := st.CreateTrigger(ctx, store.Trigger{ID: id, Pipeline: "missing", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	claimed, err := st.ClaimNextTrigger(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateRun(ctx, store.Run{ID: id, Pipeline: "missing", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	const reason = "pipeline missing is not defined in acme/web@abc; defined: build"
	if err := st.FinishRun(ctx, id, "failed", reason); err != nil {
		t.Fatal(err)
	}
	if ok, err := st.FinishTriggerAtGeneration(ctx, id, claimed.ClaimSeq); err != nil || !ok {
		t.Fatalf("FinishTriggerAtGeneration = %t, %v", ok, err)
	}
	got, err := st.GetTrigger(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "failed" || got.Error != reason || !got.IsFinished() {
		t.Fatalf("failed trigger = %+v", got)
	}
	listed, err := st.ListTriggers(ctx, store.TriggerFilter{Statuses: []string{"failed"}})
	if err != nil || len(listed) != 1 || listed[0].Error != reason {
		t.Fatalf("failed trigger listing = %+v, %v", listed, err)
	}
}
