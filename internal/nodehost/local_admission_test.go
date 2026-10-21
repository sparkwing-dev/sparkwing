package nodehost

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func localAdmissionSeal(t *testing.T, options runspec.Options) AcceptedRun {
	t.Helper()
	spec := runspec.Spec{Hash: "spec", PlanHash: "plan", Request: runspec.SealedRequest{Request: runspec.Request{Pipeline: "p"}}, Resolved: runspec.Resolved{Settings: runspec.ExecutionSettings{Options: options}}}
	raw, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	return AcceptedRun{RunID: testRunID, SpecHash: "spec", PlanHash: "plan", Spec: raw}
}

func localAdmissionRow(id string) store.NodeRow {
	return store.NodeRow{Ref: controllerwire.NodeRef{RunID: testRunID, NodeID: runspec.NodeID(id)}}
}

func localAdmissionOptions() runspec.Options {
	var options runspec.Options
	options.Priority.Set, options.Priority.Value = true, 5
	options.AdmissionClass.Set, options.AdmissionClass.Value = true, "batch"
	return options
}

func localAdmissionContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestAdmissionLeasesHostAroundEachNode(t *testing.T) {
	d := newFakeDaemon()
	admission := LocalAdmission{Daemon: d}
	accepted := localAdmissionSeal(t, localAdmissionOptions())
	errBoom := errors.New("boom")
	ctx := localAdmissionContext(t)
	for _, id := range []string{"ok", "bad"} {
		row := localAdmissionRow(id)
		row.Execution.Resources.Value.Cores.Value = 2
		row.Execution.Resources.Value.MemoryBytes.Value = 1 << 30
		err := admission.node(ctx, accepted, row, func(context.Context) error {
			if live, _, _ := d.counts(); live != 1 {
				t.Error("job ran without its compute lease")
			}
			if id == "bad" {
				return errBoom
			}
			return nil
		})
		if (err != nil) != (id == "bad") || id == "bad" && !errors.Is(err, errBoom) {
			t.Fatalf("job %s: %v", id, err)
		}
	}
	if live, granted, released := d.counts(); live != 0 || granted != 2 || released != 2 {
		t.Fatalf("leases=%d/%d/%d", live, granted, released)
	}
	for _, req := range d.requests() {
		if !strings.HasPrefix(string(req.RunID), testRunID+"/node-host/") || req.OwnerRunID != testRunID || req.OwnerLeaseToken != "lease-"+testRunID || !req.SubLease || req.SemaphoresOnly || req.Priority != 5 || req.Class != "batch" || req.Pipeline != "p" || req.Resources != (wingwire.HostResources{Cores: 2, MemoryBytes: 1 << 30}) {
			t.Fatalf("request=%+v", req)
		}
	}
}

