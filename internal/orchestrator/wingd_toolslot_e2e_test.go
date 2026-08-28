package orchestrator

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

const toolSlotPipelineName = "wingd-e2e-tool-slot"

const toolSlotNodeID = "hold-tool"

const toolSlotLintCores = 4.0

const toolSlotGrantableCores = 8.0

type toolSlotGrant struct {
	runID   string
	granted bool
}

type toolSlotContext struct {
	runID string
	ctx   context.Context
}

type toolSlotGate struct {
	budget   *sparkwing.ConcurrencyGroup
	cost     int
	grants   chan toolSlotGrant
	contexts chan toolSlotContext

	mu      sync.Mutex
	release map[string]chan struct{}
}

func newToolSlotGate(budget *sparkwing.ConcurrencyGroup, cost int) *toolSlotGate {
	return &toolSlotGate{
		budget:   budget,
		cost:     cost,
		grants:   make(chan toolSlotGrant, 8),
		contexts: make(chan toolSlotContext, 8),
		release:  map[string]chan struct{}{},
	}
}

func (g *toolSlotGate) releaseChan(runID string) chan struct{} {
	g.mu.Lock()
	defer g.mu.Unlock()
	ch, ok := g.release[runID]
	if !ok {
		ch = make(chan struct{})
		g.release[runID] = ch
	}
	return ch
}

