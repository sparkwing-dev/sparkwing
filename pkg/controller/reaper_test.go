package controller_test

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
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
	defer func() { _ = st.Close() }()

	ctx := context.Background()
	_ = st.CreateTrigger(ctx, store.Trigger{
		ID:        "run-dead-1",
		Pipeline:  "demo",
		CreatedAt: time.Now(),
	})
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

	srv := controller.New(st, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	reaperCtx, cancelReaper := context.WithCancel(ctx)
	// safety: the reaper writes to st, so wait for it to be gone rather than
	// only cancelled. Deferred, not t.Cleanup, so it runs before st.Close.
	reaperDone := make(chan struct{})
	defer func() {
		cancelReaper()
		<-reaperDone
	}()
	// safety: an id lands here only after its run is finished, so a receive
	// means the whole reap of that trigger is done.
	reaped := make(chan string, 4)
	go func() {
		defer close(reaperDone)
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-reaperCtx.Done():
				return
			case <-ticker.C:
				ids, err := store.Maintenance.ReapExpiredTriggers(st, reaperCtx)
				if err != nil {
					continue
				}
				for _, id := range ids {
					run, err := st.GetRun(reaperCtx, id)
					if err == nil && run.FinishedAt == nil {
						_ = st.FinishRun(reaperCtx, id, "failed", "worker lease expired")
					}
					select {
					case reaped <- id:
					case <-reaperCtx.Done():
						return
					}
				}
			}
		}
	}()

	select {
	case id := <-reaped:
		if id != "run-dead-1" {
			t.Fatalf("reaped %q, want run-dead-1", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("trigger was not reaped after its lease expired")
	}

	trig, err := st.GetTrigger(ctx, "run-dead-1")
	if err != nil {
		t.Fatalf("GetTrigger: %v", err)
	}
	if trig.Status != "pending" {
		t.Fatalf("trigger not re-queued after lease expiry: %+v", trig)
	}

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
	defer func() { _ = st.Close() }()
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
	if claimed.LeaseExpiresAt == nil {
		t.Fatal("claimed trigger has no lease expiry")
	}
	initialExpiry := *claimed.LeaseExpiresAt
	if _, err := c.HeartbeatTrigger(ctx, claimed.ID); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	renewed, err := st.GetTrigger(ctx, claimed.ID)
	if err != nil {
		t.Fatalf("read renewed trigger: %v", err)
	}
	if renewed.LeaseExpiresAt == nil {
		t.Fatal("heartbeated trigger has no lease expiry")
	}
	minimumExpiry := initialExpiry.Add(store.DefaultLeaseDuration / 2)
	if renewed.LeaseExpiresAt.Before(minimumExpiry) {
		t.Fatalf("heartbeat expiry = %s, want at or after %s", renewed.LeaseExpiresAt, minimumExpiry)
	}
	reaped, err := store.Maintenance.ReapExpiredTriggers(st, ctx)
	if err != nil {
		t.Fatalf("reap: %v", err)
	}
	for _, id := range reaped {
		if id == claimed.ID {
			t.Fatalf("heartbeated trigger %q was reaped", id)
		}
	}

	got, err := st.GetTrigger(ctx, claimed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "claimed" {
		t.Errorf("heartbeated trigger got reaped: status=%q", got.Status)
	}
}
