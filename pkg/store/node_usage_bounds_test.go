package store_test

import (
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func TestAddNodeUsageRejectsOverflowAtomically(t *testing.T) {
	for _, test := range []struct {
		name      string
		initial   store.NodeUsage
		increment store.NodeUsage
	}{
		{"CPU", store.NodeUsage{CPUTime: time.Duration(math.MaxInt64), Wall: time.Second, MaxRSSBytes: 4096}, store.NodeUsage{CPUTime: 1, Wall: time.Second, MaxRSSBytes: 8192}},
		{"wall", store.NodeUsage{CPUTime: time.Second, Wall: time.Duration(math.MaxInt64), MaxRSSBytes: 4096}, store.NodeUsage{CPUTime: time.Second, Wall: 1, MaxRSSBytes: 8192}},
	} {
		t.Run(test.name, func(t *testing.T) {
			st, err := storetest.New(t).TryOpen()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = st.Close() }()
			ctx := t.Context()
			if err := st.CreateRun(ctx, store.Run{ID: "overflow", Pipeline: "test", Status: "running", StartedAt: time.Now()}); err != nil {
				t.Fatal(err)
			}
			if err := st.CreateNode(ctx, store.Node{RunID: "overflow", NodeID: "node", Status: "running"}); err != nil {
				t.Fatal(err)
			}
			if err := st.AddNodeUsage(ctx, "overflow", "node", test.initial); err != nil {
				t.Fatal(err)
			}
			if err := st.AddNodeUsage(ctx, "overflow", "node", test.increment); err == nil {
				t.Error("accepted overflowing node usage")
			}
			node, err := st.GetNode(ctx, "overflow", "node")
			if err != nil {
				t.Fatal(err)
			}
			if node.CPUNanos != int64(test.initial.CPUTime) || node.ProcessWallNanos != int64(test.initial.Wall) || node.MaxRSSBytes != test.initial.MaxRSSBytes {
				t.Fatalf("rejected overflow changed totals: CPU=%d wall=%d RSS=%d", node.CPUNanos, node.ProcessWallNanos, node.MaxRSSBytes)
			}
		})
	}
}

