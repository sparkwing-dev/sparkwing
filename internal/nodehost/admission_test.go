package nodehost

import (
	"context"
	"encoding/base64"
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
	d        *fakeDaemon
	req      wingwire.AdmissionRequest
	once     sync.Once
	done     chan struct{}
	evict    chan wingwire.Evicted
	cancel   chan wingwire.Cancel
	lose     chan error
	probes   chan string
	acks     chan string
	watching sync.WaitGroup
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

func recv(t *testing.T, ctx context.Context, c <-chan string) string {
	t.Helper()
	select {
	case v := <-c:
		return v
	case <-ctx.Done():
		t.Error("timed out waiting")
		return ""
	}
}

func awaitAdmission(t *testing.T, ctx context.Context, event <-chan string, done <-chan RunResult) {
	t.Helper()
	select {
	case <-event:
	case result := <-done:
		t.Fatalf("host returned before admission: %v", result.Err)
	case <-ctx.Done():
		t.Fatal("admission did not make progress:", context.Cause(ctx))
	}
}

func TestAdmissionLeasesHostAroundEachNode(t *testing.T) {
	st := openStore(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	d := newFakeDaemon()
	doc := nodeproto.PlanDoc{Priority: new(int(5)), Jobs: []nodeproto.JobSpec{
		{ID: "ok", Resources: &nodeproto.ResourceSpec{Cores: new(float64(2)), MemoryBytes: new(int64(1 << 30))}, Steps: []nodeproto.StepSpec{{ID: "body"}}},
		{ID: "bad", Steps: []nodeproto.StepSpec{{ID: "body"}}},
	}}
	exec := func(_ context.Context, n *NodeRun) Result {
		if live, _, _ := d.counts(); live < 1 {
			return Result{Outcome: sparkwing.Failed, Err: errors.New("ran without a lease")}
		}
		if n.Plan.ID == "bad" {
			return Result{Outcome: sparkwing.Failed, Err: errors.New("boom")}
		}
		return Result{Outcome: sparkwing.Success}
	}
	res := runAdmitted(t, ctx, st, doc, &Admission{Daemon: d, Class: "batch"}, exec)
	if got := res.Nodes["ok"]; got.Outcome != sparkwing.Success {
		t.Fatalf("ok: %+v", got)
	}
	if got := res.Nodes["bad"]; got.Outcome != sparkwing.Failed || !strings.Contains(errText(got.Err), "boom") {
		t.Fatalf("bad: %+v", got)
	}
	if live, granted, released := d.counts(); live != 0 || granted != 2 || released != 2 {
		t.Fatalf("leases live=%d granted=%d released=%d, want 0/2/2", live, granted, released)
	}
	for _, req := range d.requests() {
		if !strings.HasPrefix(req.RunID, testRunID+"/node-host/") || req.OwnerRunID != testRunID || req.OwnerLeaseToken != "tok" ||
			!req.SubLease || req.SemaphoresOnly || req.Priority != 5 || req.Class != "batch" || req.Pipeline != "p" {
			t.Errorf("request %+v", req)
		}
		if req.DisplayRunID == testRunID+"/ok" && req.Resources != (wingwire.HostResources{Cores: 2, MemoryBytes: 1 << 30}) {
			t.Errorf("ok's request charged %+v, want its declared resources", req.Resources)
		}
	}
}

func TestAdmissionPriorityAndClassPrecedence(t *testing.T) {
	st := openStore(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	d := newFakeDaemon()
	front := 9
	doc := nodeproto.PlanDoc{Priority: new(int(5)), AdmissionClass: new("critical"), Jobs: []nodeproto.JobSpec{{ID: "a", Steps: []nodeproto.StepSpec{{ID: "body"}}}}}
	runAdmitted(t, ctx, st, doc, &Admission{Daemon: d, Priority: &front, Class: "batch"}, nil)
	reqs := d.requests()
	if len(reqs) != 1 || reqs[0].Priority != 9 || reqs[0].Class != "critical" {
		t.Fatalf("requests %+v, want priority 9 from the run and class critical from the plan", reqs)
	}
	want := testRunID + "/node-host/" + base64.RawURLEncoding.EncodeToString([]byte("a"))
	if reqs[0].RunID != want {
		t.Fatalf("request id %q, want %q", reqs[0].RunID, want)
	}
}

func TestAdmissionRunHoldingTheHostTakesOnlySemaphores(t *testing.T) {
	st := openStore(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	d := newFakeDaemon()
	doc := nodeproto.PlanDoc{Resources: &nodeproto.ResourceSpec{Cores: new(float64(4))}, Jobs: append(grouped("run", "", 1, 0, "g"), nodeproto.JobSpec{ID: "plain", Steps: []nodeproto.StepSpec{{ID: "body"}}})}
	res := runAdmitted(t, ctx, st, doc, &Admission{Daemon: d}, nil)
	if res.Status != "failed" || res.Nodes["plain"].Outcome != sparkwing.Failed {
		t.Fatalf("result %+v", res)
	}
	reqs := d.requests()
	if len(reqs) != 1 || !reqs[0].SemaphoresOnly || !strings.HasPrefix(reqs[0].RunID, testRunID+"/node-semaphore/") ||
		len(reqs[0].Semaphores) != 1 || reqs[0].Semaphores[0].Name != scopedConcurrencyKey("db", "run", testRunID, "") {
		t.Fatalf("requests %+v, want one semaphores-only claim for the grouped node", reqs)
	}
}

func TestAdmissionDaemonGroupBoundsParallelNodes(t *testing.T) {
	st := openStore(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	d, g := newFakeDaemon(), newGauge()
	doc := nodeproto.PlanDoc{Jobs: grouped("box", "queue", 2, 0, "a", "b", "c", "d")}
	done := make(chan RunResult, 1)
	go func() { done <- runAdmitted(t, ctx, st, doc, &Admission{Daemon: d}, g.exec) }()
	awaitAdmission(t, ctx, g.entered, done)
	awaitAdmission(t, ctx, g.entered, done)
	awaitAdmission(t, ctx, d.queued, done)
	awaitAdmission(t, ctx, d.queued, done)
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
	wantKey := scopedConcurrencyKey("db", "box", testRunID, testBox)
	for _, req := range d.requests() {
		if len(req.Semaphores) != 1 || req.Semaphores[0].Name != wantKey || req.Semaphores[0].Capacity != 2 {
			t.Errorf("request semaphores %+v, want %q", req.Semaphores, wantKey)
		}
	}
	if live, granted, released := d.counts(); live != 0 || granted != 4 || released != 4 {
		t.Fatalf("leases live=%d granted=%d released=%d", live, granted, released)
	}
}

func TestAdmissionStoreGroupBoundsParallelNodes(t *testing.T) {
	st := openStore(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	slots, g := storeSlots{st: st, queued: make(chan string, 64)}, newGauge()
	doc := nodeproto.PlanDoc{Jobs: grouped("", "queue", 2, 0, "a", "b", "c", "d")}
	done := make(chan RunResult, 1)
	go func() { done <- runAdmitted(t, ctx, st, doc, &Admission{Slots: slots}, g.exec) }()
	awaitAdmission(t, ctx, g.entered, done)
	awaitAdmission(t, ctx, g.entered, done)
	awaitAdmission(t, ctx, slots.queued, done)
	awaitAdmission(t, ctx, slots.queued, done)
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
	go func() { recv(t, ctx, slots.queued); cancelRun() }()
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
				go func() { recv(t, ctx, d.queued); cancelRun() }()
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

func TestAdmissionCancelWhileWaitingForHost(t *testing.T) {
	st := openStore(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	d := newFakeDaemon()
	d.hostGate = make(chan struct{})
	runCtx, cancelRun := context.WithCancel(ctx)
	go func() { recv(t, ctx, d.queued); cancelRun() }()
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

func TestAdmissionHostLeaseIsWatchedWhileTheNodeRuns(t *testing.T) {
	st := openStore(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	d := newFakeDaemon()
	var l *fakeLease
	exec := func(ctx context.Context, _ *NodeRun) Result {
		l = d.only(t)
		select {
		case l.probes <- "n1":
		case <-ctx.Done():
			return Result{Outcome: sparkwing.Failed, Err: errors.New("no watcher took the probe")}
		}
		if got := <-l.acks; got != "n1" {
			return Result{Outcome: sparkwing.Failed, Err: errors.New("wrong ack " + got)}
		}
		return Result{Outcome: sparkwing.Success}
	}
	res := runAdmitted(t, ctx, st, nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "a", Steps: []nodeproto.StepSpec{{ID: "body"}}}}}, &Admission{Daemon: d}, exec)
	if got := res.Nodes["a"]; got.Outcome != sparkwing.Success {
		t.Fatalf("result %+v", got)
	}
	// safety: Wait returns at once only if the watcher already exited after release.
	l.watching.Wait()
	select {
	case l.probes <- "late":
		t.Fatal("a watcher still answers after release")
	default:
	}
}

func TestAdmissionLeaseEndings(t *testing.T) {
	for _, c := range []struct {
		name    string
		grouped bool
		send    func(l *fakeLease)
		want    sparkwing.Outcome
		errHas  string
	}{
		{"evicted", true, func(l *fakeLease) {
			l.evict <- wingwire.Evicted{Key: "db", SupersededBy: "newer", Policy: wingwire.PolicyCancelOthers}
		}, sparkwing.Superseded, "newer"},
		{"daemon cancel", false, func(l *fakeLease) { l.cancel <- wingwire.Cancel{Reason: "operator said stop"} }, sparkwing.Cancelled, "operator said stop"},
		{"rejected after restart", false, func(l *fakeLease) { l.lose <- wingdclient.ErrReattachRejected }, sparkwing.Superseded, "lease lost"},
	} {
		t.Run(c.name, func(t *testing.T) {
			st := openStore(t)
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			d, g := newFakeDaemon(), newGauge()
			go func() {
				recv(t, ctx, g.entered)
				if l := d.only(t); l != nil {
					c.send(l)
				}
			}()
			nodes := []nodeproto.JobSpec{{ID: "a", Steps: []nodeproto.StepSpec{{ID: "body"}}}}
			if c.grouped {
				nodes = grouped("box", "cancel_others", 1, 0, "a")
			}
			res := runAdmitted(t, ctx, st, nodeproto.PlanDoc{Jobs: nodes}, &Admission{Daemon: d}, g.exec)
			if got := res.Nodes["a"]; got.Outcome != c.want || !strings.Contains(errText(got.Err), c.errHas) {
				t.Fatalf("result %+v, want %s with %q", got, c.want, c.errHas)
			}
			if live, _, released := d.counts(); live != 0 || released != 1 {
				t.Fatalf("live=%d released=%d", live, released)
			}
		})
	}
}

// hack: resolveFlaky fails ResolveWaiter while the fake outage clock stays under outage.
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
			go func() {
				recv(t, ctx, slots.queued)
				if _, _, _, err := teststore.ForTeam(t, st, store.DefaultTeam).ReleaseAndNotify(ctx, "global:db", "other/x", "success", "", "", 0, store.DefaultConcurrencyLease); err != nil {
					t.Error(err)
				}
			}()
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
