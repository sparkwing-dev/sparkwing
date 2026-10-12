package store_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func openPGTestStore(t *testing.T) *store.Store {
	t.Helper()
	return storetest.OpenPostgres(t)
}

func TestPostgresOpenAndMigrate(t *testing.T) {
	st := openPGTestStore(t)
	if got, want := st.Dialect(), store.DialectPostgres; got != want {
		t.Errorf("Dialect = %v, want %v", got, want)
	}
	ctx := context.Background()
	r := store.Run{
		ID:        "pg-open-test",
		Pipeline:  "p",
		Status:    "running",
		StartedAt: time.Now(),
	}
	if err := st.CreateRun(ctx, r); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	got, err := st.GetRun(ctx, r.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got == nil || got.ID != r.ID {
		t.Fatalf("GetRun returned %+v, want id %q", got, r.ID)
	}
}

func TestPostgresReconcileOrphanedLocalRuns(t *testing.T) {
	st := openPGTestStore(t)
	ctx := context.Background()

	if err := st.CreateRun(ctx, store.Run{
		ID: "pg-orphan", Pipeline: "p", Status: "running",
		StartedAt: time.Now().Add(-10 * time.Minute),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if _, err := st.DB().ExecContext(ctx,
		`UPDATE runs SET last_heartbeat_at = NULL WHERE id = $1`, "pg-orphan"); err != nil {
		t.Fatalf("clear run heartbeat: %v", err)
	}
	if err := st.CreateNode(ctx, store.Node{
		RunID: "pg-orphan", NodeID: "build", Status: "running",
	}); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}

	n, err := store.Maintenance.ReconcileOrphanedLocalRuns(st, ctx, 60*time.Second)
	if err != nil {
		t.Fatalf("ReconcileOrphanedLocalRuns: %v", err)
	}
	if n != 1 {
		t.Fatalf("reconciled count = %d, want 1", n)
	}
	got, err := st.GetRun(ctx, "pg-orphan")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got == nil || got.Status != "failed" {
		t.Fatalf("run = %+v, want status failed", got)
	}
}

func TestSchemaV30_PostgresPersistsAttemptAndSchedulesAgentLossRetry(t *testing.T) {
	st := openPGTestStore(t)
	ctx := context.Background()
	createRetryRunAndReadyNode(t, st, "run-pg-agent-loss", 1)
	identity := store.ClaimIdentity{Principal: "agent:pg-desktop", TokenPrefix: "swr_pg_desktop"}
	claimed := claimRetryNode(t, st, "run-pg-agent-loss", identity, "agent:pg-desktop:0")
	ackNodeAttempt(t, st, claimed, identity, 1)
	if _, err := st.DB().ExecContext(ctx, `UPDATE nodes SET lease_expires_at = $1 WHERE run_id = $2 AND node_id = $3`,
		time.Now().Add(-time.Second).UnixNano(), claimed.RunID, claimed.NodeID); err != nil {
		t.Fatal(err)
	}
	recovered, err := store.Maintenance.RecoverExpiredNodeClaims(st, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(recovered) != 1 || recovered[0].RetryRunID == "" || !recovered[0].Started || recovered[0].Invocations != 1 {
		t.Fatalf("recovery = %+v", recovered)
	}
	source, err := st.GetNode(ctx, claimed.RunID, claimed.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	if source.FailureReason != store.FailureAgentLost || len(source.ExecutionAttempts) != 1 ||
		source.ExecutionAttempts[0].FinishedAt == nil || source.ExecutionAttempts[0].FailureReason != store.FailureAgentLost ||
		source.ExecutionAttempts[0].RetryRunID != recovered[0].RetryRunID {
		t.Fatalf("source attempt = %+v", source)
	}
	retry, err := st.GetRun(ctx, recovered[0].RetryRunID)
	if err != nil {
		t.Fatal(err)
	}
	if retry.Status != "pending" || retry.RetryOf != claimed.RunID || retry.RetryAvailableAt == nil || retry.RetryDeadlineAt == nil {
		t.Fatalf("retry = %+v", retry)
	}
}

func TestPostgresClaimNextReadyNode_Concurrent(t *testing.T) {
	st := openPGTestStore(t)
	ctx := context.Background()

	if err := st.CreateRun(ctx, store.Run{
		ID: "r1", Pipeline: "p", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := st.CreateNode(ctx, store.Node{
		RunID: "r1", NodeID: "n1", Status: "ready",
	}); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	if err := st.MarkNodeReady(ctx, "r1", "n1"); err != nil {
		t.Fatalf("MarkNodeReady: %v", err)
	}

	var winners int
	var losers int
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			n, err := st.ClaimNextReadyNode(ctx, store.ClaimIdentity{Principal: "runner-principal", TokenPrefix: "swr_runner-principal"}, fmt.Sprintf("h-%d", id), time.Minute, nil)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil && n != nil:
				winners++
			case errors.Is(err, store.ErrNotFound):
				losers++
			default:
				t.Errorf("unexpected: n=%v err=%v", n, err)
			}
		}(i)
	}
	wg.Wait()

	if winners != 1 {
		t.Errorf("winners = %d, want 1", winners)
	}
	if losers != 3 {
		t.Errorf("losers = %d, want 3", losers)
	}
}

func TestPostgresAcquireConcurrencySlot_Serializes(t *testing.T) {
	st := openPGTestStore(t)
	ctx := context.Background()

	const key = "shared-slot"
	const n = 5

	for i := 0; i < n; i++ {
		runID := fmt.Sprintf("r-%d", i)
		if err := st.CreateRun(ctx, store.Run{
			ID: runID, Pipeline: "p", Status: "running", StartedAt: time.Now(),
		}); err != nil {
			t.Fatalf("CreateRun %s: %v", runID, err)
		}
	}

	type result struct {
		kind store.AcquireKind
		err  error
	}
	results := make([]result, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			resp, err := st.AcquireConcurrencySlot(ctx, store.AcquireSlotRequest{
				Key:      key,
				RunID:    fmt.Sprintf("r-%d", i),
				NodeID:   "n",
				HolderID: fmt.Sprintf("h-%d", i),
				Capacity: 1,
				Policy:   store.OnLimitQueue,
				Lease:    time.Minute,
			})
			results[i] = result{kind: resp.Kind, err: err}
		}(i)
	}
	wg.Wait()

	var granted, queued int
	for _, r := range results {
		if r.err != nil {
			t.Errorf("unexpected error: %v", r.err)
			continue
		}
		switch r.kind {
		case store.AcquireGranted:
			granted++
		case store.AcquireQueued:
			queued++
		default:
			t.Errorf("unexpected acquire kind: %v", r.kind)
		}
	}
	if granted != 1 {
		t.Errorf("granted = %d, want 1", granted)
	}
	if queued != n-1 {
		t.Errorf("queued = %d, want %d", queued, n-1)
	}
}

func TestPostgresResolveWaiterPromotesOnceUnderConcurrentPoll(t *testing.T) {
	st := openPGTestStore(t)
	ctx := context.Background()

	acquireT(t, st, store.AcquireSlotRequest{
		Key: "resolve-slot", HolderID: "leader", RunID: "leader", NodeID: "n",
		Capacity: 1, Policy: store.OnLimitQueue,
	})
	for i := 0; i < 2; i++ {
		acquireT(t, st, store.AcquireSlotRequest{
			Key: "resolve-slot", HolderID: fmt.Sprintf("w-%d", i), RunID: fmt.Sprintf("waiter-%d", i), NodeID: "n",
			Capacity: 1, Policy: store.OnLimitQueue,
		})
	}
	if _, err := st.DB().ExecContext(ctx,
		`DELETE FROM concurrency_holders WHERE key = $1 AND holder_id = $2`,
		"resolve-slot", "leader"); err != nil {
		t.Fatalf("manual drop: %v", err)
	}

	type result struct {
		resolution store.WaiterResolution
		err        error
	}
	results := make([]result, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			resolution, err := st.ResolveWaiter(ctx, "resolve-slot", fmt.Sprintf("waiter-%d", i), "n", "", "", "", false)
			results[i] = result{resolution: resolution, err: err}
		}(i)
	}
	wg.Wait()

	var promoted, waiting int
	for _, result := range results {
		if result.err != nil {
			t.Fatalf("resolve error: %v", result.err)
		}
		switch result.resolution.Status {
		case store.WaiterPromoted:
			promoted++
		case store.WaiterStillWaiting:
			waiting++
		default:
			t.Fatalf("unexpected resolution: %+v", result.resolution)
		}
	}
	if promoted != 1 || waiting != 1 {
		t.Fatalf("promoted=%d waiting=%d, want promoted=1 waiting=1", promoted, waiting)
	}
	state, err := st.GetConcurrencyState(ctx, "resolve-slot")
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if len(state.Holders) != 1 || state.Holders[0].RunID != "waiter-0" {
		t.Fatalf("holders = %+v", state.Holders)
	}
	if len(state.Waiters) != 1 || state.Waiters[0].RunID != "waiter-1" {
		t.Fatalf("waiters = %+v", state.Waiters)
	}
}
