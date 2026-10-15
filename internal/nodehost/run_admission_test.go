package nodehost

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func hostPlanned(t *testing.T, ctx context.Context, st *store.Store, runID string, doc nodeproto.PlanDoc, adm *Admission, exec Executor) (RunResult, func()) {
	t.Helper()
	if err := st.CreateRun(ctx, store.Run{ID: runID, Pipeline: "p", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	cfg := Config{Execute: exec, Admission: adm}
	run := &Run{ID: runID, Doc: doc, State: st, Logger: slog.New(slog.DiscardHandler), adds: make(chan addRequest), validation: nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: noV2Secrets{}}}
	res := RunResult{RunID: runID, Nodes: map[string]Result{}}
	release := runPlanned(ctx, &cfg, run, &res)
	return res, release
}

// safety: the store keeps a released plan-level holder that once queued as a
// superseded tombstone, so only a live hold or a waiter counts as unreleased.
func wantNoLiveHold(t *testing.T, st *store.Store, key string) {
	t.Helper()
	state, err := st.GetConcurrencyState(t.Context(), key)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Waiters) != 0 || slices.ContainsFunc(state.Holders, func(h store.ConcurrencyHolder) bool { return !h.Superseded }) {
		t.Fatalf("key %s holders %+v waiters %+v, want no live hold", key, state.Holders, state.Waiters)
	}
}

func (d *fakeDaemon) lease(runID string) *fakeLease {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.leases[runID]
}

func planGrouped(key, onLimit string, nodes ...string) nodeproto.PlanDoc {
	parts := strings.Split(key, ":")
	concurrency := nodeproto.ConcurrencySpec{Group: parts[len(parts)-1], Scope: new(parts[0]), Capacity: new(1)}
	if onLimit != "" {
		concurrency.OnLimit = new(onLimit)
	}
	doc := nodeproto.PlanDoc{Concurrency: []nodeproto.ConcurrencySpec{concurrency}}
	for _, id := range nodes {
		doc.Jobs = append(doc.Jobs, nodeproto.JobSpec{ID: id, Steps: []nodeproto.StepSpec{{ID: "body"}}})
	}
	return doc
}

var boxKey = scopedConcurrencyKey("deploys", "box", "", testBox)

func TestPlanGroupBoundsTwoRuns(t *testing.T) {
	for _, c := range []struct {
		name, key string
		daemon    bool
	}{
		{"store", "global:deploys", false},
		{"daemon", boxKey, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			st := openStore(t)
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			d := newFakeDaemon()
			slots := storeSlots{st: st, queued: make(chan string, 4)}
			adm := &Admission{Slots: slots}
			if c.daemon {
				adm.Daemon = d
			}
			queued := slots.queued
			if c.daemon {
				queued = d.queued
			}
			g := newGauge()
			doc := planGrouped(c.key, "queue", "a")
			first := make(chan RunResult, 1)
			go func() {
				res, release := hostPlanned(t, ctx, st, "run-1", doc, adm, g.exec)
				release()
				first <- res
			}()
			recv(t, ctx, g.entered)
			second := make(chan RunResult, 1)
			go func() {
				res, release := hostPlanned(t, ctx, st, "run-2", doc, adm, g.exec)
				release()
				second <- res
			}()
			recv(t, ctx, queued)
			g.gate <- struct{}{}
			if res := <-first; res.Status != "success" {
				t.Fatalf("first run %+v", res)
			}
			recv(t, ctx, g.entered)
			close(g.gate)
			if res := <-second; res.Status != "success" {
				t.Fatalf("second run %+v", res)
			}
			if g.peak != 1 {
				t.Fatalf("peak %d nodes, want the group to hold one run at a time", g.peak)
			}
			if c.daemon {
				if live, granted, released := d.counts(); live != 0 || granted != 4 || released != 4 {
					t.Fatalf("leases live=%d granted=%d released=%d, want each run's lease and node lease released", live, granted, released)
				}
			} else {
				wantNoLiveHold(t, st, c.key)
			}
		})
	}
}

