package orchestrator_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// Engine-semantics coverage for the cache/concurrency split: memo
// decoupled from groups, content-keyed in-flight dedupe, scope-folded
// keys, cost-weighted admission across a scope, the worker-slot yield
// during waits, and the queue timeout.

var sem struct {
	runs     atomic.Int32 // total body invocations
	inflight atomic.Int32
	peak     atomic.Int32
}

func resetSem() {
	sem.runs.Store(0)
	sem.inflight.Store(0)
	sem.peak.Store(0)
	resetLeaderBarrier()
}

// leaderHolding is set true by a held leader once it is executing -- so
// it has acquired its slot -- and a test polls it to start the follower
// deterministically instead of guessing with a sleep. leaderRelease
// frees the held leader once the follower has resolved. Both are
// in-process (the orchestrator runs in-process in these tests), so no
// store handle or timing race is involved.
var (
	leaderHolding atomic.Bool
	leaderRelease atomic.Bool
)

func resetLeaderBarrier() {
	leaderHolding.Store(false)
	leaderRelease.Store(false)
}

// held returns a job body that marks its slot held, then blocks until
// the test releases it (or ctx is cancelled), so the leader holds for
// exactly as long as the test needs -- no fixed sleep to race against.
// onStart, if non-nil, runs once the body begins and returns a cleanup
// to run when it ends (e.g. to track in-flight concurrency).
func held(onStart func() func()) func(context.Context) error {
	return func(ctx context.Context) error {
		if onStart != nil {
			if cleanup := onStart(); cleanup != nil {
				defer cleanup()
			}
		}
		leaderHolding.Store(true)
		for !leaderRelease.Load() {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(2 * time.Millisecond):
			}
		}
		return nil
	}
}

// heldSkip is the SkipIf twin of held: the leader holds its slot through
// skip evaluation until released, then skips. Used to make a skipped
// memo leader hold deterministically while a follower coalesces.
func heldSkip(ctx context.Context) bool {
	leaderHolding.Store(true)
	for !leaderRelease.Load() {
		select {
		case <-ctx.Done():
			return true
		case <-time.After(2 * time.Millisecond):
		}
	}
	return true
}

// waitForLeaderHolding blocks until a held leader signals it holds its
// slot, with a generous ceiling so a hang fails loudly rather than
// hanging the suite.
func waitForLeaderHolding(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !leaderHolding.Load() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the leader to hold its slot")
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// waitForCoalesceWaiter blocks until a coalesce waiter row exists, i.e. a
// follower has actually coalesced onto an in-flight leader. Used so a
// memo leader can be released only once the follower it is meant to
// coalesce is genuinely parked -- a coalesced follower blocks on the
// leader finishing, so the leader can't wait on the follower's run.
func waitForCoalesceWaiter(t *testing.T, dbPath string) {
	t.Helper()
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = st.Close() }()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		var n int
		if err := st.DB().QueryRowContext(context.Background(),
			`SELECT COUNT(*) FROM concurrency_waiters WHERE policy = 'coalesce'`).Scan(&n); err == nil && n > 0 {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("timed out waiting for a follower to coalesce")
}

func semStep(hold time.Duration) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		sem.runs.Add(1)
		cur := sem.inflight.Add(1)
		defer sem.inflight.Add(-1)
		for {
			p := sem.peak.Load()
			if cur <= p || sem.peak.CompareAndSwap(p, cur) {
				break
			}
		}
		time.Sleep(hold)
		return nil
	}
}

func contentKey(v string) sparkwing.CacheKeyFn {
	return func(ctx context.Context) sparkwing.CacheKey { return sparkwing.Key("sem", v) }
}

// --- Memo decoupled from concurrency group ---

type memoDiffGroupsPipe struct{ sparkwing.Base }

func (memoDiffGroupsPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	gx := sparkwing.NewConcurrencyGroup("memo-gx", sparkwing.ConcurrencyLimit{Capacity: 1})
	gy := sparkwing.NewConcurrencyGroup("memo-gy", sparkwing.ConcurrencyLimit{Capacity: 1})
	a := sparkwing.Job(plan, "a", semStep(40*time.Millisecond)).
		Concurrency(gx).Cache(contentKey("shared"))
	sparkwing.Job(plan, "b", semStep(40*time.Millisecond)).
		Concurrency(gy).Cache(contentKey("shared")).Needs(a)
	return nil
}

