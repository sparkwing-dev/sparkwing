package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	goruntime "runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/sparkwingruntime"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type nodeSpawnOut struct {
	Findings int `json:"findings"`
}

type nodeSpawnOKChild struct {
	sparkwing.Base
	sparkwing.Produces[nodeSpawnOut]
}

func (nodeSpawnOKChild) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(w, "scan", func(ctx context.Context) (nodeSpawnOut, error) {
		return nodeSpawnOut{Findings: 3}, nil
	}), nil
}

type nodeSpawnBlockingChild struct {
	sparkwing.Base
	started chan struct{}
}

func (j *nodeSpawnBlockingChild) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	sparkwing.Step(w, "wait", func(ctx context.Context) error {
		close(j.started)
		<-ctx.Done()
		return ctx.Err()
	})
	return nil, nil
}

type nodeSpawnNestingChild struct{ sparkwing.Base }

func (nodeSpawnNestingChild) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	sparkwing.JobSpawn(w, "deep", nodeSpawnOKChild{})
	return nil, nil
}

type nodeSpawnConcurrencyChild struct {
	sparkwing.Base
	live *atomic.Int32
	peak *atomic.Int32
	ran  *atomic.Int32
}

func (j *nodeSpawnConcurrencyChild) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	sparkwing.Step(w, "hold", func(ctx context.Context) error {
		now := j.live.Add(1)
		defer j.live.Add(-1)
		j.ran.Add(1)
		for {
			was := j.peak.Load()
			if now <= was || j.peak.CompareAndSwap(was, now) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		return nil
	})
	return nil, nil
}

func nodeSpawnFixture(t *testing.T, runID string, requires []string) (*store.Store, *nodeSpawnHandler) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("SPARKWING_HOME", home)
	paths := PathsAt(home)
	if err := paths.EnsureRoot(); err != nil {
		t.Fatalf("ensure root: %v", err)
	}
	st, err := store.Open(paths.StateDB())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	ctx := context.Background()
	if err := st.CreateRun(ctx, store.Run{
		ID: runID, Pipeline: "spawn-unit", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: runID, NodeID: "parent", Status: "pending"}); err != nil {
		t.Fatalf("create parent node: %v", err)
	}

	backends := LocalBackends(paths, st, nil)
	r := NewNodeExecutor(backends)
	return st, newNodeSpawnHandler(r, backends, sparkwing.NewPlan(), runID, "spawn-unit", "parent", nil, requires)
}

func TestNodeSpawnHandler_WritesTheChildRowAndReturnsItsOutput(t *testing.T) {
	st, h := nodeSpawnFixture(t, "spawn-unit-ok", nil)
	ctx := context.Background()

	out, err := h.Spawn(ctx, "parent", "scan", nodeSpawnOKChild{})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	raw, ok := out.([]byte)
	if !ok {
		t.Fatalf("Spawn returned %#v, want the child's output as JSON", out)
	}
	var got nodeSpawnOut
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal spawn output %s: %v", raw, err)
	}
	if got.Findings != 3 {
		t.Fatalf("Spawn returned %s, want the child's output", raw)
	}

	child, err := st.GetNode(ctx, "spawn-unit-ok", "parent/scan")
	if err != nil {
		t.Fatalf("get child row: %v", err)
	}
	if child.Outcome != string(sparkwing.Success) {
		t.Errorf("child outcome = %q, want success", child.Outcome)
	}
	if string(child.Output) != `{"findings":3}` {
		t.Errorf("child output = %s, want the marshaled typed output", child.Output)
	}
}

func TestNodeSpawnHandler_StampsPipelineRequiresOnTheChildRow(t *testing.T) {
	st, h := nodeSpawnFixture(t, "spawn-unit-labels", []string{"gpu"})
	ctx := context.Background()

	if _, err := h.Spawn(ctx, "parent", "scan", nodeSpawnOKChild{}); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	child, err := st.GetNode(ctx, "spawn-unit-labels", "parent/scan")
	if err != nil {
		t.Fatalf("get child row: %v", err)
	}
	if len(child.NeedsLabels) != 1 || child.NeedsLabels[0] != "gpu" {
		t.Errorf("child needs_labels = %v, want [gpu]", child.NeedsLabels)
	}
}

