package controller

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type engineRecoveryOperator struct {
	store.OperatorStore
	lease      store.SingletonLease
	pages      [][]store.ControllerRun
	cursors    []string
	renewals   int
	acquireErr error
	renewErr   error
	pageErr    error
}

func (f *engineRecoveryOperator) AcquireSingletonLease(_ context.Context, name, holder string, ttl time.Duration) (store.SingletonLease, error) {
	if name != "controller.owner" || holder != "replica" || ttl <= 0 {
		return store.SingletonLease{}, fmt.Errorf("incorrect election")
	}
	return f.lease, f.acquireErr
}

func (f *engineRecoveryOperator) RenewSingletonLease(_ context.Context, lease store.SingletonLease, ttl time.Duration) (store.SingletonLease, error) {
	if lease != f.lease || ttl <= 0 {
		return store.SingletonLease{}, fmt.Errorf("incorrect renewal")
	}
	f.renewals++
	return lease, f.renewErr
}

func (f *engineRecoveryOperator) ReconcileControllerRuns(_ context.Context, lease store.SingletonLease, after string, limit int) ([]store.ControllerRun, error) {
	if lease != f.lease || limit != controllerRecoveryBatch {
		return nil, fmt.Errorf("incorrect recovery fence or batch")
	}
	f.cursors = append(f.cursors, after)
	if len(f.pages) == 0 {
		return nil, f.pageErr
	}
	page := f.pages[0]
	f.pages = f.pages[1:]
	return page, f.pageErr
}

func TestEngineRecoveryPagesAndRenewsTheControllerFence(t *testing.T) {
	page := make([]store.ControllerRun, controllerRecoveryBatch)
	page[len(page)-1] = store.ControllerRun{Team: "a", RunID: "last"}
	fake := &engineRecoveryOperator{lease: store.SingletonLease{Name: "controller.owner", Holder: "replica", Generation: 7}, pages: [][]store.ControllerRun{page, {{Team: "b", RunID: "tail"}}}}
	count, err := reconcileControllerRuns(t.Context(), fake, "replica")
	if err != nil || count != controllerRecoveryBatch+1 || len(fake.cursors) != 2 || fake.cursors[0] != "" || fake.cursors[1] != "last" || fake.renewals != 1 {
		t.Fatalf("count=%d cursors=%v renewals=%d err=%v", count, fake.cursors, fake.renewals, err)
	}
}

func TestEngineRecoveryStopsAfterElectionRenewalAndPageFailures(t *testing.T) {
	failure := errors.New("store unavailable")
	for _, problem := range []string{"foreign", "election", "renewal", "page", "cancelled"} {
		t.Run(problem, func(t *testing.T) {
			fake := &engineRecoveryOperator{lease: store.SingletonLease{Name: "controller.owner", Holder: "replica", Generation: 7}}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			wantCalls, wantCount := 0, 0
			var wantErr error
			switch problem {
			case "foreign":
				fake.acquireErr = store.ErrLockHeld
			case "election":
				fake.acquireErr, wantErr = failure, failure
			case "renewal":
				page := make([]store.ControllerRun, controllerRecoveryBatch)
				page[len(page)-1].RunID = "last"
				fake.pages, fake.renewErr = [][]store.ControllerRun{page}, store.ErrLockHeld
				wantCount, wantCalls, wantErr = controllerRecoveryBatch, 1, store.ErrLockHeld
			case "page":
				fake.pages, fake.pageErr = [][]store.ControllerRun{{{Team: "a", RunID: "committed"}}}, failure
				wantCount, wantCalls, wantErr = 1, 1, failure
			case "cancelled":
				cancel()
				wantErr = context.Canceled
			}
			count, err := reconcileControllerRuns(ctx, fake, "replica")
			if !errors.Is(err, wantErr) || count != wantCount || len(fake.cursors) != wantCalls {
				t.Fatalf("count=%d calls=%d err=%v, want %d/%d/%v", count, len(fake.cursors), err, wantCount, wantCalls, wantErr)
			}
		})
	}
}

