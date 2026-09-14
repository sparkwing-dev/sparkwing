package controller_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func placementStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func seedReadyNode(t *testing.T, st *store.Store, runID, nodeID string, prefers []string) {
	t.Helper()
	ctx := context.Background()
	if err := st.CreateRun(ctx, store.Run{
		ID: runID, Pipeline: "demo", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := st.CreateNode(ctx, store.Node{
		RunID: runID, NodeID: nodeID, Status: "pending", PrefersLabels: prefers,
	}); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	if err := st.MarkNodeReady(ctx, runID, nodeID); err != nil {
		t.Fatalf("MarkNodeReady: %v", err)
	}
}

// safety: a claim-mode runner counts toward a hold only once this controller
// has handed it a node, so every test here lets the laptop win one first.
func proveLocalRunner(t *testing.T, st *store.Store, c *client.Client, slots int) {
	t.Helper()
	seedReadyNode(t, st, "run-proof", "warmup", nil)
	claimed, err := c.ClaimNodeWithCapacity(context.Background(), "runner:laptop:0",
		[]string{"location=local"}, time.Minute, nil,
		&client.ClaimCapacity{MaxConcurrent: slots, ActiveClaims: 0})
	if err != nil || claimed == nil {
		t.Fatalf("laptop warmup claim = %+v, %v", claimed, err)
	}
}

func TestPlacement_CloudRunnerWaitsOutTheHoldWhileALocalRunnerPolls(t *testing.T) {
	st := placementStore(t)
	srv := httptest.NewServer(controller.New(st, nil).
		WithLocalFirstPlacement(nil, time.Minute, time.Minute).Handler())
	defer srv.Close()
	c := client.New(srv.URL, nil)
	ctx := context.Background()
	local := &client.ClaimCapacity{MaxConcurrent: 4, ActiveClaims: 1}
	proveLocalRunner(t, st, c, 4)

	if _, err := c.ClaimNodeWithCapacity(ctx, "runner:laptop:1", []string{"location=local"}, time.Minute, nil, local); err != nil {
		t.Fatalf("local claim: %v", err)
	}
	seedReadyNode(t, st, "run-2", "node-b", []string{"location=local"})

	held, err := c.ClaimNode(ctx, "runner:cloudpod:1", []string{"location=cloud"}, time.Minute, nil)
	if err != nil {
		t.Fatalf("cloud claim during the hold: %v", err)
	}
	if held != nil {
		t.Fatalf("cloud runner took %s/%s during the hold", held.RunID, held.NodeID)
	}

	claimed, err := c.ClaimNodeWithCapacity(ctx, "runner:laptop:2", []string{"location=local"}, time.Minute, nil, local)
	if err != nil {
		t.Fatalf("local claim of the held node: %v", err)
	}
	if claimed == nil || claimed.NodeID != "node-b" {
		t.Fatalf("local claim returned %+v", claimed)
	}
	if claimed.PlacementReason != store.PlacementPreferred {
		t.Fatalf("placement reason = %q, want %q", claimed.PlacementReason, store.PlacementPreferred)
	}
}

func TestPlacement_CloudRunnerTakesTheNodeAfterTheHold(t *testing.T) {
	st := placementStore(t)
	srv := httptest.NewServer(controller.New(st, nil).
		WithLocalFirstPlacement(nil, time.Minute, time.Minute).Handler())
	defer srv.Close()
	c := client.New(srv.URL, nil)
	ctx := context.Background()

	proveLocalRunner(t, st, c, 2)
	if _, err := c.ClaimNodeWithCapacity(ctx, "runner:laptop:1", []string{"location=local"}, time.Minute, nil,
		&client.ClaimCapacity{MaxConcurrent: 2}); err != nil {
		t.Fatalf("local poll: %v", err)
	}
	seedReadyNode(t, st, "run-1", "node-a", []string{"location=local"})
	// safety: the hold runs from the node's hold-from stamp, so moving the
	// stamp back is the whole hold elapsing without the test waiting for it.
	res, err := st.DB().ExecContext(ctx,
		`UPDATE nodes SET placement_hold_from = ? WHERE run_id = ? AND node_id = ?`,
		time.Now().Add(-2*time.Minute).UnixNano(), "run-1", "node-a")
	if err != nil {
		t.Fatalf("age the placement hold: %v", err)
	}
	if aged, aerr := res.RowsAffected(); aerr != nil || aged != 1 {
		t.Fatalf("aged rows = %d (err %v), want 1", aged, aerr)
	}

	claimed, err := c.ClaimNode(ctx, "runner:cloudpod:1", []string{"location=cloud"}, time.Minute, nil)
	if err != nil {
		t.Fatalf("cloud claim after the hold: %v", err)
	}
	if claimed == nil || claimed.NodeID != "node-a" {
		t.Fatalf("cloud claim returned %+v", claimed)
	}
	if claimed.PlacementReason != store.PlacementFallback {
		t.Fatalf("placement reason = %q, want %q", claimed.PlacementReason, store.PlacementFallback)
	}
}

func TestPlacement_SaturatedLocalRunnerHoldsNothing(t *testing.T) {
	st := placementStore(t)
	srv := httptest.NewServer(controller.New(st, nil).
		WithLocalFirstPlacement(nil, time.Minute, time.Minute).Handler())
	defer srv.Close()
	c := client.New(srv.URL, nil)
	ctx := context.Background()

	proveLocalRunner(t, st, c, 1)
	if _, err := c.ClaimNodeWithCapacity(ctx, "runner:laptop:1", []string{"location=local"}, time.Minute, nil,
		&client.ClaimCapacity{MaxConcurrent: 1, ActiveClaims: 1}); err != nil {
		t.Fatalf("local poll: %v", err)
	}
	seedReadyNode(t, st, "run-1", "node-a", []string{"location=local"})

	claimed, err := c.ClaimNode(ctx, "runner:cloudpod:1", []string{"location=cloud"}, time.Minute, nil)
	if err != nil {
		t.Fatalf("cloud claim: %v", err)
	}
	if claimed == nil || claimed.PlacementReason != store.PlacementFallback {
		t.Fatalf("cloud claim returned %+v", claimed)
	}
}

func TestPlacement_NodeWithoutAPreferenceIsUnchanged(t *testing.T) {
	st := placementStore(t)
	srv := httptest.NewServer(controller.New(st, nil).
		WithLocalFirstPlacement(nil, time.Minute, time.Minute).Handler())
	defer srv.Close()
	c := client.New(srv.URL, nil)
	ctx := context.Background()

	proveLocalRunner(t, st, c, 2)
	if _, err := c.ClaimNodeWithCapacity(ctx, "runner:laptop:1", []string{"location=local"}, time.Minute, nil,
		&client.ClaimCapacity{MaxConcurrent: 2}); err != nil {
		t.Fatalf("local poll: %v", err)
	}
	seedReadyNode(t, st, "run-1", "node-a", nil)

	claimed, err := c.ClaimNode(ctx, "runner:cloudpod:1", nil, time.Minute, nil)
	if err != nil {
		t.Fatalf("cloud claim: %v", err)
	}
	if claimed == nil || claimed.NodeID != "node-a" {
		t.Fatalf("cloud claim returned %+v", claimed)
	}
	if claimed.PlacementReason != store.PlacementNone {
		t.Fatalf("placement reason = %q, want %q", claimed.PlacementReason, store.PlacementNone)
	}
}

func TestPlacement_ControllerDefaultPrefersHoldsANodeWithoutPrefers(t *testing.T) {
	st := placementStore(t)
	srv := httptest.NewServer(controller.New(st, nil).
		WithLocalFirstPlacement([]string{"location=local"}, time.Minute, time.Minute).Handler())
	defer srv.Close()
	c := client.New(srv.URL, nil)
	ctx := context.Background()

	proveLocalRunner(t, st, c, 2)
	if _, err := c.ClaimNodeWithCapacity(ctx, "runner:laptop:1", []string{"location=local"}, time.Minute, nil,
		&client.ClaimCapacity{MaxConcurrent: 2}); err != nil {
		t.Fatalf("local poll: %v", err)
	}
	seedReadyNode(t, st, "run-1", "node-a", nil)

	held, err := c.ClaimNode(ctx, "runner:cloudpod:1", []string{"location=cloud"}, time.Minute, nil)
	if err != nil {
		t.Fatalf("cloud claim: %v", err)
	}
	if held != nil {
		t.Fatalf("cloud runner took %s under the controller default", held.NodeID)
	}
}

func TestPlacement_LivenessWindowForgetsASilentRunner(t *testing.T) {
	st := placementStore(t)
	srv := httptest.NewServer(controller.New(st, nil).
		WithLocalFirstPlacement(nil, time.Minute, time.Millisecond).Handler())
	defer srv.Close()
	c := client.New(srv.URL, nil)
	ctx := context.Background()

	proveLocalRunner(t, st, c, 2)
	if _, err := c.ClaimNodeWithCapacity(ctx, "runner:laptop:1", []string{"location=local"}, time.Minute, nil,
		&client.ClaimCapacity{MaxConcurrent: 2}); err != nil {
		t.Fatalf("local poll: %v", err)
	}
	seedReadyNode(t, st, "run-1", "node-a", []string{"location=local"})
	time.Sleep(5 * time.Millisecond)

	claimed, err := c.ClaimNode(ctx, "runner:cloudpod:1", []string{"location=cloud"}, time.Minute, nil)
	if err != nil {
		t.Fatalf("cloud claim: %v", err)
	}
	if claimed == nil || claimed.PlacementReason != store.PlacementFallback {
		t.Fatalf("cloud claim returned %+v", claimed)
	}
}

func TestPlacement_AgentsViewCarriesSelfAssertedRunnerLabels(t *testing.T) {
	st := placementStore(t)
	seedReadyNode(t, st, "run-1", "node-a", nil)
	srv := httptest.NewServer(controller.New(st, nil).
		WithLocalFirstPlacement(nil, time.Minute, time.Minute).Handler())
	defer srv.Close()
	c := client.New(srv.URL, nil)
	ctx := context.Background()

	if _, err := c.ClaimNodeWithCapacity(ctx, "runner:laptop:1", []string{"location=local", "gpu"}, time.Minute, nil,
		&client.ClaimCapacity{MaxConcurrent: 3}); err != nil {
		t.Fatalf("local claim: %v", err)
	}

	data, err := httpGet(srv.URL + "/api/v1/agents")
	if err != nil {
		t.Fatalf("GET agents: %v", err)
	}
	var body struct {
		Agents []controller.Agent `json:"agents"`
	}
	if err := json.Unmarshal(data, &body); err != nil {
		t.Fatalf("decode agents: %v", err)
	}
	for _, agent := range body.Agents {
		if agent.Name != "laptop" {
			continue
		}
		if agent.MaxConcurrent != 3 {
			t.Fatalf("max_concurrent = %d, want 3", agent.MaxConcurrent)
		}
		if len(agent.Capabilities) != 2 || agent.Capabilities[0] != "location=local" {
			t.Fatalf("capabilities = %v", agent.Capabilities)
		}
		return
	}
	t.Fatalf("agents view lists no laptop row: %+v", body.Agents)
}

// A caller that only polls has proved nothing: a claim credential must not be
// able to reserve the queue for a runner that never executes anything.
func TestPlacement_ARunnerThatNeverClaimedHoldsNothing(t *testing.T) {
	st := placementStore(t)
	srv := httptest.NewServer(controller.New(st, nil).
		WithLocalFirstPlacement(nil, time.Minute, time.Minute).Handler())
	defer srv.Close()
	c := client.New(srv.URL, nil)
	ctx := context.Background()

	if _, err := c.ClaimNodeWithCapacity(ctx, "runner:phantom:1", []string{"location=local"}, time.Minute, nil,
		&client.ClaimCapacity{MaxConcurrent: 9999}); err != nil {
		t.Fatalf("phantom poll: %v", err)
	}
	seedReadyNode(t, st, "run-1", "node-a", []string{"location=local"})

	claimed, err := c.ClaimNode(ctx, "runner:cloudpod:1", []string{"location=cloud"}, time.Minute, nil)
	if err != nil {
		t.Fatalf("cloud claim: %v", err)
	}
	if claimed == nil {
		t.Fatal("a runner that has never claimed held the node back")
	}
}

// A runner from before the capacity field said nothing about having room, so a
// rolling upgrade must not stall the pool that can execute.
func TestPlacement_RunnerAdvertisingNoCapacityHoldsNothing(t *testing.T) {
	st := placementStore(t)
	srv := httptest.NewServer(controller.New(st, nil).
		WithLocalFirstPlacement(nil, time.Minute, time.Minute).Handler())
	defer srv.Close()
	c := client.New(srv.URL, nil)
	ctx := context.Background()

	seedReadyNode(t, st, "run-proof", "warmup", nil)
	if _, err := c.ClaimNode(ctx, "runner:laptop:0", []string{"location=local"}, time.Minute, nil); err != nil {
		t.Fatalf("laptop warmup claim: %v", err)
	}
	seedReadyNode(t, st, "run-1", "node-a", []string{"location=local"})

	claimed, err := c.ClaimNode(ctx, "runner:cloudpod:1", []string{"location=cloud"}, time.Minute, nil)
	if err != nil {
		t.Fatalf("cloud claim: %v", err)
	}
	if claimed == nil {
		t.Fatal("a runner that advertised no capacity held the node back")
	}
}

// Presence is bound to the credential that polls, so a second token cannot
// inherit the standing a first one earned under the same runner name.
func TestPlacement_PresenceIsBoundToTheClaimingCredential(t *testing.T) {
	st := placementStore(t)
	proven, _, err := st.CreateToken("proven-runner", store.TokenKindRunner,
		[]string{"nodes.claim"}, 0, time.Now().UTC())
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	other, _, err := st.CreateToken("other-runner", store.TokenKindRunner,
		[]string{"nodes.claim"}, 0, time.Now().UTC())
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	srv := httptest.NewServer(controller.New(st, nil).
		EnableAuthFromStore().
		WithLocalFirstPlacement(nil, time.Minute, time.Minute).Handler())
	defer srv.Close()
	ctx := context.Background()
	// safety: one slot, spent on the warmup node, so anything held after this is
	// held by the borrowed credential rather than the real laptop.
	laptop := client.NewWithToken(srv.URL, nil, proven)
	proveLocalRunner(t, st, laptop, 1)

	borrowed := client.NewWithToken(srv.URL, nil, other)
	if _, err := borrowed.ClaimNodeWithCapacity(ctx, "runner:laptop:1", []string{"location=local"}, time.Minute, nil,
		&client.ClaimCapacity{MaxConcurrent: 9999}); err != nil {
		t.Fatalf("borrowed poll: %v", err)
	}
	seedReadyNode(t, st, "run-1", "node-a", []string{"location=local"})

	claimed, err := client.NewWithToken(srv.URL, nil, other).
		ClaimNode(ctx, "runner:cloudpod:1", []string{"location=cloud"}, time.Minute, nil)
	if err != nil {
		t.Fatalf("cloud claim: %v", err)
	}
	if claimed == nil {
		t.Fatal("a borrowed runner name held the node back")
	}
}

func TestPlacement_RunnerAtItsCeilingHoldsNothingBack(t *testing.T) {
	st := placementStore(t)
	srv := httptest.NewServer(controller.New(st, nil).
		WithLocalFirstPlacement(nil, time.Minute, time.Minute).Handler())
	defer srv.Close()
	c := client.New(srv.URL, nil)
	ctx := context.Background()
	proveLocalRunner(t, st, c, 1)

	// safety: the warmup claim spent the laptop's only slot and it has not
	// polled since to say otherwise.
	seedReadyNode(t, st, "run-1", "node-a", []string{"location=local"})
	claimed, err := c.ClaimNode(ctx, "runner:cloudpod:1", []string{"location=cloud"}, time.Minute, nil)
	if err != nil {
		t.Fatalf("cloud claim: %v", err)
	}
	if claimed == nil {
		t.Fatal("a laptop with every slot spent held the node back")
	}
	if claimed.PlacementReason != store.PlacementFallback {
		t.Fatalf("placement reason = %q, want %q", claimed.PlacementReason, store.PlacementFallback)
	}
}
