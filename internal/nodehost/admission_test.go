package nodehost

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func init() {
	waiterPoll = 5 * time.Millisecond
}

const testBox = "box-1"

func runAdmitted(t *testing.T, ctx context.Context, st *store.Store, doc nodeproto.PlanDoc, adm *Admission, exec Executor) RunResult {
	t.Helper()
	if err := st.CreateRun(ctx, store.Run{ID: testRunID, Pipeline: "p", Status: "running"}); err != nil {
		return RunResult{Status: "failed", Err: err}
	}
	if adm.BoxID == nil {
		adm.BoxID = func() string { return testBox }
	}
	cfg := Config{Execute: exec, Admission: adm}
	run := &Run{ID: testRunID, Pipeline: "p", Doc: doc, State: st, Logger: slog.New(slog.DiscardHandler), adds: make(chan addRequest), leaseToken: "tok", validation: nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: noV2Secrets{}}}
	res := RunResult{RunID: testRunID, Nodes: map[string]Result{}}
	s := &scheduler{run: run, results: res.Nodes, started: map[string]bool{}, exec: chain(&cfg)}
	if err := s.add(ctx, ctx, doc.Jobs, nil); err != nil {
		return RunResult{Status: "failed", Err: err}
	}
	s.loop(ctx)
	res.Status, res.Err = s.verdict(ctx)
	return res
}

func grouped(scope, onLimit string, capacity int, queueTimeoutMS int64, ids ...string) []nodeproto.JobSpec {
	concurrency := nodeproto.ConcurrencySpec{Group: "db", Capacity: new(capacity), QueueTimeoutMS: new(queueTimeoutMS)}
	if scope != "" {
		concurrency.Scope = new(scope)
	}
	if onLimit != "" {
		concurrency.OnLimit = new(onLimit)
	}
	var nodes []nodeproto.JobSpec
	for _, id := range ids {
		nodes = append(nodes, nodeproto.JobSpec{ID: id, Concurrency: []nodeproto.ConcurrencySpec{concurrency}, Steps: []nodeproto.StepSpec{{ID: "body"}}})
	}
	return nodes
}

type fakeDaemon struct {
	mu       sync.Mutex
	changed  chan struct{}
	used     map[string]int
	reqs     []wingwire.AdmissionRequest
	leases   map[string]*fakeLease
	live     int
	granted  int
	released int
	hostGate chan struct{}
	queued   chan string
}

func newFakeDaemon() *fakeDaemon {
	return &fakeDaemon{changed: make(chan struct{}), used: map[string]int{}, leases: map[string]*fakeLease{}, queued: make(chan string, 64)}
}

