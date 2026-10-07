package orchestrator_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

var heartbeatSeen = make(chan struct{})

type awaitHeartbeatPipe struct{ sparkwing.Base }

func (awaitHeartbeatPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	sparkwing.Job(plan, rc.Pipeline, func(ctx context.Context) error {
		select {
		case <-heartbeatSeen:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	return nil
}

var registerAwaitHeartbeatOnce sync.Once

func TestExecuteClaimedTriggerHeartbeatsWhenAsked(t *testing.T) {
	registerAwaitHeartbeatOnce.Do(func() {
		sparkwing.Register[sparkwing.NoInputs]("await-heartbeat",
			func() sparkwing.Pipeline[sparkwing.NoInputs] { return &awaitHeartbeatPipe{} })
	})
	st, err := teststore.Open(filepath.Join(t.TempDir(), "controller.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctrl := controller.New(st, slog.New(slog.NewTextHandler(io.Discard, nil))).Handler()
	var beats atomic.Int64
	var once sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/heartbeat") && strings.HasPrefix(r.URL.Path, "/api/v1/triggers/") {
			beats.Add(1)
			once.Do(func() { close(heartbeatSeen) })
		}
		ctrl.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	cli := client.New(srv.URL, nil)
	if err := st.CreateTrigger(t.Context(), store.Trigger{ID: "await-heartbeat-run", Pipeline: "await-heartbeat", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	trig, err := st.ClaimNextTrigger(t.Context(), 0)
	if err != nil || trig == nil {
		t.Fatalf("claim trigger: %v, %+v", err, trig)
	}

	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	orchestrator.ExecuteClaimedTrigger(ctx, orchestrator.WorkerOptions{
		Logger:            slog.New(slog.NewTextHandler(io.Discard, nil)),
		HeartbeatInterval: 100 * time.Millisecond,
	}, orchestrator.RemoteBackends(cli, testLogBackend(t), nil, nil, 0), cli, trig)

	if beats.Load() == 0 {
		t.Fatal("a handle-trigger given --heartbeat never heartbeat its claim")
	}
	run, err := cli.GetRun(t.Context(), trig.ID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "success" {
		t.Fatalf("run status = %q (%s), want success once a heartbeat arrived", run.Status, run.Error)
	}
}