func TestAdmissionPriorityAndClassPrecedence(t *testing.T) {
	d := newFakeDaemon()
	admission := LocalAdmission{Daemon: d}
	options := localAdmissionOptions()
	options.Priority.Value, options.AdmissionClass.Value = 9, "critical"
	accepted := localAdmissionSeal(t, options)
	err := admission.node(localAdmissionContext(t), accepted, localAdmissionRow("a"), func(context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	want := testRunID + "/node-host/" + base64.RawURLEncoding.EncodeToString([]byte("a"))
	for _, req := range d.requests() {
		if req.Priority != 9 || req.Class != "critical" {
			t.Fatalf("sealed winners lost: %+v", req)
		}
	}
	if reqs := d.requests(); len(reqs) != 2 || string(reqs[1].RunID) != want {
		t.Fatalf("requests=%+v", reqs)
	}
}

func TestComputeAdmissionDoesNotReadConcurrency(t *testing.T) {
	d := newFakeDaemon()
	admission := LocalAdmission{Daemon: d}
	options := localAdmissionOptions()
	options.Resources.Value.Cores.Value = 4
	accepted := localAdmissionSeal(t, options)
	row := localAdmissionRow("g")
	row.Execution.Concurrency = []runspec.Concurrency{{Group: "db", Scope: "run", Capacity: 1, Cost: 1, OnLimit: "queue"}}
	err := admission.node(localAdmissionContext(t), accepted, row, func(context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if reqs := d.requests(); len(reqs) != 1 || len(reqs[0].Semaphores) != 0 {
		t.Fatalf("requests=%+v", reqs)
	}
}

func TestLocalAdmissionCancelWhileWaitingForHost(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := newFakeDaemon()
		admission := LocalAdmission{Daemon: d}
		accepted := localAdmissionSeal(t, localAdmissionOptions())
		d.hostGate = make(chan struct{})
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := make(chan error, 1)
		go func() {
			done <- admission.node(ctx, accepted, localAdmissionRow("a"), func(context.Context) error { t.Error("ran while queued"); return nil })
		}()
		synctest.Wait()
		if len(d.queued) != 1 {
			t.Error("job never queued")
		}
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Errorf("queued cancellation=%v", err)
		}
		if live, granted, released := d.counts(); live != 0 || granted != 0 || released != 0 {
			t.Fatalf("leases=%d/%d/%d", live, granted, released)
		}
	})
}

func TestAdmissionHostLeaseIsWatchedWhileTheNodeRuns(t *testing.T) {
	d := newFakeDaemon()
	admission := LocalAdmission{Daemon: d}
	accepted := localAdmissionSeal(t, localAdmissionOptions())
	var held *fakeLease
	err := admission.node(localAdmissionContext(t), accepted, localAdmissionRow("a"), func(ctx context.Context) error {
		d.mu.Lock()
		held = d.leases[string(wingwire.NodeHostParticipant(testRunID, "a"))]
		d.mu.Unlock()
		select {
		case held.probes <- "probe":
		case <-ctx.Done():
			return ctx.Err()
		}
		select {
		case got := <-held.acks:
			if got != "probe" {
				t.Error("watcher changed its probe")
			}
		case <-ctx.Done():
			return context.Cause(ctx)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if live, _, _ := d.counts(); live != 0 {
		t.Fatal("a joined host retained a watcher")
	}
	select {
	case <-held.watchDone:
	default:
		t.Fatal("host returned before its lease watcher joined")
	}
	select {
	case held.probes <- "late":
		t.Fatal("a watcher answers after release")
	default:
	}
}

func TestAdmissionLeaseEndings(t *testing.T) {
	for _, mode := range []string{"evicted", "daemon cancel", "rejected after restart"} {
		t.Run(mode, func(t *testing.T) {
			d := newFakeDaemon()
			admission := LocalAdmission{Daemon: d}
			input, state, deps, processes := executorFixture(t)
			if mode == "evicted" {
				input.Execution.Concurrency = []runspec.Concurrency{{Group: "db", Scope: "box", Capacity: 1, Cost: 1, OnLimit: "cancel_others"}}
				state.row.Execution = input.Execution
				var spec runspec.Spec
				if err := json.Unmarshal(input.Accepted.Spec, &spec); err != nil {
					t.Fatal(err)
				}
				spec.Resolved.Settings.Jobs[input.NodeID] = input.Execution
				input.Accepted.Spec, _ = json.Marshal(spec)
			}
			start := deps.Start
			deps.Start = func(ctx context.Context, in NodeInput, activity ExecutorActivity) (ExecutorProcess, error) {
				process, err := start(ctx, in, activity)
				if err != nil {
					return process, err
				}
				process.(*executorProcess).onRun = func(ctx context.Context) {
					d.mu.Lock()
					held := d.leases[string(wingwire.NodeHostParticipant(in.Accepted.RunID, in.NodeID))]
					d.mu.Unlock()
					switch mode {
					case "evicted":
						held.evict <- wingwire.Evicted{Key: "db", SupersededBy: "newer"}
					case "daemon cancel":
						held.cancel <- wingwire.Cancel{Reason: "operator said stop"}
					default:
						held.lose <- wingdclient.ErrReattachRejected
					}
					<-ctx.Done()
				}
				return process, nil
			}
			executor, err := NewNodeExecutor(deps)
			if err != nil {
				t.Fatal(err)
			}
			err = admission.node(localAdmissionContext(t), input.Accepted, state.row, func(ctx context.Context) error {
				return executor.Execute(ctx, input)
			})
			if err == nil || mode == "evicted" && !strings.Contains(err.Error(), "newer") || mode == "daemon cancel" && !strings.Contains(err.Error(), "operator said stop") || mode == "rejected after restart" && !strings.Contains(err.Error(), "lease lost") {
				t.Fatalf("ending=%s error=%v", mode, err)
			}
			want := store.NodeSuperseded
			if mode == "daemon cancel" {
				want = store.NodeCancelled
			}
			if len(state.changes) != 2 || state.changes[0].To != store.NodeRunning || state.changes[1].To != want || state.changes[1].Cause != store.CauseReport {
				t.Fatalf("ending=%s changes=%+v", mode, state.changes)
			}
			for _, process := range *processes {
				if process.closed != 1 || process.cleaned != 1 {
					t.Fatal("host returned before process cleanup")
				}
			}
			if live, granted, released := d.counts(); live != 0 || granted != 1 || released != 1 {
				t.Fatalf("leases=%d/%d/%d", live, granted, released)
			}
		})
	}
}

func TestComputeAdmissionRequiresAcceptedJobBinding(t *testing.T) {
	for _, mode := range []string{"missing seal", "changed seal", "other run", "missing job"} {
		t.Run(mode, func(t *testing.T) {
			daemon := newFakeDaemon()
			admission := LocalAdmission{Daemon: daemon}
			accepted := localAdmissionSeal(t, localAdmissionOptions())
			row := localAdmissionRow("job")
			switch mode {
			case "missing seal":
				accepted.Spec = nil
			case "changed seal":
				accepted.SpecHash = "changed"
			case "other run":
				row.Ref.RunID = "other"
			case "missing job":
				row.Ref.NodeID = ""
			}
			if err := admission.node(t.Context(), accepted, row, func(context.Context) error {
				t.Error("invalid binding reached execution")
				return nil
			}); err == nil {
				t.Fatal("invalid binding admitted")
			}
			if len(daemon.requests()) != 0 {
				t.Fatal("invalid binding acquired machine capacity")
			}
		})
	}
}