func (d *fakeDaemon) Acquire(ctx context.Context, req wingwire.AdmissionRequest, onQueued func(wingwire.Queued)) (Lease, error) {
	d.mu.Lock()
	d.reqs = append(d.reqs, req)
	d.mu.Unlock()
	if d.hostGate != nil && !req.SemaphoresOnly {
		select {
		case <-d.hostGate:
		default:
			onQueued(wingwire.Queued{RunID: req.RunID, Key: "cores", Position: 1})
			d.queued <- req.RunID
			select {
			case <-d.hostGate:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
	}
	reported := false
	for {
		d.mu.Lock()
		full := ""
		for _, c := range req.Semaphores {
			if d.used[c.Name]+max(1, c.Cost) > max(1, c.Capacity) {
				full = c.Name
			}
		}
		if full == "" {
			for _, c := range req.Semaphores {
				d.used[c.Name] += max(1, c.Cost)
			}
			l := &fakeLease{
				d: d, req: req, done: make(chan struct{}), evict: make(chan wingwire.Evicted, 1),
				cancel: make(chan wingwire.Cancel, 1), lose: make(chan error, 1), probes: make(chan string), acks: make(chan string, 1),
				watchDone: make(chan struct{}),
			}
			d.leases[req.RunID] = l
			d.live++
			d.granted++
			d.mu.Unlock()
			return l, nil
		}
		ch := d.changed
		d.mu.Unlock()
		if p := req.Semaphores[0].Policy; p == wingwire.PolicySkip || p == wingwire.PolicyFail {
			return nil, &wingdclient.AdmissionError{Policy: p, Key: full}
		}
		if !reported {
			reported = true
			onQueued(wingwire.Queued{RunID: req.RunID, Key: full, Position: 2})
			d.queued <- req.RunID
		}
		select {
		case <-ch:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func (d *fakeDaemon) counts() (live, granted, released int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.live, d.granted, d.released
}

func (d *fakeDaemon) requests() []wingwire.AdmissionRequest {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]wingwire.AdmissionRequest(nil), d.reqs...)
}

type fakeLease struct {
	d         *fakeDaemon
	req       wingwire.AdmissionRequest
	once      sync.Once
	done      chan struct{}
	evict     chan wingwire.Evicted
	cancel    chan wingwire.Cancel
	lose      chan error
	probes    chan string
	acks      chan string
	watching  sync.WaitGroup
	watchDone chan struct{}
}

func (l *fakeLease) Token() string { return "lease-" + l.req.RunID }

func (l *fakeLease) Release() error {
	l.once.Do(func() {
		d := l.d
		d.mu.Lock()
		for _, c := range l.req.Semaphores {
			d.used[c.Name] -= max(1, c.Cost)
		}
		d.live--
		d.released++
		close(d.changed)
		d.changed = make(chan struct{})
		d.mu.Unlock()
		close(l.done)
	})
	return nil
}

// hack: the fake answers liveness probes only while Watch runs, as the real
// client answers them only from its lease watcher.
func (l *fakeLease) Watch(onEvicted func(wingwire.Evicted), onCancel func(wingwire.Cancel)) error {
	l.watching.Add(1)
	defer l.watching.Done()
	defer close(l.watchDone)
	for {
		select {
		case <-l.done:
			return nil
		case ev := <-l.evict:
			onEvicted(ev)
		case c := <-l.cancel:
			onCancel(c)
		case err := <-l.lose:
			return err
		case nonce := <-l.probes:
			l.acks <- nonce
		}
	}
}

func (d *fakeDaemon) only(t *testing.T) *fakeLease {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.leases) != 1 {
		t.Errorf("%d leases, want 1", len(d.leases))
		return nil
	}
	for _, l := range d.leases {
		return l
	}
	return nil
}

type storeSlots struct {
	st     *store.Store
	queued chan string
}

func (s storeSlots) AcquireSlot(ctx context.Context, req store.AcquireSlotRequest) (store.AcquireSlotResponse, error) {
	tenant, err := s.st.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		return store.AcquireSlotResponse{}, err
	}

	resp, err := tenant.AcquireConcurrencySlot(ctx, req)
	if err == nil && resp.Kind == store.AcquireQueued && s.queued != nil {
		s.queued <- req.NodeID
	}
	return resp, err
}

func (s storeSlots) ObserveSlot(ctx context.Context, key, holderID string) (*store.ConcurrencyHolder, error) {
	tenant, err := s.st.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		return nil, err
	}

	return tenant.ConcurrencyHolder(ctx, key, holderID, time.Now())
}

func (s storeSlots) HeartbeatSlot(ctx context.Context, key, holderID string, lease time.Duration) (time.Time, bool, error) {
	tenant, err := s.st.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		return time.Time{}, false, err
	}

	return tenant.HeartbeatConcurrencySlot(ctx, key, holderID, lease)
}

func (s storeSlots) ReleaseSlot(ctx context.Context, key, holderID, outcome, outputRef, cacheKeyHash string, ttl time.Duration) error {
	tenant, err := s.st.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		return err
	}

	_, _, _, err = tenant.ReleaseAndNotify(ctx, key, holderID, outcome, outputRef, cacheKeyHash, ttl, store.DefaultConcurrencyLease)
	return err
}

func (s storeSlots) ResolveWaiter(ctx context.Context, key, runID, nodeID, cacheKeyHash, leaderRunID, leaderNodeID string, bypassRead bool) (store.WaiterResolution, error) {
	tenant, err := s.st.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		return store.WaiterResolution{}, err
	}

	return tenant.ResolveWaiter(ctx, key, runID, nodeID, cacheKeyHash, leaderRunID, leaderNodeID, bypassRead)
}

