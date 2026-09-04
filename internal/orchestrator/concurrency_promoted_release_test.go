package orchestrator

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestWaitThenRunReleasesPromotedSlotWhenWorkerSlotReacquireFails(t *testing.T) {
	const (
		key      = "g:promoted-release"
		runID    = "run-promoted-release"
		nodeID   = "node"
		holderID = runID + "/" + nodeID
	)

	home := t.TempDir()
	paths := PathsAt(home)
	if err := paths.EnsureRoot(); err != nil {
		t.Fatalf("ensure root: %v", err)
	}
	st, err := store.Open(paths.StateDB())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = st.Close() }()

	ctx := context.Background()
	if err := st.CreateRun(ctx, store.Run{
		ID:        runID,
		Pipeline:  "test",
		Status:    "running",
		StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: runID, NodeID: nodeID, Status: "pending"}); err != nil {
		t.Fatalf("create node: %v", err)
	}

	occupied, err := st.AcquireConcurrencySlot(ctx, store.AcquireSlotRequest{
		Key:      key,
		HolderID: "external-holder/-",
		RunID:    "external-holder",
		Capacity: 1,
		Policy:   store.OnLimitQueue,
	})
	if err != nil {
		t.Fatalf("external acquire: %v", err)
	}
	if occupied.Kind != store.AcquireGranted {
		t.Fatalf("external acquire = %s, want granted", occupied.Kind)
	}

	queued, err := st.AcquireConcurrencySlot(ctx, store.AcquireSlotRequest{
		Key:      key,
		HolderID: holderID,
		RunID:    runID,
		NodeID:   nodeID,
		Capacity: 1,
		Policy:   store.OnLimitQueue,
	})
	if err != nil {
		t.Fatalf("queued acquire: %v", err)
	}
	if queued.Kind != store.AcquireQueued {
		t.Fatalf("queued acquire = %s, want queued", queued.Kind)
	}

	if _, _, _, err := st.ReleaseAndNotify(ctx, key, "external-holder/-", "success", "", "", 0,
		store.DefaultConcurrencyLease); err != nil {
		t.Fatalf("release external holder: %v", err)
	}

	plan := sparkwing.NewPlan()
	node := sparkwing.Job(plan, nodeID, func(context.Context) error { return nil })
	req := runner.Request{
		RunID:               runID,
		NodeID:              nodeID,
		Pipeline:            "test",
		Node:                node,
		ReacquireWorkerSlot: func() bool { return false },
	}
	cp := coordParams{key: key, capacity: 1, cost: 1, policy: store.OnLimitQueue}

	r := &NodeExecutor{backends: LocalBackends(paths, st, nil)}
	res := r.waitThenRun(ctx, req, cp, queued, time.Minute)
	if res.Outcome != sparkwing.Cancelled {
		t.Fatalf("waitThenRun outcome = %q, want cancelled", res.Outcome)
	}

	state, err := st.GetConcurrencyState(ctx, key)
	if err != nil {
		t.Fatalf("read concurrency state: %v", err)
	}
	now := time.Now()
	for _, h := range state.Holders {
		if h.Superseded || !h.LeaseExpiresAt.After(now) {
			continue
		}
		t.Fatalf("promoted holder still occupies the slot after a cancelled reacquire: %+v", h)
	}

	next, err := st.AcquireConcurrencySlot(ctx, store.AcquireSlotRequest{
		Key:      key,
		HolderID: "next-holder/-",
		RunID:    "next-holder",
		Capacity: 1,
		Policy:   store.OnLimitQueue,
	})
	if err != nil {
		t.Fatalf("follow-on acquire: %v", err)
	}
	if next.Kind != store.AcquireGranted {
		t.Fatalf("follow-on acquire = %s, want granted: the freed capacity is withheld", next.Kind)
	}
}