func TestNodeSpawnHandler_NestsGrandchildrenUnderTheirSpawner(t *testing.T) {
	st, h := nodeSpawnFixture(t, "spawn-unit-nested", nil)
	ctx := sparkwingruntime.WithSpawnHandler(context.Background(), h)

	if _, err := h.Spawn(ctx, "parent", "mid", nodeSpawnNestingChild{}); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	grandchild, err := st.GetNode(context.Background(), "spawn-unit-nested", "parent/mid/deep")
	if err != nil {
		t.Fatalf("get grandchild row: %v", err)
	}
	if grandchild.Outcome != string(sparkwing.Success) {
		t.Errorf("grandchild outcome = %q, want success", grandchild.Outcome)
	}
}

func TestNodeSpawnHandler_BoundsFanOutConcurrency(t *testing.T) {
	_, h := nodeSpawnFixture(t, "spawn-unit-fanout", nil)
	ctx := context.Background()

	limit := goruntime.NumCPU()
	children := 2*limit + 4
	var live, peak, ran atomic.Int32

	var wg sync.WaitGroup
	for i := range children {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := h.Spawn(ctx, "parent", fmt.Sprintf("shard-%d", i),
				&nodeSpawnConcurrencyChild{live: &live, peak: &peak, ran: &ran})
			if err != nil {
				t.Errorf("spawn shard-%d: %v", i, err)
			}
		}()
	}
	wg.Wait()

	if got := int(ran.Load()); got != children {
		t.Fatalf("%d children ran, want %d", got, children)
	}
	if got := int(peak.Load()); got > limit {
		t.Errorf("peak concurrency %d exceeded the %d-slot bound", got, limit)
	}
}

func TestNodeSpawnHandler_RejectsBadSpawnIDs(t *testing.T) {
	_, h := nodeSpawnFixture(t, "spawn-unit-ids", nil)
	ctx := context.Background()

	if _, err := h.Spawn(ctx, "parent", "", nodeSpawnOKChild{}); err == nil ||
		!strings.Contains(err.Error(), "non-empty spawn id") {
		t.Errorf("empty spawn id error = %v, want the non-empty-id rejection", err)
	}

	unbound := newNodeSpawnHandler(h.runner, h.backends, h.plan, h.runID, h.pipeline, "", nil, nil)
	if _, err := unbound.Spawn(ctx, "", "scan", nodeSpawnOKChild{}); err == nil ||
		!strings.Contains(err.Error(), "parent node id") {
		t.Errorf("unparented spawn error = %v, want the missing-parent rejection", err)
	}

	if _, err := h.Spawn(ctx, "parent", "scan", nodeSpawnOKChild{}); err != nil {
		t.Fatalf("first spawn: %v", err)
	}
	_, err := h.Spawn(ctx, "parent", "scan", nodeSpawnOKChild{})
	if err == nil || !strings.Contains(err.Error(), "id collision") {
		t.Errorf("duplicate spawn error = %v, want the collision rejection", err)
	}
}

func TestNodeSpawnHandler_CancelledParentClosesTheChildRow(t *testing.T) {
	st, h := nodeSpawnFixture(t, "spawn-unit-cancel", nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	child := &nodeSpawnBlockingChild{started: make(chan struct{})}
	errCh := make(chan error, 1)
	go func() {
		_, err := h.Spawn(ctx, "parent", "blocker", child)
		errCh <- err
	}()

	select {
	case <-child.started:
	case <-time.After(10 * time.Second):
		t.Fatal("spawned child never started")
	}
	cancel()

	select {
	case err := <-errCh:
		if err == nil || !strings.Contains(err.Error(), "cancelled before terminal") {
			t.Fatalf("Spawn error = %v, want the cancelled-before-terminal shape", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Spawn did not return after cancellation")
	}

	row, err := st.GetNode(context.Background(), "spawn-unit-cancel", "parent/blocker")
	if err != nil {
		t.Fatalf("get child row: %v", err)
	}
	if row.Outcome != string(sparkwing.Cancelled) {
		t.Errorf("child outcome = %q, want cancelled", row.Outcome)
	}
	events, err := st.ListEventsAfter(context.Background(), "spawn-unit-cancel", 0, 200)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	var announced bool
	for _, ev := range events {
		if ev.Kind == "node_cancelled" && ev.NodeID == "parent/blocker" {
			announced = true
		}
	}
	if !announced {
		t.Error("no node_cancelled event; a cancelled row nothing announced is invisible to readers")
	}
}