type memoSameGroupDiffContentPipe struct{ sparkwing.Base }

func (memoSameGroupDiffContentPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	g := sparkwing.NewConcurrencyGroup("memo-same", sparkwing.ConcurrencyLimit{Capacity: 2})
	sparkwing.Job(plan, "a", semStep(40*time.Millisecond)).Concurrency(g).Cache(contentKey("k-a"))
	sparkwing.Job(plan, "b", semStep(40*time.Millisecond)).Concurrency(g).Cache(contentKey("k-b"))
	return nil
}

type memoInFlightPipe struct{ sparkwing.Base }

func (memoInFlightPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	// Two peers, same content, no group, dispatched together: one leads,
	// the other dedupes in flight on the content hash.
	sparkwing.Job(plan, "a", semStep(300*time.Millisecond)).Cache(contentKey("dup"))
	sparkwing.Job(plan, "b", semStep(300*time.Millisecond)).Cache(contentKey("dup"))
	return nil
}

// --- Scope ---

type scopeBoxPipe struct{ sparkwing.Base }

func (scopeBoxPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	g := sparkwing.NewConcurrencyGroup("scope-box", sparkwing.ConcurrencyLimit{
		Capacity: 1, Scope: sparkwing.ScopeBox,
	})
	sparkwing.Job(plan, "work", semStep(250*time.Millisecond)).Concurrency(g)
	return nil
}

// scopeRunBarrier is reset by the run-scope test; both runs' nodes must
// reach it for the test to pass, which only happens if the run-scoped
// group does NOT serialize them.
var scopeRunBarrier atomic.Pointer[runBarrier]

type runBarrier struct {
	mu    sync.Mutex
	count int
	ch    chan struct{}
}

func newRunBarrier() *runBarrier { return &runBarrier{ch: make(chan struct{})} }

func (b *runBarrier) arrive(timeout time.Duration) bool {
	b.mu.Lock()
	b.count++
	if b.count == 2 {
		close(b.ch)
	}
	b.mu.Unlock()
	select {
	case <-b.ch:
		return true
	case <-time.After(timeout):
		return false
	}
}

type scopeRunPipe struct{ sparkwing.Base }

func (scopeRunPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	g := sparkwing.NewConcurrencyGroup("scope-run", sparkwing.ConcurrencyLimit{
		Capacity: 1, Scope: sparkwing.ScopeRun,
	})
	sparkwing.Job(plan, "work", func(ctx context.Context) error {
		if b := scopeRunBarrier.Load(); b != nil && !b.arrive(3*time.Second) {
			return errors.New("run-scoped groups serialized across runs; expected isolation")
		}
		return nil
	}).Concurrency(g)
	return nil
}

// --- Cost summed across a Box scope ---

type costBoxAPipe struct{ sparkwing.Base }

func costBoxGroup() *sparkwing.ConcurrencyGroup {
	return sparkwing.NewConcurrencyGroup("cost-box", sparkwing.ConcurrencyLimit{
		Capacity: 8, Scope: sparkwing.ScopeBox,
	})
}

func (costBoxAPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	g := costBoxGroup()
	sparkwing.Job(plan, "a", semStep(300*time.Millisecond)).Concurrency(g, 4)
	sparkwing.Job(plan, "b", semStep(300*time.Millisecond)).Concurrency(g, 4)
	return nil
}

type costBoxBPipe struct{ sparkwing.Base }

func (costBoxBPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	g := costBoxGroup()
	sparkwing.Job(plan, "c", semStep(300*time.Millisecond)).Concurrency(g, 4)
	return nil
}

// --- Worker-slot yield during wait ---

var freeNodeLatency atomic.Int64 // ns from run start to free node completion

type workerSlotPipe struct{ sparkwing.Base }

func (workerSlotPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	start := time.Now()
	g := sparkwing.NewConcurrencyGroup("worker-block", sparkwing.ConcurrencyLimit{Capacity: 1})
	// One holder plus two waiters on a capacity-1 group; under a bug
	// where waiters keep their worker slot, two slots are consumed and
	// the free node starves.
	sparkwing.Job(plan, "g1", semStep(500*time.Millisecond)).Concurrency(g)
	sparkwing.Job(plan, "g2", semStep(500*time.Millisecond)).Concurrency(g)
	sparkwing.Job(plan, "g3", semStep(500*time.Millisecond)).Concurrency(g)
	sparkwing.Job(plan, "free", func(ctx context.Context) error {
		freeNodeLatency.Store(int64(time.Since(start)))
		return nil
	})
	return nil
}