func (s storeSlots) ForceReleaseSuperseded(ctx context.Context, key string) ([]store.ConcurrencyHolder, error) {
	tenant, err := s.st.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		return nil, err
	}

	return tenant.ForceReleaseSupersededHolders(ctx, key)
}

func (s storeSlots) CancelWaiter(ctx context.Context, key, runID, nodeID string) (bool, error) {
	tenant, err := s.st.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		return false, err
	}

	return tenant.CancelWaiter(ctx, key, runID, nodeID)
}

type gauge struct {
	mu        sync.Mutex
	now, peak int
	entered   chan string
	gate      chan struct{}
}

func newGauge() *gauge { return &gauge{entered: make(chan string, 64), gate: make(chan struct{})} }

func (g *gauge) exec(ctx context.Context, n *NodeRun) Result {
	g.mu.Lock()
	g.now++
	g.peak = max(g.peak, g.now)
	g.mu.Unlock()
	defer func() {
		g.mu.Lock()
		g.now--
		g.mu.Unlock()
	}()
	g.entered <- n.Plan.ID
	select {
	case <-g.gate:
		return Result{Outcome: sparkwing.Success}
	case <-ctx.Done():
		return Result{Outcome: sparkwing.Failed, Err: ctx.Err()}
	}
}

func afterAdmissionEvent(t *testing.T, parent context.Context, event <-chan string, action func(context.Context) error) {
	t.Helper()
	ctx, cancel := context.WithCancel(parent)
	done := make(chan error, 1)
	go func() {
		select {
		case <-event:
			done <- action(ctx)
		case <-ctx.Done():
			done <- nil
		}
	}()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
}

func awaitHostedEvent(t *testing.T, ctx context.Context, event <-chan string, done <-chan RunResult) string {
	t.Helper()
	select {
	case value := <-event:
		return value
	case result := <-done:
		t.Fatalf("host returned before its event: %v", result.Err)
	case <-ctx.Done():
		t.Fatal("host event did not arrive:", context.Cause(ctx))
	}
	return ""
}

func TestAdmissionStoreGroupBoundsParallelNodes(t *testing.T) {
	st := openStore(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	slots, g := storeSlots{st: st, queued: make(chan string, 64)}, newGauge()
	doc := nodeproto.PlanDoc{Jobs: grouped("", "queue", 2, 0, "a", "b", "c", "d")}
	done := make(chan RunResult, 1)
	go func() { done <- runAdmitted(t, ctx, st, doc, &Admission{Slots: slots}, g.exec) }()
	awaitHostedEvent(t, ctx, g.entered, done)
	awaitHostedEvent(t, ctx, g.entered, done)
	awaitHostedEvent(t, ctx, slots.queued, done)
	awaitHostedEvent(t, ctx, slots.queued, done)
	close(g.gate)
	var res RunResult
	select {
	case res = <-done:
	case <-ctx.Done():
		t.Fatal("admitted host did not finish:", context.Cause(ctx))
	}
	if res.Status != "success" || g.peak != 2 {
		t.Fatalf("status %s, peak %d: want success with at most 2 at once", res.Status, g.peak)
	}
	wantSlotsEmpty(t, st, "global:db")
}

func wantSlotsEmpty(t *testing.T, st *store.Store, key string) {
	t.Helper()
	state, err := teststore.ForTeam(t, st, store.DefaultTeam).GetConcurrencyState(t.Context(), key)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Holders) != 0 || len(state.Waiters) != 0 {
		t.Fatalf("key %s holders %+v waiters %+v, want none", key, state.Holders, state.Waiters)
	}
}

func holdSlotOutside(t *testing.T, st *store.Store, key string) {
	t.Helper()
	resp, err := teststore.ForTeam(t, st, store.DefaultTeam).AcquireConcurrencySlot(t.Context(), store.AcquireSlotRequest{Key: key, HolderID: "other/x", RunID: "other", NodeID: "x", Capacity: 1, Policy: "queue"})
	if err != nil || resp.Kind != store.AcquireGranted {
		t.Fatalf("outside hold: %+v %v", resp, err)
	}
}

