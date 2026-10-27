package controller

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

type engineRecoveryOperator struct {
	store.OperatorStore
	pages   [][]store.ControllerRun
	cursors []string
	pageErr error
}

func (f *engineRecoveryOperator) ReconcileControllerRuns(_ context.Context, after string, limit int) ([]store.ControllerRun, error) {
	if limit != controllerRecoveryBatch {
		return nil, fmt.Errorf("incorrect recovery batch")
	}
	f.cursors = append(f.cursors, after)
	if len(f.pages) == 0 {
		return nil, f.pageErr
	}
	page := f.pages[0]
	f.pages = f.pages[1:]
	return page, f.pageErr
}

func TestEngineRecoveryPagesThroughControllerRuns(t *testing.T) {
	page := make([]store.ControllerRun, controllerRecoveryBatch)
	page[len(page)-1] = store.ControllerRun{Team: "a", RunID: "last"}
	fake := &engineRecoveryOperator{pages: [][]store.ControllerRun{page, {{Team: "b", RunID: "tail"}}}}
	count, err := reconcileControllerRuns(t.Context(), fake)
	if err != nil || count != controllerRecoveryBatch+1 || len(fake.cursors) != 2 || fake.cursors[0] != "" || fake.cursors[1] != "last" {
		t.Fatalf("count=%d cursors=%v err=%v", count, fake.cursors, err)
	}
}

func TestEngineRecoveryStopsAfterPageFailures(t *testing.T) {
	failure := errors.New("store unavailable")
	for _, problem := range []string{"page", "cancelled"} {
		t.Run(problem, func(t *testing.T) {
			fake := &engineRecoveryOperator{}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			wantCalls, wantCount := 0, 0
			var wantErr error
			switch problem {
			case "page":
				fake.pages, fake.pageErr = [][]store.ControllerRun{{{Team: "a", RunID: "committed"}}}, failure
				wantCount, wantCalls, wantErr = 1, 1, failure
			case "cancelled":
				cancel()
				wantErr = context.Canceled
			}
			count, err := reconcileControllerRuns(ctx, fake)
			if !errors.Is(err, wantErr) || count != wantCount || len(fake.cursors) != wantCalls {
				t.Fatalf("count=%d calls=%d err=%v, want %d/%d/%v", count, len(fake.cursors), err, wantCount, wantCalls, wantErr)
			}
		})
	}
}

func TestEngineRecoveryLoopSettlesPersistedControllerRuns(t *testing.T) {
	engineServer, tenant := engineRuntimeStore(t)
	s := engineServer.Store
	engineSQL(t, s, `UPDATE runs SET dispatch='controller',sealed_at=1,spec_json=?,spec_hash='spec',plan_json=?,plan_hash='plan',plan_accepted_generation=1 WHERE id='run'`, []byte(`{}`), []byte(`{"jobs":[]}`))
	server := New(s, slog.New(slog.NewTextHandler(io.Discard, nil)))
	for range 2 {
		server.reconcileOwnedRuns(t.Context())
	}
	run, err := tenant.ReadRun(t.Context(), "run")
	if err != nil || run.State != runstate.RunSuccess || run.StateSeq != 1 || run.FinishedAt == nil {
		t.Fatalf("resumed state=%s sequence=%d finished=%v err=%v", run.State, run.StateSeq, run.FinishedAt, err)
	}
	events, err := tenant.ReadStateEvents(t.Context(), "run", 0, 10)
	if err != nil || len(events) != 1 || events[0].Kind != "run_success" {
		t.Fatalf("completion events=%+v err=%v", events, err)
	}
}