// --- Queue timeout ---

type queueTimeoutLeaderPipe struct{ sparkwing.Base }

func (queueTimeoutLeaderPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	g := sparkwing.NewConcurrencyGroup("qt-key", sparkwing.ConcurrencyLimit{Capacity: 1})
	sparkwing.Job(plan, "leader", held(nil)).Concurrency(g)
	return nil
}

type queueTimeoutFollowerPipe struct{ sparkwing.Base }

func (queueTimeoutFollowerPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	g := sparkwing.NewConcurrencyGroup("qt-key", sparkwing.ConcurrencyLimit{
		Capacity: 1, OnLimit: sparkwing.Queue, QueueTimeout: 200 * time.Millisecond,
	})
	sparkwing.Job(plan, "follower", semStep(50*time.Millisecond)).Concurrency(g)
	return nil
}

func init() {
	register("memo-diff-groups", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &memoDiffGroupsPipe{} })
	register("memo-same-group", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &memoSameGroupDiffContentPipe{} })
	register("memo-inflight", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &memoInFlightPipe{} })
	register("scope-box", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &scopeBoxPipe{} })
	register("scope-run", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &scopeRunPipe{} })
	register("cost-box-a", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &costBoxAPipe{} })
	register("cost-box-b", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &costBoxBPipe{} })
	register("worker-slot-yield", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &workerSlotPipe{} })
	register("qt-leader", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &queueTimeoutLeaderPipe{} })
	register("qt-follower", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &queueTimeoutFollowerPipe{} })
}

func nodeByID(t *testing.T, p orchestrator.Paths, runID, nodeID string) *store.Node {
	t.Helper()
	st, _ := store.Open(p.StateDB())
	defer func() { _ = st.Close() }()
	nodes, _ := st.ListNodes(context.Background(), runID)
	for _, n := range nodes {
		if n.NodeID == nodeID {
			return n
		}
	}
	t.Fatalf("node %q not found in run %q", nodeID, runID)
	return nil
}

func TestMemo_SharedAcrossDifferentGroups(t *testing.T) {
	resetSem()
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "memo-diff-groups"})
	if err != nil || res.Status != "success" {
		t.Fatalf("run: status=%q err=%v", res.Status, err)
	}
	if got := sem.runs.Load(); got != 1 {
		t.Fatalf("body ran %d times, want 1 (b should replay a's memo despite a different group)", got)
	}
	if b := nodeByID(t, p, res.RunID, "b"); b.Outcome != string(sparkwing.Cached) {
		t.Fatalf("b outcome = %q, want cached", b.Outcome)
	}
}

func TestMemo_SameGroupDifferentContentBothRun(t *testing.T) {
	resetSem()
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "memo-same-group"})
	if err != nil || res.Status != "success" {
		t.Fatalf("run: status=%q err=%v", res.Status, err)
	}
	if got := sem.runs.Load(); got != 2 {
		t.Fatalf("body ran %d times, want 2 (distinct content must not share a memo)", got)
	}
}

func TestMemo_InFlightDedupeOnContent(t *testing.T) {
	resetSem()
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "memo-inflight"})
	if err != nil || res.Status != "success" {
		t.Fatalf("run: status=%q err=%v", res.Status, err)
	}
	if got := sem.runs.Load(); got != 1 {
		t.Fatalf("body ran %d times, want 1 (identical in-flight content must dedupe)", got)
	}
	if peak := sem.peak.Load(); peak != 1 {
		t.Fatalf("peak concurrency = %d, want 1", peak)
	}
}

func TestScope_BoxSerializesAcrossRunsOnSameHost(t *testing.T) {
	resetSem()
	p := newPaths(t)
	var wg sync.WaitGroup
	for i := range 2 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _ = orchestrator.RunLocal(context.Background(), p, orchestrator.Options{
				Pipeline: "scope-box", RunID: fmt.Sprintf("box-%d", i),
			})
		}(i)
	}
	wg.Wait()
	if peak := sem.peak.Load(); peak > 1 {
		t.Fatalf("Box-scoped peak across runs = %d, want 1 (shared budget on one host)", peak)
	}
}