func TestAdmissionCancelWhileQueuedInStore(t *testing.T) {
	st := openStore(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	holdSlotOutside(t, st, "global:db")
	slots := storeSlots{st: st, queued: make(chan string, 1)}
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	afterAdmissionEvent(t, ctx, slots.queued, func(context.Context) error { cancelRun(); return nil })
	ran := false
	res := runAdmitted(t, runCtx, st, nodeproto.PlanDoc{Jobs: grouped("global", "queue", 1, 0, "b")}, &Admission{Slots: slots},
		func(context.Context, *NodeRun) Result { ran = true; return Result{Outcome: sparkwing.Success} })
	if ran || res.Nodes["b"].Outcome != sparkwing.Cancelled {
		t.Fatalf("ran=%v result %+v, want cancelled before running", ran, res.Nodes["b"])
	}
	wantRow(t, st, "b", sparkwing.Cancelled, "")
	state, err := teststore.ForTeam(t, st, store.DefaultTeam).GetConcurrencyState(ctx, "global:db")
	if err != nil || len(state.Waiters) != 0 || len(state.Holders) != 1 {
		t.Fatalf("state %+v %v, want the outside holder and no waiter", state, err)
	}
}

func TestAdmissionStoreQueueTimeoutAndLimitPolicies(t *testing.T) {
	for _, c := range []struct {
		onLimit string
		want    sparkwing.Outcome
		reason  string
	}{
		{"queue", sparkwing.Failed, store.FailureQueueTimeout},
		{"skip", sparkwing.Skipped, ""},
		{"fail", sparkwing.Failed, ""},
	} {
		t.Run(c.onLimit, func(t *testing.T) {
			st := openStore(t)
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			holdSlotOutside(t, st, "global:db")
			res := runAdmitted(t, ctx, st, nodeproto.PlanDoc{Jobs: grouped("", c.onLimit, 1, 30, "b")}, &Admission{Slots: storeSlots{st: st}},
				func(context.Context, *NodeRun) Result {
					return Result{Outcome: sparkwing.Failed, Err: errors.New("ran past a full group")}
				})
			if got := res.Nodes["b"]; got.Outcome != c.want || got.Reason != c.reason {
				t.Fatalf("result %+v, want %s reason %q", got, c.want, c.reason)
			}
			if state, err := teststore.ForTeam(t, st, store.DefaultTeam).GetConcurrencyState(ctx, "global:db"); err != nil || len(state.Waiters) != 0 {
				t.Fatalf("state %+v %v, want no waiter", state, err)
			}
		})
	}
}

type resolveFlaky struct {
	storeSlots
	clock  *fakeClock
	outage time.Duration
}

func (s resolveFlaky) ResolveWaiter(ctx context.Context, key, runID, nodeID, cacheKeyHash, leaderRunID, leaderNodeID string, bypassRead bool) (store.WaiterResolution, error) {
	if s.clock.advance(time.Minute) < s.outage {
		return store.WaiterResolution{}, errors.New("database is locked")
	}
	return s.storeSlots.ResolveWaiter(ctx, key, runID, nodeID, cacheKeyHash, leaderRunID, leaderNodeID, bypassRead)
}

type fakeClock struct {
	mu      sync.Mutex
	elapsed time.Duration
}

func (c *fakeClock) advance(d time.Duration) time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.elapsed += d
	return c.elapsed
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return time.Unix(0, 0).Add(c.elapsed)
}