func TestAddNodeUsageAcceptsExactIntegerBoundary(t *testing.T) {
	st, err := storetest.New(t).TryOpen()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	ctx := t.Context()
	if err := st.CreateRun(ctx, store.Run{ID: "boundary", Pipeline: "test", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "boundary", NodeID: "node", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	if err := st.AddNodeUsage(ctx, "boundary", "node", store.NodeUsage{CPUTime: time.Duration(math.MaxInt64 - 1), Wall: time.Duration(math.MaxInt64 - 1), MaxRSSBytes: 4096}); err != nil {
		t.Fatal(err)
	}
	if err := st.AddNodeUsage(ctx, "boundary", "node", store.NodeUsage{CPUTime: 1, Wall: 1, MaxRSSBytes: 8192}); err != nil {
		t.Fatal(err)
	}
	node, err := st.GetNode(ctx, "boundary", "node")
	if err != nil {
		t.Fatal(err)
	}
	if node.CPUNanos != math.MaxInt64 || node.ProcessWallNanos != math.MaxInt64 || node.MaxRSSBytes != 8192 {
		t.Fatalf("exact boundary changed: CPU=%d wall=%d RSS=%d", node.CPUNanos, node.ProcessWallNanos, node.MaxRSSBytes)
	}
}

func TestAddNodeUsageConcurrentWritersRespectBoundary(t *testing.T) {
	target := storetest.New(t)
	first, second := target.Open(t), target.Open(t)
	ctx := t.Context()
	seedRunAndNode(t, first, "concurrent", "node")
	if err := first.AddNodeUsage(ctx, "concurrent", "node", store.NodeUsage{CPUTime: time.Duration(math.MaxInt64 - 1), Wall: time.Duration(math.MaxInt64 - 1)}); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, writer := range []*store.Store{first, second} {
		go func() {
			<-start
			results <- writer.AddNodeUsage(ctx, "concurrent", "node", store.NodeUsage{CPUTime: 1, Wall: 1, MaxRSSBytes: 8192})
		}()
	}
	close(start)
	successes := 0
	for range 2 {
		if err := <-results; err == nil {
			successes++
		} else if !strings.Contains(err.Error(), "overflow") {
			t.Errorf("losing writer: %v, want overflow", err)
		}
	}
	if successes != 1 {
		t.Errorf("successful additions=%d, want 1", successes)
	}
	node, err := first.GetNode(ctx, "concurrent", "node")
	if err != nil {
		t.Fatal(err)
	}
	if node.CPUNanos != math.MaxInt64 || node.ProcessWallNanos != math.MaxInt64 || node.MaxRSSBytes != 8192 {
		t.Fatalf("concurrent boundary totals: CPU=%d wall=%d RSS=%d", node.CPUNanos, node.ProcessWallNanos, node.MaxRSSBytes)
	}
}

func TestAddNodeUsagePreservesClaimFenceAndMissingNode(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	usage := store.NodeUsage{CPUTime: time.Second, Wall: 2 * time.Second, MaxRSSBytes: 4096}
	if err := st.AddNodeUsage(ctx, "missing", "missing", usage); err != nil {
		t.Fatalf("unfenced missing node: %v", err)
	}
	missing := store.WithNodeClaimFence(ctx, store.NodeClaimFence{})
	if err := st.AddNodeUsage(missing, "missing", "missing", usage); !errors.Is(err, store.ErrLockHeld) {
		t.Fatalf("fenced missing node: %v", err)
	}
	seedRunAndNode(t, st, "fenced", "node")
	if err := st.MarkNodeReady(ctx, "fenced", "node"); err != nil {
		t.Fatal(err)
	}
	claimant := store.ClaimIdentity{Principal: "runner", TokenPrefix: "runner-token"}
	node, err := st.ClaimNextReadyNode(ctx, claimant, "holder", time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	if node == nil {
		t.Fatal("node was not claimed")
	}
	fence := store.NodeClaimFence{Claimant: claimant, HolderID: node.ClaimedBy, MembershipID: node.ClaimMembershipID, ReservationID: node.ReservationID, ClaimGeneration: node.ClaimGeneration}
	if err := st.AddNodeUsage(store.WithNodeClaimFence(ctx, fence), "fenced", "node", usage); err != nil {
		t.Fatal(err)
	}
	fence.ClaimGeneration++
	if err := st.AddNodeUsage(store.WithNodeClaimFence(ctx, fence), "fenced", "node", usage); !errors.Is(err, store.ErrLockHeld) {
		t.Fatalf("wrong generation: %v", err)
	}
	node, err = st.GetNode(ctx, "fenced", "node")
	if err != nil {
		t.Fatal(err)
	}
	if node.CPUNanos != int64(usage.CPUTime) || node.ProcessWallNanos != int64(usage.Wall) || node.MaxRSSBytes != usage.MaxRSSBytes {
		t.Fatalf("rejected claim changed totals: CPU=%d wall=%d RSS=%d", node.CPUNanos, node.ProcessWallNanos, node.MaxRSSBytes)
	}
}

func TestAddNodeUsageRejectsNegativeStoredTotals(t *testing.T) {
	for _, column := range []string{"cpu_nanos", "process_wall_nanos"} {
		t.Run(column, func(t *testing.T) {
			st := storetest.Open(t)
			ctx := t.Context()
			seedRunAndNode(t, st, "corrupt", "node")
			if _, err := st.DB().ExecContext(ctx, "UPDATE nodes SET "+column+" = -1"); err != nil {
				t.Fatal(err)
			}
			before, err := st.GetNode(ctx, "corrupt", "node")
			if err != nil {
				t.Fatal(err)
			}
			if err := st.AddNodeUsage(ctx, "corrupt", "node", store.NodeUsage{CPUTime: 1, Wall: 1, MaxRSSBytes: 8192}); err == nil {
				t.Error("accepted negative stored total")
			}
			after, err := st.GetNode(ctx, "corrupt", "node")
			if err != nil {
				t.Fatal(err)
			}
			if after.CPUNanos != before.CPUNanos || after.ProcessWallNanos != before.ProcessWallNanos || after.MaxRSSBytes != before.MaxRSSBytes {
				t.Fatal("rejected observation changed stored usage")
			}
		})
	}
}

func TestAddNodeUsagePreservesTriggerFence(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	identity := store.ClaimIdentity{Principal: "runner", TokenPrefix: "runner-token"}
	if err := st.CreateTrigger(ctx, store.Trigger{ID: "trigger", Pipeline: "test", Status: "pending", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	trigger, err := st.ClaimNextTriggerFor(ctx, identity, time.Minute, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if trigger == nil {
		t.Fatal("trigger was not claimed")
	}
	seedRunAndNode(t, st, "trigger", "node")
	fence := store.TriggerClaimFence{Claimant: identity, ClaimGeneration: trigger.ClaimSeq}
	fenced := store.WithTriggerClaimFence(ctx, fence)
	usage := store.NodeUsage{CPUTime: 1, Wall: 2, MaxRSSBytes: 4096}
	if err := st.AddNodeUsage(fenced, "trigger", "node", usage); err != nil {
		t.Fatal(err)
	}
	if err := st.AddNodeUsage(fenced, "trigger", "missing", usage); !errors.Is(err, store.ErrLockHeld) {
		t.Fatalf("missing node: %v", err)
	}
	fence.ClaimGeneration++
	if err := st.AddNodeUsage(store.WithTriggerClaimFence(ctx, fence), "trigger", "node", usage); !errors.Is(err, store.ErrLockHeld) {
		t.Fatalf("wrong generation: %v", err)
	}
	node, err := st.GetNode(ctx, "trigger", "node")
	if err != nil {
		t.Fatal(err)
	}
	if node.CPUNanos != 1 || node.ProcessWallNanos != 2 || node.MaxRSSBytes != 4096 {
		t.Fatalf("rejected claim changed usage: %d/%d/%d", node.CPUNanos, node.ProcessWallNanos, node.MaxRSSBytes)
	}
}