func TestScope_RunIsolatesPerRun(t *testing.T) {
	scopeRunBarrier.Store(newRunBarrier())
	p := newPaths(t)
	type outcome struct {
		status string
		runErr error
		err    error
	}
	results := make([]outcome, 2)
	var wg sync.WaitGroup
	for i := range 2 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{
				Pipeline: "scope-run", RunID: fmt.Sprintf("run-%d", i),
			})
			results[i].err = err
			if res != nil {
				results[i].status = res.Status
				results[i].runErr = res.Error
			}
		}(i)
	}
	wg.Wait()
	for i, o := range results {
		if o.status != "success" {
			t.Fatalf("run %d status = %q err=%v runErr=%v (run-scoped groups must not serialize across runs)", i, o.status, o.err, o.runErr)
		}
	}
}

func TestConcurrency_CostSummedAcrossBoxScope(t *testing.T) {
	resetSem()
	p := newPaths(t)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "cost-box-a", RunID: "cb-a"})
	}()
	go func() {
		defer wg.Done()
		_, _ = orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "cost-box-b", RunID: "cb-b"})
	}()
	wg.Wait()
	// Capacity 8, three cost-4 members across two runs on one host:
	// at most two run at once.
	if peak := sem.peak.Load(); peak > 2 {
		t.Fatalf("cost-weighted Box peak = %d, want <= 2 (8/4)", peak)
	}
}

func TestConcurrency_WaitDoesNotHoldWorkerSlot(t *testing.T) {
	resetSem()
	freeNodeLatency.Store(0)
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{
		Pipeline: "worker-slot-yield", MaxParallel: 2,
	})
	if err != nil || res.Status != "success" {
		t.Fatalf("run: status=%q err=%v", res.Status, err)
	}
	// With waiters yielding their worker slot, the free node runs while a
	// group member holds the only group slot -- well before the ~1.5s a
	// fully serialized group queue would take. A bug that pins the slot
	// to queued waiters would delay free past the first 500ms holder.
	latency := time.Duration(freeNodeLatency.Load())
	if latency == 0 || latency > 300*time.Millisecond {
		t.Fatalf("free node latency = %s, want < 300ms (queued waiters must not pin worker slots)", latency)
	}
}

func TestConcurrency_QueueTimeoutFailsWaiterCleanly(t *testing.T) {
	resetSem()
	p := newPaths(t)
	leaderDone := make(chan struct{})
	go func() {
		_, _ = orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "qt-leader", RunID: "qt-leader"})
		close(leaderDone)
	}()
	waitForLeaderHolding(t) // leader holds the only slot

	followerRes, _ := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "qt-follower", RunID: "qt-follower"})
	if followerRes.Status != "failed" {
		t.Fatalf("follower status = %q, want failed (QueueTimeout elapsed)", followerRes.Status)
	}
	n := nodeByID(t, p, followerRes.RunID, "follower")
	if n.FailureReason != store.FailureQueueTimeout {
		t.Fatalf("follower failure_reason = %q, want %q", n.FailureReason, store.FailureQueueTimeout)
	}
	leaderRelease.Store(true) // let the leader finish
	<-leaderDone
}

// --- Defect 3: skipped memo leader must not stamp coalesced followers Success ---

type memoSkipLeaderPipe struct{ sparkwing.Base }

func (memoSkipLeaderPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	// The memo leader holds the content slot through its skip evaluation
	// (heldSkip) until the test releases it, so a follower coalesces
	// deterministically; then it skips, writing no cache.
	sparkwing.Job(plan, "leader", semStep(0)).Cache(contentKey("skip-dup")).SkipIf(heldSkip)
	return nil
}

type memoSkipFollowerPipe struct{ sparkwing.Base }

func (memoSkipFollowerPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	// Same content key: it coalesces onto the in-flight leader and must
	// inherit the leader's skip rather than going green. Its own SkipIf
	// is a fallback for the standalone case.
	sparkwing.Job(plan, "follower", semStep(0)).
		Cache(contentKey("skip-dup")).
		SkipIf(func(ctx context.Context) bool { return true })
	return nil
}

// --- Defect 5: cancelling a queued grouped node must not leak a phantom holder ---

type phantomHolderPipe struct{ sparkwing.Base }

func (phantomHolderPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	g := sparkwing.NewConcurrencyGroup("phantom", sparkwing.ConcurrencyLimit{Capacity: 1, Scope: sparkwing.ScopeGlobal})
	sparkwing.Job(plan, "hold", semStep(1200*time.Millisecond)).Concurrency(g)
	return nil
}

