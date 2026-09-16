package controller_test

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestControllerFinishStepAcceptsCancellation(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	if err := st.CreateRun(ctx, store.Run{ID: "run-1", Pipeline: "gate", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "run-1", NodeID: "gate", Status: "pending"}); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(controller.New(st, nil).Handler())
	t.Cleanup(srv.Close)
	c := client.New(srv.URL, nil)
	if err := c.StartNodeStep(ctx, "run-1", "gate", "store-postgres"); err != nil {
		t.Fatal(err)
	}
	if err := c.FinishNodeStep(ctx, "run-1", "gate", "store-postgres", store.StepCancelled); err != nil {
		t.Fatalf("FinishNodeStep(cancelled): %v", err)
	}

	steps, err := st.ListNodeSteps(ctx, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 1 || steps[0].Status != store.StepCancelled || steps[0].FinishedAt == nil {
		t.Fatalf("steps = %+v, want one durably cancelled step", steps)
	}
}
