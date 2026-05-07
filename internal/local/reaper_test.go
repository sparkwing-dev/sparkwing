package local_test

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/controller/client"
	controller "github.com/sparkwing-dev/sparkwing/internal/local"
	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
)

// TestReaper_RequeuesDeadWorkerTrigger simulates a worker that
// claimed a trigger and then died without heartbeating. The reaper
// should re-queue the trigger so a fresh worker can pick it up, and
// the associated run (if one was created) should be marked failed.
func TestReaper_RequeuesDeadWorkerTrigger(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	// Seed a trigger and a half-finished run: worker got as far as
	// CreateRun but crashed before FinishRun.
	ctx := context.Background()
	_ = st.CreateTrigger(ctx, store.Trigger{
		ID:        "run-dead-1",
		Pipeline:  "demo",
		CreatedAt: time.Now(),
	})
	// Simulate worker claim with a very short lease.
	claimed, err := st.ClaimNextTrigger(ctx, 100*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	_ = st.CreateRun(ctx, store.Run{
		ID:        claimed.ID,
		Pipeline:  "demo",
		Status:    "running",
		StartedAt: time.Now(),
	})

	// Start the controller (Serve spawns the reaper). httptest.Server
	// doesn't run Serve, so we need a variant that spins the reaper
	// separately. Do it inline.
	srv := controller.New(st, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	reaperCtx, cancelReaper := context.WithCancel(ctx)
	defer cancelReaper()
	// Run the reaper directly via the store; the unit test for the
	// server's runReaper is covered by Serve integration, not here.
	go func() {
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-reaperCtx.Done():
				return
			case <-ticker.C:
				ids, err := st.ReapExpiredTriggers(reaperCtx)
				if err != nil {
					continue
				}
				for _, id := range ids {
					run, err := st.GetRun(reaperCtx, id)
					if err == nil && run.FinishedAt == nil {
						_ = st.FinishRun(reaperCtx, id, "failed", "worker lease expired")
					}
				}
			}
		}
	}()

	// Wait for lease to expire + reaper to sweep.
	deadline := time.Now().Add(2 * time.Second)
	var trig *store.Trigger
	for time.Now().Before(deadline) {
		trig, _ = st.GetTrigger(ctx, "run-dead-1")
		if trig != nil && trig.Status == "pending" {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if trig == nil || trig.Status != "pending" {
		t.Fatalf("trigger not re-queued after lease expiry: %+v", trig)
	}

	// Associated run is marked failed.
	run, err := st.GetRun(ctx, "run-dead-1")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run.Status != "failed" {
		t.Errorf("run.Status=%q want failed", run.Status)
	}
	if run.Error == "" {
		t.Error("run.Error empty; want lease-expiry message")
	}

	// A fresh worker can claim the re-queued trigger.
	c := client.New(ts.URL, nil)
	second, err := c.ClaimTrigger(ctx)
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if second == nil || second.ID != "run-dead-1" {
		t.Fatalf("second claim didn't get run-dead-1: %+v", second)
	}
}

// TestReaper_HeartbeatKeepsAlive is the happy path: a worker that
// heartbeats is not reaped.
func TestReaper_HeartbeatKeepsAlive(t *testing.T) {
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
		ID: "run-live-1", Pipeline: "demo", CreatedAt: time.Now(),
	})
	claimed, err := st.ClaimNextTrigger(ctx, 150*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}

	// Send heartbeats faster than the lease.
	hbDone := make(chan struct{})
	go func() {
		defer close(hbDone)
		for i := range 5 {
			time.Sleep(50 * time.Millisecond)
			if _, err := c.HeartbeatTrigger(ctx, claimed.ID); err != nil {
				t.Errorf("heartbeat %d: %v", i, err)
				return
			}
		}
	}()

	// Reap concurrently -- should not re-queue while heartbeats land.
	go func() {
		for range 10 {
			_, _ = st.ReapExpiredTriggers(ctx)
			time.Sleep(30 * time.Millisecond)
		}
	}()

	<-hbDone

	got, err := st.GetTrigger(ctx, claimed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "claimed" {
		t.Errorf("heartbeated trigger got reaped: status=%q", got.Status)
	}
}