func (g *toolSlotGate) hold(ctx context.Context, runID string) error {
	select {
	case g.contexts <- toolSlotContext{runID: runID, ctx: ctx}:
	case <-ctx.Done():
		return ctx.Err()
	}
	release, granted := sparkwing.ToolSlot(ctx, g.budget, g.cost)
	defer release()
	g.grants <- toolSlotGrant{runID: runID, granted: granted}
	select {
	case <-g.releaseChan(runID):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (g *toolSlotGate) awaitContext(t *testing.T, want string) context.Context {
	t.Helper()
	select {
	case got := <-g.contexts:
		if got.runID != want {
			t.Fatalf("tool slot context for %q arrived first, want %q", got.runID, want)
		}
		return got.ctx
	case <-time.After(wingdTestWait):
		t.Fatalf("run %q never entered its tool slot", want)
		return nil
	}
}

func (g *toolSlotGate) let(runID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	ch, ok := g.release[runID]
	if !ok {
		ch = make(chan struct{})
		g.release[runID] = ch
	}
	select {
	case <-ch:
	default:
		close(ch)
	}
}

func (g *toolSlotGate) awaitGrant(t *testing.T, want string) toolSlotGrant {
	t.Helper()
	select {
	case got := <-g.grants:
		if got.runID != want {
			t.Fatalf("tool slot answered %q first, want %q", got.runID, want)
		}
		return got
	case <-time.After(wingdTestWait):
		t.Fatalf("run %q never reached its tool slot", want)
		return toolSlotGrant{}
	}
}

func (g *toolSlotGate) expectNoGrantYet(t *testing.T) {
	t.Helper()
	select {
	case got := <-g.grants:
		t.Fatalf("run %q was answered (granted=%v) while the budget was full", got.runID, got.granted)
	default:
	}
}

var (
	toolSlotE2EGate     atomic.Pointer[toolSlotGate]
	toolSlotE2ERegister sync.Once
)

type toolSlotPipe struct{ sparkwing.Base }

func (toolSlotPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	plan.Resources(sparkwing.Cores(0.25))
	runID := rc.RunID
	node := sparkwing.Job(plan, toolSlotNodeID, func(ctx context.Context) error {
		return toolSlotE2EGate.Load().hold(ctx, runID)
	})
	if strings.HasPrefix(runID, "tool-progress-") {
		node.NoProgressTimeout(100 * time.Millisecond)
	}
	return nil
}

func registerToolSlotE2EPipeline() {
	toolSlotE2ERegister.Do(func() {
		sparkwing.Register[sparkwing.NoInputs](toolSlotPipelineName,
			func() sparkwing.Pipeline[sparkwing.NoInputs] { return toolSlotPipe{} })
	})
}

type toolSlotQueuedEvent struct {
	Key         string `json:"key"`
	Kind        string `json:"kind"`
	Scope       string `json:"scope"`
	Position    int    `json:"position"`
	QueueLength int    `json:"queue_length"`
}

func awaitToolSlotQueued(t *testing.T, st *store.Store, runID, nodeID string) toolSlotQueuedEvent {
	t.Helper()
	deadline := time.Now().Add(wingdTestWait)
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	for time.Now().Before(deadline) {
		events, err := st.ListEventsAfter(context.Background(), runID, 0, 500)
		if err != nil {
			t.Fatalf("list events for run %q: %v", runID, err)
		}
		for _, ev := range events {
			if ev.Kind != "concurrency_wait" || ev.NodeID != nodeID {
				continue
			}
			var q toolSlotQueuedEvent
			if json.Unmarshal(ev.Payload, &q) == nil && q.Scope == "tool" {
				return q
			}
		}
		<-poll.C
	}
	t.Fatalf("run %q node %q never recorded a tool-slot queue position", runID, nodeID)
	return toolSlotQueuedEvent{}
}

func awaitQueuedToolSlotWaiter(t *testing.T, home, runID, nodeID string) wingwire.Waiter {
	t.Helper()
	participant := nodeSemaphoreRunID(runID, nodeID)
	deadline := time.Now().Add(wingdTestWait)
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	for time.Now().Before(deadline) {
		if w, ok := findQueuedWaiter(queryWingd(t, home), participant); ok {
			return w
		}
		<-poll.C
	}
	t.Fatalf("tool slot for run %q never appeared as a queued waiter", runID)
	return wingwire.Waiter{}
}

func startToolSlotRun(t *testing.T, backends Backends, home, runID string) chan *Result {
	t.Helper()
	done := make(chan *Result, 1)
	go func() {
		res, _ := Run(context.Background(), backends, Options{
			Pipeline:  toolSlotPipelineName,
			RunID:     runID,
			Admission: testWingdAdmission(home, nil),
		})
		done <- res
	}()
	return done
}

func awaitToolSlotRunSuccess(t *testing.T, runID string, ch chan *Result) {
	t.Helper()
	select {
	case res := <-ch:
		if res == nil || res.Status != "success" {
			t.Fatalf("run %q result = %+v, want success", runID, res)
		}
	case <-time.After(wingdTestWait):
		t.Fatalf("run %q did not finish", runID)
	}
}

func TestWingd_ContendedToolSlotReportsItsQueuePositionBehindTheBoxBudget(t *testing.T) {
	registerToolSlotE2EPipeline()
	home := wingdTestHome(t)
	startWingd(t, home, 8)
	backends, st, _ := openWingdBackends(t, home)

	budget := sparkwing.BoxToolBudget("wingd-e2e-tool-contended", toolSlotGrantableCores, 0)
	if got := budget.Limit().Capacity; got != 800 {
		t.Fatalf("budget capacity = %d, want the 800 centicores 8 grantable cores buys", got)
	}
	cost := sparkwing.ToolCostCenticores(toolSlotLintCores)
	if cost != 400 {
		t.Fatalf("tool cost = %d, want 400", cost)
	}
	gate := newToolSlotGate(budget, cost)
	toolSlotE2EGate.Store(gate)

	runA := startToolSlotRun(t, backends, home, "tool-slot-a")
	if g := gate.awaitGrant(t, "tool-slot-a"); !g.granted {
		t.Fatal("first acquirer was not granted; 400 of 800 must fit")
	}
	runB := startToolSlotRun(t, backends, home, "tool-slot-b")
	if g := gate.awaitGrant(t, "tool-slot-b"); !g.granted {
		t.Fatal("second acquirer was not granted; 800 of 800 must fit")
	}

	runC := startToolSlotRun(t, backends, home, "tool-slot-c")
	q := awaitToolSlotQueued(t, st, "tool-slot-c", toolSlotNodeID)
	wantKey := scopedGroupKey(budget, "tool-slot-c")
	if q.Key != wantKey {
		t.Fatalf("queued key = %q, want the box-scoped budget key %q", q.Key, wantKey)
	}
	if label := ScopeLabelFromKey(q.Key); !strings.HasPrefix(label, "box (") {
		t.Fatalf("queued key scope = %q, want a box scope", label)
	}
	if q.Kind != "queued" || q.Scope != "tool" {
		t.Fatalf("queued event = %+v, want kind queued at tool scope", q)
	}
	if q.Position < 1 {
		t.Fatalf("queue position = %d, want at least 1", q.Position)
	}

	w := awaitQueuedToolSlotWaiter(t, home, "tool-slot-c", toolSlotNodeID)
	if !slices.Contains(w.Semaphores, wantKey) {
		t.Fatalf("daemon waiter semaphores = %v, want the budget key %q", w.Semaphores, wantKey)
	}
	gate.expectNoGrantYet(t)

	gate.let("tool-slot-a")
	if g := gate.awaitGrant(t, "tool-slot-c"); !g.granted {
		t.Fatal("third acquirer was not granted after a holder released")
	}

	gate.let("tool-slot-b")
	gate.let("tool-slot-c")
	awaitToolSlotRunSuccess(t, "tool-slot-a", runA)
	awaitToolSlotRunSuccess(t, "tool-slot-b", runB)
	awaitToolSlotRunSuccess(t, "tool-slot-c", runC)
}

func TestWingd_NoProgressTimeoutPausesForToolSlotAndResumesAfterGrant(t *testing.T) {
	registerToolSlotE2EPipeline()
	home := wingdTestHome(t)
	startWingd(t, home, 8)
	backends, st, _ := openWingdBackends(t, home)

	budget := sparkwing.BoxToolBudget("wingd-e2e-tool-progress", toolSlotLintCores, 0)
	gate := newToolSlotGate(budget, sparkwing.ToolCostCenticores(toolSlotLintCores))
	toolSlotE2EGate.Store(gate)
	t.Cleanup(func() {
		gate.let("tool-holder")
		gate.let("tool-progress-waiter")
	})

	holder := startToolSlotRun(t, backends, home, "tool-holder")
	gate.awaitContext(t, "tool-holder")
	if grant := gate.awaitGrant(t, "tool-holder"); !grant.granted {
		t.Fatal("holder was not granted the tool slot")
	}

	waiter := startToolSlotRun(t, backends, home, "tool-progress-waiter")
	waiterCtx := gate.awaitContext(t, "tool-progress-waiter")
	awaitToolSlotQueued(t, st, "tool-progress-waiter", toolSlotNodeID)
	if !ProgressTimeoutPausedForTest(waiterCtx) {
		t.Fatal("waiter progress timeout was not paused for the tool slot")
	}
	if ExpireProgressTimeoutForTest(waiterCtx) {
		t.Fatal("waiter progress timeout fired while the tool slot was queued")
	}

	gate.let("tool-holder")
	if grant := gate.awaitGrant(t, "tool-progress-waiter"); !grant.granted {
		t.Fatal("waiter was not granted after the holder released")
	}
	if !ExpireProgressTimeoutForTest(waiterCtx) {
		t.Fatal("waiter progress timeout did not fire after the tool slot was granted")
	}

	select {
	case res := <-waiter:
		if res == nil || res.Status != "failed" {
			t.Fatalf("waiter result = %+v, want failure after its clock resumed", res)
		}
		nodes, err := st.ListNodes(context.Background(), "tool-progress-waiter")
		if err != nil {
			t.Fatalf("list waiter nodes: %v", err)
		}
		if len(nodes) != 1 || nodes[0].FailureReason != store.FailureNoProgressTimeout {
			t.Fatalf("waiter nodes = %+v, want failure reason %q", nodes, store.FailureNoProgressTimeout)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waiter did not time out after its tool slot was granted")
	}
	awaitToolSlotRunSuccess(t, "tool-holder", holder)
}

func TestWingd_ToolSlotQueueTimeoutFallsBackRatherThanBlockingForever(t *testing.T) {
	registerToolSlotE2EPipeline()
	home := wingdTestHome(t)
	startWingd(t, home, 8)
	backends, _, _ := openWingdBackends(t, home)

	const queueTimeout = 300 * time.Millisecond
	budget := sparkwing.BoxToolBudget("wingd-e2e-tool-timeout", toolSlotGrantableCores, queueTimeout)
	gate := newToolSlotGate(budget, sparkwing.ToolCostCenticores(toolSlotLintCores))
	toolSlotE2EGate.Store(gate)

	runA := startToolSlotRun(t, backends, home, "tool-timeout-a")
	if g := gate.awaitGrant(t, "tool-timeout-a"); !g.granted {
		t.Fatal("first acquirer was not granted; 400 of 800 must fit")
	}
	runB := startToolSlotRun(t, backends, home, "tool-timeout-b")
	if g := gate.awaitGrant(t, "tool-timeout-b"); !g.granted {
		t.Fatal("second acquirer was not granted; 800 of 800 must fit")
	}

	start := time.Now()
	runC := startToolSlotRun(t, backends, home, "tool-timeout-c")
	if g := gate.awaitGrant(t, "tool-timeout-c"); g.granted {
		t.Fatal("third acquirer was granted while the budget was full")
	}
	if waited := time.Since(start); waited < queueTimeout {
		t.Fatalf("third acquirer gave up after %s, want it to wait out the %s queue timeout", waited, queueTimeout)
	}

	gate.let("tool-timeout-a")
	gate.let("tool-timeout-b")
	gate.let("tool-timeout-c")
	awaitToolSlotRunSuccess(t, "tool-timeout-a", runA)
	awaitToolSlotRunSuccess(t, "tool-timeout-b", runB)
	awaitToolSlotRunSuccess(t, "tool-timeout-c", runC)
}