func TestEngineRecoveryCannotContinueAfterLosingItsFence(t *testing.T) {
	property := func(generation uint32, holder string, acquired bool) bool {
		page := make([]store.ControllerRun, controllerRecoveryBatch)
		page[len(page)-1].RunID = "after-" + holder
		fake := &engineRecoveryOperator{lease: store.SingletonLease{Name: "controller.owner", Holder: holder, Generation: int64(generation) + 1}, pages: [][]store.ControllerRun{page}}
		if acquired {
			fake.renewErr = store.ErrLockHeld
		} else {
			fake.acquireErr = store.ErrLockHeld
		}
		count, err := reconcileControllerRuns(t.Context(), fake, "replica")
		if acquired {
			return errors.Is(err, store.ErrLockHeld) && count == controllerRecoveryBatch && len(fake.cursors) == 1 && fake.cursors[0] == ""
		}
		return err == nil && count == 0 && len(fake.cursors) == 0 && fake.renewals == 0
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 128}); err != nil {
		t.Fatal(err)
	}
}

func TestEngineRecoveryLoopSettlesPersistedControllerRuns(t *testing.T) {
	engineServer, tenant := engineRuntimeStore(t)
	s := engineServer.Store
	engineSQL(t, s, `UPDATE runs SET dispatch='controller',sealed_at=1,spec_json=?,spec_hash='spec',plan_json=?,plan_hash='plan',plan_accepted_generation=1 WHERE id='run'`, []byte(`{}`), []byte(`{"jobs":[]}`))
	server := New(s, slog.New(slog.NewTextHandler(io.Discard, nil)))
	foreign, err := s.AsOperator().AcquireSingletonLease(t.Context(), "controller.owner", "other-replica", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	server.reconcileOwnedRuns(t.Context())
	run, err := tenant.ReadRun(t.Context(), "run")
	if err != nil || run.State != store.RunRunning || run.StateSeq != 0 {
		t.Fatalf("foreign controller changed state=%s sequence=%d err=%v", run.State, run.StateSeq, err)
	}
	engineSQL(t, s, `UPDATE leases SET holder=? WHERE name='controller.owner' AND generation=?`, server.measureHolder(), foreign.Generation)
	for range 2 {
		server.reconcileOwnedRuns(t.Context())
	}
	run, err = tenant.ReadRun(t.Context(), "run")
	if err != nil || run.State != store.RunSuccess || run.StateSeq != 1 || run.FinishedAt == nil {
		t.Fatalf("resumed state=%s sequence=%d finished=%v err=%v", run.State, run.StateSeq, run.FinishedAt, err)
	}
	events, err := tenant.ReadStateEvents(t.Context(), "run", 0, 10)
	if err != nil || len(events) != 1 || events[0].Kind != "run_success" {
		t.Fatalf("completion events=%+v err=%v", events, err)
	}
}

func TestEngineRecoverySeparatesControllerInstancesOnOneMachine(t *testing.T) {
	engineServer, tenant := engineRuntimeStore(t)
	s := engineServer.Store
	engineSQL(t, s, `UPDATE runs SET dispatch='controller',sealed_at=1,spec_json=?,spec_hash='spec',plan_json=?,plan_hash='plan',plan_accepted_generation=1 WHERE id='run'`, []byte(`{}`), []byte(`{"jobs":[]}`))
	first, second := New(s, nil), New(s, nil)
	if _, err := s.AsOperator().AcquireSingletonLease(t.Context(), "controller.owner", first.measureHolder(), time.Minute); err != nil {
		t.Fatal(err)
	}
	second.reconcileOwnedRuns(t.Context())
	run, err := tenant.ReadRun(t.Context(), "run")
	if err != nil || run.State != store.RunRunning || run.StateSeq != 0 {
		t.Fatalf("another instance used the owner's fence: state=%s sequence=%d err=%v", run.State, run.StateSeq, err)
	}
	first.reconcileOwnedRuns(t.Context())
	run, err = tenant.ReadRun(t.Context(), "run")
	if err != nil || run.State != store.RunSuccess {
		t.Fatalf("elected instance did not settle: state=%s err=%v", run.State, err)
	}
}

func TestEngineRecoveryControllerIdentityIsInstanceScoped(t *testing.T) {
	property := func(size uint8) bool {
		holders := map[string]bool{}
		for range int(size)%16 + 2 {
			server := New(nil, nil)
			holder := server.measureHolder()
			if holder == "" || holders[holder] {
				return false
			}
			holders[holder] = true
		}
		return true
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 128}); err != nil {
		t.Fatal(err)
	}
}