func TestPlanGroupLimitPoliciesAndEviction(t *testing.T) {
	for _, c := range []struct {
		name, onLimit string
		daemon        bool
		evict         func(t *testing.T, st *store.Store, d *fakeDaemon)
		status, err   string
		node          sparkwing.Outcome
	}{
		{name: "store skip", onLimit: "skip", status: "success", node: sparkwing.Skipped},
		{name: "store fail", onLimit: "fail", status: "failed", err: "slot full under OnLimit:Fail", node: sparkwing.Cancelled},
		{name: "daemon skip", onLimit: "skip", daemon: true, status: "success", node: sparkwing.Skipped},
		{name: "daemon fail", onLimit: "fail", daemon: true, status: "failed", err: "slot full under OnLimit:Fail", node: sparkwing.Cancelled},
		{
			name: "store evict", onLimit: "cancel_others", status: "cancelled", err: "superseded", node: sparkwing.Cancelled,
			evict: func(t *testing.T, st *store.Store, _ *fakeDaemon) {
				resp, err := st.AcquireConcurrencySlot(t.Context(), store.AcquireSlotRequest{Key: "global:deploys", HolderID: "newer/-", RunID: "newer", Capacity: 1, Policy: "cancel_others"})
				if err != nil || resp.Kind != store.AcquireCancellingOthers {
					t.Errorf("newer arrival: %+v %v", resp, err)
				}
			},
		},
		{
			name: "daemon evict", onLimit: "cancel_others", daemon: true, status: "cancelled", err: "superseded by run newer", node: sparkwing.Cancelled,
			evict: func(t *testing.T, _ *store.Store, d *fakeDaemon) {
				d.lease(testRunID).evict <- wingwire.Evicted{Key: boxKey, SupersededBy: "newer", Policy: wingwire.PolicyCancelOthers}
			},
		},
		{
			name: "daemon cancel", onLimit: "queue", daemon: true, status: "cancelled", err: "operator said stop", node: sparkwing.Cancelled,
			evict: func(t *testing.T, _ *store.Store, d *fakeDaemon) {
				d.lease(testRunID).cancel <- wingwire.Cancel{Reason: "operator said stop"}
			},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			st := openStore(t)
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			d := newFakeDaemon()
			adm := &Admission{Slots: storeSlots{st: st}}
			key := "global:deploys"
			if c.daemon {
				adm.Daemon, key = d, boxKey
			}
			g := newGauge()
			if c.evict == nil {
				if c.daemon {
					d.used[key] = 1
				} else {
					holdSlotOutside(t, st, key)
				}
				close(g.gate)
			} else {
				go func() {
					recv(t, ctx, g.entered)
					c.evict(t, st, d)
				}()
			}
			res, release := hostPlanned(t, ctx, st, testRunID, planGrouped(key, c.onLimit, "a", "b"), adm, g.exec)
			release()
			if res.Status != c.status || (c.err == "") != (res.Err == nil) || !strings.Contains(errText(res.Err), c.err) {
				t.Fatalf("run %s %v, want %s with %q", res.Status, res.Err, c.status, c.err)
			}
			var ce *CancelError
			if c.status == "cancelled" && !errors.As(res.Err, &ce) {
				t.Fatalf("err %T %v, want a CancelError", res.Err, res.Err)
			}
			if c.evict == nil && g.peak != 0 {
				t.Fatal("a node ran though the run was not admitted")
			}
			for _, id := range []string{"a", "b"} {
				if row := nodeRow(t, st, id); row.Status != "done" || (c.evict == nil && row.Outcome != string(c.node)) {
					t.Errorf("node %s row %s/%s, want done/%s", id, row.Status, row.Outcome, c.node)
				}
			}
			if c.daemon {
				if live, _, _ := d.counts(); live != 0 {
					t.Fatalf("%d leases live after release", live)
				}
			} else if c.evict != nil {
				state, err := st.GetConcurrencyState(t.Context(), key)
				if err != nil || slices.ContainsFunc(state.Holders, func(h store.ConcurrencyHolder) bool { return h.RunID == testRunID }) {
					t.Fatalf("state %+v %v, want the evicted run's hold released", state, err)
				}
			}
		})
	}
}