func TestAdmissionStoreOutageToleranceMatchesTheDispatcher(t *testing.T) {
	for _, c := range []struct {
		outage time.Duration
		want   sparkwing.Outcome
	}{
		{4*time.Minute + 10*time.Second, sparkwing.Success},
		{7 * time.Minute, sparkwing.Failed},
	} {
		t.Run(c.outage.String(), func(t *testing.T) {
			st := openStore(t)
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			clock := &fakeClock{}
			outageClock = clock.now
			t.Cleanup(func() { outageClock = time.Now })
			holdSlotOutside(t, st, "global:db")
			slots := resolveFlaky{storeSlots: storeSlots{st: st, queued: make(chan string, 1)}, clock: clock, outage: c.outage}
			tenant := teststore.ForTeam(t, st, store.DefaultTeam)
			afterAdmissionEvent(t, ctx, slots.queued, func(ctx context.Context) error {
				_, _, _, err := tenant.ReleaseAndNotify(ctx, "global:db", "other/x", "success", "", "", 0, store.DefaultConcurrencyLease)
				return err
			})
			res := runAdmitted(t, ctx, st, nodeproto.PlanDoc{Jobs: grouped("", "queue", 1, 0, "b")}, &Admission{Slots: slots},
				func(context.Context, *NodeRun) Result { return Result{Outcome: sparkwing.Success} })
			if got := res.Nodes["b"]; got.Outcome != c.want {
				t.Fatalf("result %+v, want %s", got, c.want)
			}
			wantSlotsEmpty(t, st, "global:db")
		})
	}
}

func TestAdmissionGroupWithoutAStoreFails(t *testing.T) {
	st := openStore(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	res := runAdmitted(t, ctx, st, nodeproto.PlanDoc{Jobs: grouped("global", "", 1, 0, "a")}, &Admission{Daemon: newFakeDaemon()}, nil)
	if got := res.Nodes["a"]; got.Outcome != sparkwing.Failed || !strings.Contains(errText(got.Err), "no coordination store") {
		t.Fatalf("result %+v", got)
	}
}

func TestAdmissionCancelWhileWaitingForHost(t *testing.T) {
	st := openStore(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	d := newFakeDaemon()
	d.hostGate = make(chan struct{})
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	afterAdmissionEvent(t, ctx, d.queued, func(context.Context) error { cancelRun(); return nil })
	res := runAdmitted(t, runCtx, st, nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "a", Steps: []nodeproto.StepSpec{{ID: "body"}}}}}, &Admission{Daemon: d},
		func(context.Context, *NodeRun) Result { return Result{Outcome: sparkwing.Success} })
	if got := res.Nodes["a"]; got.Outcome != sparkwing.Cancelled {
		t.Fatalf("result %+v, want cancelled", got)
	}
	if live, granted, _ := d.counts(); live != 0 || granted != 0 {
		t.Fatalf("live=%d granted=%d, want no lease", live, granted)
	}
	if row := nodeRow(t, st, "a"); row.StartedAt != nil {
		t.Fatalf("node started while queued: %+v", row)
	}
}

func TestAdmissionDaemonQueueCancelTimeoutAndPolicies(t *testing.T) {
	key := scopedConcurrencyKey("db", "box", testRunID, testBox)
	for _, c := range []struct {
		name, onLimit string
		timeoutMS     int64
		cancel        bool
		want          sparkwing.Outcome
		reason        string
	}{
		{"cancel", "queue", 0, true, sparkwing.Cancelled, ""},
		{"timeout", "queue", 30, false, sparkwing.Failed, store.FailureQueueTimeout},
		{"skip", "skip", 0, false, sparkwing.Skipped, ""},
		{"fail", "fail", 0, false, sparkwing.Failed, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			st := openStore(t)
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			d := newFakeDaemon()
			d.used[key] = 1
			runCtx, cancelRun := context.WithCancel(ctx)
			defer cancelRun()
			if c.cancel {
				afterAdmissionEvent(t, ctx, d.queued, func(context.Context) error { cancelRun(); return nil })
			}
			res := runAdmitted(t, runCtx, st, nodeproto.PlanDoc{Jobs: grouped("box", c.onLimit, 1, c.timeoutMS, "b")}, &Admission{Daemon: d},
				func(context.Context, *NodeRun) Result {
					return Result{Outcome: sparkwing.Failed, Err: errors.New("ran past a full group")}
				})
			if got := res.Nodes["b"]; got.Outcome != c.want || got.Reason != c.reason {
				t.Fatalf("result %+v, want %s reason %q", got, c.want, c.reason)
			}
			if live, granted, _ := d.counts(); live != 0 || granted != 0 {
				t.Fatalf("live=%d granted=%d, want no lease", live, granted)
			}
		})
	}
}