type phantomWaiterPipe struct{ sparkwing.Base }

func (phantomWaiterPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	g := sparkwing.NewConcurrencyGroup("phantom", sparkwing.ConcurrencyLimit{Capacity: 1, Scope: sparkwing.ScopeGlobal})
	sparkwing.Job(plan, "wait", semStep(50*time.Millisecond)).Concurrency(g)
	return nil
}

func init() {
	register("memo-skip-leader", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &memoSkipLeaderPipe{} })
	register("memo-skip-follower", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &memoSkipFollowerPipe{} })
	register("phantom-holder", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &phantomHolderPipe{} })
	register("phantom-waiter", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &phantomWaiterPipe{} })
}

func TestMemo_LeaderSkippedWhileFollowerCoalesced(t *testing.T) {
	resetSem()
	p := newPaths(t)

	leaderDone := make(chan *orchestrator.Result, 1)
	go func() {
		res, _ := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "memo-skip-leader", RunID: "memo-skip-leader"})
		leaderDone <- res
	}()
	waitForLeaderHolding(t) // leader holds the content slot through its skip

	// The follower coalesces onto the held leader and blocks until the
	// leader finishes, so run it concurrently and release the leader only
	// once it has genuinely coalesced.
	followerDone := make(chan *orchestrator.Result, 1)
	go func() {
		res, _ := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "memo-skip-follower", RunID: "memo-skip-follower"})
		followerDone <- res
	}()
	waitForCoalesceWaiter(t, p.StateDB())
	leaderRelease.Store(true) // leader skips; follower inherits the skip
	leaderRes := <-leaderDone
	followerRes := <-followerDone

	if got := sem.runs.Load(); got != 0 {
		t.Fatalf("body ran %d times, want 0 (both nodes skipped)", got)
	}
	// Neither node may be Success: the leader skipped (no cache written),
	// so the coalesced follower must inherit a non-success outcome rather
	// than going green empty.
	st, _ := store.Open(p.StateDB())
	defer func() { _ = st.Close() }()
	for _, rid := range []string{leaderRes.RunID, followerRes.RunID} {
		nodes, _ := st.ListNodes(context.Background(), rid)
		if len(nodes) != 1 {
			t.Fatalf("run %s: expected 1 node, got %d", rid, len(nodes))
		}
		if nodes[0].Outcome == string(sparkwing.Success) {
			t.Fatalf("node %q in run %s is Success after a skipped memo leader; follower inherited a bogus success", nodes[0].NodeID, rid)
		}
	}
}

func TestGroupedNode_CancelWhileQueued_LeaksWaiterIntoPhantomHolder(t *testing.T) {
	resetSem()
	p := newPaths(t)

	// Holder run takes the only slot for ~1.2s.
	holderDone := make(chan struct{})
	go func() {
		_, _ = orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "phantom-holder", RunID: "phantom-holder"})
		close(holderDone)
	}()
	time.Sleep(250 * time.Millisecond) // holder acquires

	// Waiter run queues on the same group, then is cancelled while queued.
	waiterCtx, cancelWaiter := context.WithCancel(context.Background())
	waiterDone := make(chan struct{})
	go func() {
		_, _ = orchestrator.RunLocal(waiterCtx, p, orchestrator.Options{Pipeline: "phantom-waiter", RunID: "phantom-waiter"})
		close(waiterDone)
	}()
	time.Sleep(300 * time.Millisecond) // waiter parks in the queue
	cancelWaiter()
	<-waiterDone
	<-holderDone // holder releases and promotes the next waiter (if any)

	// The cancelled waiter must not have been promoted into a real
	// holder. Inspect the group's coordination key.
	st, _ := store.Open(p.StateDB())
	defer func() { _ = st.Close() }()
	state, err := st.GetConcurrencyState(context.Background(), "g:phantom")
	if err != nil {
		return // key reaped entirely is also fine -- no phantom holder
	}
	now := time.Now()
	for _, h := range state.Holders {
		if h.Superseded || !h.LeaseExpiresAt.After(now) {
			continue
		}
		if h.RunID == "phantom-waiter" {
			t.Fatalf("cancelled queued waiter was promoted into a phantom holder: %+v", h)
		}
		t.Fatalf("unexpected live holder after holder release + waiter cancel: %+v", h)
	}
}
