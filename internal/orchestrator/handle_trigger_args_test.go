package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/logs"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// safety: keyed by run ID, so a repeated test run waits on its own renewal.
var renewedRuns sync.Map

type awaitRenewalPipe struct{ sparkwing.Base }

func (awaitRenewalPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	renewed, _ := renewedRuns.Load(rc.RunID)
	sparkwing.Job(plan, rc.Pipeline, func(ctx context.Context) error {
		ch, ok := renewed.(chan struct{})
		if !ok {
			return errors.New("no renewal channel for this run")
		}
		select {
		case <-ch:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	return nil
}

var registerAwaitRenewalOnce sync.Once

func TestHandleTriggerRenewsTheClaimWithTheWorkersArgv(t *testing.T) {
	registerAwaitRenewalOnce.Do(func() {
		sparkwing.Register[sparkwing.NoInputs]("await-renewal",
			func() sparkwing.Pipeline[sparkwing.NoInputs] { return &awaitRenewalPipe{} })
	})
	cases := map[string]func(id, ctrl string) []string{
		"cluster worker argv": func(id, ctrl string) []string {
			return HandleTriggerArgs(id, ctrl, ctrl, 100*time.Millisecond)
		},
		"older worker, ID first": func(id, ctrl string) []string {
			return []string{id, "--controller", ctrl, "--heartbeat", "100ms", "--logs", ctrl}
		},
	}
	n := 0
	for name, argv := range cases {
		t.Run(name, func(t *testing.T) {
			n++
			t.Setenv("SPARKWING_HOME", t.TempDir())
			st, err := teststore.Open(filepath.Join(t.TempDir(), "controller.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Close() })
			logsSrv, err := logs.New(t.TempDir(), nil)
			if err != nil {
				t.Fatal(err)
			}
			mux := http.NewServeMux()
			mux.Handle("/api/v1/logs/", logsSrv.Handler())
			mux.Handle("/", controller.New(st, slog.New(slog.NewTextHandler(io.Discard, nil))).Handler())
			id := fmt.Sprintf("renewal-%d-%d", time.Now().UnixNano(), n)
			renewed := make(chan struct{})
			renewedRuns.Store(id, renewed)
			t.Cleanup(func() { renewedRuns.Delete(id) })
			var beats atomic.Int64
			var once sync.Once
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost && r.URL.Path == "/api/v1/triggers/"+id+"/heartbeat" {
					beats.Add(1)
					once.Do(func() { close(renewed) })
				}
				mux.ServeHTTP(w, r)
			}))
			t.Cleanup(srv.Close)
			if err := st.CreateTrigger(t.Context(), store.Trigger{ID: id, Pipeline: "await-renewal", CreatedAt: time.Now()}); err != nil {
				t.Fatal(err)
			}
			if trig, err := st.ClaimNextTrigger(t.Context(), 0); err != nil || trig == nil || trig.ID != id {
				t.Fatalf("claim: %+v %v", trig, err)
			}

			if err := runHandleTriggerCLI(argv(id, srv.URL)); err != nil {
				t.Fatalf("handle-trigger: %v", err)
			}
			if beats.Load() == 0 {
				t.Fatal("handle-trigger never renewed the claim")
			}
			run, err := st.GetRun(t.Context(), id)
			if err != nil || run.Status != "success" {
				t.Fatalf("run = %+v, %v; want success once the claim was renewed", run, err)
			}
		})
	}
}
