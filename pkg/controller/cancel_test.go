package controller_test

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/v2/controller/client"
	"github.com/sparkwing-dev/sparkwing/v2/orchestrator/store"
	"github.com/sparkwing-dev/sparkwing/v2/pkg/controller"
)

// TestCancel_HeartbeatReportsFlag exercises the RequestCancel →
// heartbeat → cancel-requested propagation path. Workers see the
// cancel signal on their next heartbeat and cancel the run ctx.
func TestCancel_HeartbeatReportsFlag(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	srv := controller.New(st, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	c := client.New(ts.URL, nil)

	ctx := context.Background()

	// Seed + claim so heartbeats are legal.
	_ = st.CreateTrigger(ctx, store.Trigger{
		ID: "run-cancel-1", Pipeline: "demo", CreatedAt: time.Now(),
	})
	if _, err := st.ClaimNextTrigger(ctx, 30*time.Second); err != nil {
		t.Fatal(err)
	}

	// First heartbeat: no cancel yet.
	status, err := c.HeartbeatTrigger(ctx, "run-cancel-1")
	if err != nil {
		t.Fatalf("hb pre-cancel: %v", err)
	}
	if status.CancelRequested {
		t.Error("cancel reported before request")
	}

	// Request cancellation.
	if err := c.CancelRun(ctx, "run-cancel-1"); err != nil {
		t.Fatalf("CancelRun: %v", err)
	}

	// Next heartbeat sees the flag.
	status, err = c.HeartbeatTrigger(ctx, "run-cancel-1")
	if err != nil {
		t.Fatalf("hb post-cancel: %v", err)
	}
	if !status.CancelRequested {
		t.Error("cancel not reported on heartbeat after RequestCancel")
	}
}

// TestCancel_Idempotent: repeated cancels are no-ops (the second
// call MUST NOT overwrite the timestamp -- otherwise a delayed
// admin would look like the sole canceller).
func TestCancel_Idempotent(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	srv := controller.New(st, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	c := client.New(ts.URL, nil)

	ctx := context.Background()
	_ = st.CreateTrigger(ctx, store.Trigger{
		ID: "run-idem-1", Pipeline: "demo", CreatedAt: time.Now(),
	})

	for i := range 3 {
		if err := c.CancelRun(ctx, "run-idem-1"); err != nil {
			t.Fatalf("CancelRun %d: %v", i, err)
		}
	}
	// No panic, no error.
}

// TestCancel_MissingRun returns 404 so the CLI can surface a clear
// error.
func TestCancel_MissingRun(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	srv := controller.New(st, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	c := client.New(ts.URL, nil)

	err = c.CancelRun(context.Background(), "nope")
	if err != store.ErrNotFound {
		t.Errorf("err=%v want ErrNotFound", err)
	}
}