func TestPlanGroupReleasedOnEveryExit(t *testing.T) {
	for _, c := range []struct {
		name   string
		exec   Executor
		cancel bool
		status string
	}{
		{"success", func(context.Context, *NodeRun) Result { return Result{Outcome: sparkwing.Success} }, false, "success"},
		{"node failure", func(context.Context, *NodeRun) Result {
			return Result{Outcome: sparkwing.Failed, Err: errors.New("boom")}
		}, false, "failed"},
		{"interrupt", nil, true, "cancelled"},
	} {
		t.Run(c.name, func(t *testing.T) {
			st := openStore(t)
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			d := newFakeDaemon()
			adm := &Admission{Daemon: d, Slots: storeSlots{st: st}}
			doc := planGrouped("global:deploys", "queue", "a")
			doc.Concurrency = append(doc.Concurrency, nodeproto.ConcurrencySpec{Group: boxKey, Capacity: new(int(1))})
			exec := c.exec
			runCtx, cancelRun := context.WithCancelCause(ctx)
			defer cancelRun(nil)
			if c.cancel {
				g := newGauge()
				exec = g.exec
				go func() { recv(t, ctx, g.entered); cancelRun(&CancelError{Reason: "interrupted by SIGINT"}) }()
			}
			res, release := hostPlanned(t, runCtx, st, testRunID, doc, adm, exec)
			if res.Status != c.status {
				t.Fatalf("run %s %v, want %s", res.Status, res.Err, c.status)
			}
			wantNoLiveHold(t, st, "global:deploys")
			if live, _, _ := d.counts(); live != 1 || d.lease(testRunID) == nil {
				t.Fatalf("%d leases live before release, want the run lease held until the row is terminal", live)
			}
			release()
			if live, _, _ := d.counts(); live != 0 {
				t.Fatalf("%d leases live after release", live)
			}
		})
	}
}

func TestRunLeaseIsTheNodeLeasesOwner(t *testing.T) {
	st := openStore(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	d := newFakeDaemon()
	front := 7
	doc := nodeproto.PlanDoc{
		AdmissionClass: new("critical"), Jobs: []nodeproto.JobSpec{{ID: "a", Steps: []nodeproto.StepSpec{{ID: "body"}}}},
		Concurrency: []nodeproto.ConcurrencySpec{{Group: boxKey, Capacity: new(int(2)), Cost: new(int(1)), OnLimit: new(string("queue")), QueueTimeoutMS: new(int64(5000))}},
	}
	res, release := hostPlanned(t, ctx, st, testRunID, doc, &Admission{Daemon: d, Priority: &front}, nil)
	release()
	reqs := d.requests()
	if len(reqs) != 2 {
		t.Fatalf("requests %+v, want the run lease then a's", reqs)
	}
	runReq, nodeReq := reqs[0], reqs[1]
	want := wingwire.SemaphoreClaim{Name: boxKey, Cost: 1, Capacity: 2, Policy: wingwire.PolicyQueue, QueueTimeoutMS: 5000}
	if runReq.RunID != testRunID || runReq.SubLease || !runReq.SemaphoresOnly || runReq.Priority != 7 || runReq.Class != "critical" ||
		len(runReq.Semaphores) != 1 || runReq.Semaphores[0] != want {
		t.Fatalf("run lease request %+v", runReq)
	}
	if nodeReq.OwnerRunID != testRunID || nodeReq.OwnerLeaseToken != "lease-"+testRunID {
		t.Fatalf("node request %+v, want it owned by the run lease", nodeReq)
	}
	if res.Status != "failed" {
		t.Fatalf("run %s %v, want a failed for want of an executor", res.Status, res.Err)
	}
}

func TestRunLeaseChargesThePlanResources(t *testing.T) {
	st := openStore(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	d := newFakeDaemon()
	doc := nodeproto.PlanDoc{Resources: &nodeproto.ResourceSpec{Cores: new(float64(4)), MemoryBytes: new(int64(1 << 30))}, Jobs: []nodeproto.JobSpec{{ID: "a", Steps: []nodeproto.StepSpec{{ID: "body"}}}}}
	_, release := hostPlanned(t, ctx, st, testRunID, doc, &Admission{Daemon: d}, func(context.Context, *NodeRun) Result { return Result{Outcome: sparkwing.Success} })
	release()
	reqs := d.requests()
	if len(reqs) != 1 || reqs[0].SemaphoresOnly || reqs[0].Resources != (wingwire.HostResources{Cores: 4, MemoryBytes: 1 << 30}) {
		t.Fatalf("requests %+v, want only the run lease, charged the plan's resources", reqs)
	}
}

func TestPlanGroupWithoutAStoreFailsTheRun(t *testing.T) {
	st := openStore(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	res, release := hostPlanned(t, ctx, st, testRunID, planGrouped("global:deploys", "queue", "a"), &Admission{}, nil)
	release()
	if res.Status != "failed" || !strings.Contains(errText(res.Err), "no coordination store") {
		t.Fatalf("run %s %v", res.Status, res.Err)
	}
	wantRow(t, st, "a", sparkwing.Cancelled, "run not admitted")
}
